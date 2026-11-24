package quality

import (
	"testing"

	"grok-quality-guard/internal/config"
)

// noReasoningToolCallSignals models the Codex continuation turn from the field
// report: grok-4.7 at xhigh effort emitted a ~75-token function call without
// any reasoning, and every account it was retried on did the same. The call
// arrived in one burst right after a 2.1s prefill.
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
		UsageReported:     true,
		OutputTokens:      75,
		FirstTokenKnown:   true,
		FirstTokenMS:      2130,
		DurationMS:        2180,
	}
}

func TestToolCallWithoutReasoningBillIsDelivered(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	sig := noReasoningToolCallSignals()
	if verdict := ClassifyQualityHold(sig, cfg.MinHoldOutputTokens); verdict.Action != QualityActionDeliver || verdict.Reason != "reason.detail.tool_call_without_reasoning_bill" {
		t.Fatalf("hold verdict = %#v, want deliver", verdict)
	}
	verdict := classifyFinal(sig, cfg, true, cfg.MinHoldOutputTokens, true)
	if verdict.Action != QualityActionDeliver || verdict.Class != ClassNone || !verdict.Signals.ReasoningSkipped {
		t.Fatalf("final verdict = %#v, want neutral deliver", verdict)
	}
	// The burst used to divide 75 tokens by the 50ms arrival tail: 1500 tok/s.
	if verdict.Signals.GenerationMS != 2180 || verdict.Signals.TPS > 40 {
		t.Fatalf("burst TPS = %.1f over %dms, want the full-duration bound", verdict.Signals.TPS, verdict.Signals.GenerationMS)
	}

	preamble := sig
	preamble.FirstVisible = true
	preamble.VisibleTokens = 12
	preamble.VisibleChunks = 3
	preamble.VisibleSpanMS = 30
	if verdict := classifyFinal(preamble, cfg, true, cfg.MinHoldOutputTokens, true); verdict.Class != ClassNone || !verdict.Signals.ReasoningSkipped {
		t.Fatalf("tool call with a short preamble = %#v, want neutral deliver", verdict)
	}

	essay := preamble
	essay.VisibleTokens = 200
	if verdict := classifyFinal(essay, cfg, true, cfg.MinHoldOutputTokens, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("long answer next to a tool call = %#v, want missing_thinking", verdict)
	}

	billed := sig
	billed.ReasoningTokens = 300
	billed.OutputTokens = 375
	if verdict := classifyFinal(billed, cfg, true, cfg.MinHoldOutputTokens, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("billed reasoning without evidence = %#v, want missing_thinking", verdict)
	}

	text := sig
	text.HasToolCall = false
	text.FirstVisible = true
	text.VisibleTokens = 75
	text.VisibleChunks = 10
	text.VisibleSpanMS = 40
	if verdict := classifyFinal(text, cfg, true, cfg.MinHoldOutputTokens, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("text answer without reasoning = %#v, want missing_thinking", verdict)
	}
}

func TestToolCallWithoutReasoningBillOnOtherPaths(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	nonStream := noReasoningToolCallSignals()
	nonStream.Streaming = false
	if verdict := classifyNonStreamResult(nonStream, cfg, true, cfg.MinHoldOutputTokens); verdict.Class != ClassNone || !verdict.Signals.ReasoningSkipped {
		t.Fatalf("non-stream tool call = %#v, want neutral deliver", verdict)
	}
	passive := noReasoningToolCallSignals()
	if verdict := classifyPassiveAudit(passive, cfg); verdict.Class != ClassNone || !verdict.Signals.ReasoningSkipped {
		t.Fatalf("passive tool call = %#v, want neutral deliver", verdict)
	}
}

func TestShortVisibleTextWaitsForToolCallBeforeSettling(t *testing.T) {
	preamble := Signals{
		ReasoningExplicit: true,
		FirstVisible:      true,
		VisibleTokens:     12,
		VisibleChunks:     3,
		VisibleSpanMS:     60,
		VisibleFlushMS:    60,
	}
	// Eight visible tokens used to withhold the turn before its tool call or
	// reasoning bill could arrive.
	if verdict := ClassifyQualityHold(preamble, 8); verdict.Action != QualityActionWait {
		t.Fatalf("short preamble = %#v, want wait", verdict)
	}
	stub := preamble
	stub.ReasoningStarted = true
	if verdict := ClassifyQualityHold(stub, 8); verdict.Action != QualityActionWait || verdict.Reason != "reason.detail.waiting_for_reasoning_evidence" {
		t.Fatalf("short preamble after a stub = %#v, want wait", verdict)
	}

	calling := preamble
	calling.HasToolCall = true
	calling.VisibleFlushMS = 5000
	calling.HoldExpired = true
	if verdict := ClassifyQualityHold(calling, 8); verdict.Action != QualityActionWait || verdict.Reason != "reason.detail.waiting_for_tool_call_completion" {
		t.Fatalf("tool call in progress = %#v, want wait for its bill", verdict)
	}

	// A preamble's tool call follows in the same burst. Short text that stays
	// alone past the window is an answer, so a stalled stream still fails over.
	stalled := stub
	stalled.VisibleFlushMS = defaultQualityPreambleSettleMS
	if verdict := ClassifyQualityHold(stalled, 8); verdict.Class != ClassMissingThinking {
		t.Fatalf("stalled short answer = %#v, want missing_thinking", verdict)
	}

	long := preamble
	long.VisibleTokens = 64
	if verdict := ClassifyQualityHold(long, 8); verdict.Class != ClassMissingThinking {
		t.Fatalf("answer beyond preamble size = %#v, want missing_thinking", verdict)
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

	cfg.FailClosed = true
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
