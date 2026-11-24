package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type verdictResult struct {
	Actioned         bool
	CrossVerify      bool
	ThresholdHit     bool
	ProxyRotate      bool
	ProxyRotateAsync bool
}

const defaultStrikeDecayMinHealthy = 10

// nextConsecutiveStrike advances an action-trigger counter without allowing it
// to grow past the configured threshold. Recovery probes keep running after a
// threshold action, so an unbounded counter eventually rendered nonsense such
// as consecutive probe errors 53/2.
func nextConsecutiveStrike(current, threshold int) int {
	if threshold <= 0 {
		return current + 1
	}
	if current >= threshold {
		return threshold
	}
	return current + 1
}

// isQualityFailureClass lists classes that are genuine quality signals. Probe
// and transport errors do not reset the healthy streak used for strike decay.
func isQualityFailureClass(class string) bool {
	switch class {
	case ClassNone, ClassIgnored, ClassProbeError, ClassProxyError, ClassRateLimited,
		ClassCredentialInvalid, ClassQuotaExhausted:
		return false
	default:
		return true
	}
}

func noteQualityFailure(account *AccountState, now time.Time) {
	if account == nil {
		return
	}
	account.QualityFailureAt = now
	account.HealthyStreak = 0
	account.HealthyStreakStartedAt = time.Time{}
}

// noteHealthyObservation advances the healthy streak and returns a decay event
// when a missing-thinking strike has survived both the configured quiet period
// and the minimum number of healthy samples.
func noteHealthyObservation(account *AccountState, now time.Time, cfg QualityConfig) *Event {
	if account == nil {
		return nil
	}
	if account.HealthyStreakStartedAt.IsZero() {
		account.HealthyStreakStartedAt = now
	}
	account.HealthyStreak++
	if !account.QualityStrikeMissingThinking || cfg.StrikeDecayAfterHours <= 0 {
		return nil
	}
	minHealthy := cfg.StrikeDecayMinHealthy
	if minHealthy <= 0 {
		minHealthy = defaultStrikeDecayMinHealthy
	}
	if account.HealthyStreak < minHealthy {
		return nil
	}
	elapsed := now.Sub(account.HealthyStreakStartedAt)
	if elapsed < time.Duration(cfg.StrikeDecayAfterHours)*time.Hour {
		return nil
	}
	streak := account.HealthyStreak
	account.QualityStrikeMissingThinking = false
	account.HealthyStreak = 0
	account.HealthyStreakStartedAt = time.Time{}
	return &Event{
		Time:       now,
		AuthID:     account.AuthID,
		Action:     "strike_decay",
		Result:     "expired",
		Detail:     fmt.Sprintf("healthy_streak=%d elapsed=%s", streak, elapsed.Round(time.Hour)),
		DetailKey:  "event.detail.healthy_streak",
		DetailArgs: map[string]any{"streak": streak, "elapsed": elapsed.Round(time.Hour).String()},
	}
}

// ApplyVerdict records one passive, transparent, or externally supplied
// observation. It serializes auth mutation with probes and Resin operations.
func (s *Service) ApplyVerdict(ctx context.Context, authID string, verdict QualityVerdict, source, callbackID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(authID) == "" {
		return nil
	}
	lock := s.env.AccountLock(authID)
	lock.Lock()
	outcome, errApply := func() (verdictResult, error) {
		defer lock.Unlock()
		return s.applyVerdictLocked(ctx, authID, verdict, source, callbackID, nil, pluginapi.HostAuthGetResponse{}, false)
	}()
	if errApply != nil {
		return errApply
	}
	if outcome.ProxyRotate {
		return s.finishProxyErrorRotation(ctx, authID, verdict)
	}
	if outcome.ProxyRotateAsync {
		s.queueProxyErrorRotation(authID, verdict)
	}
	return nil
}

