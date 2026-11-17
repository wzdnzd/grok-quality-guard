package state

import (
	proxy "grok-quality-guard/internal/proxy"
)

func bindingForAccount(account AccountState) (proxy.Binding, bool) {
	if account.Proxy.SourceID == "" {
		return proxy.CloneBinding(account.Proxy), false
	}
	return proxy.CloneBinding(account.Proxy), true
}

func proxyBindingSource(account AccountState) (string, bool) {
	if account.Proxy.SourceID == "" {
		return "", false
	}
	return account.Proxy.SourceID, true
}
func proxyScopeContains(scope string, kind proxy.SourceKind) bool {
	return scope == "all" || scope == string(kind)+"s" || (scope == "resin" && kind == proxy.SourceResin)
}
