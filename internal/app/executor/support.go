package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/i18n"
	domain "grok-quality-guard/internal/quality"
)

// attemptInfo carries the request-level reasoning context into one transparent
// attempt: whether the client asked for reasoning, whether the request path
// can expose evidence at all, and the account's own evidence history.
type attemptInfo struct {
	ReasoningNeeded   bool
	ReasoningExplicit bool
	EvidenceCapable   bool
	EvidenceSamples   int
	EvidenceHits      int
	ThinkingStrikes   int
}

func (s *Service) transparentAttemptInfo(facts Facts, authID string) attemptInfo {
	info := attemptInfo{
		ReasoningNeeded:   facts.ReasoningRequired,
		ReasoningExplicit: facts.ReasoningExplicit,
		EvidenceCapable:   facts.EvidenceCapable,
	}
	if s != nil && s.env != nil {
		if account, ok := s.env.StateAccount(authID); ok {
			info.EvidenceSamples = account.EvidenceSamples
			info.EvidenceHits = account.EvidenceHits
			info.ThinkingStrikes = account.ThinkingStrikes
		}
	}
	if info.EvidenceHits > 0 {
		// Evidence observed on this account proves the path can expose it even
		// when the client protocol alone suggests otherwise.
		info.EvidenceCapable = true
	}
	return info
}

// decorate attaches the reasoning context to a signal snapshot before the
// classifier runs, so the decision and its evidence stay consistent.
func (info attemptInfo) decorate(sig domain.Signals) domain.Signals {
	sig.ReasoningRequired = info.ReasoningNeeded
	sig.ReasoningExplicit = info.ReasoningExplicit
	sig.CapabilityKnown = true
	sig.EvidenceCapable = info.EvidenceCapable
	sig.AccountEvidenceSamples = int64(info.EvidenceSamples)
	sig.AccountThinkingStrikes = info.ThinkingStrikes
	if info.EvidenceSamples > 0 {
		sig.AccountEvidenceRate = float64(info.EvidenceHits) / float64(info.EvidenceSamples)
	}
	return sig
}

const defaultTransparentStreamIdleTimeout = 90 * time.Second
const defaultTransparentAttemptTimeout = 120 * time.Second
const defaultTransparentRequestTimeout = 240 * time.Second

// maxVerifyWindow caps the shortest hold that can still produce a verdict
const maxVerifyWindow = 3 * time.Second

func transparentMaxAttempts(policy Policy) int {
	maxAttempts := policy.Quality.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 6
	}
	return maxAttempts
}

// transparentStreamIdleTimeout bounds how long the upstream may stay silent.
// It is an inactivity window that restarts on every chunk, not a total
// budget: a long reasoning step that keeps the connection alive must not be
// mistaken for a dead stream.
func transparentStreamIdleTimeout(cfg QualityConfig) time.Duration {
	if cfg.StreamIdleTimeout.Duration > 0 {
		return cfg.StreamIdleTimeout.Duration
	}
	return defaultTransparentStreamIdleTimeout
}

func transparentAttemptTimeout(cfg QualityConfig) time.Duration {
	if cfg.AttemptTimeout.Duration > 0 {
		return cfg.AttemptTimeout.Duration
	}
	return defaultTransparentAttemptTimeout
}

// earlierDeadline returns whichever deadline comes first. A zero deadline means
// unbounded and defers to the other one.
func earlierDeadline(first, second time.Time) time.Time {
	if first.IsZero() {
		return second
	}
	if second.IsZero() || first.Before(second) {
		return first
	}
	return second
}

func transparentRequestTimeout(cfg QualityConfig) time.Duration {
	if cfg.RequestTimeout.Duration > 0 {
		return cfg.RequestTimeout.Duration
	}
	return defaultTransparentRequestTimeout
}

func transparentFailureCanFailover(replayable, emitted, preResponse bool, class string) bool {
	if emitted {
		return false
	}
	if replayable {
		return true
	}
	// A non-replayable request is still safe to move to another account when
	// transport failed before the host returned a response or opened a stream.
	// Once a stream exists, a read failure may follow a partially executed
	// request, so only replayable bodies may be retried.
	if !preResponse {
		return false
	}
	switch class {
	case ClassProxyError, ClassProbeError:
		return true
	default:
		return false
	}
}

