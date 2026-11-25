package quality

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"grok-quality-guard/internal/config"
)

func encryptedFloor(reasoningTokens int64, cfg config.QualityConfig) int64 {
	return encryptedFloorWithConfig(reasoningTokens, cfg.MinEncryptedBytes, cfg.EncryptedBytesPerReasoningToken)
}

func encryptedFloorWithConfig(reasoningTokens, minBytes, bytesPerToken int64) int64 {
	if minBytes <= 0 {
		minBytes = defaultEncryptedBytes
	}
	if bytesPerToken <= 0 {
		bytesPerToken = defaultEncryptedBytesPerToken
	}
	floor := minBytes
	if reasoningTokens > 0 {
		if reasoningTokens > math.MaxInt64/bytesPerToken {
			return math.MaxInt64
		}
		if need := reasoningTokens * bytesPerToken; need > floor {
			floor = need
		}
	}
	return floor
}

func durationMS(start, end time.Time) int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func tps(outputTokens, generationMS int64) float64 {
	if outputTokens <= 0 || generationMS <= 0 {
		return 0
	}
	return float64(outputTokens) * 1000 / float64(generationMS)
}

// arrivalWindowMS is the reference Token/s window: the tail after the first
// token, widened to the full duration only when billed reasoning was flushed
// late. Only the opt-in fail-closed checks use it, so a buffered text burst
// stays visible to them.
func arrivalWindowMS(durationMS, firstTokenMS, reasoningTokens int64) int64 {
	tail := generationTailMS(durationMS, firstTokenMS)
	if reasoningTokens > 0 && qualityBurstTail(tail, firstTokenMS) {
		return durationMS
	}
	return tail
}

// generationWindowMS is the TPS denominator. The tail after the first token
// only times generation when the output streamed through it. A tail that is
// shorter than the wait before it and under a second means the output was
// produced before it reached the plugin and arrived in one burst: tool-call
// arguments, hidden reasoning or a buffering hop. Such a tail times delivery,
// and dividing by it turned a 75-token tool call into 1500 tokens/s. The full
// duration is the only defensible bound for those samples.
func generationWindowMS(durationMS, firstTokenMS int64) int64 {
	tail := generationTailMS(durationMS, firstTokenMS)
	if qualityBurstTail(tail, firstTokenMS) {
		return durationMS
	}
	return tail
}

// signalGenerationWindowMS is the TPS denominator for one sample. Billed
// reasoning that never streamed as text was generated while the stream stayed
// silent, so those tokens sit inside the first-token wait and the tail after
// it only times the answer. Dividing the whole bill by that tail turned field
// probes with 1000-2000 hidden reasoning tokens and a 1.0-1.5s tail into
// 1000-1500 tok/s and cooled the account as hard_tps; the burst correction
// above misses them because their tail is just over one second. The full
// duration is the only window that contains every billed token. Streamed
// reasoning marks the first token itself, so its tail already covers it.
func signalGenerationWindowMS(sig Signals) int64 {
	if sig.ReasoningTokens > 0 && !sig.HasReasoningDelta && sig.DurationMS > 0 {
		return sig.DurationMS
	}
	return generationWindowMS(sig.DurationMS, sig.FirstTokenMS)
}

func generationTailMS(durationMS, firstTokenMS int64) int64 {
	if durationMS <= 0 {
		return 0
	}
	if firstTokenMS < 0 {
		firstTokenMS = 0
	}
	if durationMS <= firstTokenMS {
		return 0
	}
	return durationMS - firstTokenMS
}

func qualityBurstTail(tailMS, firstTokenMS int64) bool {
	return tailMS > 0 && tailMS < firstTokenMS && tailMS < defaultQualityMinGenerationMS
}

func qualityMeetsEncryptedFloor(sig Signals) bool {
	if sig.EncryptedBytes <= 0 {
		return false
	}
	floor := sig.EncryptedFloor
	if floor <= 0 {
		floor = encryptedFloorWithConfig(sig.ReasoningTokens, defaultEncryptedBytes, defaultEncryptedBytesPerToken)
	}
	return sig.EncryptedBytes >= floor
}

func qualityHasDumpBill(sig Signals) bool {
	return qualityMeetsEncryptedFloor(sig) || sig.ReasoningTokens >= defaultQualityMinReasoning
}

