package app

import (
	"context"
	"encoding/json"
	"errors"
	appauth "grok-quality-guard/internal/app/auth"
	managementapp "grok-quality-guard/internal/app/management"
	proxyapp "grok-quality-guard/internal/app/proxy"
	appredact "grok-quality-guard/internal/app/redact"
	appstate "grok-quality-guard/internal/app/state"
	apptasks "grok-quality-guard/internal/app/tasks"
	appconfig "grok-quality-guard/internal/config"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type managementEnvelopeForTest struct {
	OK     bool                             `json:"ok"`
	Result managementapp.ManagementResponse `json:"result"`
	Error  *pluginapiErrorTest              `json:"error"`
}

type pluginapiErrorTest struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func TestSettingsSchemaEndpointMatchesSpecs(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	response := callManagementForTest(t, plugin, http.MethodGet, "/v0/management/plugins/grok-quality-guard/settings/schema", nil, nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings schema status = %d", response.StatusCode)
	}
	var payload struct {
		Groups []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		} `json:"groups"`
		Fields []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Groups []struct {
				Key   string `json:"key"`
				Label string `json:"label"`
			} `json:"groups"`
			Fields []struct {
				Path string `json:"path"`
				Type string `json:"type"`
			} `json:"fields"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &envelope); errUnmarshal != nil {
		t.Fatalf("decode settings schema envelope: %v", errUnmarshal)
	}
	payload.Groups = envelope.Data.Groups
	payload.Fields = envelope.Data.Fields
	if len(payload.Groups) != 7 {
		t.Fatalf("settings schema groups = %d, want 7", len(payload.Groups))
	}
	for _, group := range payload.Groups {
		if strings.HasPrefix(group.Label, "ui.") {
			t.Fatalf("group %q label was not localized: %q", group.Key, group.Label)
		}
	}
	paths := make(map[string]string, len(payload.Fields))
	for _, field := range payload.Fields {
		paths[field.Path] = field.Type
	}
	for _, required := range []string{"guard", "quality.attempt_timeout", "quality.request_timeout", "routing.scoring.half_life_hours", "probe.scan_interval"} {
		if _, ok := paths[required]; !ok {
			t.Fatalf("settings schema is missing %q", required)
		}
	}
	if paths["guard"] != "enum" {
		t.Fatalf("guard type = %q, want enum", paths["guard"])
	}
}

func callManagementForTest(t *testing.T, plugin *Plugin, method, path string, headers http.Header, body []byte) managementapp.ManagementResponse {
	t.Helper()
	request := managementapp.RPCManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method:  method,
			Path:    path,
			Headers: headers,
			Body:    body,
		},
		HostCallbackID: "callback-1",
	}
	encoded, errMarshal := json.Marshal(request)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	rawResponse, errHandle := plugin.HandleManagement(encoded)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var envelope managementEnvelopeForTest
	if errUnmarshal := json.Unmarshal(rawResponse, &envelope); errUnmarshal != nil {
		t.Fatalf("decode management envelope: %v", errUnmarshal)
	}
	if !envelope.OK {
		if envelope.Error != nil {
			t.Fatalf("management error: %s: %s", envelope.Error.Code, envelope.Error.Message)
		}
		t.Fatal("management request failed")
	}
	return envelope.Result
}

func actionManagementForTest(t *testing.T, plugin *Plugin, action string, params map[string]any) managementapp.ManagementResponse {
	t.Helper()
	body, errMarshal := json.Marshal(managementapp.ActionRequest{Action: action, Params: params})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	response := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", headers, body)
	if response.StatusCode != http.StatusAccepted {
		return response
	}
	var envelope struct {
		Data apptasks.Task `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &envelope); errUnmarshal != nil {
		t.Fatalf("decode accepted task: %v", errUnmarshal)
	}
	task := waitTaskForTest(t, plugin, envelope.Data.ID)
	if task.Status == taskStatusFailed {
		status := task.ErrorStatus
		if status == 0 {
			status = http.StatusBadGateway
		}
		return managementapp.ManagementResponse{StatusCode: status, Headers: jsonHeaders(), Body: jsonError(actionErrorCode(status), task.Error)}
	}
	return managementapp.ManagementResponse{StatusCode: http.StatusOK, Headers: jsonHeaders(), Body: jsonOK(task.Result)}
}

