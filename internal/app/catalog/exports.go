package catalog

import (
	"context"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (s *Service) RunRefreshLoop(ctx context.Context)        { s.refreshLoop(ctx) }
func (s *Service) Ensure(ctx context.Context) error          { return s.ensureCatalog(ctx) }
func (s *Service) Snapshot() []Account                       { return s.catalogSnapshot() }
func (s *Service) FindAccount(authID string) (Account, bool) { return s.findAccount(authID) }
func (s *Service) ResolveAuth(ctx context.Context, authID string) (Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error) {
	return s.resolveAuth(ctx, authID)
}
func (s *Service) SyncState(accounts []Account) error { return s.syncCatalogState(accounts) }
func (s *Service) SetGrokAuthModels(credentials []pluginapi.HostAuthModels) {
	s.setGrokAuthModels(credentials)
}

const GuardCatalogMissingRetention = guardCatalogMissingRetention

func ReadAuthProxyURL(path string) (string, bool) {
	proxyURL, _, loaded := readAuthMeta(path)
	return proxyURL, loaded
}

// WaitAuthModelsRefreshContext waits until the in-flight host.auth.models
// refresh sequence has completed. It returns false when ctx expires first.
// Runtime paths use the asynchronous refresh; lifecycle code uses this to avoid
// tearing down shared host callbacks underneath it.
func (s *Service) WaitAuthModelsRefreshContext(ctx context.Context) bool {
	if s == nil {
		return true
	}
	for {
		if errAcquire := acquireGate(ctx, s.authModelsRefreshGate); errAcquire != nil {
			return false
		}
		s.authModelsMu.Lock()
		pending := s.authModelsRefreshPending
		if pending {
			// A catalog refresh requested a follow-up after the previous
			// coordinator released the gate but before this waiter acquired it.
			// Transfer the acquired gate to a new coordinator instead of
			// silently dropping the pending refresh.
			s.authModelsRefreshPending = false
			s.authModelsMu.Unlock()
			go s.runGrokAuthModelsRefresh(true)
			continue
		}
		// Release while holding authModelsMu so a concurrent start cannot set
		// pending after the check above and observe the gate still held by this
		// waiter. That window would otherwise strand the queued refresh.
		releaseGate(s.authModelsRefreshGate)
		s.authModelsMu.Unlock()
		return true
	}
}

// WaitAuthModelsRefresh is the unbounded form used by tests and code paths that
// already have an external lifetime bound.
func (s *Service) WaitAuthModelsRefresh() {
	_ = s.WaitAuthModelsRefreshContext(context.Background())
}

// WaitAuthMetaRefreshContext waits until the asynchronous auth-file metadata
// enrichment pass has completed. A catalog entry is usable before this returns;
// only proxy/OAuth enrichment fields may still be pending.
func (s *Service) WaitAuthMetaRefreshContext(ctx context.Context) bool {
	if s == nil {
		return true
	}
	for {
		if errAcquire := acquireGate(ctx, s.authMetaRefreshGate); errAcquire != nil {
			return false
		}
		s.authMetaRefreshMu.Lock()
		pending := s.authMetaRefreshPending
		if pending {
			// Preserve a queued catalog generation instead of letting the
			// waiter consume the only gate token and strand the request.
			s.authMetaRefreshPending = false
			s.authMetaRefreshMu.Unlock()
			go s.runAuthMetaRefresh()
			continue
		}
		// Release while holding authMetaRefreshMu so a concurrent catalog pass
		// cannot queue work between the pending check and gate release.
		releaseGate(s.authMetaRefreshGate)
		s.authMetaRefreshMu.Unlock()
		return true
	}
}

func (s *Service) WaitAuthMetaRefresh() {
	_ = s.WaitAuthMetaRefreshContext(context.Background())
}