// qualityWindowFinal reports whether the visible arrival window is complete.
// Time-based dump checks must wait for this evidence, otherwise a healthy
// stream is indistinguishable from a dump while its first deltas arrive.
func qualityWindowFinal(sig Signals) bool {
	return sig.Terminal || sig.HoldExpired
}

// qualityVisibleStreaming reports evidence that the visible text arrived as a
// real stream instead of a single buffered dump. Fast-flush detectors must
// respect this signal so a healthy stream is not mistaken for a dump.
func qualityVisibleStreaming(sig Signals) bool {
	if !sig.FirstVisible || sig.VisibleSpanMS < 100 {
		return false
	}
	if sig.VisibleTokens > 0 && sig.VisibleTokens*1000 > defaultQualityMaxVisibleTPS*sig.VisibleSpanMS {
		return false
	}
	return sig.VisibleChunks >= 4 || sig.VisibleSpanMS >= 200
}

func qualityFastFlush(sig Signals, limitMS int64) bool {
	if !sig.FirstVisible || !qualityWindowFinal(sig) || qualityVisibleStreaming(sig) {
		return false
	}
	return sig.VisibleSpanMS >= 0 && sig.VisibleSpanMS < limitMS
}

// qualityReasoningWaitSettled reports that waiting for reasoning evidence can
// no longer change the verdict. A healthy reasoning stream emits its evidence
// before the visible answer, so a visible window that has already started
// streaming without any evidence is a missing-thinking stream. Waiting past
// that point only blocks the client for the remaining hold timeout and reaches
// the same verdict, so the attempt can fail over early instead.
func qualityReasoningWaitSettled(sig Signals, minOutput int64) bool {
	if sig.HasThinking || !sig.FirstVisible {
		return false
	}
	if sig.VisibleTokens < minOutput {
		return false
	}
	return sig.VisibleSpanMS >= defaultQualitySettleSpanMS || sig.VisibleChunks >= defaultQualitySettleChunks
}

// qualityToolCallWithoutReasoning reports a valid client tool call the model
// made without billing any reasoning. Agent loops emit such continuation
// calls for reasons of the request, not the account: a real Codex
// continuation captured by the quality lab came back as a well-formed call
// with zero reasoning on every account it was sent to, so failing over only
// drained the pool. Billed reasoning without evidence stays the degraded
// signature, a malformed call is not exempt, and long visible text next to
// the call is judged as an answer.
func qualityToolCallWithoutReasoning(sig Signals) bool {
	return sig.HasToolCall && sig.ToolCallValid &&
		!sig.HasThinking && !sig.HasReasoningDelta && sig.ReasoningTokens <= 0 &&
		sig.VisibleTokens < defaultQualityPreambleTokens
}

// qualityReasoningSkippedVerdict delivers a tool-call turn without a
// reasoning bill. The signals keep ReasoningSkipped so account bookkeeping
// neither counts the turn as a thinking sample nor as a healthy one.
func qualityReasoningSkippedVerdict(sig Signals) QualityVerdict {
	sig.ReasoningSkipped = true
	return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.tool_call_without_reasoning_bill", Signals: sig}
}

// qualityEvidencePrecededVisible reports ordered thinking: evidence arrived
// before the visible answer started. A stream that dumps cipher-only text
// cannot produce evidence ahead of that text.
func qualityEvidencePrecededVisible(sig Signals) bool {
	if sig.EvidenceAtMS <= 0 || sig.FirstVisibleMS <= 0 {
		return false
	}
	return sig.EvidenceAtMS+defaultQualityEvidenceLeadMS <= sig.FirstVisibleMS
}

// qualityIsSilenceBurstDump catches a stream that stayed silent and then
// delivered the whole answer in one short burst. The largest gap is excluded
// from the visible span, so a genuine stream with one network stall is safe.
func qualityIsSilenceBurstDump(sig Signals) bool {
	if sig.HasReasoningDelta || !qualityWindowFinal(sig) {
		return false
	}
	if sig.VisibleGapMaxMS < defaultQualityBurstGapMS || sig.VisibleChunks < defaultQualitySettleChunks {
		return false
	}
	if sig.VisibleTokens < defaultQualityMaxVisible {
		return false
	}
	if sig.VisibleSpanMS-sig.VisibleGapMaxMS >= defaultQualityFastFlushMS {
		return false
	}
	return qualityHasDumpBill(sig)
}

