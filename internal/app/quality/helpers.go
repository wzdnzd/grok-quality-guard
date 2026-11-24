package quality

import (
	"encoding/json"
	"time"

	"grok-quality-guard/internal/app/auth"
	appstate "grok-quality-guard/internal/app/state"
	domain "grok-quality-guard/internal/quality"
)

func authDisabled(raw map[string]json.RawMessage) bool { return auth.Disabled(raw) }
func qualityEvidenceFromVerdict(verdict domain.QualityVerdict, source string, at time.Time) *appstate.QualityEvidence {
	return appstate.QualityEvidenceFromVerdict(verdict, source, at)
}
func newEvent(authID, class, action, result, detail string) *Event {
	return &Event{Time: time.Now().UTC(), AuthID: authID, Class: class, Action: action, Result: result, Detail: detail}
}

const QuotaExhaustedCooldown = quotaExhaustedCooldown

func cooldownForClass(class string, cfg QualityConfig) time.Duration {
	switch class {
	case ClassEmptyStream:
		return cfg.EmptyStreamCooldown.Duration
	case ClassQuotaExhausted:
		return quotaExhaustedCooldown
	default:
		return cfg.MissingThinkingCooldown.Duration
	}
}
func isCooldownState(value string) bool {
	return value == StateCooling || value == StateQuotaExhausted
}
func isIsolatedState(value string) bool {
	switch value {
	case StateCooling, StateQuotaExhausted, StateCredentialInvalid, StateDisabledByGuard, StateExternallyDisabled:
		return true
	default:
		return false
	}
}
func stateForDisableKind(kind string) string {
	switch kind {
	case DisableKindCooldown:
		return StateCooling
	case DisableKindQuota:
		return StateQuotaExhausted
	case DisableKindCredentialInvalid:
		return StateCredentialInvalid
	case DisableKindPermanent:
		return StateDisabledByGuard
	default:
		return StateCooling
	}
}
func isAccountStatusClass(class string) bool {
	return class == ClassCredentialInvalid || class == ClassQuotaExhausted
}

const (
	transportBackoffBase = 30 * time.Second
	transportBackoffMax  = 10 * time.Minute
)

// transportBackoff keeps network failures out of the quality pipeline. The
// window doubles while the account stays unreachable and resets once it
// recovers, so a restored link returns to the pool within minutes.
func transportBackoff(previous AccountState) time.Duration {
	backoff := transportBackoffBase
	if previous.State == StateUnreachable && !previous.LastObservedAt.IsZero() {
		if window := previous.ProbeBackoffUntil.Sub(previous.LastObservedAt); window > 0 {
			backoff = window * 2
		}
	}
	if backoff > transportBackoffMax {
		backoff = transportBackoffMax
	}
	return backoff
}

func nextProbeBackoff(cfg ProbeConfig, previous AccountState) time.Duration {
	base := cfg.SuspectBackoff.Duration
	if base <= 0 {
		base = 15 * time.Minute
	}
	maxBackoff := cfg.MaxBackoff.Duration
	if maxBackoff <= 0 {
		maxBackoff = time.Hour
	}
	if maxBackoff < base {
		maxBackoff = base
	}
	backoff := base
	if !previous.LastProbeAt.IsZero() && !previous.ProbeBackoffUntil.IsZero() {
		previousWindow := previous.ProbeBackoffUntil.Sub(previous.LastProbeAt)
		if previousWindow > 0 {
			backoff = previousWindow * 2
		}
	}
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	return backoff
}

func isConfirmedClass(class string) bool {
	switch class {
	case ClassMissingThinking, ClassBufferedBurst, ClassCipherDrool, ClassFakeEncryptedDump, ClassFastReasoningRatio, ClassHardTPS, ClassEmptyStream, ClassExpectedMarkerMissing:
		return true
	default:
		return false
	}
}
