package proxy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	appstate "grok-quality-guard/internal/app/state"
	coreproxy "grok-quality-guard/internal/proxy"
)

func TestProxyRowViewProvidesFullIdentityForAccountTooltip(t *testing.T) {
	row := proxyRowView(proxyCatalogRow{Account: Account{ID: "auth-1", Email: "alice@example.com"}})
	if got := row["display"]; got != "al***e@example.com" {
		t.Fatalf("display = %q, want masked identity", got)
	}
	if got := row["display_full"]; got != "alice@example.com" {
		t.Fatalf("display_full = %q, want full identity", got)
	}
}

func TestProxySourceViewsUseStandardMaskedEndpointFormat(t *testing.T) {
	node := proxyNodeView(coreproxy.Node{
		ID: "node-1",
		Endpoint: coreproxy.Endpoint{
			Protocol: coreproxy.ProtocolSOCKS5H, Host: "proxy.example", Port: 8080,
			Username: "user", Password: "secret", HasUser: true, HasPassword: true,
		},
	}, 0)
	if node.Endpoint != "socks5h://us***r:******@proxy.example:8080" {
		t.Fatalf("node endpoint = %q", node.Endpoint)
	}

	template, err := coreproxy.ParseTemplateURL("socks5h://user-{account}:secret@proxy.example:1080")
	if err != nil {
		t.Fatal(err)
	}
	view := proxyTemplateView(template, 0)
	if view.Endpoint != "socks5h://user-{account}:******@proxy.example:1080" {
		t.Fatalf("template endpoint = %q", view.Endpoint)
	}
}

func TestProxySourceEntryMatchesPreservesViewFilters(t *testing.T) {
	template, err := coreproxy.ParseTemplateURL("socks5h://user-{account}:secret@proxy.example:1080")
	if err != nil {
		t.Fatal(err)
	}
	template.ID = "template-1"
	template.Name = "Team Alpha"
	template.Enabled = true
	template.Revision = 7
	entry := proxySourceCandidate{
		ID:       template.ID,
		Kind:     coreproxy.SourceTemplate,
		Name:     template.Name,
		Revision: template.Revision,
		Template: &template,
		Runtime: coreproxy.SourceRuntime{
			Health:         coreproxy.HealthHealthy,
			HealthRevision: template.Revision,
		},
	}
	service := NewService(proxyViewTestEnvironment{store: newProxyTestStore(t)})
	view, ok := service.proxySourceViewForEntry(entry, map[string]int{entry.ID: 2})
	if !ok {
		t.Fatal("source view was not constructed")
	}
	for _, params := range []map[string]any{
		{},
		{"kind": "template"},
		{"kind": "resin"},
		{"query": "team alpha"},
		{"query": "team socks5h"},
		{"query": "template-1"},
		{"enabled": "true"},
		{"enabled": "disabled"},
		{"health": "healthy"},
		{"health": "unknown"},
	} {
		got := service.proxySourceEntryMatches(entry, params)
		want := proxySourceMatches(view, params)
		if got != want {
			t.Fatalf("params %#v: entry match = %t, view match = %t", params, got, want)
		}
	}
}

type proxyViewTestEnvironment struct {
	Environment
	store    *appstate.Store
	accounts []Account
}

var proxyViewTestAccountLocks sync.Map

func (e proxyViewTestEnvironment) ProxyStore() *appstate.Store { return e.store }

func (e proxyViewTestEnvironment) ProxyCatalogSnapshot() []Account {
	return append([]Account(nil), e.accounts...)
}

