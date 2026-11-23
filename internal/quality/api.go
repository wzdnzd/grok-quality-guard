package quality

import "grok-quality-guard/internal/config"

const (
	ProtocolChat      = qualityProtocolChat
	ProtocolResponses = qualityProtocolResponses
	ProtocolAnthropic = qualityProtocolAnthropic
)

// Signals and verdict helpers are re-exported through exported names so the
// application package can keep its orchestration code free of analyzer internals.

func AsString(value any) string { return asString(value) }

func Numeric(value any) (int64, bool)  { return numeric(value) }
func MaxInt64(left, right int64) int64 { return maxInt64(left, right) }

func TPS(outputTokens, generationMS int64) float64 { return tps(outputTokens, generationMS) }
func GenerationWindowMS(durationMS, firstTokenMS, reasoningTokens int64) int64 {
	return generationWindowMS(durationMS, firstTokenMS, reasoningTokens)
}

func QualityIsBurstDump(sig Signals, minOutput int64) bool { return qualityIsBurstDump(sig, minOutput) }

func IsDumpClass(class string) bool { return isDumpClass(class) }

func ClassifyFinal(sig Signals, cfg config.QualityConfig, reasoningRequired bool, minOutput int64, streaming bool) QualityVerdict {
	return classifyFinal(sig, cfg, reasoningRequired, minOutput, streaming)
}

func ClassifyProbeResult(sig Signals, cfg config.QualityConfig, expectedMatched, reasoningRequired bool) QualityVerdict {
	return classifyProbeResult(sig, cfg, expectedMatched, reasoningRequired)
}
func ClassifyPassiveAudit(sig Signals, cfg config.QualityConfig) QualityVerdict {
	return classifyPassiveAudit(sig, cfg)
}
func ClassifyNonStreamResult(sig Signals, cfg config.QualityConfig, reasoningRequired bool, minOutput int64) QualityVerdict {
	return classifyNonStreamResult(sig, cfg, reasoningRequired, minOutput)
}

func ReasoningRequired(body []byte) bool { return reasoningRequired(body) }
func ReasoningRequiredForModel(modelName string, body []byte) bool {
	return reasoningRequiredForModel(modelName, body)
}
func ReasoningRequiredForModelWithNonReasoning(modelName string, body []byte, nonReasoning []string) bool {
	return reasoningRequiredForModelWithNonReasoning(modelName, body, nonReasoning)
}
func ModelFromRequestBody(body []byte) string { return modelFromRequestBody(body) }
func DisabledReasoningValue(value any) bool   { return disabledReasoningValue(value) }
