package state

import (
	"time"

	proxy "grok-quality-guard/internal/proxy"
)

type ProxyAccountSummary struct {
	AuthID      string
	State       string
	DisableKind string
	Proxy       proxy.Binding
	AutoBind    proxy.AutoBindAccountState
	Pending     bool
}

// ProxyBindingProjection is the small immutable slice needed by binding
// resolution. It deliberately omits generated values, full lease objects and
// unrelated quality state so list requests do not copy large account values.
type ProxyBindingProjection struct {
	AuthID           string
	SourceID         string
	SourceKind       proxy.SourceKind
	SourceRevision   uint64
	Generation       uint64
	EndpointHash     string
	LeaseAccount     string
	AutoBindExcluded bool
	NextAttemptAt    time.Time
	LastError        string
	LastErrorCode    string
	Pending          bool
}

func (s ProxyAccountSummary) Managed() bool {
	return s.Proxy.SourceID != ""
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
			values[i] = ProxyAccountSummary{AuthID: id, State: a.State, DisableKind: a.DisableKind, Proxy: b, AutoBind: a.AutoBind, Pending: s.proxyPending[id] != ""}
		} else {
			values[i].AuthID = id
		}
	}
	return values
}

func (s *Store) ProxyBindingProjectionsForIDs(ids []string) []ProxyBindingProjection {
	values := make([]ProxyBindingProjection, len(ids))
	if s == nil {
		return values
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i, id := range ids {
		values[i].AuthID = id
		account := s.state.Accounts[id]
		if account == nil {
			continue
		}
		binding := account.Proxy
		values[i].SourceID = binding.SourceID
		values[i].SourceKind = binding.SourceKind
		values[i].SourceRevision = binding.SourceRevision
		values[i].Generation = binding.Generation
		values[i].EndpointHash = binding.EndpointHash
		if binding.Lease != nil {
			values[i].LeaseAccount = binding.Lease.Account
		}
		values[i].AutoBindExcluded = account.AutoBind.Excluded
		values[i].NextAttemptAt = account.AutoBind.NextAttemptAt
		values[i].LastError = account.AutoBind.LastError
		values[i].LastErrorCode = account.AutoBind.LastErrorCode
		values[i].Pending = s.proxyPending[id] != ""
	}
	return values
}
