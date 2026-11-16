package routing

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPrepareTransparentRequestReusesBodyFacts(t *testing.T) {
	body := []byte(`{"model":"grok-4.6","input":"hi","reasoning":{"effort":"high"},"prompt_cache_key":"session-1","tools":[{"type":"function","name":"lookup"}]}`)
	req := Request{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           "grok-4.6",
		Format:          "codex",
		SourceFormat:    "codex",
		Stream:          true,
		OriginalRequest: body,
		Headers:         http.Header{"X-Session-Id": []string{"header-session"}},
	}}
	service := NewService(nil)
	facts, errPrepare := service.PrepareRequest(req)
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
