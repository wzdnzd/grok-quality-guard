package management

import (
	"strings"
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
		{ID: "sparse-enabled"},
	}
	counts := catalogStateCounts(store, catalog)
	if counts[StateUnknown] != 2 || counts[StateHealthy] != 1 || counts[StateExternallyDisabled] != 1 {
		t.Fatalf("catalog state counts = %#v", counts)
	}
	if counts[""] != 0 {
		t.Fatalf("catalog state counts contain an empty state: %#v", counts)
	}
}

func TestCatalogAccountMatchesPreservesSearchSemantics(t *testing.T) {
	account := Account{
		ID:    "auth-0042",
		Email: "Alice@Example.com",
		Label: "团队 Alpha",
	}
	for _, query := range []string{
		"alice",
		"ALICE@EXAMPLE.COM",
		"auth-0042",
		"团队",
		"alpha",
		"alice@example.com auth-0042",
	} {
		if !catalogAccountMatches(account, strings.ToLower(query)) {
			t.Fatalf("query %q did not match account", query)
		}
	}
	if catalogAccountMatches(account, "missing") {
		t.Fatal("unexpected match for absent query")
	}
}

func TestAuthProxyMetadataPendingKeepsUnreadFilesUnknown(t *testing.T) {
	if !authProxyMetadataPending(Account{Path: "auth.json"}, "") {
		t.Fatal("unread file-backed auth should be pending")
	}
	if authProxyMetadataPending(Account{Path: "auth.json", ProxyURLKnown: true}, "") {
		t.Fatal("known empty proxy should not be pending")
	}
	if authProxyMetadataPending(Account{Path: "auth.json"}, "socks5h://proxy.example:1080") {
		t.Fatal("resolved proxy should not be pending")
	}
	if authProxyMetadataPending(Account{}, "") {
		t.Fatal("runtime-only auth should preserve existing unbound handling")
	}
}

func TestAuthProxyMetadataInspectedFailureIsNotPending(t *testing.T) {
	account := Account{Path: "auth.json", ProxyMetaInspected: true}
	if !authProxyMetadataUnknown(account, "") {
		t.Fatal("failed auth metadata read should keep proxy status unknown")
	}
	if authProxyMetadataPending(account, "") {
		t.Fatal("failed auth metadata read should not advertise pending work")
	}
}
