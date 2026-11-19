// Proxy tests cover source selection, binding, recovery, scale, failure rotation and resin lifecycle.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	appauth "grok-quality-guard/internal/app/auth"
	appmodel "grok-quality-guard/internal/app/model"
	proxyapp "grok-quality-guard/internal/app/proxy"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
)

func BenchmarkProxySourceSelection(b *testing.B) {
	for _, accounts := range []int{100, 10000, 100000} {
		b.Run(fmt.Sprint(accounts), func(b *testing.B) {
			store := appstate.NewStore("")
			store.SetStateForTest(appstate.State{Accounts: make(map[string]*appstate.AccountState, accounts)})
			store.UpdateStateForTest(func(state *appstate.State) {
				for i := 0; i < 10; i++ {
					state.Proxy.Nodes = append(state.Proxy.Nodes, proxy.Node{ID: fmt.Sprint(i), Name: fmt.Sprint(i), Enabled: true, Revision: 1, ConfigVersion: 1, Endpoint: proxy.Endpoint{Protocol: proxy.ProtocolHTTP, Host: "proxy.example", Port: 8000 + i}})
				}
				for i := 0; i < accounts; i++ {
					id := fmt.Sprint(i)
					state.Accounts[id] = &appstate.AccountState{AuthID: id, Proxy: proxy.Binding{SourceID: fmt.Sprint(i % 10), SourceKind: proxy.SourceNode}}
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
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "socks5h://user:secret@proxy.example:8080", Name: "reveal"})
	if errCreate != nil {
		t.Fatal(errCreate)
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
	if decoded.Data.Proxy != "socks5h://us***r:******@proxy.example:8080" || strings.Contains(decoded.Data.Proxy, "secret") {
		t.Fatalf("masked endpoint leaked credentials: %#v", decoded.Data)
	}

	response = actionManagementForTest(t, plugin, "proxy.sources.endpoint", map[string]any{"source_id": node.ID})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("source endpoint status = %d body=%s", response.StatusCode, response.Body)
	}
	var sourceDecoded struct {
		Data struct {
			SourceID  string `json:"source_id"`
			Proxy     string `json:"proxy"`
			ProxyFull string `json:"proxy_full"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body, &sourceDecoded); err != nil {
		t.Fatal(err)
	}
	if sourceDecoded.Data.SourceID != node.ID || sourceDecoded.Data.ProxyFull != "socks5h://user:secret@proxy.example:8080" {
		t.Fatalf("unexpected source endpoint response: %#v", sourceDecoded.Data)
	}
	if sourceDecoded.Data.Proxy != "socks5h://us***r:******@proxy.example:8080" {
		t.Fatalf("unexpected masked source endpoint: %#v", sourceDecoded.Data)
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

func TestProxyAutoBindSkipsOnlyAccountsInActiveManualTask(t *testing.T) {
	entries := []pluginapi.HostAuthFileEntry{
		{ID: "auth-1", AuthIndex: "auth-1", Name: "auth-1", Type: "xai", Provider: "xai", Email: "[EMAIL]", Label: "[EMAIL]"},
		{ID: "auth-2", AuthIndex: "auth-2", Name: "auth-2", Type: "xai", Provider: "xai", Email: "[EMAIL]", Label: "[EMAIL]"},
	}
	auths := map[string]json.RawMessage{
		"auth-1": makeAuthJSON(t, map[string]any{"type": "xai", "email": "[EMAIL]", "access_token": "a", "refresh_token": "a"}),
		"auth-2": makeAuthJSON(t, map[string]any{"type": "xai", "email": "[EMAIL]", "access_token": "b", "refresh_token": "b"}),
	}
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	if _, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@proxy.example:8080"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, _, errConfig := plugin.updateProxyAutoBind(map[string]any{"enabled": true, "scan_interval": "1s", "retry_initial": "1s", "retry_max": "2s"}); errConfig != nil {
		t.Fatal(errConfig)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	plugin.startTask("proxy.clear", []string{"auth-1"}, "manual clear in progress", func(ctx context.Context) (any, error) {
		close(started)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("manual task did not start")
	}

	plugin.proxyAutoBindScan(context.Background())
	first, _ := plugin.stateAccount("auth-1")
	second, _ := plugin.stateAccount("auth-2")
	if first.Proxy.SourceID != "" {
		t.Fatal("auto bind changed an account owned by the active manual task")
	}
	if second.Proxy.SourceID == "" {
		t.Fatal("auto bind was globally blocked by an unrelated account in the active manual task")
	}

	close(release)
	plugin.taskMgr.Wait()
	plugin.proxyAutoBindScan(context.Background())
	first, _ = plugin.stateAccount("auth-1")
	if first.Proxy.SourceID == "" {
		t.Fatal("auto bind did not resume for the account after the manual task finished")
	}
}

func TestProxyAutoBindWaitsForUnscopedManualBindingTask(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	if _, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@proxy.example:8080"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, _, errConfig := plugin.updateProxyAutoBind(map[string]any{"enabled": true, "scan_interval": "1s", "retry_initial": "1s", "retry_max": "2s"}); errConfig != nil {
		t.Fatal(errConfig)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	plugin.startTask("proxy.bind_unbound", nil, "manual bind in progress", func(ctx context.Context) (any, error) {
		close(started)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("manual task did not start")
	}

	plugin.proxyAutoBindScan(context.Background())
	account, _ := plugin.stateAccount("auth-1")
	if account.Proxy.SourceID != "" {
		t.Fatal("auto bind ran while a task without an account-scoped auth set was active")
	}

	close(release)
	plugin.taskMgr.Wait()
	plugin.proxyAutoBindScan(context.Background())
	account, _ = plugin.stateAccount("auth-1")
	if account.Proxy.SourceID == "" {
		t.Fatal("auto bind did not resume after the unscoped manual task finished")
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
	if errState := plugin.stateStore().Update(func(state *appstate.State) error {
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

func TestAccountSummaryDoesNotAdoptExternalProxyAsManaged(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	account := appmodel.Account{ID: "auth-1", Type: "xai", Provider: "xai", Email: "user@example.com"}
	summary := plugin.accountSummary(account, appstate.AccountState{AuthID: "auth-1"}, "socks5h://Default.external:secret@resin:2260")
	if summary.ProxyManaged {
		t.Fatalf("external proxy was incorrectly adopted as managed: %#v", summary)
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

func TestProxyEditFieldsRevealURLForEditor(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://private-user:private-pass@proxy.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	nodeResponse := actionManagementForTest(t, plugin, "proxy.sources.get", map[string]any{
		"source_id": node.ID, "edit_fields": true,
	})
	if nodeResponse.StatusCode != http.StatusOK {
		t.Fatalf("node edit detail status = %d body=%s", nodeResponse.StatusCode, nodeResponse.Body)
	}
	var nodeDetail struct {
		Data struct {
			EditURL string `json:"edit_url"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(nodeResponse.Body, &nodeDetail); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if nodeDetail.Data.EditURL != "http://private-user:private-pass@proxy.example:8080" {
		t.Fatalf("node edit_url = %q", nodeDetail.Data.EditURL)
	}

	template, errCreate := plugin.createProxyTemplate(proxyapp.TemplateInput{
		Name: "region", URL: "http://user-{region}:template-secret@proxy.example:8080",
		Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us,cn"}}},
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	templateResponse := actionManagementForTest(t, plugin, "proxy.sources.get", map[string]any{
		"source_id": template.ID, "edit_fields": true,
	})
	if templateResponse.StatusCode != http.StatusOK {
		t.Fatalf("template edit detail status = %d body=%s", templateResponse.StatusCode, templateResponse.Body)
	}
	var templateDetail struct {
		Data struct {
			EditURL string `json:"edit_url"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(templateResponse.Body, &templateDetail); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if templateDetail.Data.EditURL != "http://user-{region}:template-secret@proxy.example:8080" {
		t.Fatalf("template edit_url = %q", templateDetail.Data.EditURL)
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
	samples, ok := result["samples"].([]proxy.TemplateSample)
	if !ok || len(samples) != 2 {
		t.Fatalf("unexpected preview samples: %#v", result["samples"])
	}
	if !strings.Contains(samples[0].DisplayURL, ":******@proxy.example:8080") {
		t.Fatalf("preview display URL lost proxy shape: %q", samples[0].DisplayURL)
	}
	if strings.Contains(samples[0].MaskedURL, "user-") || strings.Contains(samples[0].MaskedURL, "******") {
		t.Fatalf("masked origin unexpectedly includes credentials: %q", samples[0].MaskedURL)
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

func TestProxyImportAcceptsNamedNodeObjects(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	value, status, errImport := plugin.actionProxyImportNodes(map[string]any{
		"nodes": []any{
			map[string]any{"name": "edge-a", "url": "http://u:p@one.example:8080", "enabled": true, "priority": -7, "max_bindings": 2},
			map[string]any{"name": "edge-b", "url": "socks5h://u:p@two.example:1080", "enabled": false, "priority": 4, "max_bindings": 0},
		},
	})
	if errImport != nil || status != 200 {
		t.Fatalf("import failed: status=%d err=%v", status, errImport)
	}
	result, ok := value.(map[string]any)
	if !ok || result["created"] != 2 {
		t.Fatalf("unexpected import result: %#v", value)
	}
	state := plugin.stateStore().Snapshot()
	if len(state.Proxy.Nodes) != 2 {
		t.Fatalf("unexpected nodes: %#v", state.Proxy.Nodes)
	}
	byName := make(map[string]proxy.Node, len(state.Proxy.Nodes))
	for _, node := range state.Proxy.Nodes {
		byName[node.Name] = node
	}
	first, second := byName["edge-a"], byName["edge-b"]
	if first.Priority != -7 || first.MaxBindings != 2 || !first.Enabled {
		t.Fatalf("unexpected first node: %#v", first)
	}
	if second.Priority != 4 || second.MaxBindings != 0 || second.Enabled {
		t.Fatalf("unexpected second node: %#v", second)
	}
}

func TestProxyImportAllowsMoreThanTenThousandNodes(t *testing.T) {
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
func TestProxySourceBatchActionsReportMissingAndDeleteSelected(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	response := actionManagementForTest(t, plugin, "proxy.sources.test_selected", map[string]any{
		"source_ids": []any{"missing-source"},
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("batch test status = %d body=%s", response.StatusCode, response.Body)
	}
	var testResult struct {
		Data struct {
			Counts map[string]int `json:"counts"`
			Items  []struct {
				SourceID string `json:"source_id"`
				Status   string `json:"status"`
				Error    string `json:"error"`
			} `json:"items"`
		} `json:"data"`
	}
	if errDecode := json.Unmarshal(response.Body, &testResult); errDecode != nil {
		t.Fatal(errDecode)
	}
	if testResult.Data.Counts["failed"] != 1 || len(testResult.Data.Items) != 1 || testResult.Data.Items[0].Status != "failed" || testResult.Data.Items[0].Error == "" {
		t.Fatalf("unexpected batch test result: %#v", testResult.Data)
	}

	first, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@one.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	second, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@two.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	response = actionManagementForTest(t, plugin, "proxy.sources.delete_selected", map[string]any{
		"source_ids": []any{first.ID, second.ID}, "force": true,
	})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("batch delete status = %d body=%s", response.StatusCode, response.Body)
	}
	var deleteResult struct {
		Data struct {
			Counts map[string]int `json:"counts"`
		} `json:"data"`
	}
	if errDecode := json.Unmarshal(response.Body, &deleteResult); errDecode != nil {
		t.Fatal(errDecode)
	}
	if deleteResult.Data.Counts["deleted"] != 2 || deleteResult.Data.Counts["failed"] != 0 {
		t.Fatalf("unexpected batch delete result: %#v", deleteResult.Data)
	}
	if state := plugin.stateStore().Snapshot(); len(state.Proxy.Nodes) != 0 {
		t.Fatalf("batch delete retained nodes: %#v", state.Proxy.Nodes)
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

func TestProxySourceUnchangedSaveIsRejected(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@node.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	enabled := node.Enabled
	_, _, errUpdate := plugin.updateProxyNode(proxyapp.NodeInput{
		ID: node.ID, Name: node.Name, Enabled: &enabled, Priority: node.Priority, MaxBindings: node.MaxBindings,
	})
	if errUpdate == nil || !strings.Contains(errUpdate.Error(), "api.error.proxy_source_unchanged") {
		t.Fatalf("unchanged node save was not rejected: %v", errUpdate)
	}
	currentNode, _ := plugin.proxyExistingNode(node.ID)
	if currentNode.ConfigVersion != node.ConfigVersion || currentNode.Revision != node.Revision {
		t.Fatalf("unchanged node advanced versions: before=%#v after=%#v", node, currentNode)
	}

	template, errCreate := plugin.createProxyTemplate(proxyapp.TemplateInput{
		Name: "region", URL: "http://user-{region}:secret@template.example:8080",
		Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us"}}},
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	enabled = template.Enabled
	_, _, errUpdate = plugin.updateProxyTemplate(proxyapp.TemplateInput{
		ID: template.ID, Name: template.Name, Enabled: &enabled, Priority: template.Priority,
		MaxBindings: template.MaxBindings, UpdatePolicy: template.UpdatePolicy,
	})
	if errUpdate == nil || !strings.Contains(errUpdate.Error(), "api.error.proxy_source_unchanged") {
		t.Fatalf("unchanged template save was not rejected: %v", errUpdate)
	}
	state := plugin.stateStore().Snapshot()
	for _, current := range state.Proxy.Templates {
		if current.ID != template.ID {
			continue
		}
		if current.ConfigVersion != template.ConfigVersion || current.Revision != template.Revision {
			t.Fatalf("unchanged template advanced versions: before=%#v after=%#v", template, current)
		}
		return
	}
	t.Fatal("template disappeared after unchanged save")
}

func TestProxyNodeRejectsDuplicateCanonicalEndpoint(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	if _, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "HTTP://u:p@Example.COM:8080/"}); errCreate != nil {
		t.Fatal(errCreate)
	}
	_, errDuplicate := plugin.createProxyNode(proxyapp.NodeInput{Name: "same endpoint", URL: "http://u:p@example.com:8080"})
	if errDuplicate == nil || !strings.Contains(errDuplicate.Error(), "api.error.proxy_duplicate") {
		t.Fatalf("duplicate node was not rejected: %v", errDuplicate)
	}
	state := plugin.stateStore().Snapshot()
	if len(state.Proxy.Nodes) != 1 {
		t.Fatalf("duplicate node was persisted: %#v", state.Proxy.Nodes)
	}
}

func TestProxyTemplateRejectsDuplicateDefinition(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	base := proxyapp.TemplateInput{
		Name: "base", URL: "http://user-{region}-{plan}:secret@template.example:8080",
		Placeholders: []proxy.Placeholder{
			{Name: "region", Kind: proxy.PlaceholderEnum, GroupID: "plans", Enum: &proxy.EnumSpec{Separator: ","}},
			{Name: "plan", Kind: proxy.PlaceholderEnum, GroupID: "plans", Enum: &proxy.EnumSpec{Separator: ","}},
		},
		ValueGroups: []proxy.ValueGroup{{ID: "plans", Name: "Plans", Columns: []string{"region", "plan"}, Rows: [][]string{{"us", "basic"}, {"cn", "pro"}}}},
	}
	first, errCreate := plugin.createProxyTemplate(base)
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	duplicate := base
	duplicate.Name = "duplicate name"
	duplicate.ValueGroups = []proxy.ValueGroup{{ID: "plans", Name: "Different display name", Columns: []string{"region", "plan"}, Rows: [][]string{{"us", "basic"}, {"cn", "pro"}}}}
	_, errDuplicate := plugin.createProxyTemplate(duplicate)
	if errDuplicate == nil || !strings.Contains(errDuplicate.Error(), "api.error.proxy_duplicate") {
		t.Fatalf("duplicate template was not rejected: %v", errDuplicate)
	}
	state := plugin.stateStore().Snapshot()
	if len(state.Proxy.Templates) != 1 {
		t.Fatalf("duplicate template was persisted: %#v", state.Proxy.Templates)
	}

	different := base
	different.Name = "different rows"
	different.ValueGroups = []proxy.ValueGroup{{ID: "plans", Columns: []string{"region", "plan"}, Rows: [][]string{{"us", "basic"}, {"cn", "enterprise"}}}}
	second, errCreate := plugin.createProxyTemplate(different)
	if errCreate != nil {
		t.Fatalf("different template definition was rejected: %v", errCreate)
	}
	_, _, errUpdate := plugin.updateProxyTemplate(proxyapp.TemplateInput{
		ID: second.ID, URL: base.URL, Placeholders: base.Placeholders, ValueGroups: base.ValueGroups,
	})
	if errUpdate == nil || !strings.Contains(errUpdate.Error(), "api.error.proxy_duplicate") {
		t.Fatalf("template update was allowed to create a duplicate: %v", errUpdate)
	}
	state = plugin.stateStore().Snapshot()
	for _, current := range state.Proxy.Templates {
		if current.ID != second.ID {
			continue
		}
		if len(current.ValueGroups) != 1 || len(current.ValueGroups[0].Rows) != 2 || current.ValueGroups[0].Rows[1][1] != "enterprise" {
			t.Fatalf("rejected update changed the existing template: %#v", current)
		}
		if current.ID == first.ID {
			t.Fatal("template IDs collided")
		}
		return
	}
	t.Fatal("updated template disappeared")
}

func TestProxyTemplateRejectsSemanticallyEquivalentDefinition(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	first, errCreate := plugin.createProxyTemplate(proxyapp.TemplateInput{
		Name: "renamed base",
		URL:  "socks5h://rn-region-{country}-sid-{session}-t-5:password@us.arxlabs.io:3010",
		Placeholders: []proxy.Placeholder{
			{Name: "country", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "US,CN,SG"}},
			{Name: "session", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 8, CaseSensitive: true}},
		},
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	_, errDuplicate := plugin.createProxyTemplate(proxyapp.TemplateInput{
		Name: "renamed duplicate",
		URL:  "socks5h://rn-region-{pl1}-sid-{pl2}-t-5:password@us.arxlabs.io:3010",
		Placeholders: []proxy.Placeholder{
			{Name: "pl2", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 8, CaseSensitive: true}},
			{Name: "pl1", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: "|", Values: []string{"SG", "US", "CN"}}},
		},
	})
	if errDuplicate == nil || !strings.Contains(errDuplicate.Error(), "api.error.proxy_duplicate") {
		t.Fatalf("semantically equivalent template was not rejected: %v", errDuplicate)
	}
	state := plugin.stateStore().Snapshot()
	if len(state.Proxy.Templates) != 1 || state.Proxy.Templates[0].ID != first.ID {
		t.Fatalf("duplicate template was persisted: %#v", state.Proxy.Templates)
	}
}

func TestProxyVersionTracksEndpointAndGenerationChanges(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	node, errCreate := plugin.createProxyNode(proxyapp.NodeInput{URL: "http://u:p@node.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	renamed, _, errUpdate := plugin.updateProxyNode(proxyapp.NodeInput{ID: node.ID, Name: "renamed-node"})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if renamed.ConfigVersion != node.ConfigVersion || renamed.Revision != node.Revision {
		t.Fatalf("metadata-only node edit advanced content versions: before=%#v after=%#v", node, renamed)
	}
	changed, _, errUpdate := plugin.updateProxyNode(proxyapp.NodeInput{ID: node.ID, URL: "http://u:p@changed-node.example:8080"})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if changed.ConfigVersion != node.ConfigVersion+1 || changed.Revision != node.Revision+1 {
		t.Fatalf("endpoint change did not advance node versions: before=%#v after=%#v", node, changed)
	}

	placeholders := []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us"}}}
	template, errCreate := plugin.createProxyTemplate(proxyapp.TemplateInput{
		Name: "region", URL: "http://user-{region}:secret@template.example:8080", Placeholders: placeholders,
	})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	renamedTemplate, templateResult, errUpdate := plugin.updateProxyTemplate(proxyapp.TemplateInput{ID: template.ID, Name: "renamed-template"})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if templateResult["endpoint_changed"] != false || templateResult["generated_changed"] != false {
		t.Fatalf("metadata-only template edit changed generated content: %#v", templateResult)
	}
	if renamedTemplate.ConfigVersion != template.ConfigVersion || renamedTemplate.Revision != template.Revision {
		t.Fatalf("metadata-only template edit advanced content versions: before=%#v after=%#v", template, renamedTemplate)
	}
	placeholders[0].Enum = &proxy.EnumSpec{Separator: ",", Input: "cn"}
	generated, generatedResult, errUpdate := plugin.updateProxyTemplate(proxyapp.TemplateInput{ID: template.ID, Placeholders: placeholders})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if generatedResult["endpoint_changed"] != false || generatedResult["generated_changed"] != true {
		t.Fatalf("placeholder edit classification is wrong: %#v", generatedResult)
	}
	if generated.ConfigVersion != template.ConfigVersion || generated.Revision != template.Revision+1 {
		t.Fatalf("generation-only edit advanced the endpoint version: before=%#v after=%#v", template, generated)
	}
	endpoint, _, errUpdate := plugin.updateProxyTemplate(proxyapp.TemplateInput{
		ID: template.ID, URL: "http://user-{region}:secret@changed-template.example:8080", Placeholders: placeholders,
	})
	if errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if endpoint.ConfigVersion != generated.ConfigVersion+1 || endpoint.Revision != generated.Revision+1 {
		t.Fatalf("template endpoint change did not advance both versions: before=%#v after=%#v", generated, endpoint)
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
	reloaded := appstate.NewStore(path)
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
	_, _, err = plugin.updateProxyNode(proxyapp.NodeInput{ID: node.ID, UsernameSet: true, Username: ""})
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
	store := appstate.NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "test"); err != nil {
		t.Fatal(err)
	}
	op := proxy.Operation{ID: "prepared", AuthID: "auth", Action: "proxy_bind", Status: "prepared", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := store.CommitProxy(appstate.ProxyCommit{Operation: &op}); err != nil {
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
	reloaded := appstate.NewStore(store.Path())
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
	if err := appstate.NewStore(store.Path()).Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "test"); err == nil {
		t.Fatal("missing base checkpoint silently discarded configuration")
	}
}

func TestProxyHistoryGCKeepsReferencedVersionsAndOrder(t *testing.T) {
	state := appstate.State{Accounts: map[string]*appstate.AccountState{"auth": {Proxy: proxy.Binding{SourceID: "template", SourceRevision: 1}}}, Proxy: proxy.ProxyState{TemplateVersions: []proxy.Template{{ID: "template", Revision: 1}, {ID: "template", Revision: 2}}}}
	for i := 0; i < 105; i++ {
		state.Proxy.Changes = append(state.Proxy.Changes, proxy.SourceChange{ID: fmt.Sprint(i), Status: "succeeded"})
	}
	appstate.GCProxyHistory(&state)
	if len(state.Proxy.TemplateVersions) != 1 || state.Proxy.TemplateVersions[0].Revision != 1 {
		t.Fatal("referenced template version was collected")
	}
	if len(state.Proxy.Changes) != 100 || state.Proxy.Changes[0].ID != "5" || state.Proxy.Changes[99].ID != "104" {
		t.Fatal("completed history order was corrupted")
	}
}

func TestProxyChangeProgressTracksCommittedItems(t *testing.T) {
	store := appstate.NewStore(filepath.Join(t.TempDir(), "state.json"))
	if err := store.Load(appconfig.PolicyFromConfig(appconfig.DefaultConfig()), "test"); err != nil {
		t.Fatal(err)
	}
	defer store.Flush()
	if err := store.Update(func(state *appstate.State) error {
		state.Proxy.Changes = []proxy.SourceChange{{ID: "progress", Status: "running", Counts: proxy.ChangeCounts{Total: 2, Pending: 2}, Items: []proxy.ChangeItem{{AuthID: "one", Status: "pending"}, {AuthID: "two", Status: "pending"}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	item := proxy.ChangeItem{AuthID: "one", Status: "succeeded"}
	if err := store.CommitProxy(appstate.ProxyCommit{ChangeID: "progress", ChangeItem: &item}); err != nil {
		t.Fatal(err)
	}
	change, _ := store.ProxyChange("progress")
	if change.Counts.Succeeded != 1 || change.Counts.Pending != 1 {
		t.Fatalf("live progress not updated: %#v", change.Counts)
	}
}
func TestProxySourceNamesAreUniqueAcrossKinds(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	first, errCreate := plugin.createProxyNode(proxyapp.NodeInput{Name: "Primary", URL: "http://u:p@one.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, errDuplicate := plugin.createProxyNode(proxyapp.NodeInput{Name: "primary", URL: "http://u:p@two.example:8080"}); errDuplicate == nil || !strings.Contains(errDuplicate.Error(), "api.error.proxy_name_duplicate") {
		t.Fatalf("duplicate node name was not rejected: %v", errDuplicate)
	}
	if _, errDuplicate := plugin.createProxyTemplate(proxyapp.TemplateInput{Name: "PRIMARY", URL: "http://user-{region}:secret@template.example:8080", Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us"}}}}); errDuplicate == nil || !strings.Contains(errDuplicate.Error(), "api.error.proxy_name_duplicate") {
		t.Fatalf("cross-kind duplicate name was not rejected: %v", errDuplicate)
	}
	template, errCreate := plugin.createProxyTemplate(proxyapp.TemplateInput{Name: "Template one", URL: "http://user-{region}:secret@template.example:8080", Placeholders: []proxy.Placeholder{{Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us"}}}})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	if _, _, errUpdate := plugin.updateProxyNode(proxyapp.NodeInput{ID: first.ID, Name: template.Name}); errUpdate == nil || !strings.Contains(errUpdate.Error(), "api.error.proxy_name_duplicate") {
		t.Fatalf("node rename to template name was not rejected: %v", errUpdate)
	}
	renamed, _, errUpdate := plugin.updateProxyNode(proxyapp.NodeInput{ID: first.ID, Name: "Primary renamed"})
	if errUpdate != nil || renamed.Name != "Primary renamed" {
		t.Fatalf("valid node rename failed: node=%#v err=%v", renamed, errUpdate)
	}
}

func TestProxySourceListFiltersEnabledAndConnectivity(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	enabled, errCreate := plugin.createProxyNode(proxyapp.NodeInput{Name: "Enabled node", URL: "http://u:p@one.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	disabled, errCreate := plugin.createProxyNode(proxyapp.NodeInput{Name: "Disabled node", URL: "http://u:p@two.example:8080"})
	if errCreate != nil {
		t.Fatal(errCreate)
	}
	disabledFlag := false
	if _, _, errUpdate := plugin.updateProxyNode(proxyapp.NodeInput{ID: disabled.ID, Enabled: &disabledFlag}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errHealth := plugin.stateStore().RecordProxyHealth(enabled.ID, enabled.Revision, proxy.HealthHealthy, time.Now().UTC(), nil); errHealth != nil {
		t.Fatal(errHealth)
	}
	if errHealth := plugin.stateStore().RecordProxyHealth(disabled.ID, disabled.Revision, proxy.HealthUnhealthy, time.Now().UTC(), nil); errHealth != nil {
		t.Fatal(errHealth)
	}

	decode := func(params map[string]any) []proxyapp.SourceView {
		response := actionManagementForTest(t, plugin, "proxy.sources.list", params)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("source list status = %d body=%s", response.StatusCode, response.Body)
		}
		var envelope struct {
			Data struct {
				Items []proxyapp.SourceView `json:"items"`
				Total int                   `json:"total"`
			} `json:"data"`
		}
		if errDecode := json.Unmarshal(response.Body, &envelope); errDecode != nil {
			t.Fatal(errDecode)
		}
		return envelope.Data.Items
	}
	if items := decode(map[string]any{"enabled": "enabled", "health": "healthy"}); len(items) != 1 || items[0].ID != enabled.ID {
		t.Fatalf("enabled+healthy filter = %#v", items)
	}
	if items := decode(map[string]any{"enabled": "disabled", "health": "unhealthy"}); len(items) != 1 || items[0].ID != disabled.ID {
		t.Fatalf("disabled+unhealthy filter = %#v", items)
	}
	if items := decode(map[string]any{"health": "unknown"}); len(items) != 0 {
		t.Fatalf("unknown filter unexpectedly matched tested sources: %#v", items)
	}
}
func TestProxySourceListHasNoImplicitResinSource(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "user@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	response := actionManagementForTest(t, plugin, "proxy.sources.list", nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("source list status = %d body=%s", response.StatusCode, response.Body)
	}
	var envelope struct {
		Data struct {
			Items []proxyapp.SourceView `json:"items"`
			Total int                   `json:"total"`
		} `json:"data"`
	}
	if errDecode := json.Unmarshal(response.Body, &envelope); errDecode != nil {
		t.Fatal(errDecode)
	}
	if envelope.Data.Total != 0 || len(envelope.Data.Items) != 0 {
		t.Fatalf("implicit Resin source leaked into the source list: %#v", envelope.Data)
	}
}

func TestProxyImportRecognizesAddressFirstFormat(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	value, status, errImport := plugin.actionProxyImportNodes(map[string]any{
		"nodes": []any{"socks5h://proxy.example:1080:u%40x:p%253A%40"},
	})
	if errImport != nil || status != 200 {
		t.Fatalf("import failed: status=%d err=%v", status, errImport)
	}
	result, ok := value.(map[string]any)
	if !ok || result["created"] != 1 {
		t.Fatalf("unexpected import result: %#v", value)
	}
	state := plugin.stateStore().Snapshot()
	if len(state.Proxy.Nodes) != 1 {
		t.Fatalf("unexpected nodes: %#v", state.Proxy.Nodes)
	}
	endpoint := state.Proxy.Nodes[0].Endpoint
	if endpoint.Host != "proxy.example" || endpoint.Username != "u@x" || endpoint.Password != "p%3A@" {
		t.Fatalf("unexpected imported endpoint: %#v", endpoint)
	}
}
func TestProxyImportRecognizesHostPortAtCredentialsFormat(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)
	value, status, errImport := plugin.actionProxyImportNodes(map[string]any{
		"nodes": []any{"http://proxy.example:8080@u%40x:p%253A%40"},
	})
	if errImport != nil || status != 200 {
		t.Fatalf("import failed: status=%d err=%v", status, errImport)
	}
	result, ok := value.(map[string]any)
	if !ok || result["created"] != 1 {
		t.Fatalf("unexpected import result: %#v", value)
	}
	state := plugin.stateStore().Snapshot()
	if len(state.Proxy.Nodes) != 1 {
		t.Fatalf("unexpected nodes: %#v", state.Proxy.Nodes)
	}
	endpoint := state.Proxy.Nodes[0].Endpoint
	if endpoint.Host != "proxy.example" || endpoint.Port != 8080 || endpoint.Username != "u@x" || endpoint.Password != "p%3A@" {
		t.Fatalf("unexpected imported endpoint: %#v", endpoint)
	}
}

// Failure rotation.

func TestProbeProxyErrorsRotateManagedProxyAtThreshold(t *testing.T) {
	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{"proxy_url": oldProxy})
	host := newMockHostCall(entries, auths)
	host.scriptStreamStatus("auth-1", http.StatusProxyAuthRequired)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_only"
		cfg.Proxy.AutoRotateAfterQualityFailure = true
	})
	seedResinBinding(t, plugin, "grok-alice-old1", 0)

	for attempt := 0; attempt < 2; attempt++ {
		if errProbe := plugin.ProbeAccount(context.Background(), "auth-1", true, ""); errProbe != nil {
			t.Fatal(errProbe)
		}
	}

	saved, errDecode := rawAuthToMap(host.auths["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	rotatedProxy := appauth.RawString(saved, "proxy_url")
	if rotatedProxy == "" || rotatedProxy == oldProxy {
		t.Fatalf("probe did not rotate the managed proxy: %q", rotatedProxy)
	}
	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateUnknown || account.ErrorStrikes != 0 {
		t.Fatalf("rotation did not clear proxy error strikes: %#v", account)
	}
	rotations := 0
	disables := 0
	recoveries := 0
	for _, event := range plugin.stateStore().Snapshot().Events {
		switch event.Action {
		case "proxy_rotate":
			rotations++
		case "guard_disable":
			disables++
		case "proxy_error_rotation":
			recoveries++
		}
	}
	if rotations != 1 || disables != 0 || recoveries != 1 {
		t.Fatalf("unexpected events: rotations=%d disables=%d recoveries=%d", rotations, disables, recoveries)
	}
	if len(host.modelCalls) != 2 {
		t.Fatalf("probe used %d upstream calls, want 2", len(host.modelCalls))
	}
}
func TestProbeProxyErrorsKeepCooldownWhenAutoRotateDisabled(t *testing.T) {
	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{"proxy_url": oldProxy})
	host := newMockHostCall(entries, auths)
	host.scriptStreamStatus("auth-1", http.StatusProxyAuthRequired)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_only"
		cfg.Proxy.AutoRotateAfterQualityFailure = false
	})
	seedResinBinding(t, plugin, "grok-alice-old1", 0)

	for attempt := 0; attempt < 2; attempt++ {
		if errProbe := plugin.ProbeAccount(context.Background(), "auth-1", true, ""); errProbe != nil {
			t.Fatal(errProbe)
		}
	}

	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateCooling || account.ErrorStrikes != 2 {
		t.Fatalf("disabled auto-rotation did not keep the cooldown fallback: %#v", account)
	}
	for _, event := range plugin.stateStore().Snapshot().Events {
		if event.Action == "proxy_rotate" || event.Action == "proxy_error_rotation" {
			t.Fatalf("disabled auto-rotation still rotated the proxy: %#v", event)
		}
	}
	if len(host.saves) != 1 {
		t.Fatalf("cooldown fallback saved auth %d times, want 1", len(host.saves))
	}
}

func TestTransparentProxyErrorRotatesManagedProxyAtThreshold(t *testing.T) {
	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{"proxy_url": oldProxy})
	host := newMockHostCall(entries, auths)
	host.scriptStreamStatus("auth-1", http.StatusProxyAuthRequired)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.AutoAction = "cooldown_only"
		cfg.Proxy.AutoRotateAfterQualityFailure = true
	})
	seedResinBinding(t, plugin, "grok-alice-old1", 0)

	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-proxy-rotation-1")
	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateUnknown || account.ErrorStrikes != 1 {
		t.Fatalf("first proxy failure should stay selectable: %#v", account)
	}
	if len(host.modelCalls) != 1 {
		t.Fatalf("first transparent request used %d attempts, want 1", len(host.modelCalls))
	}

	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-proxy-rotation-2")
	if len(host.modelCalls) != 3 {
		t.Fatalf("two transparent requests used %d attempts, want 3 with one rotation retry", len(host.modelCalls))
	}
	if host.modelCalls[1].ProxyURL == host.modelCalls[2].ProxyURL {
		t.Fatalf("retry reused the failed proxy: %#v", host.modelCalls)
	}
	if host.modelCalls[1].AuthID != "auth-1" || host.modelCalls[2].AuthID != "auth-1" {
		t.Fatalf("retry switched auth instead of retrying the rotated proxy: %#v", host.modelCalls)
	}
	rotations := 0
	for _, event := range plugin.stateStore().Snapshot().Events {
		if event.Action == "proxy_rotate" {
			rotations++
		}
	}
	if rotations != 1 {
		t.Fatalf("transparent proxy failures rotated %d times, want 1", rotations)
	}
}

func TestProbeTransportErrorsRotateManagedProxyAtThreshold(t *testing.T) {
	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{"proxy_url": oldProxy})
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_only"
		cfg.Proxy.AutoRotateAfterQualityFailure = true
	})
	seedResinBinding(t, plugin, "grok-alice-old1", 0)

	for attempt := 0; attempt < 2; attempt++ {
		if errProbe := plugin.ProbeAccount(context.Background(), "auth-1", true, ""); errProbe != nil {
			t.Fatal(errProbe)
		}
	}

	saved, errDecode := rawAuthToMap(host.auths["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if rotated := appauth.RawString(saved, "proxy_url"); rotated == "" || rotated == oldProxy {
		t.Fatalf("probe transport errors did not rotate the managed proxy: %q", rotated)
	}
	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateUnknown || account.ErrorStrikes != 0 {
		t.Fatalf("transport error rotation did not clear strikes: %#v", account)
	}
	rotations := 0
	for _, event := range plugin.stateStore().Snapshot().Events {
		if event.Action == "proxy_rotate" {
			rotations++
		}
	}
	if rotations != 1 {
		t.Fatalf("transport errors rotated %d times, want 1", rotations)
	}
}

func TestTransparentProxyFailureSwitchesAccountBeforeAsyncRotation(t *testing.T) {
	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries1, auths1 := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{"proxy_url": oldProxy})
	entries2, auths2 := singleGrokAuth(t, "auth-2", "[EMAIL]", nil)
	entries1[0].Priority = 2
	entries2[0].Priority = 1
	auths := make(map[string]json.RawMessage, len(auths1)+len(auths2))
	for key, value := range auths1 {
		auths[key] = value
	}
	for key, value := range auths2 {
		auths[key] = value
	}
	host := newMockHostCall(append(entries1, entries2...), auths)
	host.scriptStreamStatus("auth-1", http.StatusProxyAuthRequired)
	host.scriptStream("auth-2", goodStreamScript()...)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		enableTransparentForTest(cfg)
		cfg.AutoAction = "cooldown_only"
		cfg.Proxy.AutoRotateAfterQualityFailure = true
	})
	seedResinBinding(t, plugin, "grok-alice-old1", 0)
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := plugin.ensureStateAccount(state, "auth-1")
		account.ErrorStrikes = 1
		account.State = appstate.StateUnknown
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	plugin.runTransparentStreamBridge(context.Background(), streamRequestForTest(), "plugin-stream-account-first")
	plugin.taskMgr.Wait()

	if len(host.modelCalls) != 2 {
		t.Fatalf("request used %d upstream calls, want auth switch without same-auth retry", len(host.modelCalls))
	}
	if host.modelCalls[0].AuthID != "auth-1" || host.modelCalls[1].AuthID != "auth-2" {
		t.Fatalf("proxy failure did not switch account first: %#v", host.modelCalls)
	}
	saved, errDecode := rawAuthToMap(host.auths["auth-1"])
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if rotated := appauth.RawString(saved, "proxy_url"); rotated == "" || rotated == oldProxy {
		t.Fatalf("async task did not rotate the managed proxy: %q", rotated)
	}
	if appauth.Disabled(saved) {
		t.Fatal("async rotation left the first auth disabled")
	}
	if len(host.saves) != 1 {
		t.Fatalf("auth switch path saved auth %d times, want only the async rotation save", len(host.saves))
	}
	account, okAccount := plugin.stateAccount("auth-1")
	if !okAccount || account.State != appstate.StateUnknown || account.ErrorStrikes != 0 {
		t.Fatalf("async rotation did not restore the first auth: %#v", account)
	}
	rotations := 0
	for _, event := range plugin.stateStore().Snapshot().Events {
		if event.Action == "proxy_rotate" {
			rotations++
		}
		if event.Action == "guard_disable" {
			t.Fatalf("auth switch path synchronously disabled the failed account: %#v", event)
		}
	}
	if rotations != 1 {
		t.Fatalf("async rotation count = %d, want 1", rotations)
	}
}

// Resin source lifecycle.

func TestResinSourcesUseTemplateLifecycleAndSupportMultipleEntries(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)

	create := func(name, format string, placeholders []proxy.Placeholder) map[string]any {
		t.Helper()
		response := actionManagementForTest(t, plugin, "proxy.sources.create", map[string]any{
			"kind": "resin",
			"source": map[string]any{
				"name": name, "enabled": true, "local_only": true,
				"account_format": format, "placeholders": placeholders,
			},
		})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("create %s status=%d body=%s", name, response.StatusCode, response.Body)
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(response.Body, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}

	first := create("Resin A", "xai-{account}-{region}", []proxy.Placeholder{{
		Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us,jp"},
	}})
	second := create("Resin B", "svc-{account}-{suffix}", []proxy.Placeholder{{
		Name: "suffix", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 6},
	}})
	if first["id"] == "" || second["id"] == "" || first["id"] == second["id"] {
		t.Fatalf("resin sources did not receive stable unique IDs: first=%#v second=%#v", first, second)
	}
	if first["kind"] != "resin" || second["kind"] != "resin" || first["local_only"] != true {
		t.Fatalf("unexpected resin source views: first=%#v second=%#v", first, second)
	}

	list := actionManagementForTest(t, plugin, "proxy.sources.list", map[string]any{"kind": "resin"})
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.StatusCode, list.Body)
	}
	var listed struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body, &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Data.Total != 2 || len(listed.Data.Items) != 2 {
		t.Fatalf("expected two Resin sources, got total=%d items=%d", listed.Data.Total, len(listed.Data.Items))
	}

	update := actionManagementForTest(t, plugin, "proxy.sources.update", map[string]any{
		"kind": "resin",
		"source": map[string]any{
			"id": first["id"], "name": "Resin A", "enabled": true, "local_only": true,
			"account_format": "xai-{account}-{plan}-{suffix}",
			"placeholders": []proxy.Placeholder{
				{Name: "plan", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "basic,pro"}},
				{Name: "suffix", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 4}},
			},
			"expected_version": first["config_version"],
		},
	})
	if update.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.StatusCode, update.Body)
	}

	remove := actionManagementForTest(t, plugin, "proxy.sources.delete", map[string]any{"source_id": second["id"], "force": true})
	if remove.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", remove.StatusCode, remove.Body)
	}
	state := plugin.stateStore().Snapshot()
	if len(state.Proxy.ResinSources) != 1 {
		t.Fatalf("resin source map was not cleaned up: %#v", state.Proxy.ResinSources)
	}
	resinTemplates := 0
	for _, template := range state.Proxy.Templates {
		if template.Kind == proxy.TemplateResin {
			resinTemplates++
			if template.ID != first["id"] {
				t.Fatalf("unexpected remaining resin template: %#v", template)
			}
			if template.UsernamePattern != "xai-{account}-{plan}-{suffix}" {
				t.Fatalf("updated account format was not stored in the template: %q", template.UsernamePattern)
			}
		}
	}
	if resinTemplates != 1 {
		t.Fatalf("resin template count=%d, want 1", resinTemplates)
	}
}

// Resin lease removal.

func TestCatalogRemovalQueuesResinLeaseDeleteFromSnapshot(t *testing.T) {
	const platformID = "11111111-1111-1111-1111-111111111111"
	var deleted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if request.Header.Get("Authorization") != "Bearer admin-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.URL.Path != "/api/v1/platforms/"+platformID+"/leases/grok-alice-old1" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		deleted.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", nil)
	host := newMockHostCall(entries, auths)
	plugin := newTestPlugin(t, host, nil)
	snapshot := proxy.ResinSnapshot{AdminURL: server.URL, PlatformID: platformID, AdminToken: "admin-token"}
	snapshotID, errSnapshot := plugin.stateStore().EnsureResinSnapshot(snapshot)
	if errSnapshot != nil {
		t.Fatal(errSnapshot)
	}
	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		account := plugin.ensureStateAccount(state, "auth-1")
		account.Proxy = proxy.Binding{
			AuthID:         "auth-1",
			SourceID:       "resin-test",
			SourceKind:     proxy.SourceResin,
			SourceRevision: 1,
			Generation:     1,
			Lease: &proxy.LeaseRef{
				Provider: "resin", PlatformID: platformID, Account: "grok-alice-old1", SnapshotID: snapshotID,
			},
		}
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}

	host.mu.Lock()
	host.entries = nil
	host.mu.Unlock()
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	state := plugin.stateStore().Snapshot()
	account, exists := state.Accounts["auth-1"]
	if !exists || account.CatalogMissingSince.IsZero() {
		t.Fatalf("temporary catalog omission did not retain the managed auth: %#v", account)
	}
	if len(state.ResinLeaseDeletes) != 0 {
		t.Fatalf("temporary catalog omission queued lease deletes: %#v", state.ResinLeaseDeletes)
	}

	if errUpdate := plugin.stateStore().Update(func(state *appstate.State) error {
		state.Accounts["auth-1"].CatalogMissingSince = time.Now().UTC().Add(-guardCatalogMissingRetention - time.Minute)
		return nil
	}); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errRefresh := plugin.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	state = plugin.stateStore().Snapshot()
	if _, exists := state.Accounts["auth-1"]; exists {
		t.Fatalf("removed auth remained in state after retention: %#v", state.Accounts["auth-1"])
	}
	if len(state.ResinLeaseDeletes) != 1 {
		t.Fatalf("queued lease deletes = %d, want 1: %#v", len(state.ResinLeaseDeletes), state.ResinLeaseDeletes)
	}
	job := state.ResinLeaseDeletes[0]
	if job.AuthID != "auth-1" || job.Account != "grok-alice-old1" || job.AdminURL != server.URL || job.PlatformID != platformID || job.SnapshotID != snapshotID {
		t.Fatalf("unexpected lease delete job: %#v", job)
	}
	if deleted.Load() != 0 {
		t.Fatal("catalog refresh deleted the lease synchronously")
	}
	if processed := plugin.proxySvc.ProcessDueResinLeaseDeletes(context.Background(), time.Now().UTC()); processed != 1 {
		t.Fatalf("processed lease deletes = %d, want 1", processed)
	}
	if deleted.Load() != 1 {
		t.Fatalf("resin delete calls = %d, want 1", deleted.Load())
	}
	if jobs := plugin.stateStore().Snapshot().ResinLeaseDeletes; len(jobs) != 0 {
		t.Fatalf("successful lease delete remained queued: %#v", jobs)
	}
}
