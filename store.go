package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type ProxyResult struct {
	Target      string    `json:"target"`
	Protocol    string    `json:"protocol"`
	Alive       bool      `json:"alive"`
	LatencyMS   int64     `json:"latency_ms"`
	Status      int       `json:"status,omitempty"`
	CheckedAt   time.Time `json:"checked_at"`
	Country     string    `json:"country,omitempty"`
	NetworkType string    `json:"network_type,omitempty"`
	DownMbps    float64   `json:"down_mbps,omitempty"`
}

func ProxyResultFrom(r Result) ProxyResult {
	return ProxyResult{
		Target:    r.Target,
		Protocol:  r.Protocol,
		Alive:     r.Alive,
		LatencyMS: r.LatencyMS,
		Status:    r.Status,
		CheckedAt: r.CheckedAt,
	}
}

type Store struct {
	mu      sync.RWMutex
	path    string
	results map[string]ProxyResult
}

func NewStore(path string) *Store {
	return &Store{path: path, results: make(map[string]ProxyResult)}
}

func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var rs []ProxyResult
	if err := json.Unmarshal(b, &rs); err != nil {
		return err
	}
	s.results = make(map[string]ProxyResult)
	for _, r := range rs {
		if !r.Alive {
			continue
		}
		s.results[keyForProxy(r)] = r
	}
	return nil
}

func (s *Store) Replace(rs []ProxyResult) error {
	s.mu.Lock()
	s.results = make(map[string]ProxyResult)
	for _, r := range rs {
		if r.Alive {
			s.results[keyForProxy(r)] = r
		}
	}
	snapshot := s.snapshotLocked()
	s.mu.Unlock()
	return s.persist(snapshot)
}

func (s *Store) All() []ProxyResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *Store) snapshotLocked() []ProxyResult {
	out := make([]ProxyResult, 0, len(s.results))
	for _, r := range s.results {
		if r.Alive {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := out[i].LatencyMS, out[j].LatencyMS
		if li <= 0 {
			li = 1<<62 - 1
		}
		if lj <= 0 {
			lj = 1<<62 - 1
		}
		if li != lj {
			return li < lj
		}
		if out[i].DownMbps != out[j].DownMbps {
			return out[i].DownMbps > out[j].DownMbps
		}
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Protocol < out[j].Protocol
	})
	return out
}

func (s *Store) persist(rs []ProxyResult) error {
	if s.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	b, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func keyFor(r Result) string { return r.Protocol + "|" + r.Target }
func keyForProxy(r ProxyResult) string { return r.Protocol + "|" + r.Target }

type Stats struct {
	Total         int       `json:"total"`
	Alive         int       `json:"alive"`
	HTTP          int       `json:"http"`
	SOCKS5        int       `json:"socks5"`
	SOCKS4        int       `json:"socks4"`
	LowestLatency int64     `json:"lowest_latency_ms"`
	LastChecked   time.Time `json:"last_checked,omitempty"`
}

func BuildStats(rs []ProxyResult) Stats {
	var st Stats
	for _, r := range rs {
		if !r.Alive {
			continue
		}
		st.Total++
		st.Alive++
		if r.LatencyMS > 0 && (st.LowestLatency == 0 || r.LatencyMS < st.LowestLatency) {
			st.LowestLatency = r.LatencyMS
		}
		switch r.Protocol {
		case "http":
			st.HTTP++
		case "socks5":
			st.SOCKS5++
		case "socks4":
			st.SOCKS4++
		}
		if r.CheckedAt.After(st.LastChecked) {
			st.LastChecked = r.CheckedAt
		}
	}
	return st
}
