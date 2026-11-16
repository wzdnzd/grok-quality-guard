package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	proxy "grok-quality-guard/internal/proxy"
)

func proxyURLHash(value string) string {
	if endpoint, err := proxy.ParseEndpoint(value, proxy.ParseOptions{}); err == nil {
		return endpoint.Fingerprint()
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func bindingMatchesURL(binding proxy.Binding, value string) bool {
	if value == "" {
		return false
	}
	if binding.EndpointHash != "" {
		return binding.EndpointHash == proxyURLHash(value)
	}
	// A migrated lease has ownership evidence but no old endpoint fingerprint.
	// Only the recorded identity may be used; never recognize a host alone.
	return false
}

func (p *Plugin) commitProxyOperationLocked(ctx context.Context, response pluginapi.HostAuthGetResponse, raw map[string]json.RawMessage, operation proxy.Operation, automatic bool) (proxyBindingResult, error) {
	store := p.stateStore()
	result := proxyBindingResult{AuthID: operation.AuthID, SourceID: operation.Binding.SourceID, SourceKind: operation.Binding.SourceKind, OperationID: operation.ID}
	if operation.Clear {
		result.SourceID = operation.PreviousSourceID
		if state := store.ProxyAccount(operation.AuthID); state.Proxy.SourceKind != "" {
			result.SourceKind = state.Proxy.SourceKind
		}
	}
	if ctx.Err() != nil {
		return failedProxyResult(operation.AuthID, "failed", ctx.Err()), ctx.Err()
	}
	if err := store.PrepareProxy(operation, automatic); err != nil {
		return failedProxyResult(operation.AuthID, "failed", err), err
	}
	// Read immediately before patching; tokens and unknown fields are taken from
	// the latest auth under the same gate used by probes and quality mutations.
	_, latestResponse, latest, err := p.resolveAuth(ctx, operation.AuthID)
	if err != nil {
		return p.failProxyOperation(operation, err, false)
	}
	current, err := proxyURLFromRaw(latest)
	if err != nil {
		return p.failProxyOperation(operation, err, true)
	}
	if proxyURLHash(current) != operation.ExpectedHash {
		err := proxyError("proxy_binding_conflict", 409, "auth proxy changed before save")
		return p.failProxyOperation(operation, err, true)
	}
	response = latestResponse
	raw = latest
	if operation.Clear {
		delete(raw, "proxy_url")
	} else {
		setRawString(raw, "proxy_url", operation.DesiredURL)
	}
	encoded, err := marshalRawMap(raw)
	if err != nil {
		return p.failProxyOperation(operation, err, false)
	}
	if ctx.Err() != nil {
		return p.failProxyOperation(operation, ctx.Err(), false)
	}
	if operation.ChangeID != "" && store.ProxyChangeStatus(operation.ChangeID) == "canceled" {
		return p.failProxyOperation(operation, proxyError("proxy_change_canceled", 409, "source change was canceled before save"), false)
	}
	if operation.Auto && !store.ProxyAutoBindConfig().Enabled {
		return p.failProxyOperation(operation, proxyError("auto_bind_disabled", 409, "automatic binding was disabled before save"), false)
	}
	if operation.Auto {
		config := store.ProxyAutoBindConfig()
		state := store.ProxyAccount(operation.AuthID)
		a, found := p.findAccount(operation.AuthID)
		if !found {
			return p.failProxyOperation(operation, proxyError("auth_proxy_unknown", 409, "auth disappeared before automatic save"), false)
		}
		a.Disabled = authDisabled(raw)
		if !proxyAccountAllowedAuto(a, state.State, state.DisableKind, config) || !proxyScopeContains(config.SourceScope, operation.Binding.SourceKind) {
			return p.failProxyOperation(operation, proxyError("auto_bind_ineligible", 409, "automatic binding scope or account eligibility changed"), false)
		}
		entry := p.proxyCatalog().ByID[operation.Binding.SourceID]
		age, _ := time.ParseDuration(config.HealthMaxAge)
		if entry == nil || !proxyCandidateEnabled(*entry) || entry.Error != nil || (config.RequireHealthy && !proxySourceHealthFresh(*entry, age)) {
			return p.failProxyOperation(operation, proxyError("auto_bind_source_unavailable", 409, "automatic source availability changed"), false)
		}
		if len(config.SourceIDs) > 0 {
			allowed := false
			for _, id := range config.SourceIDs {
				if id == operation.Binding.SourceID {
					allowed = true
				}
			}
			if !allowed {
				return p.failProxyOperation(operation, proxyError("auto_bind_scope_changed", 409, "automatic source selection changed"), false)
			}
		}
	}
	_, saveErr := p.saveAuth(response.Name, encoded)
	if saveErr != nil {
		// The ABI can fail after the host committed. Inspect reality before
		// deciding whether it is safe to retry a write.
		_, _, actual, readErr := p.resolveAuth(context.WithoutCancel(ctx), operation.AuthID)
		if readErr != nil {
			result.Status = "commit_unknown"
			result.CommitUnknown = true
			result.NeedsReconcile = true
			result.Error = sanitizeError(saveErr)
			return result, saveErr
		}
		actualURL, valueErr := proxyURLFromRaw(actual)
		if valueErr != nil {
			return p.failProxyOperation(operation, valueErr, true)
		}
		if proxyURLHash(actualURL) != proxyURLHash(operation.DesiredURL) {
			if proxyURLHash(actualURL) == operation.ExpectedHash {
				return p.failProxyOperation(operation, saveErr, false)
			}
			return p.failProxyOperation(operation, proxyError("proxy_binding_conflict", 409, "auth proxy changed during save"), true)
		}
	}
	_, _, verified, readErr := p.resolveAuth(context.WithoutCancel(ctx), operation.AuthID)
	if readErr != nil {
		result.Status = "commit_unknown"
		result.CommitUnknown = true
		result.NeedsReconcile = true
		result.Error = sanitizeError(readErr)
		return result, readErr
	}
	verifiedURL, valueErr := proxyURLFromRaw(verified)
	if valueErr != nil {
		return p.failProxyOperation(operation, valueErr, true)
	}
	if proxyURLHash(verifiedURL) != proxyURLHash(operation.DesiredURL) {
		return p.failProxyOperation(operation, proxyError("proxy_binding_conflict", 409, "auth proxy changed after save"), true)
	}
	return p.finalizeProxyOperation(operation)
}

func (p *Plugin) failProxyOperation(operation proxy.Operation, err error, reconcile bool) (proxyBindingResult, error) {
	operation.Status = "failed"
	if reconcile {
		operation.Status = "needs_reconcile"
		operation.NextAttemptAt = time.Now().UTC().Add(time.Minute)
	}
	operation.Error = sanitizeError(err)
	operation.UpdatedAt = time.Now().UTC()
	commit := proxyCommit{Operation: &operation}
	if operation.ChangeID != "" {
		item := proxy.ChangeItem{AuthID: operation.AuthID, Generation: operation.ExpectedGeneration, EndpointHash: operation.ExpectedHash, OperationID: operation.ID, Status: operation.Status, Error: operation.Error}
		commit.ChangeID = operation.ChangeID
		commit.ChangeItem = &item
	}
	persistErr := p.stateStore().CommitProxy(commit)
	result := failedProxyResult(operation.AuthID, operation.Status, err)
	result.OperationID = operation.ID
	result.SourceID = operation.Binding.SourceID
	result.SourceKind = operation.Binding.SourceKind
	result.NeedsReconcile = reconcile || persistErr != nil
	return result, errors.Join(err, persistErr)
}

func (p *Plugin) finalizeProxyOperation(operation proxy.Operation) (proxyBindingResult, error) {
	store := p.stateStore()
	current := store.ProxyAccount(operation.AuthID)
	auto := current.AutoBind
	auto.Excluded = operation.ExcludeAutoBind
	auto.Attempts = 0
	auto.LastError = ""
	auto.NextAttemptAt = time.Time{}
	legacy := current.Resin
	if operation.Clear {
		legacy.Managed = false
		legacy.Account = ""
	} else if operation.Binding.SourceKind == proxy.SourceResin {
		legacy = ResinState{Managed: true, Generation: int64(operation.Binding.Generation)}
		if operation.Binding.Lease != nil {
			legacy.Account = operation.Binding.Lease.Account
		}
	} else {
		legacy = ResinState{}
	}
	now := time.Now().UTC()
	operation.Status = "finalized"
	operation.NextAttemptAt = time.Time{}
	operation.Error = ""
	operation.UpdatedAt = now
	commit := proxyCommit{Operation: &operation, Account: &proxyAccountCommit{AuthID: operation.AuthID, Binding: operation.Binding, AutoBind: auto, Resin: legacy}, DeleteOperations: store.PrunableProxyOperations()}
	status := "succeeded"
	if operation.Clear {
		commit.Account.Binding = proxy.Binding{Generation: current.Proxy.Generation}
		status = "cleared"
	}
	switch operation.Action {
	case "proxy_bind", "resin_bind":
		status = "bound"
	case "proxy_rotate", "resin_rotate":
		status = "rotated"
	}
	if operation.OldLease != nil && (operation.Clear || operation.Binding.Lease == nil || operation.OldLease.SnapshotID != operation.Binding.Lease.SnapshotID || operation.OldLease.Account != operation.Binding.Lease.Account) {
		if job, ok := p.cleanupForLease(operation.AuthID, *operation.OldLease); ok {
			commit.Cleanup = []ResinLeaseDelete{job}
		}
	}
	detail := "source=" + operation.Binding.SourceID
	if operation.Binding.Lease != nil {
		detail += " account=" + operation.Binding.Lease.Account
	}
	if len(commit.Cleanup) > 0 {
		detail += " remote_lease=delete_queued"
	} else if operation.Binding.SourceKind == proxy.SourceResin || operation.OldLease != nil {
		if operation.Binding.Lease != nil && operation.Binding.Lease.Provider == "resin-local" || operation.OldLease != nil && operation.OldLease.Provider == "resin-local" {
			detail += " remote_lease=skipped_local_only 未调用 Resin API"
		} else {
			detail += " remote_lease=not_queued"
		}
	}
	commit.Event = &Event{Time: now, AuthID: operation.AuthID, Action: operation.Action, Result: status, Detail: detail}
	if operation.ChangeID != "" {
		item := proxy.ChangeItem{AuthID: operation.AuthID, Generation: operation.ExpectedGeneration, EndpointHash: operation.ExpectedHash, OperationID: operation.ID, Status: "succeeded"}
		commit.ChangeID = operation.ChangeID
		commit.ChangeItem = &item
	}
	result := proxyBindingResult{AuthID: operation.AuthID, SourceID: operation.Binding.SourceID, SourceKind: operation.Binding.SourceKind, OperationID: operation.ID, Status: status, Committed: true, Generation: operation.Binding.Generation, ProxyURL: maskProxyURL(operation.DesiredURL), RemoteLeaseDeleteQueued: len(commit.Cleanup) > 0}
	if operation.Clear {
		result.SourceID = operation.PreviousSourceID
	}
	if operation.Binding.Lease != nil {
		result.LeaseAccount = operation.Binding.Lease.Account
	}
	if err := store.CommitProxy(commit); err != nil {
		result.Status = "needs_reconcile"
		result.NeedsReconcile = true
		result.Error = sanitizeError(err)
		p.updateCatalogProxy(operation.AuthID, operation.DesiredURL)
		return result, err
	}
	p.updateCatalogProxy(operation.AuthID, operation.DesiredURL)
	if result.RemoteLeaseDeleteQueued {
		p.wakeResinLeaseDeleteLoop()
	}
	p.wakeProxyAutoBind()
	return result, nil
}

func (p *Plugin) resumeProxyOperationLocked(ctx context.Context, authID string, operation proxy.Operation) (proxyBindingResult, error) {
	if operation.AuthID != authID {
		err := proxyError("proxy_operation_conflict", 409, "operation belongs to another auth")
		return failedProxyResult(authID, "conflict", err), err
	}
	if operation.Status == "finalized" {
		status := "succeeded"
		if operation.Clear {
			status = "cleared"
		} else if operation.Action == "proxy_bind" || operation.Action == "resin_bind" {
			status = "bound"
		} else if strings.Contains(operation.Action, "rotate") {
			status = "rotated"
		}
		return proxyBindingResult{AuthID: authID, OperationID: operation.ID, SourceID: operation.Binding.SourceID, SourceKind: operation.Binding.SourceKind, Status: status, Committed: true, Generation: operation.Binding.Generation, ProxyURL: maskProxyURL(operation.DesiredURL)}, nil
	}
	currentState := p.stateStore().ProxyAccount(authID)
	currentBinding, _ := bindingForAccount(currentState)
	if (currentBinding.Generation != operation.ExpectedGeneration || currentBinding.SourceID != operation.PreviousSourceID) && !(currentBinding.Generation == operation.Binding.Generation && currentBinding.SourceID == operation.Binding.SourceID && currentBinding.EndpointHash == operation.Binding.EndpointHash) {
		operation.Status = "superseded"
		operation.UpdatedAt = time.Now().UTC()
		commit := proxyCommit{Operation: &operation}
		if operation.ChangeID != "" {
			item := proxy.ChangeItem{AuthID: authID, OperationID: operation.ID, Status: "superseded"}
			commit.ChangeID = operation.ChangeID
			commit.ChangeItem = &item
		}
		err := p.stateStore().CommitProxy(commit)
		return proxyBindingResult{AuthID: authID, SourceID: operation.Binding.SourceID, OperationID: operation.ID, Status: "superseded"}, err
	}
	_, response, raw, err := p.resolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	actual, err := proxyURLFromRaw(raw)
	if err != nil {
		return p.failProxyOperation(operation, err, true)
	}
	if proxyURLHash(actual) == proxyURLHash(operation.DesiredURL) {
		return p.finalizeProxyOperation(operation)
	}
	if proxyURLHash(actual) != operation.ExpectedHash {
		return p.failProxyOperation(operation, proxyError("proxy_binding_conflict", 409, "actual auth proxy differs from both operation endpoints"), true)
	}
	if operation.Auto && !p.stateStore().ProxyAutoBindConfig().Enabled {
		return p.failProxyOperation(operation, proxyError("auto_bind_disabled", 409, "automatic binding is disabled"), false)
	}
	operation.Status = "prepared"
	return p.commitProxyOperationLocked(ctx, response, raw, operation, operation.Auto)
}

func (p *Plugin) resumeProxyOperation(ctx context.Context, authID string, operation proxy.Operation) (proxyBindingResult, error) {
	if err := p.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer p.leaveProxyOperation()
	lock := p.accountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	return p.resumeProxyOperationLocked(ctx, authID, operation)
}

func (p *Plugin) reconcileProxyOperations(ctx context.Context) {
	for _, operation := range p.stateStore().ProxyPendingOperations(100) {
		if ctx.Err() != nil {
			return
		}
		lock := p.accountLock(operation.AuthID)
		lock.Lock()
		_, _ = p.resumeProxyOperationLocked(ctx, operation.AuthID, operation)
		lock.Unlock()
	}
}

func (p *Plugin) clearProxy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		err := proxyError("auth_id_required", 400, "auth_id is required")
		return failedProxyResult(authID, "failed", err), err
	}
	if err := p.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer p.leaveProxyOperation()
	lock := p.accountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if result, found, err := p.retryProxyAction(ctx, authID, opts, "proxy_clear"); found {
		return result, err
	}
	if p.stateStore().ProxyPending(authID) != "" {
		err := proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
		return failedProxyResult(authID, "reconcile_required", err), err
	}
	_, response, raw, err := p.resolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := p.stateStore().ProxyAccount(authID)
	binding, managed, verifyErr := p.verifiedProxyBinding(state, current, opts.DryRun)
	if verifyErr != nil {
		return failedProxyResult(authID, "unknown", verifyErr), verifyErr
	}
	if opts.ExpectedSourceID != "" && binding.SourceID != opts.ExpectedSourceID {
		return proxyBindingResult{AuthID: authID, Status: "superseded"}, nil
	}
	if opts.ExpectedGeneration != nil && binding.Generation != *opts.ExpectedGeneration {
		return proxyBindingResult{AuthID: authID, Status: "superseded"}, nil
	}
	if current != "" && managed && !bindingMatchesURL(binding, current) {
		err := proxyError("proxy_binding_conflict", 409, "auth proxy URL no longer matches its managed binding")
		return failedProxyResult(authID, "conflict", err), err
	}
	if current != "" && !managed && !opts.AllowExternal {
		err := proxyError("external_proxy_present", 409, "clearing an external proxy requires allow_external=true")
		return failedProxyResult(authID, "external", err), err
	}
	clearPolicy := firstNonEmptyString(opts.ManualClearPolicy, p.stateStore().ProxyAutoBindConfig().ManualClearPolicy)
	exclude := state.AutoBind.Excluded || clearPolicy == "pause"
	if opts.ExcludeAutoBind != nil {
		exclude = *opts.ExcludeAutoBind
	}
	if opts.DryRun {
		return proxyBindingResult{AuthID: authID, SourceID: binding.SourceID, SourceKind: binding.SourceKind, Status: "dry_run", ProxyURL: maskProxyURL(current), DryRun: true}, nil
	}
	if current == "" && !managed {
		if state.AutoBind.Excluded != exclude {
			state.AutoBind.Excluded = exclude
			if err := p.stateStore().CommitProxy(proxyCommit{Account: &proxyAccountCommit{AuthID: authID, Binding: state.Proxy, Resin: state.Resin, AutoBind: state.AutoBind}}); err != nil {
				return failedProxyResult(authID, "failed", err), err
			}
		}
		return proxyBindingResult{AuthID: authID, Status: "skipped"}, nil
	}
	lease := binding.Lease
	if managed && binding.SourceKind == proxy.SourceResin && lease != nil && lease.SnapshotID == "" {
		lease, err = p.captureLegacyLease(binding, current)
		if err != nil {
			return failedProxyResult(authID, "failed", err), err
		}
	}
	now := time.Now().UTC()
	operation := proxy.Operation{ID: firstNonEmptyString(opts.OperationID, newProxyID("op")), AuthID: authID, Action: firstNonEmptyString(opts.Action, "proxy_clear"), ChangeID: opts.ChangeID, ExpectedHash: proxyURLHash(current), ExpectedGeneration: binding.Generation, PreviousSourceID: binding.SourceID, Clear: true, Binding: proxy.Binding{Generation: binding.Generation}, OldLease: lease, ExcludeAutoBind: exclude, Status: "prepared", CreatedAt: now, UpdatedAt: now}
	return p.commitProxyOperationLocked(ctx, response, raw, operation, false)
}

