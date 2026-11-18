package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"grok-quality-guard/internal/app/redact"
	"grok-quality-guard/internal/app/tasks"
	appconfig "grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
)

func proxyAccountAllowedAuto(account Account, state, disableKind string, config proxy.AutoBindConfig) bool {
	if !account.Disabled && disableKind == "" && state != StateExternallyDisabled {
		return true
	}
	if disableKind != "" {
		return config.IncludeGuardDisabled
	}
	return config.IncludeExternallyDisabled
}

func (s *Service) updateProxyAutoBind(params map[string]any) (proxy.AutoBindConfig, bool, error) {
	config := s.env.ProxyStore().ProxyAutoBindConfig()
	encoded, err := json.Marshal(params)
	if err != nil {
		return config, false, err
	}
	if err = json.Unmarshal(encoded, &config); err != nil {
		return config, false, proxyError("auto_bind_config_invalid", 400, err.Error())
	}
	settings := appconfig.ProxyConfig{AutoBind: config}
	if err = appconfig.NormalizeProxyConfig(&settings); err != nil {
		return config, false, proxyError("auto_bind_config_invalid", 400, err.Error())
	}
	config = settings.AutoBind
	for _, id := range config.SourceIDs {
		entry := s.proxyCatalog().ByID[id]
		if entry == nil {
			return config, false, proxyError("proxy_source_not_found", 400, "unknown source: "+id)
		}
		if !proxyScopeContains(config.SourceScope, entry.Kind) {
			return config, false, proxyError("auto_bind_scope_invalid", 400, "selected source is outside source_scope: "+id)
		}
	}
	proxyCfg, rotate := s.env.ProxyConfigSnapshot()
	if _, ok := params["auto_rotate_after_quality_failure"]; ok {
		rotate = optionalBoolParam(params, "auto_rotate_after_quality_failure", rotate)
	}
	err = s.env.ProxyStore().Update(func(state *State) error {
		state.Proxy.AutoBind = config
		state.Policy.ProxyAutoRotateAfterQualityFail = rotate
		return nil
	})
	if err != nil {
		return config, rotate, err
	}
	proxyCfg.AutoBind = config
	s.env.ProxyApplyConfig(proxyCfg, rotate)
	if !config.Enabled {
		s.dropProxyAutoQueue()
	}
	s.wakeProxyAutoBind()
	return config, rotate, nil
}

func (s *Service) proxyAutoBindRuntime() proxy.AutoBindRuntime {
	s.mgr.mu.Lock()
	runtime := s.mgr.runtime
	runtime.QueueDepth = len(s.mgr.queue) - s.mgr.queueHead
	runtime.InFlight = len(s.mgr.inFlight)
	s.mgr.mu.Unlock()
	runtime.Enabled = s.env.ProxyStore().ProxyAutoBindConfig().Enabled
	return runtime
}

func (s *Service) dropProxyAutoQueue() {
	s.mgr.mu.Lock()
	defer s.mgr.mu.Unlock()
	for _, id := range s.mgr.queue[s.mgr.queueHead:] {
		if _, running := s.mgr.inFlight[id]; !running {
			delete(s.mgr.queued, id)
		}
	}
	s.mgr.queue = nil
	s.mgr.queueHead = 0
	s.mgr.runtime.Running = false
	s.mgr.runtime.WaitingReason = "disabled"
}

