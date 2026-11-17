package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"grok-quality-guard/internal/app/action"
	"grok-quality-guard/internal/app/tasks"
	"net/http"
	"os"
	"sort"
	"strings"
)

func (s *Service) actionResinBind(_ context.Context, params map[string]any) (any, int, error) {
	authIDs := action.AuthIDs(params)
	if len(authIDs) == 0 {
		return nil, http.StatusBadRequest, fmt.Errorf("auth_id or auth_ids is required")
	}
	if s.hasActiveTask("resin.bind", "") {
		return nil, http.StatusConflict, fmt.Errorf("resin bind task is already running")
	}
	dryRun := action.Bool(params, "dry_run")
	task := s.startTask("resin.bind", authIDs, "正在绑定 Resin 代理", func(taskCtx context.Context) (any, error) {
		results := make([]ResinBindResult, len(authIDs))
		indices := make([]int, len(authIDs))
		for index := range authIDs {
			indices[index] = index
		}
		errRun := runBoundedQueue(taskCtx, indices, s.probeQueueSize(), s.probeConcurrency(), func(jobCtx context.Context, index int) {
			authID := authIDs[index]
			result, errBind := s.BindResin(jobCtx, authID, dryRun)
			if errBind != nil && result.Error == "" {
				result.Error = sanitizeError(errBind)
			}
			results[index] = result
		})
		if errRun != nil {
			return nil, errRun
		}
		return map[string]any{"items": results, "count": len(results)}, nil
	})
	return task, http.StatusAccepted, nil
}

func (s *Service) actionResinBindUnbound(_ context.Context, params map[string]any) (any, int, error) {
	if s.hasActiveTask("resin.bind_unbound", "") {
		return nil, http.StatusConflict, fmt.Errorf("resin bind-unbound task is already running")
	}
	dryRun := action.Bool(params, "dry_run")
	task := s.startTask("resin.bind_unbound", nil, "正在批量绑定未绑定账号", func(taskCtx context.Context) (any, error) {
		results, errBind := s.BindUnboundResin(taskCtx, dryRun)
		if errBind != nil {
			return nil, errBind
		}
		return map[string]any{"items": results, "count": len(results)}, nil
	})
	return task, http.StatusAccepted, nil
}

func (s *Service) actionResinClear(_ context.Context, params map[string]any) (any, int, error) {
	authIDs := action.AuthIDs(params)
	if len(authIDs) == 0 {
		return nil, http.StatusBadRequest, fmt.Errorf("auth_id or auth_ids is required")
	}
	if s.hasActiveTask("resin.clear", "") {
		return nil, http.StatusConflict, fmt.Errorf("resin clear task is already running")
	}
	task := s.startTask("resin.clear", authIDs, "正在清除账号代理", func(taskCtx context.Context) (any, error) {
		results := make([]ResinClearResult, len(authIDs))
		indices := make([]int, len(authIDs))
		for index := range authIDs {
			indices[index] = index
		}
		errRun := runBoundedQueue(taskCtx, indices, s.probeQueueSize(), s.probeConcurrency(), func(jobCtx context.Context, index int) {
			authID := authIDs[index]
			result, errClear := s.ClearResinProxy(jobCtx, authID)
			if errClear != nil && result.Error == "" {
				result.Error = sanitizeError(errClear)
			}
			results[index] = result
		})
		if errRun != nil {
			return nil, errRun
		}
		return map[string]any{"items": results, "count": len(results)}, nil
	})
	return task, http.StatusAccepted, nil
}

func (s *Service) actionResinRotate(_ context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(asString(params["auth_id"]))
	if authID == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("auth_id is required")
	}
	task := s.startTask("resin.rotate", []string{authID}, "正在轮换代理租约", func(taskCtx context.Context) (any, error) {
		if errRotate := s.RotateResin(taskCtx, authID); errRotate != nil {
			return nil, errRotate
		}
		account, okAccount := s.findAccount(authID)
		if !okAccount {
			return nil, fmt.Errorf("auth not found")
		}
		return s.accountViewForAccount(taskCtx, account)
	})
	return task, http.StatusAccepted, nil
}

func (s *Service) actionPolicyGet() (any, int, error) {
	policy := s.env.Policy()
	return policy, http.StatusOK, nil
}