func waitTaskForTest(t *testing.T, plugin *Plugin, taskID string) apptasks.Task {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		task, okTask := plugin.taskByID(taskID)
		if !okTask {
			t.Fatalf("task %s disappeared", taskID)
		}
		if task.Status == taskStatusSucceeded || task.Status == taskStatusFailed {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not finish", taskID)
	return apptasks.Task{}
}

func scriptHealthyProbeStream(t *testing.T, host *mockHostCall, authID string) {
	t.Helper()
	visible := strings.Repeat("reliable systems need careful design. ", 10) + "\\nQUALITY_OK"
	host.scriptStream(authID, []pluginapi.HostModelStreamReadResponse{
		streamData("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"plan carefully\"}}]}\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{\"content\":\"" + visible + "\"}}]}\n\n"),
		streamData("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"completion_tokens\":120,\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"),
		{Done: true},
	}...)
}

func TestManagementResourceAndActionAccess(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	response := callManagementForTest(t, plugin, http.MethodGet, "/dashboard", nil, nil)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(response.Body), "/plugins/grok-quality-guard/api") {
		t.Fatalf("unexpected dashboard response: status=%d body=%s", response.StatusCode, response.Body)
	}
	for _, removed := range []string{"代理（脱敏）", "选择全部筛选结果", "const strikeBadge = strikes.length", "badge('连续 ' + strikes.join", "id=\"refreshBtn\"", "id=\"overviewModeBadge\"", "window.confirm("} {
		if strings.Contains(string(response.Body), removed) {
			t.Fatalf("dashboard still contains obsolete label %q", removed)
		}
	}
	if strings.Contains(string(response.Body), "X-Grok-Quality-Guard-UI") {
		t.Fatal("dashboard still depends on the custom UI header")
	}
	if strings.Contains(string(response.Body), "对齐旧插件") {
		t.Fatal("dashboard should describe the setting semantically instead of referencing the old plugin")
	}
	for _, expected := range []string{"ui.overview", "ui.proxy_management", "ui.settings", "I18N_RESOURCE_DATA", "I18N_RESOURCES", "ensureResources", "\"ui.overview\":\"概览\"", "proxy.sources.create", "proxy.sources.update", "proxy.auto_bind.update", "policy.routing.strategy.selection", "accounts.probe_selected", "accounts.restore_selected", "accounts.disable_selected", "tasks.center.cancel", "confirmAction", "proxySourceEnabledFilter", "proxySourceHealthFilter"} {
		if !strings.Contains(string(response.Body), expected) {
			t.Fatalf("dashboard is missing %q", expected)
		}
	}
	body, _ := json.Marshal(managementapp.ActionRequest{Action: "overview"})
	response = callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", nil, body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("action without UI marker status = %d body=%s", response.StatusCode, response.Body)
	}
}

