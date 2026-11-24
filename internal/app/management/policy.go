package management

import (
	"encoding/json"
	"errors"
	"grok-quality-guard/internal/app/action"
	"grok-quality-guard/internal/app/tasks"
	"net/http"
	"os"
	"sort"
	"strings"
)

func (s *Service) actionPolicyGet() (any, int, error) {
	policy := s.env.Policy()
	return policy, http.StatusOK, nil
}

func (s *Service) actionPolicyUpdate(params map[string]any) (any, int, error) {
	rawPolicy, okPolicy := params["policy"]
	if !okPolicy {
		return nil, http.StatusBadRequest, newAPIActionError("policy_required", "api.error.policy_required", http.StatusBadRequest, nil)
	}
	encoded, errMarshal := json.Marshal(rawPolicy)
	if errMarshal != nil {
		return nil, http.StatusBadRequest, newAPIActionError("invalid_policy", "api.error.invalid_policy", http.StatusBadRequest, nil)
	}
	var policy Policy
	if errUnmarshal := json.Unmarshal(encoded, &policy); errUnmarshal != nil {
		return nil, http.StatusBadRequest, newAPIActionError("invalid_policy", "api.error.invalid_policy", http.StatusBadRequest, nil)
	}
	// Older management clients send a policy without the newer task_history
	// object. Preserve the current setting rather than interpreting the missing
	// field as enabled=false and silently disabling history.
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) == nil {
		if _, present := fields["task_history"]; !present {
			policy.TaskHistory = s.env.Policy().TaskHistory
		}
	}
	normalized, errNormalize := normalizePolicy(policy)
	if errNormalize != nil {
		return nil, http.StatusBadRequest, newAPIActionError("invalid_policy", "api.error.invalid_policy", http.StatusBadRequest, nil)
	}
	if errStore := s.stateStore().Update(func(state *State) error {
		state.Policy = normalized
		return nil
	}); errStore != nil {
		return nil, http.StatusInternalServerError, newAPIActionError("state_store_unavailable", "api.error.state_store_unavailable", http.StatusInternalServerError, nil)
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
		return nil
	}); errStore != nil {
		return nil, http.StatusInternalServerError, newAPIActionError("state_store_unavailable", "api.error.state_store_unavailable", http.StatusInternalServerError, nil)
	}
	s.env.SetPolicy(normalized)
	s.env.WakeProbeWaiters()
	return normalized, http.StatusOK, nil
}

func (s *Service) actionStateFileUpdate(params map[string]any) (any, int, error) {
	stateFile := strings.TrimSpace(asString(params["state_file"]))
	if stateFile == "" {
		return nil, http.StatusBadRequest, newAPIActionError("state_file_required", "api.error.state_file_required", http.StatusBadRequest, nil)
	}
	activePath, errUpdate := s.env.UpdateStatePath(stateFile)
	if errUpdate != nil {
		switch {
		case errors.Is(errUpdate, os.ErrInvalid):
			return nil, http.StatusBadRequest, newAPIActionError("state_file_invalid", "api.error.state_file_invalid", http.StatusBadRequest, nil)
		case errors.Is(errUpdate, os.ErrExist):
			return nil, http.StatusConflict, newAPIActionError("state_file_exists", "api.error.state_file_exists", http.StatusConflict, nil)
		default:
			return nil, http.StatusInternalServerError, newAPIActionError("state_file_update_failed", "api.error.state_file_update_failed", http.StatusInternalServerError, nil)
		}
	}
	return map[string]any{"state_file": activePath}, http.StatusOK, nil
}

func (s *Service) actionTaskGet(params map[string]any, locales ...string) (any, int, error) {
	locale := localeOrDefault(locales)
	taskID := strings.TrimSpace(asString(params["task_id"]))
	if taskID == "" {
		return nil, http.StatusBadRequest, newAPIActionError("task_id_required", "api.error.task_id_required", http.StatusBadRequest, nil)
	}
	task, okTask := s.taskByID(taskID)
	if !okTask {
		return nil, http.StatusNotFound, newAPIActionError("task_not_found", "api.error.task_not_found", http.StatusNotFound, nil)
	}
	return localizeTask(locale, task), http.StatusOK, nil
}

func (s *Service) actionTaskCancel(params map[string]any, locales ...string) (any, int, error) {
	locale := localeOrDefault(locales)
	taskID := strings.TrimSpace(asString(params["task_id"]))
	if taskID == "" {
		return nil, http.StatusBadRequest, newAPIActionError("task_id_required", "api.error.task_id_required", http.StatusBadRequest, nil)
	}
	task, errCancel := s.cancelTask(taskID)
	switch {
	case errors.Is(errCancel, tasks.ErrTaskNotFound):
		return nil, http.StatusNotFound, newAPIActionError("task_not_found", "api.error.task_not_found", http.StatusNotFound, nil)
	case errors.Is(errCancel, tasks.ErrTaskFinished):
		return nil, http.StatusConflict, newAPIActionError("task_finished", "api.error.task_finished", http.StatusConflict, nil)
	case errCancel != nil:
		return nil, http.StatusInternalServerError, errCancel
	default:
		return localizeTask(locale, task), http.StatusOK, nil
	}
}

func (s *Service) actionTaskList(params map[string]any, locales ...string) (any, int, error) {
	locale := localeOrDefault(locales)
	items := s.listTasks(action.Int(params, "limit", 50))
	for index := range items {
		items[index] = localizeTask(locale, items[index])
	}
	return map[string]any{"items": items}, http.StatusOK, nil
}

func (s *Service) actionEventsList(params map[string]any, locales ...string) (any, int, error) {
	locale := localeOrDefault(locales)
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
	items := make([]eventView, 0, end-start)
	for _, event := range filtered[start:end] {
		localized := localizeEvent(locale, event)
		items = append(items, eventView{
			Event:    localized,
			Category: verdictCategory(localized.Class),
			Code:     localized.Class,
			Decision: verdictDecision(localized.Class, ""),
		})
	}
	return map[string]any{"items": items, "next_cursor": end, "total": len(filtered)}, http.StatusOK, nil
}
