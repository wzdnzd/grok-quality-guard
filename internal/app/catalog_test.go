// Catalog tests cover refresh retention, ownership, loading failures and large-catalog management.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"

	catalogapp "grok-quality-guard/internal/app/catalog"
	managementapp "grok-quality-guard/internal/app/management"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	coreproxy "grok-quality-guard/internal/proxy"
	qualitypkg "grok-quality-guard/internal/quality"
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

func TestCatalogRefreshRetainsExternalDisabledStateDuringTemporaryOmission(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-external", "[EMAIL]", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	firstSeen := time.Now().UTC().Add(-2 * time.Hour)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := plugin.ensureStateAccount(state, "auth-external")
		account.State = appstate.StateExternallyDisabled
		account.FirstSeenAt = firstSeen
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
	account, exists := plugin.stateAccount("auth-external")
	if !exists || account.State != appstate.StateExternallyDisabled || account.CatalogMissingSince.IsZero() {
		t.Fatalf("external disabled state was lost during a partial catalog: %#v", account)
	}

	host.mu.Lock()
	host.entries = entries
	host.mu.Unlock()
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	account, exists = plugin.stateAccount("auth-external")
	if !exists || account.State != appstate.StateExternallyDisabled || !account.CatalogMissingSince.IsZero() || !account.FirstSeenAt.Equal(firstSeen) {
		t.Fatalf("external disabled state was not restored intact: %#v", account)
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
	if account, ok := plugin.stateAccount("auth-1"); ok {
		t.Fatalf("external disable should remain sparse, got %#v", account)
	}
	catalog := plugin.CatalogSnapshot()
	if len(catalog) != 1 || catalog[0].ID != "auth-1" || !catalog[0].Disabled {
		t.Fatalf("catalog did not retain the external disabled account: %#v", catalog)
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
	if response.StatusCode != 200 || !strings.Contains(string(response.Body), `"disable_reason":"缺少真实思考"`) {
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
	status, sourceID, _, managed := plugin.proxySvc.AccountBindingViewWithHash(account, url, "")
	if !managed || status != "bound" || sourceID != binding.SourceID {
		t.Fatalf("managed proxy was reclassified after catalog recovery: status=%q source=%q managed=%v", status, sourceID, managed)
	}
}

func TestCatalogRefreshPreservesFirstSeenAtForSparseAccounts(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	before := plugin.CatalogSnapshot()
	if len(before) != 1 || before[0].FirstSeenAt.IsZero() {
		t.Fatalf("catalog did not record FirstSeenAt: %#v", before)
	}
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	after := plugin.CatalogSnapshot()
	if len(after) != 1 || !after[0].FirstSeenAt.Equal(before[0].FirstSeenAt) {
		t.Fatalf("FirstSeenAt changed across refresh: before=%s after=%s", before[0].FirstSeenAt, after[0].FirstSeenAt)
	}
	if account, exists := plugin.stateAccount("auth-1"); exists {
		t.Fatalf("catalog-only account should remain sparse: %#v", account)
	}
}

func TestCatalogSyncPreservesLegacyExternalDisabledState(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-disabled", "[EMAIL]", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	initialCatalog := plugin.catalogSvc
	plugin.catalogSvc = catalogapp.NewService(plugin)
	t.Cleanup(initialCatalog.Close)
	t.Cleanup(plugin.catalogSvc.Close)
	legacyFirstSeen := time.Now().UTC().Add(-2 * time.Hour)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := plugin.ensureStateAccount(state, "auth-disabled")
		account.State = appstate.StateExternallyDisabled
		account.FirstSeenAt = legacyFirstSeen
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	account, exists := plugin.stateAccount("auth-disabled")
	if !exists || account.State != appstate.StateExternallyDisabled || !account.FirstSeenAt.Equal(legacyFirstSeen) {
		t.Fatalf("legacy externally-disabled state was not preserved: %#v", account)
	}
	catalog := plugin.CatalogSnapshot()
	if len(catalog) != 1 || !catalog[0].Disabled || !catalog[0].FirstSeenAt.Equal(legacyFirstSeen) {
		t.Fatalf("catalog did not preserve externally-disabled state: %#v", catalog)
	}
}

func TestCatalogSyncPreservesLegacyDefaultStateAndFirstSeen(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	initialCatalog := plugin.catalogSvc
	plugin.catalogSvc = catalogapp.NewService(plugin)
	t.Cleanup(initialCatalog.Close)
	legacyFirstSeen := time.Now().UTC().Add(-2 * time.Hour)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := plugin.ensureStateAccount(state, "auth-1")
		account.State = appstate.StateUnknown
		account.FirstSeenAt = legacyFirstSeen
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	account, exists := plugin.stateAccount("auth-1")
	if !exists || account.State != appstate.StateUnknown || !account.FirstSeenAt.Equal(legacyFirstSeen) {
		t.Fatalf("legacy default state was not preserved: %#v", account)
	}
	catalog := plugin.CatalogSnapshot()
	if len(catalog) != 1 || !catalog[0].FirstSeenAt.Equal(legacyFirstSeen) {
		t.Fatalf("FirstSeenAt was not preserved for legacy default state: %#v", catalog)
	}
}

// Catalog loading failures.

func TestCatalogDependentManagementActionsSurfaceLoadFailure(t *testing.T) {
	host := newMockHostCall(nil, nil)
	previousHostCall := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostAuthList {
			return nil, errors.New("catalog backend unavailable")
		}
		return host.call(method, payload)
	}
	t.Cleanup(func() { hostCall = previousHostCall })

	cfg := appconfig.DefaultConfig()
	cfg.Enabled = false
	cfg.StateFile = filepath.Join(t.TempDir(), "state", "state.json")
	rawConfig, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin := NewPlugin()
	if errConfigure := plugin.Configure(rawConfig); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(plugin.Shutdown)

	for _, action := range []string{"overview", "accounts.list"} {
		response := actionManagementForTest(t, plugin, action, map[string]any{
			"page": 1, "page_size": 50, "summary": true,
		})
		if response.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want %d; body=%s", action, response.StatusCode, http.StatusServiceUnavailable, response.Body)
		}
		if !strings.Contains(string(response.Body), `"code":"catalog_loading"`) {
			t.Fatalf("%s did not expose catalog_loading: %s", action, response.Body)
		}
		if response.Headers.Get("Retry-After") != "1" {
			t.Fatalf("%s Retry-After = %q, want 1", action, response.Headers.Get("Retry-After"))
		}
	}
}

// Large-catalog management.

func TestLargeCatalogManagementAvoidsPerAccountHostReads(t *testing.T) {
	const authCount = 3000
	entries := make([]pluginapi.HostAuthFileEntry, 0, authCount)
	auths := make(map[string]json.RawMessage, authCount)
	for index := 0; index < authCount; index++ {
		authID := fmt.Sprintf("auth-%04d", index)
		entries = append(entries, pluginapi.HostAuthFileEntry{
			ID:        authID,
			AuthIndex: authID,
			Name:      authID,
			Type:      "xai",
			Provider:  "xai",
			Label:     authID + "@example.com",
			Email:     authID + "@example.com",
			Priority:  1,
		})
		auths[authID] = makeAuthJSON(t, map[string]any{
			"type":          "xai",
			"email":         authID + "@example.com",
			"access_token":  "access-" + authID,
			"refresh_token": "refresh-" + authID,
			"disabled":      false,
		})
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	host.resetCatalogCallCounts()

	response := actionManagementForTest(t, plugin, "accounts.list", map[string]any{
		"page":      1,
		"page_size": 10000,
		"summary":   true,
	})
	if response.StatusCode != 200 {
		t.Fatalf("accounts.list status = %d body=%s", response.StatusCode, response.Body)
	}
	t.Logf("3000-account summary response bytes: %d", len(response.Body))
	var decoded struct {
		Data struct {
			Items []managementapp.AccountSummaryView `json:"items"`
			Total int                                `json:"total"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &decoded); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if decoded.Data.Total != authCount || len(decoded.Data.Items) != authCount {
		t.Fatalf("unexpected account page: total=%d items=%d", decoded.Data.Total, len(decoded.Data.Items))
	}

	idsResponse := actionManagementForTest(t, plugin, "accounts.list", map[string]any{
		"page":      1,
		"page_size": 10000,
		"ids_only":  true,
	})
	if idsResponse.StatusCode != 200 {
		t.Fatalf("accounts.list ids_only status = %d body=%s", idsResponse.StatusCode, idsResponse.Body)
	}
	var idsDecoded struct {
		Data struct {
			Items []string `json:"items"`
			Total int      `json:"total"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(idsResponse.Body, &idsDecoded); errUnmarshal != nil {
		t.Fatalf("decode ids_only response: %v", errUnmarshal)
	}
	firstID := ""
	if len(idsDecoded.Data.Items) > 0 {
		firstID = idsDecoded.Data.Items[0]
	}
	if idsDecoded.Data.Total != authCount || len(idsDecoded.Data.Items) != authCount || firstID != "auth-0000" {
		t.Fatalf("unexpected ids_only page: total=%d items=%d first=%q", idsDecoded.Data.Total, len(idsDecoded.Data.Items), firstID)
	}
	if len(idsResponse.Body)*3 >= len(response.Body) {
		t.Fatalf("ids_only response is not materially smaller: ids=%d summary=%d", len(idsResponse.Body), len(response.Body))
	}

	allIDsResponse := actionManagementForTest(t, plugin, "accounts.list", map[string]any{
		"page":      1,
		"page_size": 5,
		"ids_only":  true,
		"all_ids":   true,
	})
	if allIDsResponse.StatusCode != 200 {
		t.Fatalf("accounts.list all_ids status = %d body=%s", allIDsResponse.StatusCode, allIDsResponse.Body)
	}
	var allIDsDecoded struct {
		Data struct {
			Items []string `json:"items"`
			Total int      `json:"total"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(allIDsResponse.Body, &allIDsDecoded); errUnmarshal != nil {
		t.Fatalf("decode all_ids response: %v", errUnmarshal)
	}
	lastID := ""
	if len(allIDsDecoded.Data.Items) > 0 {
		lastID = allIDsDecoded.Data.Items[len(allIDsDecoded.Data.Items)-1]
	}
	if allIDsDecoded.Data.Total != authCount || len(allIDsDecoded.Data.Items) != authCount || lastID != "auth-2999" {
		t.Fatalf("unexpected all_ids response: total=%d items=%d last=%q", allIDsDecoded.Data.Total, len(allIDsDecoded.Data.Items), lastID)
	}

	overview := actionManagementForTest(t, plugin, "overview", nil)
	if overview.StatusCode != 200 {
		t.Fatalf("overview status = %d body=%s", overview.StatusCode, overview.Body)
	}
	if errUpdate := plugin.stateStore().UpdateAccount("auth-0000", func(account *appstate.AccountState) (*appstate.Event, error) {
		account.State = appstate.StateHealthy
		return nil, nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	overviewAfterUpdate := actionManagementForTest(t, plugin, "overview", nil)
	if overviewAfterUpdate.StatusCode != 200 {
		t.Fatalf("overview after state update status = %d body=%s", overviewAfterUpdate.StatusCode, overviewAfterUpdate.Body)
	}
	var overviewDecoded struct {
		Data struct {
			States map[string]int `json:"states"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(overviewAfterUpdate.Body, &overviewDecoded); errUnmarshal != nil {
		t.Fatalf("decode overview: %v", errUnmarshal)
	}
	if overviewDecoded.Data.States[appstate.StateHealthy] != 1 || overviewDecoded.Data.States[appstate.StateUnknown] != authCount-1 {
		t.Fatalf("overview cache was not invalidated by state revision: %#v", overviewDecoded.Data.States)
	}
	listCalls, modelCalls, getCalls := host.catalogCallCounts()
	if listCalls != 0 || modelCalls != 0 || getCalls != 0 {
		t.Fatalf("management requests used host catalog calls: list=%d models=%d get=%d", listCalls, modelCalls, getCalls)
	}

	if errRefresh := plugin.RefreshCatalog(t.Context()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	listCalls, modelCalls, getCalls = host.catalogCallCounts()
	if listCalls != 1 || modelCalls != 0 || getCalls != 0 {
		t.Fatalf("unchanged refresh used extra host calls: list=%d models=%d get=%d", listCalls, modelCalls, getCalls)
	}
}

func TestAccountListSummaryOmitsDetailFields(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := plugin.ensureStateAccount(state, "auth-1")
		account.LastEvidence = &appstate.QualityEvidence{Class: qualitypkg.ClassMissingThinking, Reason: "quality detail"}
		account.LastProbeAt = time.Now().UTC()
		account.SoftStrikes = 2
		account.Metrics.FirstTokenMS = 123
		account.Metrics.VisibleTokens = 456
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	response := actionManagementForTest(t, plugin, "accounts.list", map[string]any{"summary": true})
	if response.StatusCode != 200 {
		t.Fatalf("accounts.list status = %d body=%s", response.StatusCode, response.Body)
	}
	t.Logf("3000-account summary response bytes: %d", len(response.Body))
	text := string(response.Body)
	for _, detail := range []string{"\"evidence\"", "\"last_probe_at\"", "\"soft_strikes\"", "\"ttft_ms\"", "\"visible_tokens\""} {
		if strings.Contains(text, detail) {
			t.Fatalf("summary response leaked detail field %s: %s", detail, text)
		}
	}
	if !strings.Contains(text, "alice@example.com") || !strings.Contains(text, "\"state\"") {
		t.Fatalf("summary response missing expected fields: %s", text)
	}
}
