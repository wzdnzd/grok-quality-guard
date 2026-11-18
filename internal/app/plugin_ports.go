package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	appauth "grok-quality-guard/internal/app/auth"
	execapp "grok-quality-guard/internal/app/executor"
	"grok-quality-guard/internal/app/model"
	probeapp "grok-quality-guard/internal/app/probe"
	proxyapp "grok-quality-guard/internal/app/proxy"
	qualityapp "grok-quality-guard/internal/app/quality"
	appredact "grok-quality-guard/internal/app/redact"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
	appconfig "grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
	qualitypkg "grok-quality-guard/internal/quality"
)

// Plugin implements the Environment ports declared by the application
// subpackages. These methods are the composition-root boundary: they translate
// subpackage requirements into Plugin lifecycle, state, host, and service
// calls. They intentionally stay together here instead of becoming one
// *_adapter.go file per subpackage, which would turn package structure into a
// file-naming convention without adding a maintainable boundary.

func (p *Plugin) Store() *appstate.Store { return p.stateStore() }

func (p *Plugin) ListAuths() ([]pluginapi.HostAuthFileEntry, error) {
	return p.listAuths()
}

func (p *Plugin) ListAuthModels() ([]pluginapi.HostAuthModels, error) {
	return p.listAuthModels()
}

func (p *Plugin) GetAuth(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	return p.getAuth(authIndex)
}

func (p *Plugin) SelectionReady() bool { return p.routingSvc.IndexReady() }

func (p *Plugin) RebuildSelectionIndex() error { return p.routingSvc.RebuildIndex() }

func (p *Plugin) WakeProxyAutoBind() { p.proxySvc.WakeAutoBind() }

func (p *Plugin) WakeResinLeaseDeleteLoop() { p.proxySvc.WakeResinLeaseDeleteLoop() }

func (p *Plugin) RefreshCatalog(ctx context.Context) error {
	return p.catalogSvc.RefreshCatalog(ctx)
}

func (p *Plugin) EnsureCatalog(ctx context.Context) error { return p.catalogSvc.Ensure(ctx) }

func (p *Plugin) CatalogSnapshot() []model.Account {
	if p == nil || p.catalogSvc == nil {
		return nil
	}
	return p.catalogSvc.Snapshot()
}

func (p *Plugin) FindAccount(authID string) (model.Account, bool) {
	return p.catalogSvc.FindAccount(authID)
}

func (p *Plugin) ResolveAuth(ctx context.Context, authID string) (model.Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error) {
	return p.catalogSvc.ResolveAuth(ctx, authID)
}

func (p *Plugin) AccountLock(authID string) *sync.Mutex { return p.accountLock(authID) }

func (p *Plugin) StateAccount(authID string) (appstate.AccountState, bool) {
	return p.stateAccount(authID)
}

func (p *Plugin) UpdateStateAccount(authID string, fn func(*appstate.AccountState) error) error {
	return p.updateStateAccount(authID, fn)
}

func (p *Plugin) RefreshSelectionAccount(authID string) {
	p.routingSvc.RefreshAccount(authID)
}

func (p *Plugin) Policy() appconfig.Policy {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.policy
}

func (p *Plugin) SaveAuth(name string, raw json.RawMessage) (pluginapi.HostAuthSaveResponse, error) {
	return p.saveAuth(name, raw)
}

func (p *Plugin) SaveAuthDisabled(name string, raw map[string]json.RawMessage, disabled bool) error {
	appauth.SetRawBool(raw, "disabled", disabled)
	encoded, errMarshal := marshalRawMap(raw)
	if errMarshal != nil {
		return errMarshal
	}
	_, errSave := p.saveAuth(name, encoded)
	return errSave
}

func (p *Plugin) RecordInternalError(authID string, errValue error) error {
	store := p.stateStore()
	if store == nil {
		return errValue
	}
	_ = store.AppendEvent(appstate.Event{
		Time:   time.Now().UTC(),
		AuthID: authID,
		Action: "internal_error",
		Result: "error",
		Detail: appredact.SanitizeError(errValue),
	})
	return nil
}

func (p *Plugin) ApplyVerdict(ctx context.Context, authID string, verdict qualitypkg.QualityVerdict, source, callbackID string) error {
	return p.qualitySvc.ApplyVerdict(ctx, authID, verdict, source, callbackID)
}

func (p *Plugin) Reconcile(ctx context.Context) error { return p.qualitySvc.Reconcile(ctx) }

