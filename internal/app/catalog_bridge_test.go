package app

import (
	catalogapp "grok-quality-guard/internal/app/catalog"
	appmodel "grok-quality-guard/internal/app/model"
)

const guardCatalogMissingRetention = catalogapp.GuardCatalogMissingRetention

func (p *Plugin) syncCatalogState(accounts []appmodel.Account) error {
	return p.catalogSvc.SyncState(accounts)
}

func readAuthProxyURL(path string) (string, bool) { return catalogapp.ReadAuthProxyURL(path) }
