package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestResinLeaseDeleteSharesAdminToken(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a1@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	settings := ResinSettings{
		Configured: true,
		Enabled:    true,
		PlatformID: "platform-1",
		AdminURL:   "https://resin.example",
	}
	settings.AdminToken = strings.Join([]string{"shared", "token"}, "-")
	if errUpdate := plugin.stateStore().Update(func(state *State) error {
		queueResinLeaseDelete(state, "auth-1", settings, "account-1")
		queueResinLeaseDelete(state, "auth-2", settings, "account-2")
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	snapshot := plugin.stateStore().Snapshot()
	if len(snapshot.ResinAdminTokens) != 1 {
		t.Fatalf("admin token copies = %d, want 1: %#v", len(snapshot.ResinAdminTokens), snapshot.ResinAdminTokens)
	}
	if len(snapshot.ResinLeaseDeletes) != 2 {
		t.Fatalf("lease delete jobs = %d, want 2", len(snapshot.ResinLeaseDeletes))
	}
	ref := ""
	for _, job := range snapshot.ResinLeaseDeletes {
		if strings.TrimSpace(job.AdminToken) != "" || job.AdminTokenRef == "" {
			t.Fatalf("job retained a token copy: %#v", job)
		}
		if ref == "" {
			ref = job.AdminTokenRef
		} else if ref != job.AdminTokenRef {
			t.Fatalf("jobs use different token refs: %s != %s", ref, job.AdminTokenRef)
		}
	}
}

func TestPrepareTransparentRequestReusesBodyFacts(t *testing.T) {
	body := []byte(`{"model":"grok-4.6","input":"hi","reasoning":{"effort":"high"},"prompt_cache_key":"session-1","tools":[{"type":"function","name":"lookup"}]}`)
	req := rpcExecutorRequest{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "grok-4.6",
			Format:          "codex",
			SourceFormat:    "codex",
			Stream:          true,
			OriginalRequest: body,
			Headers:         http.Header{"X-Session-Id": []string{"header-session"}},
		},
	}
	plugin := &Plugin{}
	facts, errPrepare := plugin.prepareTransparentRequest(req)
	if errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if facts.Model != "grok-4.6" || !facts.ReasoningRequired || !facts.Replayable {
		t.Fatalf("unexpected request facts: %#v", facts)
	}
	if facts.SessionSeed != "header-session" {
		t.Fatalf("session seed = %q, want header-session", facts.SessionSeed)
	}
	if !strings.Contains(string(facts.Body), "reasoning.encrypted_content") {
		t.Fatalf("reasoning evidence include was not added: %s", facts.Body)
	}
	if !strings.Contains(string(facts.Body), `"input"`) {
		t.Fatalf("request body was mangled: %s", facts.Body)
	}
}

func TestBindingHeapPrunesExpiredBindings(t *testing.T) {
	runtime := newRoutingSelectionRuntime()
	now := time.Now().UTC()
	for index := 0; index < 2000; index++ {
		runtime.GetOrBind(fmt.Sprintf("session-%d", index), "auth-1", now, time.Second, 10000)
	}
	runtime.mu.Lock()
	runtime.pruneBindingsLocked(now.Add(2 * time.Second))
	remaining := len(runtime.bindings)
	runtime.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expired bindings remaining = %d, want 0", remaining)
	}
}

func TestPassiveAliasUsesAuthModelCache(t *testing.T) {
	plugin := newTestPlugin(t, newMockHostCall(nil, nil), nil)
	plugin.setGrokAuthModels([]pluginapi.HostAuthModels{{Provider: "xai", Models: []string{"xai-grok-alias"}}})
	if isGrokModel("xai-grok-alias", plugin.policy.ModelPrefixes) {
		t.Fatal("alias unexpectedly matched the configured prefix")
	}
	if !plugin.isGrokRouteModel("xai-grok-alias") {
		t.Fatal("alias did not match the auth model cache")
	}
}
