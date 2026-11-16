package management

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/abi"
	"grok-quality-guard/internal/app/auth"
	proxyapp "grok-quality-guard/internal/app/proxy"
	"grok-quality-guard/internal/app/redact"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
	"grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
	domain "grok-quality-guard/internal/quality"
)

type ResinBindResult = proxyapp.ResinBindResult
type ResinClearResult = proxyapp.ResinClearResult
type ResinConfigView = proxyapp.ResinConfigView

const (
	StateUnknown            = appstate.StateUnknown
	StateHealthy            = appstate.StateHealthy
	StateSuspect            = appstate.StateSuspect
	StateDegraded           = appstate.StateDegraded
	StateCooling            = appstate.StateCooling
	StateQuotaExhausted     = appstate.StateQuotaExhausted
	StateCredentialInvalid  = appstate.StateCredentialInvalid
	StateDisabledByGuard    = appstate.StateDisabledByGuard
	StateExternallyDisabled = appstate.StateExternallyDisabled
)

func okEnvelope(value any) ([]byte, error) { return abi.OK(value) }
func sanitizeError(err error) string       { return redact.SanitizeError(err) }
func asString(value any) string            { return domain.AsString(value) }
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func authRawString(raw map[string]json.RawMessage, key string) string {
	return auth.RawString(raw, key)
}
func maskAccountIdentity(value string) string { return proxyapp.MaskAccountIdentity(value) }
func maskEmail(value string) string           { return proxyapp.MaskEmail(value) }
func maskAuthID(value string) string          { return proxyapp.MaskAuthID(value) }
func maskProxyURL(value string) string        { return proxyapp.MaskProxyURL(value) }
func managedResinBindingForState(proxyURL string, resinState appstate.ResinState, cfg ResinSettings) (string, bool) {
	return proxyapp.ManagedResinBindingForState(proxyURL, resinState, cfg)
}
func jsonError(code, message string) []byte {
	raw, _ := json.Marshal(map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}})
	return raw
}
func jsonOK(data any) []byte {
	raw, _ := json.Marshal(map[string]any{"ok": true, "data": data})
	return raw
}
func runBoundedQueue[T any](ctx context.Context, values []T, queueSize, workers int, fn func(context.Context, T)) error {
	return tasks.RunBoundedQueue(ctx, values, queueSize, workers, fn)
}

func (s *Service) stateStore() *appstate.Store               { return s.env.Store() }
func (s *Service) ensureCatalog(ctx context.Context) error   { return s.env.EnsureCatalog(ctx) }
func (s *Service) RefreshCatalog(ctx context.Context) error  { return s.env.RefreshCatalog(ctx) }
func (s *Service) catalogSnapshot() []Account                { return s.env.CatalogSnapshot() }
func (s *Service) findAccount(authID string) (Account, bool) { return s.env.FindAccount(authID) }
func (s *Service) resolveAuth(ctx context.Context, authID string) (Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error) {
	return s.env.ResolveAuth(ctx, authID)
}
func (s *Service) stateAccount(authID string) (AccountState, bool) { return s.env.StateAccount(authID) }
func (s *Service) startTask(kind string, authIDs []string, progress string, fn func(context.Context) (any, error)) Task {
	return s.env.StartTask(kind, authIDs, progress, fn)
}
func (s *Service) hasActiveTask(kind, authID string) bool { return s.env.HasActiveTask(kind, authID) }
func (s *Service) taskByID(taskID string) (Task, bool)    { return s.env.TaskByID(taskID) }
func (s *Service) listTasks(limit int) []Task             { return s.env.ListTasks(limit) }
func (s *Service) probeQueueSize() int                    { return s.env.ProbeQueueSize() }
func (s *Service) probeConcurrency() int                  { return s.env.ProbeConcurrency() }
func (s *Service) ProbeAccount(ctx context.Context, authID string, force bool, callbackID string) error {
	return s.env.ProbeAccount(ctx, authID, force, callbackID)
}
func (s *Service) ManualCooldown(ctx context.Context, authID string, duration time.Duration) error {
	return s.env.Quality().ManualCooldown(ctx, authID, duration)
}
func (s *Service) ManualDisable(ctx context.Context, authID, reason string) error {
	return s.env.Quality().ManualDisable(ctx, authID, reason)
}
func (s *Service) ManualRestore(ctx context.Context, authID string) error {
	return s.env.Quality().ManualRestore(ctx, authID)
}
func (s *Service) ResinConfig() ResinSettings { return s.env.Proxy().ResinConfig() }
func (s *Service) BindResin(ctx context.Context, authID string, dryRun bool) (ResinBindResult, error) {
	return s.env.Proxy().BindResin(ctx, authID, dryRun)
}
func (s *Service) BindUnboundResin(ctx context.Context, dryRun bool) ([]ResinBindResult, error) {
	return s.env.Proxy().BindUnboundResin(ctx, dryRun)
}
func (s *Service) ClearResinProxy(ctx context.Context, authID string) (ResinClearResult, error) {
	return s.env.Proxy().ClearResinProxy(ctx, authID)
}
func (s *Service) RotateResin(ctx context.Context, authID string) error {
	return s.env.Proxy().RotateResin(ctx, authID)
}
func (s *Service) proxyBindingForAccount(account AccountState) (proxy.Binding, bool) {
	return s.env.Proxy().BindingForAccount(account)
}
func (s *Service) proxyAccountBindingView(account AccountState, value string) (string, string, proxy.SourceKind, bool) {
	return s.env.Proxy().AccountBindingView(account, value)
}

