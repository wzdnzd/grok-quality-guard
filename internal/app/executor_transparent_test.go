package app

import (
	"context"
	"encoding/json"
	"errors"
	execapp "grok-quality-guard/internal/app/executor"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	sse "grok-quality-guard/internal/sse"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func twoGrokAuths(t *testing.T) ([]pluginapi.HostAuthFileEntry, map[string]json.RawMessage) {
	t.Helper()
	entries1, auths1 := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	entries2, auths2 := singleGrokAuth(t, "auth-2", "bob@example.com", nil)
	entries1[0].Priority = 2
	entries2[0].Priority = 1
	auths := make(map[string]json.RawMessage, 2)
	for key, value := range auths1 {
		auths[key] = value
	}
	for key, value := range auths2 {
		auths[key] = value
	}
	return append(entries1, entries2...), auths
}

func badStreamScript() []pluginapi.HostModelStreamReadResponse {
	return []pluginapi.HostModelStreamReadResponse{
		streamData("data: {\"choices\":[{\"delta\":{\"content\":\"bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer \"}}]}\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":100}}\n\n"),
		{Done: true},
	}
}

func goodStreamScript() []pluginapi.HostModelStreamReadResponse {
	return []pluginapi.HostModelStreamReadResponse{
		streamData("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"plan the answer\"}}]}\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{\"content\":\"good answer\"}}]}\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":40,\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"),
		{Done: true},
	}
}

func streamRequestForTest() execapp.Request {
	body := []byte(`{"model":"grok-4.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	return execapp.Request{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "grok-4.5",
			Format:          "codex",
			SourceFormat:    "codex",
			Stream:          true,
			OriginalRequest: body,
			Headers:         http.Header{"Content-Type": []string{"application/json"}},
		},
		HostCallbackID: "callback-1",
	}
}

func nonReasoningStreamRequestForTest() execapp.Request {
	body := []byte(`{"model":"grok-composer-2.5-fast","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	return execapp.Request{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           "grok-composer-2.5-fast",
			Format:          "codex",
			SourceFormat:    "codex",
			Stream:          true,
			OriginalRequest: body,
			Headers:         http.Header{"Content-Type": []string{"application/json"}},
		},
		HostCallbackID: "callback-non-reasoning",
	}
}

func enableTransparentForTest(cfg *appconfig.Config) {
	cfg.Guard = "transparent"
	cfg.Quality.SoftTPS = 1e9
	cfg.Quality.HardTPS = 2e9
}

// setTransparentTimingForTest installs sub-second hold and attempt windows on
// the live policy. Real configs must respect the validated ranges, but timing
// tests need millisecond windows to stay fast.
func setTransparentTimingForTest(plugin *Plugin, hold, attempt time.Duration) {
	policy := plugin.Policy()
	if hold > 0 {
		policy.Quality.HoldTimeout = appconfig.NewDuration(hold)
	}
	if attempt > 0 {
		policy.Quality.AttemptTimeout = appconfig.NewDuration(attempt)
	}
	policy.Quality.RequestTimeout = appconfig.NewDuration(time.Minute)
	plugin.SetPolicy(policy)
}

func TestTransparentCandidatesIncludeProbeMarkedAccount(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.markProbeAuth("auth-1", time.Minute)

	candidates, errCandidates := plugin.transparentCandidates(context.Background())
	if errCandidates != nil {
		t.Fatal(errCandidates)
	}
	if len(candidates) != 1 || candidates[0].ID != "auth-1" {
		t.Fatalf("probe-marked account was excluded from transparent candidates: %#v", candidates)
	}
}

func TestTransparentCandidatesSkipQualitySuspectAccounts(t *testing.T) {
	for _, state := range []string{appstate.StateSuspect, appstate.StateDegraded} {
		t.Run(state, func(t *testing.T) {
			entries, auths := twoGrokAuths(t)
			host := newMockHostCall(entries, auths)
			plugin := newTestPlugin(t, host, enableTransparentForTest)
			if errUpdate := plugin.stateStore().Update(func(value *appstate.State) error {
				plugin.ensureStateAccount(value, "auth-1").State = state
				plugin.ensureStateAccount(value, "auth-2").State = appstate.StateHealthy
				return nil
			}); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			candidates, errCandidates := plugin.transparentCandidates(context.Background())
			if errCandidates != nil {
				t.Fatal(errCandidates)
			}
			if len(candidates) != 1 || candidates[0].ID != "auth-2" {
				t.Fatalf("quality-suspect account remained eligible: %#v", candidates)
			}
		})
	}
}

func TestTransparentStreamFailsOverBeforeEmit(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", badStreamScript()...)
	host.scriptStream("auth-2", goodStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-1")

	emitted := host.emitted["plugin-stream-1"]
	var joined strings.Builder
	for _, payload := range emitted {
		joined.Write(payload)
	}
	if strings.Contains(joined.String(), "bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer ") {
		t.Fatalf("bad content leaked to client: %s", joined.String())
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("good content was not delivered: %s", joined.String())
	}
	if detail := host.closes["plugin-stream-1"]; detail != nil {
		t.Fatalf("successful stream closed with error: %#v", detail)
	}
	badState, _ := plugin.stateAccount("auth-1")
	goodState, _ := plugin.stateAccount("auth-2")
	// One transparent miss is not authoritative: the account waits for a
	// second miss or a cross-verification probe before it is degraded.
	if badState.State != appstate.StateSuspect || badState.ThinkingStrikes != 1 {
		t.Fatalf("bad auth state = %q strikes=%d, want suspect with 1 strike", badState.State, badState.ThinkingStrikes)
	}
	if goodState.State != appstate.StateHealthy {
		t.Fatalf("good auth state = %q, want healthy", goodState.State)
	}
	if len(host.modelCalls) != 2 {
		t.Fatalf("transparent request used %d attempts, want 2", len(host.modelCalls))
	}
	if host.modelCalls[0].AuthID == host.modelCalls[1].AuthID {
		t.Fatalf("missing-thinking response was retried on the same account: %#v", host.modelCalls)
	}
}

func TestTransparentMissingThinkingSwitchesAccountBeforeProxyRotation(t *testing.T) {
	entries1, auths1 := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260",
	})
	entries2, auths2 := singleGrokAuth(t, "auth-2", "[EMAIL]", nil)
	entries1[0].Priority = 2
	entries2[0].Priority = 1
	auths := make(map[string]json.RawMessage, len(auths1)+len(auths2))
	for key, value := range auths1 {
		auths[key] = value
	}
	for key, value := range auths2 {
		auths[key] = value
	}
	host := newMockHostCall(append(entries1, entries2...), auths)
	host.scriptStreamSequence("auth-1", badStreamScript(), badStreamScript())
	host.scriptStream("auth-2", goodStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
	})
	seedResinBinding(t, plugin, "grok-alice-old1", 0)

	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-account-switch")

	if len(host.modelCalls) != 2 {
		t.Fatalf("missing-thinking request used %d attempts, want account switch after first failure", len(host.modelCalls))
	}
	if host.modelCalls[0].AuthID == host.modelCalls[1].AuthID {
		t.Fatalf("missing-thinking request retried the same account before switching: %#v", host.modelCalls)
	}
	for _, event := range plugin.stateStore().Snapshot().Events {
		if event.Action == "proxy_rotate" {
			t.Fatalf("missing-thinking request rotated the same account before switching: %#v", event)
		}
	}
}

func TestTransparentStreamFailsClosedWhenAllCandidatesAreQualitySuspect(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	if errUpdate := plugin.stateStore().Update(func(value *appstate.State) error {
		plugin.ensureStateAccount(value, "auth-1").State = appstate.StateSuspect
		plugin.ensureStateAccount(value, "auth-2").State = appstate.StateDegraded
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-no-quality-candidates")

	if len(host.modelCalls) != 0 {
		t.Fatalf("quality-suspect accounts were reused: %#v", host.modelCalls)
	}
	detail := host.closes["plugin-stream-no-quality-candidates"]
	if detail == nil || detail.Code != "quality_degraded" || detail.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("unexpected close detail: %#v", detail)
	}
}

func TestTransparentStreamAllCandidatesExhausted(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", badStreamScript()...)
	host.scriptStream("auth-2", badStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-2")
	if len(host.emitted["plugin-stream-2"]) != 0 {
		t.Fatalf("bad content was emitted: %#v", host.emitted["plugin-stream-2"])
	}
	detail := host.closes["plugin-stream-2"]
	if detail == nil || detail.Code != "quality_degraded" || detail.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("unexpected close detail: %#v", detail)
	}
}

func TestTransparentStreamFailUnverifiedNeverReleasesConfirmedDegradation(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", badStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.OnExhausted = "fail_unverified"
	})
	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-3")
	emitted := host.emitted["plugin-stream-3"]
	var joined strings.Builder
	for _, payload := range emitted {
		joined.Write(payload)
	}
	// fail_unverified only releases a response the gate could not judge. A
	// confirmed degraded response is never sent to the client.
	if strings.Contains(joined.String(), "bad answer") {
		t.Fatalf("fail_unverified released a confirmed degraded response: %s", joined.String())
	}
	if detail := host.closes["plugin-stream-3"]; detail == nil || detail.Code != "quality_degraded" {
		t.Fatalf("confirmed degradation did not fail closed: %#v", detail)
	}
}

