package management

import (
	"context"
	"grok-quality-guard/internal/app/action"
	"grok-quality-guard/internal/app/tasks"
	"net/http"
	"strings"
	"time"
)

func (s *Service) actionProbe(ctx context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(asString(params["auth_id"]))
	if authID == "" {
		return nil, http.StatusBadRequest, newAPIActionError("auth_id_required", "api.error.auth_id_required", http.StatusBadRequest, nil)
	}
	if _, okAccount := s.findAccount(authID); !okAccount {
		_ = s.RefreshCatalog(ctx)
		if _, okAccount = s.findAccount(authID); !okAccount {
			return nil, http.StatusNotFound, newAPIActionError("auth_not_found", "api.error.auth_not_found", http.StatusNotFound, nil)
		}
	}
	force := action.Bool(params, "force")
	task := s.startTask("accounts.probe", []string{authID}, "task.progress.probe_account", func(taskCtx context.Context) (any, error) {
		// Detached tasks must not reuse the short-lived management callback
		if force {
			stateAccount, _ := s.stateAccount(authID)
			if errRestore := s.restoreBeforeManualProbe(taskCtx, authID, stateAccount.State); errRestore != nil {
				return nil, errRestore
			}
		}
		if errProbe := s.ProbeAccount(taskCtx, authID, force, ""); errProbe != nil {
			return nil, errProbe
		}
		account, okAccount := s.findAccount(authID)
		if !okAccount {
			return nil, newAPIActionError("auth_not_found", "api.error.auth_not_found", http.StatusNotFound, nil)
		}
		return s.accountViewForAccount(taskCtx, account)
	})
	return task, http.StatusAccepted, nil
}

func (s *Service) restoreBeforeManualProbe(ctx context.Context, authID, stateBefore string) error {
	if !requiresRestoreBeforeProbe(stateBefore) {
		return nil
	}
	return s.ManualRestore(ctx, authID)
}

func requiresRestoreBeforeProbe(stateBefore string) bool {
	switch stateBefore {
	case StateCooling, StateQuotaExhausted, StateCredentialInvalid, StateDisabledByGuard:
		return true
	default:
		return false
	}
}

func (s *Service) actionProbeSelected(ctx context.Context, params map[string]any) (any, int, error) {
	authIDs := action.AuthIDs(params)
	if len(authIDs) == 0 {
		return nil, http.StatusBadRequest, newAPIActionError("auth_ids_required", "api.error.auth_ids_required", http.StatusBadRequest, nil)
	}
	candidates, errCandidates := s.selectedAccountCandidates(ctx, authIDs)
	if errCandidates != nil {
		return nil, http.StatusBadRequest, errCandidates
	}
	if s.hasActiveTask("accounts.probe_selected", "") {
		return nil, http.StatusConflict, newAPIActionError("task_active", "api.error.task_active", http.StatusConflict, nil)
	}
	task := s.startTask("accounts.probe_selected", authIDs, "task.progress.probe_selected", func(taskCtx context.Context) (any, error) {
		result := probeBatchResult{
			Items: make([]probeBatchItem, len(candidates)),
			Count: len(candidates),
		}
		indices := make([]int, len(candidates))
		for index := range candidates {
			indices[index] = index
		}
		errRun := runBoundedQueue(taskCtx, indices, s.probeQueueSize(), s.probeConcurrency(), func(jobCtx context.Context, index int) {
			candidate := candidates[index]
			stateBefore := s.accountCandidateState(candidate)
			entry := probeBatchItem{AuthID: candidate.authID, StateBefore: stateBefore}
			if stateBefore == StateExternallyDisabled {
				entry.Status = "skipped"
				result.Items[index] = entry
				tasks.ReportItem(jobCtx, entry)
				return
			}
			if requiresRestoreBeforeProbe(stateBefore) {
				if errRestore := s.restoreBeforeManualProbe(jobCtx, candidate.authID, stateBefore); errRestore != nil {
					entry.Status = "failed"
					entry.Error = sanitizeError(errRestore)
					entry.ErrorCode = tasks.ErrorCode(errRestore)
					result.Items[index] = entry
					tasks.ReportItem(jobCtx, entry)
					return
				}
				entry.Restored = true
			}
			if errProbe := s.ProbeAccount(jobCtx, candidate.authID, true, ""); errProbe != nil {
				entry.Status = "failed"
				entry.Error = sanitizeError(errProbe)
				entry.ErrorCode = tasks.ErrorCode(errProbe)
				result.Items[index] = entry
				tasks.ReportItem(jobCtx, entry)
				return
			}
			if stateAfter, okState := s.stateAccount(candidate.authID); okState {
				entry.StateAfter = stateAfter.State
			}
			entry.Status = "probed"
			result.Items[index] = entry
			tasks.ReportItem(jobCtx, entry)
		})
		if errRun != nil {
			return nil, errRun
		}
		for _, item := range result.Items {
			if item.Restored {
				result.Restored++
			}
			switch item.Status {
			case "probed":
				result.Probed++
			case "skipped":
				result.Skipped++
			case "failed":
				result.Failed++
			}
		}
		return result, nil
	})
	return task, http.StatusAccepted, nil
}
func (s *Service) actionCooldown(ctx context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(asString(params["auth_id"]))
	if authID == "" {
		return nil, http.StatusBadRequest, newAPIActionError("auth_id_required", "api.error.auth_id_required", http.StatusBadRequest, nil)
	}
	duration := time.Duration(0)
	if raw := strings.TrimSpace(asString(params["duration"])); raw != "" {
		parsed, errParse := time.ParseDuration(raw)
		if errParse != nil || parsed <= 0 {
			return nil, http.StatusBadRequest, newAPIActionError("invalid_duration", "api.error.invalid_duration", http.StatusBadRequest, nil)
		}
		duration = parsed
	}
	if errCooldown := s.ManualCooldown(ctx, authID, duration); errCooldown != nil {
		return nil, http.StatusConflict, errCooldown
	}
	return s.actionAccountGet(ctx, map[string]any{"auth_id": authID})
}

