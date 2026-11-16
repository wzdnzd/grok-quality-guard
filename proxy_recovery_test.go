package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	proxy "grok-quality-guard/internal/proxy"
)

func TestProxyFinalizeFailureRetainsQuotaAndRecoversFromJournal(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "first@example.com", nil)
	second, more := singleGrokAuth(t, "auth-2", "second@example.com", nil)
	entries = append(entries, second...)
	for id, raw := range more {
		auths[id] = raw
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	node, err := plugin.createProxyNode(proxyNodeInput{URL: "http://user:secret@proxy.example:8080", MaxBindings: 1})
	if err != nil {
		t.Fatal(err)
	}
	store := plugin.stateStore()
	path := store.Path()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := hostCall
	defer func() { hostCall = original }()
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		raw, err := original(method, payload)
		if method == pluginabi.MethodHostAuthSave && err == nil {
			store.mu.Lock()
			store.path = filepath.Join(blocker, "state.json")
			store.mu.Unlock()
		}
		return raw, err
	}
	result, err := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{OperationID: "recover-me"})
	if err == nil || !result.Committed || !result.NeedsReconcile {
		t.Fatalf("committed result lost: result=%#v err=%v", result, err)
	}
	hostCall = original
	other, err := plugin.bindProxy(context.Background(), "auth-2", proxyBindOptions{})
	if err != nil || other.Status != "unavailable" {
		t.Fatalf("pending assignment must occupy quota: %#v %v", other, err)
	}
	store.flushMu.Lock()
	if store.flushTimer != nil {
		store.flushTimer.Stop()
		store.flushTimer = nil
	}
	store.flushMu.Unlock()
	store.mu.Lock()
	store.path = path
	store.mu.Unlock()
	reloaded := NewStateStore(path)
	if err := reloaded.Load(plugin.policy, "test"); err != nil {
		t.Fatal(err)
	}
	if reloaded.ProxyPending("auth-1") != "recover-me" || reloaded.ProxyBindingCount(node.ID) != 0 {
		t.Fatal("prepared intent was not replayed")
	}
	plugin.mu.Lock()
	plugin.store = reloaded
	plugin.mu.Unlock()
	plugin.reconcileProxyOperations(context.Background())
	if reloaded.ProxyPending("auth-1") != "" || reloaded.ProxyBindingCount(node.ID) != 1 {
		t.Fatal("recovery did not finalize the existing target")
	}
	host.mu.Lock()
	saves := len(host.saves)
	host.mu.Unlock()
	if saves != 1 {
		t.Fatalf("recovery rewrote auth: saves=%d", saves)
	}
}

func TestProxyLostHostReceiptConfirmsActualCommit(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, err := plugin.createProxyNode(proxyNodeInput{URL: "http://proxy.example:8080"}); err != nil {
		t.Fatal(err)
	}
	original := hostCall
	defer func() { hostCall = original }()
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		raw, err := original(method, payload)
		if method == pluginabi.MethodHostAuthSave && err == nil {
			return json.RawMessage("broken receipt"), nil
		}
		return raw, err
	}
	result, err := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{})
	if err != nil || !result.Committed || result.NeedsReconcile {
		t.Fatalf("actual commit was not recognized: %#v %v", result, err)
	}
}

func TestProxyAutoBindDisabledAfterPrepareStopsAuthWrite(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, err := plugin.createProxyNode(proxyNodeInput{URL: "http://proxy.example:8080"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := plugin.updateProxyAutoBind(map[string]any{"enabled": true}); err != nil {
		t.Fatal(err)
	}
	original := hostCall
	defer func() { hostCall = original }()
	gets := 0
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		raw, err := original(method, payload)
		if method == pluginabi.MethodHostAuthGet {
			gets++
			if gets == 2 {
				if _, _, err := plugin.updateProxyAutoBind(map[string]any{"enabled": false}); err != nil {
					t.Fatal(err)
				}
			}
		}
		return raw, err
	}
	_, err := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{Auto: true})
	if err == nil || len(host.saves) != 0 || plugin.stateStore().ProxyPending("auth-1") != "" {
		t.Fatalf("disabled automatic operation wrote auth or remained pending: %v", err)
	}
}

func TestProxyRetryIdAndDryRunPreserveOriginalTarget(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, err := plugin.createProxyTemplate(proxyTemplateInput{Name: "sessions", URL: "http://user-{session}:secret@proxy.example:8080", Placeholders: []proxy.Placeholder{{Name: "session", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 12}}}}); err != nil {
		t.Fatal(err)
	}
	host.saveErr = errors.New("save unavailable")
	if _, err := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{OperationID: "same-target"}); err == nil {
		t.Fatal("expected host failure")
	}
	prepared, ok := plugin.stateStore().ProxyOperation("same-target")
	if !ok {
		t.Fatal("failed target not retained")
	}
	host.saveErr = nil
	if result, err := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{OperationID: "same-target", DryRun: true}); err != nil || result.Status != "dry_run" {
		t.Fatalf("preview failed: %#v %v", result, err)
	}
	if len(host.saves) != 0 {
		t.Fatal("dry run resumed an auth write")
	}
	if _, err := plugin.clearProxy(context.Background(), "auth-1", proxyBindOptions{OperationID: "same-target"}); err == nil {
		t.Fatal("operation id was reused for another action")
	}
	if _, err := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{OperationID: "same-target"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := rawAuthToMap(host.saves["auth-1"])
	if authRawString(raw, "proxy_url") != prepared.DesiredURL {
		t.Fatal("retry generated a different session")
	}
}

