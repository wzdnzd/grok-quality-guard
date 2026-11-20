package proxy

import (
	"context"
	"net"
	"time"

	statepkg "grok-quality-guard/internal/app/state"
	proxy "grok-quality-guard/internal/proxy"
)

// Public aliases keep proxy domain types usable by the application composition
// package without moving implementation details into the root package.
type (
	BindOptions     = proxyBindOptions
	BindingResult   = proxyBindingResult
	NodeInput       = proxyNodeInput
	TemplateInput   = proxyTemplateInput
	SourceView      = proxySourceView
	CatalogRow      = proxyCatalogRow
	Observation     = proxyObservation
	SourceCandidate = proxySourceCandidate
	ManagementError = proxyManagementError
)

func URLHash(value string) string { return proxyURLHash(value) }
func LeaseDeleteKey(adminURL, platformID, account string) string {
	return resinLeaseDeleteKey(adminURL, platformID, account)
}
func NewResinLeaseDelete(authID string, snapshot proxy.ResinSnapshot, account string) ResinLeaseDelete {
	return newResinLeaseDelete(authID, snapshot, account)
}
func BindingForAccount(account AccountState) (proxy.Binding, bool) {
	return bindingForAccount(account)
}
func ResinCleanupConfigured(snapshot proxy.ResinSnapshot) bool {
	return resinCleanupConfigured(snapshot)
}
func BindOptionsFromParams(params map[string]any) (BindOptions, error) {
	return proxyBindOptionsFromParams(params)
}
func MaskProxyURL(value string) string { return maskProxyURL(value) }

func (s *Service) WakeAutoBind()                               { s.wakeProxyAutoBind() }
func (s *Service) RunAutoBindLoop(ctx context.Context)         { s.proxyAutoBindLoop(ctx) }
func (s *Service) RunResinLeaseDeleteLoop(ctx context.Context) { s.resinLeaseDeleteLoop(ctx) }
func (s *Service) WakeResinLeaseDeleteLoop()                   { s.wakeResinLeaseDeleteLoop() }
func (s *Service) EnsureStateDefaults(store *statepkg.Store, autoBind proxy.AutoBindConfig) error {
	return s.ensureProxyStateDefaults(store, autoBind)
}
func (s *Service) SelectSource(opts BindOptions, excludeHash string) (*SourceCandidate, []string) {
	return s.selectProxySource(opts, excludeHash)
}
func (s *Service) BindingForAccount(account AccountState) (proxy.Binding, bool) {
	return s.proxyBindingForAccount(account)
}
func (s *Service) CreateNode(input NodeInput) (proxy.Node, error) {
	return s.createProxyNode(input)
}

func (s *Service) ImportNodes(inputs []NodeInput, text string) (map[string]any, error) {
	return s.importProxyNodes(inputs, text)
}
func (s *Service) UpdateNode(input NodeInput) (proxy.Node, map[string]any, error) {
	return s.updateProxyNode(input)
}
func (s *Service) CreateTemplate(input TemplateInput) (proxy.Template, error) {
	return s.createProxyTemplate(input)
}
func (s *Service) UpdateTemplate(input TemplateInput) (proxy.Template, map[string]any, error) {
	return s.updateProxyTemplate(input)
}
func (s *Service) Bind(ctx context.Context, authID string, opts BindOptions) (BindingResult, error) {
	return s.bindProxy(ctx, authID, opts)
}
func (s *Service) BindUnbound(ctx context.Context, opts BindOptions) ([]BindingResult, error) {
	return s.proxyBindUnbound(ctx, opts)
}
func (s *Service) Clear(ctx context.Context, authID string, opts BindOptions) (BindingResult, error) {
	return s.clearProxy(ctx, authID, opts)
}
func (s *Service) ClearWithPolicy(ctx context.Context, authID string, opts BindOptions) (BindingResult, error) {
	return s.clearProxyWithPolicy(ctx, authID, opts)
}
func (s *Service) Rotate(ctx context.Context, authID string, opts BindOptions) (BindingResult, error) {
	return s.rotateProxy(ctx, authID, opts)
}
func (s *Service) UpdateAutoBind(params map[string]any) (proxy.AutoBindConfig, bool, error) {
	return s.updateProxyAutoBind(params)
}
func (s *Service) AutoBindRuntime() proxy.AutoBindRuntime { return s.proxyAutoBindRuntime() }
func (s *Service) SetAutoBindForAuths(ids []string, excluded bool) (map[string]any, error) {
	return s.setProxyAutoBindForAuths(ids, excluded)
}
func (s *Service) ApplyNodeToAccounts(ctx context.Context, node proxy.Node, ids []string) []BindingResult {
	return s.proxyApplyNodeToAccounts(ctx, node, ids)
}
func (s *Service) ApplyTemplateToAccounts(ctx context.Context, template proxy.Template, ids []string) []BindingResult {
	return s.proxyApplyTemplateToAccounts(ctx, template, ids)
}