func TestTransparentStreamUnsafeRequestUsesSingleAttempt(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", badStreamScript()...)
	host.scriptStream("auth-2", goodStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	req := streamRequestForTest()
	req.OriginalRequest = []byte(`{"model":"grok-4.5","previous_response_id":"resp_1"}`)
	plugin.runTransparentStreamBridge(context.Background(), req, "plugin-stream-4")
	detail := host.closes["plugin-stream-4"]
	if detail == nil || detail.Code != "quality_degraded" || detail.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("unexpected close detail: %#v", detail)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("unsafe request used %d attempts, want 1", len(host.modelCalls))
	}
	if len(host.emitted["plugin-stream-4"]) != 0 {
		t.Fatal("unsafe request emitted degraded content")
	}
}

func TestTransparentUnsafeRequestFailsOverOnPreResponseTransportError(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStreamError("auth-1", errors.New("net/http: TLS handshake timeout"))
	host.scriptStream("auth-2", goodStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	req := streamRequestForTest()
	req.OriginalRequest = []byte(`{"model":"grok-4.5","previous_response_id":"resp_1"}`)
	plugin.runTransparentStreamBridge(context.Background(), req, "plugin-stream-unsafe-transport")

	joined := strings.Builder{}
	for _, payload := range host.emitted["plugin-stream-unsafe-transport"] {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("transport failover did not deliver the healthy account response: %s", joined.String())
	}
	if detail := host.closes["plugin-stream-unsafe-transport"]; detail != nil {
		t.Fatalf("transport failover closed with error: %#v", detail)
	}
	if len(host.modelCalls) != 2 {
		t.Fatalf("pre-response transport failure used %d attempts, want 2", len(host.modelCalls))
	}
	if host.modelCalls[0].AuthID != "auth-1" || host.modelCalls[1].AuthID != "auth-2" {
		t.Fatalf("pre-response transport failure did not switch accounts: %#v", host.modelCalls)
	}
}

func TestTransparentNonReasoningStreamDelivered(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1",
		streamData("data: {\"choices\":[{\"delta\":{\"content\":\"plain answer without any reasoning content at all\"}}]}\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":12}}\n\n"),
		pluginapi.HostModelStreamReadResponse{Done: true},
	)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), nonReasoningStreamRequestForTest(), "plugin-stream-non-reasoning")

	var joined strings.Builder
	for _, payload := range host.emitted["plugin-stream-non-reasoning"] {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "plain answer without any reasoning content at all") {
		t.Fatalf("non-reasoning stream was not delivered: %s", joined.String())
	}
	if detail := host.closes["plugin-stream-non-reasoning"]; detail != nil {
		t.Fatalf("non-reasoning stream closed with error: %#v", detail)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("non-reasoning stream used %d attempts, want 1", len(host.modelCalls))
	}
}

func TestTransparentThinkingGuardDisabledDeliversNoThinkingStream(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", badStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.ThinkingGuard = false
	})
	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-thinking-guard-disabled")

	var joined strings.Builder
	for _, payload := range host.emitted["plugin-stream-thinking-guard-disabled"] {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "bad answer") {
		t.Fatalf("thinking_guard=false stream was not delivered: %s", joined.String())
	}
	if detail := host.closes["plugin-stream-thinking-guard-disabled"]; detail != nil {
		t.Fatalf("thinking_guard=false stream closed with error: %#v", detail)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("thinking_guard=false stream used %d attempts, want 1", len(host.modelCalls))
	}
}

func TestTransparentNonReasoningEmptyStreamRejected(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", pluginapi.HostModelStreamReadResponse{Done: true})
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), nonReasoningStreamRequestForTest(), "plugin-stream-non-reasoning-empty")

	if len(host.emitted["plugin-stream-non-reasoning-empty"]) != 0 {
		t.Fatalf("empty stream emitted content: %#v", host.emitted["plugin-stream-non-reasoning-empty"])
	}
	detail := host.closes["plugin-stream-non-reasoning-empty"]
	if detail == nil || detail.Code != "quality_degraded" || detail.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("unexpected close detail: %#v", detail)
	}
}

func TestTransparentStreamBufferOverflowDeliversAndRecords(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	// The buffer floor is 1MiB, so the scripted chunk must exceed it.
	oversized := strings.Repeat("overflow ", (1<<20)/9+1)
	host.scriptStream("auth-1",
		streamData("data: {\"choices\":[{\"delta\":{\"content\":\""+oversized+"\"}}]}\n\n"),
		pluginapi.HostModelStreamReadResponse{Done: true},
	)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.HoldMaxBufferBytes = 1 << 20
	})
	plugin.runTransparentStreamBridge(context.Background(), nonReasoningStreamRequestForTest(), "plugin-stream-buffer-overflow")

	if len(host.emitted["plugin-stream-buffer-overflow"]) == 0 {
		t.Fatal("buffer overflow did not deliver the buffered content")
	}
	if detail := host.closes["plugin-stream-buffer-overflow"]; detail != nil {
		t.Fatalf("buffer overflow closed with error: %#v", detail)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("buffer overflow used %d attempts, want 1", len(host.modelCalls))
	}
}

