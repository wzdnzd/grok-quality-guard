package routing

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
)

type preferredEligibilityTestEnvironment struct {
	account Account
	state   appstate.AccountState
	policy  config.Policy
}

func (e *preferredEligibilityTestEnvironment) Store() *appstate.Store { return nil }
func (e *preferredEligibilityTestEnvironment) CatalogSnapshot() []Account {
	return []Account{e.account}
}
func (e *preferredEligibilityTestEnvironment) FindAccount(string) (Account, bool) {
	return e.account, true
}
func (e *preferredEligibilityTestEnvironment) StateAccount(string) (appstate.AccountState, bool) {
	return e.state, true
}
func (e *preferredEligibilityTestEnvironment) Policy() config.Policy { return e.policy }
func (e *preferredEligibilityTestEnvironment) ExecutableAuth(context.Context, string) (Account, map[string]json.RawMessage, bool) {
	return e.account, nil, true
}

func TestTransparentSelectionClearsPreferredWhenCatalogAccountBecomesDisabled(t *testing.T) {
	env := &preferredEligibilityTestEnvironment{
		account: Account{ID: "auth-1", Provider: "xai", Priority: 10},
		state:   appstate.AccountState{AuthID: "auth-1", State: appstate.StateHealthy},
		policy:  config.PolicyFromConfig(config.DefaultConfig()),
	}
	service := NewService(env)
	if errEnsure := service.ensureSelectionIndex(); errEnsure != nil {
		t.Fatal(errEnsure)
	}
	service.selectionRuntime.SetPreferred("auth-1", time.Now().UTC())
	selection, errSelection := service.newTransparentSelectionWithSession("")
	if errSelection != nil {
		t.Fatal(errSelection)
	}
	if selection.preferredID != "auth-1" {
		t.Fatalf("preferred selection = %q, want auth-1", selection.preferredID)
	}
	selection.currentLayerKey = selection.topLayerKey

	env.account.Disabled = true
	if attempt, ok := selection.tryPreferredSpecial(context.Background()); ok {
		t.Fatalf("disabled catalog account was selected as preferred: %#v", attempt)
	}
	if preferred, _ := service.selectionRuntime.Preferred(); preferred != "" {
		t.Fatalf("disabled preferred auth was retained: %q", preferred)
	}
}
