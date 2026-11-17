package proxy

import (
	"strings"

	proxy "grok-quality-guard/internal/proxy"
)

func (s *Service) ensureProxyStateDefaults(store *StateStore, autoBind proxy.AutoBindConfig, resinSettings ResinSettings) error {
	if store == nil {
		return nil
	}
	return store.UpdateIfChanged(func(state *State) (bool, error) {
		changed := false
		if state.Proxy.AutoBind.SourceScope == "" {
			state.Proxy.AutoBind = autoBind
			changed = true
		}
		if !state.Resin.Configured && state.Proxy.ResinSource.Enabled != resinSettings.Enabled {
			state.Proxy.ResinSource.Enabled = resinSettings.Enabled
			changed = true
		}
		if !state.Proxy.ResinSourcesInitialized {
			legacySource := state.Resin.Configured || state.Resin.LocalOnly || resinSettings.Enabled || resinSettings.LocalOnly
			hasResinTemplate := false
			for _, template := range state.Proxy.Templates {
				if template.Kind == proxy.TemplateResin {
					hasResinTemplate = true
					break
				}
			}
			if hasResinTemplate {
				state.Proxy.ResinSourcesInitialized = true
				changed = true
			}
			if legacySource {
				if state.Proxy.ResinSources == nil {
					state.Proxy.ResinSources = make(map[string]proxy.ResinSourceConfig)
				}
				legacy := state.Resin
				if legacy.Name == "" {
					legacy.Name = state.Proxy.ResinSource.Name
				}
				if legacy.Enabled == false && state.Proxy.ResinSource.Enabled {
					legacy.Enabled = true
				}
				if !legacy.Configured {
					legacy.Name = firstNonEmptyString(legacy.Name, resinSettings.Name, "Resin")
					legacy.LocalOnly = legacy.LocalOnly || resinSettings.LocalOnly
					legacy.Platform = firstNonEmptyString(legacy.Platform, resinSettings.Platform)
					legacy.PlatformID = firstNonEmptyString(legacy.PlatformID, resinSettings.PlatformID)
					legacy.AdminURL = firstNonEmptyString(legacy.AdminURL, resinSettings.AdminURL)
					legacy.ProxyTemplate = firstNonEmptyString(legacy.ProxyTemplate, resinSettings.ProxyTemplate)
					legacy.AdminToken = firstNonEmptyString(legacy.AdminToken, resinSettings.AdminToken)
					legacy.ProxyToken = firstNonEmptyString(legacy.ProxyToken, resinSettings.ProxyToken)
					if legacy.ValidatedAt.IsZero() {
						legacy.ValidatedAt = resinSettings.ValidatedAt
					}
				}
				accountFormat := legacyResinAccountFormat(legacy)
				input := proxyResinInput{
					Name: legacy.Name, Enabled: &legacy.Enabled, LocalOnly: legacy.LocalOnly,
					Platform: legacy.Platform, AdminURL: legacy.AdminURL, AdminToken: legacy.AdminToken,
					ProxyToken: legacy.ProxyToken, AccountFormat: accountFormat,
					Placeholders: []proxy.Placeholder{{Name: "suffix", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 12}}},
					UpdatePolicy: proxy.TemplateUpdateKeepBound,
				}
				template, config, errBuild := buildResinSource(input, nil, nil)
				if errBuild == nil {
					template.ID = "resin"
					template.Revision = max(1, state.Proxy.ResinSource.Revision)
					template.ConfigVersion = max(1, state.Proxy.ResinSource.ConfigVersion)
					config.PlatformID = strings.TrimSpace(legacy.PlatformID)
					if legacyTemplate, ok := legacyResinTemplateEndpoint(legacy.ProxyTemplate); ok {
						template.Protocol = legacyTemplate.Protocol
						template.Host = legacyTemplate.Host
						template.Port = legacyTemplate.Port
					}
					if !legacy.ValidatedAt.IsZero() {
						template.ValidatedAt = legacy.ValidatedAt
					}
					state.Proxy.Templates = append(state.Proxy.Templates, template)
					state.Proxy.ResinSources[template.ID] = config
					state.Proxy.ResinSourcesInitialized = true
					changed = true
				}
			}
		}
		return changed, nil
	})
}
