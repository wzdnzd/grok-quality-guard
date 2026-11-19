package probe

import (
	"testing"
	"time"
)

func TestProbeMarkerUsesLastNonEmptyLine(t *testing.T) {
	if !MatchProbeMarker("intro\nQUALITY_OK\u00a0\n\n", "QUALITY_OK") {
		t.Fatal("last-line marker with unicode trailing whitespace did not match")
	}
	if MatchProbeMarker("QUALITY_OK\nextra", "QUALITY_OK") {
		t.Fatal("marker outside the last non-empty line incorrectly matched")
	}
}

func TestProbeSkipCleanupRemovesOnlyExpiredEntries(t *testing.T) {
	now := time.Now().UTC()
	service := &Service{probeSkip: map[string]time.Time{
		"expired": now.Add(-time.Second),
		"live":    now.Add(time.Minute),
	}}
	service.cleanupProbeSkip(now)
	if _, exists := service.probeSkip["expired"]; exists {
		t.Fatal("expired probe skip entry was retained")
	}
	if _, exists := service.probeSkip["live"]; !exists {
		t.Fatal("live probe skip entry was removed")
	}
}
