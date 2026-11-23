package management

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"grok-quality-guard/internal/app/i18n"
	"grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
)

func localizeTask(locale string, task tasks.Task) tasks.Task {
	if strings.TrimSpace(task.Kind) != "" {
		task.Label = i18n.T(locale, tasks.TaskLabelKey(task.Kind), nil)
	} else if strings.HasPrefix(task.Label, "task.") || i18n.Has(locale, task.Label) {
		task.Label = i18n.T(locale, task.Label, nil)
	}
	args := map[string]any{"count": task.AuthCount}
	task.Progress = localizeTaskText(locale, task.Progress, args, "", "")
	task.CurrentStage = localizeTaskText(locale, task.CurrentStage, args, "", "")
	if strings.TrimSpace(task.Error) != "" {
		task.Error = localizeTaskText(locale, task.Error, args, "task.error.failed", task.ErrorCode)
	}
	return task
}

func localizeTaskText(locale, text string, args map[string]any, fallbackKey, code string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "task.") || strings.HasPrefix(text, "api.") || i18n.Has(locale, text) {
		return i18n.T(locale, text, args)
	}
	if key := localizationKeyForCode(locale, code); key != "" {
		return i18n.T(locale, key, args)
	}
	if translated := i18n.TranslateSource(locale, text); translated != text {
		return translated
	}
	if fallbackKey != "" {
		return i18n.T(locale, fallbackKey, args)
	}
	return text
}

func localizeEvent(locale string, event state.Event) state.Event {
	event.DetailKey = strings.TrimSpace(event.DetailKey)
	if event.DetailKey != "" {
		event.Detail = i18n.T(locale, event.DetailKey, localizeEventArgs(locale, event.DetailArgs))
		return event
	}
	rawDetail := strings.TrimSpace(event.Detail)
	if event.Action == "internal_error" {
		// Internal error details are sanitized when recorded. Keep free-form
		// diagnostics out of the localization pass, which drops unknown text.
		if rawDetail == "" || rawDetail == event.Action {
			event.Detail = i18n.T(locale, "event.detail.internal_error", nil)
			return event
		}
		if i18n.Has(locale, rawDetail) || responseLocalizationKey(rawDetail) != "" || i18n.TranslateSource(locale, rawDetail) != rawDetail {
			event.Detail = localizeDetailText(locale, rawDetail, "")
			return event
		}
		event.DetailRaw = rawDetail
		event.Detail = i18n.T(locale, "event.detail.internal_error", nil)
		return event
	}
	event.Detail = localizeDetailText(locale, event.Detail, "event.detail.generic_error")
	return event
}

func localizeEventArgs(locale string, args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	localized := make(map[string]any, len(args))
	for key, value := range args {
		localized[key] = value
		text, ok := value.(string)
		if !ok {
			continue
		}
		switch key {
		case "source":
			localized[key] = localizeSourceText(locale, text)
		case "reason":
			localized[key] = localizeReasonText(locale, text)
		case "error", "error_code":
			localized[key] = localizeResponseText(locale, text, text, true)
		}
	}
	return localized
}

func localizeSourceText(locale, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if i18n.Has(locale, text) {
		return i18n.T(locale, text, nil)
	}
	if key := "disable_kind." + text; i18n.Has(locale, key) {
		return i18n.T(locale, key, nil)
	}
	if key := "source." + text; i18n.Has(locale, key) {
		return i18n.T(locale, key, nil)
	}
	if translated := i18n.TranslateSource(locale, text); translated != text {
		return translated
	}
	return i18n.T(locale, "source.other", nil)
}

func localizeReasonText(locale, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if i18n.Has(locale, text) {
		return i18n.T(locale, text, nil)
	}
	for _, prefix := range []string{"reason.", "reason.detail."} {
		if key := prefix + text; i18n.Has(locale, key) {
			return i18n.T(locale, key, nil)
		}
	}
	if translated := i18n.TranslateSource(locale, text); translated != text {
		return translated
	}
	return i18n.T(locale, "reason.other", nil)
}

