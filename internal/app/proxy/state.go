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
		if state.Proxy.ResinSource.ID == "" {
			name := strings.TrimSpace(resinSettings.Name)
			if name == "" {
				name = "Resin"
			}
			state.Proxy.ResinSource = proxy.ResinSourceState{ID: "resin", Name: name, Revision: 1, ConfigVersion: 1, Enabled: resinSettings.Enabled}
			changed = true
		}
		if !state.Resin.Configured && state.Proxy.ResinSource.Enabled != resinSettings.Enabled {
			state.Proxy.ResinSource.Enabled = resinSettings.Enabled
			changed = true
		}
		return changed, nil
	})
}