// qualityMissingThinkingConfirmed decides whether a sample may be reported as
// missing_thinking. Reasoning the client asked for is always judged. An
// inferred requirement is excused when the request path cannot expose
// evidence at all, or when the account usually carries evidence and holds no
// thinking strikes yet: a single inferred miss says more about the request
// than about the account.
func qualityMissingThinkingConfirmed(sig Signals) bool {
	if sig.ReasoningExplicit {
		return true
	}
	if sig.CapabilityKnown && !sig.EvidenceCapable {
		return false
	}
	if sig.AccountEvidenceSamples >= defaultQualityPriorMinSamples &&
		sig.AccountEvidenceRate >= defaultQualityPriorEvidenceRate &&
		sig.AccountThinkingStrikes <= 0 {
		return false
	}
	return true
}

func qualityMissingThinkingReason(sig Signals) string {
	if sig.CapabilityKnown && !sig.EvidenceCapable {
		return "reason.detail.reasoning_evidence_not_exposed_by_protocol"
	}
	return "reason.detail.inferred_reasoning_unconfirmed_by_account_history"
}

func qualityIsBurstDump(sig Signals, minOutput int64) bool {
	if sig.HasReasoningDelta || sig.HasToolCall {
		return false
	}
	shortVisible := sig.VisibleTokens > 0 && sig.VisibleTokens < defaultQualityMaxVisible
	heavyReasoning := sig.ReasoningTokens >= defaultQualityMinReasoning
	if qualityIsSilenceBurstDump(sig) {
		return true
	}
	if sig.HoldExpired {
		if sig.VisibleTokens >= minOutput && qualityVisibleStreaming(sig) {
			return false
		}
		return shortVisible && heavyReasoning
	}
	return shortVisible && qualityFastFlush(sig, defaultQualityFastFlushMS) && qualityHasDumpBill(sig)
}

func qualityIsCipherDrool(sig Signals, minOutput int64) bool {
	if minOutput <= 0 {
		minOutput = defaultQualityMinOutput
	}
	if sig.HasReasoningDelta || sig.ReasoningTokens > 0 || sig.EncryptedBytes <= 0 {
		return false
	}
	if !sig.Terminal && qualityEvidencePrecededVisible(sig) {
		// Ordered evidence rules out a mid-stream cipher dump.
		return false
	}
	if sig.VisibleTokens >= defaultQualityCipherVisible {
		return true
	}
	return sig.Terminal && sig.VisibleTokens >= minOutput
}

func qualityIsFakeEncryptedDump(sig Signals, minOutput int64) bool {
	_ = minOutput
	if sig.HasReasoningDelta || sig.HasToolCall {
		return false
	}
	if !qualityFastFlush(sig, defaultQualityFakeFlushMS) {
		return false
	}
	return qualityHasDumpBill(sig)
}

func qualityIsFastReasoningRatioDump(sig Signals) bool {
	if !sig.HasReasoningDelta || sig.HasToolCall {
		return false
	}
	if !qualityFastFlush(sig, defaultQualityFakeFlushMS) {
		return false
	}
	output := sig.OutputTokens
	if output <= 0 {
		output = sig.VisibleTokens + sig.ReasoningTokens
	}
	if output <= 0 || sig.ReasoningTokens <= 0 {
		return false
	}
	return sig.ReasoningTokens*5 >= output*4
}