func (p *Plugin) clearProxyWithPolicy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	return p.clearProxy(ctx, authID, opts)
}

func (p *Plugin) rotateProxy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := p.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer p.leaveProxyOperation()
	lock := p.accountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if result, found, err := p.retryProxyAction(ctx, authID, opts, "proxy_rotate"); found {
		return result, err
	}
	if p.stateStore().ProxyPending(authID) != "" {
		err := proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
		return failedProxyResult(authID, "reconcile_required", err), err
	}
	_, response, raw, err := p.resolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := p.stateStore().ProxyAccount(authID)
	binding, managed, verifyErr := p.verifiedProxyBinding(state, current, opts.DryRun)
	if verifyErr != nil {
		return failedProxyResult(authID, "unknown", verifyErr), verifyErr
	}
	if !managed || current == "" {
		err := proxyError("rotation_unavailable", 409, "auth has no usable managed proxy binding")
		return failedProxyResult(authID, "rotation_unavailable", err), err
	}
	if opts.ExpectedSourceID != "" && binding.SourceID != opts.ExpectedSourceID {
		err := proxyError("proxy_binding_conflict", 409, "auth belongs to another proxy source")
		return failedProxyResult(authID, "conflict", err), err
	}
	if !bindingMatchesURL(binding, current) {
		err := proxyError("proxy_binding_conflict", 409, "auth proxy differs from its managed binding")
		return failedProxyResult(authID, "conflict", err), err
	}
	var candidate *proxySourceCandidate
	if len(opts.SourceIDs) == 0 && (binding.SourceKind == proxy.SourceTemplate || binding.SourceKind == proxy.SourceResin) {
		candidate = p.proxyCatalog().ByID[binding.SourceID]
		if candidate == nil || candidate.Error != nil {
			err := proxyError("rotation_unavailable", 409, "original proxy source is unavailable")
			return failedProxyResult(authID, "rotation_unavailable", err), err
		}
		if candidate.Runtime.Lifecycle != "" && candidate.Runtime.Lifecycle != "active" {
			err := proxyError("proxy_source_busy", 409, "source change is still in progress")
			return failedProxyResult(authID, "rotation_unavailable", err), err
		}
	} else {
		if opts.SourceScope == "" {
			opts.SourceScope = "nodes"
			if len(opts.SourceIDs) > 0 {
				opts.SourceScope = "all"
			}
		}
		var reasons []string
		opts.AuthID = authID
		candidate, reasons = p.selectProxySource(opts, proxyURLHash(current))
		if candidate == nil {
			err := proxyError("rotation_unavailable", 409, firstNonEmptyString(strings.Join(reasons, "; "), "no different available proxy node"))
			return failedProxyResult(authID, "rotation_unavailable", err), err
		}
		if !opts.DryRun {
			defer p.releaseProxySource(candidate.ID, authID)
		}
	}
	rendered, lease, release, err := p.generateProxySource(ctx, *candidate, authID, raw, proxyURLHash(current), opts.DryRun)
	if release != nil {
		defer release()
	}
	if err != nil {
		return failedProxyResult(authID, "rotation_unavailable", err), err
	}
	if proxyURLHash(rendered.URL) == proxyURLHash(current) {
		err := proxyError("rotation_unavailable", 409, "proxy generator has no different endpoint")
		return failedProxyResult(authID, "rotation_unavailable", err), err
	}
	if opts.DryRun {
		return proxyBindingResult{AuthID: authID, SourceID: candidate.ID, SourceKind: candidate.Kind, Status: "dry_run", ProxyURL: rendered.MaskedURL, DryRun: true}, nil
	}
	oldLease := binding.Lease
	if oldLease != nil && oldLease.SnapshotID == "" {
		oldLease, err = p.captureLegacyLease(binding, current)
		if err != nil {
			return failedProxyResult(authID, "failed", err), err
		}
	}
	now := time.Now().UTC()
	next := proxy.Binding{AuthID: authID, SourceID: candidate.ID, SourceKind: candidate.Kind, SourceRevision: candidate.Revision, Generation: binding.Generation + 1, EndpointHash: proxyURLHash(rendered.URL), ResolvedValues: rendered.Values, Lease: lease, BoundAt: binding.BoundAt, UpdatedAt: now}
	action := "proxy_rotate"
	if candidate.Kind == proxy.SourceResin {
		action = "resin_rotate"
	}
	operation := proxy.Operation{ID: firstNonEmptyString(opts.OperationID, newProxyID("op")), AuthID: authID, Action: action, ExpectedHash: proxyURLHash(current), ExpectedGeneration: binding.Generation, PreviousSourceID: binding.SourceID, DesiredURL: rendered.URL, Binding: next, OldLease: oldLease, ExcludeAutoBind: state.AutoBind.Excluded, Status: "prepared", CreatedAt: now, UpdatedAt: now}
	return p.commitProxyOperationLocked(ctx, response, raw, operation, false)
}

