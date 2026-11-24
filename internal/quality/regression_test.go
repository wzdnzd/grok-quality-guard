package quality

import (
	"testing"

	"grok-quality-guard/internal/config"
)

func TestPassiveAuditIgnoresIneligibleSamples(t *testing.T) {
	config := config.DefaultQualityConfig()
	nonStream := classifyPassiveAudit(Signals{OutputTokens: 100, FirstTokenKnown: true}, config)
	if nonStream.Class != ClassIgnored || nonStream.Reason != "reason.detail.not_build_stream" {
		t.Fatalf("non-stream passive verdict = %#v", nonStream)
	}
	missingTTFT := classifyPassiveAudit(Signals{Streaming: true, Terminal: true, DurationMS: 1000, OutputTokens: 100}, config)
	if missingTTFT.Class != ClassIgnored || missingTTFT.Reason != "reason.detail.missing_first_token" {
		t.Fatalf("missing-TTFT passive verdict = %#v", missingTTFT)
	}
	short := classifyPassiveAudit(Signals{Streaming: true, Terminal: true, FirstTokenKnown: true, FirstTokenMS: 100, DurationMS: 1000, OutputTokens: 31}, config)
	if short.Class != ClassIgnored || short.Reason != "reason.detail.insufficient_output_tokens" {
		t.Fatalf("short passive verdict = %#v", short)
	}
	healthy := classifyPassiveAudit(Signals{Streaming: true, Terminal: true, FirstTokenKnown: true, FirstTokenMS: 100, DurationMS: 1000, OutputTokens: 100}, config)
	if healthy.Class != ClassNone || healthy.Reason != "reason.detail.within_threshold" {
		t.Fatalf("healthy passive verdict = %#v", healthy)
	}
}

func TestClassifyNonStreamQuality(t *testing.T) {
	config := config.DefaultQualityConfig()
	empty := classifyNonStreamResult(Signals{}, config, true, 8)
	if empty.Class != ClassEmptyStream {
		t.Fatalf("empty non-stream class = %q", empty.Class)
	}
	missing := classifyNonStreamResult(Signals{OutputTokens: 100, VisibleTokens: 100, DurationMS: 1000}, config, true, 8)
	if missing.Class != ClassMissingThinking {
		t.Fatalf("missing-thinking non-stream class = %q", missing.Class)
	}
	short := classifyNonStreamResult(Signals{OutputTokens: 4, VisibleTokens: 4, DurationMS: 1000}, config, true, 8)
	if short.Class != ClassNone {
		t.Fatalf("short non-stream class = %q, want none", short.Class)
	}
}

func TestReasoningEvidenceCommentSatisfiesCipherFloor(t *testing.T) {
	analyzer := NewStreamAnalyzer()
	analyzer.ObserveChunk([]byte(": grok2api-reasoning-evidence 1024\n\n"))
	analyzer.ObserveChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"visible\"}}]}\n\n"))
	signals := analyzer.Finalize(true)
	if !signals.HasThinking || signals.EncryptedBytes != 1024 || signals.EncryptedFloor != 256 {
		t.Fatalf("reasoning evidence was not applied: %#v", signals)
	}

	short := NewStreamAnalyzer()
	short.ObserveChunk([]byte(": grok2api-reasoning-evidence 64\n\n"))
	shortSignals := short.Finalize(true)
	if shortSignals.HasThinking {
		t.Fatalf("short encrypted stub counted as thinking: %#v", shortSignals)
	}
}

func TestResponsesReasoningSummaryAndAggregateMessage(t *testing.T) {
	analyzer := NewStreamAnalyzerWithProtocol(qualityProtocolResponses)
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"plan the answer\"}\n\n"))
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"answer\"}]}}\n\n"))
	analyzer.ObserveChunk([]byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"output_tokens\":40,\"output_tokens_details\":{\"reasoning_tokens\":20}}}}\n\n"))
	signals := analyzer.Finalize(true)
	if !signals.HasThinking || !signals.HasReasoningDelta || !signals.SemanticOutput || !signals.Terminal {
		t.Fatalf("unexpected Responses signals: %#v", signals)
	}
	if signals.VisibleTokens != 2 || signals.OutputTokens != 40 || signals.ReasoningTokens != 20 {
		t.Fatalf("unexpected Responses counters: %#v", signals)
	}
	if signals.FirstVisible {
		t.Fatalf("aggregate message should not create a streaming visible flush: %#v", signals)
	}
}

