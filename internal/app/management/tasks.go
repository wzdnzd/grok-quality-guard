package management

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"grok-quality-guard/internal/app/action"
	"grok-quality-guard/internal/app/i18n"
	"grok-quality-guard/internal/app/tasks"
	proxy "grok-quality-guard/internal/proxy"
)

type taskCenterQueueItem struct {
	ID        string    `json:"id"`
	AuthID    string    `json:"auth_id,omitempty"`
	SourceID  string    `json:"source_id,omitempty"`
	Label     string    `json:"label"`
	Status    string    `json:"status"`
	Position  int       `json:"position,omitempty"`
	QueuedAt  time.Time `json:"queued_at,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

type taskCenterQueuePage struct {
	Items    []taskCenterQueueItem `json:"items"`
	Total    int                   `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
}

type taskCenterQueueDetail struct {
	QueueID string                `json:"queue_id"`
	Active  []taskCenterQueueItem `json:"active"`
	Waiting taskCenterQueuePage   `json:"waiting"`
}

func (s *Service) actionTaskCenterList(ctx context.Context, params map[string]any, locales ...string) (any, int, error) {
	locale := localeOrDefault(locales)
	filter := tasks.TaskCenterFilter{
		Page:     action.Int(params, "page", 1),
		PageSize: action.Int(params, "page_size", 20),
		Category: strings.TrimSpace(asString(params["category"])),
		Status:   strings.TrimSpace(asString(params["status"])),
		Query:    strings.TrimSpace(asString(params["query"])),
	}
	var result tasks.TaskCenterResult
	var errCenter error
	if provider, ok := s.env.(interface {
		TaskCenterContext(context.Context, tasks.TaskCenterFilter) (tasks.TaskCenterResult, error)
	}); ok {
		result, errCenter = provider.TaskCenterContext(ctx, filter)
	} else {
		result, errCenter = s.taskCenter(filter)
	}
	if errCenter != nil {
		return nil, http.StatusInternalServerError, errCenter
	}
	for index := range result.Items {
		result.Items[index] = localizeTask(locale, result.Items[index])
	}
	return map[string]any{
		"items":     result.Items,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
		"counts":    result.Counts,
		"history":   result.History,
	}, http.StatusOK, nil
}

// actionTaskCenterQueues is the lightweight companion to tasks.center.list.
// Resident queue state lives in memory and must not wait for the potentially
// expensive history index scan performed by the full task-center response.
func (s *Service) actionTaskCenterQueues(locales ...string) (any, int, error) {
	locale := localeOrDefault(locales)
	return map[string]any{
		"resident_queues": s.taskCenterResidentQueues(locale),
	}, http.StatusOK, nil
}
func (s *Service) actionTaskCenterQueue(params map[string]any) (any, int, error) {
	queueID := strings.TrimSpace(asString(params["queue_id"]))
	page := max(1, action.Int(params, "page", 1))
	pageSize := action.Int(params, "page_size", 20)
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	switch queueID {
	case "quality_probe":
		detail := s.env.ProbeQueueDetail()
		active := make([]taskCenterQueueItem, 0, len(detail.Active))
		for _, entry := range detail.Active {
			status := strings.TrimSpace(entry.Status)
			if status == "" {
				status = "running"
			}
			active = append(active, taskCenterQueueItem{ID: entry.AuthID, AuthID: entry.AuthID, Label: entry.AuthID, Status: status, QueuedAt: entry.QueuedAt, StartedAt: entry.StartedAt})
		}
		waiting := make([]taskCenterQueueItem, 0, len(detail.Waiting))
		for index, entry := range detail.Waiting {
			status := strings.TrimSpace(entry.Status)
			if status == "" {
				status = "waiting"
			}
			waiting = append(waiting, taskCenterQueueItem{ID: entry.AuthID, AuthID: entry.AuthID, Label: entry.AuthID, Status: status, Position: index + 1, QueuedAt: entry.QueuedAt})
		}
		return taskCenterQueueDetail{QueueID: queueID, Active: active, Waiting: paginateTaskCenterQueue(waiting, page, pageSize)}, http.StatusOK, nil
	case "auto_bind", "proxy_health":
		detail, ok := s.env.ProxyQueueDetail(queueID)
		if !ok {
			return nil, http.StatusBadRequest, newAPIActionError("queue_not_found", "api.error.queue_not_found", http.StatusBadRequest, nil)
		}
		active := make([]taskCenterQueueItem, 0, len(detail.Active))
		for _, entry := range detail.Active {
			status := strings.TrimSpace(entry.Status)
			if status == "" {
				status = "running"
			}
			item := taskCenterQueueItem{ID: entry.ID, Label: entry.ID, Status: status, QueuedAt: entry.QueuedAt, StartedAt: entry.StartedAt}
			if queueID == "auto_bind" {
				item.AuthID = entry.ID
				item.Label = entry.ID
			} else {
				item.SourceID = entry.ID
				if name, _ := s.proxySourceInfo(entry.ID); name != "" {
					item.Label = name
				}
			}
			active = append(active, item)
		}
		waiting := make([]taskCenterQueueItem, 0, len(detail.Waiting))
		for index, entry := range detail.Waiting {
			status := strings.TrimSpace(entry.Status)
			if status == "" {
				status = "waiting"
			}
			item := taskCenterQueueItem{ID: entry.ID, Label: entry.ID, Status: status, Position: index + 1, QueuedAt: entry.QueuedAt}
			if queueID == "auto_bind" {
				item.AuthID = entry.ID
			} else {
				item.SourceID = entry.ID
				if name, _ := s.proxySourceInfo(entry.ID); name != "" {
					item.Label = name
				}
			}
			waiting = append(waiting, item)
		}
		return taskCenterQueueDetail{QueueID: queueID, Active: active, Waiting: paginateTaskCenterQueue(waiting, page, pageSize)}, http.StatusOK, nil
	default:
		return nil, http.StatusBadRequest, newAPIActionError("queue_not_found", "api.error.queue_not_found", http.StatusBadRequest, nil)
	}
}

