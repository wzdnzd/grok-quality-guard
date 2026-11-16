package app

import (
	"context"
	"encoding/json"
	"time"

	proxyapp "grok-quality-guard/internal/app/proxy"
	appstate "grok-quality-guard/internal/app/state"
	proxy "grok-quality-guard/internal/proxy"
)

func proxyURLHash(value string) string { return proxyapp.URLHash(value) }
func maskProxyURL(value string) string { return proxyapp.MaskProxyURL(value) }
func resinCleanupConfigured(settings appstate.ResinSettings) bool {
	return proxyapp.ResinCleanupConfigured(settings)
}
func proxyBindOptionsFromParams(params map[string]any) (proxyapp.BindOptions, error) {
	return proxyapp.BindOptionsFromParams(params)
}

func (p *Plugin) selectProxySource(opts proxyapp.BindOptions, excludeHash string) (*proxyapp.SourceCandidate, []string) {
	return p.proxySvc.SelectSource(opts, excludeHash)
}

func (p *Plugin) createProxyNode(input proxyapp.NodeInput) (proxy.Node, error) {
	return p.proxySvc.CreateNode(input)
}
func (p *Plugin) importProxyNodes(inputs []proxyapp.NodeInput, text string) (map[string]any, error) {
	return p.proxySvc.ImportNodes(inputs, text)
}
func (p *Plugin) updateProxyNode(input proxyapp.NodeInput) (proxy.Node, map[string]any, error) {
	return p.proxySvc.UpdateNode(input)
}
func (p *Plugin) createProxyTemplate(input proxyapp.TemplateInput) (proxy.Template, error) {
	return p.proxySvc.CreateTemplate(input)
}
func (p *Plugin) updateProxyTemplate(input proxyapp.TemplateInput) (proxy.Template, map[string]any, error) {
	return p.proxySvc.UpdateTemplate(input)
}
func (p *Plugin) bindProxy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.Bind(ctx, authID, opts)
}

func (p *Plugin) clearProxy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.Clear(ctx, authID, opts)
}
func (p *Plugin) clearProxyWithPolicy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.ClearWithPolicy(ctx, authID, opts)
}
func (p *Plugin) rotateProxy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.Rotate(ctx, authID, opts)
}
func (p *Plugin) updateProxyAutoBind(params map[string]any) (proxy.AutoBindConfig, bool, error) {
	return p.proxySvc.UpdateAutoBind(params)
}

func (p *Plugin) proxyApplyTemplateToAccounts(ctx context.Context, template proxy.Template, ids []string) []proxyapp.BindingResult {
	return p.proxySvc.ApplyTemplateToAccounts(ctx, template, ids)
}

func (p *Plugin) actionProxySourcePreview(params map[string]any) (any, int, error) {
	return p.proxySvc.ActionSourcePreview(params)
}

func (p *Plugin) actionProxyImportNodes(params map[string]any) (any, int, error) {
	return p.proxySvc.ActionImportNodes(params)
}

func (p *Plugin) actionResinConfigSave(ctx context.Context, params map[string]any) (any, int, error) {
	return p.proxySvc.ActionResinConfigSave(ctx, params)
}

func (p *Plugin) ResinConfig() appstate.ResinSettings { return p.proxySvc.ResinConfig() }
func (p *Plugin) BindResin(ctx context.Context, authID string, dryRun bool) (proxyapp.ResinBindResult, error) {
	return p.proxySvc.BindResin(ctx, authID, dryRun)
}
func (p *Plugin) ClearResinProxy(ctx context.Context, authID string) (proxyapp.ResinClearResult, error) {
	return p.proxySvc.ClearResinProxy(ctx, authID)
}
func (p *Plugin) BindUnboundResin(ctx context.Context, dryRun bool) ([]proxyapp.ResinBindResult, error) {
	return p.proxySvc.BindUnboundResin(ctx, dryRun)
}
func (p *Plugin) RotateResin(ctx context.Context, authID string) error {
	return p.proxySvc.RotateResin(ctx, authID)
}

const resinSourceID = proxyapp.ResinSourceID

func (p *Plugin) proxyCatalog() *proxyapp.SourceCatalog { return p.proxySvc.Catalog() }
func (p *Plugin) proxyAutoBindScan(ctx context.Context) { p.proxySvc.AutoBindScan(ctx) }
func (p *Plugin) deleteProxyNode(id string, force bool) (map[string]any, error) {
	return p.proxySvc.DeleteProxyNode(id, force)
}

func (p *Plugin) proxySourceDetails(id string) (any, error)      { return p.proxySvc.SourceDetails(id) }
func (p *Plugin) reconcileProxyOperations(ctx context.Context)   { p.proxySvc.ReconcileOperations(ctx) }
func (p *Plugin) proxyExistingNode(id string) (proxy.Node, bool) { return p.proxySvc.ExistingNode(id) }

func managedResinBinding(raw map[string]json.RawMessage, stateAccount appstate.AccountState, cfg appstate.ResinSettings) (string, bool) {
	return proxyapp.ManagedResinBinding(raw, stateAccount, cfg)
}
func rewriteResinProxyAccount(proxyURL, account string) (string, error) {
	return proxyapp.RewriteResinProxyAccount(proxyURL, account)
}
func deleteResinLease(ctx context.Context, adminURL, platformID, adminToken, account string) error {
	return proxyapp.DeleteResinLease(ctx, adminURL, platformID, adminToken, account)
}
func (p *Plugin) processDueResinLeaseDeletes(ctx context.Context, now time.Time) int {
	return p.proxySvc.ProcessDueResinLeaseDeletes(ctx, now)
}

var socks5Authenticate = proxyapp.Socks5Authenticate

func randomBase36(length int) (string, error) { return proxyapp.RandomBase36(length) }
func validateResinPlatform(ctx context.Context, settings appstate.ResinSettings) (string, error) {
	return proxyapp.ValidateResinPlatform(ctx, settings)
}
func resolveResinPlatformByName(ctx context.Context, settings appstate.ResinSettings) (appstate.ResinSettings, error) {
	return proxyapp.ResolveResinPlatformByName(ctx, settings)
}
func resinLeaseDeleteBackoff(attempts int) time.Duration {
	return proxyapp.ResinLeaseDeleteBackoff(attempts)
}
func (p *Plugin) enqueueResinLeaseDelete(authID string, settings appstate.ResinSettings, account string) (bool, error) {
	return p.proxySvc.EnqueueResinLeaseDelete(authID, settings, account)
}
