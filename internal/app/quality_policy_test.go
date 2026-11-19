package app

import (
	"context"
	"encoding/json"
	managementapp "grok-quality-guard/internal/app/management"
	appstate "grok-quality-guard/internal/app/state"
	apptasks "grok-quality-guard/internal/app/tasks"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestConsecutiveSoftTPSThreshold(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_only" })
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassSoftTPS, Signals: qualitypkg.Signals{OutputTokens: 40, TPS: 600}}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateSuspect || account.SoftStrikes != 1 || len(host.saves) != 0 {
		t.Fatalf("first soft observation should only be suspect: %#v saves=%d", account, len(host.saves))
	}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.SoftStrikes != 2 || len(host.saves) != 1 {
		t.Fatalf("second soft observation should trigger cooldown: %#v saves=%d", account, len(host.saves))
	}
}

func TestConsecutiveErrorsThreshold(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_only" })
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassProbeError}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "probe", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateSuspect || account.ErrorStrikes != 1 || len(host.saves) != 0 {
		t.Fatalf("first error should only be suspect: %#v saves=%d", account, len(host.saves))
	}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "probe", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.ErrorStrikes != 2 || len(host.saves) != 1 {
		t.Fatalf("second error should trigger cooldown: %#v saves=%d", account, len(host.saves))
	}
}

func TestConsecutiveMissingThinkingThreshold(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_then_disable"
		cfg.Quality.ConsecutiveMissingThinking = 2
	})
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassMissingThinking}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateSuspect || account.ThinkingStrikes != 1 || len(host.saves) != 0 {
		t.Fatalf("first missing-thinking should only be suspect: %#v saves=%d", account, len(host.saves))
	}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || account.ThinkingStrikes != 2 || !account.QualityStrikeMissingThinking || len(host.saves) != 1 {
		t.Fatalf("second missing-thinking should trigger cooldown: %#v saves=%d", account, len(host.saves))
	}
}

func TestNonQualityProbeStatusDoesNotConsumeErrorStrikes(t *testing.T) {
	if class := classifyProbeStatus(401); class != qualitypkg.ClassCredentialInvalid {
		t.Fatalf("401 classified as %q, want %q", class, qualitypkg.ClassCredentialInvalid)
	}
	if class := classifyProbeStatus(500); class != qualitypkg.ClassIgnored {
		t.Fatalf("500 classified as %q, want %q", class, qualitypkg.ClassIgnored)
	}
	if class := classifyProbeStatus(407); class != qualitypkg.ClassProxyError {
		t.Fatalf("407 classified as %q, want %q", class, qualitypkg.ClassProxyError)
	}
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "cooldown_only" })
	for index := 0; index < 3; index++ {
		verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassIgnored}
		if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "probe", ""); errApply != nil {
			t.Fatal(errApply)
		}
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.ErrorStrikes != 0 || account.State != appstate.StateSuspect || len(host.saves) != 0 {
		t.Fatalf("ignored probe changed quality state: %#v saves=%d", account, len(host.saves))
	}
}

func TestSoftCrossVerifySchedulesProbeBeforeAction(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_then_disable"
		cfg.Quality.ConsecutiveSoft = 1
		cfg.Quality.SoftCrossVerify = true
	})
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassSoftTPS, Signals: qualitypkg.Signals{OutputTokens: 40, TPS: 600}}
	if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
		t.Fatal(errApply)
	}
	if len(host.saves) != 0 {
		t.Fatalf("cross-verify threshold wrote auth before confirmation: %d", len(host.saves))
	}
	found := false
	for _, task := range plugin.listTasks(20) {
		if task.Kind == "quality.cross_verify" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("soft cross-verify task was not scheduled")
	}
}

func TestObserveModeNeverWritesAuthAtThreshold(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) { cfg.AutoAction = "observe" })
	verdict := qualitypkg.QualityVerdict{Class: qualitypkg.ClassSoftTPS, Signals: qualitypkg.Signals{OutputTokens: 40, TPS: 600}}
	for index := 0; index < 2; index++ {
		if errApply := plugin.ApplyVerdict(context.Background(), "auth-1", verdict, "passive", ""); errApply != nil {
			t.Fatal(errApply)
		}
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateDegraded || len(host.saves) != 0 {
		t.Fatalf("observe mode modified auth: %#v saves=%d", account, len(host.saves))
	}
}

func TestStateStorePersistsPolicyAndStrikes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := appstate.NewStore(path)
	policy := appconfig.PolicyFromConfig(appconfig.DefaultConfig())
	policy.Quality.ConsecutiveSoft = 4
	policy.Quality.ConsecutiveErrors = 3
	policy.Quality.ConsecutiveMissingThinking = 2
	policy.Quality.ThinkingGuard = false
	policy.Quality.SoftCrossVerify = true
	policy.Quality.ThinkingCrossVerify = true
	if errLoad := store.Load(policy, "revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	if errUpdate := store.Update(func(state *appstate.State) error {
		state.Accounts["auth-1"] = &appstate.AccountState{
			AuthID:          "auth-1",
			State:           appstate.StateSuspect,
			FirstSeenAt:     time.Now().UTC(),
			SoftStrikes:     2,
			ErrorStrikes:    1,
			ThinkingStrikes: 3,
		}
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	reloaded := appstate.NewStore(path)
	if errLoad := reloaded.Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "revision-2"); errLoad != nil {
		t.Fatal(errLoad)
	}
	snapshot := reloaded.Snapshot()
	quality := snapshot.Policy.Quality
	if quality.ConsecutiveSoft != 4 || quality.ConsecutiveErrors != 3 || quality.ConsecutiveMissingThinking != 2 {
		t.Fatalf("consecutive policy was not persisted: %#v", quality)
	}
	if quality.ThinkingGuard || !quality.SoftCrossVerify || !quality.ThinkingCrossVerify {
		t.Fatalf("thinking policy was not persisted: %#v", quality)
	}
	account := snapshot.Accounts["auth-1"]
	if account == nil || account.SoftStrikes != 2 || account.ErrorStrikes != 1 || account.ThinkingStrikes != 3 {
		t.Fatalf("strikes were not persisted: %#v", account)
	}
}

func TestProbeManagementReturnsAcceptedTask(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	body, errMarshal := json.Marshal(managementapp.ActionRequest{Action: "accounts.probe", Params: map[string]any{"auth_id": "auth-1", "force": true}})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", http.Header{"Content-Type": []string{"application/json"}}, body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("probe status = %d body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data apptasks.Task `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if envelope.Data.ID == "" || envelope.Data.Kind != "accounts.probe" {
		t.Fatalf("unexpected task response: %#v", envelope.Data)
	}
	finished := waitTaskForTest(t, plugin, envelope.Data.ID)
	if finished.Status != taskStatusSucceeded && finished.Status != taskStatusFailed {
		t.Fatalf("task did not finish: %#v", finished)
	}
}