// ClassifyQualityHold judges a held stream. burstGuard enables the dump checks
// that read arrival timing (buffered_burst, fake_encrypted_dump and
// fast_reasoning_ratio_dump). An upstream that keeps hidden reasoning silent
// and then flushes the reasoning item and the answer together makes every
// healthy response look like a timing dump, so those checks follow the same
// burst_guard switch as the speed checks. cipher_drool stays on: it reads
// inconsistent evidence, encrypted text without any billed reasoning, not time.
func ClassifyQualityHold(sig Signals, minOutput int64, burstGuard bool) QualityVerdict {
	if minOutput <= 0 {
		minOutput = defaultQualityMinOutput
	}
	if sig.EncryptedFloor <= 0 {
		sig.EncryptedFloor = encryptedFloorWithConfig(sig.ReasoningTokens, defaultEncryptedBytes, defaultEncryptedBytesPerToken)
	}
	if sig.EncryptedBytes > 0 && sig.EncryptedBytes >= sig.EncryptedFloor {
		sig.HasThinking = true
	}
	switch {
	case burstGuard && qualityIsBurstDump(sig, minOutput):
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassBufferedBurst, Reason: "reason.detail.short_generation_window_dumped_output", Signals: sig}
	case qualityIsCipherDrool(sig, minOutput):
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassCipherDrool, Reason: "reason.detail.cipher_only_stream_is_dumping_visible_text", Signals: sig}
	case burstGuard && qualityIsFakeEncryptedDump(sig, minOutput):
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassFakeEncryptedDump, Reason: "reason.detail.encrypted_or_billed_reasoning_preceded_a_fast_visible_dump", Signals: sig}
	case burstGuard && qualityIsFastReasoningRatioDump(sig):
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassFastReasoningRatio, Reason: "reason.detail.reasoning_tokens_dominate_a_fast_visible_dump", Signals: sig}
	}
	if isSemanticOnly(sig) {
		return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.semantic_output_without_visible_text", Signals: sig}
	}
	if sig.HasThinking {
		if sig.HasReasoningDelta {
			return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.real_reasoning_delta_observed", Signals: sig}
		}
		if sig.Terminal {
			return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.encrypted_reasoning_met_the_floor", Signals: sig}
		}
		if sig.VisibleTokens >= minOutput && qualityVisibleStreaming(sig) {
			return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.encrypted_reasoning_with_sustained_visible_output", Signals: sig}
		}
		if sig.HasToolCall {
			// The dump checks above skip tool-call turns, so the evidence gate is
			// the only remaining check and it already passed.
			return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.encrypted_reasoning_preceded_a_tool_call", Signals: sig}
		}
		if sig.HoldExpired {
			// The hold window is over and no dump was found. Waiting for the
			// terminal event cannot add evidence and only delays the client.
			return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.encrypted_reasoning_held_until_timeout", Signals: sig}
		}
		return QualityVerdict{Action: QualityActionWait, Class: ClassNone, Reason: "reason.detail.waiting_for_encrypted_reasoning_evidence", Signals: sig}
	}
	output := qualityThinkingOutput(sig)
	enough := output >= minOutput
	if sig.ReasoningStarted && !sig.Terminal && !sig.HoldExpired && !qualityReasoningWaitSettled(sig, minOutput) {
		return QualityVerdict{Action: QualityActionWait, Class: ClassNone, Reason: "reason.detail.waiting_for_reasoning_evidence", Signals: sig}
	}
	if sig.Terminal {
		if output <= 0 {
			return QualityVerdict{Action: QualityActionWait, Class: ClassNone, Reason: "reason.detail.empty_terminal_stream", Signals: sig}
		}
		if qualityToolCallWithoutReasoning(sig) {
			return qualityReasoningSkippedVerdict(sig)
		}
		if enough {
			if qualityMissingThinkingConfirmed(sig) {
				return QualityVerdict{Action: QualityActionWithhold, Class: ClassMissingThinking, Reason: "reason.detail.no_real_reasoning_evidence", Signals: sig}
			}
			return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: qualityMissingThinkingReason(sig), Signals: sig}
		}
		return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.short_response_below_thinking_threshold", Signals: sig}
	}
	if !sig.HoldExpired && !sig.ReasoningStarted && sig.VisibleTokens < defaultQualityPreambleTokens {
		// Short text without evidence may still be the preamble of a client
		// tool call, and the reasoning bill only arrives with the terminal
		// event. Judging it before then would reject a legitimate tool turn.
		// A stream that started reasoning is not waited on: healthy thinking
		// shows its evidence before any visible text, so that case is settled.
		return QualityVerdict{Action: QualityActionWait, Class: ClassNone, Reason: "reason.detail.waiting_for_enough_output", Signals: sig}
	}
	if sig.HoldExpired && qualityToolCallWithoutReasoning(sig) {
		return qualityReasoningSkippedVerdict(sig)
	}
	if enough {
		if qualityMissingThinkingConfirmed(sig) {
			return QualityVerdict{Action: QualityActionWithhold, Class: ClassMissingThinking, Reason: "reason.detail.no_real_reasoning_evidence", Signals: sig}
		}
		return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: qualityMissingThinkingReason(sig), Signals: sig}
	}
	if sig.HoldExpired {
		if output <= 0 {
			return QualityVerdict{Action: QualityActionWait, Class: ClassNone, Reason: "reason.detail.hold_expired_without_output", Signals: sig}
		}
		if enough {
			if qualityMissingThinkingConfirmed(sig) {
				return QualityVerdict{Action: QualityActionWithhold, Class: ClassMissingThinking, Reason: "reason.detail.no_real_reasoning_evidence", Signals: sig}
			}
			return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: qualityMissingThinkingReason(sig), Signals: sig}
		}
		return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.short_response_below_thinking_threshold", Signals: sig}
	}
	return QualityVerdict{Action: QualityActionWait, Class: ClassNone, Reason: "reason.detail.waiting_for_enough_output", Signals: sig}
}
func isDumpClass(class string) bool {
	switch class {
	case ClassBufferedBurst, ClassCipherDrool, ClassFakeEncryptedDump, ClassFastReasoningRatio:
		return true
	default:
		return false
	}
}

