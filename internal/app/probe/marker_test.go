package probe

import "testing"

func TestProbeMarkerUsesLastNonEmptyLine(t *testing.T) {
	if !MatchProbeMarker("intro\nQUALITY_OK\u00a0\n\n", "QUALITY_OK") {
		t.Fatal("last-line marker with unicode trailing whitespace did not match")
	}
	if MatchProbeMarker("QUALITY_OK\nextra", "QUALITY_OK") {
		t.Fatal("marker outside the last non-empty line incorrectly matched")
	}
}
