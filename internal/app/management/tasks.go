package management

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"grok-quality-guard/internal/app/action"
	"grok-quality-guard/internal/app/tasks"
	proxy "grok-quality-guard/internal/proxy"
)

func (s *Service) actionTaskCenterList(params map[string]any) (any, int, error) {
	filter := tasks.TaskCenterFilter{
		Page:     action.Int(params, "page", 1),
		PageSize: action.Int(params, "page_size", 20),
		Category: strings.TrimSpace(asString(params["category"])),
		Status:   strings.TrimSpace(asString(params["status"])),
		Query:    strings.TrimSpace(asString(params["query"])),
	}
	result, errCenter := s.taskCenter(filter)
	if errCenter != nil {
		return nil, http.StatusInternalServerError, errCenter
	}
	return map[string]any{
		"items":           result.Items,
		"total":           result.Total,
		"page":            result.Page,
		"page_size":       result.PageSize,
		"counts":          result.Counts,
		"history":         result.History,
		"resident_queues": s.taskCenterResidentQueues(),
	}, http.StatusOK, nil
}

func (s *Service) actionTaskCenterDetail(params map[string]any) (any, int, error) {
	taskID := strings.TrimSpace(asString(params["task_id"]))
	if taskID == "" {
		return nil, http.StatusBadRequest, errors.New("task_id is required")
	}
	page := action.Int(params, "page", 1)
	pageSize := action.Int(params, "page_size", 20)
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}
	status := taskCenterStatusFilter(asString(params["item_status"]), asString(params["status_filter"]))
	detail, errDetail := s.taskDetail(taskID, page, pageSize, status)
	if errors.Is(errDetail, tasks.ErrTaskNotFound) {
		return nil, http.StatusNotFound, errors.New("任务不存在")
	}
	if errDetail != nil {
		return nil, http.StatusInternalServerError, errDetail
	}
	detail.Task = tasks.HistorySummary(detail.Task)
	return detail, http.StatusOK, nil
}

