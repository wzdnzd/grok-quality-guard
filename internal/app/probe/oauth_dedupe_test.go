package probe

import (
	"context"
	"testing"
	"time"

	appconfig "grok-quality-guard/internal/config"
)

func TestSchedulerDeduplicatesAccountsByOAuthID(t *testing.T) {
	now := time.Now().UTC()
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	scheduler := newProbeScheduler(nil, context.Background())
	accounts := []Account{
		{ID: "auth-file-a", OAuthID: "oauth-sub-1"},
		{ID: "auth-file-b", OAuthID: "oauth-sub-1"},
		{ID: "auth-file-c", OAuthID: "oauth-sub-2"},
	}
	state := State{Accounts: map[string]*AccountState{}}
	for _, account := range accounts {
		state.Accounts[account.ID] = &AccountState{AuthID: account.ID, State: StateUnknown, FirstSeenAt: now.Add(-time.Hour)}
	}
	if added := scheduler.enqueueDue(accounts, state, policy, now); added != 2 {
		t.Fatalf("OAuth ID dedupe enqueued = %d, want 2", added)
	}
	scheduler.mu.Lock()
	if _, exists := scheduler.queued["oauth:oauth-sub-1"]; !exists {
		scheduler.mu.Unlock()
		t.Fatal("first OAuth identity was not queued")
	}
	if _, exists := scheduler.queued["oauth:oauth-sub-2"]; !exists {
		scheduler.mu.Unlock()
		t.Fatal("second OAuth identity was not queued")
	}
	scheduler.queue = scheduler.queue[1:]
	scheduler.active = 1
	scheduler.mu.Unlock()
	scheduler.finish(probeCompletion{authID: "auth-file-a", queueKey: "oauth:oauth-sub-1"})
	if added := scheduler.enqueueDue(accounts, state, policy, now); added != 1 {
		t.Fatalf("OAuth ID re-enqueue after completion added = %d, want 1", added)
	}
}