func (s *Service) proxyQueueCounts(queueID string) (active, waiting int, ok bool) {
	return s.env.ProxyQueueDepth(queueID)
}

func paginateTaskCenterQueue(items []taskCenterQueueItem, page, pageSize int) taskCenterQueuePage {
	total := len(items)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	start := total
	if page-1 <= total/pageSize {
		start = (page - 1) * pageSize
	}
	end := min(total, start+pageSize)
	return taskCenterQueuePage{
		Items:    append([]taskCenterQueueItem(nil), items[start:end]...),
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}
}
func (s *Service) actionTaskCenterDetail(params map[string]any, locales ...string) (any, int, error) {
	locale := localeOrDefault(locales)
	taskID := strings.TrimSpace(asString(params["task_id"]))
	if taskID == "" {
		return nil, http.StatusBadRequest, newAPIActionError("task_id_required", "api.error.task_id_required", http.StatusBadRequest, nil)
	}
	page := action.Int(params, "page", 1)
	pageSize := action.Int(params, "page_size", 20)
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	status := taskCenterStatusFilter(asString(params["item_status"]), asString(params["status_filter"]))
	detail, errDetail := s.taskDetail(taskID, page, pageSize, status)
	if errors.Is(errDetail, tasks.ErrTaskNotFound) {
		return nil, http.StatusNotFound, newAPIActionError("task_not_found", "api.error.task_not_found", http.StatusNotFound, nil)
	}
	if errDetail != nil {
		return nil, http.StatusInternalServerError, errDetail
	}
	detail.Task = localizeTask(locale, tasks.HistorySummary(detail.Task))
	return detail, http.StatusOK, nil
}

func (s *Service) actionTaskCenterDelete(params map[string]any) (any, int, error) {
	taskID := strings.TrimSpace(asString(params["task_id"]))
	if taskID == "" {
		return nil, http.StatusBadRequest, newAPIActionError("task_id_required", "api.error.task_id_required", http.StatusBadRequest, nil)
	}
	if errDelete := s.deleteTask(taskID); errDelete != nil {
		switch {
		case errors.Is(errDelete, tasks.ErrTaskNotFound):
			return nil, http.StatusNotFound, newAPIActionError("task_not_found", "api.error.task_not_found", http.StatusNotFound, nil)
		case errors.Is(errDelete, tasks.ErrTaskActive):
			return nil, http.StatusConflict, newAPIActionError("task_active", "api.error.task_active", http.StatusConflict, nil)
		default:
			return nil, http.StatusInternalServerError, errDelete
		}
	}
	return map[string]any{"deleted": true, "task_id": taskID}, http.StatusOK, nil
}

