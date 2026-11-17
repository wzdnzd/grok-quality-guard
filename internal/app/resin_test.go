// Resin tests cover configuration, local/remote bindings and lease cleanup.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	appauth "grok-quality-guard/internal/app/auth"
	proxyapp "grok-quality-guard/internal/app/proxy"
	appstate "grok-quality-guard/internal/app/state"
	apptasks "grok-quality-guard/internal/app/tasks"
	appconfig "grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
	qualitypkg "grok-quality-guard/internal/quality"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func seedResinState(t *testing.T, plugin *Plugin, account string, generation int64) {
	t.Helper()
	_, _, raw, err := plugin.ResolveAuth(context.Background(), "auth-1")
	if err != nil {
		t.Fatal(err)
	}
	current := appauth.RawString(raw, "proxy_url")
	settings := plugin.ResinConfig()
	lease := &proxy.LeaseRef{Provider: "resin-local", Account: account}
	if !settings.LocalOnly {
		parsed, err := url.Parse(current)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.User != nil {
			settings.ProxyToken, _ = parsed.User.Password()
			username := parsed.User.Username()
			if dot := strings.LastIndexByte(username, '.'); dot >= 0 {
				settings.Platform = username[:dot]
			}
		}
		if resinCleanupConfigured(settings) {
			id, err := plugin.stateStore().EnsureResinSnapshot(settings)
			if err != nil {
				t.Fatal(err)
			}
			lease = &proxy.LeaseRef{Provider: "resin", Account: account, PlatformID: settings.PlatformID, SnapshotID: id}
		}
	}
	if err := plugin.stateStore().Update(func(state *appstate.State) error {
		a := state.Accounts["auth-1"]
		a.Resin = appstate.ResinState{Managed: true, Account: account, Generation: generation}
		a.Proxy = proxy.Binding{AuthID: "auth-1", SourceID: resinSourceID, SourceKind: proxy.SourceResin, SourceRevision: 1, Generation: uint64(generation), EndpointHash: proxyURLHash(current), Lease: lease}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func failResinStateWrites(t *testing.T, plugin *Plugin) {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "state-blocker")
	if errWrite := os.WriteFile(blocker, []byte("blocker"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := plugin.stateStore()
	originalPath := store.Path()
	store.SetPathForTest(filepath.Join(blocker, "state.json"))
	t.Cleanup(func() {
		store.SetPathForTest(originalPath)
	})
}

func newResinConcurrencyServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var active atomic.Int32
	var maxActive atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maxActive.Load()
			if current <= observed || maxActive.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		switch request.Method {
		case http.MethodGet:
			if request.URL.Path != "/api/v1/platforms/"+testResinPlatformID {
				writer.WriteHeader(http.StatusNotFound)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"` + testResinPlatformID + `","name":"Default"}`))
		case http.MethodDelete:
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return server, &maxActive
}

func TestBindResinPreservesTokensAndMasksResult(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("RESIN_PROXY_TOKEN", "proxy-secret")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer admin-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.URL.Path != "/api/v1/platforms/"+testResinPlatformID {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"` + testResinPlatformID + `","name":"Canonical"}`))
	}))
	defer server.Close()
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Canonical"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
		cfg.Resin.ProxyTemplate = "socks5h://{platform}.{account}:{proxy_token}@resin:2260"
	})
	result, errBind := plugin.BindResin(context.Background(), "auth-1", false)
	if errBind != nil {
		t.Fatal(errBind)
	}
	if !regexp.MustCompile(`^grok-alice-[0-9a-z]{12}$`).MatchString(result.Account) {
		t.Fatalf("unexpected resin account %q", result.Account)
	}
	if strings.Contains(result.ProxyURL, "proxy-secret") || strings.Contains(result.ProxyURL, "alice") {
		t.Fatalf("bind result leaked proxy credentials: %q", result.ProxyURL)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if appauth.RawString(saved, "access_token") != "access-auth-1" || appauth.RawString(saved, "refresh_token") != "refresh-auth-1" {
		t.Fatal("token fields were not preserved")
	}
	proxy := appauth.RawString(saved, "proxy_url")
	if !strings.Contains(proxy, "Canonical."+result.Account) || !strings.Contains(proxy, "proxy-secret") {
		t.Fatalf("saved proxy URL did not use canonical platform name: %q", proxy)
	}
	if strings.Contains(proxy, "Stale.") {
		t.Fatalf("saved proxy URL used stale configured platform name: %q", proxy)
	}
	stateAccount, okState := plugin.stateAccount("auth-1")
	if !okState || !stateAccount.Resin.Managed || stateAccount.Resin.Account != result.Account || stateAccount.Resin.Generation != 1 {
		t.Fatalf("unexpected resin state: %#v", stateAccount)
	}
	assertNoGuardMetadata(t, saved)
}

func TestBindResinRefusesForeignProxy(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{
		"proxy_url": "socks5h://user:pass@example.com:1080",
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.Resin.Enabled = true })
	result, errBind := plugin.BindResin(context.Background(), "auth-1", false)
	if errBind == nil || !result.Conflict {
		t.Fatalf("expected proxy conflict, got result=%#v err=%v", result, errBind)
	}
	if len(host.saves) != 0 {
		t.Fatal("foreign proxy was overwritten")
	}
}