func (e proxyViewTestEnvironment) ProxyAccountLock(id string) *sync.Mutex {
	lock, _ := proxyViewTestAccountLocks.LoadOrStore(id, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func newProxyTestStore(t *testing.T) *appstate.Store {
	t.Helper()
	return appstate.NewStore(filepath.Join(t.TempDir(), "state.json"))
}

func TestResinSourceEndpointsUseTemplateDisplayAndReveal(t *testing.T) {
	template, err := coreproxy.ParseTemplateURL("socks5h://resin-platform.rn-{region}-{account}:proxy-token@resin.example:2260")
	if err != nil {
		t.Fatal(err)
	}
	template.ID = "resin-1"
	template.Kind = coreproxy.TemplateResin
	template.Name = "Resin A"
	template.Enabled = true
	store := newProxyTestStore(t)
	store.SetStateForTest(appstate.State{Proxy: coreproxy.ProxyState{
		Templates: []coreproxy.Template{template},
		ResinSources: map[string]coreproxy.ResinSourceConfig{
			"resin-1": {
				Platform: "resin-platform", PlatformID: "platform-1",
				AdminURL: "https://resin.example:2260", AccountFormat: "rn-{region}-{account}",
				AdminToken: "admin-token",
			},
		},
	}})
	service := NewService(proxyViewTestEnvironment{store: store})
	views := service.proxySourcesView()
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	wantMasked := "socks5h://resin-platform.rn-{region}-{account}:******@resin.example:2260"
	if views[0].Endpoint != wantMasked {
		t.Fatalf("masked endpoint = %q, want %q", views[0].Endpoint, wantMasked)
	}
	full, err := service.proxySourceEndpoint("resin-1")
	if err != nil {
		t.Fatal(err)
	}
	wantFull := "socks5h://resin-platform.rn-{region}-{account}:proxy-token@resin.example:2260"
	if full != wantFull {
		t.Fatalf("revealed endpoint = %q, want %q", full, wantFull)
	}
}

func TestProxySourceInfoReturnsNameAndKind(t *testing.T) {
	store := newProxyTestStore(t)
	store.SetStateForTest(appstate.State{Proxy: coreproxy.ProxyState{
		Nodes: []coreproxy.Node{{ID: "node-1", Name: "香港节点", Enabled: true}},
	}})
	service := NewService(proxyViewTestEnvironment{store: store})
	name, kind := service.SourceInfo("node-1")
	if name != "香港节点" || kind != coreproxy.SourceNode {
		t.Fatalf("SourceInfo(node-1) = %q, %q", name, kind)
	}
	name, kind = service.SourceInfo("missing")
	if name != "" || kind != "" {
		t.Fatalf("SourceInfo(missing) = %q, %q", name, kind)
	}
}
func TestAutoBindRuntimeClearsLastErrorAfterSuccess(t *testing.T) {
	service := NewService(proxyViewTestEnvironment{store: newProxyTestStore(t)})
	service.mgr.runtime.LastError = "automatic source availability changed"
	service.recordProxyAutoResult("auth-1", proxyBindingResult{Committed: true}, nil)
	if got := service.mgr.runtime.LastError; got != "" {
		t.Fatalf("last error = %q, want cleared after successful bind", got)
	}
	if service.mgr.runtime.BoundTotal != 1 || service.mgr.runtime.LastSuccessAt.IsZero() {
		t.Fatalf("success runtime was not updated: %#v", service.mgr.runtime)
	}
}

func TestFailedProxyResultPreservesSourceDomainErrorCode(t *testing.T) {
	err := coreproxy.NewError("proxy_source_disabled", 409, "source was disabled before proxy prepare")
	result := failedProxyResult("auth-1", "failed", err)
	if result.Code != "proxy_source_disabled" {
		t.Fatalf("result code = %q, want proxy_source_disabled", result.Code)
	}
}

func TestAutoBindRecoverableSourceChangeResetsForReselection(t *testing.T) {
	store := newProxyTestStore(t)
	store.SetStateForTest(appstate.State{Proxy: coreproxy.ProxyState{AutoBind: coreproxy.AutoBindConfig{
		Enabled: true, RetryInitial: "1m", RetryMax: "1h",
	}}})
	service := NewService(proxyViewTestEnvironment{store: store})
	service.mgr.runtime.LastError = "automatic source availability changed"
	service.mgr.runtime.FailedTotal = 3
	service.mgr.runtime.SkippedTotal = 2
	err := proxyError("auto_bind_source_unavailable", 409, "automatic source availability changed")
	result := failedProxyResult("auth-1", "failed", err)
	service.recordProxyAutoResult("auth-1", result, err)

	state := store.ProxyAccount("auth-1")
	if state.AutoBind.LastError != "" {
		t.Fatalf("account last error = %q, want cleared for reselection", state.AutoBind.LastError)
	}
	if !state.AutoBind.NextAttemptAt.IsZero() || state.AutoBind.Attempts != 0 {
		t.Fatalf("recoverable retry state = %#v, want immediately eligible for the next scan", state.AutoBind)
	}
	if service.mgr.runtime.LastError != "" {
		t.Fatalf("runtime last error = %q, want recoverable source change hidden", service.mgr.runtime.LastError)
	}
	if service.mgr.runtime.FailedTotal != 3 || service.mgr.runtime.SkippedTotal != 3 {
		t.Fatalf("runtime counters = failed %d skipped %d, want recoverable result counted as skipped", service.mgr.runtime.FailedTotal, service.mgr.runtime.SkippedTotal)
	}
}

func TestProxyWaitingWithoutErrorClearsStaleRuntimeError(t *testing.T) {
	service := NewService(proxyViewTestEnvironment{store: newProxyTestStore(t)})
	service.mgr.runtime.LastError = "automatic source availability changed"
	service.setProxyWaiting("waiting_sources", nil)
	if service.mgr.runtime.LastError != "" {
		t.Fatalf("last error = %q, want cleared while waiting for sources", service.mgr.runtime.LastError)
	}
	if service.mgr.runtime.WaitingReason != "waiting_sources" {
		t.Fatalf("waiting reason = %q", service.mgr.runtime.WaitingReason)
	}
}

func TestAutoBindNoAvailableSourcesClearsStaleRetryState(t *testing.T) {
	store := newProxyTestStore(t)
	store.SetStateForTest(appstate.State{
		Accounts: map[string]*appstate.AccountState{
			"auth-1": {AuthID: "auth-1", AutoBind: coreproxy.AutoBindAccountState{Attempts: 4, LastError: "no available proxy source"}},
		},
		Proxy: coreproxy.ProxyState{AutoBind: coreproxy.AutoBindConfig{Enabled: true, RetryInitial: "1m", RetryMax: "1h"}},
	})
	service := NewService(proxyViewTestEnvironment{store: store})
	service.mgr.runtime.LastError = "no available proxy source"
	service.recordProxyAutoResult("auth-1", proxyBindingResult{Status: "unavailable", Code: "no_available_sources", Error: "no available proxy source"}, nil)

	state := store.ProxyAccount("auth-1")
	if state.AutoBind.LastError != "" || state.AutoBind.Attempts != 0 {
		t.Fatalf("auto-bind state = %#v, want stale unavailable state cleared", state.AutoBind)
	}
	if service.mgr.runtime.LastError != "" {
		t.Fatalf("runtime last error = %q, want cleared for source waiting", service.mgr.runtime.LastError)
	}
}
func TestResidentQueueDetailsReportAutoBindAndHealthWork(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(proxyViewTestEnvironment{store: newProxyTestStore(t)})
	service.mgr.queue = []string{"auth-wait"}
	service.mgr.queueHead = 0
	service.mgr.queuedAt = map[string]time.Time{
		"auth-wait":    now.Add(2 * time.Second),
		"auth-pending": now.Add(time.Second),
		"auth-run":     now,
	}
	service.mgr.pending = map[string]time.Time{"auth-pending": now.Add(time.Second)}
	service.mgr.inFlight = map[string]time.Time{"auth-run": now}

	auto, ok := service.QueueDetail("auto_bind")
	if !ok {
		t.Fatal("auto_bind queue detail not found")
	}
	if len(auto.Active) != 1 || auto.Active[0].ID != "auth-run" {
		t.Fatalf("auto_bind active = %#v", auto.Active)
	}
	if len(auto.Waiting) != 2 || auto.Waiting[0].ID != "auth-pending" || auto.Waiting[1].ID != "auth-wait" {
		t.Fatalf("auto_bind waiting = %#v", auto.Waiting)
	}
	autoActive, autoWaiting, ok := service.QueueDepth("auto_bind")
	if !ok || autoActive != len(auto.Active) || autoWaiting != len(auto.Waiting) {
		t.Fatalf("auto_bind queue depth = %d/%d ok=%t, want %d/%d", autoActive, autoWaiting, ok, len(auto.Active), len(auto.Waiting))
	}

	service.mgr.healthQueuedAt = map[string]time.Time{"source-wait": now.Add(time.Second)}
	service.mgr.healthActive = map[string]time.Time{"source-run": now}
	health, ok := service.QueueDetail("proxy_health")
	if !ok {
		t.Fatal("proxy_health queue detail not found")
	}
	if len(health.Active) != 1 || health.Active[0].ID != "source-run" {
		t.Fatalf("proxy_health active = %#v", health.Active)
	}
	if len(health.Waiting) != 1 || health.Waiting[0].ID != "source-wait" {
		t.Fatalf("proxy_health waiting = %#v", health.Waiting)
	}
	healthActive, healthWaiting, ok := service.QueueDepth("proxy_health")
	if !ok || healthActive != len(health.Active) || healthWaiting != len(health.Waiting) {
		t.Fatalf("proxy_health queue depth = %d/%d ok=%t, want %d/%d", healthActive, healthWaiting, ok, len(health.Active), len(health.Waiting))
	}
}

type proxyObservationTestEnvironment struct {
	proxyViewTestEnvironment
	raw map[string]json.RawMessage
}

func (e proxyObservationTestEnvironment) ProxyResolveAuth(context.Context, string) (Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error) {
	return Account{}, pluginapi.HostAuthGetResponse{}, e.raw, nil
}

func TestProxyObservationClearRemovesEndpointMetadata(t *testing.T) {
	service := NewService(proxyViewTestEnvironment{store: newProxyTestStore(t)})
	service.updateCatalogProxy("auth-1", "socks5h://user:secret@proxy.example:1080")
	service.updateCatalogProxy("auth-1", "")

	service.mgr.mu.Lock()
	observation := service.mgr.observations["auth-1"]
	service.mgr.mu.Unlock()
	if !observation.Known || observation.Masked != "" || observation.Hash != "" {
		t.Fatalf("cleared observation retained endpoint metadata: %#v", observation)
	}
}

func TestConfirmProxyObservationClearsStaleEndpointMetadata(t *testing.T) {
	service := NewService(proxyObservationTestEnvironment{
		proxyViewTestEnvironment: proxyViewTestEnvironment{store: newProxyTestStore(t)},
		raw:                      map[string]json.RawMessage{},
	})
	service.mgr.observations["auth-1"] = proxyObservation{
		Masked:    "socks5h://old***:******@proxy.example:1080",
		Hash:      "old-hash",
		Known:     false,
		CheckedAt: time.Now().UTC(),
	}
	observation := service.confirmProxyObservation(context.Background(), Account{ID: "auth-1", Path: "/tmp/auth-1.json"})
	if !observation.Known || observation.Masked != "" || observation.Hash != "" {
		t.Fatalf("confirmed empty proxy retained stale endpoint metadata: %#v", observation)
	}
}

func TestProxyObservationPruneReleasesDeletedAndExpiredAccounts(t *testing.T) {
	service := NewService(proxyViewTestEnvironment{store: newProxyTestStore(t)})
	service.updateCatalogProxy("auth-live", "socks5h://user:secret@proxy.example:1080")
	service.updateCatalogProxy("auth-gone", "socks5h://other:secret@proxy.example:1080")

	service.mgr.mu.Lock()
	if got := service.mgr.observations["auth-live"].Masked; got != "socks5h://us***r:******@proxy.example:1080" {
		service.mgr.mu.Unlock()
		t.Fatalf("observation stored an unmasked endpoint: %q", got)
	}
	gone := service.mgr.observations["auth-gone"]
	gone.CheckedAt = time.Now().UTC().Add(-proxyObservationRetention - time.Minute)
	service.mgr.observations["auth-gone"] = gone
	service.mgr.mu.Unlock()

	service.PruneProxyObservations([]string{"auth-live"})
	service.mgr.mu.Lock()
	if len(service.mgr.observations) != 1 {
		service.mgr.mu.Unlock()
		t.Fatalf("observations after catalog prune = %#v", service.mgr.observations)
	}
	if _, exists := service.mgr.observations["auth-gone"]; exists {
		service.mgr.mu.Unlock()
		t.Fatal("deleted auth observation was retained")
	}
	live := service.mgr.observations["auth-live"]
	live.CheckedAt = time.Now().UTC().Add(-proxyObservationRetention - time.Minute)
	service.mgr.observations["auth-live"] = live
	service.mgr.mu.Unlock()

	service.PruneProxyObservations([]string{"auth-live"})
	service.mgr.mu.Lock()
	defer service.mgr.mu.Unlock()
	if len(service.mgr.observations) != 0 {
		t.Fatalf("expired observations were retained: %#v", service.mgr.observations)
	}
}
func TestBindingStateSemanticsDistinguishUnboundCheckingAndReadFailure(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name        string
		account     Account
		projection  appstate.ProxyBindingProjection
		observation proxyObservation
		want        string
		wantProxy   string
	}{
		{name: "runtime account without proxy is unbound", account: Account{ID: "auth-1"}, want: "unbound"},
		{name: "file has not been inspected", account: Account{ID: "auth-1", Path: "auth.json"}, want: "checking"},
		{name: "inspection failed", account: Account{ID: "auth-1", Path: "auth.json", ProxyMetaInspected: true}, want: "read_failed"},
		{name: "known empty proxy is unbound", account: Account{ID: "auth-1", Path: "auth.json", ProxyURLKnown: true}, observation: proxyObservation{Known: true}, want: "unbound"},
		{name: "external proxy", account: Account{ID: "auth-1", Path: "auth.json"}, observation: proxyObservation{Known: true, Masked: "socks5h://us***r:******@proxy.example:1080"}, want: "external", wantProxy: "socks5h://us***r:******@proxy.example:1080"},
		{name: "matching managed endpoint", account: Account{ID: "auth-1", Path: "auth.json"}, projection: appstate.ProxyBindingProjection{SourceID: "node-1", SourceKind: coreproxy.SourceNode, EndpointHash: "hash-1"}, observation: proxyObservation{Known: true, Masked: "socks5h://us***r:******@proxy.example:1080", Hash: "hash-1"}, want: "bound"},
		{name: "managed endpoint drift", account: Account{ID: "auth-1", Path: "auth.json"}, projection: appstate.ProxyBindingProjection{SourceID: "node-1", SourceKind: coreproxy.SourceNode, EndpointHash: "hash-1"}, observation: proxyObservation{Known: true, Masked: "socks5h://us***r:******@proxy.example:1080", Hash: "hash-2"}, want: "drifted"},
		{name: "managed endpoint without a recorded hash is structurally unknown", account: Account{ID: "auth-1", Path: "auth.json"}, projection: appstate.ProxyBindingProjection{SourceID: "node-1", SourceKind: coreproxy.SourceNode}, observation: proxyObservation{Known: true, Masked: "socks5h://us***r:******@proxy.example:1080", Hash: "hash-1"}, want: "unknown"},
		{name: "pending operation", account: Account{ID: "auth-1", Path: "auth.json", ProxyURLKnown: true}, projection: appstate.ProxyBindingProjection{Pending: true}, observation: proxyObservation{Known: true, CheckedAt: now}, want: "reconcile_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := bindingStateFromProjection(tc.account, tc.projection, tc.observation)
			if state.Status != tc.want {
				t.Fatalf("status = %q, want %q", state.Status, tc.want)
			}
			if tc.wantProxy != "" && state.Proxy != tc.wantProxy {
				t.Fatalf("proxy = %q, want %q", state.Proxy, tc.wantProxy)
			}
		})
	}
}

