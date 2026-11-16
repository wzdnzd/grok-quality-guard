package proxy

import (
	"testing"

	coreproxy "grok-quality-guard/internal/proxy"
)

func TestProxyURLsAcceptDefaultKeepPasswordUpdate(t *testing.T) {
	const rawURL = "socks5h://proxy-{country}-sid-{session}:test-pass@proxy.example:3010"
	keep := &passwordUpdate{Action: "keep"}

	endpoint, err := proxyInputEndpoint(proxyNodeInput{URL: "socks5h://proxy-user:test-pass@proxy.example:3010", PasswordUpdate: keep})
	if err != nil {
		t.Fatalf("node URL with keep password update was rejected: %v", err)
	}
	if endpoint.Password != "test-pass" || !endpoint.HasPassword {
		t.Fatalf("node URL password was not preserved: %#v", endpoint)
	}

	template, err := buildProxyTemplate(proxyTemplateInput{
		Name:           "region proxy",
		URL:            rawURL,
		PasswordUpdate: keep,
		Placeholders: []coreproxy.Placeholder{
			{Name: "country", Kind: coreproxy.PlaceholderEnum, Enum: &coreproxy.EnumSpec{Separator: ",", Input: "US,JP,SG"}},
			{Name: "session", Kind: coreproxy.PlaceholderRandom, Random: &coreproxy.RandomSpec{Length: 8, CaseSensitive: true}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("template URL with keep password update was rejected: %v", err)
	}
	if template.Password != "test-pass" || !template.HasPassword {
		t.Fatalf("template URL password was not preserved: %#v", template)
	}
	if template.Protocol != coreproxy.ProtocolSOCKS5H || template.Host != "proxy.example" || template.Port != 3010 {
		t.Fatalf("unexpected template endpoint: %#v", template)
	}
}

func TestTemplateURLPasswordTakesPrecedence(t *testing.T) {
	const rawURL = "socks5h://proxy-{session}:test-pass@proxy.example:3010"
	inputs := []*passwordUpdate{
		{Action: "set", Value: stringPointer("new-pass")},
		{Action: "clear"},
	}
	for _, update := range inputs {
		template, err := buildProxyTemplate(proxyTemplateInput{
			Name:           "ambiguous proxy",
			URL:            rawURL,
			PasswordUpdate: update,
			Placeholders: []coreproxy.Placeholder{
				{Name: "session", Kind: coreproxy.PlaceholderRandom, Random: &coreproxy.RandomSpec{Length: 8}},
			},
		}, nil)
		if err != nil {
			t.Fatalf("URL with %s password update was rejected: %v", update.Action, err)
		}
		if template.Password != "test-pass" || !template.HasPassword {
			t.Fatalf("URL password should win over %s update: %#v", update.Action, template)
		}
	}
}

func TestTemplatePasswordUpdateAppliesWhenURLHasNoPassword(t *testing.T) {
	template, err := buildProxyTemplate(proxyTemplateInput{
		Name:           "password fallback",
		URL:            "socks5h://proxy-{session}@proxy.example:3010",
		PasswordUpdate: &passwordUpdate{Action: "set", Value: stringPointer("new-pass")},
		Placeholders: []coreproxy.Placeholder{
			{Name: "session", Kind: coreproxy.PlaceholderRandom, Random: &coreproxy.RandomSpec{Length: 8}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("password update without URL password was rejected: %v", err)
	}
	if template.Password != "new-pass" || !template.HasPassword {
		t.Fatalf("password update was not applied: %#v", template)
	}
}

func stringPointer(value string) *string { return &value }
