package app

import (
	"context"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	coreproxy "grok-quality-guard/internal/proxy"
	qualitypkg "grok-quality-guard/internal/quality"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestCatalogRefreshKeepsGuardOwnedStateDuringTemporaryOmission(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateCooling
		account.DisableKind = appstate.DisableKindCooldown
		account.CooldownUntil = time.Now().UTC().Add(time.Hour)
		account.LastReason = qualitypkg.ClassMissingThinking
		account.DisableReason = qualitypkg.ClassMissingThinking
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
	if !ok || account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown || account.LastReason != qualitypkg.ClassMissingThinking {
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
	if !ok || account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown {
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
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateUnknown
		account.DisableKind = appstate.DisableKindQuota
		account.LastReason = qualitypkg.ClassQuotaExhausted
		account.DisableReason = qualitypkg.ClassQuotaExhausted
		account.CooldownUntil = time.Now().UTC().Add(time.Hour)
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	if errSync := plugin.syncCatalogState(plugin.CatalogSnapshot()); errSync != nil {
		t.Fatal(errSync)
	}
	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.State != appstate.StateQuotaExhausted || account.DisableKind != appstate.DisableKindQuota {
		t.Fatalf("disable kind was not used to restore ownership: %#v", account)
	}
	if account.LastReason != qualitypkg.ClassQuotaExhausted || account.DisableReason != qualitypkg.ClassQuotaExhausted {
		t.Fatalf("guard reason changed: last=%q disable=%q", account.LastReason, account.DisableReason)
	}
}

func TestCatalogSyncLeavesExternalDisableReasonUnknown(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.State != appstate.StateExternallyDisabled {
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
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.State = appstate.StateCooling
		account.DisableKind = appstate.DisableKindCooldown
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
	if errDisable := plugin.applyGuardDisable("auth-1", appstate.DisableKindCooldown, time.Hour, qualitypkg.ClassMissingThinking, "test", true); errDisable != nil {
		t.Fatal(errDisable)
	}
	statePath := plugin.stateStore().Path()
	plugin.Shutdown()

	cfg := appconfig.DefaultConfig()
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
	if !ok || account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown {
		t.Fatalf("guard ownership was lost after restart: %#v", account)
	}
	if account.DisableReason != qualitypkg.ClassMissingThinking {
		t.Fatalf("guard reason was lost after restart: %q", account.DisableReason)
	}
	response := actionManagementForTest(t, restarted, "accounts.list", map[string]any{"summary": true})
	if response.StatusCode != 200 || !strings.Contains(string(response.Body), `"disable_reason":"missing_thinking"`) {
		t.Fatalf("management response did not expose the persisted guard reason: status=%d body=%s", response.StatusCode, response.Body)
	}
}

func TestCatalogRefreshKeepsManagedProxyBindingDuringTemporaryOmission(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	url := "socks5h://grok-alice:secret@resin:2260"
	endpoint, errParse := coreproxy.ParseEndpoint(url)
	if errParse != nil {
		t.Fatal(errParse)
	}
	binding := coreproxy.Binding{
		AuthID:       "auth-1",
		SourceID:     "resin-1",
		SourceKind:   coreproxy.SourceResin,
		Generation:   1,
		EndpointHash: endpoint.Fingerprint(),
	}
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.Proxy = binding
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
	if !ok || account.Proxy.SourceID != binding.SourceID || account.Proxy.EndpointHash != binding.EndpointHash {
		t.Fatalf("managed proxy binding was lost during a partial catalog: %#v", account.Proxy)
	}
	if account.CatalogMissingSince.IsZero() {
		t.Fatal("missing catalog timestamp was not recorded for a managed proxy")
	}

	host.mu.Lock()
	host.entries = entries
	host.mu.Unlock()
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	account, ok = plugin.stateAccount("auth-1")
	if !ok {
		t.Fatal("account state was not restored after catalog recovery")
	}
	status, sourceID, _, managed := plugin.proxySvc.AccountBindingView(account, url)
	if !managed || status != "bound" || sourceID != binding.SourceID {
		t.Fatalf("managed proxy was reclassified after catalog recovery: status=%q source=%q managed=%v", status, sourceID, managed)
	}
}
