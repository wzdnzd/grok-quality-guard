// Durable proxy operations and multi-account source changes.
package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"grok-quality-guard/internal/app/auth"
	"grok-quality-guard/internal/app/redact"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
	proxy "grok-quality-guard/internal/proxy"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func proxyURLHash(value string) string {
	if endpoint, err := proxy.ParseEndpoint(value); err == nil {
		return endpoint.Fingerprint()
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

func bindingMatchesURL(binding proxy.Binding, value string) bool {
	return value != "" && binding.EndpointHash != "" && binding.EndpointHash == proxyURLHash(value)
}

func (s *Service) commitProxyOperationLocked(ctx context.Context, response pluginapi.HostAuthGetResponse, _ map[string]json.RawMessage, operation proxy.Operation, automatic bool) (proxyBindingResult, error) {
	store := s.env.ProxyStore()
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
	s.TouchBindingStates(nil)
	// Read immediately before patching; tokens and unknown fields are taken from
	// the latest auth under the same gate used by probes and quality mutations.
	_, latestResponse, latest, err := s.env.ProxyResolveAuth(ctx, operation.AuthID)
	if err != nil {
		return s.failProxyOperation(operation, err, false)
	}
	current, err := proxyURLFromRaw(latest)
	if err != nil {
		return s.failProxyOperation(operation, err, true)
	}
	if proxyURLHash(current) != operation.ExpectedHash {
		err := proxyError("proxy_binding_conflict", 409, "auth proxy changed before save")
		return s.failProxyOperation(operation, err, true)
	}
	response = latestResponse
	if operation.Clear {
		delete(latest, "proxy_url")
	} else {
		auth.SetRawString(latest, "proxy_url", operation.DesiredURL)
	}
	encoded, err := marshalRawMap(latest)
	if err != nil {
		return s.failProxyOperation(operation, err, false)
	}
	if ctx.Err() != nil {
		return s.failProxyOperation(operation, ctx.Err(), false)
	}
	if operation.ChangeID != "" && store.ProxyChangeStatus(operation.ChangeID) == "canceled" {
		return s.failProxyOperation(operation, proxyError("proxy_change_canceled", 409, "source change was canceled before save"), false)
	}
	if operation.Auto && !store.ProxyAutoBindConfig().Enabled {
		return s.failProxyOperation(operation, proxyError("auto_bind_disabled", 409, "automatic binding was disabled before save"), false)
	}
	if operation.Auto {
		config := store.ProxyAutoBindConfig()
		state := store.ProxyAccount(operation.AuthID)
		a, found := s.env.ProxyFindAccount(operation.AuthID)
		if !found {
			return s.failProxyOperation(operation, proxyError("auth_proxy_unknown", 409, "auth disappeared before automatic save"), false)
		}
		a.Disabled = auth.Disabled(latest)
		if !proxyAccountAllowedAuto(a, state.State, state.DisableKind, config) || !proxyScopeContains(config.SourceScope, operation.Binding.SourceKind) {
			return s.failProxyOperation(operation, proxyError("auto_bind_ineligible", 409, "automatic binding scope or account eligibility changed"), false)
		}
		entry := s.proxyCatalog().ByID[operation.Binding.SourceID]
		age, _ := time.ParseDuration(config.HealthMaxAge)
		if entry == nil || !proxyCandidateEnabled(*entry) || entry.Error != nil || (config.RequireHealthy && !proxySourceHealthFresh(*entry, age)) {
			return s.failProxyOperation(operation, proxyError("auto_bind_source_unavailable", 409, "automatic source availability changed"), false)
		}
		if len(config.SourceIDs) > 0 {
			allowed := false
			for _, id := range config.SourceIDs {
				if id == operation.Binding.SourceID {
					allowed = true
				}
			}
			if !allowed {
				return s.failProxyOperation(operation, proxyError("auto_bind_scope_changed", 409, "automatic source selection changed"), false)
			}
		}
	}
	_, saveErr := s.env.ProxySaveAuth(response.Name, encoded)
	if saveErr != nil {
		// The ABI can fail after the host committed. Inspect reality before
		// deciding whether it is safe to retry a write.
		_, _, actual, readErr := s.env.ProxyResolveAuth(context.WithoutCancel(ctx), operation.AuthID)
		if readErr != nil {
			result.Status = "commit_unknown"
			result.CommitUnknown = true
			result.NeedsReconcile = true
			result.Error = redact.SanitizeError(saveErr)
			result.Code = proxyErrorCode(saveErr)
			return result, saveErr
		}
		actualURL, valueErr := proxyURLFromRaw(actual)
		if valueErr != nil {
			return s.failProxyOperation(operation, valueErr, true)
		}
		if proxyURLHash(actualURL) != proxyURLHash(operation.DesiredURL) {
			if proxyURLHash(actualURL) == operation.ExpectedHash {
				return s.failProxyOperation(operation, saveErr, false)
			}
			return s.failProxyOperation(operation, proxyError("proxy_binding_conflict", 409, "auth proxy changed during save"), true)
		}
	}
	_, _, verified, readErr := s.env.ProxyResolveAuth(context.WithoutCancel(ctx), operation.AuthID)
	if readErr != nil {
		result.Status = "commit_unknown"
		result.CommitUnknown = true
		result.NeedsReconcile = true
		result.Error = redact.SanitizeError(readErr)
		result.Code = proxyErrorCode(readErr)
		return result, readErr
	}
	verifiedURL, valueErr := proxyURLFromRaw(verified)
	if valueErr != nil {
		return s.failProxyOperation(operation, valueErr, true)
	}
	if proxyURLHash(verifiedURL) != proxyURLHash(operation.DesiredURL) {
		return s.failProxyOperation(operation, proxyError("proxy_binding_conflict", 409, "auth proxy changed after save"), true)
	}
	return s.finalizeProxyOperation(operation)
}

func (s *Service) failProxyOperation(operation proxy.Operation, err error, reconcile bool) (proxyBindingResult, error) {
	operation.Status = "failed"
	if reconcile {
		operation.Status = "needs_reconcile"
		operation.NextAttemptAt = time.Now().UTC().Add(time.Minute)
	}
	operation.Error = redact.SanitizeError(err)
	operation.ErrorCode = proxyErrorCode(err)
	operation.UpdatedAt = time.Now().UTC()
	commit := proxyCommit{Operation: &operation}
	if operation.ChangeID != "" {
		item := proxy.ChangeItem{AuthID: operation.AuthID, Generation: operation.ExpectedGeneration, EndpointHash: operation.ExpectedHash, OperationID: operation.ID, Status: operation.Status, Error: operation.Error, ErrorCode: operation.ErrorCode}
		commit.ChangeID = operation.ChangeID
		commit.ChangeItem = &item
	}
	persistErr := s.env.ProxyStore().CommitProxy(commit)
	if persistErr == nil {
		s.TouchBindingStates(nil)
	}
	result := failedProxyResult(operation.AuthID, operation.Status, err)
	result.OperationID = operation.ID
	result.SourceID = operation.Binding.SourceID
	result.SourceKind = operation.Binding.SourceKind
	result.NeedsReconcile = reconcile || persistErr != nil
	return result, errors.Join(err, persistErr)
}

func (s *Service) finalizeProxyOperation(operation proxy.Operation) (proxyBindingResult, error) {
	store := s.env.ProxyStore()
	current := store.ProxyAccount(operation.AuthID)
	auto := current.AutoBind
	auto.Excluded = operation.ExcludeAutoBind
	auto.Attempts = 0
	auto.LastError = ""
	auto.LastErrorCode = ""
	auto.NextAttemptAt = time.Time{}
	now := time.Now().UTC()
	operation.Status = "finalized"
	operation.NextAttemptAt = time.Time{}
	operation.Error = ""
	operation.ErrorCode = ""
	operation.UpdatedAt = now
	commit := proxyCommit{Operation: &operation, Account: &proxyAccountCommit{AuthID: operation.AuthID, Binding: operation.Binding, AutoBind: auto}, DeleteOperations: store.PrunableProxyOperations()}
	status := "succeeded"
	if operation.Clear {
		commit.Account.Binding = proxy.Binding{Generation: current.Proxy.Generation}
		status = "cleared"
	}
	switch operation.Action {
	case "proxy_bind":
		status = "bound"
	case "proxy_rotate":
		status = "rotated"
	}
	if operation.OldLease != nil && (operation.Clear || operation.Binding.Lease == nil || operation.OldLease.SnapshotID != operation.Binding.Lease.SnapshotID || operation.OldLease.Account != operation.Binding.Lease.Account) {
		if job, ok := s.cleanupForLease(operation.AuthID, *operation.OldLease); ok {
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
			detail += " remote_lease=skipped_local_only"
		} else {
			detail += " remote_lease=not_queued"
		}
	}
	commit.Event = &appstate.Event{Time: now, AuthID: operation.AuthID, Action: operation.Action, Result: status, Detail: detail}
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
		result.Error = redact.SanitizeError(err)
		result.Code = proxyErrorCode(err)
		s.updateCatalogProxy(operation.AuthID, operation.DesiredURL)
		return result, err
	}
	s.updateCatalogProxy(operation.AuthID, operation.DesiredURL)
	if result.RemoteLeaseDeleteQueued {
		s.wakeResinLeaseDeleteLoop()
	}
	s.wakeProxyAutoBind()
	return result, nil
}

func (s *Service) resumeProxyOperationLocked(ctx context.Context, authID string, operation proxy.Operation) (proxyBindingResult, error) {
	if operation.AuthID != authID {
		err := proxyError("proxy_operation_conflict", 409, "operation belongs to another auth")
		return failedProxyResult(authID, "conflict", err), err
	}
	if operation.Status == "finalized" {
		status := "succeeded"
		if operation.Clear {
			status = "cleared"
		} else if operation.Action == "proxy_bind" {
			status = "bound"
		} else if strings.Contains(operation.Action, "rotate") {
			status = "rotated"
		}
		return proxyBindingResult{AuthID: authID, OperationID: operation.ID, SourceID: operation.Binding.SourceID, SourceKind: operation.Binding.SourceKind, Status: status, Committed: true, Generation: operation.Binding.Generation, ProxyURL: maskProxyURL(operation.DesiredURL)}, nil
	}
	currentState := s.env.ProxyStore().ProxyAccount(authID)
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
		err := s.env.ProxyStore().CommitProxy(commit)
		if err == nil {
			s.TouchBindingStates(nil)
		}
		return proxyBindingResult{AuthID: authID, SourceID: operation.Binding.SourceID, OperationID: operation.ID, Status: "superseded"}, err
	}
	_, response, raw, err := s.env.ProxyResolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	actual, err := proxyURLFromRaw(raw)
	if err != nil {
		return s.failProxyOperation(operation, err, true)
	}
	if proxyURLHash(actual) == proxyURLHash(operation.DesiredURL) {
		return s.finalizeProxyOperation(operation)
	}
	if proxyURLHash(actual) != operation.ExpectedHash {
		return s.failProxyOperation(operation, proxyError("proxy_binding_conflict", 409, "actual auth proxy differs from both operation endpoints"), true)
	}
	if operation.Auto && !s.env.ProxyStore().ProxyAutoBindConfig().Enabled {
		return s.failProxyOperation(operation, proxyError("auto_bind_disabled", 409, "automatic binding is disabled"), false)
	}
	operation.Status = "prepared"
	return s.commitProxyOperationLocked(ctx, response, raw, operation, operation.Auto)
}

