package app

import (
	"context"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"
	"testing"
	"time"
)

func seedStrikeAccount(t *testing.T, plugin *Plugin, authID string, startedAt time.Time, streak int) {
	t.Helper()
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := state.Accounts[authID]
		account.State = appstate.StateHealthy
		account.QualityStrikeMissingThinking = true
		account.QualityFailureAt = startedAt
		account.HealthyStreak = streak
		account.HealthyStreakStartedAt = startedAt
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
}

func TestStrikeDecayRequiresWindowAndHealthySamples(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a1@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_then_disable"
		cfg.Quality.StrikeDecayAfterHours = 48
		cfg.Quality.StrikeDecayMinHealthy = 3
	})
	startedAt := time.Now().UTC().Add(-49 * time.Hour)
	seedStrikeAccount(t, plugin, "auth-1", startedAt, 0)

	for index := 0; index < 2; index++ {
		if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassNone, Signals: qualitypkg.Signals{OutputTokens: 10, VisibleTokens: 10}}, "passive", ""); errApply != nil {
			t.Fatal(errApply)
		}
	}
	account, _ := plugin.stateAccount("auth-1")
	if !account.QualityStrikeMissingThinking || account.HealthyStreak != 2 {
		t.Fatalf("strike decayed before enough healthy samples: %#v", account)
	}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassNone, Signals: qualitypkg.Signals{OutputTokens: 10, VisibleTokens: 10}}, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.QualityStrikeMissingThinking || account.HealthyStreak != 0 {
		t.Fatalf("strike did not decay after window and healthy samples: %#v", account)
	}
	decayed := false
	for _, event := range plugin.stateStore().Snapshot().Events {
		if event.AuthID == "auth-1" && event.Action == "strike_decay" && event.Result == "expired" {
			decayed = true
			break
		}
	}
	if !decayed {
		t.Fatal("strike decay event was not recorded")
	}
}

func TestStrikeDecayDoesNotClearBeforeWindow(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a1@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Quality.StrikeDecayAfterHours = 48
		cfg.Quality.StrikeDecayMinHealthy = 1
	})
	seedStrikeAccount(t, plugin, "auth-1", time.Now().UTC().Add(-47*time.Hour), 0)
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassNone, Signals: qualitypkg.Signals{OutputTokens: 10}}, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ := plugin.stateAccount("auth-1")
	if !account.QualityStrikeMissingThinking {
		t.Fatalf("strike decayed before the configured window: %#v", account)
	}
}

func TestProbeSuccessCountsTowardStrikeDecay(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a1@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Quality.StrikeDecayAfterHours = 48
		cfg.Quality.StrikeDecayMinHealthy = 3
	})
	startedAt := time.Now().UTC().Add(-49 * time.Hour)
	seedStrikeAccount(t, plugin, "auth-1", startedAt, 2)
	if errProbe := plugin.markProbeSuccess("auth-1", qualitypkg.Signals{Terminal: true, OutputTokens: 40, VisibleTokens: 40, HasThinking: true, ReasoningTokens: 10}); errProbe != nil {
		t.Fatal(errProbe)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.QualityStrikeMissingThinking {
		t.Fatalf("probe success did not count toward decay: %#v", account)
	}
}

func TestProbeErrorDoesNotAdvanceStrikeDecay(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a1@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Quality.StrikeDecayAfterHours = 48
		cfg.Quality.StrikeDecayMinHealthy = 3
	})
	seedStrikeAccount(t, plugin, "auth-1", time.Now().UTC().Add(-49*time.Hour), 2)
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassProbeError, Reason: "reason.detail.probe_transport_error"}, "probe", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ := plugin.stateAccount("auth-1")
	if !account.QualityStrikeMissingThinking || account.HealthyStreak != 2 {
		t.Fatalf("probe error changed strike decay state: %#v", account)
	}
}

func TestRateLimitedDoesNotResetConsecutiveCounters(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a1@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := state.Accounts["auth-1"]
		account.State = appstate.StateHealthy
		account.SoftStrikes = 1
		account.ErrorStrikes = 1
		account.ThinkingStrikes = 1
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassRateLimited, Reason: "reason.detail.upstream_status_429_rate_limited"}, "transparent", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.SoftStrikes != 1 || account.ErrorStrikes != 1 || account.ThinkingStrikes != 1 {
		t.Fatalf("rate limit reset consecutive counters: %#v", account)
	}
}

func TestBufferOverflowDoesNotRecordHealthyVerdict(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a1@example.com", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", badStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.HoldMaxBufferBytes = 1
	})
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := state.Accounts["auth-1"]
		account.State = appstate.StateHealthy
		account.SoftStrikes = 1
		account.ErrorStrikes = 1
		account.ThinkingStrikes = 1
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	streamID := "plugin-stream-overflow-no-verdict"
	plugin.runTransparentStreamBridge(context.Background(), nonReasoningStreamRequestForTest(), streamID)
	host.mu.Lock()
	emitted := len(host.emitted[streamID])
	detail := host.closes[streamID]
	host.mu.Unlock()
	if emitted == 0 || detail != nil {
		t.Fatalf("overflow was not delivered cleanly: emitted=%d detail=%#v", emitted, detail)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateHealthy || account.SoftStrikes != 1 || account.ErrorStrikes != 1 || account.ThinkingStrikes != 1 {
		t.Fatalf("overflow changed account quality state: %#v", account)
	}
}

func TestDumpVerdictWaitsForFinalVisibleWindow(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	base := qualitypkg.Signals{
		HasThinking:     true,
		VisibleTokens:   20,
		OutputTokens:    420,
		ReasoningTokens: 400,
		EncryptedBytes:  2048,
		EncryptedFloor:  256,
		FirstVisible:    true,
		VisibleChunks:   12,
		VisibleSpanMS:   224,
	}
	if verdict := ClassifyQualityHold(base, config.MinHoldOutputTokens); verdict.Action != qualitypkg.QualityActionWait {
		t.Fatalf("open visible window produced %s/%s, want wait", verdict.Action, verdict.Class)
	}
	base.Terminal = true
	if verdict := ClassifyQualityHold(base, config.MinHoldOutputTokens); verdict.Action != qualitypkg.QualityActionDeliver {
		t.Fatalf("healthy terminal stream produced %s/%s, want deliver", verdict.Action, verdict.Class)
	}
	single := base
	single.VisibleChunks = 1
	single.VisibleSpanMS = 0
	if verdict := ClassifyQualityHold(single, config.MinHoldOutputTokens); !isDumpClass(verdict.Class) {
		t.Fatalf("single-chunk terminal dump was not caught: %#v", verdict)
	}
}
