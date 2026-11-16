package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"

	resinpkg "grok-quality-guard/internal/proxy/resin"

	"regexp"
	"strings"
	"time"
)

var resinAccountChars = regexp.MustCompile(`[^a-z0-9_-]+`)

const base36Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

type resinDeleteError = resinpkg.DeleteError

type ResinClearResult struct {
	AuthID                  string `json:"auth_id"`
	Account                 string `json:"account,omitempty"`
	ProxyURL                string `json:"proxy_url,omitempty"`
	Cleared                 bool   `json:"cleared"`
	RemoteLeaseDeleteQueued bool   `json:"remote_lease_delete_queued,omitempty"`
	Skipped                 bool   `json:"skipped,omitempty"`
	Error                   string `json:"error,omitempty"`
	Committed               bool   `json:"committed,omitempty"`
	NeedsReconcile          bool   `json:"needs_reconcile,omitempty"`
}

type ResinBindResult struct {
	AuthID   string `json:"auth_id"`
	Account  string `json:"account"`
	ProxyURL string `json:"proxy_url,omitempty"`
	DryRun   bool   `json:"dry_run"`
	Conflict bool   `json:"conflict,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (p *Plugin) BindResin(ctx context.Context, authID string, dryRun bool) (ResinBindResult, error) {
	cfg := p.ResinConfig()
	if !cfg.Enabled {
		return ResinBindResult{AuthID: authID, DryRun: dryRun, Error: "resin is disabled"}, proxyError("resin_disabled", 409, "resin is disabled")
	}
	if cfg.LocalOnly {
		return ResinBindResult{AuthID: authID, DryRun: dryRun, Error: "local rotation does not create new proxy bindings"}, proxyError("resin_local_only", 409, "resin API mode is required to bind new proxies")
	}
	result, err := p.bindProxy(ctx, authID, proxyBindOptions{SourceScope: "resin", SourceIDs: []string{resinSourceID}, DryRun: dryRun})
	account := result.LeaseAccount
	if account == "" {
		state := p.stateStore().ProxyAccount(authID)
		if state.Proxy.Lease != nil {
			account = state.Proxy.Lease.Account
		} else {
			account = state.Resin.Account
		}
	}
	return ResinBindResult{AuthID: authID, Account: account, ProxyURL: result.ProxyURL, DryRun: dryRun, Conflict: result.Status == "external" || result.Status == "drifted", Error: result.Error}, err
}

func (p *Plugin) ClearResinProxy(ctx context.Context, authID string) (ResinClearResult, error) {
	state := p.stateStore().ProxyAccount(authID)
	binding, _ := bindingForAccount(state)
	account := ""
	if binding.Lease != nil {
		account = binding.Lease.Account
	}
	result, err := p.clearProxy(ctx, authID, proxyBindOptions{AllowExternal: true, Action: "resin_clear"})
	return ResinClearResult{AuthID: authID, Account: account, ProxyURL: result.ProxyURL, Cleared: result.Committed, Skipped: result.Status == "skipped", RemoteLeaseDeleteQueued: result.RemoteLeaseDeleteQueued, Error: result.Error, Committed: result.Committed, NeedsReconcile: result.NeedsReconcile}, err
}

func (p *Plugin) BindUnboundResin(ctx context.Context, dryRun bool) ([]ResinBindResult, error) {
	cfg := p.ResinConfig()
	if !cfg.Enabled {
		return nil, proxyError("resin_disabled", 409, "resin is disabled")
	}
	if cfg.LocalOnly {
		return nil, proxyError("resin_local_only", 409, "resin API mode is required to bind new proxies")
	}
	items, err := p.proxyBindUnbound(ctx, proxyBindOptions{SourceScope: "resin", SourceIDs: []string{resinSourceID}, DryRun: dryRun})
	results := make([]ResinBindResult, len(items))
	for i, item := range items {
		state := p.stateStore().ProxyAccount(item.AuthID)
		account := item.LeaseAccount
		if account == "" && state.Proxy.Lease != nil {
			account = state.Proxy.Lease.Account
		}
		results[i] = ResinBindResult{AuthID: item.AuthID, Account: account, ProxyURL: item.ProxyURL, DryRun: dryRun, Error: item.Error}
	}
	return results, err
}

func (p *Plugin) RotateResin(ctx context.Context, authID string) error {
	if !p.ResinConfig().Enabled {
		return proxyError("resin_disabled", 409, "resin is disabled")
	}
	_, err := p.rotateProxy(ctx, authID, proxyBindOptions{SourceScope: "resin", ExpectedSourceID: resinSourceID})
	return err
}

func resinAccountFromProxyURL(proxyURL string) string {
	return resinpkg.AccountFromProxyURL(proxyURL)
}

func managedResinBinding(raw map[string]json.RawMessage, stateAccount AccountState, cfg ResinSettings) (string, bool) {
	return managedResinBindingWithProxy(authRawString(raw, "proxy_url"), stateAccount, cfg)
}

func managedResinBindingWithProxy(proxyURL string, stateAccount AccountState, cfg ResinSettings) (string, bool) {
	return managedResinBindingForState(proxyURL, stateAccount.Resin, cfg)
}

func managedResinBindingForState(proxyURL string, resinState ResinState, cfg ResinSettings) (string, bool) {
	storedAccount := strings.TrimSpace(resinState.Account)
	proxyURL = strings.TrimSpace(proxyURL)
	proxyAccount := resinAccountFromProxyURL(proxyURL)
	managed := resinState.Managed || storedAccount != ""
	if managed {
		if proxyAccount != "" {
			return proxyAccount, true
		}
		return storedAccount, true
	}
	if resinProxyMatchesSettings(proxyURL, cfg) {
		return proxyAccount, proxyAccount != ""
	}
	return "", false
}

func resinProxyMatchesSettings(proxyURL string, cfg ResinSettings) bool {
	if strings.TrimSpace(proxyURL) == "" || cfg.LocalOnly {
		return false
	}
	parsedProxy, errProxy := url.Parse(strings.TrimSpace(proxyURL))
	if errProxy != nil || parsedProxy.Scheme == "" || parsedProxy.Host == "" {
		return false
	}
	rendered, errRender := renderResinProxy(cfg, "grok-validation")
	if errRender != nil {
		return false
	}
	parsedSettings, errSettings := url.Parse(rendered)
	if errSettings != nil || parsedSettings.Scheme == "" || parsedSettings.Host == "" {
		return false
	}
	return parsedProxy.Scheme == parsedSettings.Scheme && strings.EqualFold(parsedProxy.Host, parsedSettings.Host)
}

func rewriteResinProxyAccount(proxyURL, account string) (string, error) {
	return resinpkg.RewriteAccount(proxyURL, account)
}

func (p *Plugin) generateResinAccount(authID string, raw map[string]json.RawMessage) (string, error) {
	stateAccount, _ := p.stateAccount(authID)
	return p.generateResinAccountAvoiding(authID, raw, stateAccount.Resin.Account)
}

func (p *Plugin) generateResinAccountAvoiding(authID string, raw map[string]json.RawMessage, oldAccount string) (string, error) {
	base := resinAccountBase(raw)
	used := p.stateStore().ResinAccounts()
	if used == nil {
		used = make(map[string]struct{})
	}
	for attempt := 0; attempt < 32; attempt++ {
		suffix, errSuffix := randomBase36(4)
		if errSuffix != nil {
			return "", errSuffix
		}
		candidate := "grok-" + base + "-" + suffix
		if len(candidate) > 128 {
			candidate = candidate[:123] + "-" + suffix
		}
		if candidate == oldAccount {
			continue
		}
		if _, exists := used[candidate]; exists {
			continue
		}
		return candidate, nil
	}
	stateAccount, _ := p.stateAccount(authID)
	suffixSeed := fmt.Sprintf("%s|%d|%d", authID, time.Now().UnixNano(), stateAccount.Resin.Generation)
	sum := sha256.Sum256([]byte(suffixSeed))
	for offset := 0; offset+4 <= len(sum); offset += 4 {
		candidate := "grok-" + base + "-" + base36FromBytes(sum[offset:offset+4], 4)
		if candidate == oldAccount {
			continue
		}
		if _, exists := used[candidate]; exists {
			continue
		}
		return candidate, nil
	}
	return "", fmt.Errorf("generate unique resin account failed")
}

func resinAccountBase(raw map[string]json.RawMessage) string {
	source := authEmail(raw)
	if source == "" {
		source = authLabel(raw)
	}
	if source == "" {
		source = "auth_" + authSubjectHash(raw)
	}
	if at := strings.IndexByte(source, '@'); at >= 0 {
		source = source[:at]
	}
	source = strings.ToLower(strings.TrimSpace(source))
	source = resinAccountChars.ReplaceAllString(source, "_")
	for strings.Contains(source, "__") {
		source = strings.ReplaceAll(source, "__", "_")
	}
	source = strings.Trim(source, "_")
	if source == "" {
		source = "auth_" + authSubjectHash(raw)
	}
	if len(source) > 48 {
		source = source[:48]
		source = strings.TrimRight(source, "_")
	}
	return source
}

func randomBase36(length int) (string, error) {
	if length <= 0 {
		return "", nil
	}
	limit := big.NewInt(int64(len(base36Alphabet)))
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		value, errValue := rand.Int(rand.Reader, limit)
		if errValue != nil {
			return "", errValue
		}
		result[i] = base36Alphabet[value.Int64()]
	}
	return string(result), nil
}

func base36FromBytes(bytes []byte, length int) string {
	if length <= 0 || len(bytes) == 0 {
		return ""
	}
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		result[i] = base36Alphabet[int(bytes[i%len(bytes)])%len(base36Alphabet)]
	}
	return string(result)
}

func renderResinProxy(cfg ResinSettings, account string) (string, error) {
	return resinpkg.RenderProxy(cfg.ProxyTemplate, cfg.Platform, account, cfg.ProxyToken)
}

func deleteResinLease(ctx context.Context, adminURL, platformID, adminToken, account string) error {
	return resinpkg.DeleteLease(ctx, adminURL, platformID, adminToken, account)
}

func maskProxyURL(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parsed, errParse := url.Parse(value)
	if errParse != nil {
		return "[redacted]"
	}
	if parsed.Host == "" {
		return parsed.Scheme + "://[redacted]"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func maskAccountIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "@") {
		return maskEmail(value)
	}
	return maskAuthID(value)
}

func maskEmail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	at := strings.IndexByte(value, '@')
	if at <= 0 {
		return maskAuthID(value)
	}
	local := value[:at]
	domain := value[at:]
	switch {
	case len(local) <= 2:
		return local[:1] + "***" + domain
	case len(local) >= 8:
		return local[:3] + "***" + local[len(local)-2:] + domain
	default:
		return local[:2] + "***" + local[len(local)-1:] + domain
	}
}

func maskAuthID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12] + "..." + value[len(value)-4:]
}
