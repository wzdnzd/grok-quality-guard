package app

import (
	"context"
	"encoding/json"
	managementapp "grok-quality-guard/internal/app/management"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestIgnoredAndProbeErrorPreserveQualityEvidence(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(config *appconfig.Config) { config.AutoAction = "observe" })
	initial := qualitypkg.QualityVerdict{
		Class:  qualitypkg.ClassMissingThinking,
		Reason: "reason.detail.no_real_reasoning_evidence",
		Signals: qualitypkg.Signals{
			OutputTokens: 100, VisibleTokens: 100, FirstTokenMS: 100, DurationMS: 1000,
			GenerationMS: 900, TPS: 111.1, Terminal: true, Streaming: true,
		},
	}
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", initial, "passive", ""); err != nil {
		t.Fatal(err)
	}
	before, ok := plugin.stateAccount("auth-1")
	if !ok || before.LastEvidence == nil {
		t.Fatalf("initial quality evidence missing: %#v", before)
	}
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassIgnored, Reason: "reason.detail.unsuccessful"}, "passive", ""); err != nil {
		t.Fatal(err)
	}
	afterIgnored, _ := plugin.stateAccount("auth-1")
	if afterIgnored.LastAttempt == nil || afterIgnored.LastAttempt.Class != qualitypkg.ClassIgnored {
		t.Fatalf("ignored attempt was not recorded: %#v", afterIgnored.LastAttempt)
	}
	if !reflect.DeepEqual(afterIgnored.LastEvidence, before.LastEvidence) || !reflect.DeepEqual(afterIgnored.Metrics, before.Metrics) {
		t.Fatalf("ignored attempt overwrote quality evidence: before=%#v after=%#v", before, afterIgnored)
	}
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{Class: qualitypkg.ClassProbeError, Reason: "reason.detail.probe_transport_error"}, "probe", ""); err != nil {
		t.Fatal(err)
	}
	afterError, _ := plugin.stateAccount("auth-1")
	if afterError.LastAttempt == nil || afterError.LastAttempt.Class != qualitypkg.ClassProbeError {
		t.Fatalf("probe error attempt was not recorded: %#v", afterError.LastAttempt)
	}
	if !reflect.DeepEqual(afterError.LastEvidence, before.LastEvidence) || !reflect.DeepEqual(afterError.Metrics, before.Metrics) {
		t.Fatalf("probe error overwrote quality evidence: before=%#v after=%#v", before, afterError)
	}
	if afterError.LastReason != qualitypkg.ClassMissingThinking {
		t.Fatalf("last quality reason changed to %q", afterError.LastReason)
	}
}

