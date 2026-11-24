// Quality state tests cover verdict application, policy thresholds, cooldown and strike decay.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	appauth "grok-quality-guard/internal/app/auth"
	managementapp "grok-quality-guard/internal/app/management"
	appstate "grok-quality-guard/internal/app/state"
	apptasks "grok-quality-guard/internal/app/tasks"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"
)

func TestSuccessfulVerdictResetsCountersAndPreservesStrike(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if err := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateSuspect
		account.QualityStrikeMissingThinking = true
		account.SoftStrikes = 2
		account.ErrorStrikes = 2
		account.ThinkingStrikes = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassNone, Signals: qualitypkg.Signals{OutputTokens: 10}}, "passive", ""); err != nil {
		t.Fatal(err)
	}
	account, ok := plugin.stateAccount("auth-1")
	if !ok {
		t.Fatal("account state missing")
	}
	if !account.QualityStrikeMissingThinking || account.SoftStrikes != 0 || account.ErrorStrikes != 0 || account.ThinkingStrikes != 0 {
		t.Fatalf("healthy observation did not preserve the missing-thinking strike: %#v", account)
	}
}

func TestGuardDisablePreservesTokensAndWritesAuthFirst(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if err := plugin.applyGuardDisable("auth-1", appstate.DisableKindCooldown, time.Hour, qualitypkg.ClassMissingThinking, "test", true); err != nil {
		t.Fatal(err)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || !account.QualityStrikeMissingThinking {
		t.Fatalf("unexpected state after guard disable: %#v", account)
	}
	if account.DisableReason != qualitypkg.ClassMissingThinking {
		t.Fatalf("guard disable reason = %q, want %q", account.DisableReason, qualitypkg.ClassMissingThinking)
	}
	saved := host.saves["auth-1"]
	if len(saved) == 0 {
		t.Fatal("auth was not saved")
	}
	raw, err := rawAuthToMap(saved)
	if err != nil {
		t.Fatal(err)
	}
	if !appauth.Disabled(raw) {
		t.Fatal("saved auth is not disabled")
	}
	if appauth.RawString(raw, "access_token") != "access-auth-1" || appauth.RawString(raw, "refresh_token") != "refresh-auth-1" {
		t.Fatal("token fields were not preserved")
	}
	assertNoGuardMetadata(t, raw)
}

func TestGuardDisableSaveFailureLeavesIntentForReconcile(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	host.saveErr = errors.New("disk full")
	plugin := newTestPlugin(t, host, nil)
	err := plugin.applyGuardDisable("auth-1", appstate.DisableKindCooldown, time.Hour, qualitypkg.ClassMissingThinking, "test", true)
	if err == nil {
		t.Fatal("expected auth save failure")
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown || account.AuthDisabledIntent == nil || !*account.AuthDisabledIntent {
		t.Fatalf("failed auth save did not retain a disable intent: %#v", account)
	}
	if len(host.saves) != 0 {
		t.Fatal("unexpected saved auth")
	}
	host.mu.Lock()
	host.saveErr = nil
	host.mu.Unlock()
	if errReconcile := plugin.Reconcile(context.Background()); errReconcile != nil {
		t.Fatal(errReconcile)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if !appauth.Disabled(saved) {
		t.Fatal("reconcile did not complete the pending disable intent")
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.AuthDisabledIntent != nil {
		t.Fatalf("reconcile did not clear the disable intent: %#v", account)
	}
}

func TestManualRestoreSaveFailureLeavesEnableIntentForReconcile(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errDisable := plugin.applyGuardDisable("auth-1", appstate.DisableKindCooldown, time.Hour, qualitypkg.ClassMissingThinking, "test", false); errDisable != nil {
		t.Fatal(errDisable)
	}

	host.mu.Lock()
	host.saveErr = errors.New("disk full")
	host.mu.Unlock()
	if errRestore := plugin.ManualRestore(context.Background(), "auth-1"); errRestore == nil {
		t.Fatal("expected restore auth save failure")
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown || account.AuthDisabledIntent == nil || *account.AuthDisabledIntent {
		t.Fatalf("failed restore did not retain an enable intent: %#v", account)
	}

	host.mu.Lock()
	host.saveErr = nil
	host.mu.Unlock()
	if errReconcile := plugin.Reconcile(context.Background()); errReconcile != nil {
		t.Fatal(errReconcile)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if appauth.Disabled(saved) {
		t.Fatal("reconcile did not complete the pending enable intent")
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.State != appstate.StateUnknown || account.DisableKind != "" || account.AuthDisabledIntent != nil {
		t.Fatalf("reconcile did not finalize the restore: %#v", account)
	}
}

func TestRecoveryProbeMissingThinkingAfterStrikeBecomesPermanent(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_then_disable" })
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateCooling
		account.DisableKind = appstate.DisableKindCooldown
		account.QualityStrikeMissingThinking = true
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	_, response, raw, errResolve := plugin.ResolveAuth(context.Background(), "auth-1")
	if errResolve != nil {
		t.Fatal(errResolve)
	}
	lock := plugin.accountLock("auth-1")
	lock.Lock()
	errFailure := plugin.handleProbeFailure("auth-1", raw, response, true, qualitypkg.QualityVerdict{Class: qualitypkg.ClassMissingThinking}, nil)
	lock.Unlock()
	if errFailure != nil {
		t.Fatal(errFailure)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateDisabledByGuard || account.DisableKind != appstate.DisableKindPermanent {
		t.Fatalf("unexpected permanent disable state: %#v", account)
	}
	saved, err := rawAuthToMap(host.saves["auth-1"])
	if err != nil {
		t.Fatal(err)
	}
	if !appauth.Disabled(saved) {
		t.Fatalf("recovery failure did not disable auth: %#v", saved)
	}
	assertNoGuardMetadata(t, saved)
}

func TestAutoCooldownDoesNotExtendActiveCooldown(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_then_disable" })
	until := time.Now().UTC().Add(6 * time.Hour)
	if err := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateCooling
		account.CooldownUntil = until
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := plugin.applyAutoCooldown("auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassMissingThinking}, "test"); err != nil {
		t.Fatal(err)
	}
	account, _ := plugin.stateAccount("auth-1")
	if !account.CooldownUntil.Equal(until) {
		t.Fatalf("cooldown changed from %s to %s", until, account.CooldownUntil)
	}
	if len(host.saves) != 0 {
		t.Fatal("active cooldown unexpectedly wrote auth")
	}
}

func TestAutoCooldownAfterStrikeIsPermanent(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_then_disable" })
	if err := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateHealthy
		account.QualityStrikeMissingThinking = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := plugin.applyAutoCooldown("auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassMissingThinking}, "test"); err != nil {
		t.Fatal(err)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateDisabledByGuard || account.DisableKind != appstate.DisableKindPermanent {
		t.Fatalf("unexpected state: %#v", account)
	}
}

func TestExternallyDisabledAuthIsNotModified(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if err := plugin.applyGuardDisable("auth-1", appstate.DisableKindCooldown, time.Hour, qualitypkg.ClassMissingThinking, "test", true); err != nil {
		t.Fatal(err)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateExternallyDisabled {
		t.Fatalf("state = %q, want externally_disabled", account.State)
	}
	if account.DisableReason != "" {
		t.Fatalf("external disable must not invent a reason: %q", account.DisableReason)
	}
	if len(host.saves) != 0 {
		t.Fatal("externally disabled auth was modified")
	}
}

func TestRefreshCatalogKeepsExternallyDisabledSparse(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if account, exists := plugin.stateAccount("auth-1"); exists {
		t.Fatalf("catalog-only external disable should remain sparse: %#v", account)
	}
	externalFound := false
	for _, account := range plugin.CatalogSnapshot() {
		if account.ID == "auth-1" && account.Disabled {
			externalFound = true
			break
		}
	}
	if !externalFound {
		t.Fatal("external auth should remain disabled in catalog")
	}
}

func TestReconcileSkipsEnabledUnknownAccounts(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)

	if err := plugin.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if len(host.authGets) != 0 {
		t.Fatalf("reconcile read enabled unknown auths: %#v", host.authGets)
	}
}
func TestReconcileRepairsTemporaryProbeEnable(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if err := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateCooling
		account.DisableKind = appstate.DisableKindCooldown
		account.CooldownUntil = time.Now().UTC().Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := plugin.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if !appauth.Disabled(saved) {
		t.Fatal("reconcile did not restore the guard-owned disabled flag")
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown {
		t.Fatalf("unexpected reconciled state: %#v", account)
	}
}

func TestCooldownOnlyRecoveryFailureKeepsCooling(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_only" })
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateCooling
		account.DisableKind = appstate.DisableKindCooldown
		account.QualityStrikeMissingThinking = true
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	_, response, raw, errResolve := plugin.ResolveAuth(context.Background(), "auth-1")
	if errResolve != nil {
		t.Fatal(errResolve)
	}
	lock := plugin.accountLock("auth-1")
	lock.Lock()
	errFailure := plugin.handleProbeFailure("auth-1", raw, response, true, qualitypkg.QualityVerdict{Class: qualitypkg.ClassMissingThinking}, nil)
	lock.Unlock()
	if errFailure != nil {
		t.Fatal(errFailure)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown {
		t.Fatalf("cooldown-only mode did not keep cooling: %#v", account)
	}
	if account.CooldownUntil.IsZero() {
		t.Fatal("cooldown deadline was not refreshed")
	}
	saved, err := rawAuthToMap(host.saves["auth-1"])
	if err != nil {
		t.Fatal(err)
	}
	if !appauth.Disabled(saved) {
		t.Fatalf("cooldown-only recovery failure did not disable auth: %#v", saved)
	}
	assertNoGuardMetadata(t, saved)
}

func TestCooldownOnlyAppliesCooldownOnConfirmedAnomaly(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_only"
		cfg.Quality.CrossVerify = "off"
	})
	for range 2 {
		if err := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassMissingThinking}, "passive", ""); err != nil {
			t.Fatal(err)
		}
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown {
		t.Fatalf("cooldown-only anomaly was not cooled: %#v", account)
	}
	if account.CooldownUntil.IsZero() {
		t.Fatal("cooldown deadline is missing")
	}
}

// Policy thresholds.

func TestConsecutiveSoftTPSThreshold(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_only" })
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassSoftTPS, Signals: qualitypkg.Signals{OutputTokens: 40, TPS: 600}}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateSuspect || account.SoftStrikes != 1 || len(host.saves) != 0 {
		t.Fatalf("first soft observation should only be suspect: %#v saves=%d", account, len(host.saves))
	}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.SoftStrikes != 2 || len(host.saves) != 1 {
		t.Fatalf("second soft observation should trigger cooldown: %#v saves=%d", account, len(host.saves))
	}
}

func TestTransportErrorsStayUnreachableWithoutCooldown(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_only" })
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassProbeError}
	var previousBackoff time.Duration
	for strike := 1; strike <= 3; strike++ {
		if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "probe", ""); errApply != nil {
			t.Fatal(errApply)
		}
		account, _ := plugin.stateAccount("auth-1")
		if account.State != appstate.StateUnreachable || account.DisableKind != "" || len(host.saves) != 0 {
			t.Fatalf("transport error %d escalated beyond unreachable: %#v saves=%d", strike, account, len(host.saves))
		}
		backoff := time.Until(account.ProbeBackoffUntil)
		if backoff <= 0 || backoff > 10*time.Minute || backoff <= previousBackoff {
			t.Fatalf("transport error %d backoff = %s, previous %s", strike, backoff, previousBackoff)
		}
		previousBackoff = backoff
	}
}

func TestProbeErrorStrikesStayBoundedAcrossRecoveryCooldowns(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_only"
		cfg.Quality.ConsecutiveErrors = 2
	})
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassProbeError, Reason: "reason.detail.probe_transport_error"}

	for attempt := 0; attempt < 2; attempt++ {
		if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "probe", ""); errApply != nil {
			t.Fatal(errApply)
		}
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateUnreachable || account.ErrorStrikes != 2 {
		t.Fatalf("initial error threshold state = %#v, want unreachable with 2 strikes", account)
	}

	// Each expired cooldown starts a recovery probe. A repeated transport error
	// must not keep increasing the consecutive counter beyond the policy
	// threshold: the dashboard would otherwise show values such as 53/2.
	for cycle := 0; cycle < 3; cycle++ {
		if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
			account.State = appstate.StateCooling
			account.DisableKind = appstate.DisableKindCooldown
			account.CooldownUntil = time.Now().UTC().Add(-time.Second)
			return nil
		}); errUpdate != nil {
			t.Fatal(errUpdate)
		}
		if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "probe", ""); errApply != nil {
			t.Fatal(errApply)
		}
		account, _ = plugin.stateAccount("auth-1")
		if account.ErrorStrikes != 2 {
			t.Fatalf("recovery cycle %d produced error strikes %d, want bounded at threshold 2: %#v", cycle+1, account.ErrorStrikes, account)
		}
	}
}
func TestConsecutiveMissingThinkingThreshold(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_then_disable"
		cfg.Quality.ConsecutiveMissingThinking = 2
		cfg.Quality.CrossVerify = "off"
	})
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassMissingThinking}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateSuspect || account.ThinkingStrikes != 1 || len(host.saves) != 0 {
		t.Fatalf("first missing-thinking should only be suspect: %#v saves=%d", account, len(host.saves))
	}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.ThinkingStrikes != 2 || !account.QualityStrikeMissingThinking || len(host.saves) != 1 {
		t.Fatalf("second missing-thinking should trigger cooldown: %#v saves=%d", account, len(host.saves))
	}
}