func (s *Service) actionProxySummary(ctx context.Context) (any, int, error) {
	return s.env.Proxy().ActionSummary(ctx)
}
func (s *Service) actionProxySourcesList(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionSourcesList(params)
}
func (s *Service) actionProxySourceGet(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionSourceGet(params)
}
func (s *Service) actionProxySourceValidate(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionSourceValidate(params)
}
func (s *Service) actionProxySourcePreview(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionSourcePreview(params)
}
func (s *Service) actionProxySourceTest(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionSourceTest(ctx, params)
}
func (s *Service) actionProxySourceCreate(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionSourceCreate(params)
}
func (s *Service) actionProxyImportNodes(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionImportNodes(params)
}
func (s *Service) actionProxySourceUpdate(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionSourceUpdate(params)
}
func (s *Service) actionProxySourceDelete(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionSourceDelete(params)
}
func (s *Service) actionProxyBindingsList(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionBindingsList(ctx, params)
}
func (s *Service) actionProxyBindingEndpoint(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionBindingEndpoint(ctx, params)
}
func (s *Service) actionProxyBind(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionBind(ctx, params)
}
func (s *Service) actionProxyClear(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionClear(ctx, params)
}
func (s *Service) actionProxyRotate(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionRotate(ctx, params)
}
func (s *Service) actionProxyBatch(action string, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionBatch(action, params)
}
func (s *Service) actionProxyBindingsAutoBind(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionBindingsAutoBind(params)
}
func (s *Service) actionProxyTemplateApply(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionTemplateApply(ctx, params)
}
func (s *Service) actionProxyBindUnbound(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionBindUnbound(ctx, params)
}
func (s *Service) actionProxyAutoBindGet() (any, int, error) {
	return s.env.Proxy().ActionAutoBindGet()
}
func (s *Service) actionProxyAutoBindUpdate(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionAutoBindUpdate(params)
}
func (s *Service) actionProxyAutoBindRun() (any, int, error) {
	return s.env.Proxy().ActionAutoBindRun()
}
func (s *Service) actionProxyChangesList(params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionChangesList(params)
}
func (s *Service) actionProxyChange(params map[string]any, action string) (any, int, error) {
	return s.env.Proxy().ActionChange(params, action)
}
func (s *Service) actionResinCleanup(params map[string]any, action string) (any, int, error) {
	return s.env.Proxy().ActionResinCleanup(params, action)
}
func (s *Service) actionResinConfigGet() (any, int, error) {
	return s.env.Proxy().ActionResinConfigGet()
}
func (s *Service) actionResinConfigValidate(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionResinConfigValidate(ctx, params)
}
func (s *Service) actionResinConfigSave(ctx context.Context, params map[string]any) (any, int, error) {
	return s.env.Proxy().ActionResinConfigSave(ctx, params)
}

func normalizePolicy(policy Policy) (Policy, error) { return config.NormalizePolicy(policy) }