func TestTransparentNonStreamingReturns503WhenExhausted(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	req := streamRequestForTest()
	req.Stream = false
	req.OriginalRequest = []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}]}`)
	encoded, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response, errExecute := plugin.Execute(encoded)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code       string `json:"code"`
			HTTPStatus int    `json:"http_status"`
		} `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(response, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if envelope.OK || envelope.Error.Code != "quality_degraded" || envelope.Error.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("unexpected executor response: %s", response)
	}
}
func TestRouteModelSafetyAndCandidateChecks(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	request := execapp.RouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{
		SourceFormat:   "codex",
		RequestedModel: "grok-4.5",
		Stream:         true,
		Body:           []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"hi"}]}`),
	}}
	encoded, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	raw, errRoute := plugin.RouteModel(encoded)
	if errRoute != nil {
		t.Fatal(errRoute)
	}
	var routeEnvelope struct {
		OK     bool                         `json:"ok"`
		Result pluginapi.ModelRouteResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &routeEnvelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !routeEnvelope.OK || !routeEnvelope.Result.Handled {
		t.Fatalf("route was not handled: %s", raw)
	}
	request.Body = []byte(`{"model":"grok-4.5","mcp_servers":[{"url":"https://example.com"}]}`)
	encoded, _ = json.Marshal(request)
	raw, _ = plugin.RouteModel(encoded)
	_ = json.Unmarshal(raw, &routeEnvelope)
	if !routeEnvelope.Result.Handled {
		t.Fatal("unsafe MCP request bypassed the transparent gate")
	}
}

func TestRouteModelBypassesWhenNoAvailableXAIAuth(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	request := execapp.RouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{
		SourceFormat:       "codex",
		RequestedModel:     "grok-4.7",
		Stream:             true,
		Body:               []byte(`{"model":"grok-4.7","messages":[{"role":"user","content":"hi"}]}`),
		AvailableProviders: []string{"claude"},
	}}
	encoded, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	raw, errRoute := plugin.RouteModel(encoded)
	if errRoute != nil {
		t.Fatal(errRoute)
	}
	var routeEnvelope struct {
		OK     bool                         `json:"ok"`
		Result pluginapi.ModelRouteResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &routeEnvelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !routeEnvelope.OK || routeEnvelope.Result.Handled {
		t.Fatalf("request with no available xAI auth was handled: %s", raw)
	}
	if routeEnvelope.Result.Reason != "xai_auth_unavailable" {
		t.Fatalf("route reason = %q, want xai_auth_unavailable", routeEnvelope.Result.Reason)
	}
}

func responsesRequestForTest(model string, stream bool) execapp.Request {
	streamText := "false"
	if stream {
		streamText = "true"
	}
	body := []byte(`{"model":"` + model + `","stream":` + streamText + `,"input":"hi","reasoning":{"effort":"medium"}}`)
	return execapp.Request{
		ExecutorRequest: pluginapi.ExecutorRequest{
			Model:           model,
			Format:          "openai-response",
			SourceFormat:    "openai-response",
			Stream:          stream,
			OriginalRequest: body,
			Headers:         http.Header{"Content-Type": []string{"application/json"}},
		},
		HostCallbackID: "callback-responses",
	}
}

func goodResponsesStreamScript() []pluginapi.HostModelStreamReadResponse {
	return []pluginapi.HostModelStreamReadResponse{
		streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"grok-4.6\"}}\n\n"),
		streamData("data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[]}}\n\n"),
		streamData("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"plan the answer\"}\n\n"),
		streamData("data: {\"type\":\"response.output_text.delta\",\"delta\":\"good answer\"}\n\n"),
		streamData("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"usage\":{\"output_tokens\":20,\"output_tokens_details\":{\"reasoning_tokens\":8}}}}\n\n"),
		{Done: true},
	}
}

func badResponsesStreamScript() []pluginapi.HostModelStreamReadResponse {
	return []pluginapi.HostModelStreamReadResponse{
		streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_bad\",\"model\":\"grok-4.6\"}}\n\n"),
		streamData("data: {\"type\":\"response.output_text.delta\",\"delta\":\"bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer bad answer \"}\n\n"),
		streamData("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bad\",\"usage\":{\"output_tokens\":100,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n"),
		{Done: true},
	}
}

func semanticOnlyResponsesStreamScript() []pluginapi.HostModelStreamReadResponse {
	return []pluginapi.HostModelStreamReadResponse{
		streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_semantic\",\"model\":\"grok-4.6\"}}\n\n"),
		streamData("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"call_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"exec\",\"arguments\":\"{}\"}}\n\n"),
		streamData("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_semantic\",\"usage\":{\"output_tokens\":421,\"output_tokens_details\":{\"reasoning_tokens\":355}}}}\n\n"),
		{Done: true},
	}
}

func TestTransparentCodexResponsesThinkingStreamDelivered(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), "plugin-stream-responses-good")

	joined := strings.Builder{}
	for _, payload := range host.emitted["plugin-stream-responses-good"] {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "good answer") || !strings.Contains(joined.String(), "response.reasoning_summary_text.delta") {
		t.Fatalf("thinking Responses stream was not delivered intact: %s", joined.String())
	}
	if detail := host.closes["plugin-stream-responses-good"]; detail != nil {
		t.Fatalf("thinking Responses stream closed with error: %#v", detail)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("thinking Responses stream used %d accounts, want 1", len(host.modelCalls))
	}
}

func TestTransparentCodexResponsesTerminalIgnoresTrailingReadError(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	script := goodResponsesStreamScript()
	script = script[:len(script)-1]
	script = append(script, pluginapi.HostModelStreamReadResponse{
		Error: "unexpected EOF after response.completed",
		Done:  true,
	})
	host.scriptStream("auth-1", script...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), "plugin-stream-responses-terminal-error")

	joined := strings.Builder{}
	for _, payload := range host.emitted["plugin-stream-responses-terminal-error"] {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "good answer") || !strings.Contains(joined.String(), "response.completed") {
		t.Fatalf("complete terminal response was not delivered: %s", joined.String())
	}
	if detail := host.closes["plugin-stream-responses-terminal-error"]; detail != nil {
		t.Fatalf("terminal response closed with trailing error: %#v", detail)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("terminal response used %d attempts, want 1", len(host.modelCalls))
	}
}

func TestTransparentCodexResponsesHeldBufferSurvivesTrailingReadError(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1",
		streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_short\",\"model\":\"grok-4.6\"}}\n\n"),
		streamData("data: {\"type\":\"response.output_text.delta\",\"delta\":\"short\"}\n\n"),
		streamData("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_short\",\"usage\":{\"output_tokens\":3,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n"),
		pluginapi.HostModelStreamReadResponse{Error: "unexpected EOF after response.completed", Done: true},
	)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), "plugin-stream-responses-held-terminal-error")

	joined := strings.Builder{}
	for _, payload := range host.emitted["plugin-stream-responses-held-terminal-error"] {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "short") || !strings.Contains(joined.String(), "response.completed") {
		t.Fatalf("held terminal response was not delivered: %s", joined.String())
	}
	if detail := host.closes["plugin-stream-responses-held-terminal-error"]; detail != nil {
		t.Fatalf("held terminal response closed with trailing error: %#v", detail)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("held terminal response used %d attempts, want 1", len(host.modelCalls))
	}
}

func TestTransparentCodexResponsesMissingThinkingFailsOver(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", badResponsesStreamScript()...)
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), "plugin-stream-responses-failover")

	joined := strings.Builder{}
	for _, payload := range host.emitted["plugin-stream-responses-failover"] {
		joined.Write(payload)
	}
	if strings.Contains(joined.String(), "bad answer") {
		t.Fatalf("missing-thinking Responses content leaked: %s", joined.String())
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("good Responses account was not delivered: %s", joined.String())
	}
}

func TestTransparentCodexResponsesSemanticOnlyWithUsageFailsOver(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", semanticOnlyResponsesStreamScript()...)
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), "plugin-stream-responses-semantic-usage")

	joined := strings.Builder{}
	for _, payload := range host.emitted["plugin-stream-responses-semantic-usage"] {
		joined.Write(payload)
	}
	if strings.Contains(joined.String(), `"name":"exec"`) || strings.Contains(joined.String(), "resp_semantic") {
		t.Fatalf("semantic-only missing-thinking content leaked: %s", joined.String())
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("good Responses account was not delivered: %s", joined.String())
	}
	if detail := host.closes["plugin-stream-responses-semantic-usage"]; detail != nil {
		t.Fatalf("semantic-only failover closed with error: %#v", detail)
	}
	if len(host.modelCalls) != 2 {
		t.Fatalf("semantic-only failover used %d attempts, want 2", len(host.modelCalls))
	}
}

func TestTransparentNonStreamResponsesThinkingDelivered(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptModelResponse("auth-1", pluginapi.HostModelExecutionResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"id":"resp_1","object":"response","model":"grok-4.6","output":[{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"plan the answer"}]},{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"final answer"}]}],"usage":{"input_tokens":10,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":8}}}`),
	})
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	encoded, errMarshal := json.Marshal(responsesRequestForTest("grok-4.6", false))
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response, errExecute := plugin.Execute(encoded)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	var envelope struct {
		OK     bool                       `json:"ok"`
		Result pluginapi.ExecutorResponse `json:"result"`
		Error  pluginabi.Error            `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(response, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !envelope.OK || !strings.Contains(string(envelope.Result.Payload), "final answer") {
		t.Fatalf("non-stream Responses with thinking was rejected: %s", response)
	}
}

func TestRouteModelUsesBodyModelAndHandlesAllGrokRequests(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, enableTransparentForTest)

	route := func(model, body string) pluginapi.ModelRouteResponse {
		request := execapp.RouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{
			SourceFormat:   "openai-response",
			RequestedModel: model,
			Stream:         true,
			Body:           []byte(body),
		}}
		encoded, errMarshal := json.Marshal(request)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		raw, errRoute := plugin.RouteModel(encoded)
		if errRoute != nil {
			t.Fatal(errRoute)
		}
		var envelope struct {
			OK     bool                         `json:"ok"`
			Result pluginapi.ModelRouteResponse `json:"result"`
		}
		if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		return envelope.Result
	}

	if result := route("", `{"model":"grok-4.6","input":"hi","reasoning":{"effort":"medium"}}`); !result.Handled {
		t.Fatalf("body model fallback was not used: %#v", result)
	}
	if result := route("grok-composer-2.5-fast", `{"model":"grok-composer-2.5-fast","input":"hi"}`); !result.Handled {
		t.Fatalf("non-reasoning Grok model bypassed the transparent gate: %#v", result)
	}
	if result := route("grok-4.6", `{"model":"grok-4.6","input":"hi","reasoning_effort":"none"}`); !result.Handled {
		t.Fatalf("reasoning-disabled Grok request bypassed the transparent gate: %#v", result)
	}
	if result := route("claude-sonnet-4-5", `{"model":"claude-sonnet-4-5","input":"hi"}`); result.Handled {
		t.Fatalf("non-Grok request was routed: %#v", result)
	}
}

func TestRouteModelUsesXAIAuthModelAlias(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.authModels = []pluginapi.HostAuthModels{{
		ID:       "auth-1",
		Provider: "xai",
		Models:   []string{"vendor-thinking-pro"},
	}}
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	request := execapp.RouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{
		SourceFormat:   "openai-response",
		RequestedModel: "vendor-thinking-pro",
		Stream:         true,
		Body:           []byte(`{"model":"vendor-thinking-pro","input":"hi"}`),
	}}
	encoded, _ := json.Marshal(request)
	raw, errRoute := plugin.RouteModel(encoded)
	if errRoute != nil {
		t.Fatal(errRoute)
	}
	var envelope struct {
		OK     bool                         `json:"ok"`
		Result pluginapi.ModelRouteResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !envelope.OK || !envelope.Result.Handled {
		t.Fatalf("xAI auth model alias was not routed: %s", raw)
	}
}

func TestRouteModelHomeMetadataDoesNotBypassGate(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	request := execapp.RouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{
		SourceFormat:   "openai-response",
		RequestedModel: "grok-4.6",
		Stream:         true,
		Body:           []byte(`{"model":"grok-4.6","input":"hi"}`),
		Metadata:       map[string]any{"home_mode": true, "home": true},
	}}
	encoded, _ := json.Marshal(request)
	raw, errRoute := plugin.RouteModel(encoded)
	if errRoute != nil {
		t.Fatal(errRoute)
	}
	var envelope struct {
		OK     bool                         `json:"ok"`
		Result pluginapi.ModelRouteResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !envelope.OK || !envelope.Result.Handled {
		t.Fatalf("home metadata bypassed the transparent gate: %s", raw)
	}
}

func TestEnsureTransparentReasoningEvidenceInclude(t *testing.T) {
	body := []byte(`{"model":"grok-4.7","stream":true,"input":"hi","reasoning":{"effort":"xhigh"}}`)
	updated := ensureTransparentReasoningEvidence(body, "codex", reasoningRequiredForModel("grok-4.7", body))
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(updated, &payload); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	includes, _ := payload["include"].([]any)
	if len(includes) != 1 || includes[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want reasoning.encrypted_content", payload["include"])
	}

	withOther := []byte(`{"model":"grok-4.7","input":"hi","include":["no_inline_citations"],"seed":9007199254740993}`)
	updatedOther := ensureTransparentReasoningEvidence(withOther, "codex", reasoningRequiredForModel("grok-4.7", withOther))
	if !strings.Contains(string(updatedOther), `"seed":9007199254740993`) {
		t.Fatalf("large integer was altered while adding include: %s", updatedOther)
	}
	if errUnmarshal := json.Unmarshal(updatedOther, &payload); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	includes, _ = payload["include"].([]any)
	if len(includes) != 2 || includes[0] != "no_inline_citations" || includes[1] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want existing include plus reasoning.encrypted_content", payload["include"])
	}

	existing := []byte(`{"model":"grok-4.7","input":"hi","include":["no_inline_citations","reasoning.encrypted_content"]}`)
	if got := ensureTransparentReasoningEvidence(existing, "openai-response", reasoningRequiredForModel("grok-4.7", existing)); string(got) != string(existing) {
		t.Fatalf("existing reasoning include was rewritten: %s", got)
	}

	disabled := []byte(`{"model":"grok-4.7","input":"hi","reasoning":{"effort":"none"}}`)
	if got := ensureTransparentReasoningEvidence(disabled, "codex", reasoningRequiredForModel("grok-4.7", disabled)); string(got) != string(disabled) {
		t.Fatalf("reasoning-disabled request was rewritten: %s", got)
	}

	chat := []byte(`{"model":"grok-4.7","messages":[{"role":"user","content":"hi"}]}`)
	if got := ensureTransparentReasoningEvidence(chat, "openai", reasoningRequiredForModel("grok-4.7", chat)); string(got) != string(chat) {
		t.Fatalf("chat request was rewritten: %s", got)
	}
}

func TestTransparentStreamAddsReasoningIncludeBeforeHostExecution(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	request := responsesRequestForTest("grok-4.6", true)
	request.SourceFormat = "codex"
	request.Format = "codex"
	plugin.runTransparentStreamBridge(context.Background(), request, "plugin-stream-reasoning-include")

	if len(host.modelCalls) != 1 {
		t.Fatalf("request used %d attempts, want 1", len(host.modelCalls))
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(host.modelCalls[0].Body, &payload); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	includes, _ := payload["include"].([]any)
	found := false
	for _, value := range includes {
		if value == "reasoning.encrypted_content" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("host execution omitted reasoning.encrypted_content: %s", host.modelCalls[0].Body)
	}
}

func TestRouteModelBypassesResponsesCompaction(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	const compactionPrompt = "Your task is to produce a faithful summary. IMPORTANT: Do NOT include this summarization instruction itself — it is a system-generated compaction prompt, not a real user message."

	cases := []struct {
		name        string
		body        string
		wantHandled bool
	}{
		{name: "remote trigger", body: `{"model":"grok-4.7","input":[{"role":"user","content":"continue"},{"type":"compaction_trigger"}]}`},
		{name: "tui prompt", body: `{"model":"grok-4.7","input":[{"role":"user","content":"` + compactionPrompt + `"}]}`},
		{name: "normal text mention", body: `{"model":"grok-4.7","input":[{"role":"user","content":"explain compaction_trigger"}]}`, wantHandled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := execapp.RouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{
				SourceFormat:   "openai-response",
				RequestedModel: "grok-4.7",
				Stream:         true,
				Body:           []byte(tc.body),
			}}
			encoded, errMarshal := json.Marshal(request)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			raw, errRoute := plugin.RouteModel(encoded)
			if errRoute != nil {
				t.Fatal(errRoute)
			}
			var envelope struct {
				OK     bool                         `json:"ok"`
				Result pluginapi.ModelRouteResponse `json:"result"`
			}
			if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if envelope.Result.Handled != tc.wantHandled {
				t.Fatalf("handled = %t, want %t: %s", envelope.Result.Handled, tc.wantHandled, raw)
			}
			if !tc.wantHandled && envelope.Result.Reason != "responses_compaction" {
				t.Fatalf("compaction request missing bypass reason: %s", raw)
			}
		})
	}
}

func TestSSEOutputNormalizerAddsLineAndFrameBoundaries(t *testing.T) {
	normalizer := &sse.Normalizer{}
	chunks := [][]byte{
		[]byte("event: response.created"),
		[]byte(`data: {"type":"response.created","response":{"id":"resp_1"}}`),
		[]byte("event: response.output_text.delta"),
		[]byte(`data: {"type":"response.output_text.delta","delta":"good answer"}`),
	}
	var output []byte
	for _, chunk := range chunks {
		for _, payload := range normalizer.Feed(chunk) {
			output = append(output, payload...)
		}
	}
	for _, payload := range normalizer.Flush() {
		output = append(output, payload...)
	}

	want := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"good answer\"}\n\n"
	if string(output) != want {
		t.Fatalf("unexpected normalized SSE output:\nGot:  %q\nWant: %q", output, want)
	}
	if strings.Contains(string(output), "createddata:") || strings.Contains(string(output), "deltadata:") {
		t.Fatalf("SSE field lines were concatenated: %q", output)
	}
}

func TestSSEOutputNormalizerBuffersSplitDataLine(t *testing.T) {
	normalizer := &sse.Normalizer{}
	var output []byte
	for _, chunk := range [][]byte{
		[]byte(`data: {"type":"response.created",`),
		[]byte(`"response":{"id":"resp_1"}}`),
	} {
		for _, payload := range normalizer.Feed(chunk) {
			output = append(output, payload...)
		}
	}
	for _, payload := range normalizer.Flush() {
		output = append(output, payload...)
	}
	want := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"
	if string(output) != want {
		t.Fatalf("split data line was not framed: got %q want %q", output, want)
	}
}

func lineChunkedResponsesStreamScript() []pluginapi.HostModelStreamReadResponse {
	return []pluginapi.HostModelStreamReadResponse{
		streamData("event: response.created"),
		streamData(`data: {"type":"response.created","response":{"id":"resp_1","model":"grok-4.6"}}`),
		streamData("event: response.reasoning_summary_text.delta"),
		streamData(`data: {"type":"response.reasoning_summary_text.delta","delta":"plan the answer"}`),
		streamData("event: response.output_text.delta"),
		streamData(`data: {"type":"response.output_text.delta","delta":"good answer"}`),
		streamData("event: response.completed"),
		streamData(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"output_tokens":20,"output_tokens_details":{"reasoning_tokens":8}}}}`),
		{Done: true},
	}
}