func TestRotateResinQueuesLeaseDeleteAndWritesNewAccount(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("RESIN_PROXY_TOKEN", "proxy-secret")
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer admin-secret" {
			t.Errorf("authorization header not forwarded")
		}
		if request.Method != http.MethodDelete {
			t.Errorf("method = %s", request.Method)
			return
		}
		deleted = append(deleted, request.URL.Path)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{
		"proxy_url": oldProxy,
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Canonical"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
	})
	seedResinState(t, plugin, "grok-alice-old1", 1)
	if errRotate := plugin.RotateResin(context.Background(), "auth-1"); errRotate != nil {
		t.Fatal(errRotate)
	}
	if len(deleted) != 0 {
		t.Fatalf("rotation called the Resin admin API synchronously: %#v", deleted)
	}
	if jobs := plugin.store.Snapshot().ResinLeaseDeletes; len(jobs) != 1 || jobs[0].Account != "grok-alice-old1" {
		t.Fatalf("old lease was not queued for deletion: %#v", jobs)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	stateAccount, okState := plugin.stateAccount("auth-1")
	if !okState || stateAccount.Resin.Account == "grok-alice-old1" || stateAccount.Resin.Generation != 2 {
		t.Fatalf("resin account was not rotated: %#v", stateAccount)
	}
	proxy := appauth.RawString(saved, "proxy_url")
	if !strings.Contains(proxy, "Canonical."+stateAccount.Resin.Account) {
		t.Fatalf("proxy_url did not use canonical platform name: %q", proxy)
	}
	if strings.Contains(proxy, "Stale.") {
		t.Fatalf("proxy_url used stale platform name: %q", proxy)
	}
	if appauth.RawString(saved, "access_token") != "access-auth-1" || appauth.RawString(saved, "refresh_token") != "refresh-auth-1" {
		t.Fatal("token fields were not preserved during rotation")
	}
	state := plugin.store.Snapshot()
	rotationIndex := -1
	for i := range state.Events {
		if state.Events[i].Action == "resin_rotate" {
			rotationIndex = i
			break
		}
	}
	if rotationIndex < 0 {
		t.Fatal("resin rotation event was not recorded")
	}
	wantDetail := "旧租约：grok-alice-old1 → 新租约：" + stateAccount.Resin.Account + "，旧租约删除已入队"
	if !strings.Contains(state.Events[rotationIndex].Detail, "delete_queued") {
		t.Fatalf("rotation event detail = %q, want %q", state.Events[rotationIndex].Detail, wantDetail)
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != 1 {
		t.Fatalf("processed deletes = %d, want 1", processed)
	}
	if len(deleted) != 1 || !strings.Contains(deleted[0], "grok-alice-old1") {
		t.Fatalf("unexpected deleted paths: %#v", deleted)
	}
	if jobs := plugin.store.Snapshot().ResinLeaseDeletes; len(jobs) != 0 {
		t.Fatalf("successful delete was not removed from the queue: %#v", jobs)
	}
	assertNoGuardMetadata(t, saved)
}

func TestClearResinProxyQueuesLeaseDeleteAndClearsAuth(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("RESIN_PROXY_TOKEN", "")
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer admin-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method != http.MethodDelete {
			t.Errorf("method = %s", request.Method)
			return
		}
		deleted = append(deleted, request.URL.Path)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": oldProxy,
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Default"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
	})
	seedResinState(t, plugin, "grok-alice-old1", 2)
	result, errClear := plugin.ClearResinProxy(context.Background(), "auth-1")
	if errClear != nil {
		t.Fatal(errClear)
	}
	if !result.Cleared || !result.RemoteLeaseDeleteQueued || result.Account != "grok-alice-old1" {
		t.Fatalf("unexpected clear result: %#v", result)
	}
	if strings.Contains(result.ProxyURL, "proxy-secret") || strings.Contains(result.ProxyURL, "alice") {
		t.Fatalf("clear result leaked proxy credentials: %q", result.ProxyURL)
	}
	if len(deleted) != 0 {
		t.Fatalf("clear called the Resin admin API synchronously: %#v", deleted)
	}
	if jobs := plugin.store.Snapshot().ResinLeaseDeletes; len(jobs) != 1 || jobs[0].Account != "grok-alice-old1" {
		t.Fatalf("old lease was not queued for deletion: %#v", jobs)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if appauth.RawString(saved, "proxy_url") != "" {
		t.Fatalf("proxy_url was not cleared: %q", appauth.RawString(saved, "proxy_url"))
	}
	assertNoGuardMetadata(t, saved)
	if appauth.RawString(saved, "access_token") != "access-auth-1" || appauth.RawString(saved, "refresh_token") != "refresh-auth-1" {
		t.Fatal("token fields were not preserved")
	}
	stateAccount, okState := plugin.stateAccount("auth-1")
	if !okState || stateAccount.Resin.Managed || stateAccount.Resin.Account != "" || stateAccount.Resin.Generation != 2 {
		t.Fatalf("unexpected state after clear: %#v", stateAccount)
	}
	state := plugin.store.Snapshot()
	foundEvent := false
	for _, event := range state.Events {
		if event.Action == "resin_clear" && event.Result == "cleared" && strings.Contains(event.Detail, "remote_lease=delete_queued") {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatal("resin clear event was not recorded")
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != 1 {
		t.Fatalf("processed deletes = %d, want 1", processed)
	}
	if len(deleted) != 1 || !strings.Contains(deleted[0], "grok-alice-old1") {
		t.Fatalf("unexpected deleted paths: %#v", deleted)
	}
}

func TestClearResinProxyDoesNotInferOwnershipFromSavedConfig(t *testing.T) {
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer admin-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method != http.MethodDelete {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		deleted = append(deleted, request.URL.Path)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": oldProxy,
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		state.Resin = appstate.ResinSettings{
			Configured:    true,
			Enabled:       true,
			Platform:      "Default",
			PlatformID:    testResinPlatformID,
			AdminURL:      server.URL,
			AdminToken:    "admin-secret",
			ProxyToken:    "proxy-secret",
			ProxyTemplate: "socks5h://{platform}.{account}:{proxy_token}@resin:2260",
			ValidatedAt:   time.Now().UTC(),
		}
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	result, errClear := plugin.ClearResinProxy(context.Background(), "auth-1")
	if errClear != nil {
		t.Fatal(errClear)
	}
	if !result.Cleared || result.RemoteLeaseDeleteQueued || result.Account != "" {
		t.Fatalf("unexpected inferred clear result: %#v", result)
	}
	if len(deleted) != 0 {
		t.Fatalf("clear called the Resin admin API synchronously: %#v", deleted)
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != 0 {
		t.Fatalf("processed deletes = %d, want 0", processed)
	}
	if len(deleted) != 0 {
		t.Fatalf("managed lease was not deleted: %#v", deleted)
	}
}

func TestResinBindSelectedUsesBoundedQueue(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("RESIN_PROXY_TOKEN", "proxy-secret")
	server, maxActive := newResinConcurrencyServer(t)

	var entries []pluginapi.HostAuthFileEntry
	auths := make(map[string]json.RawMessage)
	authIDs := []any{}
	for index := 1; index <= 4; index++ {
		authID := fmt.Sprintf("auth-%d", index)
		entry, raw := singleGrokAuth(t, authID, fmt.Sprintf("user%d@example.com", index), nil)
		entries = append(entries, entry...)
		for key, value := range raw {
			auths[key] = value
		}
		authIDs = append(authIDs, authID)
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Probe.Concurrency = 2
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Default"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
	})
	response := actionManagementForTest(t, plugin, "resin.bind", map[string]any{"auth_ids": authIDs})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if got := maxActive.Load(); got != 0 {
		t.Fatalf("normal generation must not call the Resin API; max concurrent requests = %d", got)
	}
}

func TestResinBindUnboundUsesBoundedQueue(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("RESIN_PROXY_TOKEN", "proxy-secret")
	server, maxActive := newResinConcurrencyServer(t)

	var entries []pluginapi.HostAuthFileEntry
	auths := make(map[string]json.RawMessage)
	for index := 1; index <= 4; index++ {
		authID := fmt.Sprintf("auth-%d", index)
		entry, raw := singleGrokAuth(t, authID, fmt.Sprintf("user%d@example.com", index), nil)
		entries = append(entries, entry...)
		for key, value := range raw {
			auths[key] = value
		}
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Probe.Concurrency = 2
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Default"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
	})
	response := actionManagementForTest(t, plugin, "resin.bind_unbound", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if got := maxActive.Load(); got != 0 {
		t.Fatalf("normal generation must not call the Resin API; max concurrent requests = %d", got)
	}
}

func TestResinClearSelectedQueuesLeaseDeletes(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("RESIN_PROXY_TOKEN", "proxy-secret")
	server, maxActive := newResinConcurrencyServer(t)

	var entries []pluginapi.HostAuthFileEntry
	auths := make(map[string]json.RawMessage)
	authIDs := []any{}
	for index := 1; index <= 4; index++ {
		authID := fmt.Sprintf("auth-%d", index)
		account := fmt.Sprintf("grok-alice-%d", index)
		entry, raw := singleGrokAuth(t, authID, fmt.Sprintf("user%d@example.com", index), map[string]any{
			"proxy_url": "socks5h://Default." + account + ":proxy-secret@resin:2260",
		})
		entries = append(entries, entry...)
		for key, value := range raw {
			auths[key] = value
		}
		authIDs = append(authIDs, authID)
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Probe.Concurrency = 2
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Default"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
	})
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		for index, authID := range authIDs {
			accountID := authID.(string)
			account := state.Accounts[accountID]
			if account == nil {
				account = &appstate.AccountState{AuthID: accountID, State: appstate.StateUnknown}
				state.Accounts[accountID] = account
			}
			account.Resin = appstate.ResinState{Managed: true, Account: fmt.Sprintf("grok-alice-%d", index+1), Generation: 1}
		}
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	response := actionManagementForTest(t, plugin, "resin.clear", map[string]any{"auth_ids": authIDs})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if got := maxActive.Load(); got != 0 {
		t.Fatalf("clear called the Resin admin API synchronously: %d", got)
	}
	if jobs := plugin.store.Snapshot().ResinLeaseDeletes; len(jobs) != len(authIDs) {
		t.Fatalf("queued deletes = %d, want %d: %#v", len(jobs), len(authIDs), jobs)
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != len(authIDs) {
		t.Fatalf("processed deletes = %d, want %d", processed, len(authIDs))
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent lease deletes = %d, want 1", got)
	}
	if jobs := plugin.store.Snapshot().ResinLeaseDeletes; len(jobs) != 0 {
		t.Fatalf("successful deletes were not removed from the queue: %#v", jobs)
	}
}

func TestClearExternalProxyDoesNotRequireResin(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://user:external-secret@example.com:1080",
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	result, errClear := plugin.ClearResinProxy(context.Background(), "auth-1")
	if errClear != nil {
		t.Fatal(errClear)
	}
	if !result.Cleared || result.RemoteLeaseDeleteQueued || result.Account != "" {
		t.Fatalf("unexpected clear result: %#v", result)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if appauth.RawString(saved, "proxy_url") != "" {
		t.Fatalf("external proxy_url was not cleared: %q", appauth.RawString(saved, "proxy_url"))
	}
	if appauth.RawString(saved, "access_token") != "access-auth-1" {
		t.Fatal("externally clear removed token fields")
	}
}

func TestClearManagedProxyLeaseFailureQueuesRetryAndClearsAuth(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("RESIN_PROXY_TOKEN", "proxy-secret")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260",
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Default"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)
	result, errClear := plugin.ClearResinProxy(context.Background(), "auth-1")
	if errClear != nil {
		t.Fatal(errClear)
	}
	if !result.Cleared || !result.RemoteLeaseDeleteQueued {
		t.Fatalf("unexpected clear result: %#v", result)
	}
	if len(host.saves) != 1 {
		t.Fatalf("expected local auth clear save, got %d", len(host.saves))
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != 1 {
		t.Fatalf("processed deletes = %d, want 1", processed)
	}
	state := plugin.store.Snapshot()
	if len(state.ResinLeaseDeletes) != 1 {
		t.Fatalf("failed delete was not retained for retry: %#v", state.ResinLeaseDeletes)
	}
	job := state.ResinLeaseDeletes[0]
	if job.Attempts != 1 || job.NextAttemptAt.IsZero() || job.LastError == "" {
		t.Fatalf("unexpected retry state: %#v", job)
	}
}

func TestManagedResinBindingPrefersProxyAccountOverStaleState(t *testing.T) {
	raw, errDecode := rawAuthToMap(makeAuthJSON(t, map[string]any{
		"proxy_url": "socks5h://Default.grok-alice-new1:proxy-secret@resin:2260",
	}))
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	account, managed := managedResinBinding(raw, appstate.AccountState{
		Resin: appstate.ResinState{Managed: true, Account: "grok-alice-old1"},
	}, appstate.ResinSettings{})
	if !managed || account != "grok-alice-new1" {
		t.Fatalf("managed binding = %q, %t; want proxy account", account, managed)
	}
}

func TestResinOperationsAbortWhenPrepareCannotPersist(t *testing.T) {
	for _, action := range []string{"rotate", "clear"} {
		t.Run(action, func(t *testing.T) {
			oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
			entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{"proxy_url": oldProxy})
			host := newMockHostCall(entries, auths)
			plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.Resin.Enabled = true; cfg.Resin.LocalOnly = true })
			seedResinState(t, plugin, "grok-alice-old1", 0)
			failResinStateWrites(t, plugin)
			var err error
			if action == "rotate" {
				err = plugin.RotateResin(context.Background(), "auth-1")
			} else {
				_, err = plugin.ClearResinProxy(context.Background(), "auth-1")
			}
			if err == nil || len(host.saves) != 0 {
				t.Fatalf("failed prepare must preserve auth: err=%v saves=%d", err, len(host.saves))
			}
		})
	}
}

func TestRotateResinLocalOnlyRewritesProxyWithoutAPI(t *testing.T) {
	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": oldProxy,
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)
	if errRotate := plugin.RotateResin(context.Background(), "auth-1"); errRotate != nil {
		t.Fatal(errRotate)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	stateAccount, okState := plugin.stateAccount("auth-1")
	if !okState || !stateAccount.Resin.Managed || stateAccount.Resin.Account == "" || stateAccount.Resin.Account == "grok-alice-old1" || stateAccount.Resin.Generation != 1 {
		t.Fatalf("unexpected local rotation state: %#v", stateAccount)
	}
	rotatedProxy := appauth.RawString(saved, "proxy_url")
	if !strings.Contains(rotatedProxy, "Default."+stateAccount.Resin.Account) || !strings.Contains(rotatedProxy, "proxy-secret") {
		t.Fatalf("local rotation did not preserve platform and password: %q", rotatedProxy)
	}
	assertNoGuardMetadata(t, saved)
	state := plugin.store.Snapshot()
	foundEvent := false
	for _, event := range state.Events {
		if event.Action == "resin_rotate" && strings.Contains(event.Detail, "未调用 Resin API") {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatal("local rotation event was not recorded")
	}
}

func TestClearResinProxyLocalOnlySkipsRemoteLease(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260",
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)
	result, errClear := plugin.ClearResinProxy(context.Background(), "auth-1")
	if errClear != nil {
		t.Fatal(errClear)
	}
	if !result.Cleared || result.RemoteLeaseDeleteQueued {
		t.Fatalf("unexpected local clear result: %#v", result)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if appauth.RawString(saved, "proxy_url") != "" {
		t.Fatalf("proxy_url was not cleared: %q", appauth.RawString(saved, "proxy_url"))
	}
	if appauth.RawString(saved, "access_token") != "access-auth-1" {
		t.Fatal("local clear removed token fields")
	}
	foundEvent := false
	for _, event := range plugin.store.Snapshot().Events {
		if event.Action == "resin_clear" && strings.Contains(event.Detail, "remote_lease=skipped_local_only") {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatal("local clear event did not report skipped remote lease")
	}
}

func TestRotateAfterFailureLocalOnlyRewritesProxy(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260",
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
		cfg.Resin.AutoRotateAfterQualityFailure = true
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)
	rotated, errRotate := plugin.rotateAfterFailure(context.Background(), "auth-1", qualitypkg.ClassMissingThinking)
	if errRotate != nil {
		t.Fatal(errRotate)
	}
	if !rotated {
		t.Fatal("local auto-rotation did not run")
	}
	if len(host.saves) != 1 {
		t.Fatalf("expected one auth save, got %d", len(host.saves))
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	stateAccount, okState := plugin.stateAccount("auth-1")
	if !okState || !stateAccount.Resin.Managed || stateAccount.Resin.Account == "" || stateAccount.Resin.Account == "grok-alice-old1" {
		t.Fatalf("unexpected auto-rotation state: %#v", stateAccount)
	}
	assertNoGuardMetadata(t, saved)
	if appauth.RawString(saved, "access_token") != "access-auth-1" {
		t.Fatal("auto-rotation removed token fields")
	}
}

func TestTransparentResinRotationRetriesSameAuthWithNewProxy(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260",
	})
	host := newMockHostCall(entries, auths)
	host.scriptStreamSequence("auth-1", badStreamScript(), goodStreamScript())
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.AutoAction = "cooldown_only"
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
		cfg.Resin.AutoRotateAfterQualityFailure = true
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)

	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-rotation-success")

	if len(host.modelCalls) != 2 {
		t.Fatalf("expected two upstream attempts, got %d", len(host.modelCalls))
	}
	firstProxy := host.modelCalls[0].ProxyURL
	secondProxy := host.modelCalls[1].ProxyURL
	if firstProxy == secondProxy {
		t.Fatalf("retry reused the old proxy: %q", firstProxy)
	}
	if !strings.Contains(secondProxy, "grok-email-") || strings.Contains(secondProxy, "grok-alice-old1") {
		t.Fatalf("retry did not use the rotated lease: %q", secondProxy)
	}
	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateHealthy {
		t.Fatalf("account should stay healthy after rotated retry: %#v", account)
	}
	if len(host.saves) != 1 {
		t.Fatalf("expected only the rotation auth save, got %d", len(host.saves))
	}
	rotations := 0
	disables := 0
	for _, event := range plugin.stateStore().Snapshot().Events {
		switch event.Action {
		case "resin_rotate":
			rotations++
		case "guard_disable":
			disables++
		}
	}
	if rotations != 1 || disables != 0 {
		t.Fatalf("unexpected rotation/disable events: rotations=%d disables=%d", rotations, disables)
	}
}

func TestTransparentResinRotationCoolsAfterRetryFails(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260",
	})
	host := newMockHostCall(entries, auths)
	host.scriptStreamSequence("auth-1", badStreamScript(), badStreamScript())
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.AutoAction = "cooldown_only"
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
		cfg.Resin.AutoRotateAfterQualityFailure = true
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)

	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-rotation-failure")

	if len(host.modelCalls) != 2 {
		t.Fatalf("expected two upstream attempts, got %d", len(host.modelCalls))
	}
	firstProxy := host.modelCalls[0].ProxyURL
	secondProxy := host.modelCalls[1].ProxyURL
	if firstProxy == secondProxy {
		t.Fatalf("retry reused the old proxy: %q", firstProxy)
	}
	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateCooling || account.DisableKind != appstate.DisableKindCooldown {
		t.Fatalf("account was not cooled after rotated retry failed: %#v", account)
	}
	if account.CooldownUntil.IsZero() {
		t.Fatal("cooldown deadline was not set")
	}
	rotateIndex := -1
	disableIndex := -1
	events := plugin.stateStore().Snapshot().Events
	for index, event := range events {
		if event.Action == "resin_rotate" && rotateIndex < 0 {
			rotateIndex = index
		}
		if event.Action == "guard_disable" && disableIndex < 0 {
			disableIndex = index
		}
	}
	if rotateIndex < 0 || disableIndex < 0 || rotateIndex > disableIndex {
		t.Fatalf("rotation must precede cooldown: events=%#v", events)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if !appauth.RawBool(saved, "disabled") {
		t.Fatal("cooldown did not persist disabled=true")
	}
}

