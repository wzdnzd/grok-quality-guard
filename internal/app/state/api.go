package state

// AppendEvent appends a bounded event and applies the persisted event limit.
func AppendEvent(state *State, ev Event) {
	appendEvent(state, ev)
}

// SetPathUnchecked switches the backing path and optionally overwrites an
// existing file. It exists for rollback after a failed pointer update.
func (s *Store) SetPathUnchecked(path string, overwrite bool) error {
	return s.setPath(path, overwrite)
}

func (s *Store) ProxyPendingReserved(authID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxyPendingReserved[authID]
}

func (s *Store) ProxyLoad(sourceID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxyCounts[sourceID] + s.proxyPendingCounts[sourceID]
}

func (s *Store) ProxyBindingCount(id string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxyCounts[id]
}

func ResinSnapshotID(settings ResinSettings) string {
	return resinSnapshotID(settings)
}

type ProxyIndexes struct {
	Counts          map[string]int
	PendingCounts   map[string]int
	PendingReserved map[string]string
	LeaseAccounts   map[string]int
}

func (s *Store) ProxyIndexSnapshot() ProxyIndexes {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := ProxyIndexes{
		Counts:          make(map[string]int, len(s.proxyCounts)),
		PendingCounts:   make(map[string]int, len(s.proxyPendingCounts)),
		PendingReserved: make(map[string]string, len(s.proxyPendingReserved)),
		LeaseAccounts:   make(map[string]int, len(s.proxyLeaseAccounts)),
	}
	for key, value := range s.proxyCounts {
		out.Counts[key] = value
	}
	for key, value := range s.proxyPendingCounts {
		out.PendingCounts[key] = value
	}
	for key, value := range s.proxyPendingReserved {
		out.PendingReserved[key] = value
	}
	for key, value := range s.proxyLeaseAccounts {
		out.LeaseAccounts[key] = value
	}
	return out
}

func (s *Store) ProxyLeaseUsage(account string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxyLeaseAccounts[account]
}

func (s *Store) EnsureResinSnapshot(settings ResinSettings) (string, error) {
	id := resinSnapshotID(settings)
	if _, ok := s.ResinSnapshot(id); ok {
		return id, nil
	}
	if err := s.CommitProxy(ProxyCommit{Snapshots: map[string]ResinSettings{id: settings}}); err != nil {
		return "", err
	}
	return id, nil
}
func GCProxyHistory(state *State) {
	gcProxyHistory(state)
}

// ProxyPendingLocked is for callbacks already executing under Store.Update.
func (s *Store) ProxyPendingLocked(authID string) string {
	return s.proxyPending[authID]
}
