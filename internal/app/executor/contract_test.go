package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// TestTransparentExecutorUsesPayloadFormatAndTargetExitProtocol keeps the host
// callback protocols distinct: SourceFormat describes the translated payload while
// Format describes the response the host expects the plugin to return.
func TestTransparentExecutorUsesPayloadFormatAndTargetExitProtocol(t *testing.T) {
	req := Request{ExecutorRequest: pluginapi.ExecutorRequest{
		Model:           "grok-4.6",
		Format:          "openai-response",
		SourceFormat:    "codex",
		Stream:          true,
		OriginalRequest: []byte(`{"model":"grok-4.6","stream":true,"input":"hi"}`),
	}}
	req.Payload = []byte(`{"model":"grok-4.6","stream":true,"input":"hi"}`)

	_, sourceFormat, errRequest := executorRequestInput(req)
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	if sourceFormat != "codex" {
		t.Fatalf("payload source format = %q, want codex", sourceFormat)
	}

	execution := buildHostExecution(req, req.Payload, sourceFormat, "auth-1", "", true)
	if execution.EntryProtocol != "codex" || execution.ExitProtocol != "openai-response" {
		t.Fatalf("execution protocols = entry %q, exit %q; want codex -> openai-response", execution.EntryProtocol, execution.ExitProtocol)
	}
}
