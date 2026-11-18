package management

import (
	"net/http"
	"strings"
	"testing"

	"grok-quality-guard/internal/app/tasks"
)

func TestLocalizedErrorMessagesSupportAllLocales(t *testing.T) {
	tests := map[string]string{
		"zh-CN": "任务仍在执行中，请先取消任务并等待结束",
		"zh-TW": "任務仍在執行中，請先取消任務並等待結束",
		"en":    "The task is still running. Cancel it and wait for it to finish",
	}
	for locale, want := range tests {
		got := localizedErrorMessage(locale, "task_active", http.StatusConflict, "", nil)
		if got != want {
			t.Fatalf("localizedErrorMessage(%s) = %q, want %q", locale, got, want)
		}
	}
}

func TestLocalizeTaskUsesStableKeysAndArguments(t *testing.T) {
	task := tasks.Task{
		Kind:         "accounts.probe_selected",
		Label:        "task.label.accounts_probe_selected",
		Status:       "running",
		Progress:     "task.progress.probe_selected",
		CurrentStage: "task.progress.probe_selected",
		AuthCount:    3,
	}
	english := localizeTask("en", task)
	if english.Label != "Test selected accounts" {
		t.Fatalf("english label = %q", english.Label)
	}
	if english.CurrentStage != "Testing 3 selected accounts" {
		t.Fatalf("english stage = %q", english.CurrentStage)
	}
	traditional := localizeTask("zh-TW", task)
	if traditional.Label != "批次偵測帳號" || traditional.CurrentStage != "正在偵測 3 個已選帳號" {
		t.Fatalf("traditional localization = %#v", traditional)
	}
}

func TestLocalizeEventUsesSemanticDetailKey(t *testing.T) {
	event := localizeEvent("en", Event{
		DetailKey:  "event.detail.soft_tps",
		DetailArgs: map[string]any{"source": "probe", "strikes": 1, "threshold": 2},
	})
	if !strings.Contains(event.Detail, "soft TPS strike 1/2") {
		t.Fatalf("event detail = %q", event.Detail)
	}
}

func TestLocalizeResponseDataPreservesMachineReadableFields(t *testing.T) {
	localized, ok := localizeResponseData("en", map[string]any{
		"detail_key":      "event.detail.probe_passed",
		"code":            "task_active",
		"last_error_code": "api.error.proxy_duplicate",
		"path":            "quality.soft_tps",
		"kind":            "proxy.auto_bind.get",
		"message":         "api.error.task_active",
		"last_error":      "api.error.proxy_duplicate",
		"items": []any{map[string]any{
			"label":      "task.label.accounts_probe",
			"detail_key": "event.detail.soft_tps",
		}},
	}).(map[string]any)
	if !ok {
		t.Fatal("localized response is not a map")
	}
	for _, field := range []string{"detail_key", "code", "last_error_code", "path", "kind"} {
		if got, want := localized[field], map[string]any{
			"detail_key":      "event.detail.probe_passed",
			"code":            "task_active",
			"last_error_code": "api.error.proxy_duplicate",
			"path":            "quality.soft_tps",
			"kind":            "proxy.auto_bind.get",
		}[field]; got != want {
			t.Fatalf("structural field %s = %#v, want %#v", field, got, want)
		}
	}
	if localized["message"] != "The task is still running. Cancel it and wait for it to finish" {
		t.Fatalf("message was not localized: %#v", localized["message"])
	}
	items, _ := localized["items"].([]any)
	item, _ := items[0].(map[string]any)
	if item["detail_key"] != "event.detail.soft_tps" {
		t.Fatalf("nested detail_key = %#v, want semantic key", item["detail_key"])
	}
	if item["label"] != "Probe account" {
		t.Fatalf("nested label = %#v, want localized label", item["label"])
	}
}

func TestLocalizeProxyImportDetailsUsesStableErrorCodes(t *testing.T) {
	localized, ok := localizeResponseData("en", map[string]any{
		"skipped_items": []any{map[string]any{
			"line":       2,
			"code":       "proxy_import_missing_scheme",
			"error":      "proxy url is missing scheme",
			"error_code": "proxy_import_missing_scheme",
		}},
	}).(map[string]any)
	if !ok {
		t.Fatal("localized response is not a map")
	}
	items, _ := localized["skipped_items"].([]any)
	item, _ := items[0].(map[string]any)
	if item["code"] != "proxy_import_missing_scheme" || item["error_code"] != "proxy_import_missing_scheme" {
		t.Fatalf("import codes were not preserved: %#v", item)
	}
	want := "The proxy address is missing a URL scheme, such as http:// or socks5://"
	if item["error"] != want {
		t.Fatalf("import error = %#v, want %q", item["error"], want)
	}
}

