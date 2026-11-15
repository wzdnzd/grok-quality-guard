package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestCatalogRefreshKeepsGuardOwnedStateDuringTemporaryOmission(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *AccountState) error {
		account.State = StateCooling
		account.DisableKind = DisableKindCooldown
		account.CooldownUntil = time.Now().UTC().Add(time.Hour)
		account.LastReason = ClassMissingThinking
		account.DisableReason = ClassMissingThinking
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	host.mu.Lock()
	host.entries = nil
	host.mu.Unlock()
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.State != StateCooling || account.DisableKind != DisableKindCooldown || account.LastReason != ClassMissingThinking {
		t.Fatalf("guard-owned state was lost during a partial catalog: %#v", account)
	}
	if account.CatalogMissingSince.IsZero() {
		t.Fatal("missing catalog timestamp was not recorded")
	}

	host.mu.Lock()
	host.entries = entries
	host.mu.Unlock()
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	account, ok = plugin.stateAccount("auth-1")
	if !ok || account.State != StateCooling || account.DisableKind != DisableKindCooldown {
		t.Fatalf("guard-owned state was not restored after catalog recovery: %#v", account)
	}
	if !account.CatalogMissingSince.IsZero() {
		t.Fatalf("catalog missing timestamp was not cleared: %s", account.CatalogMissingSince)
	}
}

func TestCatalogSyncRestoresStateFromDisableKind(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *AccountState) error {
		account.State = StateUnknown
		account.DisableKind = DisableKindQuota
		account.LastReason = ClassQuotaExhausted
		account.DisableReason = ClassQuotaExhausted
		account.CooldownUntil = time.Now().UTC().Add(time.Hour)
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	if errSync := plugin.syncCatalogState(plugin.catalogSnapshot()); errSync != nil {
		t.Fatal(errSync)
	}
	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.State != StateQuotaExhausted || account.DisableKind != DisableKindQuota {
		t.Fatalf("disable kind was not used to restore ownership: %#v", account)
	}
	if account.LastReason != ClassQuotaExhausted || account.DisableReason != ClassQuotaExhausted {
		t.Fatalf("guard reason changed: last=%q disable=%q", account.LastReason, account.DisableReason)
	}
}

func TestCatalogSyncLeavesExternalDisableReasonUnknown(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.State != StateExternallyDisabled {
		t.Fatalf("external disable state = %#v", account)
	}
	if account.DisableReason != "" || account.LastReason != "" {
		t.Fatalf("external disable must not invent a reason: %#v", account)
	}
}

func TestCatalogRefreshPrunesStaleGuardStateAfterRetention(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *AccountState) error {
		account.State = StateCooling
		account.DisableKind = DisableKindCooldown
		account.CatalogMissingSince = time.Now().UTC().Add(-guardCatalogMissingRetention - time.Minute)
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	host.mu.Lock()
	host.entries = nil
	host.mu.Unlock()
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	if _, ok := plugin.stateAccount("auth-1"); ok {
		t.Fatal("stale guard-owned state was not pruned after retention")
	}
}

func TestGuardOwnershipAndReasonSurvivePluginRestart(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errDisable := plugin.applyGuardDisable("auth-1", DisableKindCooldown, time.Hour, ClassMissingThinking, "test", true); errDisable != nil {
		t.Fatal(errDisable)
	}
	statePath := plugin.stateStore().Path()
	plugin.Shutdown()

	cfg := DefaultConfig()
	cfg.Enabled = false
	cfg.StateFile = statePath
	rawConfig, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	restarted := NewPlugin()
	if errConfigure := restarted.Configure(rawConfig); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(restarted.Shutdown)
	if errRefresh := restarted.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	account, ok := restarted.stateAccount("auth-1")
	if !ok || account.State != StateCooling || account.DisableKind != DisableKindCooldown {
		t.Fatalf("guard ownership was lost after restart: %#v", account)
	}
	if account.DisableReason != ClassMissingThinking {
		t.Fatalf("guard reason was lost after restart: %q", account.DisableReason)
	}
	response := actionManagementForTest(t, restarted, "accounts.list", map[string]any{"summary": true})
	if response.StatusCode != 200 || !strings.Contains(string(response.Body), `"disable_reason":"missing_thinking"`) {
		t.Fatalf("management response did not expose the persisted guard reason: status=%d body=%s", response.StatusCode, response.Body)
	}
}
