package quality

import (
	"strings"
	"testing"
	"time"

	"grok-quality-guard/internal/config"
)

func TestGenerationWindowCorrectionBoundaries(t *testing.T) {
	cases := []struct {
		name        string
		duration    int64
		first       int64
		reasoning   int64
		wantArrival int64
		wantGen     int64
	}{
		{name: "short reasoning window uses full duration", duration: 1200, first: 1000, reasoning: 10, wantArrival: 1200, wantGen: 1200},
		{name: "long reasoning window subtracts ttft", duration: 3000, first: 1000, reasoning: 10, wantArrival: 2000, wantGen: 2000},
		{name: "burst without reasoning keeps the arrival tail only", duration: 1200, first: 1000, reasoning: 0, wantArrival: 200, wantGen: 1200},
		{name: "window equal to ttft does not correct", duration: 2000, first: 1000, reasoning: 10, wantArrival: 1000, wantGen: 1000},
		{name: "fast prefill keeps a short streamed tail", duration: 250, first: 50, reasoning: 0, wantArrival: 200, wantGen: 200},
		{name: "everything in the first chunk has no window", duration: 900, first: 900, reasoning: 0, wantArrival: 0, wantGen: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := arrivalWindowMS(tc.duration, tc.first, tc.reasoning); got != tc.wantArrival {
				t.Fatalf("arrival window = %d, want %d", got, tc.wantArrival)
			}
			if got := generationWindowMS(tc.duration, tc.first); got != tc.wantGen {
				t.Fatalf("generation window = %d, want %d", got, tc.wantGen)
			}
		})
	}
}

func TestEncryptedContentDeduplication(t *testing.T) {
	// One copy stays below the 3000-byte floor, while a double count would
	// clear it, so the test fails if the repeated item is summed.
	encrypted := strings.Repeat("A", 2500)
	analyzer := NewStreamAnalyzer()
	payload := `data: {"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning","encrypted_content":"` + encrypted + `"}}` + "\n\n" +
		`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":"` + encrypted + `"}}` + "\n\n" +
		`data: {"type":"response.completed","response":{"usage":{"output_tokens":1600,"output_tokens_details":{"reasoning_tokens":1500}}}}` + "\n\n"
	analyzer.ObserveChunk([]byte(payload))
	sig := analyzer.Finalize(true)
	if sig.EncryptedBytes != 2500 {
		t.Fatalf("encrypted bytes = %d, want 2500 after deduplication", sig.EncryptedBytes)
	}
	if sig.EncryptedFloor != 3000 {
		t.Fatalf("encrypted floor = %d, want 3000", sig.EncryptedFloor)
	}
	if sig.HasThinking {
		t.Fatal("duplicated encrypted stub incorrectly satisfied the 3000-byte floor")
	}
}