func TestTransparentCodexResponsesLineChunkStreamDelivered(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", lineChunkedResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), "plugin-stream-responses-line-chunks")

	joined := strings.Builder{}
	for _, payload := range host.emitted["plugin-stream-responses-line-chunks"] {
		joined.Write(payload)
	}
	output := joined.String()
	for _, event := range []string{
		"response.created",
		"response.reasoning_summary_text.delta",
		"response.output_text.delta",
		"response.completed",
	} {
		if !strings.Contains(output, "event: "+event+"\ndata: ") {
			t.Fatalf("event %s lost its SSE line boundary: %s", event, output)
		}
	}
	if !strings.Contains(output, "good answer") || !strings.Contains(output, "response.completed") {
		t.Fatalf("line-chunked Responses stream was not delivered: %s", output)
	}
	for _, concatenated := range []string{"createddata:", "deltadata:", "completeddata:"} {
		if strings.Contains(output, concatenated) {
			t.Fatalf("SSE fields were concatenated as %q: %s", concatenated, output)
		}
	}
	if detail := host.closes["plugin-stream-responses-line-chunks"]; detail != nil {
		t.Fatalf("line-chunked Responses stream closed with error: %#v", detail)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("line-chunked Responses stream used %d accounts, want 1", len(host.modelCalls))
	}
}

