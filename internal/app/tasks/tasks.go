package tasks

import (
	"context"
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
	taskStatusQueued    = "queued"
	taskStatusRunning   = "running"
	taskStatusSucceeded = "succeeded"
	taskStatusFailed    = "failed"

	taskRetention = time.Hour
	taskLimit     = 200
)

// Task is the management-facing view of one long-running operation.
type Task struct {
	ID          string    `json:"task_id"`
	Kind        string    `json:"kind"`
	AuthIDs     []string  `json:"auth_ids,omitempty"`
	AuthCount   int       `json:"auth_count,omitempty"`
	Status      string    `json:"status"`
	Progress    string    `json:"progress,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	Result      any       `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	ErrorStatus int       `json:"error_status,omitempty"`
}

type record struct {
	task   Task
	cancel context.CancelFunc
}

var taskSequence atomic.Uint64

type Manager struct {
	mu    sync.Mutex
	tasks map[string]*record
	wg    sync.WaitGroup
}

func NewManager() *Manager {
	return &Manager{tasks: make(map[string]*record)}
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
	task := Task{
		ID:        nextTaskID(now),
		Kind:      strings.TrimSpace(kind),
		AuthIDs:   cleanTaskAuthIDs(authIDs),
		Status:    taskStatusQueued,
		Progress:  strings.TrimSpace(progress),
		CreatedAt: now,
	}
	m.mu.Lock()
	if m.tasks == nil {
		m.tasks = make(map[string]*record)
	}
	m.pruneLocked(now)
	m.tasks[task.ID] = &record{task: task, cancel: cancel}
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
	record := m.tasks[taskID]
	if record == nil {
		m.mu.Unlock()
		return
	}
	record.task.Status = taskStatusRunning
	record.task.StartedAt = time.Now().UTC()
	m.mu.Unlock()

	var result any
	var errTask error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				errTask = fmt.Errorf("task panic: %v", recovered)
			}
		}()
		result, errTask = fn(ctx)
	}()

	m.mu.Lock()
	record = m.tasks[taskID]
	if record != nil {
		record.task.EndedAt = time.Now().UTC()
		record.task.AuthCount = len(record.task.AuthIDs)
		if len(record.task.AuthIDs) > 20 {
			record.task.AuthIDs = append([]string(nil), record.task.AuthIDs[:20]...)
		}
		if errTask != nil {
			record.task.Status = taskStatusFailed
			record.task.Error = redact.SanitizeError(errTask)
			record.task.ErrorStatus = taskErrorStatus(errTask)
			record.task.Result = result
		} else {
			record.task.Status = taskStatusSucceeded
			record.task.Result = result
			record.task.Error = ""
		}
		record.task.Progress = ""
		record.cancel()
	}
	m.mu.Unlock()
}

func taskErrorStatus(errValue error) int {
	var withStatus interface{ HTTPStatus() int }
	if errors.As(errValue, &withStatus) {
		return withStatus.HTTPStatus()
	}
	return 0
}

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
					fn(ctx, value)
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
	for _, record := range m.tasks {
		if record == nil || record.task.Kind != kind {
			continue
		}
		if record.task.Status != taskStatusQueued && record.task.Status != taskStatusRunning {
			continue
		}
		if authID == "" {
			return true
		}
		for _, candidate := range record.task.AuthIDs {
			if candidate == authID {
				return true
			}
		}
	}
	return false
}

func (m *Manager) ByID(taskID string) (Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record := m.tasks[strings.TrimSpace(taskID)]
	if record == nil {
		return Task{}, false
	}
	return Clone(record.task), true
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
	for _, record := range m.tasks {
		if record != nil {
			view := record.task
			view.AuthIDs = nil
			if result, ok := view.Result.(map[string]any); ok {
				summary := make(map[string]any, len(result))
				for key, value := range result {
					if key != "items" && key != "samples" {
						summary[key] = value
					}
				}
				view.Result = summary
			}
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
	for id, record := range m.tasks {
		if record == nil {
			delete(m.tasks, id)
			continue
		}
		finished := record.task.Status == taskStatusSucceeded || record.task.Status == taskStatusFailed
		if finished && !record.task.EndedAt.IsZero() && now.Sub(record.task.EndedAt) > taskRetention {
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
		if record := m.tasks[id]; record != nil && record.cancel != nil {
			record.cancel()
		}
		delete(m.tasks, id)
	}
}

func (m *Manager) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, record := range m.tasks {
		if record != nil && record.cancel != nil {
			record.cancel()
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
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, record := range m.tasks {
		if record == nil || (record.task.Status != taskStatusQueued && record.task.Status != taskStatusRunning) {
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(record.task.Kind, prefix) {
				return true
			}
		}
	}
	return false
}
