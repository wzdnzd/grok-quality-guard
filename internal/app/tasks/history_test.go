package tasks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultHistoryConfig(t *testing.T) {
	config := DefaultHistoryConfig()
	if !config.Enabled || config.RetentionDays != 3 || config.MaxRecords != 500 || config.MaxBytes != 128<<20 || config.MaxItemsPerTask != 10000 {
		t.Fatalf("unexpected default history config: %#v", config)
	}
}

func TestHistoryDisabledKeepsFinalResultOnlyInShortLivedMemory(t *testing.T) {
	manager := NewManager()
	if err := manager.ConfigureHistory(t.TempDir(), HistoryConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	task := manager.Start("test.disabled", []string{"auth-1"}, "running", func(context.Context) (any, error) {
		return map[string]any{"items": []map[string]any{{"auth_id": "auth-1", "status": "succeeded"}}}, nil
	})
	manager.Wait()
	final, ok := manager.ByID(task.ID)
	if !ok || final.Status != taskStatusSucceeded {
		t.Fatalf("final task was not retained for the completion waiter: %#v ok=%t", final, ok)
	}
	center, err := manager.ListCenter(TaskCenterFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if center.Total != 0 || len(center.Items) != 0 {
		t.Fatalf("disabled history leaked completed tasks into the center: %#v", center)
	}
	if _, err := os.Stat(filepath.Join(manager.historyDir, "archive")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled history created an archive directory: %v", err)
	}
}

func TestHistoryDisabledDuringTaskDiscardsActiveArchive(t *testing.T) {
	manager := NewManager()
	base := t.TempDir()
	if err := manager.ConfigureHistory(base, DefaultHistoryConfig()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	task := manager.Start("test.disable_midflight", []string{"auth-1"}, "running", func(context.Context) (any, error) {
		close(started)
		<-release
		return map[string]any{"items": []map[string]any{{"auth_id": "auth-1", "status": "succeeded"}}}, nil
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not start")
	}
	if err := manager.ConfigureHistory(t.TempDir(), HistoryConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	close(release)
	manager.Wait()

	final, ok := manager.ByID(task.ID)
	if !ok || final.Status != taskStatusSucceeded {
		t.Fatalf("final result was not retained for the waiter: %#v ok=%t", final, ok)
	}
	center, err := manager.ListCenter(TaskCenterFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if center.Total != 0 || len(center.Items) != 0 {
		t.Fatalf("disabled history leaked a mid-flight task into the center: %#v", center)
	}
	active, err := os.ReadDir(filepath.Join(base, "task-center", "active"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("active history files were not removed: %#v", active)
	}
}
func TestHistoryArchivePaginationFilteringAndSummaryHeader(t *testing.T) {
	manager := NewManager()
	if err := manager.ConfigureHistory(t.TempDir(), DefaultHistoryConfig()); err != nil {
		t.Fatal(err)
	}
	task := manager.Start("accounts.probe_selected", []string{"auth-1", "auth-2", "auth-3"}, "running", func(context.Context) (any, error) {
		return map[string]any{
			"count": 3,
			"items": []map[string]any{
				{"auth_id": "auth-1", "status": "probed"},
				{"auth_id": "auth-2", "status": "failed", "error": "upstream failed"},
				{"auth_id": "auth-3", "status": "skipped"},
			},
		}, nil
	})
	manager.Wait()

	center, err := manager.ListCenter(TaskCenterFilter{Category: "account"})
	if err != nil {
		t.Fatal(err)
	}
	if center.Total != 1 || center.Counts["failed_items"] != 1 || center.Counts["skipped_items"] != 1 {
		t.Fatalf("unexpected center result: %#v", center)
	}
	if center.History.RecordCount != 1 || center.History.ArchiveBytes <= 0 {
		t.Fatalf("unexpected history stats: %#v", center.History)
	}

	failed, err := manager.TaskDetail(task.ID, 1, 20, "failed")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Total != 1 || failed.ItemTotal != 3 || len(failed.Items) != 1 || failed.Source != "history" {
		t.Fatalf("unexpected failed detail: %#v", failed)
	}
	firstPage, err := manager.TaskDetail(task.ID, 1, 1, "all")
	if err != nil {
		t.Fatal(err)
	}
	if firstPage.Total != 3 || firstPage.ItemTotal != 3 || len(firstPage.Items) != 1 {
		t.Fatalf("unexpected first page: %#v", firstPage)
	}
	lastPage, err := manager.TaskDetail(task.ID, 3, 1, "all")
	if err != nil {
		t.Fatal(err)
	}
	if lastPage.Total != 3 || lastPage.Page != 3 || len(lastPage.Items) != 1 {
		t.Fatalf("unexpected last page: %#v", lastPage)
	}

	archives, err := filepath.Glob(filepath.Join(manager.historyDir, "archive", "*", "*.json.gz"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archive files = %#v err=%v", archives, err)
	}
	archived, items, err := readTaskArchive(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("archive item lines = %d, want 3", len(items))
	}
	result, _ := archived.Result.(map[string]any)
	if _, duplicated := result["items"]; duplicated {
		t.Fatalf("archive header duplicated item payload: %#v", archived.Result)
	}
}

func TestDeleteTaskRemovesHistoryArchiveAndDetails(t *testing.T) {
	manager := NewManager()
	if err := manager.ConfigureHistory(t.TempDir(), DefaultHistoryConfig()); err != nil {
		t.Fatal(err)
	}
	task := manager.Start("accounts.probe_selected", []string{"auth-1", "auth-2"}, "running", func(context.Context) (any, error) {
		return map[string]any{"items": []map[string]any{
			{"auth_id": "auth-1", "status": "succeeded"},
			{"auth_id": "auth-2", "status": "failed", "error": "boom"},
		}}, nil
	})
	manager.Wait()
	archives, errGlob := filepath.Glob(filepath.Join(manager.historyDir, "archive", "*", "*.json.gz"))
	if errGlob != nil || len(archives) != 1 {
		t.Fatalf("archive files = %#v err=%v", archives, errGlob)
	}
	if errDelete := manager.DeleteTask(task.ID); errDelete != nil {
		t.Fatal(errDelete)
	}
	center, errCenter := manager.ListCenter(TaskCenterFilter{})
	if errCenter != nil {
		t.Fatal(errCenter)
	}
	if center.Total != 0 || center.History.RecordCount != 0 || center.History.ArchiveBytes != 0 {
		t.Fatalf("deleted task remained in the center: %#v", center)
	}
	if _, errDetail := manager.TaskDetail(task.ID, 1, 20, "all"); !errors.Is(errDetail, ErrTaskNotFound) {
		t.Fatalf("deleted task detail error = %v, want ErrTaskNotFound", errDetail)
	}
	if _, errStat := os.Stat(archives[0]); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("deleted archive still exists: %v", errStat)
	}
}

func TestHistoryTruncatesPerTaskItems(t *testing.T) {
	config := DefaultHistoryConfig()
	config.MaxItemsPerTask = 2
	manager := NewManager()
	if err := manager.ConfigureHistory(t.TempDir(), config); err != nil {
		t.Fatal(err)
	}
	task := manager.Start("proxy.bind", []string{"a", "b", "c"}, "running", func(context.Context) (any, error) {
		return map[string]any{"items": []map[string]any{
			{"auth_id": "a", "status": "bound"},
			{"auth_id": "b", "status": "bound"},
			{"auth_id": "c", "status": "failed", "error": "boom"},
		}}, nil
	})
	manager.Wait()
	detail, err := manager.TaskDetail(task.ID, 1, 20, "all")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Task.ItemCount != 3 || detail.Task.ItemsStored != 2 || !detail.Task.ItemsTruncated || detail.ItemTotal != 2 {
		t.Fatalf("unexpected truncated detail: %#v", detail)
	}
}

func TestReportItemUpdatesRunningTaskAndOutcomeCounts(t *testing.T) {
	manager := NewManager()
	if err := manager.ConfigureHistory(t.TempDir(), DefaultHistoryConfig()); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	task := manager.Start("test.queue", []string{"a", "b"}, "running", func(ctx context.Context) (any, error) {
		return nil, RunBoundedQueue(ctx, []int{0, 1}, 2, 1, func(jobCtx context.Context, index int) {
			item := map[string]any{"auth_id": []string{"a", "b"}[index], "status": "succeeded"}
			if index == 1 {
				item["status"] = "failed"
				item["error"] = "expected failure"
			}
			ReportItem(jobCtx, item)
			if index == 0 {
				close(started)
				<-release
			}
		})
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not report its first item")
	}
	running, err := manager.TaskDetail(task.ID, 1, 20, "all")
	if err != nil {
		t.Fatal(err)
	}
	if running.Task.Status != taskStatusRunning || running.ItemTotal != 1 || running.Total != 1 {
		t.Fatalf("unexpected running detail: %#v", running)
	}
	close(release)
	manager.Wait()
	failed, err := manager.TaskDetail(task.ID, 1, 20, "failed")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Total != 1 || failed.Task.Failed != 1 || failed.Task.Succeeded != 1 || failed.Task.Completed != 2 {
		t.Fatalf("unexpected completed counts: %#v", failed)
	}
}

func TestHistoryKeepsStreamedItemsWhenFinalResultIsBounded(t *testing.T) {
	manager := NewManager()
	if err := manager.ConfigureHistory(t.TempDir(), DefaultHistoryConfig()); err != nil {
		t.Fatal(err)
	}
	task := manager.Start("proxy.bind", []string{"a", "b", "c", "d"}, "running", func(ctx context.Context) (any, error) {
		items := make([]map[string]any, 0, 4)
		for _, authID := range []string{"a", "b", "c", "d"} {
			item := map[string]any{"auth_id": authID, "status": "bound"}
			items = append(items, item)
			ReportItem(ctx, item)
		}
		// Simulate a bounded task response that only returns a sample.
		return map[string]any{"count": 4, "items": items[:2], "items_truncated": true}, nil
	})
	manager.Wait()
	detail, err := manager.TaskDetail(task.ID, 1, 20, "all")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Task.ItemCount != 4 || detail.Task.ItemsStored != 4 || !detail.Task.ItemsTruncated || detail.ItemTotal != 4 {
		t.Fatalf("bounded final result replaced streamed history: %#v", detail)
	}
}
func TestHistoryCleanupByRetentionRecordsAndBytes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	t.Run("retention", func(t *testing.T) {
		base := t.TempDir()
		writeHistoryFixture(t, base, Task{ID: "new", Kind: "test", Status: taskStatusSucceeded, CreatedAt: now.Add(-time.Hour), EndedAt: now.Add(-time.Hour)})
		writeHistoryFixture(t, base, Task{ID: "old", Kind: "test", Status: taskStatusSucceeded, CreatedAt: now.Add(-5 * 24 * time.Hour), EndedAt: now.Add(-5 * 24 * time.Hour)})
		manager := NewManager()
		if err := manager.ConfigureHistory(base, DefaultHistoryConfig()); err != nil {
			t.Fatal(err)
		}
		center, err := manager.ListCenter(TaskCenterFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if center.Total != 1 || center.Items[0].ID != "new" {
			t.Fatalf("retention cleanup kept the wrong records: %#v", center.Items)
		}
	})

	t.Run("records", func(t *testing.T) {
		base := t.TempDir()
		for index, id := range []string{"oldest", "middle", "newest"} {
			ended := now.Add(time.Duration(index-3) * time.Hour)
			writeHistoryFixture(t, base, Task{ID: id, Kind: "test", Status: taskStatusSucceeded, CreatedAt: ended, EndedAt: ended})
		}
		config := DefaultHistoryConfig()
		config.MaxRecords = 2
		manager := NewManager()
		if err := manager.ConfigureHistory(base, config); err != nil {
			t.Fatal(err)
		}
		center, err := manager.ListCenter(TaskCenterFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if center.Total != 2 || center.Items[0].ID != "newest" || center.Items[1].ID != "middle" {
			t.Fatalf("record cleanup kept the wrong entries: %#v", center.Items)
		}
		raw, err := os.ReadFile(filepath.Join(manager.historyDir, "index.jsonl"))
		if err != nil || len(strings.Split(strings.TrimSpace(string(raw)), "\n")) != 2 {
			t.Fatalf("rewritten index mismatch: %s err=%v", raw, err)
		}
		matches, _ := filepath.Glob(filepath.Join(manager.historyDir, ".index-*.tmp"))
		if len(matches) != 0 {
			t.Fatalf("temporary index files were left behind: %#v", matches)
		}
	})

	t.Run("bytes", func(t *testing.T) {
		base := t.TempDir()
		writeHistoryFixture(t, base, Task{ID: "old-large", Kind: "test", Status: taskStatusSucceeded, CreatedAt: now.Add(-time.Hour), EndedAt: now.Add(-time.Hour), Result: map[string]any{"message": strings.Repeat("x", 4096)}})
		writeHistoryFixture(t, base, Task{ID: "new-large", Kind: "test", Status: taskStatusSucceeded, CreatedAt: now, EndedAt: now, Result: map[string]any{"message": strings.Repeat("y", 4096)}})
		config := DefaultHistoryConfig()
		config.MaxBytes = 1
		manager := NewManager()
		if err := manager.ConfigureHistory(base, config); err != nil {
			t.Fatal(err)
		}
		center, err := manager.ListCenter(TaskCenterFilter{})
		if err != nil {
			t.Fatal(err)
		}
		if center.Total != 0 {
			t.Fatalf("byte cleanup kept archives over the cap: %#v", center.Items)
		}
	})
}

func TestRecoverActiveTaskWhenHistoryBecomesEnabled(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "task-center")
	if err := os.MkdirAll(filepath.Join(dir, "active"), 0o700); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute)
	meta := Task{ID: "interrupted-1", Kind: "accounts.probe_selected", Status: taskStatusRunning, CreatedAt: started, StartedAt: started, UpdatedAt: started, Total: 2, Completed: 1, Running: 1, Succeeded: 1, AuthIDs: []string{"a", "b"}, AuthCount: 2}
	if err := writeJSONFile(filepath.Join(dir, "active", "interrupted-1.json"), meta); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "active", "interrupted-1.items.jsonl"), []byte("{\"auth_id\":\"a\",\"status\":\"succeeded\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	if err := manager.ConfigureHistory(base, HistoryConfig{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.ByID("interrupted-1"); ok {
		t.Fatal("disabled history recovered an active task")
	}
	if err := manager.ConfigureHistory(base, DefaultHistoryConfig()); err != nil {
		t.Fatal(err)
	}
	recovered, ok := manager.ByID("interrupted-1")
	if !ok || recovered.Status != taskStatusInterrupted || recovered.EndedAt.IsZero() {
		t.Fatalf("recovered task = %#v ok=%t", recovered, ok)
	}
	if recovered.ItemCount != 1 || recovered.ItemsStored != 1 {
		t.Fatalf("recovered item metadata = %#v", recovered)
	}
}

func TestHistoryCleanupRunsWhenCenterIsOpenedLater(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	base := t.TempDir()
	writeHistoryFixture(t, base, Task{ID: "expired", Kind: "test", Status: taskStatusSucceeded, CreatedAt: now, EndedAt: now})
	manager := NewManager()
	if err := manager.ConfigureHistory(base, DefaultHistoryConfig()); err != nil {
		t.Fatal(err)
	}
	center, err := manager.ListCenter(TaskCenterFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if center.Total != 1 {
		t.Fatalf("initial history records = %d, want 1", center.Total)
	}
	manager.cleanupHistoryIfDue(now.Add(4 * 24 * time.Hour))
	center, err = manager.ListCenter(TaskCenterFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if center.Total != 0 || center.History.RecordCount != 0 {
		t.Fatalf("expired history was not cleaned lazily: %#v", center)
	}
}
func writeHistoryFixture(t *testing.T, base string, task Task) {
	t.Helper()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	if task.EndedAt.IsZero() {
		task.EndedAt = task.CreatedAt
	}
	if task.Status == "" {
		task.Status = taskStatusSucceeded
	}
	archiveDir := filepath.Join(base, "task-center", "archive", task.EndedAt.Format("2006-01"))
	if err := os.MkdirAll(archiveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(archiveDir, task.ID+".json.gz")
	if err := writeTaskArchive(archive, task, filepath.Join(base, "missing-items.jsonl")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := appendHistoryIndex(filepath.Join(base, "task-center"), historyEntry{Task: HistorySummary(task), Archive: archive, ArchiveBytes: info.Size()}); err != nil {
		t.Fatal(err)
	}
}
