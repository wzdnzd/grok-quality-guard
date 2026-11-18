package management

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	proxyapp "grok-quality-guard/internal/app/proxy"
	appstate "grok-quality-guard/internal/app/state"
	taskapp "grok-quality-guard/internal/app/tasks"
	appconfig "grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
)

type taskDeleteTestEnvironment struct {
	Environment
	deleted string
	err     error
}

func (e *taskDeleteTestEnvironment) DeleteTask(taskID string) error {
	e.deleted = taskID
	return e.err
}

func TestTaskCenterDeleteActionMapsTaskState(t *testing.T) {
	tests := []struct {
		name       string
		taskID     string
		err        error
		wantStatus int
		wantError  string
	}{
		{name: "success", taskID: "task-1", wantStatus: http.StatusOK},
		{name: "missing task id", wantStatus: http.StatusBadRequest, wantError: "api.error.task_id_required"},
		{name: "not found", taskID: "missing", err: taskapp.ErrTaskNotFound, wantStatus: http.StatusNotFound, wantError: "api.error.task_not_found"},
		{name: "active", taskID: "running", err: taskapp.ErrTaskActive, wantStatus: http.StatusConflict, wantError: "api.error.task_active"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := &taskDeleteTestEnvironment{err: test.err}
			service := NewService(env)
			result, status, errAction := service.actionTaskCenterDelete(map[string]any{"task_id": test.taskID})
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d", status, test.wantStatus)
			}
			if test.wantError != "" {
				if errAction == nil || errAction.Error() != test.wantError {
					t.Fatalf("error = %v, want %q", errAction, test.wantError)
				}
				return
			}
			if errAction != nil {
				t.Fatal(errAction)
			}
			deleted, _ := result.(map[string]any)
			if deleted["deleted"] != true || deleted["task_id"] != test.taskID {
				t.Fatalf("delete response = %#v", result)
			}
			if env.deleted != test.taskID {
				t.Fatalf("deleted id = %q, want %q", env.deleted, test.taskID)
			}
		})
	}
}

func TestTaskCenterDeleteActionReturnsInternalError(t *testing.T) {
	service := NewService(&taskDeleteTestEnvironment{err: errors.New("disk unavailable")})
	_, status, errAction := service.actionTaskCenterDelete(map[string]any{"task_id": "task-1"})
	if status != http.StatusInternalServerError || errAction == nil {
		t.Fatalf("unexpected delete error mapping: status=%d err=%v", status, errAction)
	}
}

func TestResidentQueueVisibility(t *testing.T) {
	if shouldShowAutoBindQueue(false, 0, 0, false) {
		t.Fatal("disabled idle auto-bind queue should be hidden")
	}
	if !shouldShowAutoBindQueue(false, 1, 0, false) {
		t.Fatal("auto-bind queue with pending work should be visible")
	}
	if !shouldShowAutoBindQueue(true, 0, 0, false) {
		t.Fatal("enabled auto-bind queue should be visible")
	}
	if shouldShowProxyHealthQueue(true, false) || !shouldShowProxyHealthQueue(true, true) {
		t.Fatal("proxy health queue visibility does not follow require_healthy")
	}
	if shouldShowProxyChangeQueue([]proxy.SourceChange{{Status: "succeeded"}}) {
		t.Fatal("completed proxy synchronization history should not remain resident")
	}
	if !shouldShowProxyChangeQueue([]proxy.SourceChange{{Status: "partial", Counts: proxy.ChangeCounts{Failed: 1}}}) {
		t.Fatal("failed proxy synchronization should remain actionable in the center")
	}
	if shouldShowResinCleanupQueue(0, 0) {
		t.Fatal("idle Resin cleanup queue should be hidden")
	}
	if !shouldShowResinCleanupQueue(1, 0) || !shouldShowResinCleanupQueue(0, 1) {
		t.Fatal("Resin cleanup queue should show pending jobs or active Resin bindings")
	}
}

type taskCenterQueueTestEnvironment struct {
	Environment
	store        *appstate.Store
	policy       appconfig.Policy
	enabled      bool
	probeQueue   ProbeQueueSnapshot
	probeDetail  ProbeQueueDetail
	proxy        *proxyapp.Service
	proxyDetails map[string]proxyapp.ResidentQueueDetail
}

