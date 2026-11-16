package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

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

func (p *Plugin) updateProxyAutoBind(params map[string]any) (proxy.AutoBindConfig, bool, error) {
	config := p.stateStore().ProxyAutoBindConfig()
	encoded, err := json.Marshal(params)
	if err != nil {
		return config, false, err
	}
	if err = json.Unmarshal(encoded, &config); err != nil {
		return config, false, proxyError("auto_bind_config_invalid", 400, err.Error())
	}
	settings := ProxyConfig{AutoBind: config}
	if err = normalizeProxyConfig(&settings); err != nil {
		return config, false, proxyError("auto_bind_config_invalid", 400, err.Error())
	}
	config = settings.AutoBind
	for _, id := range config.SourceIDs {
		entry := p.proxyCatalog().ByID[id]
		if entry == nil {
			return config, false, proxyError("proxy_source_not_found", 400, "unknown source: "+id)
		}
		if !proxyScopeContains(config.SourceScope, entry.Kind) {
			return config, false, proxyError("auto_bind_scope_invalid", 400, "selected source is outside source_scope: "+id)
		}
	}
	p.mu.RLock()
	rotate := p.cfg.Proxy.AutoRotateAfterQualityFailure
	p.mu.RUnlock()
	if _, ok := params["auto_rotate_after_quality_failure"]; ok {
		rotate = optionalBoolParam(params, "auto_rotate_after_quality_failure", rotate)
	}
	err = p.stateStore().Update(func(state *State) error {
		state.Proxy.AutoBind = config
		state.Policy.ProxyAutoRotateAfterQualityFail = rotate
		state.Policy.ResinAutoRotateAfterQualityFail = rotate
		return nil
	})
	if err != nil {
		return config, rotate, err
	}
	p.mu.Lock()
	p.cfg.Proxy.AutoBind = config
	p.cfg.Proxy.AutoRotateAfterQualityFailure = rotate
	p.cfg.Resin.AutoRotateAfterQualityFailure = rotate
	p.policy.ProxyAutoRotateAfterQualityFail = rotate
	p.policy.ResinAutoRotateAfterQualityFail = rotate
	p.mu.Unlock()
	if !config.Enabled {
		p.dropProxyAutoQueue()
	}
	p.wakeProxyAutoBind()
	return config, rotate, nil
}

func (p *Plugin) proxyAutoBindRuntime() proxy.AutoBindRuntime {
	p.proxyMgr.mu.Lock()
	runtime := p.proxyMgr.runtime
	runtime.QueueDepth = len(p.proxyMgr.queue) - p.proxyMgr.queueHead
	runtime.InFlight = len(p.proxyMgr.inFlight)
	p.proxyMgr.mu.Unlock()
	runtime.Enabled = p.stateStore().ProxyAutoBindConfig().Enabled
	return runtime
}

func (p *Plugin) dropProxyAutoQueue() {
	p.proxyMgr.mu.Lock()
	defer p.proxyMgr.mu.Unlock()
	for _, id := range p.proxyMgr.queue[p.proxyMgr.queueHead:] {
		if _, running := p.proxyMgr.inFlight[id]; !running {
			delete(p.proxyMgr.queued, id)
		}
	}
	p.proxyMgr.queue = nil
	p.proxyMgr.queueHead = 0
	p.proxyMgr.runtime.Running = false
	p.proxyMgr.runtime.WaitingReason = "disabled"
}