func TestNonQualityProbeStatusDoesNotConsumeErrorStrikes(t *testing.T) {
	if class := classifyProbeStatus(401); class != qualitypkg.ClassCredentialInvalid {
		t.Fatalf("401 classified as %q, want %q", class, qualitypkg.ClassCredentialInvalid)
	}
	if class := classifyProbeStatus(500); class != qualitypkg.ClassIgnored {
		t.Fatalf("500 classified as %q, want %q", class, qualitypkg.ClassIgnored)
	}
	if class := classifyProbeStatus(407); class != qualitypkg.ClassProxyError {
		t.Fatalf("407 classified as %q, want %q", class, qualitypkg.ClassProxyError)
	}
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_only" })
	for index := 0; index < 3; index++ {
		verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassIgnored}
		if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "probe", ""); errApply != nil {
			t.Fatal(errApply)
		}
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.ErrorStrikes != 0 || account.State != appstate.StateSuspect || len(host.saves) != 0 {
		t.Fatalf("ignored probe changed quality state: %#v saves=%d", account, len(host.saves))
	}
}

func TestSoftCrossVerifySchedulesProbeBeforeAction(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_then_disable"
		cfg.Quality.ConsecutiveSoft = 1
		cfg.Quality.CrossVerify = "soft"
	})
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassSoftTPS, Signals: qualitypkg.Signals{OutputTokens: 40, TPS: 600}}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	if len(host.saves) != 0 {
		t.Fatalf("cross-verify threshold wrote auth before confirmation: %d", len(host.saves))
	}
	found := false
	for _, task := range plugin.listTasks(20) {
		if task.Kind == "quality.cross_verify" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("soft cross-verify task was not scheduled")
	}
}

