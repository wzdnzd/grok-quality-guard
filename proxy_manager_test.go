package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	proxy "grok-quality-guard/internal/proxy"
)

func TestProxyNodeCRUDBindAndClearPreservesAuth(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	node, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://user:secret@proxy.example:8080", Name: "primary"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	result, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{})
	if errBind != nil {
		t.Fatal(errBind)
	}
	if result.Status != "bound" || result.SourceID != node.ID || !result.Committed {
		t.Fatalf("unexpected bind result: %#v", result)
	}
	raw, errDecode := rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if authRawString(raw, "proxy_url") != "http://user:secret@proxy.example:8080" {
		t.Fatalf("unexpected proxy_url: %#v", authRawString(raw, "proxy_url"))
	}
	if authRawString(raw, "access_token") != "access-auth-1" || authRawString(raw, "refresh_token") != "refresh-auth-1" {
		t.Fatal("auth tokens were not preserved")
	}
	var disabled bool
	if errUnmarshal := json.Unmarshal(raw["disabled"], &disabled); errUnmarshal != nil || !disabled {
		t.Fatal("auth disabled state was not preserved")
	}
	stateAccount, _ := plugin.stateAccount("auth-1")
	if stateAccount.Proxy.SourceID != node.ID || stateAccount.Proxy.SourceKind != proxy.SourceNode || stateAccount.Proxy.Generation != 1 {
		t.Fatalf("unexpected proxy binding: %#v", stateAccount.Proxy)
	}
	cleared, errClear := plugin.clearProxyWithPolicy(context.Background(), "auth-1", proxyBindOptions{})
	if errClear != nil {
		t.Fatal(errClear)
	}
	if cleared.Status != "cleared" || !cleared.Committed {
		t.Fatalf("unexpected clear result: %#v", cleared)
	}
	raw, errDecode = rawAuthToMap(host.saves["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if strings.TrimSpace(authRawString(raw, "proxy_url")) != "" {
		t.Fatalf("proxy_url was not removed: %#v", raw["proxy_url"])
	}
	stateAccount, _ = plugin.stateAccount("auth-1")
	if stateAccount.AutoBind.Excluded {
		t.Fatal("manual clear should allow auto bind by default")
	}
	if stateAccount.Proxy.SourceID != "" {
		t.Fatalf("binding was not removed: %#v", stateAccount.Proxy)
	}
}
func TestProxyNodeSelectionBalancesInitialBindings(t *testing.T) {
	entries := []pluginapi.HostAuthFileEntry{
		{ID: "auth-1", AuthIndex: "auth-1", Name: "auth-1", Type: "xai", Provider: "xai", Email: "a@example.com", Label: "a@example.com"},
		{ID: "auth-2", AuthIndex: "auth-2", Name: "auth-2", Type: "xai", Provider: "xai", Email: "b@example.com", Label: "b@example.com"},
	}
	auths := map[string]json.RawMessage{
		"auth-1": makeAuthJSON(t, map[string]any{"type": "xai", "email": "a@example.com", "access_token": "a", "refresh_token": "a"}),
		"auth-2": makeAuthJSON(t, map[string]any{"type": "xai", "email": "b@example.com", "access_token": "b", "refresh_token": "b"}),
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	node1, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://u:p@one.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	node2, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://u:p@two.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-2", proxyBindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	first, _ := plugin.stateAccount("auth-1")
	second, _ := plugin.stateAccount("auth-2")
	if first.Proxy.SourceID == second.Proxy.SourceID {
		t.Fatalf("expected balanced sources, got %s and %s", first.Proxy.SourceID, second.Proxy.SourceID)
	}
	seen := map[string]bool{first.Proxy.SourceID: true, second.Proxy.SourceID: true}
	if !seen[node1.ID] || !seen[node2.ID] {
		t.Fatalf("unexpected source ids: %#v", seen)
	}
}
func TestProxyTemplateCascadeBindingAndExplicitApply(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	input := proxyTemplateInput{
		Name: "cascade",
		URL:  "socks5h://vendor-{vendor}-plan-{plan}:secret@proxy.example:1080",
		Placeholders: []proxy.Placeholder{
			{Name: "vendor", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: "|"}, GroupID: "plans"},
			{Name: "plan", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: "|"}, GroupID: "plans"},
		},
		ValueGroups: []proxy.ValueGroup{{ID: "plans", Columns: []string{"vendor", "plan"}, Rows: [][]string{{"alpha", "basic"}, {"beta", "pro"}}}},
	}
	template, errCreate := plugin.createProxyTemplate(input)
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	result, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{SourceIDs: []string{template.ID}})
	if errBind != nil {
		t.Fatal(errBind)
	}
	if result.SourceKind != proxy.SourceTemplate || result.Status != "bound" {
		t.Fatalf("unexpected result: %#v", result)
	}
	raw, _ := rawAuthToMap(host.saves["auth-1"])
	proxyURL := authRawString(raw, "proxy_url")
	parsed, errParse := url.Parse(proxyURL)
	if errParse != nil {
		t.Fatal(errParse)
	}
	username := parsed.User.Username()
	if !(strings.Contains(username, "vendor-alpha-plan-basic") || strings.Contains(username, "vendor-beta-plan-pro")) {
		t.Fatalf("rendered template used an illegal combination: %q", username)
	}
	stateAccount, _ := plugin.stateAccount("auth-1")
	if len(stateAccount.Proxy.ResolvedValues) == 0 {
		t.Fatal("resolved values were not stored")
	}
	items := plugin.proxyApplyTemplateToAccounts(context.Background(), template, []string{"auth-1"})
	if len(items) != 1 {
		t.Fatalf("unexpected apply result: %#v", items)
	}
}
func TestProxyImportIgnoresMissingScheme(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	result, errImport := plugin.importProxyNodes(nil, "proxy.example:8080\nhttp://u:p@proxy.example:8081")
	if errImport != nil {
		t.Fatal(errImport)
	}
	if result["created"] != 1 || result["skipped"] != 1 {
		t.Fatalf("unexpected import result: %#v", result)
	}
	state := plugin.stateStore().Snapshot()
	if len(state.Proxy.Nodes) != 1 || state.Proxy.Nodes[0].Endpoint.Port != 8081 {
		t.Fatalf("unexpected nodes: %#v", state.Proxy.Nodes)
	}
}
func TestProxyAutoBindSkipsExternalAndBindsEmptyAuth(t *testing.T) {
	entries := []pluginapi.HostAuthFileEntry{
		{ID: "auth-1", AuthIndex: "auth-1", Name: "auth-1", Type: "xai", Provider: "xai", Email: "a@example.com", Label: "a@example.com"},
		{ID: "auth-2", AuthIndex: "auth-2", Name: "auth-2", Type: "xai", Provider: "xai", Email: "b@example.com", Label: "b@example.com"},
	}
	auths := map[string]json.RawMessage{
		"auth-1": makeAuthJSON(t, map[string]any{"type": "xai", "email": "a@example.com", "access_token": "a", "refresh_token": "a"}),
		"auth-2": makeAuthJSON(t, map[string]any{"type": "xai", "email": "b@example.com", "access_token": "b", "refresh_token": "b", "proxy_url": "http://external:secret@external.example:9000"}),
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://u:p@proxy.example:8080"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	enabled := true
	if _, _, errConfig := plugin.updateProxyAutoBind(map[string]any{"enabled": enabled, "scan_interval": "1s", "retry_initial": "1s", "retry_max": "2s"}); errConfig != nil {
		t.Fatal(errConfig)
	}
	plugin.proxyAutoBindScan(context.Background())
	first, _ := plugin.stateAccount("auth-1")
	second, _ := plugin.stateAccount("auth-2")
	if first.Proxy.SourceID == "" {
		t.Fatal("empty auth was not auto-bound")
	}
	if second.Proxy.SourceID != "" {
		t.Fatal("external auth was incorrectly adopted")
	}
	if _, saved := host.saves["auth-2"]; saved {
		t.Fatal("external proxy auth was saved")
	}
}

func TestProxyAutoBindRejectsUnknownSourceID(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	_, _, errUpdate := plugin.updateProxyAutoBind(map[string]any{"source_ids": []string{"missing-source"}})
	if errUpdate == nil || !strings.Contains(errUpdate.Error(), "unknown source") {
		t.Fatalf("expected unknown source validation error, got %v", errUpdate)
	}
}

func TestProxyAutoBindRequireHealthyUsesOnlyFreshHealthyNode(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	healthy, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://u:p@healthy.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://u:p@unknown.example:8080"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	if errState := plugin.stateStore().Update(func(state *State) error {
		for index := range state.Proxy.Nodes {
			if state.Proxy.Nodes[index].ID == healthy.ID {
				state.Proxy.Nodes[index].Health = proxy.HealthHealthy
				state.Proxy.Nodes[index].HealthChecked = time.Now().UTC()
			}
		}
		return nil
	}); errState != nil {
		t.Fatal(errState)
	}
	if _, _, errConfig := plugin.updateProxyAutoBind(map[string]any{
		"enabled": true, "source_scope": "nodes", "require_healthy": true, "health_max_age": "10m",
		"scan_interval": "1s", "retry_initial": "1s", "retry_max": "2s",
	}); errConfig != nil {
		t.Fatal(errConfig)
	}
	plugin.proxyAutoBindScan(context.Background())
	account, _ := plugin.stateAccount("auth-1")
	if account.Proxy.SourceID != healthy.ID {
		t.Fatalf("expected only healthy source %s, got %#v", healthy.ID, account.Proxy)
	}
}