func isSemanticOnly(sig Signals) bool {
	return sig.Terminal &&
		sig.SemanticOutput &&
		!sig.HasThinking &&
		sig.ReasoningTokens <= 0 &&
		sig.OutputTokens <= 0 &&
		sig.VisibleTokens <= 0
}

// qualityThinkingOutput keeps a short visible fragment from hiding billed
// output. Mid-stream usage is not trusted: only the visible answer and an
// already reported output count can reach the hold threshold, so a fabricated
// reasoning bill cannot withhold a stream before its terminal event.
func qualityThinkingOutput(sig Signals) int64 {
	output := maxInt64(sig.VisibleTokens, sig.OutputTokens)
	if sig.Terminal || sig.UsageReported {
		output = maxInt64(output, sig.ReasoningTokens)
	}
	return output
}

func isEmptyStream(sig Signals) bool {
	return sig.Terminal && !sig.HasThinking && sig.ReasoningTokens <= 0 && sig.OutputTokens <= 0 && sig.VisibleTokens <= 0 && !sig.SemanticOutput
}

func qualitySpeedSkipped(sig Signals) bool {
	// A heavy reasoning bill cannot fit in a sub-second stream. When the
	// reasoning duration was not observable, skip TPS instead of turning the
	// missing time into a hard_tps verdict.
	return sig.ReasoningTokens >= defaultQualityMinReasoning && sig.DurationMS > 0 && sig.DurationMS < defaultQualityMinGenerationMS
}

func classifyOutputSpeed(sig Signals, cfg config.QualityConfig, failClosed bool) (string, float64, int64) {
	if qualitySpeedSkipped(sig) {
		return ClassNone, 0, 0
	}
	genMS := signalGenerationWindowMS(sig)
	if genMS <= 0 || sig.OutputTokens <= 0 {
		return ClassNone, 0, genMS
	}
	speed := tps(sig.OutputTokens, genMS)
	if cfg.MinPassiveOutputTokens > 0 && sig.OutputTokens < cfg.MinPassiveOutputTokens {
		// A handful of tokens cannot be timed reliably: keep the sample for
		// display and routing, but do not judge the account by it. The egress
		// guard audit uses the same 32-token floor.
		return ClassNone, speed, genMS
	}
	if failClosed && qualityArrivalBurst(sig, cfg) {
		return ClassBufferedBurst, speed, genMS
	}
	if speed >= cfg.HardTPS {
		return ClassHardTPS, speed, genMS
	}
	if speed >= cfg.SoftTPS {
		return ClassSoftTPS, speed, genMS
	}
	return ClassNone, speed, genMS
}

// qualityArrivalBurst is the opt-in fail-closed check: output that arrived
// over a sub-threshold window at a soft-or-higher rate. Healthy accounts send
// client tool-call arguments in one burst, so those turns are not judged by
// their arrival window.
func qualityArrivalBurst(sig Signals, cfg config.QualityConfig) bool {
	if cfg.MinGenerationMS <= 0 || sig.HasToolCall {
		return false
	}
	arrival := arrivalWindowMS(sig.DurationMS, sig.FirstTokenMS, sig.ReasoningTokens)
	return arrival > 0 && arrival < cfg.MinGenerationMS && tps(sig.OutputTokens, arrival) >= cfg.SoftTPS
}