func TestObserveModeNeverWritesAuthAtThreshold(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "observe" })
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassSoftTPS, Signals: qualitypkg.Signals{OutputTokens: 40, TPS: 600}}
	for index := 0; index < 2; index++ {
		if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
			t.Fatal(errApply)
		}
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateDegraded || len(host.saves) != 0 {
		t.Fatalf("observe mode modified auth: %#v saves=%d", account, len(host.saves))
	}
}

func TestStateStorePersistsPolicyAndStrikes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := appstate.NewStore(path)
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	policy.Quality.ConsecutiveSoft = 4
	policy.Quality.ConsecutiveErrors = 3
	policy.Quality.ConsecutiveMissingThinking = 3
	policy.Quality.ThinkingGuard = false
	policy.Quality.CrossVerify = "all"
	if errLoad := store.Load(policy, "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	if errUpdate := store.Update(func(state *appstate.State) error {
		state.Accounts["auth-1"] = &appstate.AccountState{
			AuthID:          "auth-1",
			State:           appstate.StateSuspect,
			FirstSeenAt:     time.Now().UTC(),
			SoftStrikes:     2,
			ErrorStrikes:    1,
			ThinkingStrikes: 3,
		}
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	reloaded := appstate.NewStore(path)
	if errLoad := reloaded.Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "revision-2"); errLoad != nil {
		t.Fatal(errLoad)
	}
	snapshot := reloaded.Snapshot()
	quality := snapshot.Policy.Quality
	if quality.ConsecutiveSoft != 4 || quality.ConsecutiveErrors != 3 || quality.ConsecutiveMissingThinking != 3 {
		t.Fatalf("consecutive policy was not persisted: %#v", quality)
	}
	if quality.ThinkingGuard || quality.CrossVerify != "all" {
		t.Fatalf("thinking policy was not persisted: %#v", quality)
	}
	account := snapshot.Accounts["auth-1"]
	if account == nil || account.SoftStrikes != 2 || account.ErrorStrikes != 1 || account.ThinkingStrikes != 3 {
		t.Fatalf("strikes were not persisted: %#v", account)
	}
}

