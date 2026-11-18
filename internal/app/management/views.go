package management

import (
	"context"
	"strings"
)

func (s *Service) accountSummary(account Account, stateAccount AccountState, proxyURL string) accountSummaryView {
	displayFull := strings.TrimSpace(firstNonEmpty(account.Email, account.Label, account.ID))
	displayLabel := strings.TrimSpace(account.Label)
	if strings.Contains(displayLabel, "@") {
		displayLabel = maskEmail(displayLabel)
	}
	view := accountSummaryView{
		AuthID:            account.ID,
		AuthIDDisplay:     maskAuthID(account.ID),
		Display:           maskAccountIdentity(displayFull),
		DisplayFull:       displayFull,
		Email:             maskEmail(account.Email),
		Label:             displayLabel,
		State:             stateAccount.State,
		LastReason:        stateAccount.LastReason,
		DisableReason:     stateAccount.DisableReason,
		LastAttempt:       stateAccount.LastAttempt,
		LastObservedAt:    stateAccount.LastObservedAt,
		CooldownUntil:     stateAccount.CooldownUntil,
		ProbeBackoffUntil: stateAccount.ProbeBackoffUntil,
		ProxyStatus:       "unbound",
		Proxy:             maskProxyURL(proxyURL),
		TPS:               stateAccount.Metrics.TPS,
		OutputTokens:      stateAccount.Metrics.OutputTokens,
		ReasoningTokens:   stateAccount.Metrics.ReasoningTokens,
		EncryptedBytes:    stateAccount.Metrics.EncryptedBytes,
	}
	if view.State == "" {
		view.State = StateUnknown
		if account.Disabled {
			view.State = StateExternallyDisabled
		}
	}
	binding, hasBinding := s.proxyBindingForAccount(stateAccount)
	proxyStatus, sourceID, sourceKind, _ := s.proxyAccountBindingView(stateAccount, proxyURL)
	view.ProxyStatus = proxyStatus
	view.ProxySourceID = sourceID
	view.ProxySourceKind = string(sourceKind)
	if sourceID != "" {
		view.ProxySourceName, _ = s.proxySourceInfo(sourceID)
	}
	view.ProxyManaged = hasBinding
	view.AutoBindExcluded = stateAccount.AutoBind.Excluded
	if hasBinding {
		view.ProxyGeneration = binding.Generation
		if binding.Lease != nil {
			view.ProxyLeaseAccount = binding.Lease.Account
		}
	}
	return view
}

func accountStateFromSummary(summary AccountStateSummary) AccountState {
	return AccountState{
		State:             summary.State,
		LastReason:        summary.LastReason,
		DisableReason:     summary.DisableReason,
		LastAttempt:       summary.LastAttempt,
		LastObservedAt:    summary.LastObservedAt,
		CooldownUntil:     summary.CooldownUntil,
		ProbeBackoffUntil: summary.ProbeBackoffUntil,
		Proxy:             summary.Proxy,
		AutoBind:          summary.AutoBind,
		Metrics:           summary.Metrics,
	}
}

func (s *Service) accountView(account Account, stateAccount AccountState, proxyURL string) accountView {
	return accountView{
		accountSummaryView:     s.accountSummary(account, stateAccount, proxyURL),
		Evidence:               stateAccount.LastEvidence,
		LastProbeAt:            stateAccount.LastProbeAt,
		DisableKind:            stateAccount.DisableKind,
		StrikeMissingThinking:  stateAccount.QualityStrikeMissingThinking,
		QualityFailureAt:       stateAccount.QualityFailureAt,
		HealthyStreak:          stateAccount.HealthyStreak,
		HealthyStreakStartedAt: stateAccount.HealthyStreakStartedAt,
		SoftStrikes:            stateAccount.SoftStrikes,
		ErrorStrikes:           stateAccount.ErrorStrikes,
		ThinkingStrikes:        stateAccount.ThinkingStrikes,
		TTFTMS:                 stateAccount.Metrics.FirstTokenMS,
		VisibleTokens:          stateAccount.Metrics.VisibleTokens,
	}
}

// Runtime-only auths need a per-auth read for proxy metadata, so cap that fallback
// to small catalogs and keep bulk management requests free of N+1 host calls
const catalogProxyFallbackLimit = 200

func (s *Service) accountProxyURL(ctx context.Context, account Account, allowFallback bool) string {
	proxyURL := strings.TrimSpace(account.ProxyURL)
	if proxyURL != "" || strings.TrimSpace(account.Path) != "" || !allowFallback {
		return proxyURL
	}
	_, _, raw, errResolve := s.resolveAuth(ctx, account.ID)
	if errResolve != nil {
		return ""
	}
	return strings.TrimSpace(authRawString(raw, "proxy_url"))
}

func (s *Service) accountViewForAccount(ctx context.Context, account Account) (accountView, error) {
	_, _, raw, errResolve := s.resolveAuth(ctx, account.ID)
	if errResolve != nil {
		return accountView{}, errResolve
	}
	stateAccount, _ := s.stateAccount(account.ID)
	rawProxyURL := strings.TrimSpace(authRawString(raw, "proxy_url"))
	return s.accountView(account, stateAccount, rawProxyURL), nil
}