func (p *Plugin) proxyReplaceManagedEndpoint(ctx context.Context, authID string, kind proxy.SourceKind, sourceID string, revision uint64, endpoint proxy.Endpoint, values map[string]string, action string) (proxyBindingResult, error) {
	return p.replaceProxyEndpoint(ctx, authID, kind, sourceID, revision, endpoint, values, action, proxyBindOptions{})
}

func (p *Plugin) replaceProxyEndpoint(ctx context.Context, authID string, kind proxy.SourceKind, sourceID string, revision uint64, endpoint proxy.Endpoint, values map[string]string, action string, opts proxyBindOptions) (proxyBindingResult, error) {
	if err := p.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer p.leaveProxyOperation()
	lock := p.accountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if result, found, err := p.retryProxyAction(ctx, authID, opts, action); found {
		return result, err
	}
	_, response, raw, err := p.resolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := p.stateStore().ProxyAccount(authID)
	binding, managed := bindingForAccount(state)
	if !managed || binding.SourceID != sourceID || (opts.ExpectedGeneration != nil && binding.Generation != *opts.ExpectedGeneration) {
		return proxyBindingResult{AuthID: authID, SourceID: sourceID, Status: "superseded"}, nil
	}
	if !bindingMatchesURL(binding, current) {
		err := proxyError("proxy_binding_conflict", 409, "current proxy URL does not match its binding")
		return failedProxyResult(authID, "conflict", err), err
	}
	if p.stateStore().ProxyPending(authID) != "" {
		err := proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
		return failedProxyResult(authID, "reconcile_required", err), err
	}
	now := time.Now().UTC()
	next := binding
	next.SourceKind = kind
	next.SourceRevision = revision
	next.ResolvedValues = values
	next.UpdatedAt = now
	newHash := endpoint.Fingerprint()
	if newHash != proxyURLHash(current) {
		next.Generation++
	}
	next.EndpointHash = newHash
	operation := proxy.Operation{ID: firstNonEmptyString(opts.OperationID, newProxyID("op")), AuthID: authID, Action: action, ChangeID: opts.ChangeID, ExpectedHash: proxyURLHash(current), ExpectedGeneration: binding.Generation, PreviousSourceID: binding.SourceID, DesiredURL: endpoint.CanonicalURL(), Binding: next, ExcludeAutoBind: state.AutoBind.Excluded, Status: "prepared", CreatedAt: now, UpdatedAt: now}
	return p.commitProxyOperationLocked(ctx, response, raw, operation, false)
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for k, v := range values {
		cloned[k] = v
	}
	return cloned
}
func renderTemplateAvoiding(template proxy.Template, avoidHash string) (proxy.Endpoint, proxy.RenderedProxy, error) {
	compiled, err := proxy.CompileTemplate(template)
	if err != nil {
		return proxy.Endpoint{}, proxy.RenderedProxy{}, err
	}
	rendered, err := compiled.RenderAvoiding(nil, avoidHash)
	return rendered.Endpoint, rendered, err
}

