package probe

import (
	"errors"
	"testing"
)

func TestReasoningRequiredForModelWithNonReasoningOverridesFallback(t *testing.T) {
	if reasoningRequiredForModel("grok-4.8", nil) == false {
		t.Fatal("grok-4.8 fallback should require thinking without an exclusion")
	}
	if reasoningRequiredForModelWithNonReasoning("Build/grok-4.8", nil, []string{"grok-4.8"}) {
		t.Fatal("configured non-reasoning model still required thinking")
	}
}

func TestClassifyProbeTransportFailureCredentialEvidence(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		wantClass  string
		wantReason string
	}{
		{name: "invalid token", message: "invalid token", wantClass: ClassCredentialInvalid, wantReason: "reason.detail.credential_rejected"},
		{name: "token expired", message: "token expired", wantClass: ClassCredentialInvalid, wantReason: "reason.detail.credential_rejected"},
		{name: "invalid api key", message: "invalid api key", wantClass: ClassCredentialInvalid, wantReason: "reason.detail.credential_rejected"},
		{name: "no auth available", message: "no auth available", wantClass: ClassIgnored, wantReason: "reason.detail.host_auth_not_ready"},
		{name: "auth not found", message: "host_call_failed: auth_not_found: no auth available (providers=xai, model=grok-4.7)", wantClass: ClassIgnored, wantReason: "reason.detail.host_auth_not_ready"},
		{name: "unauthorized", message: "unauthorized", wantClass: ClassProbeError, wantReason: "reason.detail.probe_transport_error"},
		{name: "bare expired", message: "subscription expired", wantClass: ClassProbeError, wantReason: "reason.detail.probe_transport_error"},
		{name: "permission denied", message: "permission denied", wantClass: ClassProbeError, wantReason: "reason.detail.probe_transport_error"},
		{
			name:       "host model cooldown",
			message:    "model_cooldown: All credentials for model grok-4.7 are cooling down",
			wantClass:  ClassIgnored,
			wantReason: "reason.detail.model_cooldown",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			class, reason := classifyProbeTransportFailure(errors.New(test.message))
			if class != test.wantClass || reason != test.wantReason {
				t.Fatalf("classifyProbeTransportFailure(%q) = (%q, %q), want (%q, %q)", test.message, class, reason, test.wantClass, test.wantReason)
			}
		})
	}
}