func (p *Plugin) ProbeAccount(ctx context.Context, authID string, force bool, callbackID string) error {
	return p.probeSvc.ProbeAccount(ctx, authID, force, callbackID)
}

func (p *Plugin) AccountView(ctx context.Context, account model.Account) (any, error) {
	return p.managementSvc.AccountView(ctx, account)
}

func (p *Plugin) StartTask(kind string, authIDs []string, progress string, fn func(context.Context) (any, error)) tasks.Task {
	return p.startTask(kind, authIDs, progress, fn)
}

func (p *Plugin) HasActiveTask(kind, authID string) bool {
	return p.hasActiveTask(kind, authID)
}

func (p *Plugin) TaskByID(taskID string) (tasks.Task, bool) { return p.taskByID(taskID) }

func (p *Plugin) CancelTask(taskID string) (tasks.Task, error) { return p.cancelTask(taskID) }

func (p *Plugin) DeleteTask(taskID string) error { return p.deleteTask(taskID) }

func (p *Plugin) ListTasks(limit int) []tasks.Task { return p.listTasks(limit) }

func (p *Plugin) TaskCenter(filter tasks.TaskCenterFilter) (tasks.TaskCenterResult, error) {
	return p.taskCenter(filter)
}

func (p *Plugin) TaskDetail(taskID string, page, pageSize int, status string) (tasks.TaskDetail, error) {
	return p.taskDetail(taskID, page, pageSize, status)
}

func (p *Plugin) AccountProxyURL(ctx context.Context, account model.Account, allowFallback bool) string {
	proxyURL := strings.TrimSpace(account.ProxyURL)
	if proxyURL != "" || strings.TrimSpace(account.Path) != "" || !allowFallback {
		return proxyURL
	}
	_, _, raw, errResolve := p.catalogSvc.ResolveAuth(ctx, account.ID)
	if errResolve != nil {
		return ""
	}
	return strings.TrimSpace(appauth.RawString(raw, "proxy_url"))
}

func (p *Plugin) IsGrokRouteModel(modelName string) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	prefixes := append([]string(nil), p.policy.ModelPrefixes...)
	p.mu.RUnlock()
	return p.catalogSvc.IsGrokModel(modelName, prefixes)
}

func (p *Plugin) IsProbeAuth(authID string) bool { return p.probeSvc.IsProbeAuth(authID) }

func (p *Plugin) ProxyStore() *appstate.Store { return p.stateStore() }

func (p *Plugin) ProxyCatalogSnapshot() []model.Account {
	if p == nil || p.catalogSvc == nil {
		return nil
	}
	return p.catalogSvc.Snapshot()
}

func (p *Plugin) ProxyEnsureCatalog(ctx context.Context) error {
	return p.catalogSvc.Ensure(ctx)
}

func (p *Plugin) ProxyFindAccount(authID string) (model.Account, bool) {
	return p.catalogSvc.FindAccount(authID)
}

func (p *Plugin) ProxyResolveAuth(ctx context.Context, authID string) (model.Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error) {
	return p.catalogSvc.ResolveAuth(ctx, authID)
}

func (p *Plugin) ProxySaveAuth(name string, raw json.RawMessage) (pluginapi.HostAuthSaveResponse, error) {
	return p.saveAuth(name, raw)
}

func (p *Plugin) ProxyAccountLock(authID string) *sync.Mutex { return p.accountLock(authID) }

func (p *Plugin) ProxyStateAccount(authID string) (appstate.AccountState, bool) {
	return p.stateAccount(authID)
}

func (p *Plugin) ProxyUpdateStateAccount(authID string, fn func(*appstate.AccountState) error) error {
	return p.updateStateAccount(authID, fn)
}

func (p *Plugin) ProxyRecordEvent(target *appstate.State, authID, class, action, result, detail string) {
	p.recordEvent(target, authID, class, action, result, detail)
}

func (p *Plugin) ProxyStartTask(kind string, authIDs []string, progress string, fn func(context.Context) (any, error)) tasks.Task {
	return p.startTask(kind, authIDs, progress, fn)
}

func (p *Plugin) ProxyHasActivePrefix(prefixes ...string) bool {
	return p != nil && p.taskMgr != nil && p.taskMgr.HasActivePrefix(prefixes...)
}

func (p *Plugin) ProxyActiveTasks(prefixes ...string) []tasks.ActiveTask {
	if p == nil || p.taskMgr == nil {
		return nil
	}
	return p.taskMgr.Active(prefixes...)
}