func (s *Service) taskCenterResidentQueues(locales ...string) []map[string]any {
	locale := localeOrDefault(locales)
	queues := make([]map[string]any, 0, 5)
	policy := s.env.Policy()
	probeQueue := s.env.ProbeQueueSnapshot()
	guardEnabled := s.env.ConfigEnabled() && (policy.PassiveGuardEnabled() || policy.TransparentGuardEnabled())
	if shouldShowQualityProbeQueue(guardEnabled, probeQueue) {
		completed := probeQueue.Succeeded + probeQueue.Failed
		total := completed + probeQueue.Waiting + probeQueue.Active
		scanInterval := policy.Probe.ScanInterval.Duration
		if scanInterval <= 0 {
			scanInterval = 15 * time.Second
		}
		status := "starting"
		if probeQueue.Started {
			status = "idle"
		}
		if probeQueue.Active > 0 || probeQueue.Waiting > 0 {
			status = "running"
		}
		queues = append(queues, map[string]any{
			"id":              "quality_probe",
			"label":           i18n.T(locale, "queue.quality_probe.label", nil),
			"category":        "quality",
			"status":          status,
			"started":         probeQueue.Started,
			"description":     i18n.T(locale, "queue.quality_probe.description", nil),
			"total":           total,
			"completed":       completed,
			"running":         probeQueue.Active,
			"waiting":         probeQueue.Waiting,
			"succeeded":       probeQueue.Succeeded,
			"failed":          probeQueue.Failed,
			"skipped":         0,
			"last_scan_at":    probeQueue.LastScanAt,
			"last_success_at": probeQueue.LastSuccessAt,
			"last_error":      probeQueue.LastError,
			"last_error_code": probeQueue.LastErrorCode,
			"updated_at":      latestTime(probeQueue.LastScanAt, probeQueue.LastSuccessAt),
			"detail":          i18n.T(locale, "queue.quality_probe.detail", map[string]any{"interval": scanInterval.String(), "concurrency": policy.Probe.Concurrency, "capacity": policy.Probe.QueueSize}),
		})
	}

	store := s.stateStore()
	proxyService := s.env.Proxy()
	if store == nil || proxyService == nil {
		return queues
	}
	runtime := proxyService.AutoBindRuntime()
	autoConfig := store.ProxyAutoBindConfig()
	autoRunning, autoWaiting := runtime.InFlight, runtime.QueueDepth
	if running, waiting, ok := s.proxyQueueCounts("auto_bind"); ok {
		autoRunning, autoWaiting = running, waiting
	}

	if shouldShowAutoBindQueue(autoConfig.Enabled, autoWaiting, autoRunning, runtime.Running) {
		completed := int(runtime.BoundTotal + runtime.FailedTotal + runtime.SkippedTotal)
		status := "idle"
		if autoWaiting > 0 || autoRunning > 0 || runtime.Running {
			status = "running"
		}
		if !autoConfig.Enabled && autoWaiting == 0 && autoRunning == 0 {
			status = "paused"
		}
		queues = append(queues, map[string]any{
			"id":              "auto_bind",
			"label":           i18n.T(locale, "queue.auto_bind.label", nil),
			"category":        "proxy",
			"status":          status,
			"description":     autoBindQueueDescription(locale, autoConfig.Enabled, runtime.WaitingReason),
			"total":           completed + autoWaiting + autoRunning,
			"completed":       completed,
			"running":         autoRunning,
			"waiting":         autoWaiting,
			"succeeded":       runtime.BoundTotal,
			"failed":          runtime.FailedTotal,
			"skipped":         runtime.SkippedTotal,
			"last_scan_at":    runtime.LastScanAt,
			"last_success_at": runtime.LastSuccessAt,
			"last_error":      runtime.LastError,
			"last_error_code": runtime.LastErrorCode,
			"updated_at":      latestTime(runtime.LastScanAt, runtime.LastSuccessAt),
			"detail":          i18n.T(locale, "queue.auto_bind.detail", nil),
		})
	}

	if shouldShowProxyHealthQueue(autoConfig.Enabled, autoConfig.RequireHealthy) {
		healthRunning, healthWaiting, _ := s.proxyQueueCounts("proxy_health")
		healthStatus := "idle"
		if healthRunning > 0 || healthWaiting > 0 {
			healthStatus = "running"
		}
		queues = append(queues, map[string]any{
			"id":          "proxy_health",
			"label":       i18n.T(locale, "queue.proxy_health.label", nil),
			"category":    "proxy",
			"status":      healthStatus,
			"description": i18n.T(locale, "queue.proxy_health.description", nil),
			"total":       healthRunning + healthWaiting,
			"completed":   0,
			"running":     healthRunning,
			"waiting":     healthWaiting,
			"succeeded":   0,
			"failed":      0,
			"skipped":     0,
			"detail":      i18n.T(locale, "queue.proxy_health.detail", map[string]any{"max_age": autoConfig.HealthMaxAge}),
		})
	}

	changeTotals := store.ProxyChangeTotals()
	if changeTotals.Active {
		succeeded := changeTotals.Succeeded
		failed := changeTotals.Failed
		pending := changeTotals.Pending
		updatedAt := changeTotals.UpdatedAt
		lastError := changeTotals.LastError
		lastErrorCode := changeTotals.LastErrorCode
		status := "succeeded"
		if pending > 0 {
			status = "running"
		} else if failed > 0 {
			status = "failed"
		}
		queues = append(queues, map[string]any{
			"id":              "proxy_changes",
			"label":           i18n.T(locale, "queue.proxy_changes.label", nil),
			"category":        "proxy",
			"status":          status,
			"description":     i18n.T(locale, "queue.proxy_changes.description", nil),
			"total":           succeeded + failed + pending,
			"completed":       succeeded + failed,
			"running":         0,
			"waiting":         pending,
			"succeeded":       succeeded,
			"failed":          failed,
			"skipped":         0,
			"updated_at":      updatedAt,
			"last_error":      lastError,
			"last_error_code": lastErrorCode,
			"detail":          i18n.T(locale, "queue.proxy_changes.detail", nil),
		})
	}

	cleanup := store.ResinCleanupTotals()
	resinBound := store.ResinBindingCount()
	if shouldShowResinCleanupQueue(cleanup.Total, resinBound) {
		pending := cleanup.Pending
		failed := cleanup.Failed
		paused := cleanup.Paused
		updatedAt := cleanup.UpdatedAt
		lastError := cleanup.LastError
		lastErrorCode := cleanup.LastErrorCode
		status := "idle"
		if pending > 0 {
			status = "running"
		}
		if failed > 0 {
			status = "failed"
		}
		if pending == 0 && failed == 0 && paused > 0 {
			status = "paused"
		}
		queues = append(queues, map[string]any{
			"id":              "resin_cleanup",
			"label":           i18n.T(locale, "queue.resin_cleanup.label", nil),
			"category":        "proxy",
			"status":          status,
			"description":     i18n.T(locale, "queue.resin_cleanup.description", nil),
			"total":           cleanup.Total,
			"completed":       0,
			"running":         0,
			"waiting":         pending,
			"succeeded":       0,
			"failed":          failed,
			"skipped":         paused,
			"resin_bound":     resinBound,
			"updated_at":      updatedAt,
			"last_error":      lastError,
			"last_error_code": lastErrorCode,
			"detail":          i18n.T(locale, "queue.resin_cleanup.detail", nil),
		})
	}
	return queues
}

