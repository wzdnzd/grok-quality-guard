package resin

import (
	"net/url"
	"testing"
)

func TestAccountRewritePreservesURL(t *testing.T) {
	const rendered = "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	if account := AccountFromProxyURL(rendered); account != "grok-alice-old1" {
		t.Fatalf("unexpected account: %q", account)
	}
	rewritten, errRewrite := RewriteAccount(rendered, "grok-alice-new1")
	if errRewrite != nil {
		t.Fatal(errRewrite)
	}
	parsed, errParse := url.Parse(rewritten)
	if errParse != nil {
		t.Fatal(errParse)
	}
	if parsed.User.Username() != "Default.grok-alice-new1" {
		t.Fatalf("unexpected rewritten username: %q", parsed.User.Username())
	}
	password, okPassword := parsed.User.Password()
	if !okPassword || password != "proxy-secret" {
		t.Fatalf("password was not preserved: %q", password)
	}
}