func TestAnthropicThinkingStream(t *testing.T) {
	analyzer := NewStreamAnalyzer()
	analyzer.ObserveChunk([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n"))
	analyzer.ObserveChunk([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan carefully\"}}\n\n"))
	analyzer.ObserveChunk([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"final answer\"}}\n\n"))
	analyzer.ObserveChunk([]byte("event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":30,\"output_tokens_details\":{\"thinking_tokens\":12}}}\n\n"))
	analyzer.ObserveChunk([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	sig := analyzer.Finalize(true)
	if !sig.HasThinking || !sig.HasReasoningDelta || !sig.Terminal {
		t.Fatalf("unexpected Anthropic signals: %#v", sig)
	}
	if sig.VisibleTokens != 3 || sig.OutputTokens != 30 || sig.ReasoningTokens != 12 {
		t.Fatalf("unexpected Anthropic counters: %#v", sig)
	}
	config := config.DefaultQualityConfig()
	config.SoftTPS = 1e9
	config.HardTPS = 2e9
	if verdict := classifyFinal(sig, config, true, 8, true); verdict.Class != ClassNone {
		t.Fatalf("unexpected verdict: %#v", verdict)
	}
}

func TestResponsesEncryptedThinking(t *testing.T) {
	encrypted := strings.Repeat("B", 4000)
	analyzer := NewStreamAnalyzer()
	analyzer.ObserveChunk([]byte(`data: {"type":"response.output_item.done","item":{"id":"rs_1","type":"reasoning","encrypted_content":"` + encrypted + `"}}` + "\n\n"))
	analyzer.ObserveChunk([]byte(`data: {"type":"response.output_text.delta","delta":"visible"}` + "\n\n"))
	analyzer.ObserveChunk([]byte(`data: {"type":"response.completed","response":{"usage":{"output_tokens":1100,"output_tokens_details":{"reasoning_tokens":1000}}}}` + "\n\n"))
	sig := analyzer.Finalize(true)
	if !sig.HasThinking {
		t.Fatalf("4000 encrypted bytes did not satisfy the floor: %#v", sig)
	}
	if verdict := classifyFinal(sig, config.DefaultQualityConfig(), true, 8, true); verdict.Class == ClassMissingThinking {
		t.Fatalf("encrypted thinking was incorrectly classified as missing: %#v", verdict)
	}
}

func TestReasoningStartCommentDoesNotHideDataInSameEvent(t *testing.T) {
	analyzer := NewStreamAnalyzer()
	analyzer.ObserveChunk([]byte(": grok2api-reasoning-start\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
	sig := analyzer.Finalize(true)
	if sig.VisibleTokens == 0 {
		t.Fatal("visible data after a reasoning-start comment was ignored")
	}
}
func TestPassiveTPSSkipsSamplesBelowMinimumOutput(t *testing.T) {
	config := config.DefaultQualityConfig()
	small := classifyFinal(Signals{
		Terminal:     true,
		FirstTokenMS: 100,
		DurationMS:   200,
		OutputTokens: 10,
	}, config, false, config.MinPassiveOutputTokens, false)
	if small.Class != ClassNone {
		t.Fatalf("small sample class = %q, want none", small.Class)
	}
	large := classifyFinal(Signals{
		Terminal:     true,
		FirstTokenMS: 100,
		DurationMS:   200,
		OutputTokens: 100,
	}, config, false, config.MinPassiveOutputTokens, false)
	if large.Class != ClassHardTPS {
		t.Fatalf("large sample class = %q, want hard_tps", large.Class)
	}
}

func TestReasoningStubSettlesMissingThinkingOnceVisibleOutputStreams(t *testing.T) {
	streaming := Signals{
		ReasoningStarted: true,
		FirstVisible:     true,
		VisibleTokens:    64,
		VisibleSpanMS:    600,
		VisibleChunks:    8,
	}
	if verdict := ClassifyQualityHold(streaming, 8); verdict.Action != QualityActionWithhold || verdict.Class != ClassMissingThinking {
		t.Fatalf("stub stream with settled visible output = %#v, want missing_thinking withhold", verdict)
	}
	stubOnly := streaming
	stubOnly.VisibleTokens = 0
	stubOnly.VisibleSpanMS = 0
	stubOnly.VisibleChunks = 0
	if verdict := ClassifyQualityHold(stubOnly, 8); verdict.Action != QualityActionWait {
		t.Fatalf("stub-only stream = %#v, want wait", verdict)
	}
	pending := streaming
	pending.VisibleTokens = 3
	pending.VisibleSpanMS = 40
	pending.VisibleChunks = 1
	if verdict := ClassifyQualityHold(pending, 8); verdict.Action != QualityActionWait {
		t.Fatalf("visible output below the hold floor = %#v, want wait", verdict)
	}
	starting := streaming
	starting.VisibleSpanMS = 20
	starting.VisibleChunks = 1
	if verdict := ClassifyQualityHold(starting, 8); verdict.Action != QualityActionWait {
		t.Fatalf("first visible burst without a settled window = %#v, want wait", verdict)
	}
	twoChunks := streaming
	twoChunks.VisibleSpanMS = 20
	twoChunks.VisibleChunks = 2
	if verdict := ClassifyQualityHold(twoChunks, 8); verdict.Class != ClassMissingThinking {
		t.Fatalf("two visible chunks did not settle = %#v, want missing_thinking", verdict)
	}
	spanOnly := streaming
	spanOnly.VisibleSpanMS = 40
	spanOnly.VisibleChunks = 1
	if verdict := ClassifyQualityHold(spanOnly, 8); verdict.Class != ClassMissingThinking {
		t.Fatalf("visible span did not settle = %#v, want missing_thinking", verdict)
	}
	withThinking := streaming
	withThinking.HasThinking = true
	withThinking.HasReasoningDelta = true
	if verdict := ClassifyQualityHold(withThinking, 8); verdict.Action != QualityActionDeliver {
		t.Fatalf("reasoning delta stream = %#v, want deliver", verdict)
	}
}

func TestStreamProgressEventsTrackGeneratedOutputOnly(t *testing.T) {
	analyzer := NewStreamAnalyzerWithProtocol(qualityProtocolResponses)
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"))
	analyzer.ObserveChunk([]byte(": grok2api-reasoning-start\n\n"))
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"reasoning\",\"summary\":[]}}\n\n"))
	if got := analyzer.Preview().ProgressEvents; got != 0 {
		t.Fatalf("control frames advanced progress: %d", got)
	}
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"plan\"}\n\n"))
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n"))
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":10}}}\n\n"))
	if got := analyzer.Preview().ProgressEvents; got != 2 {
		t.Fatalf("generated output progress = %d, want 2", got)
	}
}