func TestBindResinRejectsLocalOnlyMode(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
	})
	result, errBind := plugin.BindResin(context.Background(), "auth-1", false)
	if errBind == nil || result.Error == "" {
		t.Fatalf("expected local-only bind rejection, result=%#v err=%v", result, errBind)
	}
	if len(host.saves) != 0 {
		t.Fatal("local-only bind wrote auth")
	}
}

func TestBindUnboundResinRejectsLocalOnlyMode(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
	})
	results, errBind := plugin.BindUnboundResin(context.Background(), false)
	if errBind == nil || results != nil {
		t.Fatalf("expected local-only bind-unbound rejection, results=%#v err=%v", results, errBind)
	}
}

func TestRewriteResinProxyAccountPreservesURL(t *testing.T) {
	rotated, errRotate := rewriteResinProxyAccount("socks5h://Default.grok-alice-old1:p%40ss@resin:2260/socks", "grok-alice-new1")
	if errRotate != nil {
		t.Fatal(errRotate)
	}
	parsed, errParse := url.Parse(rotated)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if parsed.Scheme != "socks5h" || parsed.Host != "resin:2260" || parsed.Path != "/socks" {
		t.Fatalf("proxy endpoint changed: %q", rotated)
	}
	password, okPassword := parsed.User.Password()
	if parsed.User.Username() != "Default.grok-alice-new1" || !okPassword || password != "p@ss" {
		t.Fatalf("proxy credentials changed: %q", rotated)
	}
}

