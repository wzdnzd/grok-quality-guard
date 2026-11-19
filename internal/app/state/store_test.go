package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
)

const testResinPlatformID = "11111111-1111-1111-1111-111111111111"

func TestStateStorePersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	store := NewStore(path)
	policy := config.PolicyFromConfig(config.DefaultConfig())
	if errLoad := store.Load(policy, "revision-1"); errLoad != nil {
		t.Fatal(errLoad)
	}
	if errUpdate := store.Update(func(state *State) error {
		intent := false
		state.Accounts["auth-1"] = &AccountState{
			AuthID:             "auth-1",
			State:              StateCooling,
			FirstSeenAt:        time.Now().UTC(),
			CooldownUntil:      time.Now().UTC().Add(time.Hour),
			AuthDisabledIntent: &intent,
		}
		state.ResinSnapshots = map[string]proxy.ResinSnapshot{
			"snapshot-1": {AdminURL: "http://resin.test", PlatformID: "platform-1", AdminToken: "admin-token"},
		}
		state.ResinLeaseDeletes = []ResinLeaseDelete{{
			AuthID:        "auth-1",
			AdminURL:      "http://resin.test",
			PlatformID:    "platform-1",
			Account:       "grok-alice-old1",
			SnapshotID:    "snapshot-1",
			CreatedAt:     time.Now().UTC(),
			NextAttemptAt: time.Now().UTC().Add(time.Minute),
		}}
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var persisted State
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("state file is not valid JSON: %v", errUnmarshal)
	}
	if persisted.SchemaVersion != stateSchemaVersion || persisted.Accounts["auth-1"] == nil || len(persisted.ResinLeaseDeletes) != 1 {
		t.Fatalf("unexpected persisted state: %#v", persisted)
	}
	reloaded := NewStore(path)
	if errLoad := reloaded.Load(policy, "revision-2"); errLoad != nil {
		t.Fatal(errLoad)
	}
	snapshot := reloaded.Snapshot()
	account := snapshot.Accounts["auth-1"]
	if account == nil || account.State != StateCooling || account.CooldownUntil.IsZero() || account.AuthDisabledIntent == nil || *account.AuthDisabledIntent {
		t.Fatalf("reloaded account mismatch: %#v", account)
	}
	if len(snapshot.ResinLeaseDeletes) != 1 || snapshot.ResinLeaseDeletes[0].Account != "grok-alice-old1" {
		t.Fatalf("reloaded lease delete queue mismatch: %#v", snapshot.ResinLeaseDeletes)
	}
	matches, errGlob := filepath.Glob(filepath.Join(filepath.Dir(path), ".state-*.tmp"))
	if errGlob != nil {
		t.Fatal(errGlob)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary state files were left behind: %#v", matches)
	}
}

func TestStateStoreTrimsEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	policy := config.PolicyFromConfig(config.DefaultConfig())
	policy.EventsLimit = 3
	if errLoad := store.Load(policy, "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	for i := 0; i < 10; i++ {
		if errUpdate := store.Update(func(state *State) error {
			appendEvent(state, Event{Time: time.Now().UTC(), Action: "test"})
			return nil
		}); errUpdate != nil {
			t.Fatal(errUpdate)
		}
	}
	state := store.Snapshot()
	if len(state.Events) != 3 {
		t.Fatalf("event count = %d, want 3", len(state.Events))
	}
}

func TestStateStoreConcurrentUpdatesRemainConsistent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	if errLoad := store.Load(config.PolicyFromConfig(config.DefaultConfig()), "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	var wait sync.WaitGroup
	for i := 0; i < 32; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_ = store.Update(func(state *State) error {
				appendEvent(state, Event{Time: time.Now().UTC(), AuthID: "auth", Action: "concurrent", Detail: "index"})
				return nil
			})
		}(i)
	}
	wait.Wait()
	state := store.Snapshot()
	if len(state.Events) != 32 {
		t.Fatalf("event count = %d, want 32", len(state.Events))
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var persisted State
	if errUnmarshal := json.Unmarshal(raw, &persisted); errUnmarshal != nil {
		t.Fatalf("state file corrupted: %v", errUnmarshal)
	}
}
func TestStateStoreUpdateIfChangedSkipsNoopWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	if errLoad := store.Load(config.PolicyFromConfig(config.DefaultConfig()), "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	before := store.Snapshot().UpdatedAt
	time.Sleep(2 * time.Millisecond)
	if errUpdate := store.UpdateIfChanged(func(*State) (bool, error) { return false, nil }); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	after := store.Snapshot().UpdatedAt
	if !after.Equal(before) {
		t.Fatalf("no-op update changed UpdatedAt from %s to %s", before, after)
	}
}

func TestStateStorePersistsResinSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	if errLoad := store.Load(config.PolicyFromConfig(config.DefaultConfig()), "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	snapshot := proxy.ResinSnapshot{
		AdminURL:   "http://resin.test",
		PlatformID: testResinPlatformID,
		AdminToken: "admin" + "-" + "token",
	}
	id, errEnsure := store.EnsureResinSnapshot(snapshot)
	if errEnsure != nil {
		t.Fatal(errEnsure)
	}
	reloaded := NewStore(path)
	if errLoad := reloaded.Load(config.PolicyFromConfig(config.DefaultConfig()), "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	got, ok := reloaded.ResinSnapshot(id)
	if !ok || got != snapshot {
		t.Fatalf("reloaded resin snapshot = %#v ok=%t, want %#v", got, ok, snapshot)
	}
}

func TestStateStoreRejectsUnsupportedSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := []byte("{\"schema_version\":2,\"updated_at\":\"2026-01-01T00:00:00Z\",\"policy\":{},\"accounts\":{},\"events\":[]}")
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errLoad := NewStore(path).Load(config.PolicyFromConfig(config.DefaultConfig()), "revision"); errLoad == nil || !strings.Contains(errLoad.Error(), "unsupported state schema version 2") {
		t.Fatalf("unsupported schema load error = %v", errLoad)
	}
}

func TestStateStoreRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := []byte("{\"schema_version\":1,\"updated_at\":\"2026-01-01T00:00:00Z\",\"policy\":{},\"accounts\":{},\"events\":[],\"resin\":{}}")
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errLoad := NewStore(path).Load(config.PolicyFromConfig(config.DefaultConfig()), "revision"); errLoad == nil || !strings.Contains(errLoad.Error(), "unknown field") {
		t.Fatalf("unknown field load error = %v", errLoad)
	}
}

func TestStateStoreAdoptsTaskHistoryDefaultForLegacyPolicy(t *testing.T) {
	path, policy := legacyPolicyState(t, func(rawPolicy map[string]any) {
		delete(rawPolicy, "task_history")
	})
	store := NewStore(path)
	if errLoad := store.Load(policy, "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	history := store.Snapshot().Policy.TaskHistory
	want := config.DefaultTaskHistoryConfig()
	if history != want {
		t.Fatalf("legacy task history = %#v, want %#v", history, want)
	}
}

func TestStateStorePreservesExplicitDisabledTaskHistory(t *testing.T) {
	path, policy := legacyPolicyState(t, func(rawPolicy map[string]any) {
		rawPolicy["task_history"] = map[string]any{"enabled": false}
	})
	store := NewStore(path)
	if errLoad := store.Load(policy, "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	history := store.Snapshot().Policy.TaskHistory
	if history.Enabled || history.RetentionDays != 3 || history.MaxRecords != 500 || history.MaxBytes != 128<<20 || history.MaxItemsPerTask != 10000 {
		t.Fatalf("explicit disabled task history was not preserved: %#v", history)
	}
}

func legacyPolicyState(t *testing.T, mutate func(map[string]any)) (string, config.Policy) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	policy := config.PolicyFromConfig(config.DefaultConfig())
	store := NewStore(path)
	if errLoad := store.Load(policy, "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var envelope map[string]any
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	rawPolicy, ok := envelope["policy"].(map[string]any)
	if !ok {
		t.Fatalf("state policy has unexpected shape: %#v", envelope["policy"])
	}
	mutate(rawPolicy)
	raw, errMarshal := json.MarshalIndent(envelope, "", "  ")
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	return path, policy
}

func TestStateStoreUpdateAccountRollbackAndEventAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	if errLoad := store.Load(config.PolicyFromConfig(config.DefaultConfig()), "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	if errUpdate := store.Update(func(state *State) error {
		state.Accounts["auth-1"] = &AccountState{AuthID: "auth-1", State: StateHealthy}
		state.Accounts["auth-2"] = &AccountState{AuthID: "auth-2", State: StateUnknown}
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	before, _ := store.AccountSnapshot("auth-1")
	wantErr := errors.New("callback failed")
	if errUpdate := store.UpdateAccount("auth-1", func(account *AccountState) (*Event, error) {
		account.State = StateDegraded
		account.LastReason = "temporary"
		return nil, wantErr
	}); !errors.Is(errUpdate, wantErr) {
		t.Fatalf("UpdateAccount() error = %v, want callback error", errUpdate)
	}
	after, _ := store.AccountSnapshot("auth-1")
	if after.State != before.State || after.LastReason != before.LastReason {
		t.Fatalf("account was not rolled back: before=%#v after=%#v", before, after)
	}

	if errUpdate := store.UpdateAccount("auth-1", func(account *AccountState) (*Event, error) {
		account.State = StateHealthy
		return &Event{Action: "observe", Result: "recorded"}, nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	account, _ := store.AccountSnapshot("auth-1")
	if account.State != StateHealthy {
		t.Fatalf("account update was not persisted: %#v", account)
	}
	events := store.EventsSnapshot()
	if len(events) != 1 || events[0].Action != "observe" || events[0].AuthID != "auth-1" {
		t.Fatalf("account event was not appended: %#v", events)
	}

	if errEvent := store.AppendEvent(Event{Action: "internal_error", Result: "error"}); errEvent != nil {
		t.Fatal(errEvent)
	}
	events = store.EventsSnapshot()
	if len(events) != 2 || events[1].Action != "internal_error" {
		t.Fatalf("standalone event was not appended: %#v", events)
	}
}

func TestRawPolicyTaskHistoryProbeSkipsLargeUnrelatedValues(t *testing.T) {
	large := strings.Repeat("x", 128*1024)
	withoutHistory := []byte(`{"accounts":{"auth-1":{"label":"` + large + `"}},"events":[{"detail":{"nested":[1,2,3]}}],"policy":{"events_limit":3}}`)
	if rawPolicyHasTaskHistory(withoutHistory) {
		t.Fatal("task history was reported without a policy task_history key")
	}
	withHistory := []byte(`{"accounts":{},"policy":{"events_limit":3,"task_history":{"enabled":true}}}`)
	if !rawPolicyHasTaskHistory(withHistory) {
		t.Fatal("policy task_history key was not detected")
	}
	if rawPolicyHasTaskHistory([]byte(`{"task_history":{"enabled":true}}`)) {
		t.Fatal("top-level task_history was incorrectly treated as policy")
	}
	if rawPolicyHasTaskHistory([]byte(`{"policy":`)) {
		t.Fatal("malformed state unexpectedly reported task history")
	}
}