func (s *Service) resumeProxyOperation(ctx context.Context, authID string, operation proxy.Operation) (proxyBindingResult, error) {
	if err := s.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer s.leaveProxyOperation()
	lock := s.env.ProxyAccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	return s.resumeProxyOperationLocked(ctx, authID, operation)
}

func (s *Service) reconcileProxyOperations(ctx context.Context) {
	for _, operation := range s.env.ProxyStore().ProxyPendingOperations(100) {
		if ctx.Err() != nil {
			return
		}
		lock := s.env.ProxyAccountLock(operation.AuthID)
		lock.Lock()
		_, _ = s.resumeProxyOperationLocked(ctx, operation.AuthID, operation)
		lock.Unlock()
	}
}

func (s *Service) clearProxy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		err := proxyError("auth_id_required", 400, "auth_id is required")
		return failedProxyResult(authID, "failed", err), err
	}
	if err := s.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer s.leaveProxyOperation()
	lock := s.env.ProxyAccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if result, found, err := s.retryProxyAction(ctx, authID, opts, "proxy_clear"); found {
		return result, err
	}
	if s.env.ProxyStore().ProxyPending(authID) != "" {
		err := proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
		return failedProxyResult(authID, "reconcile_required", err), err
	}
	_, response, raw, err := s.env.ProxyResolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := s.env.ProxyStore().ProxyAccount(authID)
	binding, managed, verifyErr := s.verifiedProxyBinding(state, current, opts.DryRun)
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
	clearPolicy := firstNonEmptyString(opts.ManualClearPolicy, s.env.ProxyStore().ProxyAutoBindConfig().ManualClearPolicy)
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
			if err := s.env.ProxyStore().CommitProxy(proxyCommit{Account: &proxyAccountCommit{AuthID: authID, Binding: state.Proxy, AutoBind: state.AutoBind}}); err != nil {
				return failedProxyResult(authID, "failed", err), err
			}
			s.TouchBindingStates(nil)
		}
		return proxyBindingResult{AuthID: authID, Status: "skipped"}, nil
	}
	lease := binding.Lease
	now := time.Now().UTC()
	operation := proxy.Operation{ID: firstNonEmptyString(opts.OperationID, newProxyID("op")), AuthID: authID, Action: firstNonEmptyString(opts.Action, "proxy_clear"), ChangeID: opts.ChangeID, ExpectedHash: proxyURLHash(current), ExpectedGeneration: binding.Generation, PreviousSourceID: binding.SourceID, Clear: true, Binding: proxy.Binding{Generation: binding.Generation}, OldLease: lease, ExcludeAutoBind: exclude, Status: "prepared", CreatedAt: now, UpdatedAt: now}
	return s.commitProxyOperationLocked(ctx, response, raw, operation, false)
}