// Migration only fills a fingerprint after the recorded identity and the
// complete configured endpoint agree. It never adopts an unrecorded proxy.
func (p *Plugin) verifiedProxyBinding(state AccountState, current string, dryRun bool) (proxy.Binding, bool, error) {
	binding, managed := bindingForAccount(state)
	if !managed || binding.EndpointHash != "" || current == "" {
		return binding, managed, nil
	}
	if binding.SourceKind != proxy.SourceResin || binding.Lease == nil || binding.Lease.Account == "" {
		return binding, managed, proxyError("proxy_legacy_unknown", 409, "legacy proxy binding requires explicit reconciliation")
	}
	settings := p.ResinConfig()
	if binding.Lease.SnapshotID != "" {
		if snapshot, ok := p.stateStore().ResinSnapshot(binding.Lease.SnapshotID); ok {
			settings = snapshot
		}
	}
	if settings.LocalOnly {
		endpoint, err := proxy.ParseEndpoint(current, proxy.ParseOptions{})
		if err != nil {
			return binding, managed, err
		}
		settings.ProxyToken = endpoint.Password
	}
	expected, err := renderResinProxy(settings, binding.Lease.Account)
	if err != nil || proxyURLHash(expected) != proxyURLHash(current) {
		return binding, managed, proxyError("proxy_legacy_unknown", 409, "legacy proxy does not match its recorded integration; explicit reconciliation is required")
	}
	binding.EndpointHash = proxyURLHash(current)
	binding.SourceRevision = max(1, binding.SourceRevision)
	if !dryRun {
		lease, err := p.captureLegacyLease(binding, current)
		if err != nil {
			return binding, managed, err
		}
		binding.Lease = lease
		if err := p.stateStore().CommitProxy(proxyCommit{Account: &proxyAccountCommit{AuthID: state.AuthID, Binding: binding, Resin: state.Resin, AutoBind: state.AutoBind}}); err != nil {
			return binding, managed, err
		}
	}
	return binding, managed, nil
}