func TestAnthropicNonStreamMessage(t *testing.T) {
	analyzer := NewStreamAnalyzerWithProtocol(qualityProtocolAnthropic)
	analyzer.SetStreaming(false)
	analyzer.ObserveJSON([]byte(`{"type":"message","content":[{"type":"thinking","thinking":"plan carefully"},{"type":"text","text":"answer"}],"stop_reason":"end_turn","usage":{"output_tokens":20,"output_tokens_details":{"thinking_tokens":5}}}`))
	signals := analyzer.Finalize(true)
	if !signals.HasThinking || !signals.HasReasoningDelta || !signals.SemanticOutput || !signals.Terminal {
		t.Fatalf("unexpected Anthropic non-stream signals: %#v", signals)
	}
	if signals.VisibleTokens != 2 || signals.OutputTokens != 20 || signals.ReasoningTokens != 5 {
		t.Fatalf("unexpected Anthropic non-stream counters: %#v", signals)
	}
}

func TestProbeThinkingGuardRequiresMinimumOutput(t *testing.T) {
	config := config.DefaultQualityConfig()
	below := classifyProbeResult(Signals{Terminal: true, OutputTokens: 63, FirstTokenMS: 100, DurationMS: 1100}, config, true, true)
	if below.Class != ClassNone {
		t.Fatalf("probe below thinking guard threshold class = %q", below.Class)
	}
	atThreshold := classifyProbeResult(Signals{Terminal: true, OutputTokens: 64, FirstTokenMS: 100, DurationMS: 1100}, config, true, true)
	if atThreshold.Class != ClassMissingThinking {
		t.Fatalf("probe at thinking guard threshold class = %q", atThreshold.Class)
	}
	shortVisible := classifyProbeResult(Signals{
		Terminal:        true,
		OutputTokens:    64,
		VisibleTokens:   3,
		FirstTokenMS:    100,
		DurationMS:      1100,
		FirstVisible:    true,
		VisibleSpanMS:   150,
		VisibleChunks:   2,
		ReasoningTokens: 0,
	}, config, true, true)
	if shortVisible.Class != ClassMissingThinking {
		t.Fatalf("probe short-visible dump class = %q, want missing_thinking", shortVisible.Class)
	}
	thinkingEvidence := classifyProbeResult(Signals{
		Terminal:          true,
		HasThinking:       true,
		OutputTokens:      100,
		VisibleTokens:     100,
		FirstTokenMS:      100,
		DurationMS:        3000,
		FirstVisible:      true,
		VisibleSpanMS:     2500,
		VisibleChunks:     10,
		ReasoningRequired: true,
	}, config, true, true)
	if thinkingEvidence.Class != ClassNone {
		t.Fatalf("probe with thinking evidence class = %q, want none", thinkingEvidence.Class)
	}
	usageOnly := Signals{
		Terminal:        true,
		OutputTokens:    100,
		VisibleTokens:   100,
		FirstTokenMS:    100,
		DurationMS:      3000,
		FirstVisible:    true,
		VisibleSpanMS:   2500,
		VisibleChunks:   10,
		ReasoningTokens: 100,
	}
	if verdict := classifyProbeResult(usageOnly, config, true, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("probe usage-only reasoning class = %q, want missing_thinking", verdict.Class)
	}
	reasoningBillOnly := Signals{
		Terminal:        true,
		ReasoningTokens: 100,
		FirstTokenMS:    100,
		DurationMS:      3000,
	}
	if verdict := classifyProbeResult(reasoningBillOnly, config, true, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("probe reasoning-bill-only class = %q, want missing_thinking", verdict.Class)
	}
}

func TestFailClosedProbeClassification(t *testing.T) {
	config := config.DefaultQualityConfig()
	config.BurstGuard = true
	burst := classifyProbeResult(Signals{Terminal: true, OutputTokens: 400, FirstTokenMS: 100, DurationMS: 600}, config, true, false)
	if burst.Class != ClassBufferedBurst {
		t.Fatalf("fail-closed burst class = %q", burst.Class)
	}
	insufficient := classifyProbeResult(Signals{Terminal: true, OutputTokens: 128, FirstTokenMS: 100, DurationMS: 600}, config, true, false)
	if insufficient.Class != ClassInsufficientGeneration {
		t.Fatalf("fail-closed generation class = %q", insufficient.Class)
	}
}