func (s *Service) actionPolicyUpdate(params map[string]any) (any, int, error) {
	rawPolicy, okPolicy := params["policy"]
	if !okPolicy {
		return nil, http.StatusBadRequest, fmt.Errorf("policy is required")
	}
	encoded, errMarshal := json.Marshal(rawPolicy)
	if errMarshal != nil {
		return nil, http.StatusBadRequest, errMarshal
	}
	var policy Policy
	if errUnmarshal := json.Unmarshal(encoded, &policy); errUnmarshal != nil {
		return nil, http.StatusBadRequest, errUnmarshal
	}
	normalized, errNormalize := normalizePolicy(policy)
	if errNormalize != nil {
		return nil, http.StatusBadRequest, errNormalize
	}
	if errStore := s.stateStore().Update(func(state *State) error {
		state.Policy = normalized
		if state.Resin.Configured {
			state.Resin.AutoRotateAfterQualityFailure = normalized.ResinAutoRotateAfterQualityFail
		}
		return nil
	}); errStore != nil {
		return nil, http.StatusInternalServerError, errStore
	}
	s.env.SetPolicy(normalized)
	s.env.WakeProbeWaiters()
	return normalized, http.StatusOK, nil
}

func (s *Service) actionPolicyReset() (any, int, error) {
	reset, revision := s.env.YAMLPolicy()
	normalized, errNormalize := normalizePolicy(reset)
	if errNormalize != nil {
		return nil, http.StatusInternalServerError, errNormalize
	}
	if errStore := s.stateStore().Update(func(state *State) error {
		state.Policy = normalized
		state.PolicyYAMLRevision = revision
		if state.Resin.Configured {
			state.Resin.AutoRotateAfterQualityFailure = normalized.ResinAutoRotateAfterQualityFail
		}
		return nil
	}); errStore != nil {
		return nil, http.StatusInternalServerError, errStore
	}
	s.env.SetPolicy(normalized)
	s.env.WakeProbeWaiters()
	return normalized, http.StatusOK, nil
}

func (s *Service) actionStateFileUpdate(params map[string]any) (any, int, error) {
	stateFile := strings.TrimSpace(asString(params["state_file"]))
	if stateFile == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("state_file is required")
	}
	activePath, errUpdate := s.env.UpdateStatePath(stateFile)
	if errUpdate != nil {
		switch {
		case errors.Is(errUpdate, os.ErrInvalid):
			return nil, http.StatusBadRequest, errUpdate
		case errors.Is(errUpdate, os.ErrExist):
			return nil, http.StatusConflict, errUpdate
		default:
			return nil, http.StatusInternalServerError, errUpdate
		}
	}
	return map[string]any{"state_file": activePath}, http.StatusOK, nil
}

func (s *Service) actionTaskGet(params map[string]any) (any, int, error) {
	taskID := strings.TrimSpace(asString(params["task_id"]))
	if taskID == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("task_id is required")
	}
	task, okTask := s.taskByID(taskID)
	if !okTask {
		return nil, http.StatusNotFound, fmt.Errorf("任务不存在")
	}
	return task, http.StatusOK, nil
}

func (s *Service) actionTaskCancel(params map[string]any) (any, int, error) {
	taskID := strings.TrimSpace(asString(params["task_id"]))
	if taskID == "" {
		return nil, http.StatusBadRequest, fmt.Errorf("task_id is required")
	}
	task, errCancel := s.cancelTask(taskID)
	switch {
	case errors.Is(errCancel, tasks.ErrTaskNotFound):
		return nil, http.StatusNotFound, fmt.Errorf("任务不存在")
	case errors.Is(errCancel, tasks.ErrTaskFinished):
		return nil, http.StatusConflict, fmt.Errorf("任务已结束，无法取消")
	case errCancel != nil:
		return nil, http.StatusInternalServerError, errCancel
	default:
		return task, http.StatusOK, nil
	}
}

func (s *Service) actionTaskList(params map[string]any) (any, int, error) {
	return map[string]any{"items": s.listTasks(action.Int(params, "limit", 50))}, http.StatusOK, nil
}

func (s *Service) actionEventsList(params map[string]any) (any, int, error) {
	events := []Event(nil)
	if store := s.stateStore(); store != nil {
		events = store.EventsSnapshot()
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Time.After(events[j].Time) })
	authID := strings.TrimSpace(asString(params["auth_id"]))
	filtered := make([]Event, 0, len(events))
	for _, event := range events {
		if authID != "" && event.AuthID != authID {
			continue
		}
		filtered = append(filtered, event)
	}
	limit := action.Int(params, "limit", 100)
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	cursor := action.Int(params, "cursor", 0)
	if cursor < 0 {
		cursor = 0
	}
	start := cursor
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + limit
	if end > len(filtered) {
		end = len(filtered)
	}
	return map[string]any{"items": filtered[start:end], "next_cursor": end, "total": len(filtered)}, http.StatusOK, nil
}
