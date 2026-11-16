package app

import (
	"context"
	"time"

	appconfig "grok-quality-guard/internal/config"
	"grok-quality-guard/internal/quality"
)

func NewStreamAnalyzer() *quality.StreamAnalyzer {
	return quality.NewStreamAnalyzer()
}

func NewStreamAnalyzerWithProtocol(protocol string) *quality.StreamAnalyzer {
	return quality.NewStreamAnalyzerWithProtocol(protocol)
}

func NewStreamAnalyzerWithProtocolAt(protocol string, startedAt time.Time) *quality.StreamAnalyzer {
	return quality.NewStreamAnalyzerWithProtocolAt(protocol, startedAt)
}

func ClassifyQualityHold(sig quality.Signals, minOutput int64) quality.QualityVerdict {
	return quality.ClassifyQualityHold(sig, minOutput)
}

func qualityIsBurstDump(sig quality.Signals, minOutput int64) bool {
	return quality.QualityIsBurstDump(sig, minOutput)
}

func isDumpClass(class string) bool {
	return quality.IsDumpClass(class)
}

func classifyFinal(sig quality.Signals, cfg appconfig.QualityConfig, reasoningRequired bool, minOutput int64, streaming bool) quality.QualityVerdict {
	return quality.ClassifyFinal(sig, cfg, reasoningRequired, minOutput, streaming)
}

func classifyProbeResult(sig quality.Signals, cfg appconfig.QualityConfig, expectedMatched, reasoningRequired bool) quality.QualityVerdict {
	return quality.ClassifyProbeResult(sig, cfg, expectedMatched, reasoningRequired)
}

func classifyPassiveAudit(sig quality.Signals, cfg appconfig.QualityConfig) quality.QualityVerdict {
	return quality.ClassifyPassiveAudit(sig, cfg)
}

func classifyNonStreamResult(sig quality.Signals, cfg appconfig.QualityConfig, reasoningRequired bool, minOutput int64) quality.QualityVerdict {
	return quality.ClassifyNonStreamResult(sig, cfg, reasoningRequired, minOutput)
}

func reasoningRequiredForModel(modelName string, body []byte) bool {
	return quality.ReasoningRequiredForModel(modelName, body)
}

func (p *Plugin) applyGuardDisable(authID, kind string, duration time.Duration, class, source string, strike bool) error {
	return p.qualitySvc.ApplyGuardDisable(authID, kind, duration, class, source, strike)
}

func (p *Plugin) applyAutoCooldown(authID string, verdict quality.QualityVerdict, source string) error {
	return p.qualitySvc.ApplyAutoCooldown(authID, verdict, source)
}

func (p *Plugin) ManualCooldown(ctx context.Context, authID string, duration time.Duration) error {
	return p.qualitySvc.ManualCooldown(ctx, authID, duration)
}

func (p *Plugin) ManualDisable(ctx context.Context, authID, reason string) error {
	return p.qualitySvc.ManualDisable(ctx, authID, reason)
}

func (p *Plugin) ManualRestore(ctx context.Context, authID string) error {
	return p.qualitySvc.ManualRestore(ctx, authID)
}
