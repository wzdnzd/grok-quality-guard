package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	catalogapp "grok-quality-guard/internal/app/catalog"
	execapp "grok-quality-guard/internal/app/executor"
	managementapp "grok-quality-guard/internal/app/management"
	passiveapp "grok-quality-guard/internal/app/passive"
	probeapp "grok-quality-guard/internal/app/probe"
	proxyapp "grok-quality-guard/internal/app/proxy"
	appquality "grok-quality-guard/internal/app/quality"
	routingapp "grok-quality-guard/internal/app/routing"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
	appconfig "grok-quality-guard/internal/config"
)

var (
	_ catalogapp.Environment    = (*Plugin)(nil)
	_ execapp.Environment       = (*Plugin)(nil)
	_ managementapp.Environment = (*Plugin)(nil)
	_ passiveapp.Environment    = (*Plugin)(nil)
	_ probeapp.Environment      = (*Plugin)(nil)
	_ proxyapp.Environment      = (*Plugin)(nil)
	_ appquality.Environment    = (*Plugin)(nil)
	_ routingapp.Environment    = (*Plugin)(nil)
)

type Plugin struct {
	mu                 sync.RWMutex
	cfg                appconfig.Config
	policy             appconfig.Policy
	yamlPolicy         appconfig.Policy
	yamlRevision       string
	store              *appstate.Store
	stateBootstrapPath string
	ctx                context.Context
	cancel             context.CancelFunc
	requestCtx         context.Context
	requestCancel      context.CancelFunc
	wg                 sync.WaitGroup
	workersActive      bool
	quiescing          bool

	accountLocksMu sync.Mutex
	accountLocks   map[string]*sync.Mutex

	modelStreamsMu sync.Mutex
	modelStreams   map[string]struct{}

	taskWG  sync.WaitGroup
	taskMgr *tasks.Manager

	passiveSvc    *passiveapp.Service
	proxySvc      *proxyapp.Service
	qualitySvc    *appquality.Service
	executorSvc   *execapp.Service
	managementSvc *managementapp.Service
	probeSvc      *probeapp.Service
	catalogSvc    *catalogapp.Service
	routingSvc    *routingapp.Service
}

func NewPlugin() *Plugin {
	requestCtx, requestCancel := context.WithCancel(context.Background())
	plugin := &Plugin{
		cfg:          appconfig.DefaultConfig(),
		policy:       appconfig.PolicyFromConfig(appconfig.DefaultConfig()),
		accountLocks: make(map[string]*sync.Mutex),
		modelStreams: make(map[string]struct{}),
		taskMgr:      tasks.NewManager(),

		requestCtx:    requestCtx,
		requestCancel: requestCancel,
	}
	plugin.proxySvc = proxyapp.NewService(plugin)
	plugin.qualitySvc = appquality.NewService(plugin)
	plugin.passiveSvc = passiveapp.NewService(plugin)
	plugin.executorSvc = execapp.NewService(plugin)
	plugin.managementSvc = managementapp.NewService(plugin)
	plugin.probeSvc = probeapp.NewService(plugin)
	plugin.catalogSvc = catalogapp.NewService(plugin)
	plugin.routingSvc = routingapp.NewService(plugin)
	return plugin
}

func (p *Plugin) stateStore() *appstate.Store {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.store
}

func (p *Plugin) Configure(raw []byte) error {
	cfg, errDecode := appconfig.Decode(raw)
	if errDecode != nil {
		return errDecode
	}
	revision := appconfig.Revision(raw)
	bootstrapPath := appstate.ConfiguredPath(cfg.StateFile)
	activePath := bootstrapPath
	if override, errPointer := appstate.ReadPathPointer(bootstrapPath); errPointer != nil {
		return errPointer
	} else if strings.TrimSpace(override) != "" {
		normalized, errNormalize := appstate.NormalizePath(override)
		if errNormalize != nil {
			return fmt.Errorf("state path pointer is invalid: %w", errNormalize)
		}
		activePath = normalized
	}
	p.mu.Lock()
	oldPath := ""
	if p.store != nil {
		oldPath = p.store.Path()
	}
	if p.store == nil || oldPath != activePath || p.stateBootstrapPath != bootstrapPath {
		if p.store != nil {
			_ = p.store.Flush()
		}
		p.store = appstate.NewStore(activePath)
		if errLoad := p.store.Load(appconfig.PolicyFromConfig(cfg), revision); errLoad != nil {
			p.mu.Unlock()
			return errLoad
		}
	}
	p.stateBootstrapPath = bootstrapPath
	if errProxyDefaults := p.proxySvc.EnsureStateDefaults(p.store, cfg.Proxy.AutoBind); errProxyDefaults != nil {
		p.mu.Unlock()
		return errProxyDefaults
	}
	cfg.StateFile = activePath
	p.cfg = cfg
	p.yamlPolicy = appconfig.PolicyFromConfig(cfg)
	p.yamlRevision = revision
	statePolicy := p.store.PolicySnapshot()
	if statePolicy.PassiveGuard || statePolicy.EventsLimit > 0 || statePolicy.AutoAction != "" {
		p.policy = statePolicy
	} else {
		p.policy = p.yamlPolicy
		if errUpdate := p.store.Update(func(s *appstate.State) error {
			s.Policy = p.policy
			s.PolicyYAMLRevision = revision
			return nil
		}); errUpdate != nil {
			p.mu.Unlock()
			return errUpdate
		}
	}
	if errApply := appconfig.ApplyPolicy(&p.cfg, p.policy); errApply != nil {
		p.mu.Unlock()
		return errApply
	}
	_ = p.taskMgr.ConfigureHistory(filepath.Dir(activePath), tasks.HistoryConfig{Enabled: p.cfg.TaskHistory.Enabled, RetentionDays: p.cfg.TaskHistory.RetentionDays, MaxRecords: p.cfg.TaskHistory.MaxRecords, MaxBytes: p.cfg.TaskHistory.MaxBytes, MaxItemsPerTask: p.cfg.TaskHistory.MaxItemsPerTask})
	if p.requestCtx == nil || p.requestCtx.Err() != nil {
		p.requestCtx, p.requestCancel = context.WithCancel(context.Background())
	}
	p.quiescing = false
	if p.cfg.Enabled {
		p.startWorkersLocked()
	} else {
		p.stopWorkersLocked()
	}
	p.mu.Unlock()
	return nil
}