func (p *Plugin) ProxyConfigSnapshot() (appconfig.ProxyConfig, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.Proxy, p.policy.ProxyAutoRotateAfterQualityFail
}

func (p *Plugin) ProxyApplyConfig(proxyCfg appconfig.ProxyConfig, rotate bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Proxy = proxyCfg
	p.policy.ProxyAutoRotateAfterQualityFail = rotate
}

func (p *Plugin) PluginID() string { return pluginID }

func (p *Plugin) RequestContext() (context.Context, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.requestCtx, p.quiescing
}

func (p *Plugin) TaskStarted() {
	p.mu.RLock()
	p.taskWG.Add(1)
	p.mu.RUnlock()
}

func (p *Plugin) TaskDone() { p.taskWG.Done() }

func (p *Plugin) PrepareRequest(req execapp.Request) (execapp.Facts, error) {
	return p.routingSvc.PrepareRequest(req)
}

func (p *Plugin) NewSelection(req execapp.Request, facts execapp.Facts) (execapp.Selection, error) {
	return p.routingSvc.NewSelection(req, facts)
}

func (p *Plugin) ExecutableAuth(ctx context.Context, authID string) (model.Account, map[string]json.RawMessage, bool) {
	account, _, raw, errResolve := p.catalogSvc.ResolveAuth(ctx, authID)
	if errResolve != nil || !appauth.IsGrokAuth(raw) || appauth.Disabled(raw) {
		return model.Account{}, nil, false
	}
	if errValidate := validateAuthPayload(raw); errValidate != nil {
		return model.Account{}, nil, false
	}
	return account, raw, true
}

func (p *Plugin) ExecuteModel(ctx context.Context, req pluginapi.HostModelExecutionRequest, callbackID string) (pluginapi.HostModelExecutionResponse, error) {
	return p.executeModel(ctx, req, callbackID)
}

func (p *Plugin) ExecuteModelStream(ctx context.Context, req pluginapi.HostModelExecutionRequest, callbackID string) (pluginapi.HostModelStreamResponse, error) {
	return p.executeModelStream(ctx, req, callbackID)
}

func (p *Plugin) ReadModelStream(ctx context.Context, streamID string) (pluginapi.HostModelStreamReadResponse, error) {
	return p.readModelStream(ctx, streamID)
}

func (p *Plugin) CloseModelStream(streamID string) error { return p.closeModelStream(streamID) }

func (p *Plugin) ReadModelStreamErrorBody(ctx context.Context, streamID string) []byte {
	return p.executorSvc.ReadModelStreamErrorBody(ctx, streamID)
}

func (p *Plugin) EmitPluginStreamChunk(streamID string, payload []byte) error {
	_, errCall := hostCall(pluginabi.MethodHostStreamEmit, map[string]any{
		"stream_id": streamID,
		"payload":   append([]byte(nil), payload...),
	})
	return errCall
}

func (p *Plugin) ClosePluginStream(streamID, code, message string, status int) {
	if streamID == "" {
		return
	}
	request := map[string]any{"stream_id": streamID}
	if message != "" || status != 0 {
		request["error_detail"] = pluginabi.Error{Code: code, Message: message, HTTPStatus: status}
	}
	_, _ = hostCall(pluginabi.MethodHostStreamClose, request)
}

func (p *Plugin) ProxyBindingForAccount(account appstate.AccountState) (proxy.Binding, bool) {
	return p.proxySvc.BindingForAccount(account)
}

func (p *Plugin) RotateProxy(ctx context.Context, authID, sourceScope string) (string, error) {
	result, errRotate := p.proxySvc.Rotate(ctx, authID, proxyapp.BindOptions{SourceScope: sourceScope})
	return result.Status, errRotate
}

func (p *Plugin) AutoRotateAfterQualityFailure() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.Proxy.AutoRotateAfterQualityFailure
}

func (p *Plugin) HasAvailableXAIAuth(providers []string) bool {
	if len(providers) > 0 {
		for _, provider := range providers {
			if appauth.IsGrokProvider(provider) {
				return true
			}
		}
		return false
	}
	return p.routingSvc.IndexTotal() > 0
}

func (p *Plugin) ConfigEnabled() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.Enabled
}

func (p *Plugin) YAMLPolicy() (appconfig.Policy, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.yamlPolicy, p.yamlRevision
}

