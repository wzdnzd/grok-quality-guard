// Proxy integration tests cover source selection, binding, recovery and scale.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appauth "grok-quality-guard/internal/app/auth"
	appmodel "grok-quality-guard/internal/app/model"
	proxyapp "grok-quality-guard/internal/app/proxy"
	statepkg "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func BenchmarkProxySourceSelection(b *testing.B) {
	for _, accounts := range []int{100, 10000, 100000} {
		b.Run(fmt.Sprint(accounts), func(b *testing.B) {
			store := statepkg.NewStore("")
			store.SetStateForTest(statepkg.State{Accounts: make(map[string]*statepkg.AccountState, accounts)})
			store.UpdateStateForTest(func(state *statepkg.State) {
				for i := 0; i < 10; i++ {
					state.Proxy.Nodes = append(state.Proxy.Nodes, proxy.Node{ID: fmt.Sprint(i), Name: fmt.Sprint(i), Enabled: true, Revision: 1, ConfigVersion: 1, Endpoint: proxy.Endpoint{Protocol: proxy.ProtocolHTTP, Host: "proxy.example", Port: 8000 + i}})
				}
				for i := 0; i < accounts; i++ {
					id := fmt.Sprint(i)
					state.Accounts[id] = &statepkg.AccountState{AuthID: id, Proxy: proxy.Binding{SourceID: fmt.Sprint(i % 10), SourceKind: proxy.SourceNode}}
				}
			})
			store.RebuildProxyIndexesForTest()
			plugin := NewPlugin()
			plugin.store = store
			plugin.proxyCatalog()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				source, _ := plugin.selectProxySource(proxyapp.BindOptions{SourceScope: "nodes", DryRun: true}, "")
				if source == nil {
					b.Fatal("source missing")
				}
			}
		})
	}
}

