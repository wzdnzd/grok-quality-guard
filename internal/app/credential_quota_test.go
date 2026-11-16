package app

import (
	"context"
	"encoding/json"
	"errors"
	appauth "grok-quality-guard/internal/app/auth"
	appquality "grok-quality-guard/internal/app/quality"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestClassifyUpstreamStatusCredentialAndQuota(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: qualitypkg.ClassCredentialInvalid},
		{name: "proxy auth", status: http.StatusProxyAuthRequired, want: qualitypkg.ClassProxyError},
		{name: "payment required", status: http.StatusPaymentRequired, want: qualitypkg.ClassQuotaExhausted},
		{name: "free quota exhausted", status: http.StatusTooManyRequests, body: `{"code":"subscription:free-usage-exhausted","error":"tokens (actual/limit): 10/10"}`, want: qualitypkg.ClassQuotaExhausted},
		{name: "model quota exhausted", status: http.StatusForbidden, body: `{"error":"You've used all the included free usage for model grok-4.7"}`, want: qualitypkg.ClassQuotaExhausted},
		{name: "generic rate limit", status: http.StatusTooManyRequests, body: `{"error":"rate limit exceeded; retry later"}`, want: qualitypkg.ClassRateLimited},
		{name: "rpm quota limit", status: http.StatusTooManyRequests, body: `{"error":"requests per minute quota reached"}`, want: qualitypkg.ClassRateLimited},
		{name: "cloudflare html", status: http.StatusForbidden, body: `<html>authentication required</html>`, want: qualitypkg.ClassIgnored},
		{name: "forbidden credential", status: http.StatusForbidden, body: `{"error":"invalid token"}`, want: qualitypkg.ClassCredentialInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, reason := classifyUpstreamStatus(test.status, []byte(test.body))
			if got != test.want {
				t.Fatalf("class = %q, want %q (reason=%q)", got, test.want, reason)
			}
			if reason == "" {
				t.Fatal("classification reason is empty")
			}
		})
	}
}

func TestCredentialInvalidDisablesAccountEvenInObserveMode(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "observe" })

	errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{
		Class: qualitypkg.ClassCredentialInvalid, Reason: "upstream status 401 (credential rejected)",
	}, "probe", "")
	if errApply != nil {
		t.Fatal(errApply)
	}
	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateCredentialInvalid || account.DisableKind != appstate.DisableKindCredentialInvalid {
		t.Fatalf("account state = %#v", account)
	}
	if !account.CooldownUntil.IsZero() {
		t.Fatalf("credential-invalid state must not have a cooldown: %#v", account)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if !appauth.Disabled(saved) {
		t.Fatalf("auth was not disabled for credential-invalid state: %#v", saved)
	}
	assertNoGuardMetadata(t, saved)
}