func (s *Service) clearProxyWithPolicy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	return s.clearProxy(ctx, authID, opts)
}

func (s *Service) rotateProxy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer s.leaveProxyOperation()
	lock := s.env.ProxyAccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if result, found, err := s.retryProxyAction(ctx, authID, opts, "proxy_rotate"); found {
		return result, err
	}
	if s.env.ProxyStore().ProxyPending(authID) != "" {
		err := proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
		return failedProxyResult(authID, "reconcile_required", err), err
	}
	_, response, raw, err := s.env.ProxyResolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := s.env.ProxyStore().ProxyAccount(authID)
	binding, managed, verifyErr := s.verifiedProxyBinding(state, current, opts.DryRun)
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
		candidate = s.proxyCatalog().ByID[binding.SourceID]
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
		candidate, reasons = s.selectProxySource(opts, proxyURLHash(current))
		if candidate == nil {
			err := proxyError("rotation_unavailable", 409, firstNonEmptyString(strings.Join(reasons, "; "), "no different available proxy node"))
			return failedProxyResult(authID, "rotation_unavailable", err), err
		}
		if !opts.DryRun {
			defer s.releaseProxySource(candidate.ID, authID)
		}
	}
	rendered, lease, release, err := s.generateProxySource(ctx, *candidate, authID, raw, proxyURLHash(current), opts.DryRun)
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
	now := time.Now().UTC()
	next := proxy.Binding{AuthID: authID, SourceID: candidate.ID, SourceKind: candidate.Kind, SourceRevision: candidate.Revision, Generation: binding.Generation + 1, EndpointHash: proxyURLHash(rendered.URL), ResolvedValues: rendered.Values, Lease: lease, BoundAt: binding.BoundAt, UpdatedAt: now}
	action := "proxy_rotate"
	operation := proxy.Operation{ID: firstNonEmptyString(opts.OperationID, newProxyID("op")), AuthID: authID, Action: action, ExpectedHash: proxyURLHash(current), ExpectedGeneration: binding.Generation, PreviousSourceID: binding.SourceID, DesiredURL: rendered.URL, Binding: next, OldLease: oldLease, ExcludeAutoBind: state.AutoBind.Excluded, Status: "prepared", CreatedAt: now, UpdatedAt: now}
	return s.commitProxyOperationLocked(ctx, response, raw, operation, false)
}

