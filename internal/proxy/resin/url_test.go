package resin

import (
	"net/url"
	"testing"
)

func TestRenderProxyAndAccountRewrite(t *testing.T) {
	rendered, errRender := RenderProxy("socks5h://{platform}.{account}:{proxy_token}@resin:2260", "Default", "grok-alice-old1", "proxy-secret")
	if errRender != nil {
		t.Fatal(errRender)
	}
	if AccountFromProxyURL(rendered) != "grok-alice-old1" {
		t.Fatalf("unexpected account: %q", rendered)
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