func TestRewriteResinProxyAccountRejectsUnsupportedUsername(t *testing.T) {
	cases := []string{
		"socks5h://user:pass@resin:2260",
		"socks5h://user@resin:2260",
		"socks5h://Default.account:pass@",
	}
	for _, proxyURL := range cases {
		if _, errRotate := rewriteResinProxyAccount(proxyURL, "grok-new1"); errRotate == nil {
			t.Fatalf("expected invalid proxy error for %q", proxyURL)
		}
	}
}

func TestDeleteResinLeaseStatusHandling(t *testing.T) {
	cases := []struct {
		status  int
		wantErr bool
	}{
		{status: http.StatusNoContent, wantErr: false},
		{status: http.StatusNotFound, wantErr: false},
		{status: http.StatusUnauthorized, wantErr: true},
		{status: http.StatusTooManyRequests, wantErr: true},
		{status: http.StatusInternalServerError, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(tc.status)
			}))
			defer server.Close()
			errDelete := deleteResinLease(context.Background(), server.URL, "platform-1", "token", "account")
			if (errDelete != nil) != tc.wantErr {
				t.Fatalf("delete error = %v, wantErr %v", errDelete, tc.wantErr)
			}
			if typed, ok := errDelete.(*proxyapp.ResinDeleteError); ok && typed.Status != tc.status {
				t.Fatalf("error status = %d, want %d", typed.Status, tc.status)
			}
		})
	}
}