func TestMissingThinkingRespectsExplicitCapabilityAndAccountPrior(t *testing.T) {
	base := Signals{
		Terminal:         true,
		FirstVisible:     true,
		VisibleTokens:    64,
		VisibleSpanMS:    1200,
		VisibleChunks:    12,
		ReasoningStarted: true,
	}
	explicit := base
	explicit.ReasoningExplicit = true
	explicit.CapabilityKnown = true
	explicit.EvidenceCapable = true
	if verdict := ClassifyQualityHold(explicit, 8); verdict.Action != QualityActionWithhold || verdict.Class != ClassMissingThinking {
		t.Fatalf("explicit reasoning without evidence = %#v, want missing_thinking", verdict)
	}

	inferred := base
	inferred.CapabilityKnown = true
	inferred.EvidenceCapable = true
	if verdict := ClassifyQualityHold(inferred, 8); verdict.Action != QualityActionWithhold {
		t.Fatalf("inferred reasoning without history = %#v, want missing_thinking", verdict)
	}
	history := inferred
	history.AccountEvidenceSamples = 5
	history.AccountEvidenceRate = 1
	if verdict := ClassifyQualityHold(history, 8); verdict.Action != QualityActionDeliver || verdict.Class != ClassNone {
		t.Fatalf("first inferred miss on an evidence account = %#v, want deliver", verdict)
	}
	if verdict := ClassifyQualityHold(history, 8); verdict.Reason != "reason.detail.inferred_reasoning_unconfirmed_by_account_history" {
		t.Fatalf("inferred release reason = %q", verdict.Reason)
	}
	weak := history
	weak.AccountEvidenceRate = 0.2
	if verdict := ClassifyQualityHold(weak, 8); verdict.Action != QualityActionWithhold {
		t.Fatalf("inferred miss on a weak account = %#v, want missing_thinking", verdict)
	}
	struck := history
	struck.AccountThinkingStrikes = 1
	if verdict := ClassifyQualityHold(struck, 8); verdict.Action != QualityActionWithhold {
		t.Fatalf("inferred miss on a struck account = %#v, want missing_thinking", verdict)
	}

	captiveChat := inferred
	captiveChat.EvidenceCapable = false
	if verdict := ClassifyQualityHold(captiveChat, 8); verdict.Action != QualityActionDeliver {
		t.Fatalf("chat path without evidence support = %#v, want deliver", verdict)
	}
	if verdict := ClassifyQualityHold(captiveChat, 8); verdict.Reason != "reason.detail.reasoning_evidence_not_exposed_by_protocol" {
		t.Fatalf("capability release reason = %q", verdict.Reason)
	}
}

func TestClassifyPassiveAuditRespectsInferredHistory(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	sig := Signals{
		Streaming:         true,
		Terminal:          true,
		ReasoningRequired: true,
		FirstTokenKnown:   true,
		FirstTokenMS:      100,
		DurationMS:        3000,
		VisibleTokens:     64,
		OutputTokens:      80,
	}
	sig.CapabilityKnown = true
	sig.EvidenceCapable = true
	if verdict := ClassifyPassiveAudit(sig, cfg); verdict.Class != ClassMissingThinking {
		t.Fatalf("passive inferred miss without history = %q, want missing_thinking", verdict.Class)
	}
	sig.AccountEvidenceSamples = 4
	sig.AccountEvidenceRate = 0.75
	if verdict := ClassifyPassiveAudit(sig, cfg); verdict.Class == ClassMissingThinking {
		t.Fatalf("passive inferred miss on an evidence account was withheld: %#v", verdict)
	}
	sig.ReasoningExplicit = true
	if verdict := ClassifyPassiveAudit(sig, cfg); verdict.Class != ClassMissingThinking {
		t.Fatalf("passive explicit miss = %q, want missing_thinking", verdict.Class)
	}
}

