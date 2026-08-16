package main

import "testing"

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
	if got, want := networkLookupURL, "http://ip-api.com/json/?fields=status,proxy,hosting"; got != want {
		t.Fatalf("networkLookupURL = %q, want %q", got, want)
	}
}

func TestClassifyNetworkType(t *testing.T) {
	tests := []struct {
		name string
		info networkLookupResponse
		want string
	}{
		{name: "residential", info: networkLookupResponse{Status: "success"}, want: "Residential"},
		{name: "residential proxy", info: networkLookupResponse{Status: "success", Proxy: true}, want: "Residential Proxy"},
		{name: "hosting wins", info: networkLookupResponse{Status: "success", Proxy: true, Hosting: true}, want: "Proxy/Hosting"},
		{name: "failed lookup", info: networkLookupResponse{Status: "fail", Proxy: true, Hosting: true}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyNetworkType(tt.info); got != tt.want {
				t.Fatalf("classifyNetworkType(%+v) = %q, want %q", tt.info, got, tt.want)
			}
		})
	}
}