func TestRotateResinQueuesAdminAuthFailureForRetry(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("RESIN_PROXY_TOKEN", "proxy-secret")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{
		"proxy_url": "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260",
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Default"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)
	if errRotate := plugin.RotateResin(context.Background(), "auth-1"); errRotate != nil {
		t.Fatal(errRotate)
	}
	if len(host.saves) != 1 {
		t.Fatalf("expected one auth save, got %d", len(host.saves))
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != 1 {
		t.Fatalf("processed deletes = %d, want 1", processed)
	}
	jobs := plugin.store.Snapshot().ResinLeaseDeletes
	if len(jobs) != 1 || jobs[0].Attempts != 1 || jobs[0].NextAttemptAt.IsZero() {
		t.Fatalf("admin auth failure was not retained for retry: %#v", jobs)
	}
}

func TestRandomBase36Format(t *testing.T) {
	pattern := regexp.MustCompile(`^[0-9a-z]{4}$`)
	seen := make(map[string]struct{})
	for i := 0; i < 32; i++ {
		value, errValue := randomBase36(4)
		if errValue != nil {
			t.Fatal(errValue)
		}
		if !pattern.MatchString(value) {
			t.Fatalf("invalid base36 value %q", value)
		}
		seen[value] = struct{}{}
	}
	if len(seen) < 16 {
		t.Fatalf("random suffixes show excessive collisions: %d unique", len(seen))
	}
}

func TestMaskProxyURLDropsCredentialsAndPath(t *testing.T) {
	masked := maskProxyURL("socks5h://user:pass@example.com:1080/secret/path?token=abc")
	if masked != "socks5h://us***r:******@example.com:1080" {
		t.Fatalf("masked proxy = %q", masked)
	}
}

func TestResinBindResultJSONDoesNotContainToken(t *testing.T) {
	result := proxyapp.ResinBindResult{AuthID: "auth-1", Account: "grok-alice-abcd", ProxyURL: maskProxyURL("socks5h://u:secret@example.com:1080")}
	encoded, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("serialized result leaked secret: %s", encoded)
	}
}

const testResinPlatformID = "11111111-1111-1111-1111-111111111111"

func TestResinConfigGetMasksSecretsAndUsesEnvironmentFallback(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	t.Setenv("RESIN_PROXY_TOKEN", "proxy-secret")
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Platform = "Default"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = "http://resin.test"
	})
	response := actionManagementForTest(t, plugin, "resin.config.get", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	text := string(response.Body)
	for _, forbidden := range []string{"admin-secret", "proxy-secret"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("config response leaked %q: %s", forbidden, text)
		}
	}
	var decoded struct {
		OK   bool                     `json:"ok"`
		Data proxyapp.ResinConfigView `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &decoded); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if decoded.Data.Configured || !decoded.Data.AdminTokenSet || !decoded.Data.ProxyTokenSet {
		t.Fatalf("unexpected config view: %#v", decoded.Data)
	}
}

func TestResinConfigSaveLocalOnlyWithoutAPI(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	params := map[string]any{
		"enabled":                           true,
		"name":                              "Primary Resin",
		"local_only":                        true,
		"platform":                          "Default",
		"platform_id":                       testResinPlatformID,
		"admin_url":                         "http://resin.local",
		"admin_token":                       "admin-secret",
		"proxy_token":                       "proxy-secret",
		"proxy_template":                    "socks5h://{platform}.{account}:{proxy_token}@resin:2260",
		"auto_rotate_after_quality_failure": true,
	}
	response := actionManagementForTest(t, plugin, "resin.config.save", params)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	state := plugin.store.Snapshot()
	if !state.Resin.Configured || !state.Resin.Enabled || !state.Resin.LocalOnly || state.Resin.Name != "Primary Resin" {
		t.Fatalf("local resin config was not persisted: %#v", state.Resin)
	}
	if state.Proxy.ResinSource.Name != "Primary Resin" {
		t.Fatalf("resin source name was not persisted: %#v", state.Proxy.ResinSource)
	}
	if state.Resin.PlatformID != "" || state.Resin.AdminURL != "" || state.Resin.AdminToken != "" || state.Resin.ProxyToken != "" {
		t.Fatalf("local resin config persisted API credentials: %#v", state.Resin)
	}
	if state.Resin.ValidatedAt.IsZero() {
		t.Fatal("local resin config did not record activation time")
	}
	settings := plugin.ResinConfig()
	if !settings.LocalOnly || settings.PlatformID != "" || settings.AdminURL != "" || settings.AdminToken != "" || settings.ProxyToken != "" {
		t.Fatalf("local resin settings exposed API fields: %#v", settings)
	}
	var decoded struct {
		OK   bool                     `json:"ok"`
		Data proxyapp.ResinConfigView `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &decoded); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !decoded.Data.LocalOnly || !decoded.Data.Configured || !decoded.Data.Enabled || decoded.Data.Name != "Primary Resin" {
		t.Fatalf("unexpected local config view: %#v", decoded.Data)
	}
	validateResponse := actionManagementForTest(t, plugin, "resin.config.validate", map[string]any{"local_only": true})
	if validateResponse.StatusCode != http.StatusOK || !strings.Contains(string(validateResponse.Body), `"local_only":true`) {
		t.Fatalf("local validation failed: status=%d body=%s", validateResponse.StatusCode, validateResponse.Body)
	}
}

