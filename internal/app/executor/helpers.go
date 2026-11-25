package executor

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"grok-quality-guard/internal/abi"
	"grok-quality-guard/internal/app/auth"
	"grok-quality-guard/internal/app/i18n"
	"grok-quality-guard/internal/app/model"
	"grok-quality-guard/internal/app/redact"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
	domain "grok-quality-guard/internal/quality"
	"grok-quality-guard/internal/sse"
)

type Account = model.Account
type Policy = config.Policy
type QualityConfig = config.QualityConfig
type QualityVerdict = domain.QualityVerdict
type QualitySignals = domain.Signals
type StreamAnalyzer = domain.StreamAnalyzer
type Event = appstate.Event
type sseOutputNormalizer = sse.Normalizer

const (
	StateUnknown            = appstate.StateUnknown
	StateHealthy            = appstate.StateHealthy
	StateCooling            = appstate.StateCooling
	StateQuotaExhausted     = appstate.StateQuotaExhausted
	StateCredentialInvalid  = appstate.StateCredentialInvalid
	StateDisabledByGuard    = appstate.StateDisabledByGuard
	StateExternallyDisabled = appstate.StateExternallyDisabled
)

const (
	ClassBufferedBurst         = domain.ClassBufferedBurst
	ClassCipherDrool           = domain.ClassCipherDrool
	ClassFakeEncryptedDump     = domain.ClassFakeEncryptedDump
	ClassMissingThinking       = domain.ClassMissingThinking
	ClassEmptyStream           = domain.ClassEmptyStream
	ClassProbeError            = domain.ClassProbeError
	ClassProxyError            = domain.ClassProxyError
	ClassCredentialInvalid     = domain.ClassCredentialInvalid
	ClassQuotaExhausted        = domain.ClassQuotaExhausted
	ClassRateLimited           = domain.ClassRateLimited
	ClassIgnored               = domain.ClassIgnored
	ClassInconclusive          = domain.ClassInconclusive
	ClassNone                  = domain.ClassNone
	ClassExpectedMarkerMissing = domain.ClassExpectedMarkerMissing
	QualityActionWait          = domain.QualityActionWait
	QualityActionDeliver       = domain.QualityActionDeliver
	QualityActionWithhold      = domain.QualityActionWithhold
)

func okEnvelope(value any) ([]byte, error)                  { return abi.OK(value) }
func errorEnvelope(code, message string, status int) []byte { return abi.Error(code, message, status) }
func executorRequestLocale(headers http.Header) string {
	if headers == nil {
		return i18n.DefaultLocale
	}
	values := make([]string, 0, 1)
	for name, list := range headers {
		if strings.EqualFold(name, "Accept-Language") {
			values = append(values, list...)
		}
	}
	return i18n.Negotiate(strings.Join(values, ","))
}
func localizedErrorEnvelope(locale, code, messageKey string, status int) []byte {
	return errorEnvelope(code, i18n.T(locale, messageKey, nil), status)
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
func sanitizeError(err error) string                  { return redact.SanitizeError(err) }
func authRawString(raw map[string]json.RawMessage, key string) string {
	return auth.RawString(raw, key)
}
func isProbeHeader(headers map[string][]string) bool { return auth.IsProbeHeader(headers) }
func cloneHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}
func cloneValues(values url.Values) url.Values {
	if values == nil {
		return nil
	}
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}
func isIsolatedState(value string) bool {
	switch value {
	case StateCooling, StateQuotaExhausted, StateCredentialInvalid, StateDisabledByGuard, StateExternallyDisabled:
		return true
	default:
		return false
	}
}

func modelFromRequestBody(body []byte) string { return domain.ModelFromRequestBody(body) }
func disabledReasoningValue(value any) bool   { return domain.DisabledReasoningValue(value) }
func NewStreamAnalyzerWithProtocolAt(protocol string, startedAt time.Time) *StreamAnalyzer {
	return domain.NewStreamAnalyzerWithProtocolAt(protocol, startedAt)
}
func classifyFinal(sig domain.Signals, cfg QualityConfig, reasoningRequired bool, minOutput int64, streaming bool) QualityVerdict {
	return domain.ClassifyFinal(sig, cfg, reasoningRequired, minOutput, streaming)
}
func classifyNonStreamResult(sig domain.Signals, cfg QualityConfig, reasoningRequired bool, minOutput int64) QualityVerdict {
	return domain.ClassifyNonStreamResult(sig, cfg, reasoningRequired, minOutput)
}
func ClassifyQualityHold(sig domain.Signals, minOutput int64, burstGuard bool) QualityVerdict {
	return domain.ClassifyQualityHold(sig, minOutput, burstGuard)
}