func classifyFinal(sig Signals, cfg config.QualityConfig, reasoningRequired bool, minOutput int64, streaming bool) QualityVerdict {
	sig.ReasoningRequired = reasoningRequired
	sig.EncryptedFloor = encryptedFloor(sig.ReasoningTokens, cfg)
	if sig.EncryptedBytes > 0 && sig.EncryptedBytes >= sig.EncryptedFloor {
		sig.HasThinking = true
	}
	if sig.OutputTokens <= 0 {
		sig.OutputTokens = sig.VisibleTokens
	}
	sig.GenerationMS = signalGenerationWindowMS(sig)
	sig.TPS = tps(sig.OutputTokens, sig.GenerationMS)
	sig.Streaming = streaming
	if isEmptyStream(sig) {
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassEmptyStream, Reason: "reason.detail.upstream_stream_completed_without_content", Signals: sig}
	}
	thinkingRequired := cfg.ThinkingGuard && reasoningRequired
	if streaming {
		hold := ClassifyQualityHold(sig, minOutput, cfg.BurstGuard)
		if hold.Action == QualityActionWithhold {
			switch {
			case !reasoningRequired && isDumpClass(hold.Class):
				hold.Action = QualityActionDeliver
				hold.Class = ClassNone
				hold.Reason = "reason.detail.non_reasoning_request_skipped_dump_detection"
			case hold.Class == ClassMissingThinking && !thinkingRequired:
				hold.Action = QualityActionDeliver
				hold.Class = ClassNone
				if cfg.ThinkingGuard {
					hold.Reason = "reason.detail.reasoning_was_disabled_for_this_request"
				} else {
					hold.Reason = "reason.detail.thinking_guard_is_disabled"
				}
			default:
				return hold
			}
		}
		sig = hold.Signals
		if hold.Action == QualityActionWait {
			if isSemanticOnly(sig) {
				hold.Action = QualityActionDeliver
				hold.Class = ClassNone
			} else {
				return QualityVerdict{Action: QualityActionWithhold, Class: ClassEmptyStream, Reason: "reason.detail.upstream_stream_completed_without_content", Signals: sig}
			}
		}
	}
	skipped := false
	if thinkingRequired && !sig.HasThinking && !isSemanticOnly(sig) {
		if qualityToolCallWithoutReasoning(sig) {
			skipped = true
		} else if output := qualityThinkingOutput(sig); output >= minOutput && qualityMissingThinkingConfirmed(sig) {
			return QualityVerdict{Action: QualityActionWithhold, Class: ClassMissingThinking, Reason: "reason.detail.no_real_reasoning_evidence", Signals: sig}
		}
	}
	class, speed, gen := classifyOutputSpeed(sig, cfg, cfg.BurstGuard)
	sig.GenerationMS = gen
	sig.TPS = speed
	if class != ClassNone {
		return QualityVerdict{Action: QualityActionWithhold, Class: class, Reason: outputSpeedReason(class), Signals: sig}
	}
	if skipped {
		return qualityReasoningSkippedVerdict(sig)
	}
	return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.within_threshold", Signals: sig}
}

func probeStreamUnparsed(sig Signals) bool {
	return sig.ByteCount > 0 &&
		sig.ParsedEvents == 0 &&
		sig.VisibleTokens <= 0 &&
		sig.OutputTokens <= 0 &&
		sig.ReasoningTokens <= 0 &&
		!sig.SemanticOutput
}