func TestAccountsListMasksSecrets(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{
		"proxy_url": "socks5h://user:proxy-secret@example.com:1080/private",
	})
	authPath := filepath.Join(t.TempDir(), "auth-1.json")
	if errWrite := os.WriteFile(authPath, auths["auth-1"], 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	info, errStat := os.Stat(authPath)
	if errStat != nil {
		t.Fatal(errStat)
	}
	entries[0].Path = authPath
	entries[0].Size = info.Size()
	entries[0].ModTime = info.ModTime()
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	response := actionManagementForTest(t, plugin, "accounts.list", map[string]any{"page_size": 10})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	text := string(response.Body)
	for _, forbidden := range []string{"proxy-secret", "access-auth-1", "refresh-auth-1"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("management response leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "al***e@example.com") || !strings.Contains(text, "socks5h://us***r:******@example.com:1080") {
		t.Fatalf("management response did not include masked values: %s", text)
	}
	if !strings.Contains(text, "auth_id_display") {
		t.Fatalf("management response missing masked auth id: %s", text)
	}
	if !strings.Contains(text, "display_full") {
		t.Fatalf("management response missing full display value: %s", text)
	}
	var decoded struct {
		OK   bool `json:"ok"`
		Data struct {
			Items []managementapp.AccountView `json:"items"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &decoded); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(decoded.Data.Items) != 1 || decoded.Data.Items[0].AuthID != "auth-1" {
		t.Fatalf("unexpected accounts list: %#v", decoded.Data.Items)
	}
}

func TestAccountAndProxyPagesShareBindingStatus(t *testing.T) {
	entries1, auths1 := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	entries2, auths2 := singleGrokAuth(t, "auth-2", "[EMAIL]", nil)
	entries := append(entries1, entries2...)
	auths := make(map[string]json.RawMessage, len(auths1)+len(auths2))
	for key, value := range auths1 {
		auths[key] = value
	}
	for key, value := range auths2 {
		auths[key] = value
	}
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "socks5h://user:secret@proxy.example:1080", Name: "primary"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{SourceIDs: []string{node.ID}}); errBind != nil {
		t.Fatal(errBind)
	}

	type accountRow struct {
		AuthID         string `json:"auth_id"`
		ProxyStatus    string `json:"proxy_status"`
		BindingVersion uint64 `json:"binding_version"`
	}
	var accounts struct {
		Data struct {
			Items          []accountRow `json:"items"`
			BindingVersion uint64       `json:"binding_version"`
		} `json:"data"`
	}
	response := actionManagementForTest(t, plugin, "accounts.list", map[string]any{"summary": true, "page_size": 10})
	if errUnmarshal := json.Unmarshal(response.Body, &accounts); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	var bindings struct {
		Data struct {
			Items          []accountRow `json:"items"`
			BindingVersion uint64       `json:"binding_version"`
		} `json:"data"`
	}
	response = actionManagementForTest(t, plugin, "proxy.bindings.list", map[string]any{"page_size": 10})
	if errUnmarshal := json.Unmarshal(response.Body, &bindings); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(accounts.Data.Items) != 2 || len(bindings.Data.Items) != 2 {
		t.Fatalf("account rows = %#v, proxy rows = %#v", accounts.Data.Items, bindings.Data.Items)
	}
	accountStatuses := make(map[string]string, len(accounts.Data.Items))
	for _, row := range accounts.Data.Items {
		accountStatuses[row.AuthID] = row.ProxyStatus
	}
	for _, row := range bindings.Data.Items {
		if got := accountStatuses[row.AuthID]; got != row.ProxyStatus {
			t.Fatalf("binding status differs for %s: account=%q proxy=%q", row.AuthID, got, row.ProxyStatus)
		}
	}
	if accountStatuses["auth-1"] != "bound" || accountStatuses["auth-2"] != "unbound" {
		t.Fatalf("unexpected shared statuses: %#v", accountStatuses)
	}
	if accounts.Data.BindingVersion == 0 || accounts.Data.BindingVersion != bindings.Data.BindingVersion {
		t.Fatalf("binding versions differ: accounts=%d proxy=%d", accounts.Data.BindingVersion, bindings.Data.BindingVersion)
	}

	var summary struct {
		Data struct {
			BindingCounts  map[string]int `json:"binding_counts"`
			BindingTotal   int            `json:"binding_total"`
			BindingVersion uint64         `json:"binding_version"`
		} `json:"data"`
	}
	response = actionManagementForTest(t, plugin, "proxy.summary", map[string]any{})
	if errUnmarshal := json.Unmarshal(response.Body, &summary); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if summary.Data.BindingVersion != bindings.Data.BindingVersion || summary.Data.BindingTotal != 2 || summary.Data.BindingCounts["bound"] != 1 || summary.Data.BindingCounts["unbound"] != 1 {
		t.Fatalf("proxy summary reuse mismatch: %#v", summary.Data)
	}

	var metadata struct {
		Data struct {
			Items          []accountRow `json:"items"`
			BindingVersion uint64       `json:"binding_version"`
		} `json:"data"`
	}
	response = actionManagementForTest(t, plugin, "accounts.proxy_metadata", map[string]any{"auth_ids": []string{"auth-2"}})
	if errUnmarshal := json.Unmarshal(response.Body, &metadata); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(metadata.Data.Items) != 1 || metadata.Data.Items[0].AuthID != "auth-2" || metadata.Data.Items[0].ProxyStatus != "unbound" {
		t.Fatalf("page-scoped metadata = %#v", metadata.Data.Items)
	}
	if metadata.Data.BindingVersion != bindings.Data.BindingVersion {
		t.Fatalf("metadata binding version = %d, want %d", metadata.Data.BindingVersion, bindings.Data.BindingVersion)
	}
}
func TestProxyClearBatchManagement(t *testing.T) {
	entries1, auths1 := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://user:secret-one@example.com:1080",
	})
	entries2, auths2 := singleGrokAuth(t, "auth-2", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://user:secret-two@example.org:1080",
	})
	entries := append(entries1, entries2...)
	auths := make(map[string]json.RawMessage, len(auths1)+len(auths2))
	for key, value := range auths1 {
		auths[key] = value
	}
	for key, value := range auths2 {
		auths[key] = value
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	response := actionManagementForTest(t, plugin, "proxy.bindings.clear", map[string]any{
		"allow_external": true,
		"auth_ids":       []any{"auth-1", "auth-2", "auth-1"},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	text := string(response.Body)
	if strings.Contains(text, "secret-one") || strings.Contains(text, "secret-two") {
		t.Fatalf("batch clear response leaked proxy credentials: %s", text)
	}
	for _, authID := range []string{"auth-1", "auth-2"} {
		saved, errDecode := rawAuthToMap(host.saves[authID])
		if errDecode != nil {
			t.Fatal(errDecode)
		}
		if appauth.RawString(saved, "proxy_url") != "" {
			t.Fatalf("proxy_url for %s was not cleared", authID)
		}
	}
}

func TestProxyClearBatchAllowsExternalProxy(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-external", "[EMAIL]", map[string]any{
		"proxy_url": "socks5h://user:external-secret@external.example:1080",
	})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	response := actionManagementForTest(t, plugin, "proxy.bindings.clear", map[string]any{
		"auth_ids":       []any{"auth-external"},
		"allow_external": true,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	if strings.Contains(string(response.Body), "external-secret") {
		t.Fatalf("batch clear response leaked proxy credentials: %s", response.Body)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-external"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if appauth.RawString(saved, "proxy_url") != "" {
		t.Fatal("external proxy_url was not cleared")
	}
}

func TestPolicyUpdateAndReset(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	policy := appconfig.PolicyFromConfig(plugin.cfg)
	policy.Quality.SoftTPS = 600
	policy.Quality.HardTPS = 1200
	response := actionManagementForTest(t, plugin, "policy.update", map[string]any{"policy": policy})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("policy update status = %d body=%s", response.StatusCode, response.Body)
	}
	if plugin.policy.Quality.SoftTPS != 600 || plugin.policy.Quality.HardTPS != 1200 {
		t.Fatalf("runtime policy not updated: %#v", plugin.policy.Quality)
	}
	state := plugin.store.Snapshot()
	if state.Policy.Quality.SoftTPS != 600 {
		t.Fatalf("persisted policy not updated: %#v", state.Policy.Quality)
	}
	response = actionManagementForTest(t, plugin, "policy.reset", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("policy reset status = %d body=%s", response.StatusCode, response.Body)
	}
	if plugin.policy.Quality.SoftTPS != 500 || plugin.policy.Quality.HardTPS != 1000 {
		t.Fatalf("policy reset did not restore YAML defaults: %#v", plugin.policy.Quality)
	}
}

func TestPolicyUpdateWithoutTaskHistoryPreservesCurrentSetting(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	plugin.policy.TaskHistory.Enabled = false
	plugin.cfg.TaskHistory.Enabled = false

	policy := appconfig.PolicyFromConfig(plugin.cfg)
	encoded, errMarshal := json.Marshal(policy)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var rawPolicy map[string]any
	if errUnmarshal := json.Unmarshal(encoded, &rawPolicy); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	delete(rawPolicy, "task_history")
	rawPolicy["quality"].(map[string]any)["soft_tps"] = 650

	response := actionManagementForTest(t, plugin, "policy.update", map[string]any{"policy": rawPolicy})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("policy update status = %d body=%s", response.StatusCode, response.Body)
	}
	if plugin.policy.TaskHistory.Enabled {
		t.Fatalf("legacy policy update re-enabled task history: %#v", plugin.policy.TaskHistory)
	}
	if plugin.policy.Quality.SoftTPS != 650 {
		t.Fatalf("legacy policy update did not apply quality change: %#v", plugin.policy.Quality)
	}
}

func TestInvalidPolicyUpdateIsRejected(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	policy := appconfig.PolicyFromConfig(plugin.cfg)
	policy.Quality.SoftTPS = 1000
	policy.Quality.HardTPS = 500
	response := actionManagementForTest(t, plugin, "policy.update", map[string]any{"policy": policy})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid policy status = %d body=%s", response.StatusCode, response.Body)
	}
}

func TestManualActionsUpdateState(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	response := actionManagementForTest(t, plugin, "accounts.cooldown", map[string]any{"auth_id": "auth-1", "duration": "2h"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cooldown status = %d body=%s", response.StatusCode, response.Body)
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.State != appstate.StateCooling || time.Until(account.CooldownUntil) < 90*time.Minute {
		t.Fatalf("unexpected cooldown state: %#v", account)
	}
	response = actionManagementForTest(t, plugin, "accounts.restore", map[string]any{"auth_id": "auth-1"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("restore status = %d body=%s", response.StatusCode, response.Body)
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.State != appstate.StateUnknown || !account.CooldownUntil.IsZero() {
		t.Fatalf("unexpected restored state: %#v", account)
	}
}

func TestManualProbeRestoresGuardOwnedAccounts(t *testing.T) {
	cases := []struct {
		name   string
		action string
		params map[string]any
	}{
		{name: "cooling", action: "accounts.cooldown", params: map[string]any{"auth_id": "auth-1", "duration": "2h"}},
		{name: "disabled", action: "accounts.disable", params: map[string]any{"auth_id": "auth-1", "reason": "manual"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
			host := newMockHostCall(entries, auths)
			scriptHealthyProbeStream(t, host, "auth-1")
			plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
				cfg.Quality.SoftTPS = 1e9
				cfg.Quality.HardTPS = 2e9
			})
			response := actionManagementForTest(t, plugin, testCase.action, testCase.params)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("prepare guarded state status = %d body=%s", response.StatusCode, response.Body)
			}
			if account, _ := plugin.stateAccount("auth-1"); account.State == appstate.StateHealthy {
				t.Fatalf("account should be non-healthy before manual probe: %#v", account)
			}
			response = actionManagementForTest(t, plugin, "accounts.probe", map[string]any{"auth_id": "auth-1", "force": true})
			if response.StatusCode != http.StatusOK {
				t.Fatalf("manual probe status = %d body=%s", response.StatusCode, response.Body)
			}
			account, _ := plugin.stateAccount("auth-1")
			if account.State != appstate.StateHealthy {
				t.Fatalf("manual probe did not restore and probe account: %#v", account)
			}
			saved, errDecode := rawAuthToMap(host.saves["auth-1"])
			if errDecode != nil {
				t.Fatal(errDecode)
			}
			if appauth.Disabled(saved) {
				t.Fatal("manual probe left auth disabled")
			}
		})
	}
}

func TestRestoreSelectedBatchManagement(t *testing.T) {
	entriesHealthy, authsHealthy := singleGrokAuth(t, "auth-healthy", "[EMAIL]", nil)
	entriesDegraded, authsDegraded := singleGrokAuth(t, "auth-degraded", "[EMAIL]", nil)
	entriesUnselected, authsUnselected := singleGrokAuth(t, "auth-unselected", "[EMAIL]", nil)
	entriesExternal, authsExternal := singleGrokAuth(t, "auth-external", "[EMAIL]", map[string]any{"disabled": true})
	entries := append(entriesHealthy, entriesDegraded...)
	entries = append(entries, entriesUnselected...)
	entries = append(entries, entriesExternal...)
	auths := make(map[string]json.RawMessage, len(authsHealthy)+len(authsDegraded)+len(authsUnselected)+len(authsExternal))
	for key, value := range authsHealthy {
		auths[key] = value
	}
	for key, value := range authsDegraded {
		auths[key] = value
	}
	for key, value := range authsUnselected {
		auths[key] = value
	}
	for key, value := range authsExternal {
		auths[key] = value
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		plugin.ensureStateAccount(state, "auth-healthy").State = appstate.StateHealthy
		degraded := plugin.ensureStateAccount(state, "auth-degraded")
		degraded.State = appstate.StateDegraded
		degraded.CooldownUntil = time.Now().UTC().Add(time.Hour)
		unselected := plugin.ensureStateAccount(state, "auth-unselected")
		unselected.State = appstate.StateDegraded
		unselected.CooldownUntil = time.Now().UTC().Add(time.Hour)
		plugin.ensureStateAccount(state, "auth-external").State = appstate.StateExternallyDisabled
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	body, errMarshal := json.Marshal(managementapp.ActionRequest{Action: "accounts.restore_selected", Params: map[string]any{"auth_ids": []string{"auth-healthy", "auth-degraded", "auth-external"}}})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", http.Header{"Content-Type": []string{"application/json"}}, body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("restore batch status = %d body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data apptasks.Task `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &envelope); errUnmarshal != nil {
		t.Fatalf("decode restore task: %v", errUnmarshal)
	}
	if envelope.Data.Kind != "accounts.restore_selected" {
		t.Fatalf("unexpected task kind: %q", envelope.Data.Kind)
	}
	task := waitTaskForTest(t, plugin, envelope.Data.ID)
	if task.Status != taskStatusSucceeded {
		t.Fatalf("restore batch task failed: %#v", task)
	}
	encodedResult, errMarshal := json.Marshal(task.Result)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var result managementapp.RestoreBatchResult
	if errUnmarshal := json.Unmarshal(encodedResult, &result); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if result.Count != 3 || result.Restored != 1 || result.Skipped != 1 || result.ExternalLeftDisabled != 1 || result.Failed != 0 {
		t.Fatalf("unexpected restore summary: %#v", result)
	}
	statuses := make(map[string]string, len(result.Items))
	for _, item := range result.Items {
		statuses[item.AuthID] = item.Status
	}
	if statuses["auth-healthy"] != "skipped" || statuses["auth-degraded"] != "restored" || statuses["auth-external"] != "external_left_disabled" {
		t.Fatalf("unexpected restore items: %#v", result.Items)
	}
	state := plugin.stateStore().Snapshot()
	if state.Accounts["auth-healthy"].State != appstate.StateHealthy {
		t.Fatalf("healthy account was modified: %#v", state.Accounts["auth-healthy"])
	}
	if state.Accounts["auth-degraded"].State != appstate.StateUnknown || !state.Accounts["auth-degraded"].CooldownUntil.IsZero() {
		t.Fatalf("degraded account was not restored: %#v", state.Accounts["auth-degraded"])
	}
	if state.Accounts["auth-external"].State != appstate.StateUnknown {
		t.Fatalf("external account guard state was not cleared: %#v", state.Accounts["auth-external"])
	}
	if state.Accounts["auth-unselected"].State != appstate.StateDegraded || state.Accounts["auth-unselected"].CooldownUntil.IsZero() {
		t.Fatalf("unselected account was modified: %#v", state.Accounts["auth-unselected"])
	}
	if _, saved := host.saves["auth-external"]; saved {
		t.Fatal("external disabled auth must not be modified")
	}
}

func TestRestoreSelectedExternalConfirmationAndPreview(t *testing.T) {
	entriesHealthy, authsHealthy := singleGrokAuth(t, "auth-healthy", "[EMAIL]", nil)
	entriesExternal, authsExternal := singleGrokAuth(t, "auth-external", "[EMAIL]", map[string]any{"disabled": true})
	entries := append(entriesHealthy, entriesExternal...)
	auths := make(map[string]json.RawMessage, len(authsHealthy)+len(authsExternal))
	for key, value := range authsHealthy {
		auths[key] = value
	}
	for key, value := range authsExternal {
		auths[key] = value
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		plugin.ensureStateAccount(state, "auth-healthy").State = appstate.StateHealthy
		plugin.ensureStateAccount(state, "auth-external").State = appstate.StateExternallyDisabled
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	previewBody, errMarshal := json.Marshal(managementapp.ActionRequest{Action: "accounts.restore_preview", Params: map[string]any{"auth_ids": []string{"auth-healthy", "auth-external"}}})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	previewResponse := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", http.Header{"Content-Type": []string{"application/json"}}, previewBody)
	if previewResponse.StatusCode != http.StatusOK {
		t.Fatalf("restore preview status = %d body=%s", previewResponse.StatusCode, previewResponse.Body)
	}
	var previewEnvelope struct {
		Data managementapp.RestorePreviewResult `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(previewResponse.Body, &previewEnvelope); errUnmarshal != nil {
		t.Fatalf("decode restore preview: %v", errUnmarshal)
	}
	if previewEnvelope.Data.Count != 2 || previewEnvelope.Data.Healthy != 1 || previewEnvelope.Data.ExternalDisabled != 1 {
		t.Fatalf("unexpected restore preview: %#v", previewEnvelope.Data)
	}

	body, errMarshal := json.Marshal(managementapp.ActionRequest{Action: "accounts.restore_selected", Params: map[string]any{"auth_ids": []string{"auth-healthy", "auth-external"}, "include_external": true}})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", http.Header{"Content-Type": []string{"application/json"}}, body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("restore batch status = %d body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data apptasks.Task `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &envelope); errUnmarshal != nil {
		t.Fatalf("decode restore task: %v", errUnmarshal)
	}
	task := waitTaskForTest(t, plugin, envelope.Data.ID)
	if task.Status != taskStatusSucceeded {
		t.Fatalf("restore batch task failed: %#v", task)
	}
	encodedResult, errMarshal := json.Marshal(task.Result)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var result managementapp.RestoreBatchResult
	if errUnmarshal := json.Unmarshal(encodedResult, &result); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if result.Count != 2 || result.Restored != 0 || result.ExternalRestored != 1 || result.ExternalLeftDisabled != 0 || result.Skipped != 1 {
		t.Fatalf("unexpected restore summary: %#v", result)
	}
	state := plugin.stateStore().Snapshot()
	if state.Accounts["auth-external"].State != appstate.StateUnknown {
		t.Fatalf("external account state = %q, want unknown", state.Accounts["auth-external"].State)
	}
	saved, errDecode := rawAuthToMap(host.saves["auth-external"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if appauth.Disabled(saved) {
		t.Fatal("confirmed external restore left auth disabled")
	}
}

func TestDisableSelectedBatchModes(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		modifyAuth bool
	}{
		{name: "plugin_only", modifyAuth: false},
		{name: "modify_auth", modifyAuth: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			entriesHealthy, authsHealthy := singleGrokAuth(t, "auth-healthy", "[EMAIL]", nil)
			entriesExternal, authsExternal := singleGrokAuth(t, "auth-external", "[EMAIL]", map[string]any{"disabled": true})
			entries := append(entriesHealthy, entriesExternal...)
			auths := make(map[string]json.RawMessage, len(authsHealthy)+len(authsExternal))
			for key, value := range authsHealthy {
				auths[key] = value
			}
			for key, value := range authsExternal {
				auths[key] = value
			}
			host := newMockHostCall(entries, auths)
			plugin := newTestPlugin(t, host, nil)

			body, errMarshal := json.Marshal(managementapp.ActionRequest{Action: "accounts.disable_selected", Params: map[string]any{"auth_ids": []string{"auth-healthy", "auth-external"}, "modify_auth": testCase.modifyAuth}})
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			response := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", http.Header{"Content-Type": []string{"application/json"}}, body)
			if response.StatusCode != http.StatusAccepted {
				t.Fatalf("disable batch status = %d body=%s", response.StatusCode, response.Body)
			}
			var envelope struct {
				Data apptasks.Task `json:"data"`
			}
			if errUnmarshal := json.Unmarshal(response.Body, &envelope); errUnmarshal != nil {
				t.Fatalf("decode disable task: %v", errUnmarshal)
			}
			task := waitTaskForTest(t, plugin, envelope.Data.ID)
			if task.Status != taskStatusSucceeded {
				t.Fatalf("disable batch task failed: %#v", task)
			}
			encodedResult, errMarshal := json.Marshal(task.Result)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			var result managementapp.DisableBatchResult
			if errUnmarshal := json.Unmarshal(encodedResult, &result); errUnmarshal != nil {
				t.Fatal(errUnmarshal)
			}
			if result.Count != 2 || result.Disabled != 1 || result.Skipped != 1 || result.Failed != 0 || result.ModifiedAuth != testCase.modifyAuth {
				t.Fatalf("unexpected disable summary: %#v", result)
			}
			state := plugin.stateStore().Snapshot()
			account := state.Accounts["auth-healthy"]
			if account.State != appstate.StateDisabledByGuard || account.DisableKind != appstate.DisableKindPermanent {
				t.Fatalf("healthy account was not isolated: %#v", account)
			}
			if account.PluginOnlyDisable != !testCase.modifyAuth {
				t.Fatalf("plugin-only flag = %t, want %t", account.PluginOnlyDisable, !testCase.modifyAuth)
			}
			if account, exists := state.Accounts["auth-external"]; exists {
				t.Fatalf("skipped external auth should remain sparse: %#v", account)
			}
			externalFound := false
			for _, external := range plugin.CatalogSnapshot() {
				if external.ID == "auth-external" && external.Disabled {
					externalFound = true
					break
				}
			}
			if !externalFound {
				t.Fatal("external account should remain disabled in catalog")
			}
			if testCase.modifyAuth {
				saved, errDecode := rawAuthToMap(host.saves["auth-healthy"])
				if errDecode != nil {
					t.Fatal(errDecode)
				}
				if !appauth.Disabled(saved) {
					t.Fatal("auth-modifying disable did not set disabled=true")
				}
				return
			}
			if _, saved := host.saves["auth-healthy"]; saved {
				t.Fatal("plugin-only disable modified the auth file")
			}
			if errReconcile := plugin.Reconcile(context.Background()); errReconcile != nil {
				t.Fatal(errReconcile)
			}
			stateAccount, _ := plugin.stateAccount("auth-healthy")
			if stateAccount.State != appstate.StateDisabledByGuard || !stateAccount.PluginOnlyDisable {
				t.Fatalf("reconcile cleared plugin-only disable: %#v", stateAccount)
			}
			if _, saved := host.saves["auth-healthy"]; saved {
				t.Fatal("reconcile modified a plugin-only disabled auth file")
			}
		})
	}
}
func TestProbeSelectedBatchManagement(t *testing.T) {
	entriesHealthy, authsHealthy := singleGrokAuth(t, "auth-healthy", "[EMAIL]", nil)
	entriesSuspect, authsSuspect := singleGrokAuth(t, "auth-suspect", "[EMAIL]", nil)
	entriesGuard, authsGuard := singleGrokAuth(t, "auth-guard", "[EMAIL]", nil)
	entriesExternal, authsExternal := singleGrokAuth(t, "auth-external", "[EMAIL]", map[string]any{"disabled": true})
	entries := append(entriesHealthy, entriesSuspect...)
	entries = append(entries, entriesGuard...)
	entries = append(entries, entriesExternal...)
	auths := make(map[string]json.RawMessage, len(authsHealthy)+len(authsSuspect)+len(authsGuard)+len(authsExternal))
	for key, value := range authsHealthy {
		auths[key] = value
	}
	for key, value := range authsSuspect {
		auths[key] = value
	}
	for key, value := range authsGuard {
		auths[key] = value
	}
	for key, value := range authsExternal {
		auths[key] = value
	}
	host := newMockHostCall(entries, auths)
	scriptHealthyProbeStream(t, host, "auth-suspect")
	scriptHealthyProbeStream(t, host, "auth-guard")
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Quality.SoftTPS = 1e9
		cfg.Quality.HardTPS = 2e9
	})
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		plugin.ensureStateAccount(state, "auth-healthy").State = appstate.StateHealthy
		plugin.ensureStateAccount(state, "auth-suspect").State = appstate.StateSuspect
		plugin.ensureStateAccount(state, "auth-external").State = appstate.StateExternallyDisabled
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if response := actionManagementForTest(t, plugin, "accounts.disable", map[string]any{"auth_id": "auth-guard", "reason": "manual"}); response.StatusCode != http.StatusOK {
		t.Fatalf("disable guard account status = %d body=%s", response.StatusCode, response.Body)
	}

	body, errMarshal := json.Marshal(managementapp.ActionRequest{Action: "accounts.probe_selected", Params: map[string]any{"auth_ids": []string{"auth-suspect", "auth-guard", "auth-external"}}})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", http.Header{"Content-Type": []string{"application/json"}}, body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("probe batch status = %d body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data apptasks.Task `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &envelope); errUnmarshal != nil {
		t.Fatalf("decode probe task: %v", errUnmarshal)
	}
	if envelope.Data.Kind != "accounts.probe_selected" {
		t.Fatalf("unexpected task kind: %q", envelope.Data.Kind)
	}
	task := waitTaskForTest(t, plugin, envelope.Data.ID)
	if task.Status != taskStatusSucceeded {
		t.Fatalf("probe batch task failed: %#v", task)
	}
	encodedResult, errMarshal := json.Marshal(task.Result)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var result managementapp.ProbeBatchResult
	if errUnmarshal := json.Unmarshal(encodedResult, &result); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if result.Count != 3 || result.Probed != 2 || result.Restored != 1 || result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("unexpected probe summary: %#v", result)
	}
	statuses := make(map[string]string, len(result.Items))
	for _, item := range result.Items {
		statuses[item.AuthID] = item.Status
	}
	if statuses["auth-suspect"] != "probed" || statuses["auth-guard"] != "probed" || statuses["auth-external"] != "skipped" {
		t.Fatalf("unexpected probe items: %#v", result.Items)
	}
	state := plugin.stateStore().Snapshot()
	if state.Accounts["auth-healthy"].State != appstate.StateHealthy {
		t.Fatalf("healthy account was modified: %#v", state.Accounts["auth-healthy"])
	}
	if state.Accounts["auth-suspect"].State != appstate.StateHealthy {
		t.Fatalf("suspect account was not probed to healthy: %#v", state.Accounts["auth-suspect"])
	}
	if state.Accounts["auth-guard"].State != appstate.StateHealthy {
		t.Fatalf("guard-disabled account was not restored and probed: %#v", state.Accounts["auth-guard"])
	}
	if account := state.Accounts["auth-external"]; account != nil && account.State != appstate.StateExternallyDisabled {
		t.Fatalf("external account should be skipped: %#v", account)
	}
	externalFound := false
	for _, account := range plugin.CatalogSnapshot() {
		if account.ID == "auth-external" && account.Disabled {
			externalFound = true
			break
		}
	}
	if !externalFound {
		t.Fatal("external account should remain disabled in the catalog")
	}
}

func TestManualProbeSkipsExternallyDisabledAccount(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-external", "[EMAIL]", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		plugin.ensureStateAccount(state, "auth-external").State = appstate.StateExternallyDisabled
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	response := actionManagementForTest(t, plugin, "accounts.probe", map[string]any{"auth_id": "auth-external", "force": true})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("manual probe status = %d body=%s", response.StatusCode, response.Body)
	}
	account, exists := plugin.stateAccount("auth-external")
	if !exists || account.State != appstate.StateExternallyDisabled {
		t.Fatalf("externally disabled state changed during manual probe: %#v", account)
	}
	host.mu.Lock()
	modelCalls := len(host.modelCalls)
	saves := len(host.saves)
	host.mu.Unlock()
	if modelCalls != 0 {
		t.Fatalf("externally disabled account triggered %d model calls", modelCalls)
	}
	if saves != 0 {
		t.Fatalf("externally disabled auth was rewritten %d times", saves)
	}
}
func TestProbeSelectedRestoresUsingExecutionTimeState(t *testing.T) {
	entriesFirst, authsFirst := singleGrokAuth(t, "auth-first", "[EMAIL]", nil)
	entriesSecond, authsSecond := singleGrokAuth(t, "auth-second", "[EMAIL]", nil)
	entries := append(entriesFirst, entriesSecond...)
	auths := make(map[string]json.RawMessage, len(authsFirst)+len(authsSecond))
	for key, value := range authsFirst {
		auths[key] = value
	}
	for key, value := range authsSecond {
		auths[key] = value
	}
	host := newMockHostCall(entries, auths)
	scriptHealthyProbeStream(t, host, "auth-first")
	scriptHealthyProbeStream(t, host, "auth-second")
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.Probe.Concurrency = 1
		cfg.Quality.SoftTPS = 1e9
		cfg.Quality.HardTPS = 2e9
	})
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		plugin.ensureStateAccount(state, "auth-first").State = appstate.StateSuspect
		plugin.ensureStateAccount(state, "auth-second").State = appstate.StateSuspect
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	entered := make(chan string, 2)
	release := make(chan struct{})
	installStreamHold(entered, release)
	body, errMarshal := json.Marshal(managementapp.ActionRequest{Action: "accounts.probe_selected", Params: map[string]any{
		"auth_ids": []string{"auth-first", "auth-second"},
	}})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", http.Header{"Content-Type": []string{"application/json"}}, body)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("probe batch status = %d body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data apptasks.Task `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &envelope); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	select {
	case authID := <-entered:
		if authID != "auth-first" {
			t.Fatalf("first executed auth = %q, want auth-first", authID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first selected probe did not start")
	}
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		plugin.ensureStateAccount(state, "auth-second").State = appstate.StateDisabledByGuard
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	close(release)

	task := waitTaskForTest(t, plugin, envelope.Data.ID)
	if task.Status != taskStatusSucceeded {
		t.Fatalf("probe batch task failed: %#v", task)
	}
	encodedResult, errMarshalResult := json.Marshal(task.Result)
	if errMarshalResult != nil {
		t.Fatal(errMarshalResult)
	}
	var result managementapp.ProbeBatchResult
	if errUnmarshal := json.Unmarshal(encodedResult, &result); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if result.Probed != 2 || result.Restored != 1 || result.Failed != 0 {
		t.Fatalf("unexpected probe summary: %#v", result)
	}
	statuses := make(map[string]managementapp.ProbeBatchItem, len(result.Items))
	for _, item := range result.Items {
		statuses[item.AuthID] = item
	}
	second := statuses["auth-second"]
	if second.StateBefore != appstate.StateDisabledByGuard || !second.Restored || second.Status != "probed" {
		t.Fatalf("second account did not use execution-time restore decision: %#v", second)
	}
}

func TestSelectedBatchActionsRejectInvalidAuthIDs(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	for _, action := range []string{"accounts.probe_selected", "accounts.restore_selected"} {
		empty := actionManagementForTest(t, plugin, action, nil)
		if empty.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s without auth_ids status = %d body=%s", action, empty.StatusCode, empty.Body)
		}
		unknown := actionManagementForTest(t, plugin, action, map[string]any{"auth_ids": []string{"missing"}})
		if unknown.StatusCode != http.StatusNotFound {
			t.Fatalf("%s unknown auth status = %d body=%s", action, unknown.StatusCode, unknown.Body)
		}
	}
}

func TestSanitizeErrorRedactsSecrets(t *testing.T) {
	input := errors.New(`Authorization: Bearer bearer-secret proxy=socks5h://user:proxy-pass@example.com:1080 "access_token":"access-secret" ?token=query-secret`)
	output := appredact.SanitizeError(input)
	for _, forbidden := range []string{"bearer-secret", "proxy-pass", "access-secret", "query-secret"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("sanitized error leaked %q: %s", forbidden, output)
		}
	}
	if !strings.Contains(output, "[redacted]") {
		t.Fatalf("sanitized error did not mark redactions: %s", output)
	}
}

