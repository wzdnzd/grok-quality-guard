package quality

import (
	"time"

	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/quality"
)

func observeRoutingVerdict(account *AccountState, source string, verdict QualityVerdict, now time.Time, halfLifeHours int) {
	if account == nil || verdict.Class == ClassIgnored {
		return
	}
	sig := verdict.Signals
	reasoning := sig.ReasoningRequired || sig.ReasoningTokens > 0 || sig.HasReasoningDelta || sig.HasThinking
	appstate.ObserveRoutingSample(
		&account.RoutingStats,
		source,
		reasoning,
		verdict.Class == quality.ClassNone,
		sig.FirstTokenMS,
		sig.TPS,
		now,
		appstate.RoutingSampleHalfLife(source, halfLifeHours),
	)
}
