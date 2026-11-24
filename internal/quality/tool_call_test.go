package quality

import (
	"testing"

	"grok-quality-guard/internal/config"
)

// noReasoningToolCallSignals models the Codex continuation turns from the
// field reports: grok-4.7 at xhigh effort emitted ~75-token function calls
// with no reasoning bill and no evidence, in one burst after a 2.1s prefill.
// Healthy turns of the same size carried 245-294 reasoning tokens, so these
// are the 0-thinking degraded stream grok2api keeps behind the gate.
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

func TestToolCallWithoutReasoningIsMissingThinking(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	sig := noReasoningToolCallSignals()
	if verdict := ClassifyQualityHold(sig, cfg.MinHoldOutputTokens); verdict.Action != QualityActionWithhold || verdict.Class != ClassMissingThinking {
		t.Fatalf("hold verdict = %#v, want missing_thinking withhold", verdict)
	}
	if verdict := classifyFinal(sig, cfg, true, cfg.MinHoldOutputTokens, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("final verdict = %#v, want missing_thinking", verdict)
	}

	preamble := sig
	preamble.FirstVisible = true
	preamble.VisibleTokens = 12
	preamble.VisibleChunks = 3
	preamble.VisibleSpanMS = 30
	if verdict := classifyFinal(preamble, cfg, true, cfg.MinHoldOutputTokens, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("tool call with a preamble = %#v, want missing_thinking", verdict)
	}

	nonStream := sig
	nonStream.Streaming = false
	if verdict := classifyNonStreamResult(nonStream, cfg, true, cfg.MinHoldOutputTokens); verdict.Class != ClassMissingThinking {
		t.Fatalf("non-stream tool call = %#v, want missing_thinking", verdict)
	}
	if verdict := classifyPassiveAudit(noReasoningToolCallSignals(), cfg); verdict.Class != ClassMissingThinking {
		t.Fatalf("passive tool call = %#v, want missing_thinking", verdict)
	}

	// Only a turn with neither output nor a bill is semantic-only.
	semantic := sig
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
	if verdict := ClassifyQualityHold(calling, 8); verdict.Action != QualityActionWait {
		t.Fatalf("tool call before its terminal event = %#v, want wait", verdict)
	}
	preamble := Signals{
		ReasoningExplicit: true,
		FirstVisible:      true,
		VisibleTokens:     12,
		VisibleChunks:     3,
		VisibleSpanMS:     60,
	}
	// grok2api withholds a no-evidence visible stream once it reaches the
	// hold floor; a preamble is not given extra room.
	if verdict := ClassifyQualityHold(preamble, 8); verdict.Class != ClassMissingThinking {
		t.Fatalf("no-evidence preamble = %#v, want missing_thinking", verdict)
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
