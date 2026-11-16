package state

import (
	"strings"

	proxy "grok-quality-guard/internal/proxy"
)

func bindingForAccount(account AccountState) (proxy.Binding, bool) {
	if account.Proxy.SourceID != "" {
		return proxy.CloneBinding(account.Proxy), true
	}
	if account.Resin.Managed || account.Resin.Account != "" {
		return proxy.Binding{
			AuthID:     account.AuthID,
			SourceKind: proxy.SourceResin,
			Generation: uint64(max(account.Resin.Generation, 0)),
			Lease:      &proxy.LeaseRef{Provider: "resin", Account: account.Resin.Account},
		}, true
	}
	return proxy.CloneBinding(account.Proxy), false
}

func proxyBindingSource(account AccountState) (string, bool) {
	binding, ok := bindingForAccount(account)
	if !ok {
		return "", false
	}
	return binding.SourceID, true
}
func proxyScopeContains(scope string, kind proxy.SourceKind) bool {
	return scope == "all" || scope == string(kind)+"s" || (scope == "resin" && kind == proxy.SourceResin)
}

func resinCleanupConfigured(settings ResinSettings) bool {
	return !settings.LocalOnly &&
		strings.TrimSpace(settings.AdminURL) != "" &&
		strings.TrimSpace(settings.PlatformID) != "" &&
		strings.TrimSpace(settings.AdminToken) != ""
}