func TestLocalizeDetailTextTranslatesResinCleanupRetryError(t *testing.T) {
	tests := map[string]string{
		"zh-CN": "错误：Resin 清理请求过于频繁，请稍后重试",
		"zh-TW": "錯誤：Resin 清理請求過於頻繁，請稍後重試",
		"en":    "Error: Resin cleanup is rate limited. Try again later",
	}
	for locale, want := range tests {
		got := localizeDetailText(locale, "account=alpha attempt=2 retry_in=1m0s error=resin_cleanup_rate_limited", "")
		if !strings.Contains(got, want) {
			t.Fatalf("%s localized cleanup detail = %q, want substring %q", locale, got, want)
		}
	}
}

func TestLocalizeResponseDataTranslatesValidationMessagesWithoutChangingCodes(t *testing.T) {
	tests := []struct {
		locale string
		want   string
	}{
		{locale: "zh-CN", want: "代理地址无效或过长"},
		{locale: "zh-TW", want: "代理位址無效或過長"},
		{locale: "en", want: "The proxy URL is invalid or too long"},
	}
	for _, test := range tests {
		t.Run(test.locale, func(t *testing.T) {
			issue := map[string]any{
				"code":    "endpoint_invalid",
				"path":    "url",
				"message": "proxy url is invalid",
			}
			localized, ok := localizeResponseData(test.locale, map[string]any{
				"valid":      false,
				"error":      "proxy url is invalid",
				"error_code": "endpoint_invalid",
				"errors":     []any{issue},
				"validation": map[string]any{
					"valid":  false,
					"errors": []any{issue},
				},
			}).(map[string]any)
			if !ok {
				t.Fatal("localized response is not a map")
			}
			if localized["error"] != test.want {
				t.Fatalf("top-level error = %#v, want %q", localized["error"], test.want)
			}
			if localized["error_code"] != "endpoint_invalid" {
				t.Fatalf("top-level error_code = %#v, want endpoint_invalid", localized["error_code"])
			}
			for _, field := range []string{"errors", "validation"} {
				value := localized[field]
				if field == "validation" {
					value = localized["validation"].(map[string]any)["errors"]
				}
				issues, okIssues := value.([]any)
				if !okIssues || len(issues) != 1 {
					t.Fatalf("%s = %#v, want one issue", field, value)
				}
				item, _ := issues[0].(map[string]any)
				if item["code"] != "endpoint_invalid" || item["path"] != "url" {
					t.Fatalf("%s machine-readable fields changed: %#v", field, item)
				}
				if item["message"] != test.want {
					t.Fatalf("%s message = %#v, want %q", field, item["message"], test.want)
				}
			}
		})
	}
}

func TestLocalizeResponseDataDoesNotLeakUncodedEnglishErrors(t *testing.T) {
	localized, ok := localizeResponseData("en", map[string]any{
		"error":      "task manager is not initialized",
		"message":    "request body is empty",
		"last_error": "no available proxy source",
	}).(map[string]any)
	if !ok {
		t.Fatal("localized response is not a map")
	}
	want := "The operation failed. See task details"
	for _, field := range []string{"error", "message", "last_error"} {
		if localized[field] != want {
			t.Fatalf("%s = %#v, want %q", field, localized[field], want)
		}
	}
}

func TestLocalizeEventTranslatesGuardDisableKindAndReconcileSource(t *testing.T) {
	tests := []struct {
		locale        string
		wantCooldown  string
		wantReconcile string
	}{
		{locale: "zh-CN", wantCooldown: "冷却", wantReconcile: "自动校正"},
		{locale: "zh-TW", wantCooldown: "冷卻", wantReconcile: "自動校正"},
		{locale: "en", wantCooldown: "Cooldown", wantReconcile: "Automatic reconciliation"},
	}
	for _, test := range tests {
		t.Run(test.locale, func(t *testing.T) {
			disableEvent := localizeEvent(test.locale, Event{Action: "guard_disable", Result: "cooldown", Detail: "cooldown"})
			if disableEvent.Detail != test.wantCooldown {
				t.Fatalf("guard disable detail = %q, want %q", disableEvent.Detail, test.wantCooldown)
			}
			restoreEvent := localizeEvent(test.locale, Event{Action: "restore", Result: "restored", Detail: "reconcile"})
			if restoreEvent.Detail != test.wantReconcile {
				t.Fatalf("reconcile detail = %q, want %q", restoreEvent.Detail, test.wantReconcile)
			}
		})
	}
}