func TestPageScopedBindingResolutionDoesNotPruneOtherObservations(t *testing.T) {
	store := newProxyTestStore(t)
	service := NewService(proxyViewTestEnvironment{store: store})
	now := time.Now()
	service.mgr.observations["auth-page"] = proxyObservation{Masked: "socks5h://u***@one:1080", Known: true, CheckedAt: now}
	service.mgr.observations["auth-other"] = proxyObservation{Masked: "socks5h://u***@two:1080", Known: true, CheckedAt: now}
	service.mgr.observationsPruned = now.Add(-2 * proxyObservationPruneInterval)

	states := service.BindingStatesForAccounts([]Account{{ID: "auth-page", Path: "auth-page.json"}})
	if len(states) != 1 || states[0].Status != "external" {
		t.Fatalf("page states = %#v", states)
	}
	if _, ok := service.mgr.observations["auth-other"]; !ok {
		t.Fatal("page-scoped resolution pruned an observation for another page")
	}
}

func TestAccountAndProxyPagesUseTheSameBindingProjection(t *testing.T) {
	store := newProxyTestStore(t)
	store.SetStateForTest(appstate.State{Accounts: map[string]*appstate.AccountState{
		"auth-1": {AuthID: "auth-1", Proxy: coreproxy.Binding{SourceID: "node-1", SourceKind: coreproxy.SourceNode, EndpointHash: "hash-1"}},
	}})
	service := NewService(proxyViewTestEnvironment{store: store, accounts: []Account{{ID: "auth-1", Path: "auth-1.json"}}})
	service.PruneProxyObservations([]string{"auth-1"})
	service.mgr.observations["auth-1"] = proxyObservation{Masked: "socks5h://u***@proxy:1080", Hash: "hash-1", Known: true, CheckedAt: time.Now()}

	pageStates := service.BindingStatesForAccounts([]Account{{ID: "auth-1", Path: "auth-1.json"}})
	var catalogState BindingState
	service.walkProxyCatalog(func(row proxyCatalogRow) { catalogState = row.State })
	if len(pageStates) != 1 || pageStates[0].Status != "bound" || catalogState.Status != "bound" {
		t.Fatalf("account status = %#v, proxy catalog status = %#v", pageStates, catalogState)
	}
	if pageStates[0].Version != catalogState.Version {
		t.Fatalf("binding versions = %d and %d, want one shared version", pageStates[0].Version, catalogState.Version)
	}
}