func (s *Service) proxyReplaceManagedEndpoint(ctx context.Context, authID string, kind proxy.SourceKind, sourceID string, revision uint64, endpoint proxy.Endpoint, values map[string]string, action string) (proxyBindingResult, error) {
	return s.replaceProxyEndpoint(ctx, authID, kind, sourceID, revision, endpoint, values, action, proxyBindOptions{})
}

func (s *Service) replaceProxyEndpoint(ctx context.Context, authID string, kind proxy.SourceKind, sourceID string, revision uint64, endpoint proxy.Endpoint, values map[string]string, action string, opts proxyBindOptions) (proxyBindingResult, error) {
	if err := s.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer s.leaveProxyOperation()
	lock := s.env.ProxyAccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if result, found, err := s.retryProxyAction(ctx, authID, opts, action); found {
		return result, err
	}
	_, response, raw, err := s.env.ProxyResolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := s.env.ProxyStore().ProxyAccount(authID)
	binding, managed := bindingForAccount(state)
	if !managed || binding.SourceID != sourceID || (opts.ExpectedGeneration != nil && binding.Generation != *opts.ExpectedGeneration) {
		return proxyBindingResult{AuthID: authID, SourceID: sourceID, Status: "superseded"}, nil
	}
	if !bindingMatchesURL(binding, current) {
		err := proxyError("proxy_binding_conflict", 409, "current proxy URL does not match its binding")
		return failedProxyResult(authID, "conflict", err), err
	}
	if s.env.ProxyStore().ProxyPending(authID) != "" {
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
	return s.commitProxyOperationLocked(ctx, response, raw, operation, false)
}

func (s *Service) verifiedProxyBinding(state AccountState, _ string, _ bool) (proxy.Binding, bool, error) {
	binding, managed := bindingForAccount(state)
	return binding, managed, nil
}
func (s *Service) adoptProxy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(opts.SourceIDs) != 1 {
		return failedProxyResult(authID, "failed", proxyError("proxy_source_required", 400, "adoption requires exactly one source")), proxyError("proxy_source_required", 400, "adoption requires exactly one source")
	}
	if err := s.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer s.leaveProxyOperation()
	lock := s.env.ProxyAccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if opts.OperationID != "" {
		if operation, ok := s.env.ProxyStore().ProxyOperation(opts.OperationID); ok {
			if operation.Action != "proxy_adopt" {
				err := proxyError("proxy_operation_conflict", 409, "operation_id belongs to another action")
				return failedProxyResult(authID, "conflict", err), err
			}
			return s.resumeProxyOperationLocked(ctx, authID, operation)
		}
	}
	if s.env.ProxyStore().ProxyPending(authID) != "" {
		err := proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
		return failedProxyResult(authID, "conflict", err), err
	}
	_, response, raw, err := s.env.ProxyResolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := s.env.ProxyStore().ProxyAccount(authID)
	old, managed := bindingForAccount(state)
	entry := s.proxyCatalog().ByID[opts.SourceIDs[0]]
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
		if entry.Template == nil || entry.ResinConfig == nil {
			err := proxyError("proxy_source_invalid", 409, "Resin source configuration is missing")
			return failedProxyResult(authID, "conflict", err), err
		}
		parsedURL, errParse := url.Parse(strings.TrimSpace(current))
		if errParse != nil || parsedURL.User == nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
			err := proxyError("proxy_binding_conflict", 409, "existing proxy URL is invalid")
			return failedProxyResult(authID, "conflict", err), err
		}
		parsedEndpoint, errEndpoint := proxy.ParseEndpoint(current)
		if errEndpoint != nil || !parsedEndpoint.HasPassword {
			err := proxyError("proxy_binding_conflict", 409, "existing proxy URL is invalid")
			return failedProxyResult(authID, "conflict", err), err
		}
		config := *entry.ResinConfig
		prefix := strings.TrimSpace(config.Platform)
		username := parsedURL.User.Username()
		account := resinAccountFromProxyURL(current)
		if prefix != "" {
			if !strings.HasPrefix(username, prefix+".") {
				err := proxyError("proxy_binding_conflict", 409, "proxy does not match the selected Resin platform")
				return failedProxyResult(authID, "conflict", err), err
			}
			account = strings.TrimPrefix(username, prefix+".")
		}
		if !config.LocalOnly && (parsedEndpoint.Protocol != entry.Template.Protocol ||
			!strings.EqualFold(parsedEndpoint.Host, entry.Template.Host) || parsedEndpoint.Port != entry.Template.Port ||
			parsedEndpoint.Password != entry.Template.Password) {
			err := proxyError("proxy_binding_conflict", 409, "proxy does not match the selected Resin connection")
			return failedProxyResult(authID, "conflict", err), err
		}
		usage := s.env.ProxyStore().ProxyLeaseUsage(account)
		if old.Lease != nil && old.Lease.Account == account {
			usage--
		}
		if usage > 0 {
			err := proxyError("resin_lease_in_use", 409, "Resin lease is referenced by another binding or cleanup operation")
			return failedProxyResult(authID, "conflict", err), err
		}
		if account == "" {
			err := proxyError("proxy_binding_conflict", 409, "proxy does not match the selected Resin integration")
			return failedProxyResult(authID, "conflict", err), err
		}
		if !opts.DryRun {
			if config.LocalOnly {
				lease = &proxy.LeaseRef{Provider: "resin-local", Account: account}
			} else {
				snapshot := proxy.ResinSnapshot{AdminURL: config.AdminURL, PlatformID: config.PlatformID, AdminToken: config.AdminToken}
				id, err := s.env.ProxyStore().EnsureResinSnapshot(snapshot)
				if err != nil {
					return failedProxyResult(authID, "failed", err), err
				}
				lease = &proxy.LeaseRef{Provider: "resin", Account: account, SnapshotID: id, PlatformID: config.PlatformID}
			}
		}
		rendered = proxy.RenderedProxy{URL: current, Endpoint: parsedEndpoint}
	} else {
		if !opts.Replace {
			err := proxyError("proxy_replace_required", 400, "template adoption requires replace=true")
			return failedProxyResult(authID, "failed", err), err
		}
		rendered, lease, release, err = s.generateProxySource(ctx, *entry, authID, raw, "", opts.DryRun)
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
	return s.commitProxyOperationLocked(ctx, response, raw, operation, false)
}