func (s *Service) actionDisable(ctx context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(asString(params["auth_id"]))
	if authID == "" {
		return nil, http.StatusBadRequest, newAPIActionError("auth_id_required", "api.error.auth_id_required", http.StatusBadRequest, nil)
	}
	if errDisable := s.ManualDisable(ctx, authID, asString(params["reason"])); errDisable != nil {
		return nil, http.StatusConflict, errDisable
	}
	return s.actionAccountGet(ctx, map[string]any{"auth_id": authID})
}

func (s *Service) actionRestore(ctx context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(asString(params["auth_id"]))
	if authID == "" {
		return nil, http.StatusBadRequest, newAPIActionError("auth_id_required", "api.error.auth_id_required", http.StatusBadRequest, nil)
	}
	if errRestore := s.ManualRestore(ctx, authID); errRestore != nil {
		return nil, http.StatusConflict, errRestore
	}
	return s.actionAccountGet(ctx, map[string]any{"auth_id": authID})
}

func (s *Service) actionRestorePreview(ctx context.Context, params map[string]any) (any, int, error) {
	authIDs := action.AuthIDs(params)
	if len(authIDs) == 0 {
		return nil, http.StatusBadRequest, newAPIActionError("auth_ids_required", "api.error.auth_ids_required", http.StatusBadRequest, nil)
	}
	candidates, errCandidates := s.selectedAccountCandidates(ctx, authIDs)
	if errCandidates != nil {
		return nil, http.StatusBadRequest, errCandidates
	}
	result := restorePreviewResult{Count: len(candidates)}
	for _, candidate := range candidates {
		switch s.accountCandidateState(candidate) {
		case StateHealthy:
			result.Healthy++
		case StateExternallyDisabled:
			result.Restorable++
			result.ExternalDisabled++
		default:
			result.Restorable++
		}
	}
	return result, http.StatusOK, nil
}