func TestProxyNodeCRUDBindAndClearPreservesAuth(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", map[string]any{"disabled": true})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://user:secret@proxy.example:8080", Name: "primary"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	result, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{})
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
	if appauth.RawString(raw, "proxy_url") != "http://user:secret@proxy.example:8080" {
		t.Fatalf("unexpected proxy_url: %#v", appauth.RawString(raw, "proxy_url"))
	}
	if appauth.RawString(raw, "access_token") != "access-auth-1" || appauth.RawString(raw, "refresh_token") != "refresh-auth-1" {
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
	cleared, errClear := plugin.clearProxyWithPolicy(context.Background(), "auth-1", proxyapp.BindOptions{})
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
	if strings.TrimSpace(appauth.RawString(raw, "proxy_url")) != "" {
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
func TestProxyBindingEndpointRevealIsOnDemand(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, err := plugin.createProxyNode(proxyapp.NodeInput{URL: "socks5h://user:secret@proxy.example:8080", Name: "reveal"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{}); err != nil {
		t.Fatal(err)
	}
	response := actionManagementForTest(t, plugin, "proxy.bindings.endpoint", map[string]any{"auth_id": "auth-1"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.StatusCode, response.Body)
	}
	var decoded struct {
		Data struct {
			AuthID    string `json:"auth_id"`
			Proxy     string `json:"proxy"`
			ProxyFull string `json:"proxy_full"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Data.AuthID != "auth-1" || decoded.Data.ProxyFull != "socks5h://user:secret@proxy.example:8080" {
		t.Fatalf("unexpected endpoint response: %#v", decoded.Data)
	}
	if decoded.Data.Proxy != "socks5h://proxy.example:8080" || strings.Contains(decoded.Data.Proxy, "secret") {
		t.Fatalf("masked endpoint leaked credentials: %#v", decoded.Data)
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
	node1, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@one.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	node2, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@two.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-2", proxyapp.BindOptions{}); errBind != nil {
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
	input := proxyapp.TemplateInput{
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
	result, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{SourceIDs: []string{template.ID}})
	if errBind != nil {
		t.Fatal(errBind)
	}
	if result.SourceKind != proxy.SourceTemplate || result.Status != "bound" {
		t.Fatalf("unexpected result: %#v", result)
	}
	raw, _ := rawAuthToMap(host.saves["auth-1"])
	proxyURL := appauth.RawString(raw, "proxy_url")
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
	if _, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@proxy.example:8080"}); errCreate != nil {
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
	healthy, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@healthy.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@unknown.example:8080"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	if errState := plugin.stateStore().Update(func(state *statepkg.State) error {
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
	template, errCreate := plugin.createProxyTemplate(proxyapp.TemplateInput{
		Name: "region", URL: "http://user-{region}:secret@proxy.example:8080",
		Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us"}}},
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{SourceIDs: []string{template.ID}}); errBind != nil {
		t.Fatal(errBind)
	}
	before := append(json.RawMessage(nil), host.saves["auth-1"]...)
	updated, result, errUpdate := plugin.updateProxyTemplate(proxyapp.TemplateInput{
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
	_, result, errUpdate = plugin.updateProxyTemplate(proxyapp.TemplateInput{
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
	if !strings.Contains(appauth.RawString(raw, "proxy_url"), "user-ca") {
		t.Fatalf("bound auth was not rerendered: %q", appauth.RawString(raw, "proxy_url"))
	}
	account, _ := plugin.stateAccount("auth-1")
	if account.Proxy.SourceRevision != updated.Revision+1 && !strings.Contains(appauth.RawString(raw, "proxy_url"), "user-ca") {
		t.Fatalf("rerendered binding did not track the updated revision: %#v", account.Proxy)
	}
}

func TestProxyManualClearPolicyAndPerCallOverride(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@proxy.example:8080"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, _, errConfig := plugin.updateProxyAutoBind(map[string]any{"manual_clear_policy": "pause"}); errConfig != nil {
		t.Fatal(errConfig)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	if _, errClear := plugin.clearProxyWithPolicy(context.Background(), "auth-1", proxyapp.BindOptions{}); errClear != nil {
		t.Fatal(errClear)
	}
	account, _ := plugin.stateAccount("auth-1")
	if !account.AutoBind.Excluded {
		t.Fatal("manual_clear_policy=pause did not exclude the account")
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	allow := false
	if _, errClear := plugin.clearProxyWithPolicy(context.Background(), "auth-1", proxyapp.BindOptions{ExcludeAutoBind: &allow}); errClear != nil {
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
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@proxy.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{}); errBind != nil {
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
	account := appmodel.Account{ID: "auth-1", Type: "xai", Provider: "xai", Email: "user@example.com"}
	summary := plugin.accountSummary(account, statepkg.AccountState{AuthID: "auth-1"}, statepkg.ResinSettings{
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
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://private-user:private-pass@proxy.example:8080"})
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
	template, errCreate := plugin.createProxyTemplate(proxyapp.TemplateInput{
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

func TestProxyImportAllowsMoreThanLegacyTenThousandNodes(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	nodes := make([]any, 10001)
	for index := range nodes {
		nodes[index] = fmt.Sprintf("http://u:p@proxy-%d.example:8080", index)
	}
	value, status, errImport := plugin.actionProxyImportNodes(map[string]any{"nodes": nodes})
	if errImport != nil || status != 200 {
		t.Fatalf("import failed: status=%d err=%v", status, errImport)
	}
	result, ok := value.(map[string]any)
	if !ok || result["created"] != 10001 {
		t.Fatalf("unexpected import result: %#v", value)
	}
}
func TestProxyNodeUpdateCanRetrySyncWithSameEndpoint(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@one.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errBind := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{}); errBind != nil {
		t.Fatal(errBind)
	}
	host.saveErr = errors.New("host save unavailable")
	updated, first, errUpdate := plugin.updateProxyNode(proxyapp.NodeInput{ID: node.ID, URL: "http://u:p@two.example:8080"})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	items, _ := first["items"].([]proxyapp.BindingResult)
	if len(items) != 1 || items[0].Status == "succeeded" {
		t.Fatalf("expected initial sync failure, got %#v", first["items"])
	}
	host.saveErr = nil
	_, second, errUpdate := plugin.updateProxyNode(proxyapp.NodeInput{ID: node.ID, URL: updated.Endpoint.CanonicalURL()})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if second["endpoint_changed"] != false || second["sync_requested"] != true {
		t.Fatalf("same-endpoint retry did not request sync: %#v", second)
	}
	raw, _ := rawAuthToMap(host.saves["auth-1"])
	if appauth.RawString(raw, "proxy_url") != updated.Endpoint.CanonicalURL() {
		t.Fatalf("retry did not synchronize auth proxy_url: %q", appauth.RawString(raw, "proxy_url"))
	}
}

func TestStateMigrationV1MovesResinBindingToGenericProxy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := statepkg.State{
		SchemaVersion: 1,
		UpdatedAt:     time.Now().UTC(),
		Accounts: map[string]*statepkg.AccountState{
			"auth-1": {
				AuthID: "auth-1",
				State:  statepkg.StateUnknown,
				Resin:  statepkg.ResinState{Managed: true, Account: "grok-old", Generation: 3},
			},
		},
		Events: []statepkg.Event{},
	}
	encoded, errMarshal := json.Marshal(legacy)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if errWrite := os.WriteFile(path, encoded, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := statepkg.NewStore(path)
	if errLoad := store.Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "test-revision"); errLoad != nil {
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
	var decoded statepkg.State
	if errUnmarshal := json.Unmarshal(persisted, &decoded); errUnmarshal != nil || decoded.SchemaVersion != 2 {
		t.Fatalf("migrated schema was not persisted: err=%v schema=%d", errUnmarshal, decoded.SchemaVersion)
	}
}

func TestProxySourceUpdateCanResetPriorityAndQuotaToZero(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@proxy.example:8080", Priority: 10, MaxBindings: 5})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	updated, _, errUpdate := plugin.updateProxyNode(proxyapp.NodeInput{ID: node.ID, Priority: 0, MaxBindings: 0, PrioritySet: true, MaxBindingsSet: true})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if updated.Priority != 0 || updated.MaxBindings != 0 {
		t.Fatalf("priority/quota reset failed: %#v", updated)
	}
	template, errCreate := plugin.createProxyTemplate(proxyapp.TemplateInput{
		Name: "region", URL: "http://user-{region}:secret@proxy.example:8080", Priority: 8, MaxBindings: 3,
		Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us"}}},
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	updatedTemplate, _, errUpdate := plugin.updateProxyTemplate(proxyapp.TemplateInput{ID: template.ID, Priority: 0, MaxBindings: 0, PrioritySet: true, MaxBindingsSet: true})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if updatedTemplate.Priority != 0 || updatedTemplate.MaxBindings != 0 {
		t.Fatalf("template priority/quota reset failed: %#v", updatedTemplate)
	}
}

func TestProxyFinalizeFailureRetainsQuotaAndRecoversFromJournal(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "first@example.com", nil)
	second, more := singleGrokAuth(t, "auth-2", "second@example.com", nil)
	entries = append(entries, second...)
	for id, raw := range more {
		auths[id] = raw
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	node, err := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://user:secret@proxy.example:8080", MaxBindings: 1})
	if err != nil {
		t.Fatal(err)
	}
	store := plugin.stateStore()
	path := store.Path()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := hostCall
	defer func() { hostCall = original }()
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		raw, err := original(method, payload)
		if method == pluginabi.MethodHostAuthSave && err == nil {
			store.SetPathForTest(filepath.Join(blocker, "state.json"))
		}
		return raw, err
	}
	result, err := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{OperationID: "recover-me"})
	if err == nil || !result.Committed || !result.NeedsReconcile {
		t.Fatalf("committed result lost: result=%#v err=%v", result, err)
	}
	hostCall = original
	other, err := plugin.bindProxy(context.Background(), "auth-2", proxyapp.BindOptions{})
	if err != nil || other.Status != "unavailable" {
		t.Fatalf("pending assignment must occupy quota: %#v %v", other, err)
	}
	store.StopFlushTimerForTest()
	store.SetPathForTest(path)
	reloaded := statepkg.NewStore(path)
	if err := reloaded.Load(plugin.policy, "test"); err != nil {
		t.Fatal(err)
	}
	if reloaded.ProxyPending("auth-1") != "recover-me" || reloaded.ProxyBindingCount(node.ID) != 0 {
		t.Fatal("prepared intent was not replayed")
	}
	plugin.mu.Lock()
	plugin.store = reloaded
	plugin.mu.Unlock()
	plugin.reconcileProxyOperations(context.Background())
	if reloaded.ProxyPending("auth-1") != "" || reloaded.ProxyBindingCount(node.ID) != 1 {
		t.Fatal("recovery did not finalize the existing target")
	}
	host.mu.Lock()
	saves := len(host.saves)
	host.mu.Unlock()
	if saves != 1 {
		t.Fatalf("recovery rewrote auth: saves=%d", saves)
	}
}

func TestProxyLostHostReceiptConfirmsActualCommit(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, err := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://proxy.example:8080"}); err != nil {
		t.Fatal(err)
	}
	original := hostCall
	defer func() { hostCall = original }()
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		raw, err := original(method, payload)
		if method == pluginabi.MethodHostAuthSave && err == nil {
			return json.RawMessage("broken receipt"), nil
		}
		return raw, err
	}
	result, err := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{})
	if err != nil || !result.Committed || result.NeedsReconcile {
		t.Fatalf("actual commit was not recognized: %#v %v", result, err)
	}
}

func TestProxyAutoBindDisabledAfterPrepareStopsAuthWrite(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, err := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://proxy.example:8080"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := plugin.updateProxyAutoBind(map[string]any{"enabled": true}); err != nil {
		t.Fatal(err)
	}
	original := hostCall
	defer func() { hostCall = original }()
	gets := 0
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		raw, err := original(method, payload)
		if method == pluginabi.MethodHostAuthGet {
			gets++
			if gets == 2 {
				if _, _, err := plugin.updateProxyAutoBind(map[string]any{"enabled": false}); err != nil {
					t.Fatal(err)
				}
			}
		}
		return raw, err
	}
	_, err := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{Auto: true})
	if err == nil || len(host.saves) != 0 || plugin.stateStore().ProxyPending("auth-1") != "" {
		t.Fatalf("disabled automatic operation wrote auth or remained pending: %v", err)
	}
}

func TestProxyRetryIdAndDryRunPreserveOriginalTarget(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, err := plugin.createProxyTemplate(proxyapp.TemplateInput{Name: "sessions", URL: "http://user-{session}:secret@proxy.example:8080", Placeholders: []proxy.Placeholder{{Name: "session", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 12}}}}); err != nil {
		t.Fatal(err)
	}
	host.saveErr = errors.New("save unavailable")
	if _, err := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{OperationID: "same-target"}); err == nil {
		t.Fatal("expected host failure")
	}
	prepared, ok := plugin.stateStore().ProxyOperation("same-target")
	if !ok {
		t.Fatal("failed target not retained")
	}
	host.saveErr = nil
	if result, err := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{OperationID: "same-target", DryRun: true}); err != nil || result.Status != "dry_run" {
		t.Fatalf("preview failed: %#v %v", result, err)
	}
	if len(host.saves) != 0 {
		t.Fatal("dry run resumed an auth write")
	}
	if _, err := plugin.clearProxy(context.Background(), "auth-1", proxyapp.BindOptions{OperationID: "same-target"}); err == nil {
		t.Fatal("operation id was reused for another action")
	}
	if _, err := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{OperationID: "same-target"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := rawAuthToMap(host.saves["auth-1"])
	if appauth.RawString(raw, "proxy_url") != prepared.DesiredURL {
		t.Fatal("retry generated a different session")
	}
}

func TestProxyNodeRotationDefaultsToNodeScope(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	one, _ := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://one.example:8080"})
	two, _ := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://two.example:8080"})
	if _, err := plugin.bindProxy(context.Background(), "auth-1", proxyapp.BindOptions{SourceIDs: []string{one.ID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.createProxyTemplate(proxyapp.TemplateInput{Name: "higher priority", URL: "http://user@template.example:8080", Priority: 100}); err != nil {
		t.Fatal(err)
	}
	opts, err := proxyBindOptionsFromParams(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := plugin.rotateProxy(context.Background(), "auth-1", opts)
	if err != nil || result.SourceID != two.ID || result.SourceKind != proxy.SourceNode {
		t.Fatalf("implicit cross-provider rotation: %#v %v", result, err)
	}
}

func TestProxyAuthenticationCannotDisappearDuringPartialEdit(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "a@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	node, err := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://user:secret@proxy.example:8080"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = plugin.updateProxyNode(proxyapp.NodeInput{ID: node.ID, UsernameSet: true, Username: "", PasswordUpdate: &proxyapp.PasswordUpdate{Action: "keep"}})
	if err == nil {
		t.Fatal("empty username silently discarded retained password")
	}
	current, _ := plugin.proxyExistingNode(node.ID)
	if current.Endpoint.Fingerprint() != node.Endpoint.Fingerprint() {
		t.Fatal("invalid edit was saved")
	}
}

func TestAuthProxyMetadataFailuresRemainUnknown(t *testing.T) {
	for _, content := range []string{"null", "{", `{"proxy_url":123}`} {
		path := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, known := readAuthProxyURL(path); known {
			t.Fatalf("invalid metadata was treated as empty: %s", content)
		}
	}
	if _, err := rawAuthToMap(json.RawMessage("null")); err == nil {
		t.Fatal("null auth accepted as writable object")
	}
}

func TestProxyJournalTailRecoveryAndCheckpointProtection(t *testing.T) {
	store := statepkg.NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "test"); err != nil {
		t.Fatal(err)
	}
	op := proxy.Operation{ID: "prepared", AuthID: "auth", Action: "proxy_bind", Status: "prepared", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CommitProxy(statepkg.ProxyCommit{Operation: &op}); err != nil {
		t.Fatal(err)
	}
	store.StopFlushTimerForTest()
	path := store.Path() + ".proxy-journal"
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("incomplete"); err != nil {
		t.Fatal(err)
	}
	file.Close()
	reloaded := statepkg.NewStore(store.Path())
	if err := reloaded.Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "test"); err != nil {
		t.Fatal(err)
	}
	if reloaded.ProxyPending("auth") != "prepared" {
		t.Fatal("complete journal record was lost")
	}
	contents, _ := os.ReadFile(path)
	if strings.Contains(string(contents), "incomplete") {
		t.Fatal("incomplete tail was not removed")
	}
	if err := os.Remove(store.Path()); err != nil {
		t.Fatal(err)
	}
	if err := statepkg.NewStore(store.Path()).Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "test"); err == nil {
		t.Fatal("missing base checkpoint silently discarded configuration")
	}
}

func TestProxyHistoryGCKeepsReferencedVersionsAndOrder(t *testing.T) {
	state := statepkg.State{Accounts: map[string]*statepkg.AccountState{"auth": {Proxy: proxy.Binding{SourceID: "template", SourceRevision: 1}}}, Proxy: proxy.ProxyState{TemplateVersions: []proxy.Template{{ID: "template", Revision: 1}, {ID: "template", Revision: 2}}}}
	for i := 0; i < 105; i++ {
		state.Proxy.Changes = append(state.Proxy.Changes, proxy.SourceChange{ID: fmt.Sprint(i), Status: "succeeded"})
	}
	statepkg.GCProxyHistory(&state)
	if len(state.Proxy.TemplateVersions) != 1 || state.Proxy.TemplateVersions[0].Revision != 1 {
		t.Fatal("referenced template version was collected")
	}
	if len(state.Proxy.Changes) != 100 || state.Proxy.Changes[0].ID != "5" || state.Proxy.Changes[99].ID != "104" {
		t.Fatal("completed history order was corrupted")
	}
}

func TestProxyChangeProgressTracksCommittedItems(t *testing.T) {
	store := statepkg.NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "test"); err != nil {
		t.Fatal(err)
	}
	defer store.Flush()
	if err := store.Update(func(state *statepkg.State) error {
		state.Proxy.Changes = []proxy.SourceChange{{ID: "progress", Status: "running", Counts: proxy.ChangeCounts{Total: 2, Pending: 2}, Items: []proxy.ChangeItem{{AuthID: "one", Status: "pending"}, {AuthID: "two", Status: "pending"}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	item := proxy.ChangeItem{AuthID: "one", Status: "succeeded"}
	if err := store.CommitProxy(statepkg.ProxyCommit{ChangeID: "progress", ChangeItem: &item}); err != nil {
		t.Fatal(err)
	}
	change, _ := store.ProxyChange("progress")
	if change.Counts.Succeeded != 1 || change.Counts.Pending != 1 {
		t.Fatalf("live progress not updated: %#v", change.Counts)
	}
}