func TestResinConfigValidateDoesNotPersist(t *testing.T) {
	adminServer, socksAddr := newResinValidationServers(t, "admin-secret", "proxy-secret", true)
	defer adminServer.Close()
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	params := validResinParams(adminServer.URL, socksAddr)
	response := actionManagementForTest(t, plugin, "resin.config.validate", params)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	text := string(response.Body)
	for _, forbidden := range []string{"admin-secret", "proxy-secret", socksAddr} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("validation response leaked %q: %s", forbidden, text)
		}
	}
	state := plugin.store.Snapshot()
	if state.Resin.Configured {
		t.Fatalf("validate persisted resin config: %#v", state.Resin)
	}
}

func TestResinConfigSavePersistsValidatedSettingsAndPreservesBlankTokens(t *testing.T) {
	adminServer, socksAddr := newResinValidationServers(t, "admin-secret", "proxy-secret", true)
	defer adminServer.Close()
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	params := validResinParams(adminServer.URL, socksAddr)
	params["platform"] = "default"
	response := actionManagementForTest(t, plugin, "resin.config.save", params)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	state := plugin.store.Snapshot()
	if state.Resin.Platform != "Default" {
		t.Fatalf("saved platform was not canonicalized from platform name: %#v", state.Resin)
	}
	if !state.Resin.Configured || state.Resin.AdminToken != "admin-secret" || state.Resin.ProxyToken != "proxy-secret" {
		t.Fatalf("validated resin config was not persisted: %#v", state.Resin)
	}
	if state.Resin.ValidatedAt.IsZero() {
		t.Fatal("validated_at was not persisted")
	}
	blankTokens := validResinParams(adminServer.URL, socksAddr)
	blankTokens["admin_token"] = ""
	blankTokens["proxy_token"] = ""
	response = actionManagementForTest(t, plugin, "resin.config.save", blankTokens)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("second save status = %d body=%s", response.StatusCode, response.Body)
	}
	state = plugin.store.Snapshot()
	if state.Resin.AdminToken != "admin-secret" || state.Resin.ProxyToken != "proxy-secret" {
		t.Fatalf("blank token fields cleared stored secrets: %#v", state.Resin)
	}
	if strings.Contains(string(response.Body), "admin-secret") || strings.Contains(string(response.Body), "proxy-secret") {
		t.Fatalf("save response leaked secret: %s", response.Body)
	}
}

func TestResinConfigSaveRejectsInvalidAdminTokenWithoutPersisting(t *testing.T) {
	adminServer, socksAddr := newResinValidationServers(t, "admin-secret", "proxy-secret", true)
	defer adminServer.Close()
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	params := validResinParams(adminServer.URL, socksAddr)
	params["admin_token"] = "wrong-secret"
	response := actionManagementForTest(t, plugin, "resin.config.save", params)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if plugin.store.Snapshot().Resin.Configured {
		t.Fatal("invalid admin token was persisted")
	}
	if strings.Contains(string(response.Body), "wrong-secret") || strings.Contains(string(response.Body), "admin-secret") {
		t.Fatalf("error response leaked token: %s", response.Body)
	}
}

func TestResinConfigSaveRejectsInvalidProxyTokenWithoutPersisting(t *testing.T) {
	adminServer, socksAddr := newResinValidationServers(t, "admin-secret", "proxy-secret", false)
	defer adminServer.Close()
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	params := validResinParams(adminServer.URL, socksAddr)
	params["proxy_token"] = "wrong-proxy-secret"
	response := actionManagementForTest(t, plugin, "resin.config.save", params)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if plugin.store.Snapshot().Resin.Configured {
		t.Fatal("invalid proxy token was persisted")
	}
	if strings.Contains(string(response.Body), "wrong-proxy-secret") || strings.Contains(string(response.Body), "proxy-secret") {
		t.Fatalf("error response leaked token: %s", response.Body)
	}
}

func validResinParams(adminURL, socksAddr string) map[string]any {
	return map[string]any{
		"enabled":                           true,
		"platform":                          "Default",
		"platform_id":                       testResinPlatformID,
		"admin_url":                         adminURL,
		"admin_token":                       "admin-secret",
		"proxy_token":                       "proxy-secret",
		"proxy_template":                    "socks5h://{platform}.{account}:{proxy_token}@" + socksAddr,
		"auto_rotate_after_quality_failure": true,
	}
}

