package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const version = "0.1.2"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if len(os.Args) < 2 {
		runServe(os.Args[1:])
		return
	}

	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "scan":
		runScan(os.Args[2:])
	case "version", "--version", "-version":
		fmt.Printf("psoc %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	case "help", "-h", "--help":
		printHelp()
	default:
		// Preserve a friendly default: `psoc --listen ...` means serve.
		if strings.HasPrefix(os.Args[1], "-") {
			runServe(os.Args[1:])
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		printHelp()
		os.Exit(2)
	}
}

func printHelp() {
	fmt.Print(`psoc - bounded public HTTP/SOCKS proxy checker with a dashboard

Usage:
  psoc serve [flags]   Run dashboard/API server (default command)
  psoc scan  [flags]   Run a one-shot scan from a file or stdin
  psoc version         Print version

Target formats:
  203.0.113.10:8080
  http://203.0.113.10:8080
  socks5://203.0.113.10:1080
  socks4://203.0.113.10:1080

Bare host:port targets are checked as http, socks5, then socks4.
Private, loopback, link-local, multicast, and unspecified IPs are blocked by default.
`)
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", envString("PSOC_LISTEN", "127.0.0.1:8080"), "listen address")
	dataFile := fs.String("data", envString("PSOC_DATA", "./data/results.json"), "results JSON file")
	concurrency := fs.Int("concurrency", envInt("PSOC_CONCURRENCY", 64), "max concurrent checks")
	timeout := fs.Duration("timeout", envDuration("PSOC_TIMEOUT", 8*time.Second), "per-proxy timeout")
	verifyURL := fs.String("verify-url", envString("PSOC_VERIFY_URL", "https://example.com/"), "URL fetched through each proxy")
	maxTargets := fs.Int("max-targets", envInt("PSOC_MAX_TARGETS", 5000), "maximum targets per scan")
	allowPrivate := fs.Bool("allow-private", envBool("PSOC_ALLOW_PRIVATE", false), "allow private/local target IPs")
	apiToken := fs.String("api-token", os.Getenv("PSOC_API_TOKEN"), "optional bearer token for scan API")
	initialFile := fs.String("targets", envString("PSOC_TARGETS_FILE", ""), "optional target file scanned at startup")
	_ = fs.Parse(args)

	cfg, err := normalizeConfig(Config{
		Concurrency:  *concurrency,
		Timeout:      *timeout,
		VerifyURL:    *verifyURL,
		MaxTargets:   *maxTargets,
		AllowPrivate: *allowPrivate,
	})
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	store := NewStore(*dataFile)
	if err := store.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("warning: load results: %v", err)
	}

	app := NewApp(cfg, store, *apiToken)
	srv := &http.Server{
		Addr:              *listen,
		Handler:           app.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	if !isLoopbackListen(*listen) && *apiToken == "" {
		log.Printf("warning: dashboard is listening on a non-loopback address without PSOC_API_TOKEN; restrict network access or set a token")
	}

	if *initialFile != "" {
		b, err := os.ReadFile(*initialFile)
		if err != nil {
			log.Printf("warning: startup targets: %v", err)
		} else {
			targets := ParseTargetLines(string(b))
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(targets)+1)*cfg.Timeout)
				defer cancel()
				if _, err := app.StartScan(ctx, targets); err != nil {
					log.Printf("startup scan: %v", err)
				}
			}()
		}
	}

	go func() {
		log.Printf("psoc %s listening on http://%s", version, *listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func runScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	input := fs.String("input", "-", "target file, or - for stdin")
	output := fs.String("output", "-", "result file, or - for stdout")
	format := fs.String("format", "json", "json, csv, or text")
	concurrency := fs.Int("concurrency", envInt("PSOC_CONCURRENCY", 64), "max concurrent checks")
	timeout := fs.Duration("timeout", envDuration("PSOC_TIMEOUT", 8*time.Second), "per-proxy timeout")
	verifyURL := fs.String("verify-url", envString("PSOC_VERIFY_URL", "https://example.com/"), "URL fetched through each proxy")
	maxTargets := fs.Int("max-targets", envInt("PSOC_MAX_TARGETS", 5000), "maximum targets per scan")
	allowPrivate := fs.Bool("allow-private", envBool("PSOC_ALLOW_PRIVATE", false), "allow private/local target IPs")
	protocols := fs.String("protocols", "", "comma-separated protocols for bare targets: http,socks5,socks4")
	_ = fs.Parse(args)

	cfg, err := normalizeConfig(Config{
		Concurrency:  *concurrency,
		Timeout:      *timeout,
		VerifyURL:    *verifyURL,
		MaxTargets:   *maxTargets,
		AllowPrivate: *allowPrivate,
		Protocols:    splitProtocols(*protocols),
	})
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	var r io.Reader = os.Stdin
	if *input != "-" {
		f, err := os.Open(*input)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		r = f
	}
	body, err := io.ReadAll(io.LimitReader(r, 16<<20))
	if err != nil {
		log.Fatal(err)
	}
	targets := ParseTargetLines(string(body))
	scanner, err := NewScanner(cfg)
	if err != nil {
		log.Fatal(err)
	}
	results, err := scanner.Scan(context.Background(), targets)
	if err != nil {
		log.Fatal(err)
	}

	var w io.Writer = os.Stdout
	var outFile *os.File
	if *output != "-" {
		if dir := filepath.Dir(*output); dir != "." {
			_ = os.MkdirAll(dir, 0o755)
		}
		outFile, err = os.Create(*output)
		if err != nil {
			log.Fatal(err)
		}
		defer outFile.Close()
		w = outFile
	}
	if err := writeResults(w, *format, results); err != nil {
		log.Fatal(err)
	}
}

func writeResults(w io.Writer, format string, results []Result) error {
	switch strings.ToLower(format) {
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	case "csv":
		cw := csv.NewWriter(w)
		defer cw.Flush()
		_ = cw.Write([]string{"target", "protocol", "alive", "latency_ms", "status", "checked_at", "error"})
		for _, r := range results {
			_ = cw.Write([]string{r.Target, r.Protocol, strconv.FormatBool(r.Alive), strconv.FormatInt(r.LatencyMS, 10), strconv.Itoa(r.Status), r.CheckedAt.Format(time.RFC3339), r.Error})
		}
		return cw.Error()
	case "text":
		bw := bufio.NewWriter(w)
		defer bw.Flush()
		for _, r := range results {
			state := "dead"
			if r.Alive {
				state = "alive"
			}
			fmt.Fprintf(bw, "%-5s %-7s %-24s %5dms status=%d %s\n", state, r.Protocol, r.Target, r.LatencyMS, r.Status, r.Error)
		}
		return nil
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func splitProtocols(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }
