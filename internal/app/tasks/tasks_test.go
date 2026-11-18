package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestManagerCancelsRunningTaskAndKeepsPartialResult(t *testing.T) {
	manager := NewManager()
	started := make(chan struct{})
	task := manager.Start("test.cancel", nil, "running", func(ctx context.Context) (any, error) {
		close(started)
		<-ctx.Done()
		return map[string]any{"partial": true}, ctx.Err()
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not start")
	}
	cancelled, errCancel := manager.CancelTask(task.ID)
	if errCancel != nil {
		t.Fatal(errCancel)
	}
	if cancelled.Status != taskStatusRunning || cancelled.Progress != "task.stage.cancelling" {
		t.Fatalf("unexpected cancel response: %#v", cancelled)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := manager.ByID(task.ID)
		if !ok {
			t.Fatal("task disappeared")
		}
		if current.Status == taskStatusCancelled {
			result, okResult := current.Result.(map[string]any)
			if !okResult || result["partial"] != true {
				t.Fatalf("cancelled task lost its partial result: %#v", current.Result)
			}
			if current.Error != "" {
				t.Fatalf("cancelled task has an error: %#v", current)
			}
			if _, errRepeat := manager.CancelTask(task.ID); !errors.Is(errRepeat, ErrTaskFinished) {
				t.Fatalf("repeat cancel error = %v, want ErrTaskFinished", errRepeat)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("task did not reach cancelled state")
}

func TestManagerCancelUnknownTask(t *testing.T) {
	manager := NewManager()
	if _, errCancel := manager.CancelTask("missing"); !errors.Is(errCancel, ErrTaskNotFound) {
		t.Fatalf("cancel unknown task error = %v, want ErrTaskNotFound", errCancel)
	}
}

func TestManagerCancelArchivedTaskReturnsFinished(t *testing.T) {
	manager := NewManager()
	if errConfigure := manager.ConfigureHistory(t.TempDir(), DefaultHistoryConfig()); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	task := manager.Start("test.archived", nil, "running", func(context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	manager.Wait()
	if _, ok := manager.historyTask(task.ID); !ok {
		t.Fatal("task was not archived")
	}
	if _, errCancel := manager.CancelTask(task.ID); !errors.Is(errCancel, ErrTaskFinished) {
		t.Fatalf("archived cancel error = %v, want ErrTaskFinished", errCancel)
	}
}

func TestDeleteTaskRejectsActiveTask(t *testing.T) {
	manager := NewManager()
	started := make(chan struct{})
	release := make(chan struct{})
	task := manager.Start("test.delete_active", nil, "running", func(context.Context) (any, error) {
		close(started)
		<-release
		return map[string]any{"ok": true}, nil
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not start")
	}
	if errDelete := manager.DeleteTask(task.ID); !errors.Is(errDelete, ErrTaskActive) {
		t.Fatalf("delete active task error = %v, want ErrTaskActive", errDelete)
	}
	close(release)
	manager.Wait()
}

func TestDeleteTaskRemovesShortLivedRecentResult(t *testing.T) {
	manager := NewManager()
	if err := manager.ConfigureHistory(t.TempDir(), HistoryConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	task := manager.Start("test.delete_recent", nil, "running", func(context.Context) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	manager.Wait()
	if _, ok := manager.ByID(task.ID); !ok {
		t.Fatal("completed task was not retained for its waiter")
	}
	if errDelete := manager.DeleteTask(task.ID); errDelete != nil {
		t.Fatal(errDelete)
	}
	if _, ok := manager.ByID(task.ID); ok {
		t.Fatal("deleted task remained in the recent result cache")
	}
	if errDelete := manager.DeleteTask(task.ID); !errors.Is(errDelete, ErrTaskNotFound) {
		t.Fatalf("repeat delete error = %v, want ErrTaskNotFound", errDelete)
	}
}

func TestTaskKindsAreGroupedAndLabeledForTheTaskCenter(t *testing.T) {
	tests := []struct {
		kind     string
		category string
		label    string
	}{
		{kind: "accounts.probe_selected", category: "account", label: "task.label.accounts_probe_selected"},
		{kind: "proxy.sources.test_selected", category: "proxy", label: "task.label.proxy_sources_test_selected"},
		{kind: "resin.cleanup.retry", category: "proxy", label: "task.label.default"},
		{kind: "quality.cross_verify", category: "quality", label: "task.label.quality_cross_verify"},
		{kind: "system.cleanup", category: "system", label: "task.label.default"},
	}
	for _, test := range tests {
		if got := TaskCategory(test.kind); got != test.category {
			t.Fatalf("TaskCategory(%q) = %q, want %q", test.kind, got, test.category)
		}
		if got := TaskLabelKey(test.kind); got != test.label {
			t.Fatalf("TaskLabelKey(%q) = %q, want %q", test.kind, got, test.label)
		}
	}
}

func TestTaskSentinelErrorsExposeLocalizationCodes(t *testing.T) {
	tests := map[error]string{
		ErrTaskNotFound: "task_not_found",
		ErrTaskFinished: "task_finished",
		ErrTaskActive:   "task_active",
	}
	for errValue, want := range tests {
		var coded interface{ ErrorCode() string }
		if !errors.As(errValue, &coded) {
			t.Fatalf("%v does not expose an ErrorCode", errValue)
		}
		if got := coded.ErrorCode(); got != want {
			t.Fatalf("%v ErrorCode() = %q, want %q", errValue, got, want)
		}
		if got := ErrorCode(errValue); got != want {
			t.Fatalf("ErrorCode(%v) = %q, want %q", errValue, got, want)
		}
	}
}
