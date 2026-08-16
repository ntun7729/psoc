package main

// Reset clears the in-memory result set before a new scan starts. Results are
// persisted when the scan finishes, so the dashboard can update rapidly while
// checks are still running without rewriting the JSON file for every proxy.
func (s *Store) Reset() {
	s.mu.Lock()
	s.results = make(map[string]ProxyResult)
	s.mu.Unlock()
}

// Upsert accepts a scanner result and retains it only when it is alive.
func (s *Store) Upsert(r Result) {
	if !r.Alive {
		s.mu.Lock()
		delete(s.results, keyFor(r))
		s.mu.Unlock()
		return
	}
	s.UpsertProxy(ProxyResultFrom(r))
}

// UpsertProxy updates one enriched alive result in memory for live dashboard reads.
func (s *Store) UpsertProxy(r ProxyResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !r.Alive {
		delete(s.results, keyForProxy(r))
		return
	}
	s.results[keyForProxy(r)] = r
}

// Save persists the current in-memory snapshot.
func (s *Store) Save() error {
	s.mu.RLock()
	snapshot := s.snapshotLocked()
	s.mu.RUnlock()
	return s.persist(snapshot)
}