func newResinValidationServers(t *testing.T, adminToken, proxyToken string, acceptProxy bool) (*httptest.Server, string) {
	t.Helper()
	adminServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+adminToken {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"error":{"code":"UNAUTHORIZED"}}`))
			return
		}
		switch request.URL.Path {
		case "/api/v1/system/info":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"version":"test"}`))
		case "/api/v1/platforms":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"items":[{"id":"` + testResinPlatformID + `","name":"Default"}],"total":1,"limit":1000,"offset":0}`))
		case "/api/v1/platforms/" + testResinPlatformID:
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"` + testResinPlatformID + `","name":"Default"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	socksAddr := startSocks5AuthServer(t, "Default.grok-validation", proxyToken, acceptProxy)
	return adminServer, socksAddr
}

func startSocks5AuthServer(t *testing.T, expectedUsername, expectedPassword string, accept bool) string {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go handleTestSocks5Auth(conn, expectedUsername, expectedPassword, accept)
		}
	}()
	return listener.Addr().String()
}

func handleTestSocks5Auth(conn net.Conn, expectedUsername, expectedPassword string, accept bool) {
	defer conn.Close()
	request := make([]byte, 3)
	if _, errRead := io.ReadFull(conn, request); errRead != nil {
		return
	}
	_, _ = conn.Write([]byte{0x05, 0x02})
	authHeader := make([]byte, 2)
	if _, errRead := io.ReadFull(conn, authHeader); errRead != nil {
		return
	}
	username := make([]byte, int(authHeader[1]))
	if _, errRead := io.ReadFull(conn, username); errRead != nil {
		return
	}
	passwordLength := make([]byte, 1)
	if _, errRead := io.ReadFull(conn, passwordLength); errRead != nil {
		return
	}
	password := make([]byte, int(passwordLength[0]))
	if _, errRead := io.ReadFull(conn, password); errRead != nil {
		return
	}
	status := byte(0x01)
	if accept && string(username) == expectedUsername && string(password) == expectedPassword {
		status = 0x00
	}
	_, _ = conn.Write([]byte{0x01, status})
}

func TestSocks5AuthenticateRejectsWrongPassword(t *testing.T) {
	socksAddr := startSocks5AuthServer(t, "user", "right-password", false)
	conn, errDial := net.Dial("tcp", socksAddr)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer conn.Close()
	errAuth := socks5Authenticate(conn, "user", "wrong-password")
	if errAuth == nil || !strings.Contains(errAuth.Error(), "rejected") {
		t.Fatalf("unexpected auth error: %v", errAuth)
	}
	if strings.Contains(fmt.Sprint(errAuth), "wrong-password") {
		t.Fatal("auth error leaked password")
	}
}

func TestResinConfigSaveRejectsMalformedAdminURL(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	params := validResinParams("ftp://resin.test", "127.0.0.1:1")
	response := actionManagementForTest(t, plugin, "resin.config.save", params)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if plugin.store.Snapshot().Resin.Configured {
		t.Fatal("malformed config was persisted")
	}
}

func TestResinConfigSaveDoesNotPersistWhenValidationContextCancelled(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	params := validResinParams("http://127.0.0.1:1", "127.0.0.1:1")
	taskValue, status, errSave := plugin.actionResinConfigSave(ctx, params)
	if errSave != nil || status != http.StatusAccepted {
		t.Fatalf("save submission error = %v status=%d", errSave, status)
	}
	task, okTask := taskValue.(apptasks.Task)
	if !okTask {
		t.Fatalf("save did not return a task: %#v", taskValue)
	}
	finished := waitTaskForTest(t, plugin, task.ID)
	if finished.Status != taskStatusFailed || finished.ErrorStatus != http.StatusBadGateway {
		t.Fatalf("save task result = %#v", finished)
	}
	if plugin.store.Snapshot().Resin.Configured {
		t.Fatal("cancelled validation persisted config")
	}
}

func TestValidateResinPlatformReturnsCanonicalName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"` + testResinPlatformID + `","name":"Canonical"}`))
	}))
	defer server.Close()
	settings := appstate.ResinSettings{
		Platform:   "Stale",
		PlatformID: testResinPlatformID,
		AdminURL:   server.URL,
		AdminToken: "admin-secret",
	}
	name, errValidate := validateResinPlatform(context.Background(), settings)
	if errValidate != nil || name != "Canonical" {
		t.Fatalf("name=%q error=%v", name, errValidate)
	}
}

