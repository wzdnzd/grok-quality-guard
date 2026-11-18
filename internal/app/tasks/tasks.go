package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"grok-quality-guard/internal/app/redact"
)

const (
	taskStatusQueued      = "queued"
	taskStatusRunning     = "running"
	taskStatusSucceeded   = "succeeded"
	taskStatusFailed      = "failed"
	taskStatusCancelled   = "cancelled"
	taskStatusInterrupted = "interrupted"

	taskRetention = time.Hour
	taskLimit     = 200
)

var (
	ErrTaskNotFound = errors.New("task not found")
	ErrTaskFinished = errors.New("task already finished")
)

// Task is the management-facing view of one long-running operation.
type Task struct {
	ID             string    `json:"task_id"`
	Kind           string    `json:"kind"`
	Category       string    `json:"category,omitempty"`
	Label          string    `json:"label,omitempty"`
	AuthIDs        []string  `json:"auth_ids,omitempty"`
	AuthCount      int       `json:"auth_count,omitempty"`
	Status         string    `json:"status"`
	Progress       string    `json:"progress,omitempty"`
	CurrentStage   string    `json:"current_stage,omitempty"`
	Total          int       `json:"total"`
	Completed      int       `json:"completed"`
	Running        int       `json:"running"`
	Waiting        int       `json:"waiting"`
	Succeeded      int       `json:"succeeded"`
	Failed         int       `json:"failed"`
	Skipped        int       `json:"skipped"`
	ItemCount      int       `json:"item_count,omitempty"`
	ItemsStored    int       `json:"items_stored,omitempty"`
	ItemsTruncated bool      `json:"items_truncated,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	EndedAt        time.Time `json:"ended_at,omitempty"`
	DurationMS     int64     `json:"duration_ms,omitempty"`
	Result         any       `json:"result,omitempty"`
	Error          string    `json:"error,omitempty"`
	ErrorStatus    int       `json:"error_status,omitempty"`
}

// ActiveTask is the scheduling view of a queued or running task. AuthIDs are
// only meaningful for task kinds that operate on auth files.
type ActiveTask struct {
	Kind    string
	AuthIDs []string
}

type record struct {
	task            Task
	cancel          context.CancelFunc
	cancelRequested bool
	history         *activeHistory
	lastMetaWrite   time.Time
}

type taskContextKey struct{}
type taskItemKey struct{}

type contextTask struct {
	manager *Manager
	id      string
}

type itemReport struct {
	outcome string
}

type recentTask struct {
	task      Task
	expiresAt time.Time
}

var taskSequence atomic.Uint64

type Manager struct {
	mu     sync.Mutex
	tasks  map[string]*record
	recent map[string]recentTask
	wg     sync.WaitGroup

	historyWriteMu    sync.Mutex
	historyMu         sync.Mutex
	historyDir        string
	historyConfig     HistoryConfig
	historyConfigured bool
	historyLoaded     bool
	historyIndex      []historyEntry
	historyCleanupAt  time.Time
}

func NewManager() *Manager {
	return &Manager{tasks: make(map[string]*record), recent: make(map[string]recentTask)}
}

func (m *Manager) Wait() {
	m.wg.Wait()
}

func (m *Manager) Start(kind string, authIDs []string, progress string, fn func(context.Context) (any, error)) Task {
	if m == nil {
		return Task{Kind: kind, Status: taskStatusFailed, Error: "task manager is not initialized"}
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	ids := cleanTaskAuthIDs(authIDs)
	task := Task{
		ID:           nextTaskID(now),
		Kind:         strings.TrimSpace(kind),
		Category:     TaskCategory(kind),
		Label:        TaskLabel(kind),
		AuthIDs:      ids,
		AuthCount:    len(ids),
		Status:       taskStatusQueued,
		Progress:     strings.TrimSpace(progress),
		CurrentStage: strings.TrimSpace(progress),
		Total:        len(ids),
		Waiting:      len(ids),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	m.mu.Lock()
	if m.tasks == nil {
		m.tasks = make(map[string]*record)
	}
	if m.recent == nil {
		m.recent = make(map[string]recentTask)
	}
	m.pruneRecentLocked(now)
	m.pruneLocked(now)
	entry := &record{task: task, cancel: cancel, lastMetaWrite: now}
	if active, errHistory := m.openActiveHistoryLocked(task); errHistory == nil {
		entry.history = active
	}
	m.tasks[task.ID] = entry
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		m.runTask(ctx, task.ID, fn)
	}()
	return Clone(task)
}

func nextTaskID(now time.Time) string {
	return fmt.Sprintf("%d-%d", now.UnixNano(), taskSequence.Add(1))
}

func cleanTaskAuthIDs(authIDs []string) []string {
	seen := make(map[string]struct{}, len(authIDs))
	out := make([]string, 0, len(authIDs))
	for _, authID := range authIDs {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		if _, exists := seen[authID]; exists {
			continue
		}
		seen[authID] = struct{}{}
		out = append(out, authID)
	}
	return out
}

func (m *Manager) runTask(ctx context.Context, taskID string, fn func(context.Context) (any, error)) {
	m.mu.Lock()
	entry := m.tasks[taskID]
	if entry == nil {
		m.mu.Unlock()
		return
	}
	entry.task.Status = taskStatusRunning
	entry.task.StartedAt = time.Now().UTC()
	entry.task.UpdatedAt = entry.task.StartedAt
	cancelledBeforeRun := entry.cancelRequested || ctx.Err() != nil
	m.persistActiveMetadataLocked(entry, true)
	m.mu.Unlock()

	ctx = context.WithValue(ctx, taskContextKey{}, &contextTask{manager: m, id: taskID})
	var result any
	var errTask error
	if cancelledBeforeRun {
		errTask = context.Canceled
	} else {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					errTask = fmt.Errorf("task panic: %v", recovered)
				}
			}()
			result, errTask = fn(ctx)
		}()
	}

	now := time.Now().UTC()
	m.mu.Lock()
	entry = m.tasks[taskID]
	if entry == nil {
		m.mu.Unlock()
		return
	}
	entry.task.EndedAt = now
	entry.task.UpdatedAt = now
	if !entry.task.StartedAt.IsZero() {
		entry.task.DurationMS = maxInt64(0, now.Sub(entry.task.StartedAt).Milliseconds())
	}
	applyResultCounts(&entry.task, result)
	applyResultItemMetrics(&entry.task, result, m.historyMaxItems())
	entry.task.AuthCount = len(entry.task.AuthIDs)
	entry.task.Waiting = 0
	entry.task.Running = 0
	if entry.task.Total == 0 {
		entry.task.Total = maxInt(entry.task.Completed, len(entry.task.AuthIDs))
	}
	if entry.task.Completed == 0 && entry.task.Total > 0 && errTask == nil && entry.task.Succeeded == 0 && entry.task.Failed == 0 && entry.task.Skipped == 0 {
		entry.task.Completed = entry.task.Total
		entry.task.Succeeded = entry.task.Total
	}
	if entry.cancelRequested || errors.Is(errTask, context.Canceled) {
		entry.task.Status = taskStatusCancelled
		entry.task.Result = result
		entry.task.Error = ""
		entry.task.ErrorStatus = 0
		entry.task.CurrentStage = "已取消"
	} else if errTask != nil {
		entry.task.Status = taskStatusFailed
		entry.task.Error = redact.SanitizeError(errTask)
		entry.task.ErrorStatus = taskErrorStatus(errTask)
		entry.task.Result = result
		entry.task.CurrentStage = "执行失败"
	} else {
		entry.task.Status = taskStatusSucceeded
		entry.task.Result = result
		entry.task.Error = ""
		entry.task.CurrentStage = "已完成"
	}
	entry.task.Progress = ""
	if entry.task.Completed > entry.task.Total && entry.task.Total > 0 {
		entry.task.Completed = entry.task.Total
	}
	if entry.task.Succeeded+entry.task.Failed+entry.task.Skipped < entry.task.Completed {
		entry.task.Succeeded += entry.task.Completed - entry.task.Succeeded - entry.task.Failed - entry.task.Skipped
	}
	if len(entry.task.AuthIDs) > 20 {
		entry.task.AuthIDs = append([]string(nil), entry.task.AuthIDs[:20]...)
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	history := entry.history
	entry.history = nil
	if history != nil {
		writeFinalResultItems(history, &entry.task, m.historyMaxItems())
		flushActiveHistory(history)
	}
	m.persistActiveMetadataLocked(entry, true)
	finalTask := Clone(entry.task)
	m.mu.Unlock()

	persisted := false
	if history != nil {
		persisted = m.finalizeHistory(finalTask, history)
	}
	if persisted || history == nil || history.discarded {
		m.mu.Lock()
		delete(m.tasks, taskID)
		if history == nil || history.discarded {
			m.cacheRecentLocked(finalTask, now)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) cacheRecentLocked(task Task, now time.Time) {
	if m.recent == nil {
		m.recent = make(map[string]recentTask)
	}
	m.recent[task.ID] = recentTask{task: Clone(task), expiresAt: now.Add(time.Minute)}
	for len(m.recent) > 64 {
		var oldestID string
		var oldest time.Time
		for id, recent := range m.recent {
			if oldestID == "" || recent.expiresAt.Before(oldest) {
				oldestID, oldest = id, recent.expiresAt
			}
		}
		delete(m.recent, oldestID)
	}
}

func (m *Manager) pruneRecentLocked(now time.Time) {
	for id, recent := range m.recent {
		if recent.expiresAt.IsZero() || !recent.expiresAt.After(now) {
			delete(m.recent, id)
		}
	}
}

func taskErrorStatus(errValue error) int {
	var withStatus interface{ HTTPStatus() int }
	if errors.As(errValue, &withStatus) {
		return withStatus.HTTPStatus()
	}
	return 0
}

// RunBoundedQueue executes at most workers callbacks in parallel. When the
// context belongs to a managed task, queue occupancy is reflected in the task
// progress counters automatically.
func RunBoundedQueue[T any](ctx context.Context, values []T, queueSize, workers int, fn func(context.Context, T)) error {
	if len(values) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if workers <= 0 {
		workers = 1
	}
	if workers > len(values) {
		workers = len(values)
	}
	if queueSize <= 0 || queueSize > len(values) {
		queueSize = len(values)
	}
	ensureTaskTotal(ctx, len(values))
	jobs := make(chan T, queueSize)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case value, ok := <-jobs:
					if !ok {
						return
					}
					if ctx.Err() != nil {
						return
					}
					beginTaskItem(ctx)
					jobCtx := context.WithValue(ctx, taskItemKey{}, &itemReport{})
					func() {
						defer completeTaskItem(jobCtx, "completed", nil)
						fn(jobCtx, value)
					}()
				}
			}
		}()
	}
	for _, value := range values {
		select {
		case jobs <- value:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	return nil
}

func (m *Manager) HasActive(kind, authID string) bool {
	if m == nil {
		return false
	}
	kind = strings.TrimSpace(kind)
	authID = strings.TrimSpace(authID)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.tasks {
		if entry == nil || entry.task.Kind != kind {
			continue
		}
		if entry.task.Status != taskStatusQueued && entry.task.Status != taskStatusRunning {
			continue
		}
		if authID == "" {
			return true
		}
		for _, candidate := range entry.task.AuthIDs {
			if candidate == authID {
				return true
			}
		}
	}
	return false
}

func (m *Manager) ByID(taskID string) (Task, bool) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return Task{}, false
	}
	m.mu.Lock()
	entry := m.tasks[taskID]
	if entry != nil {
		task := Clone(entry.task)
		m.mu.Unlock()
		return task, true
	}
	m.pruneRecentLocked(time.Now().UTC())
	if recent, ok := m.recent[taskID]; ok {
		m.mu.Unlock()
		return Clone(recent.task), true
	}
	m.mu.Unlock()
	return m.historyTask(taskID)
}

func (m *Manager) List(limit int) []Task {
	if limit <= 0 {
		limit = 50
	}
	if limit > taskLimit {
		limit = taskLimit
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now().UTC())
	tasks := make([]Task, 0, len(m.tasks))
	for _, entry := range m.tasks {
		if entry != nil {
			view := Clone(entry.task)
			view.AuthIDs = nil
			view.Result = summaryResult(view.Result)
			tasks = append(tasks, view)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].CreatedAt.After(tasks[j].CreatedAt) })
	if len(tasks) > limit {
		tasks = tasks[:limit]
	}
	return tasks
}

func (m *Manager) pruneLocked(now time.Time) {
	for id, entry := range m.tasks {
		if entry == nil {
			delete(m.tasks, id)
			continue
		}
		finished := entry.task.Status == taskStatusSucceeded || entry.task.Status == taskStatusFailed || entry.task.Status == taskStatusCancelled || entry.task.Status == taskStatusInterrupted
		if finished && !entry.task.EndedAt.IsZero() && now.Sub(entry.task.EndedAt) > taskRetention {
			delete(m.tasks, id)
		}
	}
	if len(m.tasks) <= taskLimit {
		return
	}
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		left := m.tasks[ids[i]]
		right := m.tasks[ids[j]]
		if left == nil {
			return true
		}
		if right == nil {
			return false
		}
		return left.task.CreatedAt.Before(right.task.CreatedAt)
	})
	for _, id := range ids[:len(ids)-taskLimit] {
		if entry := m.tasks[id]; entry != nil && entry.cancel != nil {
			entry.cancel()
		}
		delete(m.tasks, id)
	}
}

// CancelTask requests cancellation for one queued or running task. Cancellation
// is cooperative: the task stops admitting queued work and the active worker
// exits when its context-aware operation returns.
func (m *Manager) CancelTask(taskID string) (Task, error) {
	taskID = strings.TrimSpace(taskID)
	if m == nil {
		return Task{}, ErrTaskNotFound
	}
	m.mu.Lock()
	entry := m.tasks[taskID]
	if entry != nil {
		if entry.task.Status != taskStatusQueued && entry.task.Status != taskStatusRunning {
			task := Clone(entry.task)
			m.mu.Unlock()
			return task, ErrTaskFinished
		}
		entry.cancelRequested = true
		entry.task.Progress = "正在取消任务"
		entry.task.CurrentStage = "正在取消任务"
		entry.task.UpdatedAt = time.Now().UTC()
		m.persistActiveMetadataLocked(entry, true)
		if entry.cancel != nil {
			entry.cancel()
		}
		task := Clone(entry.task)
		m.mu.Unlock()
		return task, nil
	}
	if recent, ok := m.recent[taskID]; ok {
		task := Clone(recent.task)
		m.mu.Unlock()
		return task, ErrTaskFinished
	}
	m.mu.Unlock()
	if task, ok := m.historyTask(taskID); ok {
		return task, ErrTaskFinished
	}
	return Task{}, ErrTaskNotFound
}

func (m *Manager) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.tasks {
		if entry != nil {
			entry.cancelRequested = true
			if entry.cancel != nil {
				entry.cancel()
			}
		}
	}
}

func Clone(task Task) Task {
	cloned := task
	if task.AuthIDs != nil {
		cloned.AuthIDs = append([]string(nil), task.AuthIDs...)
	}
	return cloned
}

func (m *Manager) HasActivePrefix(prefixes ...string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.tasks {
		if entry == nil || (entry.task.Status != taskStatusQueued && entry.task.Status != taskStatusRunning) {
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry.task.Kind, prefix) {
				return true
			}
		}
	}
	return false
}

// Active returns queued and running tasks matching any prefix. The returned
// auth IDs are copied so callers can use the snapshot without locking the
// manager or observing later task mutations.
func (m *Manager) Active(prefixes ...string) []ActiveTask {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var active []ActiveTask
	for _, entry := range m.tasks {
		if entry == nil || (entry.task.Status != taskStatusQueued && entry.task.Status != taskStatusRunning) {
			continue
		}
		matched := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry.task.Kind, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		active = append(active, ActiveTask{
			Kind:    entry.task.Kind,
			AuthIDs: append([]string(nil), entry.task.AuthIDs...),
		})
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].Kind == active[j].Kind {
			return strings.Join(active[i].AuthIDs, "\x00") < strings.Join(active[j].AuthIDs, "\x00")
		}
		return active[i].Kind < active[j].Kind
	})
	return active
}

// SetStage updates the human-readable stage shown while a task is running.
func SetStage(ctx context.Context, stage string) {
	item := taskFromContext(ctx)
	if item == nil {
		return
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return
	}
	item.manager.mu.Lock()
	defer item.manager.mu.Unlock()
	entry := item.manager.tasks[item.id]
	if entry == nil {
		return
	}
	entry.task.Progress = stage
	entry.task.CurrentStage = stage
	entry.task.UpdatedAt = time.Now().UTC()
	item.manager.persistActiveMetadataLocked(entry, false)
}

func taskFromContext(ctx context.Context) *contextTask {
	if ctx == nil {
		return nil
	}
	value, _ := ctx.Value(taskContextKey{}).(*contextTask)
	if value == nil || value.manager == nil {
		return nil
	}
	return value
}

func ensureTaskTotal(ctx context.Context, total int) {
	item := taskFromContext(ctx)
	if item == nil || total <= 0 {
		return
	}
	item.manager.mu.Lock()
	defer item.manager.mu.Unlock()
	entry := item.manager.tasks[item.id]
	if entry == nil || entry.task.Total >= total {
		return
	}
	entry.task.Total = total
	entry.task.Waiting = maxInt(entry.task.Waiting, total-entry.task.Completed-entry.task.Running)
	entry.task.UpdatedAt = time.Now().UTC()
	item.manager.persistActiveMetadataLocked(entry, false)
}

func beginTaskItem(ctx context.Context) {
	item := taskFromContext(ctx)
	if item == nil {
		return
	}
	item.manager.mu.Lock()
	defer item.manager.mu.Unlock()
	entry := item.manager.tasks[item.id]
	if entry == nil {
		return
	}
	if entry.task.Waiting > 0 {
		entry.task.Waiting--
	}
	entry.task.Running++
	entry.task.UpdatedAt = time.Now().UTC()
	item.manager.persistActiveMetadataLocked(entry, false)
}

func completeTaskItem(ctx context.Context, status string, errValue error) {
	item := taskFromContext(ctx)
	if item == nil {
		return
	}
	item.manager.mu.Lock()
	defer item.manager.mu.Unlock()
	entry := item.manager.tasks[item.id]
	if entry == nil {
		return
	}
	if entry.task.Running > 0 {
		entry.task.Running--
	}
	entry.task.Completed++
	entry.task.UpdatedAt = time.Now().UTC()
	outcome := ""
	if report, _ := ctx.Value(taskItemKey{}).(*itemReport); report != nil {
		outcome = report.outcome
	}
	if outcome == "" {
		outcome = itemOutcome(status, errValue)
	}
	switch outcome {
	case "failed":
		entry.task.Failed++
	case "skipped":
		entry.task.Skipped++
	default:
		entry.task.Succeeded++
	}
	item.manager.persistActiveMetadataLocked(entry, false)
}

// ReportItem records a completed work item against the active task history.
// Callers should invoke it from inside a RunBoundedQueue callback. The item is
// visible on the next task-detail refresh and also determines how the queue
// classifies the completed unit as succeeded, failed, or skipped.
func ReportItem(ctx context.Context, item any) {
	taskValue := taskFromContext(ctx)
	if taskValue == nil || item == nil {
		return
	}
	raw, errMarshal := json.Marshal(item)
	if errMarshal != nil {
		return
	}
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	status := ""
	if value != nil {
		status = stringFromAny(value["status"])
		if stringFromAny(value["error"]) != "" {
			status = "failed"
		}
	}
	outcome := itemOutcomeFromMap(value, status)
	if report, _ := ctx.Value(taskItemKey{}).(*itemReport); report != nil {
		report.outcome = outcome
	}

	taskValue.manager.mu.Lock()
	defer taskValue.manager.mu.Unlock()
	entry := taskValue.manager.tasks[taskValue.id]
	if entry == nil {
		return
	}
	if entry.history != nil {
		entry.task.ItemCount++
		if !appendActiveItem(entry.history, raw, taskValue.manager.historyMaxItems()) {
			entry.task.ItemsTruncated = true
		}
		entry.task.ItemsStored = entry.history.itemsStored
		entry.task.UpdatedAt = time.Now().UTC()
		taskValue.manager.persistActiveMetadataLocked(entry, false)
	}
}

func itemOutcome(status string, errValue error) string {
	if errValue != nil {
		return "failed"
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error", "unreachable", "partial", "needs_reconcile", "reconcile_required", "commit_unknown", "unavailable", "permanent_failure":
		return "failed"
	case "skipped", "superseded", "unchanged", "already_bound", "pending", "waiting", "protected", "external_left_disabled", "canceled", "cancelled", "paused":
		return "skipped"
	default:
		return "succeeded"
	}
}

func itemOutcomeFromMap(value map[string]any, status string) string {
	if value != nil {
		if booleanFromAny(value["needs_reconcile"]) || booleanFromAny(value["commit_unknown"]) {
			return "failed"
		}
		if stringFromAny(value["error"]) != "" {
			return "failed"
		}
	}
	return itemOutcome(status, nil)
}

func booleanFromAny(value any) bool {
	boolean, _ := value.(bool)
	return boolean
}

func applyResultCounts(task *Task, result any) {
	if task == nil || result == nil {
		return
	}
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return
	}
	var value any
	if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal != nil {
		return
	}
	root, okRoot := value.(map[string]any)
	if !okRoot {
		return
	}
	reported := task.Succeeded + task.Failed + task.Skipped
	total := numberFromAny(root["total"])
	if total <= 0 {
		total = numberFromAny(root["count"])
	}
	if counts, ok := root["counts"].(map[string]any); ok && reported == 0 {
		total = maxInt(total, numberFromAny(counts["total"]))
		task.Succeeded = numberFromAny(counts["succeeded"])
		task.Failed = numberFromAny(counts["failed"])
		if pending := numberFromAny(counts["pending"]); pending > 0 {
			task.Skipped = maxInt(task.Skipped, pending)
		}
		if task.Succeeded == 0 {
			task.Succeeded = numberFromAny(counts["reachable"]) + numberFromAny(counts["deleted"])
		}
		if task.Failed == 0 {
			task.Failed = numberFromAny(counts["unreachable"])
		}
	}
	items := resultItems(root)
	if len(items) > 0 && reported == 0 {
		total = maxInt(total, len(items))
		success, failed, skipped := 0, 0, 0
		for _, rawItem := range items {
			var item map[string]any
			if json.Unmarshal(rawItem, &item) != nil {
				continue
			}
			switch itemOutcomeFromMap(item, stringFromAny(item["status"])) {
			case "failed":
				failed++
			case "skipped":
				skipped++
			default:
				success++
			}
		}
		if task.Succeeded == 0 && task.Failed == 0 && task.Skipped == 0 {
			task.Succeeded, task.Failed, task.Skipped = success, failed, skipped
		}
	}
	if total > 0 {
		task.Total = maxInt(task.Total, total)
	}
	if task.Completed < task.Succeeded+task.Failed+task.Skipped {
		task.Completed = task.Succeeded + task.Failed + task.Skipped
	}
	if task.Total > 0 && task.Completed > task.Total {
		task.Completed = task.Total
	}
}

func applyResultItemMetrics(task *Task, result any, maxItems int) {
	if task == nil || result == nil {
		return
	}
	raw, errMarshal := json.Marshal(result)
	if errMarshal != nil {
		return
	}
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return
	}
	items := resultItems(root)
	if len(items) == 0 {
		return
	}
	task.ItemCount = maxInt(task.ItemCount, len(items))
	stored := len(items)
	if maxItems > 0 && stored > maxItems {
		stored = maxItems
		task.ItemsTruncated = true
	}
	if task.ItemsStored < stored {
		task.ItemsStored = stored
	}
	if booleanFromAny(root["items_truncated"]) {
		task.ItemsTruncated = true
	}
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

// TaskCategory groups tasks in the central task view.
func TaskCategory(kind string) string {
	kind = strings.TrimSpace(kind)
	switch {
	case strings.HasPrefix(kind, "accounts."):
		return "account"
	case strings.HasPrefix(kind, "proxy."), strings.HasPrefix(kind, "resin."):
		return "proxy"
	case strings.HasPrefix(kind, "quality."):
		return "quality"
	default:
		return "system"
	}
}

// TaskLabel returns a stable user-facing name for a task kind.
func TaskLabel(kind string) string {
	labels := map[string]string{
		"accounts.probe":                "主动探测账号",
		"accounts.probe_selected":       "批量检测账号",
		"accounts.restore_selected":     "批量恢复账号",
		"accounts.disable_selected":     "批量禁用账号",
		"quality.cross_verify":          "质量交叉验证",
		"proxy.auto_rotate":             "质量异常后轮换代理",
		"proxy.bind":                    "批量绑定代理",
		"proxy.clear":                   "批量清除代理",
		"proxy.rotate":                  "批量轮换代理",
		"proxy.adopt":                   "批量接管代理",
		"proxy.bind_unbound":            "绑定未绑定账号",
		"proxy.template.apply":          "应用代理模板",
		"proxy.source.create":           "添加代理",
		"proxy.source.update":           "保存代理",
		"proxy.source.delete":           "删除代理",
		"proxy.sources.test_selected":   "批量检测代理",
		"proxy.sources.delete_selected": "批量删除代理",
		"proxy.change.retry":            "重试代理同步",
	}
	if label := strings.TrimSpace(labels[strings.TrimSpace(kind)]); label != "" {
		return label
	}
	return "任务"
}
