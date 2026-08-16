package main

import (
	"net"
	"testing"
	"time"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in, proto, addr string
	}{
		{"1.2.3.4:8080", "", "1.2.3.4:8080"},
		{"http://1.2.3.4:3128", "http", "1.2.3.4:3128"},
		{"https://1.2.3.4:3128", "http", "1.2.3.4:3128"},
		{"socks5://example.com:1080", "socks5", "example.com:1080"},
		{"socks4://[2001:4860:4860::8888]:1080", "socks4", "[2001:4860:4860::8888]:1080"},
	}
	for _, tc := range cases {
		got, err := parseTarget(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if got.Protocol != tc.proto || got.Address != tc.addr {
			t.Fatalf("%s: got %#v", tc.in, got)
		}
	}
}

func TestParseTargetRejectsBadPort(t *testing.T) {
	if _, err := parseTarget("1.2.3.4:70000"); err == nil {
		t.Fatal("expected error")
	}
}

func TestPublicIPFilter(t *testing.T) {
	private := []string{"127.0.0.1", "10.0.0.1", "172.16.0.1", "192.168.1.1", "169.254.1.1", "::1", "fc00::1"}
	for _, s := range private {
		if isPublicIP(net.ParseIP(s)) {
			t.Fatalf("expected %s to be blocked", s)
		}
	}
	public := []string{"1.1.1.1", "8.8.8.8", "2001:4860:4860::8888"}
	for _, s := range public {
		if !isPublicIP(net.ParseIP(s)) {
			t.Fatalf("expected %s to be public", s)
		}
	}
}

func TestNormalizeConfigCaps(t *testing.T) {
	cfg, err := normalizeConfig(Config{Concurrency: 999, Timeout: 5 * time.Minute, VerifyURL: "https://example.com/", MaxTargets: 999999})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 256 || cfg.Timeout != 60*time.Second || cfg.MaxTargets != 50000 {
		t.Fatalf("unexpected caps: %+v", cfg)
	}
}