func TestProbeManagementReturnsAcceptedTask(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	body, errMarshal := json.Marshal(managementapp.ActionRequest{Action: "accounts.probe", Params: map[string]any{"auth_id": "auth-1", "force": true}})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", http.Header{"Content-Type": []string{"application/json"}}, body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("probe status = %d body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data apptasks.Task `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if envelope.Data.ID == "" || envelope.Data.Kind != "accounts.probe" {
		t.Fatalf("unexpected task response: %#v", envelope.Data)
	}
	finished := waitTaskForTest(t, plugin, envelope.Data.ID)
	if finished.Status != taskStatusSucceeded && finished.Status != taskStatusFailed {
		t.Fatalf("task did not finish: %#v", finished)
	}
}

// Strike decay.

func seedStrikeAccount(t *testing.T, plugin *Plugin, authID string, startedAt time.Time, streak int) {
	t.Helper()
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := plugin.ensureStateAccount(state, authID)
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
		account := plugin.ensureStateAccount(state, "auth-1")
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
	// The buffer floor is 1MiB, so the scripted stream must exceed it.
	oversized := strings.Repeat("overflow ", (1<<20)/9+1)
	host.scriptStream("auth-1",
		streamData("data: {\"choices\":[{\"delta\":{\"content\":\""+oversized+"\"}}]}\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
		pluginapi.HostModelStreamReadResponse{Done: true},
	)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.HoldMaxBufferBytes = 1 << 20
	})
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := plugin.ensureStateAccount(state, "auth-1")
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

func TestDumpVerdictReleasesSustainedVisibleStream(t *testing.T) {
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
	if verdict := ClassifyQualityHold(base, config.MinHoldOutputTokens); verdict.Action != qualitypkg.QualityActionDeliver || verdict.Class != qualitypkg.ClassNone {
		t.Fatalf("sustained visible stream with encrypted evidence produced %s/%s, want deliver", verdict.Action, verdict.Class)
	}
	starting := base
	starting.VisibleChunks = 1
	starting.VisibleSpanMS = 100
	if verdict := ClassifyQualityHold(starting, config.MinHoldOutputTokens); verdict.Action != qualitypkg.QualityActionWait {
		t.Fatalf("first visible chunk produced %s/%s, want wait", verdict.Action, verdict.Class)
	}
	base.Terminal = true
	if verdict := ClassifyQualityHold(base, config.MinHoldOutputTokens); verdict.Action != qualitypkg.QualityActionDeliver || verdict.Class != qualitypkg.ClassNone {
		t.Fatalf("terminal fast stream produced %s/%s, want deliver", verdict.Action, verdict.Class)
	}
	single := base
	single.VisibleChunks = 1
	single.VisibleSpanMS = 0
	if verdict := ClassifyQualityHold(single, config.MinHoldOutputTokens); !isDumpClass(verdict.Class) {
		t.Fatalf("single-chunk terminal dump was not caught: %#v", verdict)
	}
}
