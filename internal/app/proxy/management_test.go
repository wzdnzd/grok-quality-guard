package proxy

import (
	"testing"

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