func localizeDetailText(locale, text, fallbackKey string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	separator := i18n.T(locale, "ui.meta_separator", nil)
	parts := strings.Split(text, separator)
	localized := make([]string, 0, len(parts))
	for _, part := range parts {
		translated := localizeDetailPart(locale, part)
		if translated != "" {
			localized = append(localized, translated)
		}
	}
	if len(localized) == 0 && fallbackKey != "" {
		return i18n.T(locale, fallbackKey, nil)
	}
	return strings.Join(localized, separator)
}

func localizeDetailPart(locale, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if i18n.Has(locale, text) {
		return i18n.T(locale, text, nil)
	}
	if key := responseLocalizationKey(text); key != "" && i18n.Has(locale, key) {
		return i18n.T(locale, key, nil)
	}
	if key := "disable_kind." + text; i18n.Has(locale, key) {
		return i18n.T(locale, key, nil)
	}
	if key := "source." + text; i18n.Has(locale, key) {
		return i18n.T(locale, key, nil)
	}
	if key := "reason." + text; i18n.Has(locale, key) {
		return i18n.T(locale, key, nil)
	}
	if key := "reason.detail." + text; i18n.Has(locale, key) {
		return i18n.T(locale, key, nil)
	}
	if translated := i18n.TranslateSource(locale, text); translated != text {
		return translated
	}
	if localized := localizeDetailFields(locale, text); localized != "" {
		return localized
	}
	return ""
}

func localizeDetailFields(locale, text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	labelSeparator := i18n.T(locale, "ui.detail_separator", nil)
	fieldSeparator := i18n.T(locale, "ui.meta_separator", nil)
	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		name, value, ok := strings.Cut(field, "=")
		if !ok || name == "" {
			continue
		}
		labelKey := "proxy.detail." + name
		if !i18n.Has(locale, labelKey) {
			continue
		}
		value = localizeDetailFieldValue(locale, name, value)
		parts = append(parts, i18n.T(locale, labelKey, nil)+labelSeparator+value)
	}
	return strings.Join(parts, fieldSeparator)
}

func localizeDetailFieldValue(locale, name, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if name == "remote_lease" {
		if key := "lease.remote." + value; i18n.Has(locale, key) {
			return i18n.T(locale, key, nil)
		}
	}
	if name == "error" || name == "error_code" {
		return localizeResponseText(locale, value, value, true)
	}
	if translated := i18n.TranslateSource(locale, value); translated != value {
		return translated
	}
	return value
}

func localeOrDefault(locales []string) string {
	if len(locales) == 0 {
		return i18n.DefaultLocale
	}
	locale := i18n.NormalizeLocale(locales[0])
	if locale == "" {
		return i18n.DefaultLocale
	}
	return locale
}

type localeContextKey struct{}

func withRequestLocale(ctx context.Context, locale string) context.Context {
	return context.WithValue(ctx, localeContextKey{}, locale)
}

func requestLocaleFromContext(ctx context.Context) string {
	if ctx == nil {
		return i18n.DefaultLocale
	}
	if locale, ok := ctx.Value(localeContextKey{}).(string); ok {
		return localeOrDefault([]string{locale})
	}
	return i18n.DefaultLocale
}

func localizeResponseData(locale string, data any) any {
	raw, errMarshal := json.Marshal(data)
	if errMarshal != nil {
		return data
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return data
	}
	return localizeResponseValue(locale, value)
}