func TestBindingCountsCacheInvalidatesWhenCatalogMembershipChanges(t *testing.T) {
	service := NewService(proxyViewTestEnvironment{store: newProxyTestStore(t)})
	service.PruneProxyObservations([]string{"auth-1", "auth-2"})
	counts := emptyBindingCounts()
	counts["bound"] = 2
	version := service.BindingVersion()
	service.rememberBindingCounts(counts, 2, version)
	if _, _, _, ok := service.recentBindingCounts(); !ok {
		t.Fatal("binding counts cache was not populated")
	}
	service.PruneProxyObservations([]string{"auth-1"})
	if _, _, _, ok := service.recentBindingCounts(); ok {
		t.Fatal("binding counts cache survived a catalog membership change")
	}
}
func TestBindingCountsCacheInvalidatesOnBindingRevision(t *testing.T) {
	service := NewService(proxyViewTestEnvironment{store: newProxyTestStore(t)})
	counts := emptyBindingCounts()
	counts["bound"] = 2
	service.mgr.bindingRevision = 7
	service.rememberBindingCounts(counts, 3, 7)
	got, total, version, ok := service.recentBindingCounts()
	if !ok || total != 3 || version != 7 || got["bound"] != 2 {
		t.Fatalf("recent binding counts = %#v total=%d ok=%v", got, total, ok)
	}
	service.mgr.bindingRevision = 8
	if _, _, _, ok := service.recentBindingCounts(); ok {
		t.Fatal("binding counts cache survived a newer binding revision")
	}
}
