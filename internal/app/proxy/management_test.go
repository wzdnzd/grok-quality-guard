package proxy

import (
	"testing"

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

func (e proxyViewTestEnvironment) ProxyStore() *appstate.Store { return e.store }

func TestResinSourceEndpointsUseTemplateDisplayAndReveal(t *testing.T) {
	template, err := coreproxy.ParseTemplateURL("socks5h://resin-platform.rn-{region}-{account}:proxy-token@resin.example:2260")
	if err != nil {
		t.Fatal(err)
	}
	template.ID = "resin-1"
	template.Kind = coreproxy.TemplateResin
	template.Name = "Resin A"
	template.Enabled = true
	store := appstate.NewStore("")
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
	store := appstate.NewStore("")
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
	service := NewService(proxyViewTestEnvironment{store: appstate.NewStore("")})
	service.mgr.runtime.LastError = "automatic source availability changed"
	service.recordProxyAutoResult("auth-1", proxyBindingResult{Committed: true}, nil)
	if got := service.mgr.runtime.LastError; got != "" {
		t.Fatalf("last error = %q, want cleared after successful bind", got)
	}
	if service.mgr.runtime.BoundTotal != 1 || service.mgr.runtime.LastSuccessAt.IsZero() {
		t.Fatalf("success runtime was not updated: %#v", service.mgr.runtime)
	}
}