func classifyProbeResult(sig Signals, cfg config.QualityConfig, expectedMatched, reasoningRequired bool) QualityVerdict {
	sig.ReasoningRequired = reasoningRequired
	thinkingRequired := cfg.ThinkingGuard && reasoningRequired
	if probeStreamUnparsed(sig) {
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassProbeError, Reason: "reason.detail.probe_stream_produced_no_parseable_events", Signals: sig}
	}
	if !sig.Terminal {
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassProbeError, Reason: "reason.detail.probe_stream_ended_without_a_terminal_event", Signals: sig}
	}
	if !expectedMatched {
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassExpectedMarkerMissing, Reason: "reason.detail.expected_marker_missing", Signals: sig}
	}
	output := qualityThinkingOutput(sig)
	if output < cfg.MinPassiveOutputTokens {
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassInsufficientOutput, Reason: "reason.detail.insufficient_output_tokens", Signals: sig}
	}
	sig.EncryptedFloor = encryptedFloor(sig.ReasoningTokens, cfg)
	if sig.EncryptedBytes > 0 && sig.EncryptedBytes >= sig.EncryptedFloor {
		sig.HasThinking = true
	}
	if thinkingRequired && !sig.HasThinking && output >= thinkingGuardMinOutputTokens {
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassMissingThinking, Reason: "reason.detail.missing_thinking", Signals: sig}
	}
	hold := ClassifyQualityHold(sig, cfg.MinHoldOutputTokens, cfg.BurstGuard)
	if hold.Action == QualityActionWithhold {
		switch {
		case !reasoningRequired && isDumpClass(hold.Class):
			// Non-reasoning probe models legitimately produce fast visible text.
		case hold.Class == ClassMissingThinking:
			// The usage-level thinking gate above already handled every
			// qualifying sample. Remaining hits are below its threshold.
		default:
			return hold
		}
	}
	class, speed, gen := classifyOutputSpeed(sig, cfg, cfg.BurstGuard)
	sig.GenerationMS = gen
	sig.TPS = speed
	if class == ClassNone && cfg.BurstGuard && !qualitySpeedSkipped(sig) &&
		arrivalWindowMS(sig.DurationMS, sig.FirstTokenMS, sig.ReasoningTokens) < cfg.MinGenerationMS {
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassInsufficientGeneration, Reason: "reason.detail.insufficient_generation_window", Signals: sig}
	}
	if class != ClassNone {
		return QualityVerdict{Action: QualityActionWithhold, Class: class, Reason: outputSpeedReason(class), Signals: sig}
	}
	return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.within_threshold", Signals: sig}
}

func classifyPassiveAudit(sig Signals, cfg config.QualityConfig) QualityVerdict {
	if !sig.Streaming {
		return QualityVerdict{Action: QualityActionDeliver, Class: ClassIgnored, Reason: "reason.detail.not_build_stream", Signals: sig}
	}
	if !sig.Terminal || !sig.FirstTokenKnown {
		return QualityVerdict{Action: QualityActionDeliver, Class: ClassIgnored, Reason: "reason.detail.missing_first_token", Signals: sig}
	}
	sig.EncryptedFloor = encryptedFloor(sig.ReasoningTokens, cfg)
	if sig.EncryptedBytes > 0 && sig.EncryptedBytes >= sig.EncryptedFloor {
		sig.HasThinking = true
	}
	sig.GenerationMS = signalGenerationWindowMS(sig)
	sig.TPS = tps(sig.OutputTokens, sig.GenerationMS)
	thinkingOutput := qualityThinkingOutput(sig)
	thinkingEligible := cfg.ThinkingGuard && sig.ReasoningRequired && thinkingOutput >= cfg.MinPassiveOutputTokens
	if sig.GenerationMS <= 0 || (sig.OutputTokens < cfg.MinPassiveOutputTokens && !thinkingEligible) {
		return QualityVerdict{Action: QualityActionDeliver, Class: ClassIgnored, Reason: "reason.detail.insufficient_output_tokens", Signals: sig}
	}
	if thinkingEligible && !sig.HasThinking && !isSemanticOnly(sig) && !qualityToolCallWithoutReasoning(sig) && qualityMissingThinkingConfirmed(sig) {
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassMissingThinking, Reason: "reason.detail.no_real_reasoning_evidence", Signals: sig}
	}
	class, speed, gen := classifyOutputSpeed(sig, cfg, cfg.BurstGuard)
	sig.GenerationMS = gen
	sig.TPS = speed
	if class == ClassNone {
		if thinkingEligible && qualityToolCallWithoutReasoning(sig) {
			return qualityReasoningSkippedVerdict(sig)
		}
		return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.within_threshold", Signals: sig}
	}
	return QualityVerdict{Action: QualityActionWithhold, Class: class, Reason: outputSpeedReason(class), Signals: sig}
}