func TestSemanticOutputWithUsageIgnoresShortVisibleText(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	cfg.SoftTPS = 1e9
	cfg.HardTPS = 2e9
	signals := Signals{
		Streaming:         true,
		Terminal:          true,
		SemanticOutput:    true,
		FirstTokenKnown:   true,
		ReasoningRequired: true,
		FirstTokenMS:      100,
		DurationMS:        2000,
		FirstVisible:      true,
		VisibleSpanMS:     150,
		VisibleChunks:     2,
		OutputTokens:      100,
		VisibleTokens:     3,
	}
	if verdict := classifyFinal(signals, cfg, true, 8, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("streaming semantic output class = %q, want missing_thinking", verdict.Class)
	}
	if verdict := classifyPassiveAudit(signals, cfg); verdict.Class != ClassMissingThinking {
		t.Fatalf("passive semantic output class = %q, want missing_thinking", verdict.Class)
	}
	signals.Streaming = false
	if verdict := classifyNonStreamResult(signals, cfg, true, 8); verdict.Class != ClassMissingThinking {
		t.Fatalf("non-stream semantic output class = %q, want missing_thinking", verdict.Class)
	}
}

func TestShortVisibleTextCantHideBilledOutput(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	cfg.SoftTPS = 1e9
	cfg.HardTPS = 2e9
	signals := Signals{
		Streaming:         true,
		Terminal:          true,
		FirstTokenKnown:   true,
		FirstTokenMS:      100,
		DurationMS:        3000,
		FirstVisible:      true,
		VisibleSpanMS:     2500,
		VisibleChunks:     10,
		OutputTokens:      100,
		VisibleTokens:     3,
		ReasoningRequired: true,
	}
	if verdict := classifyFinal(signals, cfg, true, 8, true); verdict.Class != ClassMissingThinking {
		t.Fatalf("streaming short visible class = %q, want missing_thinking", verdict.Class)
	}
	if verdict := classifyPassiveAudit(signals, cfg); verdict.Class != ClassMissingThinking {
		t.Fatalf("passive short visible class = %q, want missing_thinking", verdict.Class)
	}
	signals.Streaming = false
	if verdict := classifyNonStreamResult(signals, cfg, true, 8); verdict.Class != ClassMissingThinking {
		t.Fatalf("non-stream short visible class = %q, want missing_thinking", verdict.Class)
	}
}

func TestReasoningBillWithoutThinkingEvidenceIsMissingThinking(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	cfg.SoftTPS = 1e9
	cfg.HardTPS = 2e9
	signals := Signals{
		Streaming:         true,
		Terminal:          true,
		SemanticOutput:    true,
		FirstTokenKnown:   true,
		FirstTokenMS:      100,
		DurationMS:        3000,
		ReasoningTokens:   100,
		ReasoningRequired: true,
	}
	verdict := classifyFinal(signals, cfg, true, 8, true)
	if verdict.Class != ClassMissingThinking || verdict.Action != QualityActionWithhold {
		t.Fatalf("reasoning-bill-only response = %#v, want missing_thinking withhold", verdict)
	}
	if verdict := classifyPassiveAudit(signals, cfg); verdict.Class != ClassMissingThinking {
		t.Fatalf("passive reasoning-bill-only class = %q, want missing_thinking", verdict.Class)
	}
	signals.Streaming = false
	if verdict := classifyNonStreamResult(signals, cfg, true, 8); verdict.Class != ClassMissingThinking {
		t.Fatalf("non-stream reasoning-bill-only class = %q, want missing_thinking", verdict.Class)
	}
}

func TestSemanticOnlyWithUsageIsMissingThinkingOnTerminalPaths(t *testing.T) {
	cfg := config.DefaultQualityConfig()
	cfg.SoftTPS = 1e9
	cfg.HardTPS = 2e9
	signals := Signals{
		Streaming:         true,
		Terminal:          true,
		SemanticOutput:    true,
		FirstTokenKnown:   true,
		FirstTokenMS:      100,
		DurationMS:        2000,
		OutputTokens:      100,
		ReasoningTokens:   0,
		VisibleTokens:     0,
		ReasoningRequired: true,
	}
	if verdict := classifyPassiveAudit(signals, cfg); verdict.Class != ClassMissingThinking {
		t.Fatalf("passive semantic-only class = %q, want missing_thinking", verdict.Class)
	}
	signals.Streaming = false
	if verdict := classifyNonStreamResult(signals, cfg, true, 8); verdict.Class != ClassMissingThinking {
		t.Fatalf("non-stream semantic-only class = %q, want missing_thinking", verdict.Class)
	}
}