func localizeResponseValue(locale string, value any) any {
	switch typed := value.(type) {
	case []any:
		for index := range typed {
			typed[index] = localizeResponseValue(locale, typed[index])
		}
		return typed
	case map[string]any:
		code, _ := typed["code"].(string)
		errorCode, _ := typed["error_code"].(string)
		messageCode, _ := typed["message_code"].(string)
		lastErrorCode, _ := typed["last_error_code"].(string)
		for _, field := range []string{"error", "message", "last_error"} {
			message, ok := typed[field].(string)
			if !ok || strings.TrimSpace(message) == "" {
				continue
			}
			fieldCode := code
			switch field {
			case "error":
				if errorCode != "" {
					fieldCode = errorCode
				}
			case "message":
				if messageCode != "" {
					fieldCode = messageCode
				}
			case "last_error":
				if lastErrorCode != "" {
					fieldCode = lastErrorCode
				}
			}
			typed[field] = localizeResponseText(locale, message, fieldCode, true)
		}
		for _, field := range []string{"detail"} {
			message, ok := typed[field].(string)
			if !ok || strings.TrimSpace(message) == "" {
				continue
			}
			if detailKey, ok := typed["detail_key"].(string); ok && i18n.Has(locale, detailKey) {
				var args map[string]any
				if value, ok := typed["detail_args"].(map[string]any); ok {
					args = localizeEventArgs(locale, value)
				}
				typed[field] = i18n.T(locale, detailKey, args)
			} else {
				typed[field] = localizeDetailText(locale, message, "")
			}
		}
		for _, field := range []string{"reason", "last_reason", "disable_reason"} {
			message, ok := typed[field].(string)
			if !ok || strings.TrimSpace(message) == "" {
				continue
			}
			typed[field] = localizeReasonText(locale, message)
		}
		for _, field := range []string{"description", "progress", "current_stage"} {
			message, ok := typed[field].(string)
			if !ok || strings.TrimSpace(message) == "" {
				continue
			}
			typed[field] = localizeResponseText(locale, message, code, false)
		}
		for key, child := range typed {
			if text, ok := child.(string); ok {
				if responseDisplayField(key) {
					typed[key] = localizeResponseText(locale, text, "", false)
				}
				continue
			}
			typed[key] = localizeResponseValue(locale, child)
		}
		return typed
	case string:
		if lookup := responseLocalizationKey(typed); lookup != "" && i18n.Has(locale, lookup) {
			return i18n.T(locale, lookup, nil)
		}
		return typed
	default:
		return value
	}
}

func localizeResponseText(locale, text, code string, fallback bool) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if lookup := responseLocalizationKey(text); lookup != "" && i18n.Has(locale, lookup) {
		return i18n.T(locale, lookup, nil)
	}
	if key := localizationKeyForCode(locale, code); key != "" {
		return i18n.T(locale, key, nil)
	}
	if translated := i18n.TranslateSource(locale, text); translated != text {
		return translated
	}
	if fallback {
		return i18n.T(locale, "api.error.operation_failed", nil)
	}
	return text
}

func localizationKeyForCode(locale, code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ""
	}
	candidates := []string{code, "api.error." + code}
	if strings.HasPrefix(code, "api_error.") {
		candidates = append(candidates, "api.error."+strings.TrimPrefix(code, "api_error."))
	}
	if strings.HasPrefix(code, "api.error.") {
		candidates = append(candidates, strings.TrimPrefix(code, "api.error."))
	}
	for _, candidate := range candidates {
		if i18n.Has(locale, candidate) {
			return candidate
		}
	}
	return ""
}

// responseDisplayField limits automatic translation to fields whose values are
// presentation copy. Structural fields such as detail_key, code, kind, path,
// and resource identifiers must remain machine-readable for API clients.
func responseDisplayField(field string) bool {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "error", "message", "last_error", "detail", "description", "progress",
		"current_stage", "label", "reason", "last_reason", "disable_reason",
		"title", "text", "hint", "summary":
		return true
	default:
		return false
	}
}

func responseLocalizationKey(value string) string {
	text := strings.TrimSpace(value)
	for _, prefix := range []string{
		"ui.", "task.", "event.", "language.", "api.error.", "api_error.",
		"registration.", "reason.", "status.", "action.", "result.", "source.",
		"quality.", "proxy.", "lease.", "queue.",
	} {
		if strings.HasPrefix(text, prefix) {
			return text
		}
	}
	return ""
}