func TestExecuteStreamWithoutStreamIDFramesLineChunks(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", lineChunkedResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	req := responsesRequestForTest("grok-4.6", true)
	req.StreamID = ""
	encoded, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	raw, errExecute := plugin.ExecuteStream(encoded)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	var envelope struct {
		OK     bool                   `json:"ok"`
		Result execapp.StreamResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !envelope.OK {
		t.Fatalf("buffered stream failed: %s", raw)
	}
	joined := strings.Builder{}
	for _, chunk := range envelope.Result.Chunks {
		joined.Write(chunk.Payload)
	}
	output := joined.String()
	if !strings.Contains(output, "event: response.output_text.delta\ndata: ") ||
		!strings.Contains(output, "good answer") ||
		!strings.Contains(output, "response.completed") {
		t.Fatalf("buffered line chunks were not framed or delivered: %s", output)
	}
	for _, concatenated := range []string{"createddata:", "deltadata:", "completeddata:"} {
		if strings.Contains(output, concatenated) {
			t.Fatalf("buffered SSE fields were concatenated as %q: %s", concatenated, output)
		}
	}
}

func TestTransparentTPSIncludesPreStreamLatency(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)

	innerHostCall := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostModelExecuteStream:
			time.Sleep(200 * time.Millisecond)
		case pluginabi.MethodHostModelStreamRead:
			time.Sleep(10 * time.Millisecond)
		}
		return innerHostCall(method, payload)
	}
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), "plugin-stream-tps-clock")

	account, ok := plugin.stateAccount("auth-1")
	if !ok {
		t.Fatal("account state missing after transparent stream")
	}
	if account.Metrics.DurationMS < 180 || account.Metrics.FirstTokenMS < 180 {
		t.Fatalf("TPS clock started after the upstream wait: %#v", account.Metrics)
	}
	if account.Metrics.GenerationMS != account.Metrics.DurationMS {
		t.Fatalf("reasoning tail should use full request duration: %#v", account.Metrics)
	}
	if account.Metrics.TPS <= 0 || account.Metrics.TPS > 500 {
		t.Fatalf("transparent TPS = %.1f, want a sane grok2api-scale value: %#v", account.Metrics.TPS, account.Metrics)
	}
}

