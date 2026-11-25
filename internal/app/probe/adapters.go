package probe

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/auth"
	execapp "grok-quality-guard/internal/app/executor"
	qualityapp "grok-quality-guard/internal/app/quality"
	appstate "grok-quality-guard/internal/app/state"
	domain "grok-quality-guard/internal/quality"
)

type AttemptEvidence = appstate.AttemptEvidence
type Metrics = appstate.Metrics

const (
	DisableKindCooldown          = appstate.DisableKindCooldown
	DisableKindQuota             = appstate.DisableKindQuota
	DisableKindCredentialInvalid = appstate.DisableKindCredentialInvalid
	DisableKindPermanent         = appstate.DisableKindPermanent
)

const probeHeader = "X-Grok-Quality-Probe"

func (s *Service) stateStore() *Store { return s.env.Store() }

func (s *Service) accountLock(authID string) *sync.Mutex { return s.env.AccountLock(authID) }
func (s *Service) resolveAuth(ctx context.Context, authID string) (Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error) {
	return s.env.ResolveAuth(ctx, authID)
}
func (s *Service) refreshCatalog(ctx context.Context) error        { return s.env.RefreshCatalog(ctx) }
func (s *Service) stateAccount(authID string) (AccountState, bool) { return s.env.StateAccount(authID) }
func (s *Service) refreshSelectionAccount(authID string)           { s.env.RefreshSelectionAccount(authID) }
func (s *Service) executeModelStream(ctx context.Context, req pluginapi.HostModelExecutionRequest, callbackID string) (pluginapi.HostModelStreamResponse, error) {
	return s.env.ExecuteModelStream(ctx, req, callbackID)
}
func (s *Service) closeModelStream(streamID string) error { return s.env.CloseModelStream(streamID) }

func (s *Service) readModelStreamErrorBody(ctx context.Context, streamID string) []byte {
	return s.env.ReadModelStreamErrorBody(ctx, streamID)
}
func (s *Service) recordInternalError(authID string, err error) error {
	return s.env.RecordInternalError(authID, err)
}
func (s *Service) setStateExternal(authID, class string) error {
	return s.env.Quality().SetStateExternal(authID, class)
}
func (s *Service) applyVerdictLocked(ctx context.Context, authID string, verdict QualityVerdict, source, callbackID string, raw map[string]json.RawMessage, response pluginapi.HostAuthGetResponse, reEnabled bool) (qualityapp.VerdictResult, error) {
	return s.env.Quality().ApplyVerdictLocked(ctx, authID, verdict, source, callbackID, raw, response, reEnabled)
}
func authDisabled(raw map[string]json.RawMessage) bool { return auth.Disabled(raw) }
func authRawString(raw map[string]json.RawMessage, key string) string {
	return auth.RawString(raw, key)
}
func isGrokAuth(raw map[string]json.RawMessage) bool {
	for _, key := range []string{"type", "provider", "oauth_provider", "base_url"} {
		if auth.IsGrokProvider(auth.RawString(raw, key)) {
			return true
		}
	}
	return false
}
func classifyTransportFailure(err error) (string, string) {
	return execapp.ClassifyTransportFailure(err)
}

const hostAuthNotReadyReason = "reason.detail.host_auth_not_ready"

// probeAuthReadyWaits spaces the retries of a probe whose freshly re-enabled
// auth the host has not loaded yet. The host reloads an auth file through a
// file watcher, asynchronously; a busy host took long enough that a probe
// sent right after the write was refused hundreds of times an hour.
var probeAuthReadyWaits = []time.Duration{
	250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second,
}

// hostAuthNotReady reports the host refusing a pinned request because its
// scheduler cannot see the auth, which says nothing about the account.
func hostAuthNotReady(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "auth_not_found") || strings.Contains(text, "no auth available")
}

// sleepContext waits for d and reports false when ctx ended first.
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// A fixed-AuthID probe must make progress even when the host wraps an
// operational failure in text that the shared classifier treats as a
// non-quality error. Host model cooldowns remain ignored because they describe
// a host-wide scheduling state rather than this auth's probe result, and so do
// host reports that the auth is not loaded yet.
func classifyProbeTransportFailure(err error) (string, string) {
	if hostAuthNotReady(err) {
		return ClassIgnored, hostAuthNotReadyReason
	}
	class, reason := classifyTransportFailure(err)
	if reason == "reason.detail.model_cooldown" || class == ClassProxyError || class == ClassQuotaExhausted {
		return class, reason
	}
	// Only explicit token/key rejection phrases are strong enough to disable
	// an auth. Bare "no auth", "unauthorized", "expired", or "permission
	// denied" can describe the host scheduler, WAF, model policy, or quota
	// state rather than this auth's credentials.
	if isProbeCredentialRejectionText(strings.ToLower(err.Error())) {
		return ClassCredentialInvalid, "reason.detail.credential_rejected"
	}
	if class == ClassIgnored && reason == "reason.detail.probe_transport_error" {
		return ClassProbeError, reason
	}
	return class, reason
}

func isProbeCredentialRejectionText(text string) bool {
	for _, marker := range []string{
		"invalid token",
		"token expired",
		"invalid api key",
		"incorrect api key",
		"invalid_grant",
		"credential rejected",
		"authentication failed",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func qualityEvidenceFromVerdict(verdict QualityVerdict, source string, at time.Time) *appstate.QualityEvidence {
	return appstate.QualityEvidenceFromVerdict(verdict, source, at)
}
func noteHealthyObservation(account *AccountState, now time.Time, cfg QualityConfig) *Event {
	return qualityapp.NoteHealthyObservation(account, now, cfg)
}
func isCooldownState(value string) bool { return value == StateCooling || value == StateQuotaExhausted }

func classifyUpstreamStatus(status int, body []byte) (string, string) {
	return execapp.ClassifyUpstreamStatus(status, body)
}
func NewStreamAnalyzerWithProtocolAt(protocol string, startedAt time.Time) *domain.StreamAnalyzer {
	return domain.NewStreamAnalyzerWithProtocolAt(protocol, startedAt)
}
func reasoningRequiredForModel(model string, body []byte) bool {
	return domain.ReasoningRequiredForModel(model, body)
}
func reasoningRequiredForModelWithNonReasoning(model string, body []byte, nonReasoning []string) bool {
	return domain.ReasoningRequiredForModelWithNonReasoning(model, body, nonReasoning)
}
func classifyProbeResult(sig Signals, cfg QualityConfig, expectedMatched, reasoningRequired bool) QualityVerdict {
	return domain.ClassifyProbeResult(sig, cfg, expectedMatched, reasoningRequired)
}