func (p *Plugin) SetPolicy(policy appconfig.Policy) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.policy = policy
	p.cfg.TransparentGuard = policy.TransparentGuard
	p.cfg.PassiveGuard = policy.PassiveGuard
	p.cfg.AutoAction = policy.AutoAction
	p.cfg.ModelPrefixes = append([]string(nil), policy.ModelPrefixes...)
	p.cfg.Routing = policy.Routing
	p.cfg.Quality = policy.Quality
	p.cfg.Probe = policy.Probe
	p.cfg.Proxy.AutoRotateAfterQualityFailure = policy.ProxyAutoRotateAfterQualityFail
	p.cfg.TaskHistory = policy.TaskHistory
	p.cfg.EventsLimit = policy.EventsLimit
	if p.store != nil {
		_ = p.taskMgr.ConfigureHistory(filepath.Dir(p.store.Path()), tasks.HistoryConfig{Enabled: policy.TaskHistory.Enabled, RetentionDays: policy.TaskHistory.RetentionDays, MaxRecords: policy.TaskHistory.MaxRecords, MaxBytes: policy.TaskHistory.MaxBytes, MaxItemsPerTask: policy.TaskHistory.MaxItemsPerTask})
	}
}

func (p *Plugin) WakeProbeWaiters()                          { p.probeSvc.WakeWaiters() }
func (p *Plugin) ProbeQueueSnapshot() probeapp.QueueSnapshot { return p.probeSvc.QueueSnapshot() }
func (p *Plugin) ProbeQueueSize() int                        { return p.probeSvc.ProbeQueueSize() }
func (p *Plugin) ProbeConcurrency() int {
	return p.probeSvc.ProbeConcurrency()
}

func (p *Plugin) Proxy() *proxyapp.Service { return p.proxySvc }

func (p *Plugin) Quality() *qualityapp.Service { return p.qualitySvc }

func validateAuthPayload(raw map[string]json.RawMessage) error {
	if raw == nil {
		return errors.New("auth payload is empty")
	}
	if appauth.RawString(raw, "access_token") == "" && appauth.RawString(raw, "refresh_token") == "" {
		return errors.New("auth payload has no access_token or refresh_token")
	}
	return nil
}

func (p *Plugin) startTask(kind string, authIDs []string, progress string, fn func(context.Context) (any, error)) tasks.Task {
	if p == nil || p.taskMgr == nil {
		return tasks.Task{Kind: kind, Status: taskStatusFailed, Error: "task manager is not initialized"}
	}
	return p.taskMgr.Start(kind, authIDs, progress, fn)
}

func (p *Plugin) hasActiveTask(kind, authID string) bool {
	return p != nil && p.taskMgr != nil && p.taskMgr.HasActive(kind, authID)
}

func (p *Plugin) taskByID(taskID string) (tasks.Task, bool) {
	if p == nil || p.taskMgr == nil {
		return tasks.Task{}, false
	}
	return p.taskMgr.ByID(taskID)
}

func (p *Plugin) cancelTask(taskID string) (tasks.Task, error) {
	if p == nil || p.taskMgr == nil {
		return tasks.Task{}, tasks.ErrTaskNotFound
	}
	return p.taskMgr.CancelTask(taskID)
}

func (p *Plugin) deleteTask(taskID string) error {
	if p == nil || p.taskMgr == nil {
		return tasks.ErrTaskNotFound
	}
	return p.taskMgr.DeleteTask(taskID)
}

func (p *Plugin) taskCenter(filter tasks.TaskCenterFilter) (tasks.TaskCenterResult, error) {
	if p == nil || p.taskMgr == nil {
		return tasks.TaskCenterResult{}, nil
	}
	return p.taskMgr.ListCenter(filter)
}

func (p *Plugin) taskDetail(taskID string, page, pageSize int, status string) (tasks.TaskDetail, error) {
	if p == nil || p.taskMgr == nil {
		return tasks.TaskDetail{}, tasks.ErrTaskNotFound
	}
	return p.taskMgr.TaskDetail(taskID, page, pageSize, status)
}

func (p *Plugin) listTasks(limit int) []tasks.Task {
	if p == nil || p.taskMgr == nil {
		return nil
	}
	return p.taskMgr.List(limit)
}

func (p *Plugin) cancelTasks() {
	if p != nil && p.taskMgr != nil {
		p.taskMgr.Cancel()
	}
}

const (
	taskStatusSucceeded = "succeeded"
	taskStatusFailed    = "failed"
)