func TestTransparentUsesHostTranslatedExecutorPayload(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", lineChunkedResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	req := responsesRequestForTest("grok-4.6", true)
	req.OriginalRequest = []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"original client format"}]}`)
	req.Payload = []byte(`{"model":"grok-4.6","stream":true,"input":"host translated payload"}`)
	req.Format = "codex"
	req.SourceFormat = "codex"
	plugin.runTransparentStreamBridge(context.Background(), req, "plugin-stream-translated-payload")

	if len(host.modelCalls) != 1 {
		t.Fatalf("transparent request used %d attempts, want 1", len(host.modelCalls))
	}
	execution := host.modelCalls[0]
	if !strings.Contains(string(execution.Body), "host translated payload") || strings.Contains(string(execution.Body), "original client format") {
		t.Fatalf("plugin used the original client body instead of the host-translated payload: %s", execution.Body)
	}
	if execution.EntryProtocol != "codex" || execution.ExitProtocol != "codex" {
		t.Fatalf("unexpected execution protocols: entry=%q exit=%q", execution.EntryProtocol, execution.ExitProtocol)
	}
}

func TestSSEOutputNormalizerPairsEventWithData(t *testing.T) {
	normalizer := &sse.Normalizer{}
	if output := normalizer.Feed([]byte("event: response.completed")); len(output) != 0 {
		t.Fatalf("event line emitted before its data line: %q", output)
	}
	output := normalizer.Feed([]byte(`data: {"type":"response.completed","response":{"id":"resp_1"}}`))
	want := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n"
	if len(output) != 1 || string(output[0]) != want {
		t.Fatalf("event and data were not emitted as one complete frame: %q", output)
	}
}

func TestTransparentUnsafeRequestDoesNotFailoverAfterStreamOpened(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", pluginapi.HostModelStreamReadResponse{Error: "unexpected EOF while reading upstream stream"})
	host.scriptStream("auth-2", goodStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	req := streamRequestForTest()
	req.OriginalRequest = []byte(`{"model":"grok-4.5","stream":true,"previous_response_id":"resp_1","messages":[{"role":"user","content":"hi"}]}`)
	plugin.runTransparentStreamBridge(context.Background(), req, "plugin-stream-unsafe-read-error")

	if len(host.modelCalls) != 1 {
		t.Fatalf("post-response read error used %d attempts, want 1", len(host.modelCalls))
	}
	detail := host.closes["plugin-stream-unsafe-read-error"]
	if detail == nil || detail.Code != "quality_degraded" || detail.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("unexpected close detail: %#v", detail)
	}
	if len(host.emitted["plugin-stream-unsafe-read-error"]) != 0 {
		t.Fatalf("unsafe request emitted content after a read error: %#v", host.emitted["plugin-stream-unsafe-read-error"])
	}
}

func TestTransparentUnsafeNonStreamFailsOverOnPreResponseTransportError(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptModelError("auth-1", errors.New("net/http: TLS handshake timeout"))
	host.scriptModelResponse("auth-2", pluginapi.HostModelExecutionResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"application/json"}},
		Body:       []byte(`{"id":"resp_2","object":"response","model":"grok-4.6","output":[{"id":"rs_2","type":"reasoning","summary":[{"type":"summary_text","text":"plan the answer"}]},{"id":"msg_2","type":"message","content":[{"type":"output_text","text":"final answer"}]}],"usage":{"input_tokens":10,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":8}}}`),
	})
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	req := responsesRequestForTest("grok-4.6", false)
	req.OriginalRequest = []byte(`{"model":"grok-4.6","stream":false,"input":"hi","previous_response_id":"resp_1","reasoning":{"effort":"medium"}}`)
	encoded, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response, errExecute := plugin.Execute(encoded)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	var envelope struct {
		OK     bool                       `json:"ok"`
		Result pluginapi.ExecutorResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(response, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !envelope.OK || !strings.Contains(string(envelope.Result.Payload), "final answer") {
		t.Fatalf("pre-response transport error did not fail over: %s", response)
	}
	if len(host.modelCalls) != 2 || host.modelCalls[0].AuthID != "auth-1" || host.modelCalls[1].AuthID != "auth-2" {
		t.Fatalf("pre-response transport error did not switch accounts: %#v", host.modelCalls)
	}
	plugin.waitSettled()
	account, _ := plugin.stateAccount("auth-1")
	if account.LastAttempt == nil || account.LastAttempt.Reason != "reason.detail.upstream_network_error" {
		t.Fatalf("transparent transport reason was not recorded with the i18n key: %#v", account.LastAttempt)
	}
}

func TestTransparentUnsafeBufferedStreamFailsOverOnPreResponseTransportError(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStreamError("auth-1", errors.New("net/http: TLS handshake timeout"))
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	req := responsesRequestForTest("grok-4.6", true)
	req.StreamID = ""
	req.OriginalRequest = []byte(`{"model":"grok-4.6","stream":true,"input":"hi","previous_response_id":"resp_1","reasoning":{"effort":"medium"}}`)
	encoded, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	raw, errExecute := plugin.ExecuteStream(encoded)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	var envelope struct {
		OK     bool                   `json:"ok"`
		Result execapp.StreamResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if !envelope.OK {
		t.Fatalf("buffered transport failover failed: %s", raw)
	}
	joined := strings.Builder{}
	for _, chunk := range envelope.Result.Chunks {
		joined.Write(chunk.Payload)
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("buffered transport failover did not deliver the healthy account response: %s", joined.String())
	}
	if len(host.modelCalls) != 2 || host.modelCalls[0].AuthID != "auth-1" || host.modelCalls[1].AuthID != "auth-2" {
		t.Fatalf("buffered transport failover did not switch accounts: %#v", host.modelCalls)
	}
}

func TestTransparentUnsafeBufferedStreamDoesNotFailoverAfterStreamOpened(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", pluginapi.HostModelStreamReadResponse{Error: "unexpected EOF while reading upstream stream"})
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	req := responsesRequestForTest("grok-4.6", true)
	req.StreamID = ""
	req.OriginalRequest = []byte(`{"model":"grok-4.6","stream":true,"input":"hi","previous_response_id":"resp_1","reasoning":{"effort":"medium"}}`)
	encoded, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	raw, errExecute := plugin.ExecuteStream(encoded)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	var envelope struct {
		OK bool `json:"ok"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if envelope.OK {
		t.Fatalf("post-open read error unexpectedly delivered a response: %s", raw)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("post-open read error used %d attempts, want 1: %#v", len(host.modelCalls), host.modelCalls)
	}
}

func TestTransparentEncryptedFastVisibleStreamDelivered(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	script := []pluginapi.HostModelStreamReadResponse{
		streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fast\",\"model\":\"grok-4.6\"}}\n\n"),
		streamData("data: {\"type\":\"response.output_item.done\",\"item\":{\"id\":\"rs_fast\",\"type\":\"reasoning\",\"encrypted_content\":\"" + strings.Repeat("A", 2048) + "\"}}\n\n"),
	}
	for index := 0; index < 12; index++ {
		script = append(script, streamData("data: {\"type\":\"response.output_text.delta\",\"delta\":\"word \"}\n\n"))
	}
	script = append(script,
		streamData("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fast\",\"usage\":{\"output_tokens\":420,\"output_tokens_details\":{\"reasoning_tokens\":400}}}}\n\n"),
		pluginapi.HostModelStreamReadResponse{Done: true},
	)
	host.scriptStream("auth-1", script...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)

	innerHostCall := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamRead {
			time.Sleep(20 * time.Millisecond)
		}
		return innerHostCall(method, payload)
	}
	defer func() { hostCall = innerHostCall }()

	streamID := "plugin-stream-encrypted-fast-deltas"
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)

	var joined strings.Builder
	for _, payload := range host.emitted[streamID] {
		joined.Write(payload)
	}
	if strings.Count(joined.String(), "word ") != 12 {
		t.Fatalf("healthy encrypted fast stream was not delivered: %s", joined.String())
	}
	if detail := host.closes[streamID]; detail != nil {
		t.Fatalf("healthy encrypted fast stream closed with error: %#v", detail)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("healthy encrypted fast stream used %d attempts, want 1", len(host.modelCalls))
	}
}

func runBlockedTransparentStream(t *testing.T, host *mockHostCall, plugin *Plugin, req execapp.Request, streamID string, blockRead int) string {
	t.Helper()
	innerHostCall := hostCall
	releaseRead := make(chan struct{})
	released := false
	readCount := 0
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamRead {
			readCount++
			if readCount == blockRead {
				<-releaseRead
			}
		}
		return innerHostCall(method, payload)
	}
	defer func() { hostCall = innerHostCall }()

	done := make(chan struct{})
	go func() {
		plugin.runTransparentStreamBridge(context.Background(), req, streamID)
		close(done)
	}()
	defer func() {
		if !released {
			close(releaseRead)
			released = true
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		host.mu.Lock()
		emitted := len(host.emitted[streamID])
		host.mu.Unlock()
		if emitted > 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("stream %s was not emitted before terminal", streamID)
		case <-ticker.C:
		}
	}

	close(releaseRead)
	released = true
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("stream %s did not finish after release", streamID)
	}

	joined := strings.Builder{}
	for _, payload := range host.emitted[streamID] {
		joined.Write(payload)
	}
	return joined.String()
}

func TestTransparentThinkingGuardDisabledEmitsBeforeTerminal(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1",
		streamData("data: {\"choices\":[{\"delta\":{\"content\":\"word word word word word word word word\"}}]}\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":12}}\n\n"),
		pluginapi.HostModelStreamReadResponse{Done: true},
	)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.ThinkingGuard = false
	})
	setTransparentTimingForTest(plugin, 50*time.Millisecond, 0)

	streamID := "plugin-stream-thinking-guard-before-terminal"
	output := runBlockedTransparentStream(t, host, plugin, streamRequestForTest(), streamID, 2)
	if !strings.Contains(output, "word word word") {
		t.Fatalf("thinking_guard=false stream was not delivered: %s", output)
	}
	if detail := host.closes[streamID]; detail != nil {
		t.Fatalf("thinking_guard=false stream closed with error: %#v", detail)
	}
}

func TestTransparentThinkingGuardDisabledEmitsAfterUnblockedHold(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1",
		streamData(": grok2api-reasoning-start\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{\"content\":\"word word word word word word word word\"}}]}\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":12}}\n\n"),
		pluginapi.HostModelStreamReadResponse{Done: true},
	)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.ThinkingGuard = false
	})
	setTransparentTimingForTest(plugin, 50*time.Millisecond, 0)

	streamID := "plugin-stream-thinking-guard-unblocked-hold"
	output := runBlockedTransparentStream(t, host, plugin, streamRequestForTest(), streamID, 3)
	if !strings.Contains(output, "word word word") {
		t.Fatalf("thinking_guard=false stream was not delivered: %s", output)
	}
	if detail := host.closes[streamID]; detail != nil {
		t.Fatalf("thinking_guard=false stream closed with error: %#v", detail)
	}
}

func TestTransparentSilentAfterResponseCreatedFailsOver(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_silent\",\"model\":\"grok-4.6\"}}\n\n"))
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
	})
	setTransparentTimingForTest(plugin, 20*time.Millisecond, 80*time.Millisecond)

	innerHostCall := hostCall
	releaseRead := make(chan struct{})
	var readCount atomic.Int32
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamRead && readCount.Add(1) == 2 {
			<-releaseRead
		}
		return innerHostCall(method, payload)
	}

	streamID := "plugin-stream-silent-response-created"
	done := make(chan struct{})
	go func() {
		plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)
		close(done)
	}()
	defer func() {
		close(releaseRead)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		hostCall = innerHostCall
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("silent stream after response.created did not reach the stream idle timeout")
	}

	var joined strings.Builder
	for _, payload := range host.emitted[streamID] {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("healthy account response was not delivered after idle failover: %s", joined.String())
	}
	if detail := host.closes[streamID]; detail != nil {
		t.Fatalf("idle failover closed the client stream with error: %#v", detail)
	}
	if len(host.modelCalls) != 2 {
		t.Fatalf("silent stream used %d attempts, want 2", len(host.modelCalls))
	}
	if host.modelCalls[0].AuthID == host.modelCalls[1].AuthID {
		t.Fatalf("silent stream did not switch accounts: %#v", host.modelCalls)
	}
}

func TestTransparentStubOnlyIdleTimeoutDoesNotFailOpen(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", streamData(": grok2api-reasoning-start\n\n"))
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.OnExhausted = "fail_unverified"
	})
	setTransparentTimingForTest(plugin, 20*time.Millisecond, 80*time.Millisecond)

	innerHostCall := hostCall
	releaseRead := make(chan struct{})
	var readCount atomic.Int32
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamRead && readCount.Add(1) == 2 {
			<-releaseRead
		}
		return innerHostCall(method, payload)
	}

	streamID := "plugin-stream-stub-only-idle"
	done := make(chan struct{})
	go func() {
		plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)
		close(done)
	}()
	defer func() {
		close(releaseRead)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		hostCall = innerHostCall
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stub-only stream did not reach the stream idle timeout")
	}

	if len(host.emitted[streamID]) != 0 {
		t.Fatalf("stub-only idle stream was emitted: %#v", host.emitted[streamID])
	}
	detail := host.closes[streamID]
	if detail == nil || detail.Code != "quality_degraded" || detail.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("stub-only idle stream did not fail closed: %#v", detail)
	}
}

func TestTransparentStreamIdleTimeoutResetsOnProgress(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
	})
	setTransparentTimingForTest(plugin, 0, 100*time.Millisecond)

	innerHostCall := hostCall
	var readCount atomic.Int32
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamRead && readCount.Add(1) > 1 {
			time.Sleep(30 * time.Millisecond)
		}
		return innerHostCall(method, payload)
	}
	defer func() { hostCall = innerHostCall }()

	streamID := "plugin-stream-idle-progress"
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)

	var joined strings.Builder
	for _, payload := range host.emitted[streamID] {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("steady stream was not delivered before idle timeout: %s", joined.String())
	}
	if detail := host.closes[streamID]; detail != nil {
		t.Fatalf("steady stream was closed as idle: %#v", detail)
	}
}

func executeBufferedStreamForTest(t *testing.T, plugin *Plugin, req execapp.Request) (bool, execapp.StreamResponse, string) {
	t.Helper()
	encoded, errMarshal := json.Marshal(req)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	raw, errExecute := plugin.ExecuteStream(encoded)
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	var envelope struct {
		OK     bool                   `json:"ok"`
		Result execapp.StreamResponse `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	return envelope.OK, envelope.Result, string(raw)
}

func modelCallAuthIDs(host *mockHostCall) []string {
	host.mu.Lock()
	defer host.mu.Unlock()
	ids := make([]string, len(host.modelCalls))
	for index, call := range host.modelCalls {
		ids[index] = call.AuthID
	}
	return ids
}

func waitForModelCalls(t *testing.T, host *mockHostCall, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		got := len(host.modelCalls)
		host.mu.Unlock()
		if got >= count {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d model calls: %#v", count, modelCallAuthIDs(host))
}

func TestTransparentStreamOpenIdleTimeoutFailsOverBeforeFirstChunk(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
	})
	setTransparentTimingForTest(plugin, 0, 60*time.Millisecond)

	innerHostCall := hostCall
	releaseOpen := make(chan struct{})
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelExecuteStream {
			if req, ok := payload.(hostModelExecutionRequest); ok && req.AuthID == "auth-1" {
				<-releaseOpen
			}
		}
		return innerHostCall(method, payload)
	}
	defer func() {
		close(releaseOpen)
		waitForModelCalls(t, host, 2)
		hostCall = innerHostCall
	}()

	streamID := "plugin-stream-open-idle-failover"
	done := make(chan struct{})
	go func() {
		plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream open silence was not bounded by stream_idle_timeout")
	}

	host.mu.Lock()
	emitted := append([][]byte(nil), host.emitted[streamID]...)
	closeDetail := host.closes[streamID]
	host.mu.Unlock()
	var joined strings.Builder
	for _, payload := range emitted {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("healthy account was not delivered after stream-open timeout: %s", joined.String())
	}
	if closeDetail != nil {
		t.Fatalf("stream-open timeout closed the client stream: %#v", closeDetail)
	}

	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.LastAttempt == nil || account.LastAttempt.Class != "probe_error" || account.LastAttempt.Reason != "reason.detail.upstream_stream_idle_timeout" {
		t.Fatalf("stream-open timeout was not recorded as a provider transport failure: %#v", account.LastAttempt)
	}
}

func TestTransparentStreamOpenIdleTimeoutDoesNotFailOpen(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.OnExhausted = "fail_unverified"
	})
	setTransparentTimingForTest(plugin, 0, 60*time.Millisecond)

	innerHostCall := hostCall
	releaseOpen := make(chan struct{})
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelExecuteStream {
			<-releaseOpen
		}
		return innerHostCall(method, payload)
	}
	defer func() {
		close(releaseOpen)
		waitForModelCalls(t, host, 1)
		hostCall = innerHostCall
	}()

	streamID := "plugin-stream-open-idle-fail-closed"
	done := make(chan struct{})
	go func() {
		plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("single silent stream did not end within stream_idle_timeout")
	}

	host.mu.Lock()
	emitted := len(host.emitted[streamID])
	closeDetail := host.closes[streamID]
	host.mu.Unlock()
	if emitted != 0 {
		t.Fatalf("fail_open emitted empty content after stream-open timeout: %d chunks", emitted)
	}
	if closeDetail == nil || closeDetail.Code != "quality_degraded" || closeDetail.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("stream-open timeout did not fail closed: %#v", closeDetail)
	}
}

