package main

import (
	"strings"
	"testing"
)

func TestDefaultPublicSourcesAreHTTPS(t *testing.T) {
	sources := DefaultPublicSources()
	if len(sources) < 10 {
		t.Fatalf("expected a broad source catalog, got %d", len(sources))
	}
	for _, source := range sources {
		if !strings.HasPrefix(source.URL, "https://") {
			t.Fatalf("source %q is not HTTPS: %s", source.Name, source.URL)
		}
	}
}

func TestParseSourceTargetsPrefixesProtocolAndDedupes(t *testing.T) {
	body := strings.NewReader("1.1.1.1:80\n1.1.1.1:80\n8.8.8.8:8080 metadata\ninvalid\n")
	targets, err := parseSourceTargets(body, "http", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d: %#v", len(targets), targets)
	}
	for _, target := range targets {
		if target.Protocol != "http" {
			t.Fatalf("expected http target, got %#v", target)
		}
	}
}

func TestParseSourceTargetsKeepsExplicitProtocols(t *testing.T) {
	body := strings.NewReader("socks5://1.1.1.1:1080\nhttp://8.8.8.8:8080\n")
	targets, err := parseSourceTargets(body, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	if targets[0].Protocol != "socks5" || targets[1].Protocol != "http" {
		t.Fatalf("unexpected protocols: %#v", targets)
	}
}

func TestParseSourceTargetsHonorsLimit(t *testing.T) {
	body := strings.NewReader("1.1.1.1:80\n8.8.8.8:80\n9.9.9.9:80\n")
	targets, err := parseSourceTargets(body, "http", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected limit of 2, got %d", len(targets))
	}
}