// applyVerdictLocked is shared by passive observations and active probes.
// Callers must hold the per-auth lock.
func (s *Service) applyVerdictLocked(
	ctx context.Context,
	authID string,
	verdict QualityVerdict,
	source, _ string,
	raw map[string]json.RawMessage,
	response pluginapi.HostAuthGetResponse,
	reEnabled bool,
) (verdictResult, error) {
	if strings.TrimSpace(authID) == "" {
		return verdictResult{}, nil
	}
	store := s.env.Store()
	if store == nil {
		return verdictResult{}, errors.New("state store is not initialized")
	}
	defer s.env.RefreshSelectionAccount(authID)
	policy := s.env.Policy()

	sig := verdict.Signals
	now := time.Now().UTC()
	previousState, _ := s.env.StateAccount(authID)
	priorMissingStrike := previousState.QualityStrikeMissingThinking
	backoff := nextProbeBackoff(policy.Probe, previousState)
	outcome := verdictResult{}

	errUpdate := store.RecordObservation(authID, func(account *AccountState) (*Event, error) {
		if priorMissingStrike {
			account.QualityStrikeMissingThinking = true
		}
		account.LastObservedAt = now
		noteEvidenceObservation(account, sig)
		account.LastAttempt = &AttemptEvidence{
			At: now, Source: source, Class: verdict.Class, Reason: verdict.Reason,
		}
		if isQualityFailureClass(verdict.Class) {
			noteQualityFailure(account, now)
		}
		if verdict.Class != ClassIgnored && !isProbeErrorClass(verdict.Class) && !isAccountStatusClass(verdict.Class) {
			account.Metrics = Metrics{
				FirstTokenMS:    sig.FirstTokenMS,
				DurationMS:      sig.DurationMS,
				GenerationMS:    sig.GenerationMS,
				OutputTokens:    sig.OutputTokens,
				ReasoningTokens: sig.ReasoningTokens,
				VisibleTokens:   sig.VisibleTokens,
				EncryptedBytes:  sig.EncryptedBytes,
				TPS:             sig.TPS,
			}
			account.LastReason = verdict.Class
			account.LastEvidence = qualityEvidenceFromVerdict(verdict, source, now)
		}
		observeRoutingVerdict(account, source, verdict, now, policy.Routing.Scoring.HalfLifeHours)
		if source == "probe" {
			account.LastProbeAt = now
			if isProbeErrorClass(verdict.Class) || verdict.Class == ClassIgnored {
				account.ProbeBackoffUntil = now.Add(backoff)
			}
		}

		preserveIsolation := func() bool {
			return isIsolatedState(account.State)
		}
		eventAction := "observe"
		if source == "probe" {
			eventAction = "probe"
		}
		eventResult := "recorded"
		eventDetail := source
		eventDetailKey := ""
		eventDetailArgs := map[string]any{}
		if verdict.Reason != "" {
			eventDetail += " · " + verdict.Reason
		}
		eventClass := verdict.Class

		switch verdict.Class {
		case ClassNone:
			account.SoftStrikes = 0
			account.ErrorStrikes = 0
			account.ThinkingStrikes = 0
			if !preserveIsolation() {
				account.State = StateHealthy
				if decay := noteHealthyObservation(account, now, policy.Quality); decay != nil {
					return decay, nil
				}
			}
			return nil, nil

		case ClassSoftTPS, ClassInsufficientOutput, ClassInsufficientGeneration:
			account.ThinkingStrikes = 0
			threshold := positiveInt(policy.Quality.ConsecutiveSoft, 2)
			account.SoftStrikes = nextConsecutiveStrike(account.SoftStrikes, threshold)
			if account.SoftStrikes >= threshold {
				outcome.ThresholdHit = true
				if policy.Quality.CrossVerifySoft() && source != "probe" {
					outcome.CrossVerify = true
					eventAction = "cross_verify_schedule"
					eventResult = "pending"
					eventDetailKey = "event.detail.soft_tps_pending"
					eventDetailArgs = map[string]any{"source": source, "strikes": account.SoftStrikes, "threshold": threshold}
					if !preserveIsolation() {
						account.State = StateSuspect
					}
				} else {
					outcome.Actioned = true
					eventResult = "threshold_reached"
					eventDetailKey = "event.detail.soft_tps"
					eventDetailArgs = map[string]any{"source": source, "strikes": account.SoftStrikes, "threshold": threshold}
					if !preserveIsolation() {
						account.State = StateDegraded
					}
				}
			} else {
				eventResult = "pending"
				eventDetailKey = "event.detail.soft_tps"
				eventDetailArgs = map[string]any{"source": source, "strikes": account.SoftStrikes, "threshold": threshold}
				if !preserveIsolation() {
					account.State = StateSuspect
				}
			}

		case ClassRateLimited:
			account.ProbeBackoffUntil = now.Add(backoff)
			eventResult = "rate_limited"
			eventDetailKey = "event.detail.rate_limited"
			eventDetailArgs = map[string]any{"source": source}
			if verdict.Reason != "" {
				eventDetail += " · " + verdict.Reason
			}
			if !preserveIsolation() {
				account.State = StateSuspect
			}

		case ClassProbeError, ClassProxyError:
			account.ThinkingStrikes = 0
			threshold := positiveInt(policy.Quality.ConsecutiveErrors, 2)
			account.ErrorStrikes = nextConsecutiveStrike(account.ErrorStrikes, threshold)
			proxyRotationReady := false
			if s.env.AutoRotateAfterQualityFailure() && !preserveIsolation() {
				if _, managed := s.env.ProxyBindingForAccount(previousState); managed {
					proxyRotationReady = true
				}
			}
			// Transport failures never become quality evidence. They keep the
			// account out of the pool for a short window and let a link recovery
			// return it without a manual restore.
			if !preserveIsolation() {
				account.State = StateUnreachable
				account.ProbeBackoffUntil = now.Add(transportBackoff(previousState))
			}
			if account.ErrorStrikes >= threshold {
				outcome.ThresholdHit = true
				if proxyRotationReady {
					if source == "probe" {
						outcome.ProxyRotate = true
					} else {
						outcome.ProxyRotateAsync = true
					}
				}
				eventResult = "threshold_reached"
				eventDetailKey = "event.detail.probe_errors"
				eventDetailArgs = map[string]any{"source": source, "strikes": account.ErrorStrikes, "threshold": threshold}
				if verdict.Reason != "" {
					eventDetail += " · " + verdict.Reason
				}
			} else {
				eventResult = "pending"
				eventDetailKey = "event.detail.probe_errors"
				eventDetailArgs = map[string]any{"source": source, "strikes": account.ErrorStrikes, "threshold": threshold}
				if verdict.Reason != "" {
					eventDetail += " · " + verdict.Reason
				}
			}

		case ClassCredentialInvalid, ClassQuotaExhausted:
			account.SoftStrikes = 0
			account.ErrorStrikes = 0
			account.ThinkingStrikes = 0
			outcome.Actioned = true
			eventResult = "actioned"
			eventDetailKey = "event.detail.credential_invalid"
			if verdict.Class == ClassQuotaExhausted {
				eventDetailKey = "event.detail.quota_exhausted"
			}
			eventDetailArgs = map[string]any{"source": source}

		case ClassIgnored:
			eventClass = ""
			eventResult = "ignored"
			eventDetailKey = "event.detail.non_quality_error"
			eventDetailArgs = map[string]any{"source": source}
			if verdict.Reason != "" {
				eventDetail += " · " + verdict.Reason
			}

			// An ignored active probe is still evidence that the account has
			// not passed a quality check. Move it out of pending/unknown so the
			// scheduler can continue with suspect backoff instead of leaving it
			// in the initial-check state forever.
			if source == "probe" && account.State == StateUnknown {
				account.State = StateSuspect
			}

		case ClassMissingThinking:
			threshold := positiveInt(policy.Quality.ConsecutiveMissingThinking, 1)
			if source == "probe" {
				// A probe is the deliberate confirmation step: it asks for reasoning
				// on a protocol that exposes it, so one miss is authoritative.
				threshold = 1
			}
			account.ThinkingStrikes = nextConsecutiveStrike(account.ThinkingStrikes, threshold)
			account.SoftStrikes = 0
			if account.ThinkingStrikes >= threshold {
				outcome.ThresholdHit = true
				if policy.Quality.CrossVerifyThinking() && source != "probe" {
					outcome.CrossVerify = true
					eventAction = "cross_verify_schedule"
					eventResult = "pending"
					eventDetailKey = "event.detail.missing_thinking_pending"
					eventDetailArgs = map[string]any{"source": source, "strikes": account.ThinkingStrikes, "threshold": threshold}
					if !preserveIsolation() {
						account.State = StateSuspect
					}
				} else {
					outcome.Actioned = true
					eventResult = "threshold_reached"
					eventDetailKey = "event.detail.missing_thinking"
					eventDetailArgs = map[string]any{"source": source, "strikes": account.ThinkingStrikes, "threshold": threshold, "eligible": priorMissingStrike}
					if !preserveIsolation() {
						account.State = StateDegraded
					}
				}
			} else {
				eventResult = "pending"
				eventDetailKey = "event.detail.missing_thinking_strikes"
				eventDetailArgs = map[string]any{"source": source, "strikes": account.ThinkingStrikes, "threshold": threshold}
				if !preserveIsolation() {
					account.State = StateSuspect
				}
			}

		default:
			if isConfirmedClass(verdict.Class) {
				account.ThinkingStrikes = 0
				outcome.ThresholdHit = true
				outcome.Actioned = true
				eventResult = "threshold_reached"
				if !preserveIsolation() {
					account.State = StateDegraded
				}
			} else if !preserveIsolation() {
				account.State = StateSuspect
			}
			eventDetail = source
			if verdict.Reason != "" {
				eventDetail += " · " + verdict.Reason
			}
		}

		return &Event{
			Time:       time.Now().UTC(),
			AuthID:     authID,
			Class:      eventClass,
			Action:     eventAction,
			Result:     eventResult,
			Detail:     eventDetail,
			DetailKey:  eventDetailKey,
			DetailArgs: eventDetailArgs,
		}, nil
	})
	if errUpdate != nil {
		return outcome, errUpdate
	}
	if outcome.Actioned || outcome.CrossVerify || isAccountStatusClass(verdict.Class) {
		if errFlush := store.Flush(); errFlush != nil {
			return outcome, errFlush
		}
	}

	if isAccountStatusClass(verdict.Class) {
		if raw == nil {
			_, resolvedResponse, resolvedRaw, errResolve := s.env.ResolveAuth(ctx, authID)
			if errResolve != nil {
				return outcome, errResolve
			}
			response = resolvedResponse
			raw = resolvedRaw
		}
		_, errAction := s.applyAutoCooldownWithAuthLocked(ctx, authID, verdict, source, raw, response, false, true)
		return outcome, errAction
	}

	if outcome.CrossVerify {
		s.scheduleVerdictCrossVerify(authID, verdict.Class, source)
		return outcome, nil
	}
	if outcome.ProxyRotate || outcome.ProxyRotateAsync {
		return outcome, nil
	}
	if !outcome.Actioned {
		if source == "probe" && reEnabled {
			if errHold := s.holdProbeRecoveryLocked(ctx, authID, raw, response, verdict.Class, backoff); errHold != nil {
				return outcome, errHold
			}
		}
		return outcome, nil
	}
	if policy.AutoAction == "observe" {
		if source == "probe" && reEnabled {
			if errHold := s.holdProbeRecoveryLocked(ctx, authID, raw, response, verdict.Class, backoff); errHold != nil {
				return outcome, errHold
			}
		}
		return outcome, nil
	}
	permanentEligible := priorMissingStrike
	if raw == nil {
		_, resolvedResponse, resolvedRaw, errResolve := s.env.ResolveAuth(ctx, authID)
		if errResolve != nil {
			return outcome, errResolve
		}
		raw = resolvedRaw
		response = resolvedResponse
	}
	if _, errAction := s.applyAutoCooldownWithAuthLocked(
		ctx,
		authID,
		verdict,
		source,
		raw,
		response,
		permanentEligible,
		true,
	); errAction != nil {
		return outcome, errAction
	}
	return outcome, nil
}

// noteEvidenceObservation keeps a bounded ratio of samples that carried real
// reasoning evidence. A single inferred miss is then judged against the
// account's own history instead of assuming the worst.
func noteEvidenceObservation(account *AccountState, sig Signals) {
	if account == nil {
		return
	}
	observed := sig.Terminal || sig.VisibleTokens > 0 || sig.OutputTokens > 0 || sig.ReasoningTokens > 0 || sig.HasThinking
	if !observed {
		return
	}
	const evidenceWindow = 32
	if account.EvidenceSamples >= evidenceWindow {
		account.EvidenceSamples /= 2
		account.EvidenceHits /= 2
	}
	account.EvidenceSamples++
	if sig.HasThinking {
		account.EvidenceHits++
	}
}
