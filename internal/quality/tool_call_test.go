package quality

import (
	"testing"

	"grok-quality-guard/internal/config"
)

// noReasoningToolCallSignals models a Codex continuation turn: grok-4.7 at
// xhigh effort emitted a ~75-token function call with no reasoning bill and
// no evidence, in one burst after a 2.1s prefill. The quality lab sent the
// same real continuation to three different accounts and all three answered
// this way with a well-formed call, so the turn reflects the request.
func noReasoningToolCallSignals() Signals {
	return Signals{
		Protocol:          ProtocolResponses,
		Streaming:         true,
		Terminal:          true,
		ReasoningRequired: true,
		ReasoningExplicit: true,
		CapabilityKnown:   true,
		EvidenceCapable:   true,
		SemanticOutput:    true,
		HasToolCall:       true,
		ToolCallValid:     true,
		UsageReported:     true,
		OutputTokens:      75,
		FirstTokenKnown:   true,
		FirstTokenMS:      2130,
		DurationMS:        2180,
	}
}

func TestValidToolCallWithoutReasoningIsDelivered(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	sig := noReasoningToolCallSignals()
	skipped := func(name string, verdict QualityVerdict) {
		t.Helper()
		if verdict.Action != QualityActionDeliver || verdict.Class != ClassNone || !verdict.Signals.ReasoningSkipped ||
			verdict.Reason != "reason.detail.tool_call_without_reasoning_bill" {
			t.Fatalf("%s = %#v, want delivered as a skipped-reasoning tool turn", name, verdict)
		}
	}
	skipped("hold", ClassifyQualityHold(sig, cfg.MinHoldOutputTokens, cfg.BurstGuard))
	skipped("final", classifyFinal(sig, cfg, true, cfg.MinHoldOutputTokens, true))
	nonStream := sig
	nonStream.Streaming = false
	skipped("non-stream", classifyNonStreamResult(nonStream, cfg, true, cfg.MinHoldOutputTokens))
	passive := sig
	passive.OutputTokens = 75
	skipped("passive", classifyPassiveAudit(passive, cfg))

	// A short preamble before the call is still a tool turn.
	preamble := sig
	preamble.FirstVisible = true
	preamble.VisibleTokens = 12
	preamble.VisibleChunks = 3
	preamble.VisibleSpanMS = 30
	skipped("preamble", classifyFinal(preamble, cfg, true, cfg.MinHoldOutputTokens, true))
}

func TestToolCallExemptionStaysNarrow(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	missing := func(name string, sig Signals) {
		t.Helper()
		if verdict := classifyFinal(sig, cfg, true, cfg.MinHoldOutputTokens, true); verdict.Class != ClassMissingThinking {
			t.Fatalf("%s = %#v, want missing_thinking", name, verdict)
		}
	}
	// Arguments that never became a well-formed call are not a usable turn.
	malformed := noReasoningToolCallSignals()
	malformed.ToolCallValid = false
	missing("malformed tool call", malformed)

	// Billed reasoning without its evidence is the degraded signature.
	billed := noReasoningToolCallSignals()
	billed.ReasoningTokens = 190
	billed.OutputTokens = 265
	missing("billed reasoning without evidence", billed)

	// A full answer next to the call is judged as an answer.
	answer := noReasoningToolCallSignals()
	answer.FirstVisible = true
	answer.VisibleTokens = 120
	answer.VisibleChunks = 20
	answer.VisibleSpanMS = 900
	missing("long answer beside the call", answer)

	// Only a turn with neither output nor a bill is semantic-only.
	semantic := noReasoningToolCallSignals()
	semantic.ToolCallValid = false
	semantic.OutputTokens = 0
	semantic.UsageReported = false
	if verdict := classifyFinal(semantic, cfg, true, cfg.MinHoldOutputTokens, true); verdict.Class != ClassNone {
		t.Fatalf("semantic-only tool call = %#v, want deliver", verdict)
	}
}

func TestToolCallInProgressWaitsForItsBill(t *testing.T) {
	calling := Signals{
		ReasoningExplicit: true,
		SemanticOutput:    true,
		HasToolCall:       true,
	}
	if verdict := ClassifyQualityHold(calling, 8, false); verdict.Action != QualityActionWait {
		t.Fatalf("tool call before its terminal event = %#v, want wait", verdict)
	}
	// Short text may still be followed by a tool call and the terminal bill.
	preamble := Signals{
		ReasoningExplicit: true,
		FirstVisible:      true,
		VisibleTokens:     12,
		VisibleChunks:     3,
		VisibleSpanMS:     60,
	}
	if verdict := ClassifyQualityHold(preamble, 8, false); verdict.Action != QualityActionWait {
		t.Fatalf("short preamble mid-stream = %#v, want wait", verdict)
	}
	// Once the hold expires, or the text outgrows a preamble, it is an answer.
	expired := preamble
	expired.HoldExpired = true
	if verdict := ClassifyQualityHold(expired, 8, false); verdict.Class != ClassMissingThinking {
		t.Fatalf("preamble at hold expiry = %#v, want missing_thinking", verdict)
	}
	long := preamble
	long.VisibleTokens = 64
	if verdict := ClassifyQualityHold(long, 8, false); verdict.Class != ClassMissingThinking {
		t.Fatalf("64 visible tokens without evidence = %#v, want missing_thinking", verdict)
	}
}