func (e taskCenterQueueTestEnvironment) Store() *appstate.Store   { return e.store }
func (e taskCenterQueueTestEnvironment) Policy() appconfig.Policy { return e.policy }
func (e taskCenterQueueTestEnvironment) ConfigEnabled() bool      { return e.enabled }
func (e taskCenterQueueTestEnvironment) ProbeQueueSnapshot() ProbeQueueSnapshot {
	return e.probeQueue
}
func (e taskCenterQueueTestEnvironment) ProbeQueueDetail() ProbeQueueDetail {
	return e.probeDetail
}
func (e taskCenterQueueTestEnvironment) ProxyQueueDetail(queueID string) (proxyapp.ResidentQueueDetail, bool) {
	if detail, ok := e.proxyDetails[queueID]; ok {
		return detail, true
	}
	if e.proxy == nil {
		return proxyapp.ResidentQueueDetail{}, false
	}
	return e.proxy.QueueDetail(queueID)
}
func (e taskCenterQueueTestEnvironment) Proxy() *proxyapp.Service { return e.proxy }

func TestTaskCenterShowsQualityProbeQueueWithoutProxy(t *testing.T) {
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	policy.TransparentGuard = true
	now := time.Now().UTC()
	service := NewService(taskCenterQueueTestEnvironment{
		store:   appstate.NewStore(""),
		policy:  policy,
		enabled: true,
		probeQueue: ProbeQueueSnapshot{
			Started:       true,
			Waiting:       2494,
			Active:        1,
			Succeeded:     400,
			Failed:        2,
			LastScanAt:    now,
			LastSuccessAt: now.Add(-time.Second),
		},
	})
	queues := service.taskCenterResidentQueues()
	if len(queues) != 1 {
		t.Fatalf("resident queues = %#v, want one quality queue", queues)
	}
	queue := queues[0]
	if queue["id"] != "quality_probe" || queue["category"] != "quality" {
		t.Fatalf("quality queue identity = %#v", queue)
	}
	if queue["status"] != "running" || queue["waiting"] != 2494 || queue["running"] != 1 {
		t.Fatalf("quality queue progress = %#v", queue)
	}
	if queue["completed"] != 402 || queue["total"] != 2897 {
		t.Fatalf("quality queue totals = %#v", queue)
	}
}

func TestTaskCenterResidentQueueMetadataIsLocalized(t *testing.T) {
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	policy.TransparentGuard = true
	service := NewService(taskCenterQueueTestEnvironment{
		store:   appstate.NewStore(""),
		policy:  policy,
		enabled: true,
	})
	english := service.taskCenterResidentQueues("en")
	if len(english) != 1 || english[0]["label"] != "Quality probe queue" {
		t.Fatalf("english queue = %#v", english)
	}
	traditional := service.taskCenterResidentQueues("zh-TW")
	if len(traditional) != 1 || traditional[0]["label"] != "品質偵測佇列" {
		t.Fatalf("traditional queue = %#v", traditional)
	}
}

func TestQualityProbeQueueVisibility(t *testing.T) {
	if shouldShowQualityProbeQueue(false, ProbeQueueSnapshot{}) {
		t.Fatal("disabled idle quality queue should be hidden")
	}
	if !shouldShowQualityProbeQueue(true, ProbeQueueSnapshot{}) {
		t.Fatal("enabled quality queue should be visible while idle")
	}
	if shouldShowQualityProbeQueue(false, ProbeQueueSnapshot{Started: true}) {
		t.Fatal("paused idle quality scheduler should be hidden")
	}
	if !shouldShowQualityProbeQueue(false, ProbeQueueSnapshot{Waiting: 1}) {
		t.Fatal("quality queue with waiting work should remain visible")
	}
}
func TestQualityProbeQueueDoesNotReportIdleBeforeSchedulerStarts(t *testing.T) {
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	policy.TransparentGuard = true
	service := NewService(taskCenterQueueTestEnvironment{
		store:      appstate.NewStore(""),
		policy:     policy,
		enabled:    true,
		probeQueue: ProbeQueueSnapshot{Started: false},
	})
	queues := service.taskCenterResidentQueues()
	if len(queues) != 1 {
		t.Fatalf("resident queues = %#v, want one quality queue", queues)
	}
	if queues[0]["status"] != "starting" || queues[0]["started"] != false {
		t.Fatalf("quality queue startup state = %#v", queues[0])
	}
}

type residentQueueProxyTestEnvironment struct {
	proxyapp.Environment
	store *appstate.Store
}

func (e residentQueueProxyTestEnvironment) ProxyStore() *appstate.Store { return e.store }

