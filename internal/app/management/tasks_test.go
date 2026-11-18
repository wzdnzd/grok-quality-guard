package management

import (
	"testing"
	"time"

	proxyapp "grok-quality-guard/internal/app/proxy"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
)

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
	store      *appstate.Store
	policy     appconfig.Policy
	enabled    bool
	probeQueue ProbeQueueSnapshot
}

func (e taskCenterQueueTestEnvironment) Store() *appstate.Store   { return e.store }
func (e taskCenterQueueTestEnvironment) Policy() appconfig.Policy { return e.policy }
func (e taskCenterQueueTestEnvironment) ConfigEnabled() bool      { return e.enabled }
func (e taskCenterQueueTestEnvironment) ProbeQueueSnapshot() ProbeQueueSnapshot {
	return e.probeQueue
}
func (e taskCenterQueueTestEnvironment) Proxy() *proxyapp.Service { return nil }

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
