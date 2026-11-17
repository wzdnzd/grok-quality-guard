package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	appauth "grok-quality-guard/internal/app/auth"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
)

func TestProbeProxyErrorsRotateManagedProxyAtThreshold(t *testing.T) {
	oldProxy := "socks5h://Default.grok-alice-old1:proxy-secret@resin:2260"
	entries, auths := singleGrokAuth(t, "auth-1", "[EMAIL]", map[string]any{"proxy_url": oldProxy})
	host := newMockHostCall(entries, auths)
	host.scriptStreamStatus("auth-1", http.StatusProxyAuthRequired)
	plugin := newTestPlugin(t, host, func(cfg *appconfig.Config) {
		cfg.AutoAction = "cooldown_only"
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
		cfg.Proxy.AutoRotateAfterQualityFailure = true
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)

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
		case "resin_rotate":
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
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
		cfg.Proxy.AutoRotateAfterQualityFailure = false
		cfg.Resin.AutoRotateAfterQualityFailure = false
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)

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
		if event.Action == "resin_rotate" || event.Action == "proxy_error_rotation" {
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
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
		cfg.Proxy.AutoRotateAfterQualityFailure = true
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)

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
		if event.Action == "resin_rotate" {
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
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
		cfg.Proxy.AutoRotateAfterQualityFailure = true
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)

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
		if event.Action == "resin_rotate" {
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
		cfg.Resin.Enabled = true
		cfg.Resin.LocalOnly = true
		cfg.Proxy.AutoRotateAfterQualityFailure = true
	})
	seedResinState(t, plugin, "grok-alice-old1", 0)
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
		if event.Action == "resin_rotate" {
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