func TestResidentQueueCardsUseLiveProxyWorkCounts(t *testing.T) {
	now := time.Now().UTC()
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	store := appstate.NewStore("")
	store.UpdateStateForTest(func(value *appstate.State) {
		value.Proxy.AutoBind.Enabled = true
		value.Proxy.AutoBind.RequireHealthy = true
	})
	proxyService := proxyapp.NewService(residentQueueProxyTestEnvironment{store: store})
	service := NewService(taskCenterQueueTestEnvironment{
		store:   store,
		policy:  policy,
		enabled: true,
		proxy:   proxyService,
		proxyDetails: map[string]proxyapp.ResidentQueueDetail{
			"auto_bind": {
				Active:  []proxyapp.ResidentQueueEntry{{ID: "auth-run", StartedAt: now}},
				Waiting: []proxyapp.ResidentQueueEntry{{ID: "auth-wait", QueuedAt: now}},
			},
			"proxy_health": {
				Active:  []proxyapp.ResidentQueueEntry{{ID: "source-run", StartedAt: now}},
				Waiting: []proxyapp.ResidentQueueEntry{{ID: "source-wait", QueuedAt: now}},
			},
		},
	})
	queues := service.taskCenterResidentQueues()
	byID := make(map[string]map[string]any, len(queues))
	for _, queue := range queues {
		id, _ := queue["id"].(string)
		byID[id] = queue
	}
	for _, id := range []string{"auto_bind", "proxy_health"} {
		queue, ok := byID[id]
		if !ok {
			t.Fatalf("%s queue missing from %#v", id, queues)
		}
		if queue["status"] != "running" || queue["running"] != 1 || queue["waiting"] != 1 || queue["total"] != 2 {
			t.Fatalf("%s live counts = %#v", id, queue)
		}
	}
}

func TestResidentQueueCopyHasNoTerminalPunctuation(t *testing.T) {
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	policy.TransparentGuard = true
	store := appstate.NewStore("")
	store.UpdateStateForTest(func(value *appstate.State) {
		value.Proxy.AutoBind.Enabled = true
		value.Proxy.AutoBind.RequireHealthy = true
		value.Proxy.AutoBind.HealthMaxAge = "10m"
		value.Proxy.Changes = []proxy.SourceChange{{
			ID:     "change-1",
			Status: "running",
			Counts: proxy.ChangeCounts{Total: 1, Pending: 1},
		}}
		value.ResinLeaseDeletes = []appstate.ResinLeaseDelete{{
			AuthID:     "auth-1",
			Account:    "account-1",
			SnapshotID: "snapshot-1",
			Status:     "pending",
		}}
	})
	proxyService := proxyapp.NewService(residentQueueProxyTestEnvironment{store: store})
	service := NewService(taskCenterQueueTestEnvironment{
		store:      store,
		policy:     policy,
		enabled:    true,
		probeQueue: ProbeQueueSnapshot{Started: true, Waiting: 1},
		proxy:      proxyService,
	})
	queues := service.taskCenterResidentQueues()
	if len(queues) != 5 {
		t.Fatalf("resident queues = %d, want 5: %#v", len(queues), queues)
	}
	for _, queue := range queues {
		id, _ := queue["id"].(string)
		for _, key := range []string{"description", "detail"} {
			value, _ := queue[key].(string)
			if residentQueueCopyEndsWithPunctuation(value) {
				t.Fatalf("%s %s ends with punctuation: %q", id, key, value)
			}
		}
	}
}

func residentQueueCopyEndsWithPunctuation(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	var last rune
	for _, char := range value {
		last = char
	}
	return strings.ContainsRune("。！？…；：，、,.!?;:", last)
}
func TestTaskCenterQueueDetailPagesQualityProbeWork(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(taskCenterQueueTestEnvironment{
		store: appstate.NewStore(""),
		probeDetail: ProbeQueueDetail{
			Active: []ProbeQueueEntry{{AuthID: "oauth-active", Status: "running", StartedAt: now}},
			Waiting: []ProbeQueueEntry{
				{AuthID: "oauth-wait-1", Status: "waiting", QueuedAt: now.Add(time.Second)},
				{AuthID: "oauth-wait-2", Status: "waiting", QueuedAt: now.Add(2 * time.Second)},
				{AuthID: "oauth-wait-3", Status: "waiting", QueuedAt: now.Add(3 * time.Second)},
			},
		},
	})
	result, status, err := service.actionTaskCenterQueue(map[string]any{"queue_id": "quality_probe", "page": 2, "page_size": 1})
	if err != nil || status != http.StatusOK {
		t.Fatalf("queue detail status = %d err=%v", status, err)
	}
	detail, ok := result.(taskCenterQueueDetail)
	if !ok {
		t.Fatalf("queue detail type = %T", result)
	}
	if detail.QueueID != "quality_probe" || len(detail.Active) != 1 || detail.Active[0].AuthID != "oauth-active" {
		t.Fatalf("active queue detail = %#v", detail)
	}
	if detail.Waiting.Total != 3 || detail.Waiting.Page != 2 || len(detail.Waiting.Items) != 1 || detail.Waiting.Items[0].AuthID != "oauth-wait-2" {
		t.Fatalf("waiting queue page = %#v", detail.Waiting)
	}
}