func TestProxyTemplateUpdateKeepsBoundByDefaultAndRerendersWhenConfigured(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	template, errCreate := plugin.createProxyTemplate(proxyTemplateInput{
		Name: "region", URL: "http://user-{region}:secret@proxy.example:8080",
		Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us"}}},
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{SourceIDs: []string{template.ID}}); errBind != nil {
		t.Fatal(errBind)
	}
	before := append(json.RawMessage(nil), host.saves["auth-1"]...)
	updated, result, errUpdate := plugin.updateProxyTemplate(proxyTemplateInput{
		ID:           template.ID,
		Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "cn"}}},
	})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if result["rerender"] != false {
		t.Fatalf("default update unexpectedly rerendered: %#v", result)
	}
	if string(host.saves["auth-1"]) != string(before) {
		t.Fatal("keep_bound update changed the auth proxy_url")
	}
	_, result, errUpdate = plugin.updateProxyTemplate(proxyTemplateInput{
		ID: template.ID, UpdatePolicy: proxy.TemplateUpdateRerenderBound,
		Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "ca"}}},
	})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if result["rerender"] != true {
		t.Fatalf("rerender_bound update did not report rerender: %#v", result)
	}
	raw, _ := rawAuthToMap(host.saves["auth-1"])
	if !strings.Contains(authRawString(raw, "proxy_url"), "user-ca") {
		t.Fatalf("bound auth was not rerendered: %q", authRawString(raw, "proxy_url"))
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.Proxy.SourceRevision != updated.Revision+1 && !strings.Contains(authRawString(raw, "proxy_url"), "user-ca") {
		t.Fatalf("rerendered binding did not track the updated revision: %#v", account.Proxy)
	}
}

