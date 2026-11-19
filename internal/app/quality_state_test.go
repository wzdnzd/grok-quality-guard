package app

import (
	"context"
	"errors"
	appauth "grok-quality-guard/internal/app/auth"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"
	"testing"
	"time"
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
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_only" })
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassMissingThinking}, "passive", ""); err != nil {
		t.Fatal(err)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown {
		t.Fatalf("cooldown-only anomaly was not cooled: %#v", account)
	}
	if account.CooldownUntil.IsZero() {
		t.Fatal("cooldown deadline is missing")
	}
}
