package management

import (
	"context"
	"fmt"
	"grok-quality-guard/internal/app/action"
	"net/http"
	"strings"
	"time"
)

func (s *Service) actionProbe(ctx context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(asString(params["auth_id"]))
	if authID == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("auth_id is required")
	}
	if _, okAccount := s.findAccount(authID); !okAccount {
		_ = s.RefreshCatalog(ctx)
		if _, okAccount = s.findAccount(authID); !okAccount {
			return nil, http.StatusNotFound, fmt.Errorf("auth not found")
		}
	}
	force := action.Bool(params, "force")
	task := s.startTask("accounts.probe", []string{authID}, "正在执行主动探测", func(taskCtx context.Context) (any, error) {
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
			return nil, fmt.Errorf("auth not found")
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
		return nil, http.StatusBadRequest, fmt.Errorf("auth_ids is required")
	}
	candidates, errCandidates := s.selectedAccountCandidates(ctx, authIDs)
	if errCandidates != nil {
		return nil, http.StatusBadRequest, errCandidates
	}
	if s.hasActiveTask("accounts.probe_selected", "") {
		return nil, http.StatusConflict, fmt.Errorf("probe task is already running")
	}
	task := s.startTask("accounts.probe_selected", authIDs, fmt.Sprintf("正在检测 %d 个已选账号", len(candidates)), func(taskCtx context.Context) (any, error) {
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
				return
			}
			if requiresRestoreBeforeProbe(stateBefore) {
				if errRestore := s.restoreBeforeManualProbe(jobCtx, candidate.authID, stateBefore); errRestore != nil {
					entry.Status = "failed"
					entry.Error = sanitizeError(errRestore)
					result.Items[index] = entry
					return
				}
				entry.Restored = true
			}
			if errProbe := s.ProbeAccount(jobCtx, candidate.authID, true, ""); errProbe != nil {
				entry.Status = "failed"
				entry.Error = sanitizeError(errProbe)
				result.Items[index] = entry
				return
			}
			if stateAfter, okState := s.stateAccount(candidate.authID); okState {
				entry.StateAfter = stateAfter.State
			}
			entry.Status = "probed"
			result.Items[index] = entry
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
		return nil, http.StatusBadRequest, fmt.Errorf("auth_id is required")
	}
	duration := time.Duration(0)
	if raw := strings.TrimSpace(asString(params["duration"])); raw != "" {
		parsed, errParse := time.ParseDuration(raw)
		if errParse != nil || parsed <= 0 {
			return nil, http.StatusBadRequest, fmt.Errorf("duration must be a positive duration")
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
		return nil, http.StatusBadRequest, fmt.Errorf("auth_id is required")
	}
	if errDisable := s.ManualDisable(ctx, authID, asString(params["reason"])); errDisable != nil {
		return nil, http.StatusConflict, errDisable
	}
	return s.actionAccountGet(ctx, map[string]any{"auth_id": authID})
}

func (s *Service) actionRestore(ctx context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(asString(params["auth_id"]))
	if authID == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("auth_id is required")
	}
	if errRestore := s.ManualRestore(ctx, authID); errRestore != nil {
		return nil, http.StatusConflict, errRestore
	}
	return s.actionAccountGet(ctx, map[string]any{"auth_id": authID})
}

func (s *Service) actionRestoreSelected(ctx context.Context, params map[string]any) (any, int, error) {
	authIDs := action.AuthIDs(params)
	if len(authIDs) == 0 {
		return nil, http.StatusBadRequest, fmt.Errorf("auth_ids is required")
	}
	candidates, errCandidates := s.selectedAccountCandidates(ctx, authIDs)
	if errCandidates != nil {
		return nil, http.StatusBadRequest, errCandidates
	}
	if s.hasActiveTask("accounts.restore_selected", "") {
		return nil, http.StatusConflict, fmt.Errorf("restore task is already running")
	}
	task := s.startTask("accounts.restore_selected", authIDs, fmt.Sprintf("正在恢复 %d 个已选账号", len(candidates)), func(taskCtx context.Context) (any, error) {
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
				return
			}
			if errRestore := s.ManualRestore(jobCtx, candidate.authID); errRestore != nil {
				entry.Status = "failed"
				entry.Error = sanitizeError(errRestore)
				result.Items[index] = entry
				return
			}
			if stateBefore == StateExternallyDisabled {
				entry.Status = "external_left_disabled"
			} else {
				entry.Status = "restored"
			}
			result.Items[index] = entry
		})
		if errRun != nil {
			return nil, errRun
		}
		for _, item := range result.Items {
			switch item.Status {
			case "restored":
				result.Restored++
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
