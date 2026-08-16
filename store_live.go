package main

// Reset clears the in-memory result set before a new scan starts. Results are
// persisted when the scan finishes, so the dashboard can update rapidly while
// checks are still running without rewriting the JSON file for every proxy.
func (s *Store) Reset() {
	s.mu.Lock()
	s.results = make(map[string]Result)
	s.mu.Unlock()
}

// Upsert updates one result in memory for live dashboard reads.
func (s *Store) Upsert(r Result) {
	s.mu.Lock()
	s.results[keyFor(r)] = r
	s.mu.Unlock()
}

// Save persists the current in-memory snapshot.
func (s *Store) Save() error {
	s.mu.RLock()
	snapshot := s.snapshotLocked()
	s.mu.RUnlock()
	return s.persist(snapshot)
}