func TestEvidenceOrderedStreamIsNotCipherDrool(t *testing.T) {
	sig := Signals{
		FirstVisible:     true,
		VisibleTokens:    2048,
		VisibleSpanMS:    3000,
		VisibleChunks:    200,
		EncryptedBytes:   2048,
		EncryptedFloor:   256,
		EvidenceAtMS:     120,
		FirstVisibleMS:   900,
		ReasoningStartMS: 100,
	}
	if verdict := ClassifyQualityHold(sig, 8); verdict.Class == ClassCipherDrool {
		t.Fatalf("ordered evidence was judged cipher drool: %#v", verdict)
	}
	late := sig
	late.EvidenceAtMS = 0
	if verdict := ClassifyQualityHold(late, 8); verdict.Class != ClassCipherDrool {
		t.Fatalf("evidence-free cipher stream class = %q, want cipher_drool", verdict.Class)
	}
}

func TestSilenceThenBurstIsBufferedBurst(t *testing.T) {
	sig := Signals{
		Terminal:        true,
		FirstVisible:    true,
		VisibleTokens:   120,
		VisibleSpanMS:   3000,
		VisibleChunks:   12,
		VisibleGapMaxMS: 2600,
		ReasoningTokens: 200,
		EncryptedBytes:  2048,
		EncryptedFloor:  256,
	}
	if verdict := ClassifyQualityHold(sig, 8); verdict.Class != ClassBufferedBurst {
		t.Fatalf("silence-then-burst class = %q, want buffered_burst", verdict.Class)
	}
	steady := sig
	steady.VisibleGapMaxMS = 40
	if verdict := ClassifyQualityHold(steady, 8); verdict.Class == ClassBufferedBurst {
		t.Fatalf("steady stream was judged a burst: %#v", verdict)
	}
	cheap := sig
	cheap.ReasoningTokens = 0
	cheap.EncryptedBytes = 0
	cheap.EncryptedFloor = 0
	if verdict := ClassifyQualityHold(cheap, 8); verdict.Class == ClassBufferedBurst {
		t.Fatalf("bill-free burst was judged a burst: %#v", verdict)
	}
}

func TestVisibleTokensWeightCJKText(t *testing.T) {
	cjk := NewStreamAnalyzerWithProtocol(qualityProtocolResponses)
	cjk.ObserveChunk([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"你好世界\"}\n\n"))
	if got := cjk.Preview().VisibleTokens; got != 4 {
		t.Fatalf("CJK visible tokens = %d, want 4", got)
	}
	ascii := NewStreamAnalyzerWithProtocol(qualityProtocolResponses)
	ascii.ObserveChunk([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"abcdefgh\"}\n\n"))
	if got := ascii.Preview().VisibleTokens; got != 2 {
		t.Fatalf("ASCII visible tokens = %d, want 2", got)
	}
}

func TestVisibleGapsReported(t *testing.T) {
	analyzer := NewStreamAnalyzerWithProtocol(qualityProtocolResponses)
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"a\"}\n\n"))
	time.Sleep(30 * time.Millisecond)
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"b\"}\n\n"))
	time.Sleep(10 * time.Millisecond)
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"c\"}\n\n"))
	sig := analyzer.Preview()
	if sig.VisibleGapMaxMS < 20 || sig.VisibleGapMedMS < 5 {
		t.Fatalf("visible gap stats = max %d median %d", sig.VisibleGapMaxMS, sig.VisibleGapMedMS)
	}
}