func (p *Plugin) adoptProxy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(opts.SourceIDs) != 1 {
		return failedProxyResult(authID, "failed", proxyError("proxy_source_required", 400, "adoption requires exactly one source")), proxyError("proxy_source_required", 400, "adoption requires exactly one source")
	}
	if err := p.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer p.leaveProxyOperation()
	lock := p.accountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if opts.OperationID != "" {
		if operation, ok := p.stateStore().ProxyOperation(opts.OperationID); ok {
			if operation.Action != "proxy_adopt" {
				err := proxyError("proxy_operation_conflict", 409, "operation_id belongs to another action")
				return failedProxyResult(authID, "conflict", err), err
			}
			return p.resumeProxyOperationLocked(ctx, authID, operation)
		}
	}
	if p.stateStore().ProxyPending(authID) != "" {
		err := proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
		return failedProxyResult(authID, "conflict", err), err
	}
	_, response, raw, err := p.resolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := p.stateStore().ProxyAccount(authID)
	old, managed := bindingForAccount(state)
	entry := p.proxyCatalog().ByID[opts.SourceIDs[0]]
	if entry == nil || entry.Error != nil {
		err := proxyError("proxy_source_unavailable", 409, "adoption source is unavailable")
		return failedProxyResult(authID, "failed", err), err
	}
	if !proxyCandidateEnabled(*entry) || entry.Runtime.ChangeID != "" {
		err := proxyError("proxy_source_busy", 409, "adoption source is disabled or updating")
		return failedProxyResult(authID, "failed", err), err
	}
	var rendered proxy.RenderedProxy
	var lease *proxy.LeaseRef
	var release func()
	if entry.Node != nil && !opts.Replace {
		if proxyURLHash(current) != entry.Node.Endpoint.Fingerprint() {
			err := proxyError("proxy_binding_conflict", 409, "existing proxy does not exactly match the selected node")
			return failedProxyResult(authID, "conflict", err), err
		}
		rendered = proxy.RenderedProxy{URL: current, Endpoint: entry.Node.Endpoint}
	} else if entry.Kind == proxy.SourceResin && !opts.Replace {
		account := resinAccountFromProxyURL(current)
		usage := p.stateStore().ProxyLeaseUsage(account)
		if old.Lease != nil && old.Lease.Account == account {
			usage--
		}
		if usage > 0 {
			err := proxyError("resin_lease_in_use", 409, "Resin lease is referenced by another binding or cleanup operation")
			return failedProxyResult(authID, "conflict", err), err
		}
		expected, renderErr := renderResinProxy(*entry.ResinSettings, account)
		if account == "" || renderErr != nil || proxyURLHash(expected) != proxyURLHash(current) {
			err := proxyError("proxy_binding_conflict", 409, "proxy does not match the selected Resin integration")
			return failedProxyResult(authID, "conflict", err), err
		}
		if !opts.DryRun {
			id, err := p.stateStore().EnsureResinSnapshot(*entry.ResinSettings)
			if err != nil {
				return failedProxyResult(authID, "failed", err), err
			}
			lease = &proxy.LeaseRef{Provider: "resin", Account: account, SnapshotID: id, PlatformID: entry.ResinSettings.PlatformID}
		}
		e, _ := proxy.ParseEndpoint(current, proxy.ParseOptions{})
		rendered = proxy.RenderedProxy{URL: current, Endpoint: e}
	} else {
		if !opts.Replace {
			err := proxyError("proxy_replace_required", 400, "template adoption requires replace=true")
			return failedProxyResult(authID, "failed", err), err
		}
		rendered, lease, release, err = p.generateProxySource(ctx, *entry, authID, raw, "", opts.DryRun)
		if release != nil {
			defer release()
		}
		if err != nil {
			return failedProxyResult(authID, "failed", err), err
		}
	}
	if opts.DryRun {
		return proxyBindingResult{AuthID: authID, SourceID: entry.ID, SourceKind: entry.Kind, Status: "dry_run", DryRun: true, ProxyURL: maskProxyURL(rendered.URL)}, nil
	}
	now := time.Now().UTC()
	generation := old.Generation
	if !managed || old.SourceID != entry.ID || proxyURLHash(current) != proxyURLHash(rendered.URL) {
		generation++
	}
	next := proxy.Binding{AuthID: authID, SourceID: entry.ID, SourceKind: entry.Kind, SourceRevision: entry.Revision, Generation: generation, EndpointHash: proxyURLHash(rendered.URL), ResolvedValues: rendered.Values, Lease: lease, BoundAt: now, UpdatedAt: now}
	operation := proxy.Operation{ID: firstNonEmptyString(opts.OperationID, newProxyID("op")), AuthID: authID, Action: "proxy_adopt", ExpectedHash: proxyURLHash(current), ExpectedGeneration: old.Generation, PreviousSourceID: old.SourceID, DesiredURL: rendered.URL, Binding: next, ExcludeAutoBind: state.AutoBind.Excluded, Status: "prepared", CreatedAt: now, UpdatedAt: now}
	// A drifted old record is not sufficient evidence to delete its remote lease.
	if managed && bindingMatchesURL(old, current) {
		operation.OldLease = old.Lease
	}
	return p.commitProxyOperationLocked(ctx, response, raw, operation, false)
}