func TestProxyNodeRotationDefaultsToNodeScope(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	one, _ := plugin.createProxyNode(proxyNodeInput{URL: "http://one.example:8080"})
	two, _ := plugin.createProxyNode(proxyNodeInput{URL: "http://two.example:8080"})
	if _, err := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{SourceIDs: []string{one.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.createProxyTemplate(proxyTemplateInput{Name: "higher priority", URL: "http://user@template.example:8080", Priority: 100}); err != nil {
		t.Fatal(err)
	}
	opts, err := proxyBindOptionsFromParams(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := plugin.rotateProxy(context.Background(), "auth-1", opts)
	if err != nil || result.SourceID != two.ID || result.SourceKind != proxy.SourceNode {
		t.Fatalf("implicit cross-provider rotation: %#v %v", result, err)
	}
}

func TestProxyAuthenticationCannotDisappearDuringPartialEdit(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	node, err := plugin.createProxyNode(proxyNodeInput{URL: "http://user:secret@proxy.example:8080"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = plugin.updateProxyNode(proxyNodeInput{ID: node.ID, UsernameSet: true, Username: "", PasswordUpdate: &passwordUpdate{Action: "keep"}})
	if err == nil {
		t.Fatal("empty username silently discarded retained password")
	}
	current, _ := plugin.proxyExistingNode(node.ID)
	if current.Endpoint.Fingerprint() != node.Endpoint.Fingerprint() {
		t.Fatal("invalid edit was saved")
	}
}

func TestAuthProxyMetadataFailuresRemainUnknown(t *testing.T) {
	for _, content := range []string{"null", "{", `{"proxy_url":123}`} {
		path := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, known := readAuthProxyURL(path); known {
			t.Fatalf("invalid metadata was treated as empty: %s", content)
		}
	}
	if _, err := rawAuthToMap(json.RawMessage("null")); err == nil {
		t.Fatal("null auth accepted as writable object")
	}
}

func TestProxyJournalTailRecoveryAndCheckpointProtection(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Load(PolicyFromConfig(DefaultConfig()), "test"); err != nil {
		t.Fatal(err)
	}
	op := proxy.Operation{ID: "prepared", AuthID: "auth", Action: "proxy_bind", Status: "prepared", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CommitProxy(proxyCommit{Operation: &op}); err != nil {
		t.Fatal(err)
	}
	store.flushMu.Lock()
	if store.flushTimer != nil {
		store.flushTimer.Stop()
		store.flushTimer = nil
	}
	store.flushMu.Unlock()
	path := store.Path() + ".proxy-journal"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("incomplete"); err != nil {
		t.Fatal(err)
	}
	file.Close()
	reloaded := NewStateStore(store.Path())
	if err := reloaded.Load(PolicyFromConfig(DefaultConfig()), "test"); err != nil {
		t.Fatal(err)
	}
	if reloaded.ProxyPending("auth") != "prepared" {
		t.Fatal("complete journal record was lost")
	}
	contents, _ := os.ReadFile(path)
	if strings.Contains(string(contents), "incomplete") {
		t.Fatal("incomplete tail was not removed")
	}
	if err := os.Remove(store.Path()); err != nil {
		t.Fatal(err)
	}
	if err := NewStateStore(store.Path()).Load(PolicyFromConfig(DefaultConfig()), "test"); err == nil {
		t.Fatal("missing base checkpoint silently discarded configuration")
	}
}

func TestProxyHistoryGCKeepsReferencedVersionsAndOrder(t *testing.T) {
	state := State{Accounts: map[string]*AccountState{"auth": {Proxy: proxy.Binding{SourceID: "template", SourceRevision: 1}}}, Proxy: proxy.ProxyState{TemplateVersions: []proxy.Template{{ID: "template", Revision: 1}, {ID: "template", Revision: 2}}}}
	for i := 0; i < 105; i++ {
		state.Proxy.Changes = append(state.Proxy.Changes, proxy.SourceChange{ID: fmt.Sprint(i), Status: "succeeded"})
	}
	gcProxyHistory(&state)
	if len(state.Proxy.TemplateVersions) != 1 || state.Proxy.TemplateVersions[0].Revision != 1 {
		t.Fatal("referenced template version was collected")
	}
	if len(state.Proxy.Changes) != 100 || state.Proxy.Changes[0].ID != "5" || state.Proxy.Changes[99].ID != "104" {
		t.Fatal("completed history order was corrupted")
	}
}

func TestProxyChangeProgressTracksCommittedItems(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Load(PolicyFromConfig(DefaultConfig()), "test"); err != nil {
		t.Fatal(err)
	}
	defer store.Flush()
	if err := store.Update(func(state *State) error {
		state.Proxy.Changes = []proxy.SourceChange{{ID: "progress", Status: "running", Counts: proxy.ChangeCounts{Total: 2, Pending: 2}, Items: []proxy.ChangeItem{{AuthID: "one", Status: "pending"}, {AuthID: "two", Status: "pending"}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	item := proxy.ChangeItem{AuthID: "one", Status: "succeeded"}
	if err := store.CommitProxy(proxyCommit{ChangeID: "progress", ChangeItem: &item}); err != nil {
		t.Fatal(err)
	}
	change, _ := store.ProxyChange("progress")
	if change.Counts.Succeeded != 1 || change.Counts.Pending != 1 {
		t.Fatalf("live progress not updated: %#v", change.Counts)
	}
}
