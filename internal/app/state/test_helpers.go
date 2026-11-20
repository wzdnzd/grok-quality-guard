package state

// Test helpers are intentionally exported inside internal/app/state so the
// integration package can exercise persistence failure modes without exposing
// store internals in production APIs.

func (s *Store) SetStateForTest(value State) {
	s.mu.Lock()
	s.state = value
	s.markOverviewChangedLocked()
	s.mu.Unlock()
}

func (s *Store) UpdateStateForTest(fn func(*State)) {
	s.mu.Lock()
	fn(&s.state)
	s.markOverviewChangedLocked()
	s.mu.Unlock()
}

func (s *Store) RebuildProxyIndexesForTest() {
	s.mu.Lock()
	s.rebuildProxyIndexesLocked()
	s.mu.Unlock()
}

func (s *Store) SetPathForTest(path string) {
	s.mu.Lock()
	s.path = path
	s.mu.Unlock()
}

func (s *Store) StopFlushTimerForTest() {
	s.flushMu.Lock()
	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}
	s.flushMu.Unlock()
}
