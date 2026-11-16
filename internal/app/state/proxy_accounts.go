package state

import proxy "grok-quality-guard/internal/proxy"

type ProxyAccountSummary struct {
	AuthID      string
	State       string
	DisableKind string
	Proxy       proxy.Binding
	Resin       ResinState
	AutoBind    proxy.AutoBindAccountState
	Pending     bool
}

func (s ProxyAccountSummary) Managed() bool {
	return s.Proxy.SourceID != "" || s.Resin.Managed || s.Resin.Account != ""
}

func (s *Store) ProxyAccountsForIDs(ids []string) []ProxyAccountSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]ProxyAccountSummary, len(ids))
	for i, id := range ids {
		if a := s.state.Accounts[id]; a != nil {
			b := a.Proxy
			b.ResolvedValues = nil
			if b.Lease != nil {
				lease := *b.Lease
				b.Lease = &lease
			}
			values[i] = ProxyAccountSummary{AuthID: id, State: a.State, DisableKind: a.DisableKind, Proxy: b, Resin: a.Resin, AutoBind: a.AutoBind, Pending: s.proxyPending[id] != ""}
		} else {
			values[i].AuthID = id
		}
	}
	return values
}