func shouldRotateTransparentFailure(hasAlternative bool, class string) bool {
	// Account-level quality evidence and transport failures should switch
	// accounts first. Proxy rotation is the single-account/low-latency fallback.
	switch class {
	case ClassMissingThinking, ClassProxyError, ClassProbeError:
		return !hasAlternative
	default:
		return true
	}
}

func proxyFailureReachedRotationThreshold(class string, strikes, threshold int) bool {
	if class != ClassProxyError && class != ClassProbeError {
		return false
	}
	if threshold <= 0 {
		threshold = 2
	}
	return strikes+1 >= threshold
}

func executorRequestInput(req Request) ([]byte, string, error) {
	if len(req.Payload) > 0 {
		sourceFormat := firstNonEmpty(req.SourceFormat, req.Format, "codex")
		return req.Payload, sourceFormat, nil
	}
	body := req.OriginalRequest
	if len(body) == 0 {
		return nil, "", fmt.Errorf("request body is empty")
	}
	sourceFormat := firstNonEmpty(req.SourceFormat, req.Format, "codex")
	return body, sourceFormat, nil
}

func executionModel(req Request, body []byte) string {
	return firstNonEmpty(req.Model, asString(req.Metadata["model"]), modelFromRequestBody(body), req.Alt)
}

// ensureTransparentReasoningEvidence mirrors the grok2api Build/Console request
// boundary. Codex and Responses clients may omit encrypted reasoning from the
// include list, which makes a healthy stream indistinguishable from a degraded
// one. Add the include before the quality gate observes the upstream stream.
func ensureTransparentReasoningEvidence(body []byte, sourceFormat string, reasoningRequired bool) []byte {
	if len(body) == 0 {
		return body
	}
	format := strings.ToLower(strings.TrimSpace(sourceFormat))
	if !strings.Contains(format, "codex") && !strings.Contains(format, "response") {
		return body
	}
	if !reasoningRequired {
		return body
	}
	var payload map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil || payload == nil {
		return body
	}
	if _, ok := payload["input"]; !ok {
		return body
	}
	includes := make([]string, 0)
	if raw, exists := payload["include"]; exists && len(raw) > 0 && string(raw) != "null" {
		if errUnmarshal := json.Unmarshal(raw, &includes); errUnmarshal != nil {
			return body
		}
		for _, name := range includes {
			if strings.TrimSpace(name) == "" {
				continue
			}
			if name == "reasoning.encrypted_content" {
				return body
			}
		}
	}
	encodedInclude, errMarshalInclude := json.Marshal(append(includes, "reasoning.encrypted_content"))
	if errMarshalInclude != nil {
		return body
	}
	payload["include"] = encodedInclude
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return body
	}
	return encoded
}

func buildHostExecution(req Request, body []byte, sourceFormat, authID, proxyURL string, stream bool) pluginapi.HostModelExecutionRequest {
	return buildHostExecutionWithModel(req, body, sourceFormat, executionModel(req, body), authID, proxyURL, stream)
}

func buildHostExecutionWithModel(req Request, body []byte, sourceFormat, model, authID, proxyURL string, stream bool) pluginapi.HostModelExecutionRequest {
	return pluginapi.HostModelExecutionRequest{
		EntryProtocol:  sourceFormat,
		ExitProtocol:   firstNonEmpty(req.Format, sourceFormat),
		Model:          model,
		Stream:         stream,
		Body:           body,
		Headers:        cloneHeader(req.Headers),
		Query:          cloneValues(req.Query),
		Alt:            req.Alt,
		ForcedProvider: "xai",
		AuthID:         authID,
		ProxyURL:       strings.TrimSpace(proxyURL),
	}
}

func (s *Service) emitPluginChunks(streamID string, payloads [][]byte) error {
	for _, payload := range payloads {
		if errEmit := s.emitPluginStreamChunk(streamID, payload); errEmit != nil {
			return errEmit
		}
	}
	return nil
}

func (s *Service) emitPluginStreamChunk(streamID string, payload []byte) error {
	if strings.TrimSpace(streamID) == "" {
		return fmt.Errorf("plugin stream id is required")
	}
	return s.env.EmitPluginStreamChunk(streamID, append([]byte(nil), payload...))
}

func (s *Service) closePluginStream(streamID, code, message string, status int) {
	s.env.ClosePluginStream(streamID, code, message, status)
}

