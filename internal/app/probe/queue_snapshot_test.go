package probe

import (
	"context"
	"errors"
	"testing"
	"time"

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
}