func TestTransparentBufferedStreamOpenIdleTimeoutFailsOver(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
	})
	setTransparentTimingForTest(plugin, 0, 60*time.Millisecond)

	innerHostCall := hostCall
	releaseOpen := make(chan struct{})
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelExecuteStream {
			if req, ok := payload.(hostModelExecutionRequest); ok && req.AuthID == "auth-1" {
				<-releaseOpen
			}
		}
		return innerHostCall(method, payload)
	}
	defer func() {
		close(releaseOpen)
		waitForModelCalls(t, host, 2)
		hostCall = innerHostCall
	}()

	req := responsesRequestForTest("grok-4.6", true)
	req.StreamID = ""
	ok, result, raw := executeBufferedStreamForTest(t, plugin, req)
	if !ok {
		t.Fatalf("buffered stream-open timeout did not fail over: %s", raw)
	}
	var joined strings.Builder
	for _, chunk := range result.Chunks {
		joined.Write(chunk.Payload)
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("buffered stream-open timeout did not deliver the healthy account response: %s", joined.String())
	}
}

func TestTransparentBufferedStreamReadIdleTimeoutFailsOver(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_idle\",\"model\":\"grok-4.6\"}}\n\n"))
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
	})
	setTransparentTimingForTest(plugin, 0, 60*time.Millisecond)

	innerHostCall := hostCall
	releaseRead := make(chan struct{})
	var readCount atomic.Int32
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamRead && readCount.Add(1) == 2 {
			<-releaseRead
		}
		return innerHostCall(method, payload)
	}
	defer func() {
		close(releaseRead)
		hostCall = innerHostCall
	}()

	req := responsesRequestForTest("grok-4.6", true)
	req.StreamID = ""
	ok, result, raw := executeBufferedStreamForTest(t, plugin, req)
	if !ok {
		t.Fatalf("buffered read idle timeout did not fail over: %s", raw)
	}
	var joined strings.Builder
	for _, chunk := range result.Chunks {
		joined.Write(chunk.Payload)
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("buffered read idle timeout did not deliver the healthy account response: %s", joined.String())
	}
}

func TestTransparentStreamReasoningStubsDoNotResetIdleTimeout(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	stubs := make([]pluginapi.HostModelStreamReadResponse, 0, 100)
	for index := 0; index < 100; index++ {
		stubs = append(stubs, streamData(": grok2api-reasoning-start\n\n"))
	}
	host.scriptStream("auth-1", stubs...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.Quality.OnExhausted = "fail_unverified"
	})
	setTransparentTimingForTest(plugin, 10*time.Millisecond, 60*time.Millisecond)

	innerHostCall := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamRead {
			time.Sleep(10 * time.Millisecond)
		}
		return innerHostCall(method, payload)
	}
	defer func() { hostCall = innerHostCall }()

	streamID := "plugin-stream-stub-keepalive"
	done := make(chan struct{})
	go func() {
		plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reasoning stubs kept a stalled stream alive past stream_idle_timeout")
	}

	host.mu.Lock()
	emitted := len(host.emitted[streamID])
	closeDetail := host.closes[streamID]
	host.mu.Unlock()
	if emitted != 0 {
		t.Fatalf("reasoning stubs were emitted after idle timeout: %d chunks", emitted)
	}
	if closeDetail == nil || closeDetail.Code != "quality_degraded" || closeDetail.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("reasoning-stub idle stream did not fail closed: %#v", closeDetail)
	}
}

func TestTransparentBufferedUnsafeReadIdleTimeoutFailsClosed(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_idle\",\"model\":\"grok-4.6\"}}\n\n"))
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
	})
	setTransparentTimingForTest(plugin, 0, 60*time.Millisecond)

	innerHostCall := hostCall
	releaseRead := make(chan struct{})
	var readCount atomic.Int32
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamRead && readCount.Add(1) == 2 {
			<-releaseRead
		}
		return innerHostCall(method, payload)
	}
	defer func() {
		close(releaseRead)
		hostCall = innerHostCall
	}()

	req := responsesRequestForTest("grok-4.6", true)
	req.StreamID = ""
	req.OriginalRequest = []byte(`{"model":"grok-4.6","stream":true,"input":"hi","previous_response_id":"resp_1","reasoning":{"effort":"medium"}}`)
	ok, _, raw := executeBufferedStreamForTest(t, plugin, req)
	if ok {
		t.Fatalf("unsafe buffered stream unexpectedly failed over after idle timeout: %s", raw)
	}
	if calls := modelCallAuthIDs(host); len(calls) != 1 || calls[0] != "auth-1" {
		t.Fatalf("unsafe buffered idle timeout used %d attempts: %#v", len(calls), calls)
	}
}

func TestTransparentDegradedVisibleStreamFailsOverBeforeHoldTimeout(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	degraded := []pluginapi.HostModelStreamReadResponse{
		streamData(": grok2api-reasoning-start\n\n"),
		streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_degraded\",\"model\":\"grok-4.6\"}}\n\n"),
		streamData("data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"rs_degraded\",\"type\":\"reasoning\",\"summary\":[]}}\n\n"),
	}
	for index := 0; index < 8; index++ {
		degraded = append(degraded, streamData("data: {\"type\":\"response.output_text.delta\",\"delta\":\"word \"}\n\n"))
	}
	// A stalled degraded account keeps sending reasoning stubs: the stream never
	// terminates and never produces thinking evidence.
	for index := 0; index < 2000; index++ {
		degraded = append(degraded, streamData(": grok2api-reasoning-start\n\n"))
	}
	host.scriptStream("auth-1", degraded...)
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		// Both windows stay far beyond the assertion deadline: only an early
		// missing-thinking judgment can release the client this quickly.
	})
	setTransparentTimingForTest(plugin, 30*time.Second, 30*time.Second)

	innerHostCall := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamRead {
			time.Sleep(20 * time.Millisecond)
		}
		return innerHostCall(method, payload)
	}
	defer func() { hostCall = innerHostCall }()

	streamID := "plugin-stream-degraded-visible"
	done := make(chan struct{})
	go func() {
		plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		host.mu.Lock()
		calls := len(host.modelCalls)
		emitted := len(host.emitted[streamID])
		closeDetail := host.closes[streamID]
		host.mu.Unlock()
		t.Fatalf("degraded visible stream kept the client waiting for the hold timeout (calls=%d emitted=%d close=%#v auths=%v)", calls, emitted, closeDetail, modelCallAuthIDs(host))
	}

	host.mu.Lock()
	emitted := append([][]byte(nil), host.emitted[streamID]...)
	closeDetail := host.closes[streamID]
	host.mu.Unlock()
	var joined strings.Builder
	for _, payload := range emitted {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "good answer") {
		t.Fatalf("degraded failover did not deliver the healthy account response: %s", joined.String())
	}
	if strings.Contains(joined.String(), "word word word") {
		t.Fatalf("degraded content leaked to the client: %s", joined.String())
	}
	if closeDetail != nil {
		t.Fatalf("degraded failover closed the client stream with error: %#v", closeDetail)
	}
	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.LastAttempt == nil || account.LastAttempt.Class != "missing_thinking" {
		t.Fatalf("degraded attempt was not recorded as missing_thinking: %#v", account.LastAttempt)
	}
}