func (s *Service) retryProxyAction(ctx context.Context, authID string, opts proxyBindOptions, action string) (proxyBindingResult, bool, error) {
	if opts.OperationID == "" {
		return proxyBindingResult{}, false, nil
	}
	operation, ok := s.env.ProxyStore().ProxyOperation(opts.OperationID)
	if !ok {
		return proxyBindingResult{}, false, nil
	}
	if operation.AuthID != authID || operation.Action != action {
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
	result, err := s.resumeProxyOperationLocked(ctx, authID, operation)
	return result, true, err
}

func (s *Service) runProxyChange(ctx context.Context, id string) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mgr.changeMu.Lock()
	if s.mgr.runningChanges == nil {
		s.mgr.runningChanges = make(map[string]struct{})
	}
	if _, running := s.mgr.runningChanges[id]; running {
		s.mgr.changeMu.Unlock()
		return nil, proxyError("proxy_change_busy", 409, "proxy change is already running")
	}
	s.mgr.runningChanges[id] = struct{}{}
	s.mgr.changeMu.Unlock()
	defer func() {
		s.mgr.changeMu.Lock()
		delete(s.mgr.runningChanges, id)
		s.mgr.changeMu.Unlock()
	}()
	change, ok := s.env.ProxyStore().ProxyChange(id)
	if !ok {
		return nil, proxyError("proxy_change_not_found", 404, "proxy change not found")
	}
	if change.Status == "succeeded" {
		return proxyChangeResult(change, nil), nil
	}
	var node proxy.Node
	var compiled *proxy.CompiledTemplate
	if change.Kind == "node_sync" {
		var found bool
		node, found = s.env.ProxyStore().ProxyNodeVersion(change.SourceID, change.TargetRevision)
		if !found {
			return nil, proxyError("proxy_source_version_missing", 409, "node target version is missing")
		}
	}
	if change.Kind != "node_sync" && change.Kind != "source_delete" {
		t, found := s.env.ProxyStore().ProxyTemplateVersion(change.SourceID, change.TargetRevision)
		if !found {
			return nil, proxyError("proxy_source_version_missing", 409, "template target version is missing")
		}
		var err error
		compiled, err = proxy.CompileTemplate(t)
		if err != nil {
			return nil, err
		}
	}
	if err := s.env.ProxyStore().Update(func(state *State) error {
		runtime := state.Proxy.SourceRuntime[change.SourceID]
		if runtime.ChangeID != "" && runtime.ChangeID != id {
			return proxyError("proxy_source_busy", 409, "another source change is in progress")
		}
		if runtime.ChangeID == "" && change.Kind != "source_delete" {
			revision := uint64(0)
			for _, n := range state.Proxy.Nodes {
				if n.ID == change.SourceID {
					revision = n.Revision
				}
			}
			for _, t := range state.Proxy.Templates {
				if t.ID == change.SourceID {
					revision = t.Revision
				}
			}
			if revision != change.TargetRevision {
				return proxyError("proxy_source_version_conflict", 409, "source changed after cancellation; apply its current version instead")
			}
		}
		if state.Proxy.SourceRuntime == nil {
			state.Proxy.SourceRuntime = make(map[string]proxy.SourceRuntime)
		}
		runtime.ChangeID = id
		runtime.Lifecycle = "syncing"
		if change.Kind == "source_delete" {
			runtime.Lifecycle = "deleting"
		}
		state.Proxy.SourceRuntime[change.SourceID] = runtime
		for i := range state.Proxy.Changes {
			if state.Proxy.Changes[i].ID == id {
				state.Proxy.Changes[i].Status = "running"
				return nil
			}
		}
		return proxyError("proxy_change_not_found", 404, "proxy change not found")
	}); err != nil {
		return nil, err
	}
	indices := make([]int, 0, len(change.Items))
	for i, item := range change.Items {
		if item.Status != "succeeded" && item.Status != "superseded" && item.Status != "skipped" {
			indices = append(indices, i)
		}
	}
	results := make([]proxyBindingResult, len(indices))
	for i, index := range indices {
		results[i] = proxyBindingResult{AuthID: change.Items[index].AuthID, Status: "pending"}
	}
	errRun := tasks.RunBoundedQueue(ctx, proxyIndices(len(indices)), min(256, len(indices)), s.proxyWorkerCount(), func(jobCtx context.Context, position int) {
		if s.env.ProxyStore().ProxyChangeStatus(id) == "canceled" {
			results[position].Status = "canceled"
			tasks.ReportItem(jobCtx, results[position])
			return
		}
		item := change.Items[indices[position]]
		opts := proxyBindOptions{ChangeID: id, OperationID: item.OperationID, ExpectedSourceID: change.SourceID, ExpectedGeneration: &item.Generation}
		var result proxyBindingResult
		var err error
		if operation, found := s.env.ProxyStore().ProxyOperation(item.OperationID); item.OperationID != "" && found {
			if operation.ChangeID != id {
				err = proxyError("proxy_operation_conflict", 409, "operation belongs to another change")
			} else {
				result, err = s.resumeProxyOperation(jobCtx, item.AuthID, operation)
			}
		} else if change.Kind == "source_delete" {
			state := s.env.ProxyStore().ProxyAccount(item.AuthID)
			excluded := state.AutoBind.Excluded
			opts.ExcludeAutoBind = &excluded
			result, err = s.clearProxy(jobCtx, item.AuthID, opts)
		} else {
			endpoint := node.Endpoint
			var values map[string]string
			if compiled != nil {
				rendered, renderErr := compiled.RenderWithReader(nil, nil)
				if renderErr != nil {
					err = renderErr
				} else {
					endpoint = rendered.Endpoint
					values = rendered.Values
				}
			}
			if err == nil {
				kind := proxy.SourceNode
				if compiled != nil {
					kind = proxy.SourceTemplate
				}
				result, err = s.replaceProxyEndpoint(jobCtx, item.AuthID, kind, change.SourceID, change.TargetRevision, endpoint, values, "proxy_source_apply", opts)
			}
		}
		if err != nil {
			if result.Error == "" {
				result.Error = redact.SanitizeError(err)
			}
			if result.Code == "" {
				result.Code = proxyErrorCode(err)
			}
		}
		if result.Status == "" {
			result.Status = "failed"
		}
		result.AuthID = item.AuthID
		if result.OperationID != "" {
			item.OperationID = result.OperationID
		}
		item.Status = result.Status
		item.Error = result.Error
		item.ErrorCode = result.Code
		switch result.Status {
		case "succeeded", "bound", "rotated", "cleared", "unchanged":
			item.Status = "succeeded"
		}
		if !result.Committed || result.NeedsReconcile {
			if err := s.env.ProxyStore().CommitProxy(proxyCommit{ChangeID: id, ChangeItem: &item}); err != nil {
				result.NeedsReconcile = true
				result.Error = redact.SanitizeError(err)
			}
		}
		results[position] = result
		tasks.ReportItem(jobCtx, result)
	})
	var final proxy.SourceChange
	errFinish := s.env.ProxyStore().Update(func(state *State) error {
		for i := range state.Proxy.Changes {
			current := &state.Proxy.Changes[i]
			if current.ID != id {
				continue
			}
			counts := proxy.ChangeCounts{Total: len(current.Items)}
			for _, item := range current.Items {
				switch item.Status {
				case "succeeded", "superseded", "skipped":
					counts.Succeeded++
				case "pending", "":
					counts.Pending++
				default:
					counts.Failed++
				}
			}
			current.Counts = counts
			if current.Status == "canceled" {
			} else if errRun != nil {
				current.Status = "pending"
				current.Error = redact.SanitizeError(errRun)
			} else if counts.Failed > 0 || counts.Pending > 0 {
				current.Status = "partial"
			} else {
				current.Status = "succeeded"
				current.CompletedAt = time.Now().UTC()
			}
			runtime := state.Proxy.SourceRuntime[change.SourceID]
			if current.Status == "succeeded" {
				if change.Kind == "source_delete" {
					if len(proxyBindingAuthIDsFromState(*state, change.SourceID, proxy.SourceKind(s.proxyKindFromState(*state, change.SourceID)))) > 0 {
						current.Status = "partial"
						current.Error = "bindings changed during deletion; clear or reconcile the remaining bindings"
						current.ErrorCode = "proxy_source_cleanup_failed"
					} else {
						for j, n := range state.Proxy.Nodes {
							if n.ID == change.SourceID {
								state.Proxy.Nodes = append(state.Proxy.Nodes[:j], state.Proxy.Nodes[j+1:]...)
								break
							}
						}
						for j, t := range state.Proxy.Templates {
							if t.ID == change.SourceID {
								state.Proxy.Templates = append(state.Proxy.Templates[:j], state.Proxy.Templates[j+1:]...)
								break
							}
						}
						delete(state.Proxy.ResinSources, change.SourceID)
						delete(state.Proxy.SourceRuntime, change.SourceID)
					}
				} else if runtime.ChangeID == id {
					runtime.Lifecycle = "active"
					runtime.ChangeID = ""
					state.Proxy.SourceRuntime[change.SourceID] = runtime
				}
			}
			// Successful change items never retry; their generated secrets need not
			// remain in the operation journal. Failed prepared targets are retained.
			for opID, operation := range state.Proxy.Operations {
				if operation.ChangeID == id && operation.Status == "finalized" {
					delete(state.Proxy.Operations, opID)
				}
			}
			final = *current
			return nil
		}
		return proxyError("proxy_change_not_found", 404, "proxy change disappeared")
	})
	if errFinish == nil {
		s.wakeProxyAutoBind()
	}
	return proxyChangeResult(final, results), errors.Join(errRun, errFinish)
}

