package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	appstate "grok-quality-guard/internal/app/state"
	proxy "grok-quality-guard/internal/proxy"
)

func TestCatalogRemovalQueuesResinLeaseDeleteFromSnapshot(t *testing.T) {
	const platformID = "11111111-1111-1111-1111-111111111111"
	var deleted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.Header.Get("Authorization") != "Bearer admin-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.URL.Path != "/api/v1/platforms/"+platformID+"/leases/grok-alice-old1" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		deleted.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	snapshot := proxy.ResinSnapshot{AdminURL: server.URL, PlatformID: platformID, AdminToken: "admin-token"}
	snapshotID, errSnapshot := plugin.stateStore().EnsureResinSnapshot(snapshot)
	if errSnapshot != nil {
		t.Fatal(errSnapshot)
	}
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := plugin.ensureStateAccount(state, "auth-1")
		account.Proxy = proxy.Binding{
			AuthID:         "auth-1",
			SourceID:       "resin-test",
			SourceKind:     proxy.SourceResin,
			SourceRevision: 1,
			Generation:     1,
			Lease: &proxy.LeaseRef{
				Provider: "resin", PlatformID: platformID, Account: "grok-alice-old1", SnapshotID: snapshotID,
			},
		}
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	host.mu.Lock()
	host.entries = nil
	host.mu.Unlock()
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	state := plugin.stateStore().Snapshot()
	if _, exists := state.Accounts["auth-1"]; exists {
		t.Fatalf("removed auth remained in state: %#v", state.Accounts["auth-1"])
	}
	if len(state.ResinLeaseDeletes) != 1 {
		t.Fatalf("queued lease deletes = %d, want 1: %#v", len(state.ResinLeaseDeletes), state.ResinLeaseDeletes)
	}
	job := state.ResinLeaseDeletes[0]
	if job.AuthID != "auth-1" || job.Account != "grok-alice-old1" || job.AdminURL != server.URL || job.PlatformID != platformID || job.SnapshotID != snapshotID {
		t.Fatalf("unexpected lease delete job: %#v", job)
	}
	if deleted.Load() != 0 {
		t.Fatal("catalog refresh deleted the lease synchronously")
	}
	if processed := plugin.proxySvc.ProcessDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != 1 {
		t.Fatalf("processed lease deletes = %d, want 1", processed)
	}
	if deleted.Load() != 1 {
		t.Fatalf("resin delete calls = %d, want 1", deleted.Load())
	}
	if jobs := plugin.stateStore().Snapshot().ResinLeaseDeletes; len(jobs) != 0 {
		t.Fatalf("successful lease delete remained queued: %#v", jobs)
	}
}
