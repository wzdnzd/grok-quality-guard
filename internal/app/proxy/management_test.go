package proxy

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

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

type proxyViewTestEnvironment struct {
	Environment
	store *appstate.Store
}

var proxyViewTestAccountLocks sync.Map

func (e proxyViewTestEnvironment) ProxyStore() *appstate.Store { return e.store }

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
}
