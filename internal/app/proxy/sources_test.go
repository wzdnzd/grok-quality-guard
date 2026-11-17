package proxy

import (
	"errors"
	"testing"

	coreproxy "grok-quality-guard/internal/proxy"
)

func TestProxyURLCredentialsAreAuthoritative(t *testing.T) {
	endpoint, err := proxyInputEndpoint(proxyNodeInput{URL: "socks5h://proxy-user:test-pass@proxy.example:3010"})
	if err != nil {
		t.Fatalf("parse node URL: %v", err)
	}
	if endpoint.Password != "test-pass" || !endpoint.HasPassword {
		t.Fatalf("node URL password was not preserved: %#v", endpoint)
	}
	if _, errAmbiguous := proxyInputEndpoint(proxyNodeInput{URL: "socks5h://proxy-user:test-pass@proxy.example:3010", Username: "other"}); errAmbiguous == nil {
		t.Fatal("URL and individual endpoint fields were accepted together")
	}

	const rawURL = "socks5h://proxy-{country}-sid-{session}:test-pass@proxy.example:3010"
	template, err := buildProxyTemplate(proxyTemplateInput{
		Name: "region proxy",
		URL:  rawURL,
		Placeholders: []coreproxy.Placeholder{
			{Name: "country", Kind: coreproxy.PlaceholderEnum, Enum: &coreproxy.EnumSpec{Separator: ",", Input: "US,JP,SG"}},
			{Name: "session", Kind: coreproxy.PlaceholderRandom, Random: &coreproxy.RandomSpec{Length: 8, CaseSensitive: true}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("build template from URL: %v", err)
	}
	if template.Password != "test-pass" || !template.HasPassword {
		t.Fatalf("template URL password was not preserved: %#v", template)
	}
	if template.Protocol != coreproxy.ProtocolSOCKS5H || template.Host != "proxy.example" || template.Port != 3010 {
		t.Fatalf("unexpected template endpoint: %#v", template)
	}
}

func TestProxyInputEndpointFallsBackToAddressFirst(t *testing.T) {
	endpoint, err := proxyInputEndpoint(proxyNodeInput{URL: "http://proxy.example:8080:alice:secret"})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Host != "proxy.example" || endpoint.Username != "alice" || endpoint.Password != "secret" {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
}
func TestProxyParamsRejectRetiredPasswordUpdate(t *testing.T) {
	_, err := decodeProxyParams[proxyNodeInput](map[string]any{
		"url":             "http://proxy.example:8080",
		"password_update": map[string]any{"action": "keep"},
	}, "")
	if err == nil {
		t.Fatal("retired password_update field was silently accepted")
	}
	var typed *proxyManagementError
	if !errors.As(err, &typed) || typed.Status != 400 || typed.Code != "proxy_password_update_unsupported" {
		t.Fatalf("unexpected retired-field error: %v", err)
	}
}