func TestDefaultEncryptedFloorAcceptsObservedHealthyRatios(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	cfg.SoftTPS = 1e9
	cfg.HardTPS = 2e9
	// Encrypted sizes observed on healthy accounts that also streamed a
	// reasoning summary. Half of them are below 4 bytes per reasoning token,
	// which made accounts without a summary fail as missing_thinking.
	healthy := []struct{ encrypted, reasoning int64 }{
		{encrypted: 9678, reasoning: 3973},
		{encrypted: 1019, reasoning: 374},
		{encrypted: 5638, reasoning: 2020},
		{encrypted: 979, reasoning: 282},
		{encrypted: 724, reasoning: 185},
		{encrypted: 716, reasoning: 180},
	}
	for _, sample := range healthy {
		sig := Signals{
			Streaming:       true,
			Terminal:        true,
			FirstVisible:    true,
			VisibleTokens:   64,
			VisibleSpanMS:   2000,
			VisibleChunks:   60,
			UsageReported:   true,
			OutputTokens:    sample.reasoning + 64,
			ReasoningTokens: sample.reasoning,
			EncryptedBytes:  sample.encrypted,
			FirstTokenMS:    4000,
			DurationMS:      8000,
		}
		sig.ReasoningExplicit = true
		sig.CapabilityKnown = true
		sig.EvidenceCapable = true
		if verdict := classifyFinal(sig, cfg, true, 8, true); verdict.Class != ClassNone {
			t.Fatalf("healthy %d bytes for %d reasoning tokens = %#v, want none", sample.encrypted, sample.reasoning, verdict)
		}
	}

	stub := Signals{
		Streaming:         true,
		Terminal:          true,
		FirstVisible:      true,
		VisibleTokens:     64,
		VisibleSpanMS:     2000,
		VisibleChunks:     60,
		UsageReported:     true,
		OutputTokens:      1564,
		ReasoningTokens:   1500,
		EncryptedBytes:    400,
		FirstTokenMS:      4000,
		DurationMS:        8000,
		ReasoningExplicit: true,
		CapabilityKnown:   true,
		EvidenceCapable:   true,
	}
	if verdict := classifyFinal(stub, cfg, true, 8, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("short stub against a large reasoning bill = %#v, want missing_thinking", verdict)
	}
}

func TestToolCallSignalTracksClientExecutedCallsOnly(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		payload  string
		want     bool
	}{
		{name: "responses function call item", protocol: qualityProtocolResponses, payload: "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"exec_command\",\"arguments\":\"\"}}\n\n", want: true},
		{name: "responses custom tool input", protocol: qualityProtocolResponses, payload: "data: {\"type\":\"response.custom_tool_call_input.delta\",\"delta\":\"*** Begin Patch\"}\n\n", want: true},
		{name: "responses hosted web search", protocol: qualityProtocolResponses, payload: "data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"ws_1\",\"type\":\"web_search_call\",\"status\":\"in_progress\"}}\n\n", want: false},
		{name: "responses hosted mcp arguments", protocol: qualityProtocolResponses, payload: "data: {\"type\":\"response.mcp_call_arguments.delta\",\"delta\":\"{}\"}\n\n", want: false},
		{name: "chat tool call delta", protocol: qualityProtocolChat, payload: "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"exec\",\"arguments\":\"\"}}]}}]}\n\n", want: true},
		{name: "anthropic tool use", protocol: qualityProtocolAnthropic, payload: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"bash\",\"input\":{}}}\n\n", want: true},
		{name: "anthropic hosted tool", protocol: qualityProtocolAnthropic, payload: "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"server_tool_use\",\"id\":\"srvtoolu_1\",\"name\":\"web_search\",\"input\":{}}}\n\n", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			analyzer := NewStreamAnalyzerWithProtocol(tc.protocol)
			analyzer.ObserveChunk([]byte(tc.payload))
			sig := analyzer.Preview()
			if sig.HasToolCall != tc.want {
				t.Fatalf("HasToolCall = %v, want %v: %#v", sig.HasToolCall, tc.want, sig)
			}
			if !sig.SemanticOutput {
				t.Fatalf("tool output was not recorded as semantic output: %#v", sig)
			}
		})
	}
}

// toolCallTurnSignals models a Codex agent turn: real reasoning, a short
// preamble, then a tool call. Healthy xAI accounts deliver this tail in one
// burst right after the reasoning finishes.
func toolCallTurnSignals() Signals {
	return Signals{
		Terminal:          true,
		Streaming:         true,
		ReasoningRequired: true,
		ReasoningExplicit: true,
		CapabilityKnown:   true,
		EvidenceCapable:   true,
		ReasoningStarted:  true,
		FirstVisible:      true,
		VisibleTokens:     10,
		VisibleSpanMS:     0,
		VisibleChunks:     2,
		UsageReported:     true,
		OutputTokens:      204,
		ReasoningTokens:   190,
		EncryptedBytes:    3000,
		EncryptedFloor:    760,
		SemanticOutput:    true,
		HasToolCall:       true,
	}
}

