package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestScanJobCount(t *testing.T) {
	cfg := Config{Protocols: []string{"http", "socks5", "socks4"}}
	targets := []Target{
		{Address: "203.0.113.10:8080"},
		{Address: "203.0.113.11:1080", Protocol: "socks5"},
	}
	if got, want := scanJobCount(targets, cfg), 4; got != want {
		t.Fatalf("scanJobCount() = %d, want %d", got, want)
	}
}

func TestScanWithProgressCallsBack(t *testing.T) {
	s, err := NewScanner(Config{
		Concurrency:  2,
		Timeout:      100 * time.Millisecond,
		VerifyURL:    "https://example.com/",
		MaxTargets:   10,
		AllowPrivate: false,
		Protocols:    []string{"http"},
	})
	if err != nil {
		t.Fatal(err)
	}

	called := 0
	results, err := s.ScanWithProgress(context.Background(), []Target{{
		Raw:      "http://127.0.0.1:1",
		Address:  "127.0.0.1:1",
		Protocol: "http",
	}}, func(Result) {
		called++
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := called, 1; got != want {
		t.Fatalf("callback count = %d, want %d", got, want)
	}
	if got, want := len(results), 1; got != want {
		t.Fatalf("result count = %d, want %d", got, want)
	}
}

func TestStoreLiveUpdateAndSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.json")
	store := NewStore(path)
	store.Upsert(Result{Target: "203.0.113.10:8080", Protocol: "http", Alive: true})
	if got := len(store.All()); got != 1 {
		t.Fatalf("store result count = %d, want 1", got)
	}
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	store.Reset()
	if got := len(store.All()); got != 0 {
		t.Fatalf("store result count after reset = %d, want 0", got)
	}
}
