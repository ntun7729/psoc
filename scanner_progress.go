package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ScanWithProgress behaves like Scan but calls onResult as each proxy check
// completes. The callback is invoked serially from the result collector, so
// callers can safely update scan progress and an in-memory store without
// additional synchronization around the callback itself.
func (s *Scanner) ScanWithProgress(ctx context.Context, targets []Target, onResult func(Result)) ([]Result, error) {
	if len(targets) == 0 {
		return nil, errors.New("no valid targets")
	}
	if len(targets) > s.cfg.MaxTargets {
		return nil, fmt.Errorf("too many targets: %d > %d", len(targets), s.cfg.MaxTargets)
	}

	type job struct {
		t Target
		p string
	}
	jobs := make(chan job)
	results := make(chan Result)
	var wg sync.WaitGroup
	workers := s.cfg.Concurrency
	if workers > len(targets)*3 {
		workers = len(targets) * 3
	}
	if workers < 1 {
		workers = 1
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				r := s.check(ctx, j.t, j.p)
				select {
				case results <- r:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, t := range targets {
			if t.Protocol != "" {
				select {
				case jobs <- job{t: t, p: t.Protocol}:
				case <-ctx.Done():
					return
				}
				continue
			}
			for _, p := range s.cfg.Protocols {
				select {
				case jobs <- job{t: t, p: p}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	out := make([]Result, 0, scanJobCount(targets, s.cfg))
	for r := range results {
		out = append(out, r)
		if onResult != nil {
			onResult(r)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Alive != out[j].Alive {
			return out[i].Alive
		}
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Protocol < out[j].Protocol
	})
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return out, err
	}
	return out, nil
}

func scanJobCount(targets []Target, cfg Config) int {
	total := 0
	for _, t := range targets {
		if t.Protocol != "" {
			total++
		} else {
			total += len(cfg.Protocols)
		}
	}
	return total
}
