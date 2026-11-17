package proxy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	resinpkg "grok-quality-guard/internal/app/proxy/resin"
	statepkg "grok-quality-guard/internal/app/state"
	coreproxy "grok-quality-guard/internal/proxy"
)

func TestResinAccountFormatUsesEmailPrefixAndTemplatePlaceholders(t *testing.T) {
	template, config, err := buildResinSource(proxyResinInput{
		Name: "Primary Resin", Platform: "CPA", AdminURL: "https://resin.example:8443",
		AdminToken: "admin-token", ProxyToken: "proxy-token",
		AccountFormat: "xai-{account}-{region}",
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
		Platform: config.Platform, ProxyToken: template.Password,
	})
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

func TestLegacyResinAccountFormatExtractsUsernamePattern(t *testing.T) {
	cases := []struct {
		name     string
		settings ResinSettings
		want     string
	}{
		{
			name: "bare account marker gains stable prefix and random suffix",
			settings: ResinSettings{
				Platform:      "Default",
				ProxyTemplate: "socks5h://{platform}.{account}:{proxy_token}@resin:2260",
			},
			want: "grok-{account}-{suffix}",
		},
		{
			name: "resolved platform prefix is removed from full URL",
			settings: ResinSettings{
				Platform:      "CPA",
				ProxyTemplate: "socks5h://CPA.svc-{account}:token@resin:2260",
			},
			want: "svc-{account}-{suffix}",
		},
		{
			name: "explicit account format and placeholders are preserved",
			settings: ResinSettings{
				Platform:      "CPA",
				AccountFormat: "xai-{account}-{region}",
				ProxyTemplate: "socks5h://CPA.xai-{account}-{region}:token@resin:2260",
			},
			want: "xai-{account}-{region}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := legacyResinAccountFormat(tc.settings); got != tc.want {
				t.Fatalf("legacy account format = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLegacyResinStateMigratesToTemplateSource(t *testing.T) {
	store := statepkg.NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Load(statepkg.Policy{}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(state *statepkg.State) error {
		state.Resin = ResinSettings{
			Configured: true, Name: "Legacy Resin", Enabled: true,
			Platform: "CPA", PlatformID: "123e4567-e89b-12d3-a456-426614174000",
			AdminURL:      "https://admin.example:9443",
			ProxyTemplate: "socks5h://{platform}.xai-{account}:{proxy_token}@resin:2260",
			AdminToken:    "admin-secret", ProxyToken: "proxy-secret",
		}
		state.Proxy.ResinSource = coreproxy.ResinSourceState{ID: "resin", Name: "Legacy Resin", Revision: 2, ConfigVersion: 3, Enabled: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := &Service{}
	if err := service.ensureProxyStateDefaults(store, coreproxy.AutoBindConfig{}, ResinSettings{}); err != nil {
		t.Fatal(err)
	}
	projection := store.ProxySourcesSnapshot()
	if !projection.ResinSourcesInitialized {
		t.Fatal("legacy Resin state was not marked migrated")
	}
	if len(projection.Templates) != 1 || projection.Templates[0].Kind != coreproxy.TemplateResin {
		t.Fatalf("legacy Resin was not migrated to one template: %#v", projection.Templates)
	}
	template := projection.Templates[0]
	config, ok := projection.ResinSources[template.ID]
	if !ok {
		t.Fatalf("migrated Resin config is missing: %#v", projection.ResinSources)
	}
	if template.UsernamePattern != "CPA.xai-{account}-{suffix}" {
		t.Fatalf("unexpected migrated username pattern: %q", template.UsernamePattern)
	}
	if template.Revision != 2 || template.ConfigVersion != 3 {
		t.Fatalf("legacy source versions were not preserved: revision=%d config=%d", template.Revision, template.ConfigVersion)
	}
	if config.PlatformID != "123e4567-e89b-12d3-a456-426614174000" || config.AccountFormat != "xai-{account}-{suffix}" {
		t.Fatalf("migrated Resin connection fields were lost: %#v", config)
	}
	if config.AdminToken != "admin-secret" || template.Password != "proxy-secret" {
		t.Fatalf("migrated Resin credentials were lost: %#v", config)
	}
	reserved := false
	random := false
	for _, placeholder := range template.Placeholders {
		if placeholder.Name == coreproxy.ReservedAccountPlaceholder {
			reserved = placeholder.Kind == coreproxy.PlaceholderReserved
		}
		if placeholder.Name == "suffix" {
			random = placeholder.Kind == coreproxy.PlaceholderRandom && placeholder.Random != nil && placeholder.Random.Length == 12
		}
	}
	if !reserved || !random {
		t.Fatalf("migrated placeholders are incomplete: %#v", template.Placeholders)
	}
}

func TestConfigOnlyResinStateMigratesToTemplateSource(t *testing.T) {
	store := statepkg.NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Load(statepkg.Policy{}, "test"); err != nil {
		t.Fatal(err)
	}
	service := &Service{}
	settings := ResinSettings{
		Enabled: true, Name: "Config Resin",
		Platform: "CPA", PlatformID: "123e4567-e89b-12d3-a456-426614174000",
		AdminURL:      "https://admin.example:9443",
		ProxyTemplate: "socks5h://{platform}.xai-{account}:{proxy_token}@resin:2260",
		AdminToken:    "admin-secret", ProxyToken: "proxy-secret",
	}
	if err := service.ensureProxyStateDefaults(store, coreproxy.AutoBindConfig{}, settings); err != nil {
		t.Fatal(err)
	}
	projection := store.ProxySourcesSnapshot()
	if !projection.ResinSourcesInitialized || len(projection.Templates) != 1 {
		t.Fatalf("config-only Resin was not migrated: initialized=%t templates=%#v", projection.ResinSourcesInitialized, projection.Templates)
	}
	template := projection.Templates[0]
	config, ok := projection.ResinSources[template.ID]
	if !ok {
		t.Fatalf("config-only Resin connection is missing: %#v", projection.ResinSources)
	}
	if template.UsernamePattern != "CPA.xai-{account}-{suffix}" || config.AccountFormat != "xai-{account}-{suffix}" {
		t.Fatalf("config-only Resin format was not migrated: template=%q config=%q", template.UsernamePattern, config.AccountFormat)
	}
	if template.Protocol != coreproxy.ProtocolSOCKS5H || template.Host != "resin" || template.Port != 2260 {
		t.Fatalf("legacy proxy endpoint was replaced by the admin endpoint: %#v", template)
	}
	if config.PlatformID != settings.PlatformID || config.AdminToken != settings.AdminToken || template.Password != settings.ProxyToken {
		t.Fatalf("config-only Resin credentials were not migrated: template=%#v config=%#v", template, config)
	}
}
