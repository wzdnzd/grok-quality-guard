package app

import (
	"context"
	"strings"
	"testing"

	appauth "grok-quality-guard/internal/app/auth"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestProbeStrongCredentialRejectionDisablesAuth(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	readError := "invalid token"
	host.scriptStream("auth-1", pluginapi.HostModelStreamReadResponse{Error: readError, Done: true})
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "observe" })

	if errProbe := plugin.ProbeAccount(context.Background(), "auth-1", true, ""); errProbe != nil {
		t.Fatal(errProbe)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCredentialInvalid || account.DisableKind != appstate.DisableKindCredentialInvalid {
		t.Fatalf("strong credential rejection was not actioned: %#v", account)
	}
	if account.LastAttempt == nil || account.LastAttempt.Class != qualitypkg.ClassCredentialInvalid || account.LastAttempt.Reason != "reason.detail.credential_rejected" {
		t.Fatalf("credential rejection attempt = %#v", account.LastAttempt)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if !appauth.Disabled(saved) {
		t.Fatalf("credential rejection did not disable auth: %#v", saved)
	}

	foundInternalError := false
	for _, event := range plugin.store.Snapshot().Events {
		if event.AuthID == "auth-1" && event.Action == "internal_error" && strings.Contains(event.Detail, readError) {
			foundInternalError = true
			break
		}
	}
	if !foundInternalError {
		t.Fatal("credential rejection did not preserve the sanitized host error")
	}
}