func (s *Service) actionRestoreSelected(ctx context.Context, params map[string]any) (any, int, error) {
	authIDs := action.AuthIDs(params)
	if len(authIDs) == 0 {
		return nil, http.StatusBadRequest, newAPIActionError("auth_ids_required", "api.error.auth_ids_required", http.StatusBadRequest, nil)
	}
	includeExternal := action.Bool(params, "include_external")
	candidates, errCandidates := s.selectedAccountCandidates(ctx, authIDs)
	if errCandidates != nil {
		return nil, http.StatusBadRequest, errCandidates
	}
	if s.hasActiveTask("accounts.restore_selected", "") {
		return nil, http.StatusConflict, newAPIActionError("task_active", "api.error.task_active", http.StatusConflict, nil)
	}
	task := s.startTask("accounts.restore_selected", authIDs, "task.progress.restore_selected", func(taskCtx context.Context) (any, error) {
		result := restoreBatchResult{
			Items: make([]restoreBatchItem, len(candidates)),
			Count: len(candidates),
		}
		indices := make([]int, len(candidates))
		for index := range candidates {
			indices[index] = index
		}
		errRun := runBoundedQueue(taskCtx, indices, s.probeQueueSize(), s.probeConcurrency(), func(jobCtx context.Context, index int) {
			candidate := candidates[index]
			stateBefore := s.accountCandidateState(candidate)
			entry := restoreBatchItem{AuthID: candidate.authID, StateBefore: stateBefore}
			if stateBefore == StateHealthy {
				entry.Status = "skipped"
				result.Items[index] = entry
				tasks.ReportItem(jobCtx, entry)
				return
			}
			if errRestore := s.ManualRestoreExternal(jobCtx, candidate.authID, includeExternal); errRestore != nil {
				entry.Status = "failed"
				entry.Error = sanitizeError(errRestore)
				entry.ErrorCode = tasks.ErrorCode(errRestore)
				result.Items[index] = entry
				tasks.ReportItem(jobCtx, entry)
				return
			}
			if stateBefore == StateExternallyDisabled {
				if includeExternal {
					entry.Status = "external_restored"
				} else {
					entry.Status = "external_left_disabled"
				}
			} else {
				entry.Status = "restored"
			}
			result.Items[index] = entry
			tasks.ReportItem(jobCtx, entry)
		})
		if errRun != nil {
			return nil, errRun
		}
		for _, item := range result.Items {
			switch item.Status {
			case "restored":
				result.Restored++
			case "external_restored":
				result.ExternalRestored++
			case "external_left_disabled":
				result.ExternalLeftDisabled++
			case "skipped":
				result.Skipped++
			case "failed":
				result.Failed++
			}
		}
		return result, nil
	})
	return task, http.StatusAccepted, nil
}

func (s *Service) actionDisableSelected(ctx context.Context, params map[string]any) (any, int, error) {
	authIDs := action.AuthIDs(params)
	if len(authIDs) == 0 {
		return nil, http.StatusBadRequest, newAPIActionError("auth_ids_required", "api.error.auth_ids_required", http.StatusBadRequest, nil)
	}
	modifyAuth := action.Bool(params, "modify_auth")
	candidates, errCandidates := s.selectedAccountCandidates(ctx, authIDs)
	if errCandidates != nil {
		return nil, http.StatusBadRequest, errCandidates
	}
	if s.hasActiveTask("accounts.disable_selected", "") {
		return nil, http.StatusConflict, newAPIActionError("task_active", "api.error.task_active", http.StatusConflict, nil)
	}
	task := s.startTask("accounts.disable_selected", authIDs, "task.progress.disable_selected", func(taskCtx context.Context) (any, error) {
		result := disableBatchResult{
			Items:        make([]disableBatchItem, len(candidates)),
			Count:        len(candidates),
			ModifiedAuth: modifyAuth,
		}
		indices := make([]int, len(candidates))
		for index := range candidates {
			indices[index] = index
		}
		errRun := runBoundedQueue(taskCtx, indices, s.probeQueueSize(), s.probeConcurrency(), func(jobCtx context.Context, index int) {
			candidate := candidates[index]
			stateBefore := s.accountCandidateState(candidate)
			entry := disableBatchItem{AuthID: candidate.authID, StateBefore: stateBefore}
			if stateBefore == StateExternallyDisabled || (stateBefore == StateDisabledByGuard && (!modifyAuth || candidate.account.Disabled)) {
				entry.Status = "skipped"
				result.Items[index] = entry
				tasks.ReportItem(jobCtx, entry)
				return
			}
			var errDisable error
			if modifyAuth {
				errDisable = s.ManualDisable(jobCtx, candidate.authID, "manual")
			} else {
				errDisable = s.ManualDisablePluginOnly(jobCtx, candidate.authID, "manual")
			}
			if errDisable != nil {
				entry.Status = "failed"
				entry.Error = sanitizeError(errDisable)
				entry.ErrorCode = tasks.ErrorCode(errDisable)
				result.Items[index] = entry
				tasks.ReportItem(jobCtx, entry)
				return
			}
			entry.Status = "disabled"
			result.Items[index] = entry
			tasks.ReportItem(jobCtx, entry)
		})
		if errRun != nil {
			return nil, errRun
		}
		for _, item := range result.Items {
			switch item.Status {
			case "disabled":
				result.Disabled++
			case "skipped":
				result.Skipped++
			case "failed":
				result.Failed++
			}
		}
		return result, nil
	})
	return task, http.StatusAccepted, nil
}
