package probe

import "time"

func (s *Service) IsProbeAuth(authID string) bool {
	s.probeSkipMu.Lock()
	defer s.probeSkipMu.Unlock()
	expiry, ok := s.probeSkip[authID]
	if ok && time.Now().After(expiry) {
		delete(s.probeSkip, authID)
		return false
	}
	return ok
}

func (s *Service) MarkProbeAuth(authID string, ttl time.Duration) {
	if authID == "" {
		return
	}
	s.probeSkipMu.Lock()
	s.probeSkip[authID] = time.Now().Add(ttl)
	s.probeSkipMu.Unlock()
}

func (s *Service) cleanupProbeSkip(now time.Time) {
	if s == nil {
		return
	}
	s.probeSkipMu.Lock()
	for authID, expiry := range s.probeSkip {
		if !now.Before(expiry) {
			delete(s.probeSkip, authID)
		}
	}
	s.probeSkipMu.Unlock()
}
