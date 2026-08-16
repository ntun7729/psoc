package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const maxSourceBody = 4 << 20

type ProxySource struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Protocol string `json:"protocol,omitempty"`
	URL      string `json:"url"`
}

type SourceStatus struct {
	ProxySource
	OK         bool      `json:"ok"`
	HTTPStatus int       `json:"http_status,omitempty"`
	LatencyMS  int64     `json:"latency_ms,omitempty"`
	Targets    int       `json:"targets"`
	CheckedAt  time.Time `json:"checked_at,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// DefaultPublicSources is intentionally a curated catalog of HTTPS feeds.
// The scanner still verifies every imported proxy; a provider saying a proxy
// is live is never treated as proof that it works from this machine.
func DefaultPublicSources() []ProxySource {
	return []ProxySource{
		{Name: "ProxyScrape all", Provider: "ProxyScrape", URL: "https://api.proxyscrape.com/v4/free-proxy-list/get?request=display_proxies&proxy_format=protocolipport&format=text"},

		{Name: "Proxifly HTTP", Provider: "Proxifly", Protocol: "http", URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/http/data.txt"},
		{Name: "Proxifly SOCKS4", Provider: "Proxifly", Protocol: "socks4", URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks4/data.txt"},
		{Name: "Proxifly SOCKS5", Provider: "Proxifly", Protocol: "socks5", URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/protocols/socks5/data.txt"},

		{Name: "monosans HTTP", Provider: "monosans", Protocol: "http", URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt"},
		{Name: "monosans SOCKS4", Provider: "monosans", Protocol: "socks4", URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks4.txt"},
		{Name: "monosans SOCKS5", Provider: "monosans", Protocol: "socks5", URL: "https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/socks5.txt"},

		{Name: "TheSpeedX HTTP", Provider: "TheSpeedX", Protocol: "http", URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/http.txt"},
		{Name: "TheSpeedX SOCKS4", Provider: "TheSpeedX", Protocol: "socks4", URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks4.txt"},
		{Name: "TheSpeedX SOCKS5", Provider: "TheSpeedX", Protocol: "socks5", URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks5.txt"},

		{Name: "OpenProxyList HTTP", Provider: "roosterkid/openproxylist", Protocol: "http", URL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/HTTPS_RAW.txt"},
		{Name: "OpenProxyList SOCKS4", Provider: "roosterkid/openproxylist", Protocol: "socks4", URL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/SOCKS4_RAW.txt"},
		{Name: "OpenProxyList SOCKS5", Provider: "roosterkid/openproxylist", Protocol: "socks5", URL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/SOCKS5_RAW.txt"},
	}
}

func InitialSourceStatuses() []SourceStatus {
	sources := DefaultPublicSources()
	out := make([]SourceStatus, len(sources))
	for i, source := range sources {
		out[i] = SourceStatus{ProxySource: source}
	}
	return out
}

// FetchPublicProxySources checks every configured feed, parses its targets and
// returns a globally de-duplicated set capped by maxTargets. Feed checks run in
// parallel so one slow provider does not serially block all of the others.
func FetchPublicProxySources(ctx context.Context, maxTargets int) ([]Target, []SourceStatus) {
	if maxTargets <= 0 {
		maxTargets = 5000
	}
	if maxTargets > 50000 {
		maxTargets = 50000
	}

	sources := DefaultPublicSources()
	statuses := make([]SourceStatus, len(sources))
	batches := make([][]Target, len(sources))
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 4 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("refusing non-HTTPS redirect")
			}
			return nil
		},
	}

	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i, source := range sources {
		i, source := i, source
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				statuses[i] = SourceStatus{ProxySource: source, CheckedAt: time.Now().UTC(), Error: ctx.Err().Error()}
				return
			}
			batches[i], statuses[i] = fetchOneSource(ctx, client, source, maxTargets)
		}()
	}
	wg.Wait()

	seen := make(map[string]bool, maxTargets)
	targets := make([]Target, 0, maxTargets)
	for _, batch := range batches {
		for _, target := range batch {
			key := target.Protocol + "|" + target.Address
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, target)
			if len(targets) >= maxTargets {
				return targets, statuses
			}
		}
	}
	return targets, statuses
}

func fetchOneSource(ctx context.Context, client *http.Client, source ProxySource, limit int) ([]Target, SourceStatus) {
	status := SourceStatus{ProxySource: source, CheckedAt: time.Now().UTC()}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.URL, nil)
	if err != nil {
		status.Error = err.Error()
		return nil, status
	}
	req.Header.Set("Accept", "text/plain, */*;q=0.2")
	req.Header.Set("User-Agent", "psoc/"+version+" public-source-checker")

	resp, err := client.Do(req)
	status.LatencyMS = time.Since(started).Milliseconds()
	if err != nil {
		status.Error = err.Error()
		return nil, status
	}
	defer resp.Body.Close()
	status.HTTPStatus = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return nil, status
	}

	targets, err := parseSourceTargets(io.LimitReader(resp.Body, maxSourceBody+1), source.Protocol, limit)
	if err != nil {
		status.Error = err.Error()
		return nil, status
	}
	status.Targets = len(targets)
	status.OK = len(targets) > 0
	if !status.OK {
		status.Error = "feed returned no valid proxy targets"
	}
	return targets, status
}

func parseSourceTargets(r io.Reader, protocol string, limit int) ([]Target, error) {
	if limit <= 0 {
		limit = 5000
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	seen := make(map[string]bool)
	out := make([]Target, 0, minInt(limit, 1024))
	bytesRead := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		bytesRead += len(line) + 1
		if bytesRead > maxSourceBody {
			return out, fmt.Errorf("source exceeds %d MiB limit", maxSourceBody>>20)
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Some feeds add whitespace-delimited metadata. The first field is the
		// proxy endpoint in every bundled plain-text source.
		if fields := strings.Fields(line); len(fields) > 0 {
			line = fields[0]
		}
		if !strings.Contains(line, "://") && protocol != "" {
			line = protocol + "://" + line
		}
		target, err := parseTarget(line)
		if err != nil || target.Protocol == "" {
			continue
		}
		key := target.Protocol + "|" + target.Address
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, target)
		if len(out) >= limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