func (s *Service) proxyKindFromState(state State, id string) string {
	for _, n := range state.Proxy.Nodes {
		if n.ID == id {
			return "node"
		}
	}
	for _, t := range state.Proxy.Templates {
		if t.ID == id {
			if t.Kind == proxy.TemplateResin {
				return "resin"
			}
			return "template"
		}
	}
	return ""
}
func proxyChangeResult(change proxy.SourceChange, results []proxyBindingResult) map[string]any {
	if len(results) > 1000 {
		results = results[:1000]
	}
	return map[string]any{"change_id": change.ID, "source_id": change.SourceID, "status": change.Status, "target_revision": change.TargetRevision, "counts": change.Counts, "count": change.Counts.Total, "bound_count": change.Counts.Total, "items": results}
}
func (s *Service) proxyWorkerCount() int {
	config := s.env.ProxyStore().ProxyAutoBindConfig()
	return max(1, min(8, config.Concurrency))
}

func (s *Service) proxyApplyNodeToAccounts(ctx context.Context, node proxy.Node, ids []string) []proxyBindingResult {
	results := make([]proxyBindingResult, len(ids))
	_ = tasks.RunBoundedQueue(ctx, proxyIndices(len(ids)), min(len(ids), 256), s.proxyWorkerCount(), func(jobCtx context.Context, i int) {
		result, err := s.proxyReplaceManagedEndpoint(jobCtx, ids[i], proxy.SourceNode, node.ID, node.Revision, node.Endpoint, nil, "proxy_node_sync")
		if err != nil && result.Error == "" {
			result.Error = redact.SanitizeError(err)
		}
		if err != nil && result.Code == "" {
			result.Code = proxyErrorCode(err)
		}
		results[i] = result
		tasks.ReportItem(jobCtx, result)
	})
	return results
}
func (s *Service) proxyApplyTemplateToAccounts(ctx context.Context, template proxy.Template, ids []string) []proxyBindingResult {
	compiled, err := proxy.CompileTemplate(template)
	if err != nil {
		return []proxyBindingResult{{Status: "failed", Error: redact.SanitizeError(err), Code: proxyErrorCode(err)}}
	}
	results := make([]proxyBindingResult, len(ids))
	_ = tasks.RunBoundedQueue(ctx, proxyIndices(len(ids)), min(len(ids), 256), s.proxyWorkerCount(), func(jobCtx context.Context, i int) {
		rendered, err := compiled.RenderWithReader(nil, nil)
		if err != nil {
			results[i] = failedProxyResult(ids[i], "failed", err)
			tasks.ReportItem(jobCtx, results[i])
			return
		}
		result, err := s.proxyReplaceManagedEndpoint(jobCtx, ids[i], proxy.SourceTemplate, template.ID, template.Revision, rendered.Endpoint, rendered.Values, "proxy_template_apply")
		if err != nil && result.Error == "" {
			result.Error = redact.SanitizeError(err)
		}
		if err != nil && result.Code == "" {
			result.Code = proxyErrorCode(err)
		}
		results[i] = result
		tasks.ReportItem(jobCtx, result)
	})
	return results
}
func proxyIndices(length int) []int {
	indices := make([]int, length)
	for i := range indices {
		indices[i] = i
	}
	return indices
}