func TestManagementUnknownActionReturnsBadRequest(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	response := actionManagementForTest(t, plugin, "does.not.exist", nil)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown action status = %d body=%s", response.StatusCode, response.Body)
	}
}

func TestManagementActionDoesNotRequireUIHeader(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	body, errMarshal := json.Marshal(managementapp.ActionRequest{Action: "overview"})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	response := callManagementForTest(t, plugin, http.MethodPost, "/plugins/grok-quality-guard/api", http.Header{"Content-Type": []string{"application/json"}}, body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("unmarked request status = %d body=%s", response.StatusCode, response.Body)
	}
}
func TestManagementTaskCancelAction(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	started := make(chan struct{})
	task := plugin.startTask("test.cancel", nil, "running", func(ctx context.Context) (any, error) {
		close(started)
		<-ctx.Done()
		return map[string]any{"partial": true}, ctx.Err()
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not start")
	}
	response := actionManagementForTest(t, plugin, "tasks.cancel", map[string]any{"task_id": task.ID})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("cancel task status = %d body=%s", response.StatusCode, response.Body)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := plugin.taskByID(task.ID)
		if !ok {
			t.Fatal("task disappeared")
		}
		if current.Status == "cancelled" {
			if result, okResult := current.Result.(map[string]any); !okResult || result["partial"] != true {
				t.Fatalf("cancelled task result = %#v", current.Result)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, _ := plugin.taskByID(task.ID)
	if current.Status != "cancelled" {
		t.Fatalf("task did not cancel: %#v", current)
	}
	if repeated := actionManagementForTest(t, plugin, "tasks.cancel", map[string]any{"task_id": task.ID}); repeated.StatusCode != http.StatusConflict {
		t.Fatalf("repeat cancel status = %d body=%s", repeated.StatusCode, repeated.Body)
	}
	if missing := actionManagementForTest(t, plugin, "tasks.cancel", map[string]any{"task_id": "missing"}); missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing cancel status = %d body=%s", missing.StatusCode, missing.Body)
	}
}
func TestEventsListReturnsSanitizedInternalErrorDiagnostic(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	diagnostic := "read tcp [IP]:443: i/o timeout Authorization: Bearer bearer-secret"
	if errRecord := plugin.RecordInternalError("auth-1", errors.New(diagnostic)); errRecord != nil {
		t.Fatal(errRecord)
	}
	stored := plugin.stateStore().EventsSnapshot()
	if len(stored) != 1 || !strings.Contains(stored[0].Detail, "i/o timeout") {
		t.Fatalf("stored internal error count=%d", len(stored))
	}
	if stored[0].DetailKey != "event.detail.internal_error" {
		t.Fatalf("stored detail key = %q", stored[0].DetailKey)
	}

	response := actionManagementForTest(t, plugin, "events.list", map[string]any{})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("events.list status = %d body=%s", response.StatusCode, response.Body)
	}
	var decoded struct {
		Data struct {
			Items []managementapp.Event `json:"items"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(response.Body, &decoded); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	var internal *managementapp.Event
	for index := range decoded.Data.Items {
		if decoded.Data.Items[index].Action == "internal_error" && strings.Contains(decoded.Data.Items[index].DetailRaw, "i/o timeout") {
			internal = &decoded.Data.Items[index]
			break
		}
	}
	if internal == nil {
		for _, item := range decoded.Data.Items {
			t.Logf("event action=%q detail_present=%v detail_raw_present=%v detail_raw_len=%d", item.Action, item.Detail != "", item.DetailRaw != "", len(item.DetailRaw))
		}
		t.Fatalf("events.list did not return the internal error diagnostic")
	}
	if strings.Contains(internal.DetailRaw, "bearer-secret") {
		t.Fatalf("detail_raw leaked a credential: %q", internal.DetailRaw)
	}
	if !strings.Contains(internal.DetailRaw, "read tcp [IP]:443: i/o timeout") {
		t.Fatalf("detail_raw = %q", internal.DetailRaw)
	}
	if internal.Detail == internal.DetailRaw || !strings.Contains(internal.Detail, "请查看日志") {
		t.Fatalf("detail = %q, detail_raw = %q", internal.Detail, internal.DetailRaw)
	}
}