func classifyNonStreamResult(sig Signals, cfg config.QualityConfig, reasoningRequired bool, minOutput int64) QualityVerdict {
	sig.ReasoningRequired = reasoningRequired
	sig.Streaming = false
	sig.Terminal = true
	sig.EncryptedFloor = encryptedFloor(sig.ReasoningTokens, cfg)
	if sig.EncryptedBytes > 0 && sig.EncryptedBytes >= sig.EncryptedFloor {
		sig.HasThinking = true
	}
	if sig.OutputTokens <= 0 {
		sig.OutputTokens = sig.VisibleTokens
	}
	// Non-streaming responses have no observable generation window. Deriving a
	// TPS from parse latency produced false hard_tps verdicts for every request.
	sig.GenerationMS = 0
	sig.TPS = 0
	if isEmptyStream(sig) {
		return QualityVerdict{Action: QualityActionWithhold, Class: ClassEmptyStream, Reason: "reason.detail.upstream_response_completed_without_content", Signals: sig}
	}
	if cfg.ThinkingGuard && reasoningRequired && !sig.HasThinking && !isSemanticOnly(sig) {
		if qualityToolCallWithoutReasoning(sig) {
			return qualityReasoningSkippedVerdict(sig)
		}
		output := qualityThinkingOutput(sig)
		if output >= minOutput && qualityMissingThinkingConfirmed(sig) {
			return QualityVerdict{Action: QualityActionWithhold, Class: ClassMissingThinking, Reason: "reason.detail.no_real_reasoning_evidence", Signals: sig}
		}
	}
	return QualityVerdict{Action: QualityActionDeliver, Class: ClassNone, Reason: "reason.detail.within_threshold", Signals: sig}
}

func outputSpeedReason(class string) string {
	switch class {
	case ClassBufferedBurst:
		return "reason.detail.short_generation_window_dumped_output"
	case ClassHardTPS:
		return "reason.detail.output_tokens_per_second_exceeded_hard_threshold"
	case ClassSoftTPS:
		return "reason.detail.output_tokens_per_second_exceeded_soft_threshold"
	default:
		return ""
	}
}
func reasoningRequired(body []byte) bool {
	if len(body) == 0 {
		return true
	}
	return reasoningRequiredPayload(decodeRequestPayload(body))
}

func reasoningRequiredForModel(model string, body []byte) bool {
	return reasoningRequiredForModelPayload(model, decodeRequestPayload(body))
}

func reasoningRequiredForModelWithNonReasoning(model string, body []byte, nonReasoning []string) bool {
	return reasoningRequiredForModelPayloadWithNonReasoning(model, decodeRequestPayload(body), nonReasoning)
}

func modelFromRequestBody(body []byte) string {
	payload := decodeRequestPayload(body)
	if payload == nil {
		return ""
	}
	return strings.TrimSpace(asString(payload["model"]))
}

func disabledReasoningValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return !typed
	case string:
		normalized := strings.ToLower(strings.TrimSpace(typed))
		return normalized == "none" || normalized == "off" || normalized == "disabled" || normalized == "false"
	case map[string]any:
		if kind := strings.ToLower(strings.TrimSpace(asString(typed["type"]))); kind == "disabled" || kind == "none" || kind == "off" {
			return true
		}
		if effort := strings.ToLower(strings.TrimSpace(asString(typed["effort"]))); effort == "none" || effort == "off" {
			return true
		}
		if enabled, ok := typed["enabled"].(bool); ok && !enabled {
			return true
		}
		if mode := strings.ToLower(strings.TrimSpace(asString(typed["mode"]))); mode == "none" || mode == "off" {
			return true
		}
		if budget, ok := numeric(typed["budget_tokens"]); ok && budget <= 0 {
			return true
		}
	}
	return false
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		if typed == math.Trunc(typed) {
			return strconvFormatInt(int64(typed))
		}
	}
	return ""
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if text := asString(values[key]); text != "" {
			return text
		}
	}
	return ""
}

func firstInt(values map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := numeric(values[key]); ok {
			return value
		}
	}
	return 0
}

func numeric(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int8:
		return int64(typed), true
	case int16:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case uint:
		return int64(typed), true
	case uint8:
		return int64(typed), true
	case uint16:
		return int64(typed), true
	case uint32:
		return int64(typed), true
	case uint64:
		return int64(typed), true
	case float32:
		return int64(typed), true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, errParse := typed.Int64()
		return parsed, errParse == nil
	case string:
		parsed, errParse := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, errParse == nil
	default:
		return 0, false
	}
}

func strconvFormatInt(value int64) string {
	return fmt.Sprintf("%d", value)
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