func TestTransparentFailoverDoesNotWaitForAccountLock(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	host.scriptStream("auth-1", badResponsesStreamScript()...)
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	if plugin.accountLock("auth-1") == plugin.accountLock("auth-2") {
		t.Fatal("test accounts share a lock stripe")
	}

	// A probe holds the account lock for its whole run. Verdict bookkeeping for
	// the failed attempt needs the same lock, so it must not run on the request
	// path or the client waits for the probe before the next account starts.
	lock := plugin.accountLock("auth-1")
	lock.Lock()
	locked := true
	defer func() {
		if locked {
			lock.Unlock()
		}
	}()

	streamID := "plugin-stream-locked-failover"
	done := make(chan struct{})
	go func() {
		plugin.executorSvc.RunTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("failover waited for the account lock of the failed attempt (auths=%v)", modelCallAuthIDs(host))
	}

	host.mu.Lock()
	emitted := append([][]byte(nil), host.emitted[streamID]...)
	closeDetail := host.closes[streamID]
	host.mu.Unlock()
	var joined strings.Builder
	for _, payload := range emitted {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "good answer") || closeDetail != nil {
		t.Fatalf("failover did not deliver the healthy response: close=%#v output=%s", closeDetail, joined.String())
	}

	lock.Unlock()
	locked = false
	plugin.waitSettled()
	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.LastAttempt == nil || account.LastAttempt.Class != "missing_thinking" {
		t.Fatalf("settled verdict was not recorded for the failed attempt: %#v", account.LastAttempt)
	}
}

func TestTransparentToolCallPreambleWithEncryptedReasoningIsDelivered(t *testing.T) {
	entries, auths := twoGrokAuths(t)
	host := newMockHostCall(entries, auths)
	encrypted := strings.Repeat("A", 3000)
	// A Codex agent turn: hidden reasoning, a short preamble, then a tool call,
	// all flushed in one burst once the reasoning finishes.
	host.scriptStream("auth-1",
		streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_tool\",\"model\":\"grok-4.6\"}}\n\n"),
		streamData("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[]}}\n\n"),
		streamData("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[],\"encrypted_content\":\""+encrypted+"\"}}\n\n"),
		streamData("data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"),
		streamData("data: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"先看一下\"}\n\n"),
		streamData("data: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"目录结构\"}\n\n"),
		streamData("data: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"exec_command\",\"arguments\":\"\"}}\n\n"),
		streamData("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":2,\"delta\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}\n\n"),
		streamData("data: {\"type\":\"response.output_item.done\",\"output_index\":2,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"exec_command\",\"arguments\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}}\n\n"),
		streamData("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_tool\",\"usage\":{\"output_tokens\":204,\"output_tokens_details\":{\"reasoning_tokens\":190}}}}\n\n"),
		pluginapi.HostModelStreamReadResponse{Done: true},
	)
	host.scriptStream("auth-2", goodResponsesStreamScript()...)
	plugin := newTestPlugin(t, host, enableTransparentForTest)
	streamID := "plugin-stream-tool-call"
	plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.6", true), streamID)

	host.mu.Lock()
	emitted := append([][]byte(nil), host.emitted[streamID]...)
	closeDetail := host.closes[streamID]
	host.mu.Unlock()
	var joined strings.Builder
	for _, payload := range emitted {
		joined.Write(payload)
	}
	if !strings.Contains(joined.String(), "exec_command") || !strings.Contains(joined.String(), "response.completed") || closeDetail != nil {
		t.Fatalf("tool call turn was not delivered: close=%#v output=%s", closeDetail, joined.String())
	}
	if calls := modelCallAuthIDs(host); len(calls) != 1 || calls[0] != "auth-1" {
		t.Fatalf("healthy tool call turn failed over: %v", calls)
	}
	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.LastAttempt == nil || account.LastAttempt.Class != "" || account.State == appstate.StateCooling {
		t.Fatalf("healthy tool call turn was penalized: state=%s attempt=%#v", account.State, account.LastAttempt)
	}
}

// TestTransparentToolCallWithoutReasoningFailsOver replays the field reports:
// Codex continuation turns on grok-4.7 returned function calls with no
// reasoning bill and no evidence, while healthy turns of the same size carried
// 245-294 reasoning tokens. Delivering them let an agent run for nine minutes
// without reasoning until the quota ran out. grok2api keeps such 0-thinking
// tool turns behind the gate, and so does the plugin.
func TestTransparentToolCallWithoutReasoningFailsOver(t *testing.T) {
	scripts := map[string][]pluginapi.HostModelStreamReadResponse{
		"bare call": {
			streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_call\",\"model\":\"grok-4.7\"}}\n\n"),
			streamData("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"exec_command\",\"arguments\":\"\"}}\n\n"),
			streamData("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"delta\":\"{\\\"cmd\\\":\\\"sed -n 1,200p internal/quality/verdict.go\\\"}\"}\n\n"),
			streamData("data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"exec_command\",\"arguments\":\"{\\\"cmd\\\":\\\"sed -n 1,200p internal/quality/verdict.go\\\"}\"}}\n\n"),
			streamData("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_call\",\"usage\":{\"output_tokens\":75,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n"),
			{Done: true},
		},
		"preamble after empty reasoning item": {
			streamData("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_call\",\"model\":\"grok-4.7\"}}\n\n"),
			streamData("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"rs_1\",\"type\":\"reasoning\",\"summary\":[]}}\n\n"),
			streamData("data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n\n"),
			streamData("data: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"Let me read the verdict \"}\n\n"),
			streamData("data: {\"type\":\"response.output_text.delta\",\"output_index\":1,\"delta\":\"code before changing it.\"}\n\n"),
			streamData("data: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"exec_command\",\"arguments\":\"\"}}\n\n"),
			streamData("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":2,\"delta\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}\n\n"),
			streamData("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_call\",\"usage\":{\"output_tokens\":61,\"output_tokens_details\":{\"reasoning_tokens\":0}}}}\n\n"),
			{Done: true},
		},
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			entries, auths := twoGrokAuths(t)
			host := newMockHostCall(entries, auths)
			host.scriptStream("auth-1", script...)
			host.scriptStream("auth-2", goodResponsesStreamScript()...)
			plugin := newTestPlugin(t, host, enableTransparentForTest)
			streamID := "plugin-stream-no-reasoning-call"
			plugin.runTransparentStreamBridge(context.Background(), responsesRequestForTest("grok-4.7", true), streamID)
			plugin.waitSettled()

			host.mu.Lock()
			emitted := append([][]byte(nil), host.emitted[streamID]...)
			closeDetail := host.closes[streamID]
			host.mu.Unlock()
			var joined strings.Builder
			for _, payload := range emitted {
				joined.Write(payload)
			}
			if strings.Contains(joined.String(), "exec_command") || strings.Contains(joined.String(), "resp_call") {
				t.Fatalf("no-reasoning tool call leaked to the client: %s", joined.String())
			}
			if !strings.Contains(joined.String(), "good answer") || closeDetail != nil {
				t.Fatalf("reasoning account was not delivered: close=%#v output=%s", closeDetail, joined.String())
			}
			if calls := modelCallAuthIDs(host); len(calls) != 2 || calls[0] != "auth-1" || calls[1] != "auth-2" {
				t.Fatalf("no-reasoning tool call attempts = %v, want auth-1 then auth-2", calls)
			}
			account, ok := plugin.stateAccount("auth-1")
			if !ok || account.LastAttempt == nil || account.LastAttempt.Class != "missing_thinking" {
				t.Fatalf("no-reasoning tool call was not judged missing_thinking: %#v", account.LastAttempt)
			}
		})
	}
}
