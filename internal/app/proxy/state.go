package proxy

import (
	proxy "grok-quality-guard/internal/proxy"
)

func (s *Service) ensureProxyStateDefaults(store *StateStore, autoBind proxy.AutoBindConfig) error {
	if store == nil {
		return nil
	}
	return store.UpdateIfChanged(func(state *State) (bool, error) {
		if state.Proxy.AutoBind.SourceScope != "" {
			return false, nil
		}
		state.Proxy.AutoBind = autoBind
		return true, nil
	})
}