func (s *Service) createTemplateApplyChange(id string) (string, error) {
	s.mgr.changeMu.Lock()
	defer s.mgr.changeMu.Unlock()
	entry := s.proxyCatalog().ByID[id]
	if entry == nil || entry.Template == nil || entry.Template.Kind == proxy.TemplateResin {
		return "", proxyError("proxy_source_not_found", 404, "template not found")
	}
	var changeID string
	err := s.env.ProxyStore().Update(func(state *State) error {
		if runtime := state.Proxy.SourceRuntime[id]; runtime.ChangeID != "" {
			return proxyError("proxy_source_busy", 409, "previous template application is unfinished")
		}
		ids := proxyBindingAuthIDsFromState(*state, id, proxy.SourceTemplate)
		changeID = beginProxyChange(state, id, "template_apply", entry.Revision, ids, "syncing")
		return nil
	})
	return changeID, err
}
func (s *Service) cancelProxyChange(id string) error {
	return s.env.ProxyStore().Update(func(state *State) error {
		for i := range state.Proxy.Changes {
			change := &state.Proxy.Changes[i]
			if change.ID == id {
				if change.Status == "succeeded" {
					return proxyError("proxy_change_completed", 409, "completed changes cannot be canceled")
				}
				change.Status = "canceled"
				if !hasPendingSourceOperation(*state, change.SourceID) {
					runtime := state.Proxy.SourceRuntime[change.SourceID]
					if runtime.ChangeID == id {
						runtime.Lifecycle = "active"
						runtime.ChangeID = ""
						state.Proxy.SourceRuntime[change.SourceID] = runtime
					}
				}
				return nil
			}
		}
		return proxyError("proxy_change_not_found", 404, fmt.Sprintf("proxy change %q not found", id))
	})
}
