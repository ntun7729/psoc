package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Concurrency  int
	Timeout      time.Duration
	VerifyURL    string
	MaxTargets   int
	AllowPrivate bool
	Protocols    []string
}

type Result struct {
	Target    string    `json:"target"`
	Protocol  string    `json:"protocol"`
	Alive     bool      `json:"alive"`
	LatencyMS int64     `json:"latency_ms"`
	Status    int       `json:"status,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	Error     string    `json:"error,omitempty"`
}

type Target struct {
	Raw      string `json:"raw"`
	Address  string `json:"address"`
	Protocol string `json:"protocol,omitempty"`
}

type Scanner struct {
	cfg       Config
	verifyURL *url.URL
}

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 64
	}
	if cfg.Concurrency > 256 {
		cfg.Concurrency = 256
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 8 * time.Second
	}
	if cfg.Timeout > 60*time.Second {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.VerifyURL == "" {
		cfg.VerifyURL = "https://example.com/"
	}
	u, err := url.Parse(cfg.VerifyURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return cfg, fmt.Errorf("verify URL must be http/https with a hostname")
	}
	if cfg.MaxTargets <= 0 {
		cfg.MaxTargets = 5000
	}
	if cfg.MaxTargets > 50000 {
		cfg.MaxTargets = 50000
	}
	if len(cfg.Protocols) == 0 {
		cfg.Protocols = []string{"http", "socks5", "socks4"}
	}
	seen := map[string]bool{}
	out := make([]string, 0, 3)
	for _, p := range cfg.Protocols {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "http" && p != "socks5" && p != "socks4" {
			return cfg, fmt.Errorf("unsupported protocol %q", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	cfg.Protocols = out
	return cfg, nil
}

func NewScanner(cfg Config) (*Scanner, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(cfg.VerifyURL)
	return &Scanner{cfg: cfg, verifyURL: u}, nil
}

func ParseTargetLines(body string) []Target {
	lines := strings.FieldsFunc(body, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == '\t' || r == ' '
	})
	seen := map[string]bool{}
	out := make([]Target, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		t, err := parseTarget(line)
		if err != nil {
			continue
		}
		key := t.Protocol + "|" + t.Address
		if !seen[key] {
			seen[key] = true
			out = append(out, t)
		}
	}
	return out
}

func parseTarget(s string) (Target, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Target{}, errors.New("empty target")
	}
	proto := ""
	addr := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return Target{}, err
		}
		proto = strings.ToLower(u.Scheme)
		if proto == "https" {
			proto = "http"
		}
		if proto != "http" && proto != "socks5" && proto != "socks4" {
			return Target{}, fmt.Errorf("unsupported protocol %q", proto)
		}
		addr = u.Host
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return Target{}, fmt.Errorf("target must be host:port: %w", err)
	}
	if host == "" {
		return Target{}, errors.New("missing host")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Target{}, errors.New("invalid port")
	}
	return Target{Raw: raw, Address: net.JoinHostPort(host, port), Protocol: proto}, nil
}

func (s *Scanner) Scan(ctx context.Context, targets []Target) ([]Result, error) {
	if len(targets) == 0 {
		return nil, errors.New("no valid targets")
	}
	if len(targets) > s.cfg.MaxTargets {
		return nil, fmt.Errorf("too many targets: %d > %d", len(targets), s.cfg.MaxTargets)
	}

	type job struct {
		t Target
		p string
	}
	jobs := make(chan job)
	results := make(chan Result)
	var wg sync.WaitGroup
	workers := s.cfg.Concurrency
	if workers > len(targets)*3 {
		workers = len(targets) * 3
	}
	if workers < 1 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				results <- s.check(ctx, j.t, j.p)
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, t := range targets {
			if t.Protocol != "" {
				select {
				case jobs <- job{t: t, p: t.Protocol}:
				case <-ctx.Done():
					return
				}
				continue
			}
			for _, p := range s.cfg.Protocols {
				select {
				case jobs <- job{t: t, p: p}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	out := make([]Result, 0, len(targets)*len(s.cfg.Protocols))
	for r := range results {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Alive != out[j].Alive {
			return out[i].Alive
		}
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Protocol < out[j].Protocol
	})
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return out, err
	}
	return out, nil
}

func (s *Scanner) check(parent context.Context, t Target, protocol string) Result {
	r := Result{Target: t.Address, Protocol: protocol, CheckedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(parent, s.cfg.Timeout)
	defer cancel()

	if err := s.validatePublicTarget(ctx, t.Address); err != nil {
		r.Error = err.Error()
		return r
	}

	start := time.Now()
	status, err := s.checkProtocol(ctx, t.Address, protocol)
	r.LatencyMS = time.Since(start).Milliseconds()
	r.Status = status
	if err != nil {
		r.Error = cleanError(err)
		return r
	}
	r.Alive = status >= 200 && status < 400
	if !r.Alive {
		r.Error = fmt.Sprintf("verify returned HTTP %d", status)
	}
	return r
}

func (s *Scanner) validatePublicTarget(ctx context.Context, addr string) error {
	if s.cfg.AllowPrivate {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return errors.New("private/local target blocked")
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("resolve target: %w", err)
	}
	if len(ips) == 0 {
		return errors.New("target hostname has no IPs")
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return errors.New("target hostname resolves to private/local IP")
		}
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !a.IsLoopback() && !a.IsLinkLocalUnicast() && !a.IsLinkLocalMulticast()
}

func (s *Scanner) checkProtocol(ctx context.Context, proxyAddr, protocol string) (int, error) {
	dialer := net.Dialer{Timeout: s.cfg.Timeout, KeepAlive: -1}
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(s.cfg.Timeout))

	destHost := s.verifyURL.Hostname()
	destPort := s.verifyURL.Port()
	if destPort == "" {
		if s.verifyURL.Scheme == "https" {
			destPort = "443"
		} else {
			destPort = "80"
		}
	}
	destAddr := net.JoinHostPort(destHost, destPort)

	switch protocol {
	case "http":
		if s.verifyURL.Scheme == "http" {
			return doHTTPAbsolute(conn, s.verifyURL)
		}
		if err := httpConnect(conn, destAddr); err != nil {
			return 0, err
		}
	case "socks5":
		if err := socks5Connect(conn, destHost, destPort); err != nil {
			return 0, err
		}
	case "socks4":
		if err := socks4Connect(conn, destHost, destPort); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("unsupported protocol %q", protocol)
	}

	if s.verifyURL.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: destHost, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return 0, fmt.Errorf("tls: %w", err)
		}
		defer tlsConn.Close()
		conn = tlsConn
	}
	return doOriginGET(conn, s.verifyURL)
}

func httpConnect(conn net.Conn, dest string) error {
	req := "CONNECT " + dest + " HTTP/1.1\r\nHost: " + dest + "\r\nProxy-Connection: close\r\nUser-Agent: psoc/" + version + "\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("proxy CONNECT response: %w", err)
	}
	parts := strings.Fields(statusLine)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return fmt.Errorf("invalid proxy CONNECT response")
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return fmt.Errorf("invalid proxy CONNECT status")
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return fmt.Errorf("proxy CONNECT headers: %w", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	if code < 200 || code >= 300 {
		return fmt.Errorf("proxy CONNECT returned HTTP %d", code)
	}
	return nil
}

func doHTTPAbsolute(conn net.Conn, u *url.URL) (int, error) {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	absolute := u.Scheme + "://" + u.Host + path
	req := "GET " + absolute + " HTTP/1.1\r\nHost: " + u.Host + "\r\nConnection: close\r\nUser-Agent: psoc/" + version + "\r\nAccept: */*\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return 0, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
	return resp.StatusCode, nil
}

func doOriginGET(conn net.Conn, u *url.URL) (int, error) {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	req := "GET " + path + " HTTP/1.1\r\nHost: " + u.Host + "\r\nConnection: close\r\nUser-Agent: psoc/" + version + "\r\nAccept: */*\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		return 0, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
	return resp.StatusCode, nil
}

func socks5Connect(conn net.Conn, host, port string) error {
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if buf[0] != 0x05 || buf[1] != 0x00 {
		return fmt.Errorf("socks5 auth method rejected: %x %x", buf[0], buf[1])
	}

	p, _ := strconv.Atoi(port)
	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, 0x01)
			req = append(req, ip4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("socks5 hostname too long")
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(p))
	if _, err := conn.Write(req); err != nil {
		return err
	}

	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		return err
	}
	if head[0] != 0x05 || head[1] != 0x00 {
		return fmt.Errorf("socks5 connect failed, code 0x%02x", head[1])
	}
	switch head[3] {
	case 0x01:
		_, err := io.CopyN(io.Discard, conn, 4+2)
		return err
	case 0x04:
		_, err := io.CopyN(io.Discard, conn, 16+2)
		return err
	case 0x03:
		var n [1]byte
		if _, err := io.ReadFull(conn, n[:]); err != nil {
			return err
		}
		_, err := io.CopyN(io.Discard, conn, int64(n[0])+2)
		return err
	default:
		return fmt.Errorf("socks5 invalid address type 0x%02x", head[3])
	}
}

func socks4Connect(conn net.Conn, host, port string) error {
	p, _ := strconv.Atoi(port)
	req := []byte{0x04, 0x01, byte(p >> 8), byte(p)}
	ip := net.ParseIP(host)
	if ip4 := ip.To4(); ip4 != nil {
		req = append(req, ip4...)
		req = append(req, 0x00)
	} else {
		// SOCKS4a domain form: 0.0.0.1 + empty user ID + domain + NUL.
		req = append(req, 0x00, 0x00, 0x00, 0x01, 0x00)
		req = append(req, host...)
		req = append(req, 0x00)
	}
	if _, err := conn.Write(req); err != nil {
		return err
	}
	resp := make([]byte, 8)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return err
	}
	if resp[1] != 0x5a {
		return fmt.Errorf("socks4 connect failed, code 0x%02x", resp[1])
	}
	return nil
}

func cleanError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}