func TestPassiveUsageFailureFiltering(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)

	nonGrok, errMarshal := json.Marshal(pluginapi.UsageRecord{
		AuthID: "auth-1", RequestID: "non-grok", Provider: "openai", Model: "gpt-5", Failed: true,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.HandleUsage(nonGrok)
	account, _ := plugin.stateAccount("auth-1")
	if account.LastAttempt != nil {
		t.Fatalf("non-Grok failure was recorded as an attempt: %#v", account.LastAttempt)
	}

	grokFailure, errMarshal := json.Marshal(pluginapi.UsageRecord{
		AuthID: "auth-1", RequestID: "grok-failure", Provider: "xai", Model: "grok-4.5", Failed: true,
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.HandleUsage(grokFailure)
	account, _ = plugin.stateAccount("auth-1")
	if account.LastAttempt == nil || account.LastAttempt.Class != qualitypkg.ClassIgnored || account.LastAttempt.Reason != "reason.detail.unsuccessful" {
		t.Fatalf("Grok failure was not recorded as ignored: %#v", account.LastAttempt)
	}
	if account.ErrorStrikes != 0 || account.LastEvidence != nil {
		t.Fatalf("Grok failure changed quality evidence: %#v", account)
	}
}

func TestAccountViewExposesQualityEvidence(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(config *appconfig.Config) { config.AutoAction = "observe" })
	verdict := qualitypkg.QualityVerdict{
		Class:  qualitypkg.ClassMissingThinking,
		Reason: "reason.detail.no_real_reasoning_evidence",
		Signals: qualitypkg.Signals{
			OutputTokens: 100, VisibleTokens: 100, ReasoningTokens: 20, EncryptedBytes: 200,
			EncryptedFloor: 256, FirstTokenMS: 100, DurationMS: 1000, GenerationMS: 900,
			TPS: 111.1, Terminal: true, Streaming: true, Protocol: qualitypkg.ProtocolResponses,
		},
	}
	if err := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); err != nil {
		t.Fatal(err)
	}
	response := actionManagementForTest(t, plugin, "accounts.list", map[string]any{"page_size": 10})
	if response.StatusCode != 200 {
		t.Fatalf("accounts.list status = %d body=%s", response.StatusCode, response.Body)
	}
	var decoded struct {
		Data struct {
			Items []managementapp.AccountView `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Data.Items) != 1 {
		t.Fatalf("unexpected account count: %d", len(decoded.Data.Items))
	}
	item := decoded.Data.Items[0]
	if item.LastAttempt == nil || item.LastAttempt.Class != qualitypkg.ClassMissingThinking {
		t.Fatalf("last attempt missing from account view: %#v", item.LastAttempt)
	}
	if item.Evidence == nil || item.Evidence.Class != qualitypkg.ClassMissingThinking || item.Evidence.Protocol != qualitypkg.ProtocolResponses {
		t.Fatalf("quality evidence missing from account view: %#v", item.Evidence)
	}
	if strings.TrimSpace(item.Evidence.Reason) == "" || item.Evidence.OutputTokens != 100 || item.Evidence.EncryptedFloor != 256 {
		t.Fatalf("quality evidence fields incomplete: %#v", item.Evidence)
	}
}

func TestHoldExpiredBurstRequiresReasoningBill(t *testing.T) {
	cipher := qualitypkg.Signals{
		HoldExpired: true, FirstVisible: true, VisibleFlushMS: 1500, VisibleTokens: 4,
		EncryptedBytes: 300, EncryptedFloor: 256,
	}
	if qualityIsBurstDump(cipher, 8) {
		t.Fatalf("cipher floor alone must not satisfy hold-expired burst reasoning bill: %#v", cipher)
	}
	cipher.ReasoningTokens = 80
	if !qualityIsBurstDump(cipher, 8) {
		t.Fatalf("reasoning bill at hold expiry should trigger burst: %#v", cipher)
	}
}

func TestPassiveAuditFlagsMissingThinkingForReasoningRequest(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	signals := qualitypkg.Signals{
		Protocol:          qualitypkg.ProtocolResponses,
		Streaming:         true,
		Terminal:          true,
		FirstTokenKnown:   true,
		FirstTokenMS:      1700,
		DurationMS:        3554,
		OutputTokens:      99,
		ReasoningTokens:   0,
		VisibleTokens:     0,
		UsageReported:     true,
		ReasoningRequired: true,
	}
	verdict := classifyPassiveAudit(signals, config)
	if verdict.Class != qualitypkg.ClassMissingThinking {
		t.Fatalf("class = %q, want %q (%#v)", verdict.Class, qualitypkg.ClassMissingThinking, verdict.Signals)
	}
}

func TestProbeWindowDoesNotDropRealPassiveUsage(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)

	initRequest, errMarshal := json.Marshal(streamChunkInterceptRequestForTest{
		StreamChunkInterceptRequest: pluginapi.StreamChunkInterceptRequest{
			RequestID:       "real-request",
			RequestedModel:  "grok-4.7",
			OriginalRequest: []byte(`{"model":"grok-4.7","reasoning":{"effort":"xhigh"}}`),
			ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
		},
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.InterceptStreamChunk(initRequest)
	chunkRequest, errMarshal := json.Marshal(streamChunkInterceptRequestForTest{
		StreamChunkInterceptRequest: pluginapi.StreamChunkInterceptRequest{
			RequestID:      "real-request",
			RequestedModel: "grok-4.7",
			Body:           []byte(`data: {"type":"response.output_text.delta","delta":"` + strings.Repeat("visible ", 64) + `"}` + "\n\n"),
			ChunkIndex:     0,
		},
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.InterceptStreamChunk(chunkRequest)
	plugin.markProbeAuth("auth-1", 3*time.Minute)

	record := pluginapi.UsageRecord{
		AuthID:          "auth-1",
		RequestID:       "real-request",
		Provider:        "xai",
		Model:           "grok-4.7",
		ReasoningEffort: "xhigh",
		Stream:          true,
		TTFT:            1700 * time.Millisecond,
		Latency:         3554 * time.Millisecond,
		Detail: pluginapi.UsageDetail{
			OutputTokens:    99,
			ReasoningTokens: 0,
		},
	}
	raw, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.HandleUsage(raw)

	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.LastAttempt == nil {
		t.Fatalf("real usage during probe window was not recorded: %#v", account)
	}
	if account.LastAttempt.Class != qualitypkg.ClassMissingThinking {
		t.Fatalf("real usage class = %q, want %q", account.LastAttempt.Class, qualitypkg.ClassMissingThinking)
	}
}

func TestPassiveUsageFlagsReasoningRequestWithoutThinking(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)

	record := pluginapi.UsageRecord{
		AuthID:          "auth-1",
		RequestID:       "passive-responses-missing-thinking",
		Provider:        "xai",
		Model:           "grok-4.7",
		ReasoningEffort: "xhigh",
		Stream:          true,
		TTFT:            1700 * time.Millisecond,
		Latency:         3554 * time.Millisecond,
		Detail: pluginapi.UsageDetail{
			OutputTokens:    99,
			ReasoningTokens: 0,
		},
	}
	raw, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin.HandleUsage(raw)

	account, ok := plugin.stateAccount("auth-1")
	if !ok || account.LastAttempt == nil {
		t.Fatalf("passive usage was not recorded: %#v", account)
	}
	if account.LastAttempt.Class != qualitypkg.ClassMissingThinking {
		t.Fatalf("passive class = %q, want %q", account.LastAttempt.Class, qualitypkg.ClassMissingThinking)
	}
	if account.LastEvidence == nil || !account.LastEvidence.ReasoningRequired {
		t.Fatalf("passive evidence did not preserve reasoning requirement: %#v", account.LastEvidence)
	}
}

func TestStreamingSemanticOnlyOutputBypassesMissingThinking(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	config.SoftTPS = 1e9
	config.HardTPS = 2e9
	analyzer := NewStreamAnalyzerWithProtocol(qualitypkg.ProtocolResponses)
	encrypted := strings.Repeat("A", 1331)
	analyzer.ObserveChunk([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":"` + encrypted + `"}}` + "\n\n"))
	analyzer.ObserveChunk([]byte(`data: {"type":"response.output_item.done","item":{"id":"call_1","type":"function_call","call_id":"call_1","name":"exec","arguments":"{}"}}` + "\n\n"))
	analyzer.ObserveChunk([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"output_tokens":421,"output_tokens_details":{"reasoning_tokens":355}}}}` + "\n\n"))
	signals := analyzer.Finalize(true)
	if signals.HasThinking || !signals.SemanticOutput || signals.VisibleTokens != 0 {
		t.Fatalf("unexpected semantic-only signals: %#v", signals)
	}
	verdict := classifyFinal(signals, config, true, 8, true)
	if verdict.Class != qualitypkg.ClassNone || verdict.Action != qualitypkg.QualityActionDeliver {
		t.Fatalf("semantic-only response was rejected: %#v", verdict)
	}
	if !verdict.Signals.ReasoningRequired {
		t.Fatalf("reasoning requirement was not preserved in evidence: %#v", verdict.Signals)
	}
	if verdict.Signals.EncryptedFloor != 1420 {
		t.Fatalf("encrypted floor = %d, want 1420", verdict.Signals.EncryptedFloor)
	}
}

func TestThinkingGuardDisabledBypassesMissingThinkingInAllQualityPaths(t *testing.T) {
	config := appconfig.DefaultQualityConfig()
	config.ThinkingGuard = false
	config.SoftTPS = 1e9
	config.HardTPS = 2e9
	signals := qualitypkg.Signals{
		Streaming:       true,
		Terminal:        true,
		FirstTokenKnown: true,
		FirstTokenMS:    100,
		DurationMS:      2000,
		OutputTokens:    100,
		VisibleTokens:   100,
	}
	if verdict := classifyFinal(signals, config, true, 8, true); verdict.Class != qualitypkg.ClassNone {
		t.Fatalf("stream thinking_guard=false class = %q, want none (%#v)", verdict.Class, verdict)
	}
	if verdict := classifyNonStreamResult(signals, config, true, 8); verdict.Class != qualitypkg.ClassNone {
		t.Fatalf("non-stream thinking_guard=false class = %q, want none (%#v)", verdict.Class, verdict)
	}
	passive := signals
	passive.ReasoningRequired = true
	passive.FirstTokenMS = 100
	passive.DurationMS = 2000
	if verdict := classifyPassiveAudit(passive, config); verdict.Class != qualitypkg.ClassNone {
		t.Fatalf("passive thinking_guard=false class = %q, want none (%#v)", verdict.Class, verdict)
	}
}