func (s *Service) closePluginErrorStream(streamID, locale, code string, status int) {
	key := "api.error." + code
	message := i18n.T(locale, key, nil)
	if !i18n.Has(locale, key) {
		message = i18n.T(locale, "api.error.internal", nil)
	}
	s.closePluginStream(streamID, code, message, status)
}

func (s *Service) recordTransparentOverflow(authID string, limit int64) {
	if s == nil || s.env == nil || s.env.Store() == nil {
		return
	}
	event := Event{
		Time:       time.Now().UTC(),
		AuthID:     authID,
		Action:     "transparent_overflow",
		Result:     "delivered",
		Detail:     fmt.Sprintf("hold buffer exceeded %d bytes; quality gate bypassed", limit),
		DetailKey:  "event.detail.hold_buffer_exceeded",
		DetailArgs: map[string]any{"limit": limit},
	}
	// The overflow is recorded from inside the stream loop, so a full state
	// write must not stall the chunks that follow.
	s.runSettlement(func(context.Context) {
		_ = s.env.Store().AppendEvent(event)
	})
}

func (s *Service) recordAttemptError(authID, class string, errValue error) error {
	if s.env.Store() == nil {
		return errValue
	}
	return s.env.Store().AppendEvent(Event{
		Time:      time.Now().UTC(),
		AuthID:    authID,
		Class:     class,
		Action:    "transparent_attempt",
		Result:    "failed",
		Detail:    sanitizeError(errValue),
		DetailKey: "event.detail.quality_gate_error",
	})
}

func (s *Service) applyTransparentFailureVerdict(ctx context.Context, authID string, verdict QualityVerdict, class string, errValue error, callbackID string) {
	if verdict.Class == "" {
		verdict.Class = class
	}
	if verdict.Class == "" || verdict.Class == ClassIgnored {
		return
	}
	if verdict.Reason == "" && errValue != nil {
		verdict.Reason = "reason.detail.unknown_error"
	}
	_ = s.env.ApplyVerdict(ctx, authID, verdict, "transparent", callbackID)
}

// rotateAfterFailure runs before the verdict is applied so the same auth can retry on the new proxy
func (s *Service) rotateAfterFailure(ctx context.Context, authID, class string) (bool, error) {
	if !s.env.AutoRotateAfterQualityFailure() {
		return false, nil
	}
	stateAccount, _ := s.env.StateAccount(authID)
	if proxyFailureReachedRotationThreshold(class, stateAccount.ErrorStrikes, s.env.Policy().ProxyRotateAfterErrors) {
		// Proxy failures rotate only after the same consecutive-error threshold
		// used by account quality handling.
	} else if class != ClassMissingThinking && class != ClassCipherDrool && class != ClassFakeEncryptedDump && class != ClassBufferedBurst {
		return false, nil
	}
	if isIsolatedState(stateAccount.State) {
		return false, nil
	}
	binding, managed := s.env.ProxyBindingForAccount(stateAccount)
	if !managed {
		return false, nil
	}
	if _, _, ok := s.env.ExecutableAuth(ctx, authID); !ok {
		errResolve := fmt.Errorf("auth is not executable")
		_ = s.recordAttemptError(authID, ClassProxyError, errResolve)
		return false, errResolve
	}
	status, errRotate := s.env.RotateProxy(ctx, authID, string(binding.SourceKind))
	if errRotate != nil {
		_ = s.recordAttemptError(authID, ClassProxyError, errRotate)
		if status == "rotation_unavailable" {
			return false, nil
		}
		return false, errRotate
	}
	return status == "rotated" || status == "succeeded", nil
}

// recordShadowVerdict stores what the quality gate would have done without
// withholding content or penalizing the account.
func (s *Service) recordShadowVerdict(authID string, verdict QualityVerdict) {
	if s == nil || s.env == nil || s.env.Store() == nil {
		return
	}
	class := verdict.Class
	if class == ClassNone {
		class = "none"
	}
	event := Event{
		Time:       time.Now().UTC(),
		AuthID:     authID,
		Action:     "quality_shadow",
		Result:     "observed",
		Detail:     "shadow quality verdict delivered without action: " + class,
		DetailKey:  "reason.detail.shadow_quality_verdict",
		DetailArgs: map[string]any{"class": class},
	}
	s.runSettlement(func(context.Context) {
		_ = s.env.Store().AppendEvent(event)
	})
}

