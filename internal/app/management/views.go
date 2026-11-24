package management

import (
	"context"
	"strings"

	proxyapp "grok-quality-guard/internal/app/proxy"
)

func (s *Service) accountSummary(account Account, stateAccount AccountState, proxyURL string) accountSummaryView {
	return s.accountSummaryWithHash(account, stateAccount, proxyURL, "")
}

func (s *Service) accountSummaryWithHash(account Account, stateAccount AccountState, proxyURL, proxyHash string) accountSummaryView {
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
		Proxy:             proxyURL,
		TPS:               stateAccount.Metrics.TPS,
		OutputTokens:      stateAccount.Metrics.OutputTokens,
		ReasoningTokens:   stateAccount.Metrics.ReasoningTokens,
		EncryptedBytes:    stateAccount.Metrics.EncryptedBytes,
		Verdict:           buildVerdictView(stateAccount),
	}
	if view.Proxy != "" && (account.ProxyMasked == "" || view.Proxy != account.ProxyMasked) {
		view.Proxy = maskProxyURL(view.Proxy)
	}
	if view.State == "" {
		view.State = StateUnknown
		if account.Disabled {
			view.State = StateExternallyDisabled
		}
	}
	binding, hasBinding := s.proxyBindingForAccount(stateAccount)
	proxyStatus, sourceID, sourceKind, _ := s.proxyAccountBindingViewWithHash(stateAccount, proxyURL, proxyHash)
	if authProxyMetadataUnknown(account, proxyURL) {
		// Auth-file metadata is enriched asynchronously after the host catalog is
		// published. Keep both the in-flight and failed-read cases explicitly
		// unknown instead of claiming the account is unbound or drifted.
		proxyStatus = "unknown"
	}
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

// accountSummaryWithBinding is the canonical management projection. The
// account table and proxy binding table both feed this same resolver output,
// so their binding status cannot diverge while one page has fresher data.
func (s *Service) accountSummaryWithBinding(account Account, stateAccount AccountState, binding proxyapp.BindingState) accountSummaryView {
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
		ProxyStatus:       binding.Status,
		Proxy:             binding.Proxy,
		ProxySourceID:     binding.SourceID,
		ProxySourceKind:   string(binding.SourceKind),
		ProxyManaged:      binding.SourceID != "",
		ProxyGeneration:   binding.Generation,
		ProxyLeaseAccount: binding.LeaseAccount,
		ProxyPending:      binding.Pending,
		ProxyInspected:    binding.Inspected,
		BindingVersion:    binding.Version,
		TPS:               stateAccount.Metrics.TPS,
		OutputTokens:      stateAccount.Metrics.OutputTokens,
		ReasoningTokens:   stateAccount.Metrics.ReasoningTokens,
		EncryptedBytes:    stateAccount.Metrics.EncryptedBytes,
		Verdict:           buildVerdictView(stateAccount),
	}
	if view.State == "" {
		view.State = StateUnknown
		if account.Disabled {
			view.State = StateExternallyDisabled
		}
	}
	if binding.SourceID != "" {
		view.ProxySourceName, _ = s.proxySourceInfo(binding.SourceID)
	}
	view.AutoBindExcluded = stateAccount.AutoBind.Excluded
	return view
}

func authProxyMetadataUnknown(account Account, proxyURL string) bool {
	return !account.ProxyURLKnown && strings.TrimSpace(account.Path) != "" && strings.TrimSpace(proxyURL) == ""
}

func authProxyMetadataPending(account Account, proxyURL string) bool {
	return authProxyMetadataUnknown(account, proxyURL) && !account.ProxyMetaInspected
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

func (s *Service) accountViewWithBinding(account Account, stateAccount AccountState, binding proxyapp.BindingState) accountView {
	return accountView{
		accountSummaryView:     s.accountSummaryWithBinding(account, stateAccount, binding),
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

func (s *Service) accountViewForAccount(ctx context.Context, account Account) (accountView, error) {
	stateAccount, _ := s.stateAccount(account.ID)
	bindings := s.env.Proxy().BindingStatesContext(ctx, []Account{account})
	if len(bindings) == 0 {
		return accountView{}, nil
	}
	return s.accountViewWithBinding(account, stateAccount, bindings[0]), nil
}