func shouldShowQualityProbeQueue(enabled bool, snapshot ProbeQueueSnapshot) bool {
	return enabled || snapshot.Waiting > 0 || snapshot.Active > 0
}

func shouldShowAutoBindQueue(enabled bool, queueDepth, inFlight int, running bool) bool {
	return enabled || queueDepth > 0 || inFlight > 0 || running
}

func shouldShowProxyHealthQueue(enabled, requireHealthy bool) bool {
	return enabled && requireHealthy
}

func shouldShowProxyChangeQueue(changes []proxy.SourceChange) bool {
	for _, change := range changes {
		if change.Counts.Pending > 0 || change.Counts.Failed > 0 {
			return true
		}
		switch change.Status {
		case "pending", "running", "partial", "failed":
			return true
		}
	}
	return false
}

func shouldShowResinCleanupQueue(cleanupCount, resinBound int) bool {
	return cleanupCount > 0 || resinBound > 0
}

func autoBindQueueDescription(locale string, enabled bool, waitingReason string) string {
	if !enabled {
		return i18n.T(locale, "queue.auto_bind.description.disabled", nil)
	}
	switch waitingReason {
	case "waiting_health":
		return i18n.T(locale, "queue.auto_bind.description.waiting_health", nil)
	case "disabled":
		return i18n.T(locale, "queue.auto_bind.description.paused", nil)
	default:
		return i18n.T(locale, "queue.auto_bind.description.enabled", nil)
	}
}

func latestTime(values ...time.Time) time.Time {
	var latest time.Time
	for _, value := range values {
		if value.After(latest) {
			latest = value
		}
	}
	return latest
}

func taskCenterStatusFilter(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