func (p *Plugin) proxyAutoBindLoop(ctx context.Context) {
	recovered := p.stateStore().TakeRecoveredProxyChanges()
	healthCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.proxyHealthLoop(healthCtx) }()
	defer func() { cancel(); wg.Wait() }()
	p.reconcileProxyOperations(ctx)
	for _, id := range recovered {
		if ctx.Err() != nil {
			return
		}
		_, _ = p.runProxyChange(ctx, id)
	}
	for {
		p.reconcileProxyOperations(ctx)
		if ctx.Err() != nil {
			return
		}
		p.proxyAutoBindScan(ctx)
		interval, err := time.ParseDuration(p.stateStore().ProxyAutoBindConfig().ScanInterval)
		if err != nil || interval < time.Second {
			interval = 30 * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-p.proxyMgr.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (p *Plugin) manualProxyTaskActive() bool {
	p.tasksMu.Lock()
	defer p.tasksMu.Unlock()
	for _, record := range p.tasks {
		if record != nil && (record.task.Status == taskStatusQueued || record.task.Status == taskStatusRunning) && (strings.HasPrefix(record.task.Kind, "proxy.") || strings.HasPrefix(record.task.Kind, "resin.")) {
			return true
		}
	}
	return false
}

func (p *Plugin) proxyAutoBindScan(ctx context.Context) {
	p.proxyMgr.scanMu.Lock()
	defer p.proxyMgr.scanMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	config := p.stateStore().ProxyAutoBindConfig()
	if !config.Enabled {
		p.dropProxyAutoQueue()
		return
	}
	if err := p.ensureCatalog(ctx); err != nil {
		p.setProxyWaiting("catalog_unknown", err)
		return
	}
	if p.manualProxyTaskActive() {
		p.setProxyWaiting("manual_task", nil)
		return
	}
	maxAge, _ := time.ParseDuration(config.HealthMaxAge)
	opts := proxyBindOptions{SourceScope: config.SourceScope, SourceIDs: config.SourceIDs, RequireHealthy: config.RequireHealthy, HealthMaxAge: maxAge, DryRun: true, Auto: true}
	if entry, _ := p.selectProxySource(opts, ""); entry == nil {
		reason := "waiting_sources"
		if config.RequireHealthy {
			reason = "waiting_health"
		}
		p.setProxyWaiting(reason, nil)
		return
	}
	accounts := p.catalogSnapshot()
	budget := min(len(accounts), max(128, min(4096, config.MaxPerCycle*8)))
	p.proxyMgr.mu.Lock()
	offset := p.proxyMgr.scanOffset
	p.proxyMgr.runtime.LastScanAt = time.Now().UTC()
	p.proxyMgr.runtime.WaitingReason = ""
	p.proxyMgr.mu.Unlock()
	window := make([]Account, budget)
	for i := range window {
		window[i] = accounts[(offset+i)%len(accounts)]
	}
	states := p.stateStore().ProxyAccountsForIDs(catalogAccountIDs(window))
	observations := make([]proxyObservation, len(window))
	unknown := make([]int, 0, min(32, len(window)))
	for i, a := range window {
		observations[i] = p.proxyObservationFor(a)
		s := states[i]
		if s.AutoBind.Excluded || s.AutoBind.NextAttemptAt.After(time.Now()) || !proxyAccountAllowedAuto(a, s.State, s.DisableKind, config) {
			continue
		}
		if s.managed() {
			continue
		}
		if !observations[i].Known && a.Path == "" && len(unknown) < min(32, config.MaxPerCycle) {
			unknown = append(unknown, i)
		}
	}
	_ = runBoundedQueue(ctx, unknown, 32, 4, func(jobCtx context.Context, i int) { observations[i] = p.confirmProxyObservation(jobCtx, window[i]) })
	visited := 0
	for i, a := range window {
		if ctx.Err() != nil {
			break
		}
		visited++
		s := states[i]
		observation := observations[i]
		if !observation.Known || observation.URL != "" || s.AutoBind.Excluded || s.AutoBind.NextAttemptAt.After(time.Now()) || !proxyAccountAllowedAuto(a, s.State, s.DisableKind, config) {
			continue
		}
		if s.managed() || p.stateStore().ProxyPending(a.ID) != "" {
			continue
		}
		p.proxyMgr.mu.Lock()
		if len(p.proxyMgr.queue)-p.proxyMgr.queueHead >= config.QueueSize {
			p.proxyMgr.mu.Unlock()
			visited--
			break
		}
		if _, exists := p.proxyMgr.queued[a.ID]; !exists {
			p.proxyMgr.queued[a.ID] = struct{}{}
			p.proxyMgr.queue = append(p.proxyMgr.queue, a.ID)
		}
		p.proxyMgr.mu.Unlock()
	}
	p.proxyMgr.mu.Lock()
	if len(accounts) > 0 {
		p.proxyMgr.scanOffset = (offset + visited) % len(accounts)
	}
	count := min(config.MaxPerCycle, len(p.proxyMgr.queue)-p.proxyMgr.queueHead)
	jobs := append([]string(nil), p.proxyMgr.queue[p.proxyMgr.queueHead:p.proxyMgr.queueHead+count]...)
	p.proxyMgr.queueHead += count
	if p.proxyMgr.queueHead == len(p.proxyMgr.queue) {
		p.proxyMgr.queue = nil
		p.proxyMgr.queueHead = 0
	} else if p.proxyMgr.queueHead >= config.QueueSize {
		p.proxyMgr.queue = append([]string(nil), p.proxyMgr.queue[p.proxyMgr.queueHead:]...)
		p.proxyMgr.queueHead = 0
	}
	p.proxyMgr.runtime.Running = count > 0
	p.proxyMgr.mu.Unlock()
	opts.DryRun = false
	_ = runBoundedQueue(ctx, jobs, min(count, 256), config.Concurrency, func(jobCtx context.Context, id string) {
		defer func() {
			p.proxyMgr.mu.Lock()
			delete(p.proxyMgr.queued, id)
			delete(p.proxyMgr.inFlight, id)
			p.proxyMgr.mu.Unlock()
		}()
		if !p.stateStore().ProxyAutoBindConfig().Enabled || p.manualProxyTaskActive() {
			return
		}
		p.proxyMgr.mu.Lock()
		p.proxyMgr.inFlight[id] = struct{}{}
		p.proxyMgr.mu.Unlock()
		result, err := p.bindProxy(jobCtx, id, opts)
		p.recordProxyAutoResult(id, result, err)
	})
	p.proxyMgr.mu.Lock()
	for _, id := range jobs {
		delete(p.proxyMgr.queued, id)
	}
	p.proxyMgr.runtime.Running = false
	p.proxyMgr.mu.Unlock()
}

func (p *Plugin) setProxyWaiting(reason string, err error) {
	p.proxyMgr.mu.Lock()
	p.proxyMgr.runtime.WaitingReason = reason
	p.proxyMgr.runtime.Running = false
	if err != nil {
		p.proxyMgr.runtime.LastError = sanitizeError(err)
	}
	p.proxyMgr.mu.Unlock()
}

func (p *Plugin) recordProxyAutoResult(id string, result proxyBindingResult, err error) {
	p.proxyMgr.mu.Lock()
	if result.Committed {
		p.proxyMgr.runtime.BoundTotal++
		p.proxyMgr.runtime.LastSuccessAt = time.Now().UTC()
	} else if err != nil {
		p.proxyMgr.runtime.FailedTotal++
		p.proxyMgr.runtime.LastError = sanitizeError(err)
	} else {
		p.proxyMgr.runtime.SkippedTotal++
	}
	p.proxyMgr.mu.Unlock()
	if err == nil || result.Committed || errors.Is(err, context.Canceled) {
		return
	}
	lock := p.accountLock(id)
	lock.Lock()
	defer lock.Unlock()
	state := p.stateStore().ProxyAccount(id)
	if _, bound := proxyBindingSource(state); bound {
		return
	}
	config := p.stateStore().ProxyAutoBindConfig()
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
	state.AutoBind.LastError = sanitizeError(err)
	state.AutoBind.LastSourceID = result.SourceID
	_ = p.stateStore().CommitProxy(proxyCommit{Account: &proxyAccountCommit{AuthID: id, Binding: state.Proxy, Resin: state.Resin, AutoBind: state.AutoBind}})
}

func (p *Plugin) setProxyAutoBindForAuths(ids []string, excluded bool) (map[string]any, error) {
	for _, id := range uniqueNonEmptyStrings(ids) {
		lock := p.accountLock(id)
		lock.Lock()
		state := p.stateStore().ProxyAccount(id)
		state.AutoBind.Excluded = excluded
		commit := proxyCommit{Account: &proxyAccountCommit{AuthID: id, Binding: state.Proxy, Resin: state.Resin, AutoBind: state.AutoBind}}
		if opID := p.stateStore().ProxyPending(id); opID != "" {
			operation, _ := p.stateStore().ProxyOperation(opID)
			operation.ExcludeAutoBind = excluded
			commit.Operation = &operation
		}
		err := p.stateStore().CommitProxy(commit)
		lock.Unlock()
		if err != nil {
			return nil, err
		}
	}
	p.wakeProxyAutoBind()
	return map[string]any{"count": len(ids), "excluded": excluded}, nil
}

func (p *Plugin) proxyBindUnbound(ctx context.Context, opts proxyBindOptions) ([]proxyBindingResult, error) {
	if err := p.ensureCatalog(ctx); err != nil {
		return nil, err
	}
	ids := []string{}
	p.walkProxyCatalog(func(row proxyCatalogRow) {
		if row.Binding.SourceID == "" && (row.Observation.Known && row.Observation.URL == "" || row.Account.Path == "" && !row.Observation.Known) {
			ids = append(ids, row.Account.ID)
		}
	})
	results := make([]proxyBindingResult, len(ids))
	err := runBoundedQueue(ctx, proxyIndices(len(ids)), min(256, len(ids)), p.proxyWorkerCount(), func(jobCtx context.Context, i int) {
		result, err := p.bindProxy(jobCtx, ids[i], opts)
		if err != nil && result.Error == "" {
			result.Error = sanitizeError(err)
		}
		results[i] = result
	})
	return results, err
}

func (p *Plugin) proxyHealthLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		p.refreshProxySourceHealth(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Plugin) refreshProxySourceHealth(ctx context.Context) {
	config := p.stateStore().ProxyAutoBindConfig()
	if !config.Enabled || !config.RequireHealthy {
		return
	}
	age, _ := time.ParseDuration(config.HealthMaxAge)
	catalog := p.proxyCatalog()
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
	_ = runBoundedQueue(ctx, jobs, 8, 2, func(jobCtx context.Context, id string) { _, _ = p.testProxySource(jobCtx, id, "") })
}

func proxyCandidateEnabled(entry proxySourceCandidate) bool {
	return entry.Node != nil && entry.Node.Enabled || entry.Template != nil && entry.Template.Enabled || entry.ResinSettings != nil && entry.ResinSettings.Enabled && !entry.ResinSettings.LocalOnly
}