// recordDiagnosticVerdict writes an opt-in diagnostic event for one attempt.
// Events already carry the class and reason, so this only runs when the
// operator asks for it: it adds the signals that explain why the class was
// chosen, without duplicating the verdict fields the account page shows.
func (s *Service) recordDiagnosticVerdict(authID, source string, cfg QualityConfig, verdict QualityVerdict, signals QualitySignals, extra map[string]any) {
	if !cfg.DebugLogging || s == nil || s.env == nil || s.env.Store() == nil {
		return
	}
	args := map[string]any{
		"class":            verdict.Class,
		"source":           source,
		"visible_tokens":   signals.VisibleTokens,
		"output_tokens":    signals.OutputTokens,
		"reasoning_tokens": signals.ReasoningTokens,
		"encrypted_bytes":  signals.EncryptedBytes,
		"encrypted_floor":  signals.EncryptedFloor,
		"generation_ms":    signals.GenerationMS,
		"first_token_ms":   signals.FirstTokenMS,
		"tps":              signals.TPS,
		"hold_expired":     signals.HoldExpired,
	}
	for key, value := range extra {
		args[key] = value
	}
	event := Event{
		Time:       time.Now().UTC(),
		AuthID:     authID,
		Action:     "quality_debug",
		Result:     "observed",
		Detail:     "diagnostic verdict: " + firstNonEmpty(verdict.Class, "none"),
		DetailKey:  "event.detail.diagnostic_verdict",
		DetailArgs: args,
	}
	s.runSettlement(func(context.Context) {
		_ = s.env.Store().AppendEvent(event)
	})
}

// transparentSettleTimeout bounds background verdict bookkeeping. Applying a
// verdict may save the auth file through the host, so it needs more room than
// a single state write.
const transparentSettleTimeout = 2 * time.Minute

// runSettlement moves bookkeeping off the request path. Every verdict persists
// the full state file and takes the account lock, which a running probe holds
// for its whole duration. Doing that inline kept the client stream open after
// the answer and delayed every failover by seconds while the client saw
// nothing. While the plugin is quiescing the work runs inline, so shutdown
// still flushes it.
func (s *Service) runSettlement(settle func(context.Context)) {
	if s == nil || s.env == nil || settle == nil {
		return
	}
	run := func() {
		ctx, cancel := context.WithTimeout(context.Background(), transparentSettleTimeout)
		defer cancel()
		settle(ctx)
	}
	if !s.env.TaskStarted() {
		run()
		return
	}
	go func() {
		defer s.env.TaskDone()
		run()
	}()
}

// settleTransparentVerdict records the verdict of an attempt whose response
// was already delivered.
func (s *Service) settleTransparentVerdict(authID string, verdict QualityVerdict, callbackID string) {
	s.runSettlement(func(ctx context.Context) {
		_ = s.env.ApplyVerdict(ctx, authID, verdict, "transparent", callbackID)
	})
}

// settleTransparentFailure records a failed attempt and, unless the attempt is
// being retried on a rotated proxy, applies its verdict to the account.
func (s *Service) settleTransparentFailure(authID string, verdict QualityVerdict, class string, errValue error, callbackID string, apply bool) {
	s.runSettlement(func(ctx context.Context) {
		_ = s.recordAttemptError(authID, class, errValue)
		if apply {
			s.applyTransparentFailureVerdict(ctx, authID, verdict, class, errValue, callbackID)
		}
	})
}

// transparentHoldLimit bounds one visible hold by the request budget shared
// across accounts. attempt_timeout is an inactivity timer enforced by the
// stream reader, so it does not shorten the hold. A hold that cannot last
// minVerifyWindow cannot produce a quality verdict, so the caller must stop
// instead of classifying the stream.
func transparentHoldLimit(cfg QualityConfig, requestDeadline time.Time) (time.Duration, bool) {
	limit := cfg.HoldTimeout.Duration
	if limit <= 0 {
		limit = 30 * time.Second
	}
	if !requestDeadline.IsZero() {
		if remaining := time.Until(requestDeadline); remaining < limit {
			limit = remaining
		}
	}
	if limit < minVerifyWindow(cfg) {
		return 0, false
	}
	return limit, true
}

// minVerifyWindow is the shortest hold that can still tell a healthy stream
// from a degraded one: half the configured hold, capped at maxVerifyWindow.
func minVerifyWindow(cfg QualityConfig) time.Duration {
	window := cfg.HoldTimeout.Duration / 2
	if window <= 0 || window > maxVerifyWindow {
		return maxVerifyWindow
	}
	return window
}
