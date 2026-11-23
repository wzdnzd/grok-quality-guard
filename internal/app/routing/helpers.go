package routing

import (
	"strings"

	"grok-quality-guard/internal/app/auth"
	execapp "grok-quality-guard/internal/app/executor"
	appstate "grok-quality-guard/internal/app/state"
	domain "grok-quality-guard/internal/quality"
)

const (
	StateUnknown            = appstate.StateUnknown
	StateHealthy            = appstate.StateHealthy
	StateSuspect            = appstate.StateSuspect
	StateDegraded           = appstate.StateDegraded
	StateCooling            = appstate.StateCooling
	StateQuotaExhausted     = appstate.StateQuotaExhausted
	StateCredentialInvalid  = appstate.StateCredentialInvalid
	StateDisabledByGuard    = appstate.StateDisabledByGuard
	StateExternallyDisabled = appstate.StateExternallyDisabled
)

const (
	ClassIgnored     = domain.ClassIgnored
	ClassRateLimited = domain.ClassRateLimited
	ClassProbeError  = domain.ClassProbeError
	ClassProxyError  = domain.ClassProxyError
)

func isGrokProvider(value string) bool { return auth.IsGrokProvider(value) }
func isTransparentCandidateState(value string) bool {
	switch value {
	case "", StateHealthy, StateUnknown:
		return true
	default:
		return false
	}
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
func asString(value any) string                       { return domain.AsString(value) }
func decodeRequestPayload(body []byte) map[string]any { return domain.DecodeRequestPayload(body) }
func reasoningRequiredForModelPayload(name string, payload map[string]any) bool {
	return domain.ReasoningRequiredForModelPayload(name, payload)
}
func reasoningRequiredForModelPayloadWithNonReasoning(name string, payload map[string]any, nonReasoning []string) bool {
	return domain.ReasoningRequiredForModelPayloadWithNonReasoning(name, payload, nonReasoning)
}
func ensureTransparentReasoningEvidence(body []byte, sourceFormat string, required bool) []byte {
	return execapp.EnsureTransparentReasoningEvidence(body, sourceFormat, required)
}
func safeReplayPayload(payload map[string]any, sourceFormat string) bool {
	return execapp.SafeReplayPayload(payload, sourceFormat)
}
func executionModel(req Request, body []byte) string { return execapp.ExecutionModel(req, body) }
