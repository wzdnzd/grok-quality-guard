package management

import (
	"testing"

	appstate "grok-quality-guard/internal/app/state"
)

func TestCatalogStateCountsOnlyLiveCatalogAccounts(t *testing.T) {
	store := appstate.NewStore("")
	store.SetStateForTest(appstate.State{Accounts: map[string]*appstate.AccountState{
		"live-unknown": {AuthID: "live-unknown", State: StateUnknown},
		"live-healthy": {AuthID: "live-healthy", State: StateHealthy},
		"stale":        {AuthID: "stale", State: StateUnknown},
	}})
	catalog := []Account{
		{ID: "live-unknown"},
		{ID: "live-healthy"},
		{ID: "live-disabled", Disabled: true},
	}
	counts := catalogStateCounts(store, catalog)
	if counts[StateUnknown] != 1 || counts[StateHealthy] != 1 || counts[StateExternallyDisabled] != 1 {
		t.Fatalf("catalog state counts = %#v", counts)
	}
	if counts[""] != 0 {
		t.Fatalf("catalog state counts contain an empty state: %#v", counts)
	}
}
