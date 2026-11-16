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
	Actioned     bool
	CrossVerify  bool
	ThresholdHit bool
}

const defaultStrikeDecayMinHealthy = 10

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
		Time:   now,
		AuthID: account.AuthID,
		Action: "strike_decay",
		Result: "expired",
		Detail: fmt.Sprintf("healthy_streak=%d elapsed=%s", streak, elapsed.Round(time.Hour)),
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
	defer lock.Unlock()
	_, errApply := s.applyVerdictLocked(ctx, authID, verdict, source, callbackID, nil, pluginapi.HostAuthGetResponse{}, false)
	return errApply
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
			account.SoftStrikes++
			threshold := positiveInt(policy.Quality.ConsecutiveSoft, 2)
			if account.SoftStrikes >= threshold {
				outcome.ThresholdHit = true
				if policy.Quality.SoftCrossVerify && source != "probe" {
					outcome.CrossVerify = true
					eventAction = "cross_verify_schedule"
					eventResult = "pending"
					eventDetail = fmt.Sprintf("%s · 连续软 TPS %d/%d，等待交叉验证", source, account.SoftStrikes, threshold)
					if !preserveIsolation() {
						account.State = StateSuspect
					}
				} else {
					outcome.Actioned = true
					eventResult = "threshold_reached"
					eventDetail = fmt.Sprintf("%s · 连续软 TPS %d/%d", source, account.SoftStrikes, threshold)
					if !preserveIsolation() {
						account.State = StateDegraded
					}
				}
			} else {
				eventResult = "pending"
				eventDetail = fmt.Sprintf("%s · 连续软 TPS %d/%d", source, account.SoftStrikes, threshold)
				if !preserveIsolation() {
					account.State = StateSuspect
				}
			}

		case ClassRateLimited:
			account.ProbeBackoffUntil = now.Add(backoff)
			eventResult = "rate_limited"
			eventDetail = source + " · upstream rate limit, not a quality failure"
			if verdict.Reason != "" {
				eventDetail += " · " + verdict.Reason
			}
			if !preserveIsolation() {
				account.State = StateSuspect
			}

		case ClassProbeError, ClassProxyError:
			account.ThinkingStrikes = 0
			account.ErrorStrikes++
			threshold := positiveInt(policy.Quality.ConsecutiveErrors, 2)
			if account.ErrorStrikes >= threshold {
				outcome.ThresholdHit = true
				outcome.Actioned = true
				eventResult = "threshold_reached"
				eventDetail = fmt.Sprintf("%s · 连续探测错误 %d/%d", source, account.ErrorStrikes, threshold)
				if verdict.Reason != "" {
					eventDetail += " · " + verdict.Reason
				}
				if !preserveIsolation() {
					account.State = StateDegraded
				}
			} else {
				eventResult = "pending"
				eventDetail = fmt.Sprintf("%s · 连续探测错误 %d/%d", source, account.ErrorStrikes, threshold)
				if verdict.Reason != "" {
					eventDetail += " · " + verdict.Reason
				}
				if !preserveIsolation() {
					account.State = StateSuspect
				}
			}

		case ClassCredentialInvalid, ClassQuotaExhausted:
			account.SoftStrikes = 0
			account.ErrorStrikes = 0
			account.ThinkingStrikes = 0
			outcome.Actioned = true
			eventResult = "actioned"

		case ClassIgnored:
			eventClass = ""
			eventResult = "ignored"
			eventDetail = source + " · 非质量类错误，未计入连续错误"
			if verdict.Reason != "" {
				eventDetail += " · " + verdict.Reason
			}

		case ClassMissingThinking:
			account.ThinkingStrikes++
			account.SoftStrikes = 0
			threshold := positiveInt(policy.Quality.ConsecutiveMissingThinking, 1)
			if account.ThinkingStrikes >= threshold {
				outcome.ThresholdHit = true
				if policy.Quality.ThinkingCrossVerify && source != "probe" {
					outcome.CrossVerify = true
					eventAction = "cross_verify_schedule"
					eventResult = "pending"
					eventDetail = fmt.Sprintf("%s · 连续缺少真实思考 %d/%d，等待交叉验证", source, account.ThinkingStrikes, threshold)
					if !preserveIsolation() {
						account.State = StateSuspect
					}
				} else {
					outcome.Actioned = true
					eventResult = "threshold_reached"
					eventDetail = fmt.Sprintf("%s · 连续缺少真实思考 %d/%d · permanent_eligible=%t", source, account.ThinkingStrikes, threshold, priorMissingStrike)
					if !preserveIsolation() {
						account.State = StateDegraded
					}
				}
			} else {
				eventResult = "pending"
				eventDetail = fmt.Sprintf("%s · 连续缺少真实思考 %d/%d", source, account.ThinkingStrikes, threshold)
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
			Time:   time.Now().UTC(),
			AuthID: authID,
			Class:  eventClass,
			Action: eventAction,
			Result: eventResult,
			Detail: eventDetail,
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
