package state

import (
	"time"

	"grok-quality-guard/internal/quality"
)

// QualityEvidenceFromVerdict projects an analyzer verdict into persisted state.
func QualityEvidenceFromVerdict(verdict quality.QualityVerdict, source string, at time.Time) *QualityEvidence {
	sig := verdict.Signals
	return &QualityEvidence{
		At: at, Source: source, Action: verdict.Action, Class: verdict.Class, Reason: verdict.Reason,
		Protocol: sig.Protocol, Streaming: sig.Streaming, Terminal: sig.Terminal, HoldExpired: sig.HoldExpired,
		HasThinking: sig.HasThinking, HasReasoningDelta: sig.HasReasoningDelta, ReasoningRequired: sig.ReasoningRequired, ReasoningStarted: sig.ReasoningStarted,
		SemanticOutput: sig.SemanticOutput, UsageReported: sig.UsageReported,
		VisibleRunes: sig.VisibleRunes, VisibleTokens: sig.VisibleTokens, OutputTokens: sig.OutputTokens,
		ReasoningTokens: sig.ReasoningTokens, EncryptedBytes: sig.EncryptedBytes, EncryptedFloor: sig.EncryptedFloor,
		FirstTokenMS: sig.FirstTokenMS, DurationMS: sig.DurationMS, GenerationMS: sig.GenerationMS,
		VisibleFlushMS: sig.VisibleFlushMS, VisibleSpanMS: sig.VisibleSpanMS, VisibleChunks: sig.VisibleChunks, TPS: sig.TPS,
		ChunkCount: sig.ChunkCount, ByteCount: sig.ByteCount, EventCount: sig.EventCount,
		ParsedEvents: sig.ParsedEvents, UnknownEvents: sig.UnknownEvents, ParseFailures: sig.ParseFailures,
	}
}
