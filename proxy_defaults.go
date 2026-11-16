package main

import (
	proxy "grok-quality-guard/internal/proxy"
)

func (p *Plugin) ensureProxyStateDefaults(store *StateStore, autoBind proxy.AutoBindConfig, resinSettings ResinSettings) error {
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
			state.Proxy.ResinSource = proxy.ResinSourceState{ID: resinSourceID, Name: "Resin", Revision: 1, ConfigVersion: 1, Enabled: resinSettings.Enabled}
			changed = true
		}
		if !state.Resin.Configured && state.Proxy.ResinSource.Enabled != resinSettings.Enabled {
			state.Proxy.ResinSource.Enabled = resinSettings.Enabled
			changed = true
		}
		return changed, nil
	})
}
