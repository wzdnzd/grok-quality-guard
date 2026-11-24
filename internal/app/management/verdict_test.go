package management

import (
	"testing"
	"time"

	appstate "grok-quality-guard/internal/app/state"
	domain "grok-quality-guard/internal/quality"
)

func TestVerdictDecisionFollowsStateThenClass(t *testing.T) {
	tests := []struct {
		class string
		state string
		want  string
	}{
		{"", appstate.StateHealthy, verdictDecisionDeliver},
		{"", appstate.StateUnknown, verdictDecisionObserve},
		{domain.ClassMissingThinking, appstate.StateDegraded, verdictDecisionQuarantine},
		{domain.ClassRateLimited, appstate.StateCooling, verdictDecisionCooldown},
		{domain.ClassCredentialInvalid, appstate.StateCredentialInvalid, verdictDecisionDisable},
		{domain.ClassProxyError, appstate.StateUnreachable, verdictDecisionRetry},
		{domain.ClassInconclusive, appstate.StateSuspect, verdictDecisionDeliver},
		{domain.ClassSoftTPS, appstate.StateSuspect, verdictDecisionObserve},
	}
	for _, test := range tests {
		if got := verdictDecision(test.class, test.state); got != test.want {
			t.Fatalf("verdictDecision(%q, %q) = %q, want %q", test.class, test.state, got, test.want)
		}
	}
}

func TestVerdictCategorySeparatesTransportFromQuality(t *testing.T) {
	tests := map[string]string{
		"":                            verdictCategoryIgnored,
		domain.ClassMissingThinking:   verdictCategoryQuality,
		domain.ClassProxyError:        verdictCategoryTransport,
		domain.ClassProbeError:        verdictCategoryTransport,
		domain.ClassRateLimited:       verdictCategoryAccount,
		domain.ClassInconclusive:      verdictCategoryInconclusive,
		domain.ClassFakeEncryptedDump: verdictCategoryQuality,
	}
	for class, want := range tests {
		if got := verdictCategory(class); got != want {
			t.Fatalf("verdictCategory(%q) = %q, want %q", class, got, want)
		}
	}
}

func TestBuildVerdictViewPrefersEvidenceAndSkipsUntouchedAccounts(t *testing.T) {
	if view := buildVerdictView(appstate.AccountState{State: appstate.StateUnknown}); view != nil {
		t.Fatalf("untouched account verdict = %#v, want nil", view)
	}
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	account := appstate.AccountState{
		State:           appstate.StateDegraded,
		ThinkingStrikes: 2,
		LastAttempt:     &appstate.AttemptEvidence{At: at.Add(-time.Minute), Source: "probe", Class: domain.ClassProbeError},
		LastEvidence: &appstate.QualityEvidence{
			At:            at,
			Source:        "transparent",
			Class:         domain.ClassMissingThinking,
			Reason:        "reason.missing_thinking",
			VisibleTokens: 1580,
			HoldExpired:   true,
		},
	}
	view := buildVerdictView(account)
	if view == nil || view.Evidence == nil {
		t.Fatalf("verdict view = %#v, want evidence", view)
	}
	if view.Code != domain.ClassMissingThinking || view.Category != verdictCategoryQuality || view.Decision != verdictDecisionQuarantine {
		t.Fatalf("verdict = %+v, want quality/missing_thinking/quarantine", *view)
	}
	if view.Source != "transparent" || !view.At.Equal(at) {
		t.Fatalf("verdict source/at = %q/%v, want transparent/%v", view.Source, view.At, at)
	}
	if view.Evidence.Strikes != 2 || view.Evidence.VisibleTokens != 1580 || !view.Evidence.HoldExpired {
		t.Fatalf("evidence = %+v", *view.Evidence)
	}
}