func TestResolveResinPlatformByName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/platforms" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.URL.Query().Get("keyword") != "canonical" {
			t.Fatalf("keyword = %q", request.URL.Query().Get("keyword"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"items":[{"id":"` + testResinPlatformID + `","name":"Canonical"}],"total":1,"limit":1000,"offset":0}`))
	}))
	defer server.Close()
	settings := appstate.ResinSettings{
		Platform:   "canonical",
		AdminURL:   server.URL,
		AdminToken: "admin-secret",
	}
	resolved, errResolve := resolveResinPlatformByName(context.Background(), settings)
	if errResolve != nil {
		t.Fatal(errResolve)
	}
	if resolved.Platform != "Canonical" || resolved.PlatformID != testResinPlatformID {
		t.Fatalf("unexpected resolved platform: %#v", resolved)
	}
}

func TestResinLeaseDeleteBackoff(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: 30 * time.Second},
		{attempts: 1, want: 30 * time.Second},
		{attempts: 2, want: time.Minute},
		{attempts: 3, want: 2 * time.Minute},
		{attempts: 8, want: time.Hour},
		{attempts: 100, want: time.Hour},
	}
	for _, tc := range cases {
		if got := resinLeaseDeleteBackoff(tc.attempts); got != tc.want {
			t.Fatalf("backoff(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}

func TestCatalogRemovalQueuesManagedResinLeaseDelete(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	var deleted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.Header.Get("Authorization") != "Bearer admin-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.URL.Path != "/api/v1/platforms/"+testResinPlatformID+"/leases/grok-alice-old1" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		deleted.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Default"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
	})
	settings := plugin.ResinConfig()
	snapshotID, err := plugin.stateStore().EnsureResinSnapshot(settings)
	if err != nil {
		t.Fatal(err)
	}
	if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
		account.Resin = appstate.ResinState{Managed: true, Account: "grok-alice-old1", Generation: 1}
		account.Proxy = proxy.Binding{AuthID: "auth-1", SourceID: resinSourceID, SourceKind: proxy.SourceResin, Generation: 1, Lease: &proxy.LeaseRef{Provider: "resin", Account: "grok-alice-old1", PlatformID: settings.PlatformID, SnapshotID: snapshotID}}
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

	state := plugin.store.Snapshot()
	if _, exists := state.Accounts["auth-1"]; exists {
		t.Fatalf("removed auth remained in state: %#v", state.Accounts["auth-1"])
	}
	if len(state.ResinLeaseDeletes) != 1 {
		t.Fatalf("queued lease deletes = %d, want 1: %#v", len(state.ResinLeaseDeletes), state.ResinLeaseDeletes)
	}
	job := state.ResinLeaseDeletes[0]
	if job.AuthID != "auth-1" || job.Account != "grok-alice-old1" || job.AdminURL != server.URL || job.PlatformID != testResinPlatformID {
		t.Fatalf("unexpected lease delete job: %#v", job)
	}
	if deleted.Load() != 0 {
		t.Fatalf("catalog refresh deleted the lease synchronously")
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != 1 {
		t.Fatalf("processed lease deletes = %d, want 1", processed)
	}
	if deleted.Load() != 1 {
		t.Fatalf("resin delete calls = %d, want 1", deleted.Load())
	}
	if jobs := plugin.store.Snapshot().ResinLeaseDeletes; len(jobs) != 0 {
		t.Fatalf("successful lease delete remained queued: %#v", jobs)
	}
}

func TestCatalogRemovalSkipsResinLeaseDeleteWithoutAPIConfig(t *testing.T) {
	tests := []struct {
		name       string
		disabled   bool
		localOnly  bool
		clearToken bool
		resin      appstate.ResinState
	}{
		{name: "local only", localOnly: true, resin: appstate.ResinState{Managed: true, Account: "grok-alice-old1"}},
		{name: "missing token", clearToken: true, resin: appstate.ResinState{Managed: true, Account: "grok-alice-old1"}},
		{name: "unmanaged", resin: appstate.ResinState{}},
		{name: "missing account", resin: appstate.ResinState{Managed: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.clearToken {
				t.Setenv("RESIN_ADMIN_TOKEN", "")
			} else {
				t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
			}
			entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
			host := newMockHostCall(entries, auths)
			plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
				cfg.Resin.Enabled = !tc.disabled
				cfg.Resin.LocalOnly = tc.localOnly
				cfg.Resin.Platform = "Default"
				cfg.Resin.PlatformID = testResinPlatformID
				cfg.Resin.AdminURL = "http://resin.test"
			})
			if errUpdate := plugin.updateStateAccount("auth-1", func(account *appstate.AccountState) error {
				account.Resin = tc.resin
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

			state := plugin.store.Snapshot()
			if _, exists := state.Accounts["auth-1"]; exists {
				t.Fatalf("removed auth remained in state: %#v", state.Accounts["auth-1"])
			}
			if len(state.ResinLeaseDeletes) != 0 {
				t.Fatalf("unexpected queued lease deletes: %#v", state.ResinLeaseDeletes)
			}
		})
	}
}

func TestResinLeaseDeleteRetriesAfterBackoffAndDeduplicates(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "admin-secret")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch requests.Add(1) {
		case 1, 2:
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.Platform = "Default"
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = server.URL
	})
	settings := plugin.ResinConfig()
	added, errQueue := plugin.enqueueResinLeaseDelete("auth-1", settings, "grok-alice-old1")
	if errQueue != nil || !added {
		t.Fatalf("enqueue delete: added=%t err=%v", added, errQueue)
	}
	added, errQueue = plugin.enqueueResinLeaseDelete("auth-1", settings, "grok-alice-old1")
	if errQueue != nil || added {
		t.Fatalf("duplicate enqueue: added=%t err=%v", added, errQueue)
	}
	if jobs := plugin.store.Snapshot().ResinLeaseDeletes; len(jobs) != 1 {
		t.Fatalf("queued jobs = %d, want 1", len(jobs))
	}

	now := time.Now().UTC()
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), now); processed != 1 {
		t.Fatalf("first processed = %d, want 1", processed)
	}
	job := plugin.store.Snapshot().ResinLeaseDeletes[0]
	if job.Attempts != 1 || !job.NextAttemptAt.After(now) {
		t.Fatalf("first retry was not scheduled: %#v", job)
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), now.Add(time.Second)); processed != 0 {
		t.Fatalf("early retry processed = %d, want 0", processed)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("early retry sent %d requests, want 1", got)
	}

	if processed := plugin.processDueResinLeaseDeletes(context.Background(), job.NextAttemptAt.Add(time.Second)); processed != 1 {
		t.Fatalf("second processed = %d, want 1", processed)
	}
	job = plugin.store.Snapshot().ResinLeaseDeletes[0]
	if job.Attempts != 2 || !job.NextAttemptAt.After(time.Now().UTC()) {
		t.Fatalf("second retry was not scheduled: %#v", job)
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), job.NextAttemptAt.Add(time.Second)); processed != 1 {
		t.Fatalf("third processed = %d, want 1", processed)
	}
	if jobs := plugin.store.Snapshot().ResinLeaseDeletes; len(jobs) != 0 {
		t.Fatalf("successful retry left jobs behind: %#v", jobs)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("delete requests = %d, want 3", got)
	}
}

func TestResinLeaseDeleteUsesStoredTargetWhenConfigChanges(t *testing.T) {
	t.Setenv("RESIN_ADMIN_TOKEN", "new-token")
	var oldCalls atomic.Int32
	var newCalls atomic.Int32
	oldServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		oldCalls.Add(1)
		if request.Header.Get("Authorization") != "Bearer old-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer oldServer.Close()
	newServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		newCalls.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer newServer.Close()

	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Resin.Enabled = true
		cfg.Resin.PlatformID = testResinPlatformID
		cfg.Resin.AdminURL = newServer.URL
	})
	settings := appstate.ResinSettings{
		AdminURL:   oldServer.URL,
		AdminToken: "old-token",
		PlatformID: testResinPlatformID,
	}
	added, errQueue := plugin.enqueueResinLeaseDelete("auth-1", settings, "grok-alice-old1")
	if errQueue != nil || !added {
		t.Fatalf("enqueue delete: added=%t err=%v", added, errQueue)
	}
	if processed := plugin.processDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != 1 {
		t.Fatalf("processed deletes = %d, want 1", processed)
	}
	if oldCalls.Load() != 1 || newCalls.Load() != 0 {
		t.Fatalf("delete target calls: old=%d new=%d", oldCalls.Load(), newCalls.Load())
	}
	if jobs := plugin.store.Snapshot().ResinLeaseDeletes; len(jobs) != 0 {
		t.Fatalf("stored target delete was not removed: %#v", jobs)
	}
}