func (s *Service) taskCenterResidentQueues() []map[string]any {
	queues := make([]map[string]any, 0, 5)
	policy := s.env.Policy()
	probeQueue := s.env.ProbeQueueSnapshot()
	guardEnabled := s.env.ConfigEnabled() && (policy.PassiveGuard || policy.TransparentGuard)
	if shouldShowQualityProbeQueue(guardEnabled, probeQueue) {
		completed := probeQueue.Succeeded + probeQueue.Failed
		total := completed + probeQueue.Waiting + probeQueue.Active
		status := "idle"
		if probeQueue.Active > 0 || probeQueue.Waiting > 0 {
			status = "running"
		}
		queues = append(queues, map[string]any{
			"id":              "quality_probe",
			"label":           "质量检测队列",
			"category":        "quality",
			"status":          status,
			"description":     "扫描到期账号并逐项执行主动质量探测",
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
			"updated_at":      latestTime(probeQueue.LastScanAt, probeQueue.LastSuccessAt),
			"detail":          fmt.Sprintf("自动探测并发 %d，队列容量 %d；只处理到期的非健康账号。实时进度在此汇总，单账号结果写入事件日志。", policy.Probe.Concurrency, policy.Probe.QueueSize),
		})
	}

	store := s.stateStore()
	proxyService := s.env.Proxy()
	if store == nil || proxyService == nil {
		return queues
	}
	runtime := proxyService.AutoBindRuntime()
	autoConfig := store.ProxyAutoBindConfig()

	if shouldShowAutoBindQueue(autoConfig.Enabled, runtime.QueueDepth, runtime.InFlight, runtime.Running) {
		completed := int(runtime.BoundTotal + runtime.FailedTotal + runtime.SkippedTotal)
		status := "idle"
		if runtime.QueueDepth > 0 || runtime.InFlight > 0 || runtime.Running {
			status = "running"
		}
		if !autoConfig.Enabled && runtime.QueueDepth == 0 && runtime.InFlight == 0 {
			status = "paused"
		}
		queues = append(queues, map[string]any{
			"id":              "auto_bind",
			"label":           "自动绑定队列",
			"category":        "proxy",
			"status":          status,
			"description":     autoBindQueueDescription(autoConfig.Enabled, runtime.WaitingReason),
			"total":           completed + runtime.QueueDepth + runtime.InFlight,
			"completed":       completed,
			"running":         runtime.InFlight,
			"waiting":         runtime.QueueDepth,
			"succeeded":       runtime.BoundTotal,
			"failed":          runtime.FailedTotal,
			"skipped":         runtime.SkippedTotal,
			"last_scan_at":    runtime.LastScanAt,
			"last_success_at": runtime.LastSuccessAt,
			"last_error":      runtime.LastError,
			"updated_at":      latestTime(runtime.LastScanAt, runtime.LastSuccessAt),
			"detail":          "按账号逐项展示自动绑定结果；仅在自动绑定开启或队列仍有工作时显示。",
		})
	}

	if shouldShowProxyHealthQueue(autoConfig.Enabled, autoConfig.RequireHealthy) {
		queues = append(queues, map[string]any{
			"id":           "proxy_health",
			"label":        "代理健康检查",
			"category":     "proxy",
			"status":       "running",
			"description":  "自动绑定要求代理保持新鲜健康状态",
			"total":        0,
			"completed":    0,
			"running":      0,
			"waiting":      0,
			"succeeded":    0,
			"failed":       0,
			"skipped":      0,
			"last_scan_at": runtime.LastScanAt,
			"last_error":   runtime.LastError,
			"updated_at":   runtime.LastScanAt,
			"detail":       "健康检查最长有效时间：" + autoConfig.HealthMaxAge + "；仅自动绑定需要健康检查时显示。",
		})
	}

	changes := store.ProxyChangeSummaries()
	if shouldShowProxyChangeQueue(changes) {
		succeeded, failed, pending := 0, 0, 0
		updatedAt := time.Time{}
		lastError := ""
		for _, change := range changes {
			succeeded += change.Counts.Succeeded
			failed += change.Counts.Failed
			pending += change.Counts.Pending
			if change.CreatedAt.After(updatedAt) {
				updatedAt = change.CreatedAt
			}
			if lastError == "" && change.Error != "" {
				lastError = change.Error
			}
		}
		status := "succeeded"
		if pending > 0 {
			status = "running"
		} else if failed > 0 {
			status = "failed"
		}
		queues = append(queues, map[string]any{
			"id":          "proxy_changes",
			"label":       "代理同步队列",
			"category":    "proxy",
			"status":      status,
			"description": "代理配置变更按账号逐项同步",
			"total":       succeeded + failed + pending,
			"completed":   succeeded + failed,
			"running":     0,
			"waiting":     pending,
			"succeeded":   succeeded,
			"failed":      failed,
			"skipped":     0,
			"updated_at":  updatedAt,
			"last_error":  lastError,
			"detail":      "包含自动绑定、模板应用和手动同步产生的逐个账号执行记录。",
		})
	}

	cleanup := store.ResinLeaseDeletesSnapshot()
	resinBound := 0
	counts := store.ProxyCounts()
	if catalog := proxyService.Catalog(); catalog != nil {
		for _, source := range catalog.Sources {
			if source.Kind == proxy.SourceResin {
				resinBound += counts[source.ID]
			}
		}
	}
	if shouldShowResinCleanupQueue(len(cleanup), resinBound) {
		pending, failed, paused := 0, 0, 0
		updatedAt := time.Time{}
		lastError := ""
		for _, job := range cleanup {
			if job.Paused {
				paused++
			} else if job.LastError != "" || job.Status == "permanent_failure" || job.Status == "credential_blocked" {
				failed++
			} else {
				pending++
			}
			if job.LastError != "" && lastError == "" {
				lastError = job.LastError
			}
			if job.CreatedAt.After(updatedAt) {
				updatedAt = job.CreatedAt
			}
		}
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
			"id":          "resin_cleanup",
			"label":       "Resin 租约清理",
			"category":    "proxy",
			"status":      status,
			"description": "清理轮换后不再引用的 Resin 租约",
			"total":       len(cleanup),
			"completed":   0,
			"running":     0,
			"waiting":     pending,
			"succeeded":   0,
			"failed":      failed,
			"skipped":     paused,
			"resin_bound": resinBound,
			"updated_at":  updatedAt,
			"last_error":  lastError,
			"detail":      "只有存在待清理租约，或 Resin 代理仍被账号使用时显示。",
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

func autoBindQueueDescription(enabled bool, waitingReason string) string {
	if !enabled {
		return "自动绑定已关闭，仅保留尚未结束的队列状态"
	}
	switch waitingReason {
	case "waiting_health":
		return "等待代理健康检查通过后继续"
	case "disabled":
		return "自动绑定已暂停"
	default:
		return "扫描未绑定账号并逐项执行代理绑定"
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
