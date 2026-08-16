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

type Store struct {
	mu      sync.RWMutex
	path    string
	results map[string]Result
}

func NewStore(path string) *Store {
	return &Store{path: path, results: make(map[string]Result)}
}

func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var rs []Result
	if err := json.Unmarshal(b, &rs); err != nil {
		return err
	}
	for _, r := range rs {
		s.results[keyFor(r)] = r
	}
	return nil
}

func (s *Store) Replace(rs []Result) error {
	s.mu.Lock()
	for _, r := range rs {
		s.results[keyFor(r)] = r
	}
	snapshot := s.snapshotLocked()
	s.mu.Unlock()
	return s.persist(snapshot)
}

func (s *Store) All() []Result {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotLocked()
}

func (s *Store) snapshotLocked() []Result {
	out := make([]Result, 0, len(s.results))
	for _, r := range s.results {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Alive != out[j].Alive {
			return out[i].Alive
		}
		if !out[i].CheckedAt.Equal(out[j].CheckedAt) {
			return out[i].CheckedAt.After(out[j].CheckedAt)
		}
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Protocol < out[j].Protocol
	})
	return out
}

func (s *Store) persist(rs []Result) error {
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

type Stats struct {
	Total       int       `json:"total"`
	Alive       int       `json:"alive"`
	Dead        int       `json:"dead"`
	HTTP        int       `json:"http"`
	SOCKS5      int       `json:"socks5"`
	SOCKS4      int       `json:"socks4"`
	AvgLatency  int64     `json:"avg_latency_ms"`
	LastChecked time.Time `json:"last_checked,omitempty"`
}

func BuildStats(rs []Result) Stats {
	var st Stats
	var latencySum int64
	for _, r := range rs {
		st.Total++
		if r.Alive {
			st.Alive++
			latencySum += r.LatencyMS
			switch r.Protocol {
			case "http":
				st.HTTP++
			case "socks5":
				st.SOCKS5++
			case "socks4":
				st.SOCKS4++
			}
		} else {
			st.Dead++
		}
		if r.CheckedAt.After(st.LastChecked) {
			st.LastChecked = r.CheckedAt
		}
	}
	if st.Alive > 0 {
		st.AvgLatency = latencySum / int64(st.Alive)
	}
	return st
}