func TestToolCallTurnSkipsVisibleWindowDumpChecks(t *testing.T) {
	burst := toolCallTurnSignals()
	if verdict := ClassifyQualityHold(burst, 8); verdict.Action != QualityActionDeliver || verdict.Class != ClassNone {
		t.Fatalf("tool call with a short preamble = %#v, want deliver", verdict)
	}
	textOnly := burst
	textOnly.HasToolCall = false
	if verdict := ClassifyQualityHold(textOnly, 8); verdict.Class != ClassBufferedBurst {
		t.Fatalf("text-only short dump class = %q, want buffered_burst", verdict.Class)
	}

	longPreamble := burst
	longPreamble.VisibleTokens = 40
	longPreamble.VisibleSpanMS = 150
	longPreamble.VisibleChunks = 3
	if verdict := ClassifyQualityHold(longPreamble, 8); verdict.Action != QualityActionDeliver {
		t.Fatalf("tool call with a fast preamble = %#v, want deliver", verdict)
	}
	longPreamble.HasToolCall = false
	if verdict := ClassifyQualityHold(longPreamble, 8); verdict.Class != ClassFakeEncryptedDump {
		t.Fatalf("text-only fast dump class = %q, want fake_encrypted_dump", verdict.Class)
	}

	summarized := burst
	summarized.HasReasoningDelta = true
	summarized.HasThinking = true
	if verdict := ClassifyQualityHold(summarized, 8); verdict.Action != QualityActionDeliver {
		t.Fatalf("summarized tool call = %#v, want deliver", verdict)
	}
	summarized.HasToolCall = false
	if verdict := ClassifyQualityHold(summarized, 8); verdict.Class != ClassFastReasoningRatio {
		t.Fatalf("summarized text dump class = %q, want fast_reasoning_ratio_dump", verdict.Class)
	}

	cfg := config.DefaultQualityConfig()
	cfg.SoftTPS = 1e9
	cfg.HardTPS = 2e9
	if verdict := classifyFinal(toolCallTurnSignals(), cfg, true, 8, true); verdict.Class != ClassNone {
		t.Fatalf("final tool call verdict = %#v, want none", verdict)
	}
}

func TestToolCallWithoutEvidenceIsStillMissingThinking(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	cfg.SoftTPS = 1e9
	cfg.HardTPS = 2e9
	sig := toolCallTurnSignals()
	sig.EncryptedBytes = 0
	sig.ReasoningStarted = false
	if verdict := classifyFinal(sig, cfg, true, 8, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("tool call without reasoning evidence = %#v, want missing_thinking", verdict)
	}
	sig.EncryptedBytes = 300
	if verdict := classifyFinal(sig, cfg, true, 8, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("tool call with an encrypted stub below the floor = %#v, want missing_thinking", verdict)
	}
}

func TestEncryptedEvidenceReleasesToolCallBeforeTerminal(t *testing.T) {
	sig := toolCallTurnSignals()
	sig.Terminal = false
	sig.UsageReported = false
	sig.OutputTokens = 0
	sig.ReasoningTokens = 0
	sig.EncryptedFloor = 0
	sig.FirstVisible = false
	sig.VisibleTokens = 0
	sig.VisibleChunks = 0
	verdict := ClassifyQualityHold(sig, 8)
	if verdict.Action != QualityActionDeliver || verdict.Reason != "reason.detail.encrypted_reasoning_preceded_a_tool_call" {
		t.Fatalf("tool call after encrypted reasoning = %#v, want deliver", verdict)
	}
	sig.HasToolCall = false
	if verdict := ClassifyQualityHold(sig, 8); verdict.Action != QualityActionWait {
		t.Fatalf("encrypted reasoning before any output = %#v, want wait", verdict)
	}
}

func TestEncryptedEvidenceReleasesAtHoldExpiry(t *testing.T) {
	// The visible text really streams but stays below the hold floor. Without
	// this release the attempt waited for the terminal event after the hold.
	sig := Signals{
		ReasoningStarted: true,
		FirstVisible:     true,
		VisibleTokens:    6,
		VisibleSpanMS:    400,
		VisibleChunks:    5,
		EncryptedBytes:   2048,
	}
	if verdict := ClassifyQualityHold(sig, 8); verdict.Action != QualityActionWait {
		t.Fatalf("short visible text inside the hold = %#v, want wait", verdict)
	}
	sig.HoldExpired = true
	verdict := ClassifyQualityHold(sig, 8)
	if verdict.Action != QualityActionDeliver || verdict.Reason != "reason.detail.encrypted_reasoning_held_until_timeout" {
		t.Fatalf("expired hold with encrypted reasoning = %#v, want deliver", verdict)
	}
	dumped := sig
	dumped.VisibleSpanMS = 0
	dumped.VisibleChunks = 1
	if verdict := ClassifyQualityHold(dumped, 8); verdict.Class != ClassFakeEncryptedDump {
		t.Fatalf("single burst at hold expiry class = %q, want fake_encrypted_dump", verdict.Class)
	}
}
