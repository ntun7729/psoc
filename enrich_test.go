package main

import (
	"testing"
	"time"
)

func TestSpeedMeasurementSettings(t *testing.T) {
	if got, want := speedTestURL, "https://speed.cloudflare.com/__down"; got != want {
		t.Fatalf("speedTestURL = %q, want %q", got, want)
	}
	if got, want := proxyMeasurementConcurrency, 5; got != want {
		t.Fatalf("proxyMeasurementConcurrency = %d, want %d", got, want)
	}
	if got, want := speedSampleBytes, int64(1<<20); got != want {
		t.Fatalf("speedSampleBytes = %d, want %d", got, want)
	}
}

func TestMbpsFor(t *testing.T) {
	if got, want := mbpsFor(1_000_000, time.Second), 8.0; got != want {
		t.Fatalf("mbpsFor() = %.2f, want %.2f", got, want)
	}
}
