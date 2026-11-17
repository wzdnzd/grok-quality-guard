package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	resinpkg "grok-quality-guard/internal/app/proxy/resin"
	coreproxy "grok-quality-guard/internal/proxy"
)

func TestResinAccountFormatUsesEmailPrefixAndTemplatePlaceholders(t *testing.T) {
	template, config, err := buildResinSource(proxyResinInput{
		Name: "Primary Resin", Platform: "CPA", AdminURL: "https://resin.example:8443",
		AdminToken: "admin" + "-" + "token", ProxyToken: "proxy" + "-" + "token", AccountFormat: "xai-{account}-{region}",
		Placeholders: []coreproxy.Placeholder{{
			Name: "region", Kind: coreproxy.PlaceholderEnum, Enum: &coreproxy.EnumSpec{Separator: ",", Input: "us,jp"},
		}},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if template.Kind != coreproxy.TemplateResin || template.UsernamePattern != "CPA.xai-{account}-{region}" {
		t.Fatalf("unexpected resin template: %#v", template)
	}
	if config.AccountFormat != "xai-{account}-{region}" {
		t.Fatalf("unexpected account format: %#v", config)
	}
	reserved := false
	for _, placeholder := range template.Placeholders {
		if placeholder.Name == coreproxy.ReservedAccountPlaceholder {
			reserved = placeholder.Kind == coreproxy.PlaceholderReserved && placeholder.Enum == nil && placeholder.Random == nil
		}
	}
	if !reserved {
		t.Fatalf("account was not reserved: %#v", template.Placeholders)
	}
	provider, err := resinpkg.NewTemplateProvider(template, resinpkg.ProviderSettings{
		Platform: config.Platform})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := provider.Generate(context.Background(), coreproxy.GenerateRequest{AccountBase: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rendered.GeneratedAccount, "xai-alice-") {
		t.Fatalf("account did not use the email prefix: %#v", rendered)
	}
	if !strings.HasPrefix(rendered.Endpoint.Username, "CPA.xai-alice-") {
		t.Fatalf("platform prefix was not rendered: %q", rendered.Endpoint.Username)
	}
}

func TestResinPlaceholderDefinitionsKeepAccountReserved(t *testing.T) {
	definitions, err := resinPlaceholderDefinitions("xai-{account}-{region}", []coreproxy.Placeholder{
		{Name: coreproxy.ReservedAccountPlaceholder, Kind: coreproxy.PlaceholderEnum, Enum: &coreproxy.EnumSpec{Separator: ",", Input: "bad,value"}},
		{Name: "region", Kind: coreproxy.PlaceholderEnum, Enum: &coreproxy.EnumSpec{Separator: ",", Input: "us,jp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 {
		t.Fatalf("definitions = %#v", definitions)
	}
	account := definitions[0]
	if account.Name != coreproxy.ReservedAccountPlaceholder || account.Kind != coreproxy.PlaceholderReserved || account.Enum != nil || account.Random != nil || account.GroupID != "" {
		t.Fatalf("account placeholder accepted user configuration: %#v", account)
	}
	if definitions[1].Name != "region" || definitions[1].Kind != coreproxy.PlaceholderEnum {
		t.Fatalf("normal placeholder was lost: %#v", definitions[1])
	}
}

func TestNormalizeResinAccountFormatRejectsFullURLAndPlatform(t *testing.T) {
	for _, value := range []string{"{platform}.{account}", "socks5h://user-{account}:pass@proxy:2260", "xai-{account}@proxy"} {
		if _, err := normalizeResinAccountFormat(value); err == nil {
			t.Fatalf("invalid account format %q was accepted", value)
		}
	}
	if got, err := normalizeResinAccountFormat("xai-{account}-{region}"); err != nil || got != "xai-{account}-{region}" {
		t.Fatalf("valid account format = %q, %v", got, err)
	}
}

func TestResolveResinPlatformByNameReturnsCanonicalName(t *testing.T) {
	const platformID = "11111111-1111-1111-1111-111111111111"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/platforms" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.URL.Query().Get("keyword") != "canonical" {
			t.Fatalf("keyword = %q", request.URL.Query().Get("keyword"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"items":[{"id":"` + platformID + `","name":"Canonical"}],"total":1,"limit":1000,"offset":0}`))
	}))
	defer server.Close()
	resolved, err := resolveResinPlatformByName(context.Background(), resinIntegration{
		Platform: "canonical", AdminURL: server.URL, AdminToken: "admin-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Platform != "Canonical" || resolved.PlatformID != platformID {
		t.Fatalf("unexpected resolved platform: %#v", resolved)
	}
}

func TestNormalizeResinAdminSettingsRejectsUnsafeURL(t *testing.T) {
	for _, raw := range []string{
		"http://alice:private@resin.example",
		"http://resin.example?token=private",
		"http://resin.example#private",
		"ftp://resin.example",
	} {
		if _, err := normalizeResinAdminSettings(resinIntegration{Platform: "Default", AdminURL: raw, AdminToken: "token"}); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe admin URL was accepted or disclosed: %v", err)
		}
	}
}
