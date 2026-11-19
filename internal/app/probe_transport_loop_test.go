package app

import (
	"context"
	"strings"
	"testing"

	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestProbeBroadTransportErrorCountsAndStopsPendingLoop(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	readError := "no auth available"
	host.scriptStreamSequence("auth-1",
		[]pluginapi.HostModelStreamReadResponse{{Error: readError, Done: true}},
		[]pluginapi.HostModelStreamReadResponse{{Error: readError, Done: true}},
	)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "observe"
		cfg.Quality.ConsecutiveErrors = 2
	})

	for attempt := 0; attempt < 2; attempt++ {
		if errProbe := plugin.ProbeAccount(context.Background(), "auth-1", true, ""); errProbe != nil {
			t.Fatal(errProbe)
		}
	}

	account, _ := plugin.stateAccount("auth-1")
	if account.State == appstate.StateUnknown {
		t.Fatalf("transport failures left the account pending forever: %#v", account)
	}
	if account.State != appstate.StateDegraded {
		t.Fatalf("state = %q, want %q: %#v", account.State, appstate.StateDegraded, account)
	}
	if account.ErrorStrikes != 2 {
		t.Fatalf("error strikes = %d, want 2: %#v", account.ErrorStrikes, account)
	}
	if account.LastAttempt == nil || account.LastAttempt.Class != qualitypkg.ClassProbeError {
		t.Fatalf("attempt class = %#v, want %q", account.LastAttempt, qualitypkg.ClassProbeError)
	}
	if account.LastEvidence != nil {
		t.Fatalf("operational transport failure overwrote quality evidence: %#v", account.LastEvidence)
	}
	if due, _ := plugin.probeDue("auth-1", false); due {
		t.Fatal("degraded account remained due for endless probing")
	}

	foundInternalError := false
	for _, event := range plugin.store.Snapshot().Events {
		if event.AuthID == "auth-1" && event.Action == "internal_error" && strings.Contains(event.Detail, readError) {
			foundInternalError = true
			break
		}
	}
	if !foundInternalError {
		t.Fatal("probe transport error was not preserved in the event log")
	}
}
