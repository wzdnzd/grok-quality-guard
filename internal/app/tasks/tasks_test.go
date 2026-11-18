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
	if cancelled.Status != taskStatusRunning || cancelled.Progress != "正在取消任务" {
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

func TestTaskKindsAreGroupedAndLabeledForTheTaskCenter(t *testing.T) {
	tests := []struct {
		kind     string
		category string
		label    string
	}{
		{kind: "accounts.probe_selected", category: "account", label: "批量检测账号"},
		{kind: "proxy.sources.test_selected", category: "proxy", label: "批量检测代理"},
		{kind: "resin.cleanup.retry", category: "proxy", label: "任务"},
		{kind: "quality.cross_verify", category: "quality", label: "质量交叉验证"},
		{kind: "system.cleanup", category: "system", label: "任务"},
	}
	for _, test := range tests {
		if got := TaskCategory(test.kind); got != test.category {
			t.Fatalf("TaskCategory(%q) = %q, want %q", test.kind, got, test.category)
		}
		if got := TaskLabel(test.kind); got != test.label {
			t.Fatalf("TaskLabel(%q) = %q, want %q", test.kind, got, test.label)
		}
	}
}
