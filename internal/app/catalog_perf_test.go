package app

import (
	"encoding/json"
	"fmt"
	managementapp "grok-quality-guard/internal/app/management"
	appstate "grok-quality-guard/internal/app/state"
	qualitypkg "grok-quality-guard/internal/quality"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestLargeCatalogManagementAvoidsPerAccountHostReads(t *testing.T) {
	const authCount = 3000
	entries := make([]pluginapi.HostAuthFileEntry, 0, authCount)
	auths := make(map[string]json.RawMessage, authCount)
	for index := 0; index < authCount; index++ {
		authID := fmt.Sprintf("auth-%04d", index)
		entries = append(entries, pluginapi.HostAuthFileEntry{
			ID:        authID,
			AuthIndex: authID,
			Name:      authID,
			Type:      "xai",
			Provider:  "xai",
			Label:     authID + "@example.com",
			Email:     authID + "@example.com",
			Priority:  1,
		})
		auths[authID] = makeAuthJSON(t, map[string]any{
			"type":          "xai",
			"email":         authID + "@example.com",
			"access_token":  "access-" + authID,
			"refresh_token": "refresh-" + authID,
			"disabled":      false,
		})
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	host.resetCatalogCallCounts()

	response := actionManagementForTest(t, plugin, "accounts.list", map[string]any{
		"page":      1,
		"page_size": 10000,
		"summary":   true,
	})
	if response.StatusCode != 200 {
		t.Fatalf("accounts.list status = %d body=%s", response.StatusCode, response.Body)
	}
	t.Logf("3000-account summary response bytes: %d", len(response.Body))
	var decoded struct {
		Data struct {
			Items []managementapp.AccountSummaryView `json:"items"`
			Total int                                `json:"total"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &decoded); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if decoded.Data.Total != authCount || len(decoded.Data.Items) != authCount {
		t.Fatalf("unexpected account page: total=%d items=%d", decoded.Data.Total, len(decoded.Data.Items))
	}

	overview := actionManagementForTest(t, plugin, "overview", nil)
	if overview.StatusCode != 200 {
		t.Fatalf("overview status = %d body=%s", overview.StatusCode, overview.Body)
	}
	listCalls, modelCalls, getCalls := host.catalogCallCounts()
	if listCalls != 0 || modelCalls != 0 || getCalls != 0 {
		t.Fatalf("management requests used host catalog calls: list=%d models=%d get=%d", listCalls, modelCalls, getCalls)
	}

	if errRefresh := plugin.RefreshCatalog(t.Context()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	listCalls, modelCalls, getCalls = host.catalogCallCounts()
	if listCalls != 1 || modelCalls != 0 || getCalls != 0 {
		t.Fatalf("unchanged refresh used extra host calls: list=%d models=%d get=%d", listCalls, modelCalls, getCalls)
	}
}

func TestAccountListSummaryOmitsDetailFields(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := state.Accounts["auth-1"]
		account.LastEvidence = &appstate.QualityEvidence{Class: qualitypkg.ClassMissingThinking, Reason: "quality detail"}
		account.LastProbeAt = time.Now().UTC()
		account.SoftStrikes = 2
		account.Metrics.FirstTokenMS = 123
		account.Metrics.VisibleTokens = 456
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	response := actionManagementForTest(t, plugin, "accounts.list", map[string]any{"summary": true})
	if response.StatusCode != 200 {
		t.Fatalf("accounts.list status = %d body=%s", response.StatusCode, response.Body)
	}
	t.Logf("3000-account summary response bytes: %d", len(response.Body))
	text := string(response.Body)
	for _, detail := range []string{"\"evidence\"", "\"last_probe_at\"", "\"soft_strikes\"", "\"ttft_ms\"", "\"visible_tokens\""} {
		if strings.Contains(text, detail) {
			t.Fatalf("summary response leaked detail field %s: %s", detail, text)
		}
	}
	if !strings.Contains(text, "alice@example.com") || !strings.Contains(text, "\"state\"") {
		t.Fatalf("summary response missing expected fields: %s", text)
	}
}
