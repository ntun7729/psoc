package main

import (
	"testing"
	"time"
)

func TestStoreKeepsOnlyAliveAndSortsLowestLatency(t *testing.T) {
	store := NewStore("")
	store.Upsert(Result{Target: "203.0.113.1:80", Protocol: "http", Alive: true, LatencyMS: 320})
	store.Upsert(Result{Target: "203.0.113.2:80", Protocol: "http", Alive: false, LatencyMS: 20})
	store.Upsert(Result{Target: "203.0.113.3:80", Protocol: "http", Alive: true, LatencyMS: 85})

	got := store.All()
	if len(got) != 2 {
		t.Fatalf("alive result count = %d, want 2", len(got))
	}
	if got[0].Target != "203.0.113.3:80" || got[0].LatencyMS != 85 {
		t.Fatalf("first result = %#v, want lowest latency proxy", got[0])
	}
}

func TestBuildStatsLowestLatency(t *testing.T) {
	now := time.Now().UTC()
	st := BuildStats([]ProxyResult{
		{Target: "203.0.113.1:80", Protocol: "http", Alive: true, LatencyMS: 412, CheckedAt: now},
		{Target: "203.0.113.2:1080", Protocol: "socks5", Alive: true, LatencyMS: 97, CheckedAt: now},
	})
	if st.Alive != 2 {
		t.Fatalf("alive = %d, want 2", st.Alive)
	}
	if st.LowestLatency != 97 {
		t.Fatalf("lowest latency = %d, want 97", st.LowestLatency)
	}
}

func TestMbpsFor(t *testing.T) {
	got := mbpsFor(1_000_000, time.Second)
	if got != 8 {
		t.Fatalf("mbpsFor() = %v, want 8", got)
	}
}