func (s *Service) proxyAutoBindLoop(ctx context.Context) {
	recovered := s.env.ProxyStore().TakeRecoveredProxyChanges()
	healthCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.proxyHealthLoop(healthCtx) }()
	defer func() { cancel(); wg.Wait() }()
	s.reconcileProxyOperations(ctx)
	for _, id := range recovered {
		if ctx.Err() != nil {
			return
		}
		_, _ = s.runProxyChange(ctx, id)
	}
	for {
		s.reconcileProxyOperations(ctx)
		if ctx.Err() != nil {
			return
		}
		s.proxyAutoBindScan(ctx)
		interval, err := time.ParseDuration(s.env.ProxyStore().ProxyAutoBindConfig().ScanInterval)
		if err != nil || interval < time.Second {
			interval = 30 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.mgr.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

type proxyManualTaskScope struct {
	all     bool
	authIDs map[string]struct{}
}

func (scope proxyManualTaskScope) contains(authID string) bool {
	if scope.all {
		return true
	}
	_, ok := scope.authIDs[authID]
	return ok
}

func proxyTaskUsesAuthIDs(kind string) bool {
	switch kind {
	case "proxy.bind", "proxy.clear", "proxy.rotate", "proxy.adopt", "proxy.auto_rotate":
		return true
	default:
		return false
	}
}

func (s *Service) manualProxyTaskScope() proxyManualTaskScope {
	scope := proxyManualTaskScope{authIDs: make(map[string]struct{})}
	for _, task := range s.env.ProxyActiveTasks("proxy.", "resin.") {
		if !proxyTaskUsesAuthIDs(task.Kind) || len(task.AuthIDs) == 0 {
			scope.all = true
			continue
		}
		for _, authID := range task.AuthIDs {
			if authID = strings.TrimSpace(authID); authID != "" {
				scope.authIDs[authID] = struct{}{}
			}
		}
	}
	return scope
}

func (s *Service) proxyAutoBindScan(ctx context.Context) {
	s.mgr.scanMu.Lock()
	defer s.mgr.scanMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	config := s.env.ProxyStore().ProxyAutoBindConfig()
	if !config.Enabled {
		s.dropProxyAutoQueue()
		return
	}
	if err := s.env.ProxyEnsureCatalog(ctx); err != nil {
		s.setProxyWaiting("catalog_unknown", err)
		return
	}
	manualScope := s.manualProxyTaskScope()
	if manualScope.all {
		s.setProxyWaiting("manual_task", nil)
		return
	}
	maxAge, _ := time.ParseDuration(config.HealthMaxAge)
	opts := proxyBindOptions{SourceScope: config.SourceScope, SourceIDs: config.SourceIDs, RequireHealthy: config.RequireHealthy, HealthMaxAge: maxAge, DryRun: true, Auto: true}
	if entry, _ := s.selectProxySource(opts, ""); entry == nil {
		reason := "waiting_sources"
		if config.RequireHealthy {
			reason = "waiting_health"
		}
		s.setProxyWaiting(reason, nil)
		return
	}
	accounts := s.env.ProxyCatalogSnapshot()
	budget := min(len(accounts), max(128, min(4096, config.MaxPerCycle*8)))
	s.mgr.mu.Lock()
	offset := s.mgr.scanOffset
	s.mgr.runtime.LastScanAt = time.Now().UTC()
	s.mgr.runtime.WaitingReason = ""
	s.mgr.mu.Unlock()
	window := make([]Account, budget)
	for i := range window {
		window[i] = accounts[(offset+i)%len(accounts)]
	}
	states := s.env.ProxyStore().ProxyAccountsForIDs(proxyCatalogAccountIDs(window))
	observations := make([]proxyObservation, len(window))
	unknown := make([]int, 0, min(32, len(window)))
	for i, a := range window {
		if manualScope.contains(a.ID) {
			continue
		}
		observations[i] = s.proxyObservationFor(a)
		accountState := states[i]
		if accountState.AutoBind.Excluded || accountState.AutoBind.NextAttemptAt.After(time.Now()) || !proxyAccountAllowedAuto(a, accountState.State, accountState.DisableKind, config) {
			continue
		}
		if accountState.Managed() {
			continue
		}
		if !observations[i].Known && a.Path == "" && len(unknown) < min(32, config.MaxPerCycle) {
			unknown = append(unknown, i)
		}
	}
	_ = tasks.RunBoundedQueue(ctx, unknown, 32, 4, func(jobCtx context.Context, i int) { observations[i] = s.confirmProxyObservation(jobCtx, window[i]) })
	visited := 0
	for i, a := range window {
		if ctx.Err() != nil {
			break
		}
		visited++
		accountState := states[i]
		observation := observations[i]
		if manualScope.contains(a.ID) {
			continue
		}
		if !observation.Known || observation.URL != "" || accountState.AutoBind.Excluded || accountState.AutoBind.NextAttemptAt.After(time.Now()) || !proxyAccountAllowedAuto(a, accountState.State, accountState.DisableKind, config) {
			continue
		}
		if accountState.Managed() || s.env.ProxyStore().ProxyPending(a.ID) != "" {
			continue
		}
		s.mgr.mu.Lock()
		if len(s.mgr.queue)-s.mgr.queueHead >= config.QueueSize {
			s.mgr.mu.Unlock()
			visited--
			break
		}
		if _, exists := s.mgr.queued[a.ID]; !exists {
			s.mgr.queued[a.ID] = struct{}{}
			s.mgr.queue = append(s.mgr.queue, a.ID)
		}
		s.mgr.mu.Unlock()
	}
	s.mgr.mu.Lock()
	if len(accounts) > 0 {
		s.mgr.scanOffset = (offset + visited) % len(accounts)
	}
	count := min(config.MaxPerCycle, len(s.mgr.queue)-s.mgr.queueHead)
	jobs := append([]string(nil), s.mgr.queue[s.mgr.queueHead:s.mgr.queueHead+count]...)
	s.mgr.queueHead += count
	if s.mgr.queueHead == len(s.mgr.queue) {
		s.mgr.queue = nil
		s.mgr.queueHead = 0
	} else if s.mgr.queueHead >= config.QueueSize {
		s.mgr.queue = append([]string(nil), s.mgr.queue[s.mgr.queueHead:]...)
		s.mgr.queueHead = 0
	}
	s.mgr.runtime.Running = count > 0
	s.mgr.mu.Unlock()
	opts.DryRun = false
	_ = tasks.RunBoundedQueue(ctx, jobs, min(count, 256), config.Concurrency, func(jobCtx context.Context, id string) {
		defer func() {
			s.mgr.mu.Lock()
			delete(s.mgr.queued, id)
			delete(s.mgr.inFlight, id)
			s.mgr.mu.Unlock()
		}()
		scope := s.manualProxyTaskScope()
		if !s.env.ProxyStore().ProxyAutoBindConfig().Enabled || scope.contains(id) {
			return
		}
		s.mgr.mu.Lock()
		s.mgr.inFlight[id] = struct{}{}
		s.mgr.mu.Unlock()
		result, err := s.bindProxy(jobCtx, id, opts)
		s.recordProxyAutoResult(id, result, err)
	})
	s.mgr.mu.Lock()
	for _, id := range jobs {
		delete(s.mgr.queued, id)
	}
	s.mgr.runtime.Running = false
	s.mgr.mu.Unlock()
}

func (s *Service) setProxyWaiting(reason string, err error) {
	s.mgr.mu.Lock()
	s.mgr.runtime.WaitingReason = reason
	s.mgr.runtime.Running = false
	if err != nil {
		s.mgr.runtime.LastError = redact.SanitizeError(err)
	}
	s.mgr.mu.Unlock()
}

func (s *Service) recordProxyAutoResult(id string, result proxyBindingResult, err error) {
	s.mgr.mu.Lock()
	if result.Committed {
		s.mgr.runtime.BoundTotal++
		s.mgr.runtime.LastSuccessAt = time.Now().UTC()
	} else if err != nil {
		s.mgr.runtime.FailedTotal++
		s.mgr.runtime.LastError = redact.SanitizeError(err)
	} else {
		s.mgr.runtime.SkippedTotal++
	}
	s.mgr.mu.Unlock()
	if err == nil || result.Committed || errors.Is(err, context.Canceled) {
		return
	}
	lock := s.env.ProxyAccountLock(id)
	lock.Lock()
	defer lock.Unlock()
	state := s.env.ProxyStore().ProxyAccount(id)
	if _, bound := proxyBindingSource(state); bound {
		return
	}
	config := s.env.ProxyStore().ProxyAutoBindConfig()
	initial, _ := time.ParseDuration(config.RetryInitial)
	maximum, _ := time.ParseDuration(config.RetryMax)
	if initial <= 0 {
		initial = time.Minute
	}
	if maximum < initial {
		maximum = time.Hour
	}
	state.AutoBind.Attempts = min(64, state.AutoBind.Attempts+1)
	delay := initial
	for i := 1; i < state.AutoBind.Attempts && delay < maximum; i++ {
		if delay > maximum/2 {
			delay = maximum
		} else {
			delay *= 2
		}
	}
	delay = min(delay, maximum)
	delay = delay / 100 * time.Duration(90+randomIndex(21))
	state.AutoBind.NextAttemptAt = time.Now().UTC().Add(delay)
	state.AutoBind.LastError = redact.SanitizeError(err)
	state.AutoBind.LastSourceID = result.SourceID
	_ = s.env.ProxyStore().CommitProxy(proxyCommit{Account: &proxyAccountCommit{AuthID: id, Binding: state.Proxy, AutoBind: state.AutoBind}})
}

func (s *Service) setProxyAutoBindForAuths(ids []string, excluded bool) (map[string]any, error) {
	for _, id := range appconfig.UniqueNonEmptyStrings(ids) {
		lock := s.env.ProxyAccountLock(id)
		lock.Lock()
		state := s.env.ProxyStore().ProxyAccount(id)
		state.AutoBind.Excluded = excluded
		commit := proxyCommit{Account: &proxyAccountCommit{AuthID: id, Binding: state.Proxy, AutoBind: state.AutoBind}}
		if opID := s.env.ProxyStore().ProxyPending(id); opID != "" {
			operation, _ := s.env.ProxyStore().ProxyOperation(opID)
			operation.ExcludeAutoBind = excluded
			commit.Operation = &operation
		}
		err := s.env.ProxyStore().CommitProxy(commit)
		lock.Unlock()
		if err != nil {
			return nil, err
		}
	}
	s.wakeProxyAutoBind()
	return map[string]any{"count": len(ids), "excluded": excluded}, nil
}

func (s *Service) proxyBindUnbound(ctx context.Context, opts proxyBindOptions) ([]proxyBindingResult, error) {
	if err := s.env.ProxyEnsureCatalog(ctx); err != nil {
		return nil, err
	}
	ids := []string{}
	s.walkProxyCatalog(func(row proxyCatalogRow) {
		if row.Binding.SourceID == "" && (row.Observation.Known && row.Observation.URL == "" || row.Account.Path == "" && !row.Observation.Known) {
			ids = append(ids, row.Account.ID)
		}
	})
	results := make([]proxyBindingResult, len(ids))
	err := tasks.RunBoundedQueue(ctx, proxyIndices(len(ids)), min(256, len(ids)), s.proxyWorkerCount(), func(jobCtx context.Context, i int) {
		result, err := s.bindProxy(jobCtx, ids[i], opts)
		if err != nil && result.Error == "" {
			result.Error = redact.SanitizeError(err)
		}
		results[i] = result
		tasks.ReportItem(jobCtx, result)
	})
	return results, err
}

func (s *Service) proxyHealthLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.refreshProxySourceHealth(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) refreshProxySourceHealth(ctx context.Context) {
	config := s.env.ProxyStore().ProxyAutoBindConfig()
	if !config.Enabled || !config.RequireHealthy {
		return
	}
	age, _ := time.ParseDuration(config.HealthMaxAge)
	catalog := s.proxyCatalog()
	ids := make(map[string]struct{}, len(config.SourceIDs))
	for _, id := range config.SourceIDs {
		ids[id] = struct{}{}
	}
	jobs := make([]string, 0, 8)
	for _, entry := range catalog.Sources {
		if len(jobs) >= 8 {
			break
		}
		if !proxyScopeContains(config.SourceScope, entry.Kind) || entry.Error != nil {
			continue
		}
		if len(ids) > 0 {
			if _, ok := ids[entry.ID]; !ok {
				continue
			}
		}
		if !proxyCandidateEnabled(entry) || proxySourceHealthFresh(entry, age) || (!entry.Runtime.HealthChecked.IsZero() && time.Since(entry.Runtime.HealthChecked) < time.Minute) {
			continue
		}
		jobs = append(jobs, entry.ID)
	}
	_ = tasks.RunBoundedQueue(ctx, jobs, 8, 2, func(jobCtx context.Context, id string) { _, _ = s.testProxySource(jobCtx, id, "") })
}

func proxyCandidateEnabled(entry proxySourceCandidate) bool {
	return entry.Node != nil && entry.Node.Enabled || entry.Template != nil && entry.Template.Enabled
}
func proxyCatalogAccountIDs(accounts []Account) []string {
	ids := make([]string, len(accounts))
	for index, account := range accounts {
		ids[index] = strings.TrimSpace(account.ID)
	}
	return ids
}