func (p *Plugin) retryProxyAction(ctx context.Context, authID string, opts proxyBindOptions, action string) (proxyBindingResult, bool, error) {
	if opts.OperationID == "" {
		return proxyBindingResult{}, false, nil
	}
	operation, ok := p.stateStore().ProxyOperation(opts.OperationID)
	if !ok {
		return proxyBindingResult{}, false, nil
	}
	normalize := func(name string) string { return strings.TrimPrefix(strings.TrimPrefix(name, "proxy_"), "resin_") }
	if operation.AuthID != authID || normalize(operation.Action) != normalize(action) {
		err := proxyError("proxy_operation_conflict", 409, "operation_id belongs to another auth or action")
		return failedProxyResult(authID, "conflict", err), true, err
	}
	if len(opts.SourceIDs) > 0 && action != "proxy_clear" {
		found := false
		for _, id := range opts.SourceIDs {
			if id == operation.Binding.SourceID {
				found = true
			}
		}
		if !found {
			err := proxyError("proxy_operation_conflict", 409, "operation_id belongs to another source")
			return failedProxyResult(authID, "conflict", err), true, err
		}
	}
	if opts.DryRun {
		return proxyBindingResult{AuthID: authID, SourceID: operation.Binding.SourceID, SourceKind: operation.Binding.SourceKind, Status: "dry_run", OperationID: operation.ID, ProxyURL: maskProxyURL(operation.DesiredURL), DryRun: true}, true, nil
	}
	result, err := p.resumeProxyOperationLocked(ctx, authID, operation)
	return result, true, err
}
