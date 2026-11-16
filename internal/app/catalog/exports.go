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

func ReadAuthProxyURL(path string) (string, bool) { return readAuthProxyURL(path) }