func TestQuotaExhaustedCoolsFor24HoursEvenInObserveMode(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "observe" })

	before := time.Now().UTC()
	errApply := plugin.ApplyVerdict(context.Background(), "auth-1", qualitypkg.QualityVerdict{
		Class: qualitypkg.ClassQuotaExhausted, Reason: "upstream status 429 (quota exhausted)",
	}, "probe", "")
	if errApply != nil {
		t.Fatal(errApply)
	}
	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateQuotaExhausted || account.DisableKind != appstate.DisableKindQuota {
		t.Fatalf("account state = %#v", account)
	}
	minimum := before.Add(appquality.QuotaExhaustedCooldown - 2*time.Second)
	maximum := before.Add(appquality.QuotaExhaustedCooldown + 2*time.Second)
	if account.CooldownUntil.Before(minimum) || account.CooldownUntil.After(maximum) {
		t.Fatalf("cooldown = %s, want approximately %s", account.CooldownUntil, before.Add(appquality.QuotaExhaustedCooldown))
	}
	if due, _ := plugin.probeDue("auth-1", false); due {
		t.Fatal("quota-exhausted account must not be probed before cooldown expires")
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if !appauth.Disabled(saved) {
		t.Fatalf("auth was not disabled for quota state: %#v", saved)
	}
	assertNoGuardMetadata(t, saved)
}
func TestProbeUnauthorizedMarksCredentialInvalid(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStreamStatus("auth-1", http.StatusUnauthorized,
		streamData(`{"error":"invalid token"}`),
		pluginapi.HostModelStreamReadResponse{Done: true},
	)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "observe" })

	if errProbe := plugin.ProbeAccount(context.Background(), "auth-1", true, ""); errProbe != nil {
		t.Fatal(errProbe)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCredentialInvalid || account.DisableKind != appstate.DisableKindCredentialInvalid {
		t.Fatalf("account state = %#v", account)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if !appauth.Disabled(saved) {
		t.Fatalf("probe did not disable credential-invalid auth: %#v", saved)
	}
	assertNoGuardMetadata(t, saved)
}

func TestProbeQuota429MarksQuotaExhausted(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStreamStatus("auth-1", http.StatusTooManyRequests,
		streamData(`{"code":"subscription:free-usage-exhausted","error":"tokens (actual/limit): 622160/600000"}`),
		pluginapi.HostModelStreamReadResponse{Done: true},
	)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "observe" })

	before := time.Now().UTC()
	if errProbe := plugin.ProbeAccount(context.Background(), "auth-1", true, ""); errProbe != nil {
		t.Fatal(errProbe)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateQuotaExhausted || account.DisableKind != appstate.DisableKindQuota {
		t.Fatalf("account state = %#v", account)
	}
	if !account.CooldownUntil.After(before.Add(appquality.QuotaExhaustedCooldown - 2*time.Second)) {
		t.Fatalf("quota cooldown was not applied: %#v", account)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if !appauth.Disabled(saved) {
		t.Fatalf("probe did not cool quota-exhausted auth: %#v", saved)
	}
	assertNoGuardMetadata(t, saved)
}
func TestProbeUnauthorizedWithoutStreamIDMarksCredentialInvalid(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	host.scriptStreamStatusWithoutID("auth-1", http.StatusUnauthorized)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "observe" })

	if errProbe := plugin.ProbeAccount(context.Background(), "auth-1", true, ""); errProbe != nil {
		t.Fatal(errProbe)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCredentialInvalid || account.DisableKind != appstate.DisableKindCredentialInvalid {
		t.Fatalf("account state = %#v", account)
	}
}

const modelCooldownHostError = `model_cooldown: All credentials for model grok-4.7 are cooling down (last error: You've used all the included free usage for model grok-4.7 for now. Usage resets over a rolling 24-hour window - tokens (actual/limit): 506780/500000. Upgrade to a Grok subscription for higher limits: https://grok.com/supergrok)`

func TestClassifyTransportErrorIgnoresHostModelCooldown(t *testing.T) {
	class := classifyTransportError(errors.New(modelCooldownHostError))
	if class != qualitypkg.ClassIgnored {
		t.Fatalf("class = %q, want %q", class, qualitypkg.ClassIgnored)
	}
}

func TestProbeModelCooldownDoesNotDisableAuth(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "observe" })
	baseCall := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelExecuteStream {
			return nil, errors.New(modelCooldownHostError)
		}
		return baseCall(method, payload)
	}

	if errProbe := plugin.ProbeAccount(context.Background(), "auth-1", true, ""); errProbe != nil {
		t.Fatal(errProbe)
	}
	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateUnknown || account.DisableKind != "" {
		t.Fatalf("host model cooldown changed auth ownership: %#v", account)
	}
	if account.LastAttempt == nil || account.LastAttempt.Class != qualitypkg.ClassIgnored || account.LastAttempt.Reason != "host_call_failed: model cooldown" {
		t.Fatalf("host model cooldown attempt = %#v", account.LastAttempt)
	}
	if len(host.saves) != 0 {
		t.Fatalf("host model cooldown wrote auth state: %#v", host.saves)
	}
	saved, errDecode := rawAuthToMap(host.auths["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if appauth.Disabled(saved) {
		t.Fatalf("host model cooldown disabled auth: %#v", saved)
	}
}
