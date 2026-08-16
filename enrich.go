package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	countryLookupURL = "https://api.country.is/"
	speedTestURL     = "https://speed.cloudflare.com/__down"
	speedSampleBytes = int64(1 << 20)
)

type countryLookupResponse struct {
	Country string `json:"country"`
}

// MeasureProxy enriches an already-verified proxy. Measurement failures do not
// change the proxy's alive state; unavailable fields are simply left empty/zero.
func (s *Scanner) MeasureProxy(parent context.Context, proxyAddr, protocol string) (string, float64) {
	country := ""
	geoCtx, geoCancel := context.WithTimeout(parent, s.measurementTimeout())
	if status, body, _, err := s.fetchViaProxy(geoCtx, proxyAddr, protocol, countryLookupURL, 8<<10, ""); err == nil && status >= 200 && status < 300 {
		var geo countryLookupResponse
		if json.Unmarshal(body, &geo) == nil {
			cc := strings.ToUpper(strings.TrimSpace(geo.Country))
			if len(cc) == 2 {
				country = cc
			}
		}
	}
	geoCancel()

	// Cloudflare's speed-test endpoint generates exactly the requested payload.
	// Use a 1 MiB sample to reduce noise without adding excessive bandwidth.
	downloadURL := fmt.Sprintf("%s?bytes=%d", speedTestURL, speedSampleBytes)
	speedCtx, speedCancel := context.WithTimeout(parent, s.measurementTimeout())
	status, body, elapsed, err := s.fetchViaProxy(speedCtx, proxyAddr, protocol, downloadURL, speedSampleBytes, "")
	speedCancel()
	if err != nil || status != http.StatusOK {
		return country, 0
	}
	return country, mbpsFor(int64(len(body)), elapsed)
}

func (s *Scanner) measurementTimeout() time.Duration {
	d := s.cfg.Timeout
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	if d > 12*time.Second {
		d = 12 * time.Second
	}
	return d
}

func mbpsFor(bytes int64, elapsed time.Duration) float64 {
	if bytes <= 0 || elapsed <= 0 {
		return 0
	}
	mbps := float64(bytes*8) / elapsed.Seconds() / 1_000_000
	return math.Round(mbps*100) / 100
}

func (s *Scanner) fetchViaProxy(ctx context.Context, proxyAddr, protocol, rawURL string, limit int64, rangeHeader string) (int, []byte, time.Duration, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return 0, nil, 0, err
	}
	conn, absolute, err := s.openProxyConn(ctx, proxyAddr, protocol, u)
	if err != nil {
		return 0, nil, 0, err
	}
	defer conn.Close()

	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	target := path
	if absolute {
		target = u.Scheme + "://" + u.Host + path
	}

	var req strings.Builder
	fmt.Fprintf(&req, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: psoc/%s\r\nAccept: */*\r\n", target, u.Host, version)
	if rangeHeader != "" {
		fmt.Fprintf(&req, "Range: %s\r\n", rangeHeader)
	}
	req.WriteString("\r\n")
	if _, err := io.WriteString(conn, req.String()); err != nil {
		return 0, nil, 0, err
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()

	started := time.Now()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	elapsed := time.Since(started)
	if err != nil {
		return resp.StatusCode, body, elapsed, err
	}
	return resp.StatusCode, body, elapsed, nil
}

func (s *Scanner) openProxyConn(ctx context.Context, proxyAddr, protocol string, u *url.URL) (net.Conn, bool, error) {
	dialer := net.Dialer{Timeout: s.measurementTimeout(), KeepAlive: -1}
	conn, err := dialer.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, false, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()

	deadline := time.Now().Add(s.measurementTimeout())
	if dl, has := ctx.Deadline(); has && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	destHost := u.Hostname()
	destPort := u.Port()
	if destPort == "" {
		if u.Scheme == "https" {
			destPort = "443"
		} else {
			destPort = "80"
		}
	}
	destAddr := net.JoinHostPort(destHost, destPort)
	absolute := false

	switch protocol {
	case "http":
		if u.Scheme == "http" {
			absolute = true
		} else if err := httpConnect(conn, destAddr); err != nil {
			return nil, false, err
		}
	case "socks5":
		if err := socks5Connect(conn, destHost, destPort); err != nil {
			return nil, false, err
		}
	case "socks4":
		if err := socks4Connect(conn, destHost, destPort); err != nil {
			return nil, false, err
		}
	default:
		return nil, false, fmt.Errorf("unsupported protocol %q", protocol)
	}

	if u.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: destHost, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return nil, false, err
		}
		conn = tlsConn
	}
	ok = true
	return conn, absolute, nil
}