// UpdateStatePath moves the live state file and records a durable pointer next
// to the YAML/bootstrap path so the new location survives plugin restarts.
func (p *Plugin) UpdateStatePath(value string) (string, error) {
	target, errNormalize := appstate.NormalizePath(value)
	if errNormalize != nil {
		return "", errNormalize
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	store := p.store
	if store == nil {
		return "", errors.New("state store is not initialized")
	}
	oldPath := store.Path()
	if normalizedOldPath, errOld := appstate.NormalizePath(oldPath); errOld == nil && normalizedOldPath == target {
		p.cfg.StateFile = target
		return target, nil
	}
	bootstrapPath := strings.TrimSpace(p.stateBootstrapPath)
	if bootstrapPath == "" {
		bootstrapPath = oldPath
	}
	if errSwitch := store.SetPath(target); errSwitch != nil {
		return "", errSwitch
	}
	if errPointer := appstate.WritePathPointer(bootstrapPath, target); errPointer != nil {
		if errRestore := store.SetPathUnchecked(oldPath, true); errRestore != nil {
			return "", errors.Join(errPointer, fmt.Errorf("restore previous state path: %w", errRestore))
		}
		return "", errPointer
	}
	p.cfg.StateFile = target
	_ = p.taskMgr.ConfigureHistory(filepath.Dir(target), tasks.HistoryConfig{Enabled: p.cfg.TaskHistory.Enabled, RetentionDays: p.cfg.TaskHistory.RetentionDays, MaxRecords: p.cfg.TaskHistory.MaxRecords, MaxBytes: p.cfg.TaskHistory.MaxBytes, MaxItemsPerTask: p.cfg.TaskHistory.MaxItemsPerTask})
	return target, nil
}

func (p *Plugin) startWorkersLocked() {
	if p.workersActive || !p.cfg.Enabled {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.ctx = ctx
	p.cancel = cancel
	p.workersActive = true
	p.wg.Add(4)
	go func() {
		defer p.wg.Done()
		p.catalogSvc.RunRefreshLoop(ctx)
	}()
	go func() {
		defer p.wg.Done()
		p.probeSvc.RunLoop(ctx)
	}()
	go func() {
		defer p.wg.Done()
		p.proxySvc.RunResinLeaseDeleteLoop(ctx)
	}()
	go func() {
		defer p.wg.Done()
		p.proxySvc.RunAutoBindLoop(ctx)
	}()
}

func (p *Plugin) stopWorkersLocked() {
	if p.cancel != nil {
		p.cancel()
	}
	p.cancel = nil
	p.ctx = nil
	p.workersActive = false
}

func (p *Plugin) Quiesce() {
	p.mu.Lock()
	p.quiescing = true
	p.stopWorkersLocked()
	if p.requestCancel != nil {
		p.requestCancel()
	}
	p.mu.Unlock()
	p.cancelTasks()
	p.closeAllModelStreams()
}

func (p *Plugin) Shutdown() {
	p.mu.Lock()
	p.quiescing = true
	p.stopWorkersLocked()
	if p.requestCancel != nil {
		p.requestCancel()
	}
	p.mu.Unlock()
	p.cancelTasks()
	p.closeAllModelStreams()
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		p.taskWG.Wait()
		p.taskMgr.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	if store := p.stateStore(); store != nil {
		_ = store.Flush()
	}
}
