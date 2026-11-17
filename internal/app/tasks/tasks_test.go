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
