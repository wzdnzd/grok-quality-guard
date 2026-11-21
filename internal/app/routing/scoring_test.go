package routing

import (
	"math"
	"testing"
	"time"

	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
)

func scoreStats(success float32, ttftMS, tps float64) appstate.RoutingStats {
	nowMS := time.Now().UTC().UnixMilli()
	window := appstate.RoutingSampleWindow{
		Samples:        8,
		SuccessEWMA:    success,
		FailureEWMA:    1 - success,
		LogTTFTEWMA:    float32(math.Log(ttftMS)),
		LogTPSEWMA:     float32(math.Log(tps)),
		LastObservedMS: nowMS,
	}
	return appstate.RoutingStats{Probe: appstate.RoutingSourceStats{Reasoning: window}}
}

func TestSelectionIndexBuildsScoreBucketsFromConfiguredCapacity(t *testing.T) {
	accounts := []Account{
		{ID: "auth-1", Provider: "xai", Priority: 10},
		{ID: "auth-2", Provider: "xai", Priority: 10},
		{ID: "auth-3", Provider: "xai", Priority: 10},
		{ID: "auth-4", Provider: "xai", Priority: 10},
		{ID: "auth-5", Provider: "xai", Priority: 10},
		{ID: "unknown-1", Provider: "xai", Priority: 10},
		{ID: "unknown-2", Provider: "xai", Priority: 10},
		{ID: "unknown-3", Provider: "xai", Priority: 10},
	}
	states := map[string]string{
		"auth-1": StateHealthy, "auth-2": StateHealthy, "auth-3": StateHealthy,
		"auth-4": StateHealthy, "auth-5": StateHealthy,
		"unknown-1": StateUnknown, "unknown-2": StateUnknown, "unknown-3": StateUnknown,
	}
	stats := map[string]appstate.RoutingStats{
		"auth-1": scoreStats(0.20, 7000, 30),
		"auth-2": scoreStats(0.95, 250, 900),
		"auth-3": scoreStats(0.55, 2500, 100),
		"auth-4": scoreStats(0.80, 900, 400),
		"auth-5": scoreStats(0.35, 4000, 60),
	}
	policy := config.PolicyFromConfig(config.DefaultConfig())
	policy.Routing.Scoring.MaxAccountsPerBucket = 2

	var index SelectionIndex
	index.BuildWithPolicy(accounts, states, stats, policy)
	buckets := index.Load().Buckets("0:10")
	if len(buckets) != 3 {
		t.Fatalf("bucket count = %d, want 3", len(buckets))
	}
	for position, bucket := range buckets {
		if len(bucket.Accounts) > 2 {
			t.Fatalf("bucket %d has %d accounts, capacity is 2", position, len(bucket.Accounts))
		}
	}
	if got := buckets[0].Accounts[0].ID; got != "auth-2" {
		t.Fatalf("top scored auth = %s, want auth-2", got)
	}
	unknown := index.Load().Buckets("1:10")
	if len(unknown) != 2 || unknown[0].Accounts[0].ID != "unknown-1" || unknown[1].Accounts[0].ID != "unknown-3" {
		t.Fatalf("unknown fallback bucket = %#v", unknown)
	}
}

func TestSelectionRefreshDueUsesAgeOrAccumulatedDrift(t *testing.T) {
	minInterval := 5 * time.Minute
	maxAge := 30 * time.Minute
	if selectionRefreshDue(4*time.Minute, true, 1, 0.03, minInterval, maxAge) {
		t.Fatal("refresh happened before the minimum interval")
	}
	if selectionRefreshDue(6*time.Minute, false, 0, 0.03, minInterval, maxAge) {
		t.Fatal("clean index refreshed before max age")
	}
	if !selectionRefreshDue(6*time.Minute, true, 0.04, 0.03, minInterval, maxAge) {
		t.Fatal("dirty index did not refresh after enough drift")
	}
	if !selectionRefreshDue(maxAge, false, 0, 0.03, minInterval, maxAge) {
		t.Fatal("index did not refresh at max age")
	}
}

func TestAccountRoutingScoreFadesStaleEvidenceTowardNeutral(t *testing.T) {
	policy := config.PolicyFromConfig(config.DefaultConfig())
	now := time.Now().UTC()
	fresh := scoreStats(0.95, 250, 900)
	freshScore := accountRoutingScore(Account{ID: "fresh", Provider: "xai"}, fresh, policy, now)

	stale := scoreStats(0.95, 250, 900)
	stale.Probe.Reasoning.LastObservedMS = now.Add(-30 * 24 * time.Hour).UnixMilli()
	staleScore := accountRoutingScore(Account{ID: "stale", Provider: "xai"}, stale, policy, now)

	if freshScore <= 0.8 {
		t.Fatalf("fresh high-quality score = %f, want > 0.8", freshScore)
	}
	if staleScore <= 0.5 || staleScore >= 0.6 {
		t.Fatalf("stale high-quality score = %f, want it to fade to near neutral", staleScore)
	}
	if staleScore >= freshScore {
		t.Fatalf("stale score %f should be below fresh score %f", staleScore, freshScore)
	}
}

func TestAdaptiveRefreshIntervalBoundedAndTrafficSensitive(t *testing.T) {
	index := routingSelectionIndex{}
	refresh := config.DefaultRoutingConfig().Scoring.Refresh
	refresh.MinInterval = config.NewDuration(5 * time.Minute)
	refresh.MaxAge = config.NewDuration(30 * time.Minute)
	now := time.Now().UTC()

	lowLoad := index.adaptiveRefreshInterval(refresh, now)
	index.activityRate = 10
	index.activityUpdated = now
	highLoad := index.adaptiveRefreshInterval(refresh, now)
	if highLoad < refresh.MinInterval.Duration || highLoad > refresh.MaxAge.Duration {
		t.Fatalf("high-load interval %s is outside configured bounds", highLoad)
	}
	if lowLoad < refresh.MinInterval.Duration || lowLoad > refresh.MaxAge.Duration {
		t.Fatalf("low-load interval %s is outside configured bounds", lowLoad)
	}
	if highLoad >= lowLoad {
		t.Fatalf("high-load interval %s should be shorter than low-load interval %s", highLoad, lowLoad)
	}
}
