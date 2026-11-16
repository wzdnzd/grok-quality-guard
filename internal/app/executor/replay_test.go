package executor

import "testing"

func TestSafeReplayRequest(t *testing.T) {
	safe := []byte(`{"model":"grok-4.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if !safeReplayRequest(safe, "codex", "grok-4.5") {
		t.Fatal("simple text request should be safe")
	}
	unsafeCases := [][]byte{
		[]byte(`{"previous_response_id":"resp_1"}`),
		[]byte(`{"store":true}`),
		[]byte(`{"background":true}`),
		[]byte(`{"web_search_options":{}}`),
		[]byte(`{"tools":[{"type":"web_search"}]}`),
		[]byte(`{"reasoning_effort":"none"}`),
		[]byte(`{"input":[{"type":"compaction_trigger"}]}`),
		[]byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`), []byte(`{"modalities":["text","image"],"messages":[]}`),
	}
	for _, body := range unsafeCases {
		if safeReplayRequest(body, "codex", "grok-4.5") {
			t.Fatalf("request should be unsafe: %s", body)
		}
	}
}

func TestMCPEmptyArrayIsReplaySafe(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","mcp_servers":[],"messages":[{"role":"user","content":"hi"}]}`)
	if !safeReplayRequest(body, "codex", "grok-4.5") {
		t.Fatal("empty mcp_servers should not block replay")
	}
	body = []byte(`{"model":"grok-4.5","mcp_servers":[{"url":"https://example.com"}],"messages":[]}`)
	if safeReplayRequest(body, "codex", "grok-4.5") {
		t.Fatal("non-empty mcp_servers must block replay")
	}
}