func (s *Service) ActionSummary(ctx context.Context) (any, int, error) {
	return s.actionProxySummary(ctx)
}
func (s *Service) ActionSourcesList(params map[string]any) (any, int, error) {
	return s.actionProxySourcesList(params)
}
func (s *Service) ActionSourceGet(params map[string]any) (any, int, error) {
	return s.actionProxySourceGet(params)
}
func (s *Service) ActionSourceEndpoint(params map[string]any) (any, int, error) {
	return s.actionProxySourceEndpoint(params)
}
func (s *Service) ActionSourceValidate(params map[string]any) (any, int, error) {
	return s.actionProxySourceValidate(params)
}
func (s *Service) ActionSourcePreview(params map[string]any) (any, int, error) {
	return s.actionProxySourcePreview(params)
}
func (s *Service) ActionSourceTest(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxySourceTest(ctx, params)
}
func (s *Service) ActionSourcesTestSelected(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxySourcesTestSelected(ctx, params)
}
func (s *Service) ActionSourcesDeleteSelected(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxySourcesDeleteSelected(ctx, params)
}
func (s *Service) ActionSourceCreate(params map[string]any) (any, int, error) {
	return s.actionProxySourceCreate(params)
}
func (s *Service) ActionImportNodes(params map[string]any) (any, int, error) {
	return s.actionProxyImportNodes(params)
}
func (s *Service) ActionSourceUpdate(params map[string]any) (any, int, error) {
	return s.actionProxySourceUpdate(params)
}
func (s *Service) ActionSourceDelete(params map[string]any) (any, int, error) {
	return s.actionProxySourceDelete(params)
}
func (s *Service) ActionBindingsList(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyBindingsList(ctx, params)
}
func (s *Service) BindingStates(accounts []Account) []BindingState {
	return s.BindingStatesForAccounts(accounts)
}
func (s *Service) TouchBindings(_ []string) { s.TouchBindingStates(nil) }
func (s *Service) ActionBindingEndpoint(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyBindingEndpoint(ctx, params)
}
func (s *Service) ActionBind(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyBind(ctx, params)
}
func (s *Service) ActionClear(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyClear(ctx, params)
}
func (s *Service) ActionRotate(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyRotate(ctx, params)
}
func (s *Service) ActionBatch(action string, params map[string]any) (any, int, error) {
	return s.actionProxyBatch(action, params)
}
func (s *Service) ActionBindingsAutoBind(params map[string]any) (any, int, error) {
	return s.actionProxyBindingsAutoBind(params)
}
func (s *Service) ActionTemplateApply(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyTemplateApply(ctx, params)
}
func (s *Service) ActionBindUnbound(ctx context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyBindUnbound(ctx, params)
}
func (s *Service) ActionAutoBindGet() (any, int, error) { return s.actionProxyAutoBindGet() }
func (s *Service) ActionAutoBindUpdate(params map[string]any) (any, int, error) {
	return s.actionProxyAutoBindUpdate(params)
}
func (s *Service) ActionAutoBindRun() (any, int, error) { return s.actionProxyAutoBindRun() }
func (s *Service) ActionChangesList(params map[string]any) (any, int, error) {
	return s.actionProxyChangesList(params)
}
func (s *Service) ActionChange(params map[string]any, action string) (any, int, error) {
	return s.actionProxyChange(params, action)
}
func (s *Service) ActionResinCleanup(params map[string]any, action string) (any, int, error) {
	return s.actionResinCleanup(params, action)
}

func MaskAccountIdentity(value string) string { return maskAccountIdentity(value) }
func MaskEmail(value string) string           { return maskEmail(value) }
func MaskAuthID(value string) string          { return maskAuthID(value) }
func (s *Service) AccountBindingViewWithHash(state AccountState, value, hash string) (string, string, proxy.SourceKind, bool) {
	return s.proxyAccountBindingViewWithHash(state, value, hash)
}

// SourceInfo resolves the stable display name and kind for an account's proxy
// binding without exposing its raw URL or requiring a second list request.
func (s *Service) SourceInfo(sourceID string) (string, proxy.SourceKind) {
	return s.proxySourceInfo(sourceID)
}

type SourceCatalog = proxySourceCatalog

func (s *Service) Catalog() *SourceCatalog          { return s.proxyCatalog() }
func (s *Service) AutoBindScan(ctx context.Context) { s.proxyAutoBindScan(ctx) }
func (s *Service) DeleteProxyNode(id string, force bool) (map[string]any, error) {
	return s.deleteProxyNode(id, force)
}
func (s *Service) DeleteProxyTemplate(id string, force bool) (map[string]any, error) {
	return s.deleteProxyTemplate(id, force)
}
func (s *Service) SourceDetails(id string) (any, error)      { return s.proxySourceDetails(id) }
func (s *Service) ReconcileOperations(ctx context.Context)   { s.reconcileProxyOperations(ctx) }
func (s *Service) ExistingNode(id string) (proxy.Node, bool) { return s.proxyExistingNode(id) }

func RewriteResinProxyAccount(proxyURL, account string) (string, error) {
	return rewriteResinProxyAccount(proxyURL, account)
}
func DeleteResinLease(ctx context.Context, adminURL, platformID, adminToken, account string) error {
	return deleteResinLease(ctx, adminURL, platformID, adminToken, account)
}
func (s *Service) ProcessDueResinLeaseDeletes(ctx context.Context, now time.Time) int {
	return s.processDueResinLeaseDeletes(ctx, now)
}

type ResinDeleteError = resinDeleteError

func Socks5Authenticate(conn net.Conn, username, password string) error {
	return socks5Authenticate(conn, username, password)
}
func ResinLeaseDeleteBackoff(attempts int) time.Duration { return resinLeaseDeleteBackoff(attempts) }