func TestProxyManualClearPolicyAndPerCallOverride(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://u:p@proxy.example:8080"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, _, errConfig := plugin.updateProxyAutoBind(map[string]any{"manual_clear_policy": "pause"}); errConfig != nil {
		t.Fatal(errConfig)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	if _, errClear := plugin.clearProxyWithPolicy(context.Background(), "auth-1", proxyBindOptions{}); errClear != nil {
		t.Fatal(errClear)
	}
	account, _ := plugin.stateAccount("auth-1")
	if !account.AutoBind.Excluded {
		t.Fatal("manual_clear_policy=pause did not exclude the account")
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	allow := false
	if _, errClear := plugin.clearProxyWithPolicy(context.Background(), "auth-1", proxyBindOptions{ExcludeAutoBind: &allow}); errClear != nil {
		t.Fatal(errClear)
	}
	account, _ = plugin.stateAccount("auth-1")
	if account.AutoBind.Excluded {
		t.Fatal("exclude_auto_bind=false did not override the pause policy")
	}
}

func TestProxySourceDeleteFailureRetainsSourceAndBinding(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	node, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://u:p@proxy.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	host.saveErr = errors.New("host save unavailable")
	if _, errDelete := plugin.deleteProxyNode(node.ID, true); errDelete == nil {
		t.Fatal("expected delete to fail while auth cleanup fails")
	}
	state := plugin.stateStore().Snapshot()
	found := false
	for _, candidate := range state.Proxy.Nodes {
		found = found || candidate.ID == node.ID
	}
	if !found {
		t.Fatal("source was deleted even though auth cleanup failed")
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.Proxy.SourceID != node.ID {
		t.Fatalf("binding metadata was lost after cleanup failure: %#v", account.Proxy)
	}
	host.saveErr = nil
	if _, errDelete := plugin.deleteProxyNode(node.ID, true); errDelete != nil {
		t.Fatal(errDelete)
	}
	state = plugin.stateStore().Snapshot()
	if len(state.Proxy.Nodes) != 0 {
		t.Fatalf("source was not deleted after auth cleanup succeeded: %#v", state.Proxy.Nodes)
	}
}

func TestAccountSummaryDoesNotAdoptExternalProxyAsResin(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	account := Account{ID: "auth-1", Type: "xai", Provider: "xai", Email: "user@example.com"}
	summary := plugin.accountSummary(account, AccountState{AuthID: "auth-1"}, ResinSettings{
		Platform: "Default", ProxyTemplate: "socks5h://{platform}.{account}:{proxy_token}@resin:2260",
	}, "socks5h://Default.external:secret@resin:2260")
	if summary.ResinManaged {
		t.Fatalf("external proxy was incorrectly adopted as Resin: %#v", summary)
	}
	if summary.ProxyStatus != "external" {
		t.Fatalf("external proxy must remain external, got %q", summary.ProxyStatus)
	}
}

func TestProxySourceDetailsRedactCredentials(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	node, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://private-user:private-pass@proxy.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	detail, errDetail := plugin.proxySourceDetails(node.ID)
	if errDetail != nil {
		t.Fatal(errDetail)
	}
	encoded, errMarshal := json.Marshal(detail)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if strings.Contains(string(encoded), "private-user") || strings.Contains(string(encoded), "private-pass") {
		t.Fatalf("source detail leaked proxy credentials: %s", encoded)
	}
}

func TestProxyTemplatePreviewSupportsEditingWithoutRetypingURL(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	template, errCreate := plugin.createProxyTemplate(proxyTemplateInput{
		Name: "region", URL: "http://user-{region}:secret@proxy.example:8080",
		Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us,cn"}}},
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	value, status, errPreview := plugin.actionProxySourcePreview(map[string]any{
		"source": map[string]any{
			"id": template.ID, "name": "region",
			"placeholders": []any{map[string]any{"name": "region", "kind": "enum", "enum": map[string]any{"separator": ",", "input": "us,cn"}}},
		},
		"count": 2,
	})
	if errPreview != nil || status != 200 {
		t.Fatalf("preview failed: status=%d err=%v", status, errPreview)
	}
	result, ok := value.(map[string]any)
	if !ok || result["valid"] != true {
		t.Fatalf("unexpected preview result: %#v", value)
	}
}

func TestProxyImportAcceptsMultipleNodeStrings(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	value, status, errImport := plugin.actionProxyImportNodes(map[string]any{
		"nodes": []any{"http://u:p@one.example:8080", "socks5h://u:p@two.example:1080"},
	})
	if errImport != nil || status != 200 {
		t.Fatalf("import failed: status=%d err=%v", status, errImport)
	}
	result, ok := value.(map[string]any)
	if !ok || result["created"] != 2 {
		t.Fatalf("unexpected import result: %#v", value)
	}
}

func TestProxyNodeUpdateCanRetrySyncWithSameEndpoint(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	node, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://u:p@one.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyBindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	host.saveErr = errors.New("host save unavailable")
	updated, first, errUpdate := plugin.updateProxyNode(proxyNodeInput{ID: node.ID, URL: "http://u:p@two.example:8080"})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	items, _ := first["items"].([]proxyBindingResult)
	if len(items) != 1 || items[0].Status == "succeeded" {
		t.Fatalf("expected initial sync failure, got %#v", first["items"])
	}
	host.saveErr = nil
	_, second, errUpdate := plugin.updateProxyNode(proxyNodeInput{ID: node.ID, URL: updated.Endpoint.CanonicalURL()})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if second["endpoint_changed"] != false || second["sync_requested"] != true {
		t.Fatalf("same-endpoint retry did not request sync: %#v", second)
	}
	raw, _ := rawAuthToMap(host.saves["auth-1"])
	if authRawString(raw, "proxy_url") != updated.Endpoint.CanonicalURL() {
		t.Fatalf("retry did not synchronize auth proxy_url: %q", authRawString(raw, "proxy_url"))
	}
}

func TestStateMigrationV1MovesResinBindingToGenericProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := State{
		SchemaVersion: 1,
		UpdatedAt:     time.Now().UTC(),
		Accounts: map[string]*AccountState{
			"auth-1": {
				AuthID: "auth-1",
				State:  StateUnknown,
				Resin:  ResinState{Managed: true, Account: "grok-old", Generation: 3},
			},
		},
		Events: []Event{},
	}
	encoded, errMarshal := json.Marshal(legacy)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, encoded, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := NewStateStore(path)
	if errLoad := store.Load(PolicyFromConfig(DefaultConfig()), "test-revision"); errLoad != nil {
		t.Fatal(errLoad)
	}
	snapshot := store.Snapshot()
	if snapshot.SchemaVersion != 2 {
		t.Fatalf("state schema was not migrated: %d", snapshot.SchemaVersion)
	}
	account := snapshot.Accounts["auth-1"]
	if account == nil || account.Proxy.SourceID != resinSourceID || account.Proxy.SourceKind != proxy.SourceResin || account.Proxy.Lease == nil || account.Proxy.Lease.Account != "grok-old" {
		t.Fatalf("legacy Resin binding was not migrated: %#v", account)
	}
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	var decoded State
	if errUnmarshal := json.Unmarshal(persisted, &decoded); errUnmarshal != nil || decoded.SchemaVersion != 2 {
		t.Fatalf("migrated schema was not persisted: err=%v schema=%d", errUnmarshal, decoded.SchemaVersion)
	}
}

func TestProxySourceUpdateCanResetPriorityAndQuotaToZero(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	node, errCreate := plugin.createProxyNode(proxyNodeInput{URL: "http://u:p@proxy.example:8080", Priority: 10, MaxBindings: 5})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	updated, _, errUpdate := plugin.updateProxyNode(proxyNodeInput{ID: node.ID, Priority: 0, MaxBindings: 0, PrioritySet: true, MaxBindingsSet: true})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if updated.Priority != 0 || updated.MaxBindings != 0 {
		t.Fatalf("priority/quota reset failed: %#v", updated)
	}
	template, errCreate := plugin.createProxyTemplate(proxyTemplateInput{
		Name: "region", URL: "http://user-{region}:secret@proxy.example:8080", Priority: 8, MaxBindings: 3,
		Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us"}}},
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	updatedTemplate, _, errUpdate := plugin.updateProxyTemplate(proxyTemplateInput{ID: template.ID, Priority: 0, MaxBindings: 0, PrioritySet: true, MaxBindingsSet: true})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if updatedTemplate.Priority != 0 || updatedTemplate.MaxBindings != 0 {
		t.Fatalf("template priority/quota reset failed: %#v", updatedTemplate)
	}
}
