package management

import (
	"strings"
	"time"

	appstate "grok-quality-guard/internal/app/state"
	domain "grok-quality-guard/internal/quality"
)

type verdictView struct {
	Category string        `json:"category,omitempty"`
	Code     string        `json:"code,omitempty"`
	Decision string        `json:"decision,omitempty"`
	Source   string        `json:"source,omitempty"`
	At       time.Time     `json:"at,omitzero"`
	Detail   string        `json:"detail,omitempty"`
	Evidence *evidenceView `json:"evidence,omitempty"`
}

type eventView struct {
	appstate.Event
	Category string `json:"category,omitempty"`
	Code     string `json:"code,omitempty"`
	Decision string `json:"decision,omitempty"`
}

type evidenceView struct {
	Protocol          string  `json:"protocol,omitempty"`
	Streaming         bool    `json:"streaming,omitempty"`
	HasThinking       bool    `json:"has_thinking,omitempty"`
	EncryptedBytes    int64   `json:"encrypted_bytes,omitempty"`
	EncryptedBytesMax int64   `json:"encrypted_bytes_max,omitempty"`
	EncryptedFloor    int64   `json:"encrypted_floor,omitempty"`
	ReasoningTokens   int64   `json:"reasoning_tokens,omitempty"`
	VisibleTokens     int64   `json:"visible_tokens,omitempty"`
	OutputTokens      int64   `json:"output_tokens,omitempty"`
	FirstTokenMS      int64   `json:"first_token_ms,omitempty"`
	DurationMS        int64   `json:"duration_ms,omitempty"`
	GenerationMS      int64   `json:"generation_ms,omitempty"`
	TPS               float64 `json:"tps,omitempty"`
	HoldExpired       bool    `json:"hold_expired,omitempty"`
	Strikes           int     `json:"strikes,omitempty"`
	StrikeLimit       int     `json:"strike_limit,omitempty"`
}

const (
	verdictCategoryQuality      = "quality"
	verdictCategoryTransport    = "transport"
	verdictCategoryAccount      = "account"
	verdictCategoryInconclusive = "inconclusive"
	verdictCategoryIgnored      = "ignored"

	verdictDecisionDeliver    = "deliver"
	verdictDecisionRetry      = "retry"
	verdictDecisionQuarantine = "quarantine"
	verdictDecisionCooldown   = "cooldown"
	verdictDecisionDisable    = "disable"
	verdictDecisionObserve    = "observe"
)

// verdictCategory groups every class into the four buckets the dashboard
// filters on. Transport and account failures never count as quality strikes.
func verdictCategory(class string) string {
	switch class {
	case "", domain.ClassIgnored:
		return verdictCategoryIgnored
	case domain.ClassInconclusive:
		return verdictCategoryInconclusive
	case domain.ClassProbeError, domain.ClassProxyError:
		return verdictCategoryTransport
	case domain.ClassCredentialInvalid, domain.ClassQuotaExhausted, domain.ClassRateLimited:
		return verdictCategoryAccount
	default:
		return verdictCategoryQuality
	}
}

// verdictDecision is the single action the plugin took for this observation.
func verdictDecision(class string, state string) string {
	switch state {
	case appstate.StateDisabledByGuard, appstate.StateCredentialInvalid:
		return verdictDecisionDisable
	case appstate.StateCooling, appstate.StateQuotaExhausted:
		return verdictDecisionCooldown
	case appstate.StateDegraded:
		return verdictDecisionQuarantine
	}
	switch class {
	case domain.ClassCredentialInvalid:
		return verdictDecisionDisable
	case domain.ClassQuotaExhausted:
		return verdictDecisionCooldown
	case domain.ClassSoftTPS, domain.ClassRateLimited, domain.ClassIgnored, "":
		return verdictDecisionObserve
	case domain.ClassInconclusive:
		return verdictDecisionDeliver
	case domain.ClassProbeError, domain.ClassProxyError:
		return verdictDecisionRetry
	default:
		return verdictDecisionRetry
	}
}

// buildVerdictView turns the persisted evidence and attempt records into one
// decision object. The account page renders this object once instead of
// repeating the same class in the attempt line, the verdict line and the
// evidence section.
func buildVerdictView(account appstate.AccountState) *verdictView {
	evidence := account.LastEvidence
	attempt := account.LastAttempt
	class := ""
	source := ""
	at := time.Time{}
	detail := ""
	if evidence != nil {
		class = evidence.Class
		source = evidence.Source
		at = evidence.At
		detail = evidence.Reason
	}
	if class == "" && attempt != nil {
		class = attempt.Class
		source = attempt.Source
		at = attempt.At
		detail = attempt.Reason
	}
	if class == "" && account.LastReason != "" {
		class = account.LastReason
		at = account.LastObservedAt
	}
	if class == "" && account.State == appstate.StateUnknown {
		return nil
	}
	view := &verdictView{
		Category: verdictCategory(class),
		Code:     class,
		Decision: verdictDecision(class, account.State),
		Source:   source,
		At:       at,
		Detail:   detail,
	}
	if evidence != nil {
		view.Evidence = &evidenceView{
			Protocol:        evidence.Protocol,
			Streaming:       evidence.Streaming,
			HasThinking:     evidence.HasThinking,
			EncryptedBytes:  evidence.EncryptedBytes,
			EncryptedFloor:  evidence.EncryptedFloor,
			ReasoningTokens: evidence.ReasoningTokens,
			VisibleTokens:   evidence.VisibleTokens,
			OutputTokens:    evidence.OutputTokens,
			FirstTokenMS:    evidence.FirstTokenMS,
			DurationMS:      evidence.DurationMS,
			GenerationMS:    evidence.GenerationMS,
			TPS:             evidence.TPS,
			HoldExpired:     evidence.HoldExpired,
			Strikes:         account.ThinkingStrikes,
		}
		if view.Evidence.EncryptedBytes == 0 && account.Metrics.EncryptedBytes > 0 {
			view.Evidence.EncryptedBytes = account.Metrics.EncryptedBytes
		}
		if view.Evidence.ReasoningTokens == 0 && account.Metrics.ReasoningTokens > 0 {
			view.Evidence.ReasoningTokens = account.Metrics.ReasoningTokens
		}
		if view.Detail == "" {
			view.Detail = strings.TrimSpace(account.LastReason)
		}
	}
	return view
}
