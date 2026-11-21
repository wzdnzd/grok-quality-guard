package state

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestObserveRoutingSampleDecaysByRealTime(t *testing.T) {
	now := time.Now().UTC()
	stats := RoutingStats{}
	ObserveRoutingSample(&stats, RoutingSourceProbe, true, true, 500, 100, now, time.Hour)
	first := stats.Probe.Reasoning
	if first.Samples != 1 || first.SuccessEWMA <= 0 || first.LogTTFTEWMA == 0 || first.LogTPSEWMA == 0 {
		t.Fatalf("first routing sample was not recorded: %#v", first)
	}

	ObserveRoutingSample(&stats, RoutingSourceProbe, true, false, 0, 0, now.Add(30*time.Minute), time.Hour)
	second := stats.Probe.Reasoning
	if second.Samples != 2 {
		t.Fatalf("sample count = %d, want 2", second.Samples)
	}
	if second.SuccessEWMA >= first.SuccessEWMA {
		t.Fatalf("success evidence did not decay: before=%f after=%f", first.SuccessEWMA, second.SuccessEWMA)
	}
	if second.FailureEWMA <= 0 {
		t.Fatalf("failure evidence was not recorded: %#v", second)
	}
	if math.Abs(float64(second.LogTTFTEWMA-first.LogTTFTEWMA)) > 1e-6 {
		t.Fatalf("missing TTFT sample changed the TTFT EWMA: before=%f after=%f", first.LogTTFTEWMA, second.LogTTFTEWMA)
	}
}

func TestRoutingSourceHalfLifeFollowsHealthyProbeCadence(t *testing.T) {
	for _, source := range []string{RoutingSourceProbe, RoutingSourceTransparent, RoutingSourcePassive} {
		if got := RoutingSampleHalfLife(source, 24); got != 24*time.Hour {
			t.Fatalf("%s half-life = %s, want 24h", source, got)
		}
		if got := RoutingSampleHalfLife(source, 0); got != 168*time.Hour {
			t.Fatalf("%s disabled-probe half-life = %s, want 168h", source, got)
		}
	}
}

func TestRoutingStatsUseCompactJSON(t *testing.T) {
	raw, errMarshal := json.Marshal(AccountState{})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if strings.Contains(string(raw), `"routing_stats"`) {
		t.Fatalf("zero routing stats were not omitted: %s", raw)
	}

	stats := RoutingStats{}
	stats.Probe.Reasoning.Samples = 1
	raw, errMarshal = json.Marshal(AccountState{RoutingStats: stats})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	text := string(raw)
	if !strings.Contains(text, `"probe":{"reasoning":{"samples":1}}`) {
		t.Fatalf("unexpected compact routing stats JSON: %s", text)
	}
	if strings.Contains(text, `"transparent"`) || strings.Contains(text, `"passive"`) {
		t.Fatalf("empty routing sources were not omitted: %s", text)
	}
}