func TestTaskCenterQueueDetailIncludesAutoBindAndProxyHealthWork(t *testing.T) {
	now := time.Now().UTC()
	store := appstate.NewStore("")
	proxyService := proxyapp.NewService(residentQueueProxyTestEnvironment{store: store})
	service := NewService(taskCenterQueueTestEnvironment{
		store: store,
		proxy: proxyService,
		proxyDetails: map[string]proxyapp.ResidentQueueDetail{
			"auto_bind": {
				Active:  []proxyapp.ResidentQueueEntry{{ID: "auth-run", Status: "running", StartedAt: now}},
				Waiting: []proxyapp.ResidentQueueEntry{{ID: "auth-wait", Status: "waiting", QueuedAt: now.Add(-time.Second)}},
			},
			"proxy_health": {
				Active:  []proxyapp.ResidentQueueEntry{{ID: "source-run", Status: "running", StartedAt: now}},
				Waiting: []proxyapp.ResidentQueueEntry{{ID: "source-wait", Status: "waiting", QueuedAt: now.Add(-time.Second)}},
			},
		},
	})
	tests := []struct {
		queueID   string
		activeID  string
		waitingID string
		sourceID  bool
	}{
		{queueID: "auto_bind", activeID: "auth-run", waitingID: "auth-wait"},
		{queueID: "proxy_health", activeID: "source-run", waitingID: "source-wait", sourceID: true},
	}
	for _, test := range tests {
		result, status, err := service.actionTaskCenterQueue(map[string]any{"queue_id": test.queueID})
		if err != nil || status != http.StatusOK {
			t.Fatalf("%s queue detail status = %d err=%v", test.queueID, status, err)
		}
		detail, ok := result.(taskCenterQueueDetail)
		if !ok {
			t.Fatalf("%s queue detail type = %T", test.queueID, result)
		}
		if len(detail.Active) != 1 || detail.Active[0].ID != test.activeID {
			t.Fatalf("%s active detail = %#v", test.queueID, detail.Active)
		}
		if len(detail.Waiting.Items) != 1 || detail.Waiting.Items[0].ID != test.waitingID {
			t.Fatalf("%s waiting detail = %#v", test.queueID, detail.Waiting)
		}
		if test.sourceID && detail.Active[0].SourceID != test.activeID {
			t.Fatalf("%s active source_id = %#v", test.queueID, detail.Active[0])
		}
		if !test.sourceID && detail.Active[0].AuthID != test.activeID {
			t.Fatalf("%s active auth_id = %#v", test.queueID, detail.Active[0])
		}
	}
}

type taskCancelTestEnvironment struct {
	Environment
	task taskapp.Task
	err  error
}

func (e *taskCancelTestEnvironment) CancelTask(string) (taskapp.Task, error) {
	return e.task, e.err
}

func TestTaskCancelActionMapsSentinelErrors(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantMessage string
	}{
		{name: "not found", err: taskapp.ErrTaskNotFound, wantStatus: http.StatusNotFound, wantCode: "task_not_found", wantMessage: "Task not found"},
		{name: "already finished", err: taskapp.ErrTaskFinished, wantStatus: http.StatusConflict, wantCode: "task_finished", wantMessage: "The task has already finished and cannot be cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(&taskCancelTestEnvironment{err: test.err})
			_, status, errAction := service.actionTaskCancel(map[string]any{"task_id": "task-1"}, "en")
			if status != test.wantStatus {
				t.Fatalf("status = %d, want %d", status, test.wantStatus)
			}
			coded, ok := errAction.(interface{ ErrorCode() string })
			if !ok || coded.ErrorCode() != test.wantCode {
				t.Fatalf("error code = %#v, want %q", errAction, test.wantCode)
			}
			if message := localizedErrorMessage("en", test.wantCode, test.wantStatus, "", nil); message != test.wantMessage {
				t.Fatalf("localized message = %q, want %q", message, test.wantMessage)
			}
		})
	}
}