func TestAnalyzerMarksCompleteToolCalls(t *testing.T) {
	sse := func(lines ...string) []byte {
		out := ""
		for _, line := range lines {
			out += "data: " + line + "\n\n"
		}
		return []byte(out)
	}

	responses := NewStreamAnalyzerWithProtocol(ProtocolResponses)
	responses.ObserveChunk(sse(`{"type":"response.output_item.added","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"exec_command","arguments":""}}`))
	if sig := responses.Preview(); !sig.HasToolCall || sig.ToolCallValid {
		t.Fatalf("announced call = %#v, want a tool call that is not complete yet", sig)
	}
	responses.ObserveChunk(sse(`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"ls\"}"}}`))
	if !responses.Preview().ToolCallValid {
		t.Fatal("completed function call was not marked valid")
	}

	truncated := NewStreamAnalyzerWithProtocol(ProtocolResponses)
	truncated.ObserveChunk(sse(`{"type":"response.output_item.done","output_index":0,"item":{"id":"fc_1","type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"ls"}}`))
	if sig := truncated.Preview(); !sig.HasToolCall || sig.ToolCallValid {
		t.Fatalf("truncated arguments = %#v, want an invalid tool call", sig)
	}

	chat := NewStreamAnalyzerWithProtocol(ProtocolChat)
	chat.ObserveChunk(sse(
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
	))
	if chat.Preview().ToolCallValid {
		t.Fatal("chat call counted as complete before finish_reason")
	}
	chat.ObserveChunk(sse(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`))
	if !chat.Preview().ToolCallValid {
		t.Fatal("chat call with complete arguments was not marked valid")
	}

	anthropic := NewStreamAnalyzerWithProtocol(ProtocolAnthropic)
	anthropic.ObserveChunk(sse(
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"exec_command","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
	))
	if anthropic.Preview().ToolCallValid {
		t.Fatal("anthropic call counted as complete before content_block_stop")
	}
	anthropic.ObserveChunk(sse(`{"type":"content_block_stop","index":1}`))
	if !anthropic.Preview().ToolCallValid {
		t.Fatal("anthropic call with complete input was not marked valid")
	}
}

func TestBurstTPSUsesFullDurationBound(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	// The same burst on a request without a reasoning requirement: the 50ms
	// arrival tail used to time it at 1500 tok/s and fail it as hard_tps.
	verdict := classifyFinal(noReasoningToolCallSignals(), cfg, false, cfg.MinHoldOutputTokens, true)
	if verdict.Class != ClassNone || verdict.Signals.GenerationMS != 2180 || verdict.Signals.TPS > 40 {
		t.Fatalf("burst verdict = %#v, want none at the full-duration bound", verdict)
	}
}

func TestOutputSpeedTreatsBurstsAsDelivery(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	// Too few tokens to time: report the speed but never judge it.
	tiny := Signals{OutputTokens: 20, FirstTokenMS: 5, DurationMS: 15}
	if class, speed, _ := classifyOutputSpeed(tiny, cfg, false); class != ClassNone || speed < cfg.HardTPS {
		t.Fatalf("tiny sample = %q at %.0f tok/s, want unjudged", class, speed)
	}
	// A streamed tail stays the window, so impossible speeds are still caught.
	streamed := Signals{OutputTokens: 200, FirstTokenMS: 50, DurationMS: 250}
	if class, _, gen := classifyOutputSpeed(streamed, cfg, false); class != ClassHardTPS || gen != 200 {
		t.Fatalf("streamed 200 tokens in 200ms = %q over %dms, want hard_tps", class, gen)
	}
	// A burst far beyond the full-duration bound is still impossible.
	dump := Signals{OutputTokens: 3000, FirstTokenMS: 2000, DurationMS: 2050}
	if class, _, gen := classifyOutputSpeed(dump, cfg, false); class != ClassHardTPS || gen != 2050 {
		t.Fatalf("3000-token burst = %q over %dms, want hard_tps", class, gen)
	}

	cfg.BurstGuard = true
	text := Signals{OutputTokens: 2000, FirstTokenMS: 10000, DurationMS: 10100}
	if class, _, _ := classifyOutputSpeed(text, cfg, true); class != ClassBufferedBurst {
		t.Fatalf("fail-closed buffered text = %q, want buffered_burst", class)
	}
	tool := text
	tool.OutputTokens = 75
	tool.HasToolCall = true
	if class, _, _ := classifyOutputSpeed(tool, cfg, true); class != ClassNone {
		t.Fatalf("fail-closed tool-call burst = %q, want none", class)
	}
}

func TestAnalyzerRevisionSkipsControlFrames(t *testing.T) {
	analyzer := NewStreamAnalyzerWithProtocol(qualityProtocolResponses)
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n"))
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_1\"}}\n\n"))
	if got := analyzer.Revision(); got != 0 {
		t.Fatalf("control frames advanced the revision to %d", got)
	}
	analyzer.ObserveChunk([]byte(": grok2api-reasoning-start\n\n"))
	afterStub := analyzer.Revision()
	if afterStub == 0 {
		t.Fatal("reasoning start did not advance the revision")
	}
	analyzer.ObserveChunk([]byte(": grok2api-reasoning-start\n\n"))
	if got := analyzer.Revision(); got != afterStub {
		t.Fatalf("repeated stub advanced the revision to %d", got)
	}
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"answer\"}\n\n"))
	if got := analyzer.Revision(); got == afterStub {
		t.Fatal("visible text did not advance the revision")
	}
	if got := analyzer.ProgressEvents(); got != 1 {
		t.Fatalf("progress events = %d, want 1", got)
	}
}
