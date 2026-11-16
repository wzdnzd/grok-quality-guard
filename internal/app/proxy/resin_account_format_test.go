package proxy

import "testing"

func TestResinProxyTemplateFromAccountFormat(t *testing.T) {
	template, err := resinProxyTemplateFromAccountFormat("grok-{account}", "https://resin.example:8443/admin")
	if err != nil {
		t.Fatal(err)
	}
	if template != "socks5h://{platform}.grok-{account}:{proxy_token}@resin.example:8443" {
		t.Fatalf("unexpected template: %s", template)
	}

	template, err = resinProxyTemplateFromAccountFormat("svc-{account}", "http://resin.example")
	if err != nil {
		t.Fatal(err)
	}
	if template != "socks5h://{platform}.svc-{account}:{proxy_token}@resin.example:80" {
		t.Fatalf("unexpected default-port template: %s", template)
	}
}

func TestResinCandidateUsesAccountFormatWithCurrentAdminURL(t *testing.T) {
	current := ResinSettings{AdminURL: "http://old-resin:2260", ProxyTemplate: "socks5h://old.{account}:{proxy_token}@old-resin:2260"}
	candidate, err := resinCandidateFromParams(map[string]any{
		"account_format": "{account}",
		"admin_url":      "https://new-resin:9443/base",
	}, current)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ProxyTemplate != "socks5h://{platform}.{account}:{proxy_token}@new-resin:9443" {
		t.Fatalf("unexpected composed template: %s", candidate.ProxyTemplate)
	}
}

func TestResinAccountFormatRejectsPlatformAndFullURL(t *testing.T) {
	for _, value := range []string{"{platform}.{account}", "socks5h://user-{account}:pass@proxy:2260"} {
		if _, err := resinProxyTemplateFromAccountFormat(value, "http://resin:2260"); err == nil {
			t.Fatalf("invalid account format %q was accepted", value)
		}
	}
}

func TestResinAccountSuffixRemovesAutomaticPlatformPrefix(t *testing.T) {
	if got := resinAccountSuffix("{platform}.grok-{account}", "CPA"); got != "grok-{account}" {
		t.Fatalf("placeholder platform prefix was not removed: %q", got)
	}
	if got := resinAccountSuffix("CPA.grok-{account}", "CPA"); got != "grok-{account}" {
		t.Fatalf("resolved platform prefix was not removed: %q", got)
	}
}
