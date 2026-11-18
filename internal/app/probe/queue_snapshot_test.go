package probe

import (
	"context"
	"errors"
	"testing"
	"time"

	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
)

func TestSchedulerSnapshotTracksWaitingAndCompletions(t *testing.T) {
	now := time.Now().UTC()
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	scheduler := newProbeScheduler(nil, context.Background())
	accounts := []Account{{ID: "auth-1"}, {ID: "auth-2"}}
	state := State{Accounts: map[string]*AccountState{
		"auth-1": {AuthID: "auth-1", State: StateUnknown, FirstSeenAt: now.Add(-time.Hour)},
		"auth-2": {AuthID: "auth-2", State: StateUnknown, FirstSeenAt: now.Add(-time.Hour)},
	}}
	if added := scheduler.enqueueDue(accounts, state, policy, now); added != 2 {
		t.Fatalf("enqueued = %d, want 2", added)
	}
	snapshot := scheduler.snapshot()
	if !snapshot.Started || snapshot.Waiting != 2 || snapshot.Active != 0 {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}
	if snapshot.LastScanAt.IsZero() {
		t.Fatal("last scan time was not recorded")
	}

	scheduler.mu.Lock()
	scheduler.queue = scheduler.queue[1:]
	scheduler.active = 1
	scheduler.mu.Unlock()
	scheduler.finish(probeCompletion{authID: "auth-1"})

	snapshot = scheduler.snapshot()
	if snapshot.Waiting != 1 || snapshot.Active != 0 || snapshot.Succeeded != 1 || snapshot.Failed != 0 {
		t.Fatalf("snapshot after success = %#v", snapshot)
	}
	if snapshot.LastSuccessAt.IsZero() {
		t.Fatal("last success time was not recorded")
	}

	scheduler.mu.Lock()
	scheduler.queue = nil
	scheduler.active = 1
	scheduler.mu.Unlock()
	scheduler.finish(probeCompletion{authID: "auth-2", err: errors.New("probe timeout")})

	snapshot = scheduler.snapshot()
	if snapshot.Waiting != 0 || snapshot.Active != 0 || snapshot.Succeeded != 1 || snapshot.Failed != 1 {
		t.Fatalf("snapshot after failure = %#v", snapshot)
	}
	if snapshot.LastError != "probe timeout" {
		t.Fatalf("last error = %q, want probe timeout", snapshot.LastError)
	}
	if snapshot.LastErrorCode != "operation_failed" {
		t.Fatalf("last error code = %q, want operation_failed", snapshot.LastErrorCode)
	}
}

type blockingReconcileProbeEnvironment struct {
	Environment
	store   *appstate.Store
	policy  appconfig.Policy
	started chan struct{}
}

func (e *blockingReconcileProbeEnvironment) Store() *appstate.Store { return e.store }
func (e *blockingReconcileProbeEnvironment) Policy() appconfig.Policy {
	return e.policy
}
func (e *blockingReconcileProbeEnvironment) CatalogSnapshot() []Account { return nil }
func (e *blockingReconcileProbeEnvironment) Reconcile(ctx context.Context) error {
	select {
	case e.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestProbeSchedulerStartsWhileReconcileIsBlocked(t *testing.T) {
	env := &blockingReconcileProbeEnvironment{
		store:   appstate.NewStore(""),
		policy:  appconfig.PolicyFromConfig(appconfig.DefaultConfig()),
		started: make(chan struct{}, 1),
	}
	service := NewService(env)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		service.probeLoop(ctx)
		close(done)
	}()

	deadline := time.After(time.Second)
	for {
		snapshot := service.QueueSnapshot()
		if snapshot.Started && !snapshot.LastScanAt.IsZero() {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatalf("scheduler did not start while reconcile was blocked: %#v", snapshot)
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case <-env.started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("reconcile was not started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probe loop did not stop")
	}
}

func TestSchedulerDeduplicatesQueuedAndActiveAccounts(t *testing.T) {
	now := time.Now().UTC()
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	scheduler := newProbeScheduler(nil, context.Background())
	accounts := []Account{{ID: "auth-1"}}
	state := State{Accounts: map[string]*AccountState{
		"auth-1": {AuthID: "auth-1", State: StateUnknown, FirstSeenAt: now.Add(-time.Hour)},
	}}
	if added := scheduler.enqueueDue(accounts, state, policy, now); added != 1 {
		t.Fatalf("first enqueue added = %d, want 1", added)
	}
	if added := scheduler.enqueueDue(accounts, state, policy, now); added != 0 {
		t.Fatalf("duplicate waiting enqueue added = %d, want 0", added)
	}
	scheduler.mu.Lock()
	scheduler.queue = nil
	scheduler.active = 1
	scheduler.mu.Unlock()
	if added := scheduler.enqueueDue(accounts, state, policy, now); added != 0 {
		t.Fatalf("active duplicate enqueue added = %d, want 0", added)
	}
	scheduler.finish(probeCompletion{authID: "auth-1"})
	if added := scheduler.enqueueDue(accounts, state, policy, now); added != 1 {
		t.Fatalf("enqueue after completion added = %d, want 1", added)
	}
}

func TestProbeScanIntervalUsesPolicy(t *testing.T) {
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	policy.Probe.ScanInterval = appconfig.NewDuration(7 * time.Second)
	service := NewService(&blockingReconcileProbeEnvironment{
		store:  appstate.NewStore(""),
		policy: policy,
	})
	if got := service.probeScanInterval(); got != 7*time.Second {
		t.Fatalf("probe scan interval = %s, want 7s", got)
	}
}
func TestServiceQueueDetailReportsActiveAndWaitingAuthIDs(t *testing.T) {
	now := time.Now().UTC()
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	service := &Service{}
	scheduler := newProbeScheduler(nil, context.Background())
	service.setScheduler(scheduler)
	accounts := []Account{{ID: "auth-active"}, {ID: "auth-wait-1"}, {ID: "auth-wait-2"}}
	state := State{Accounts: map[string]*AccountState{
		"auth-active": {AuthID: "auth-active", State: StateUnknown, FirstSeenAt: now.Add(-time.Hour)},
		"auth-wait-1": {AuthID: "auth-wait-1", State: StateUnknown, FirstSeenAt: now.Add(-time.Hour)},
		"auth-wait-2": {AuthID: "auth-wait-2", State: StateUnknown, FirstSeenAt: now.Add(-time.Hour)},
	}}
	if added := scheduler.enqueueDue(accounts, state, policy, now); added != 3 {
		t.Fatalf("enqueued = %d, want 3", added)
	}
	scheduler.mu.Lock()
	active := scheduler.queue[0]
	scheduler.queue = scheduler.queue[1:]
	scheduler.active = 1
	scheduler.activeIDs[active] = now
	scheduler.mu.Unlock()

	detail := service.QueueDetail()
	if len(detail.Active) != 1 || detail.Active[0].AuthID != "auth-active" {
		t.Fatalf("active detail = %#v, want auth-active", detail.Active)
	}
	if len(detail.Waiting) != 2 || detail.Waiting[0].AuthID != "auth-wait-1" || detail.Waiting[1].AuthID != "auth-wait-2" {
		t.Fatalf("waiting detail = %#v, want wait order", detail.Waiting)
	}
	if detail.Active[0].StartedAt.IsZero() || detail.Waiting[0].QueuedAt.IsZero() {
		t.Fatalf("detail timestamps missing: %#v", detail)
	}
}
