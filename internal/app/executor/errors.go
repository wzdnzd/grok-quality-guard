package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const maxUpstreamErrorBodyBytes = 64 << 10
const maxUpstreamErrorClassifyBytes = 4 << 10

func classifyProbeStatus(status int) string {
	class, _ := classifyUpstreamStatus(status, nil)
	return class
}

func classifyUpstreamStatus(status int, body []byte) (string, string) {
	switch status {
	case http.StatusUnauthorized:
		return ClassCredentialInvalid, "upstream status 401 (credential rejected)"
	case http.StatusProxyAuthRequired:
		return ClassProxyError, "upstream status 407 (proxy authentication required)"
	case http.StatusPaymentRequired:
		return ClassQuotaExhausted, "upstream status 402 (quota exhausted)"
	}
	if len(body) > maxUpstreamErrorClassifyBytes {
		body = body[:maxUpstreamErrorClassifyBytes]
	}
	text := strings.ToLower(string(body))
	switch status {
	case http.StatusForbidden:
		if isQuotaExhaustionText(text) {
			return ClassQuotaExhausted, "upstream status 403 (quota exhausted)"
		}
		// A WAF/CDN HTML page often contains the word authentication. Only a
		// structured JSON error with a strong credential phrase is actionable.
		if json.Valid(body) && isCredentialRejectionText(text) {
			return ClassCredentialInvalid, "upstream status 403 (credential rejected)"
		}
		return ClassIgnored, "upstream status 403"
	case http.StatusTooManyRequests:
		if isQuotaExhaustionText(text) {
			return ClassQuotaExhausted, "upstream status 429 (quota exhausted)"
		}
		return ClassRateLimited, "upstream status 429 (rate limited)"
	}
	return ClassIgnored, fmt.Sprintf("upstream status %d", status)
}

func isQuotaExhaustionText(text string) bool {
	for _, marker := range []string{
		"subscription:free-usage-exhausted",
		"used all the included free usage",
		"included free usage",
		"usage limit reached",
		"usage balance exhausted",
		"run out of credits",
		"out of credits",
		"spending-limit",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isCredentialRejectionText(text string) bool {
	for _, marker := range []string{
		"invalid token",
		"token expired",
		"invalid api key",
		"incorrect api key",
		"credential rejected",
		"authentication failed",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (s *Service) readModelStreamErrorBody(ctx context.Context, streamID string) []byte {
	if s == nil || s.env == nil || strings.TrimSpace(streamID) == "" {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body := make([]byte, 0, 4096)
	for len(body) < maxUpstreamErrorBodyBytes {
		if readCtx.Err() != nil {
			break
		}
		read, errRead := s.readModelStreamContext(readCtx, streamID)
		if errRead != nil {
			break
		}
		if len(read.Payload) > 0 {
			remaining := maxUpstreamErrorBodyBytes - len(body)
			if len(read.Payload) > remaining {
				read.Payload = read.Payload[:remaining]
			}
			body = append(body, read.Payload...)
		}
		if read.Error != "" {
			remaining := maxUpstreamErrorBodyBytes - len(body)
			if remaining > 0 {
				errorText := []byte(read.Error)
				if len(errorText) > remaining {
					errorText = errorText[:remaining]
				}
				body = append(body, errorText...)
			}
		}
		if read.Done {
			break
		}
	}
	return body
}

func classifyTransportFailure(errValue error) (string, string) {
	if errValue == nil {
		return ClassProbeError, "probe transport error"
	}
	text := strings.ToLower(errValue.Error())
	if isHostModelCooldownText(text) {
		return ClassIgnored, "host_call_failed: model cooldown"
	}
	if strings.Contains(text, "proxy") || strings.Contains(text, "resin") || strings.Contains(text, "socks") {
		return ClassProxyError, "probe transport error"
	}
	if isQuotaExhaustionText(text) {
		return ClassQuotaExhausted, "host_call_failed: quota exhausted"
	}
	for _, marker := range []string{"invalid token", "expired", "no auth", "quota", "rate limit", "ratelimit", "too many requests", "permission denied", "forbidden", "unauthorized"} {
		if strings.Contains(text, marker) {
			return ClassIgnored, "probe transport error"
		}
	}
	return ClassProbeError, "probe transport error"
}

func isHostModelCooldownText(text string) bool {
	for _, marker := range []string{
		"model_cooldown",
		"all credentials for model",
		"remaining credentials are cooling down",
		"credential is cooling down",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func classifyTransportError(errValue error) string {
	class, _ := classifyTransportFailure(errValue)
	return class
}

func sseHeaders() http.Header {
	return http.Header{
		"Content-Type":  []string{"text/event-stream"},
		"Cache-Control": []string{"no-cache"},
	}
}

func safeReplayRequest(body []byte, sourceFormat, _ string) bool {
	return safeReplayPayload(decodeRequestPayload(body), sourceFormat)
}

func nonEmptyJSONValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func containsCompactionTrigger(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if strings.EqualFold(strings.TrimSpace(key), "compaction_trigger") {
				return true
			}
			if containsCompactionTrigger(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsCompactionTrigger(child) {
				return true
			}
		}
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "compaction_trigger")
	}
	return false
}

const clientCompactionPromptMarker = "it is a system-generated compaction prompt, not a real user message"

func isResponsesCompactionPayload(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	if hasCompactionTriggerItem(payload["input"]) {
		return true
	}
	return lastItemLooksLikeCompactionPrompt(payload["input"]) || lastItemLooksLikeCompactionPrompt(payload["messages"])
}

func hasCompactionTriggerItem(value any) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, rawItem := range items {
		item, _ := rawItem.(map[string]any)
		if item != nil && strings.EqualFold(strings.TrimSpace(asString(item["type"])), "compaction_trigger") {
			return true
		}
	}
	return false
}

func lastItemLooksLikeCompactionPrompt(value any) bool {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return false
	}
	item, _ := items[len(items)-1].(map[string]any)
	if item == nil || !strings.EqualFold(strings.TrimSpace(asString(item["role"])), "user") {
		return false
	}
	return strings.Contains(strings.ToLower(compactContentText(item["content"])), clientCompactionPromptMarker)
}

func compactContentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var text strings.Builder
		for _, rawPart := range typed {
			part, _ := rawPart.(map[string]any)
			if part == nil {
				continue
			}
			text.WriteString(asString(part["text"]))
		}
		return text.String()
	default:
		return ""
	}
}

func localShellTool(tool map[string]any) bool {
	env, _ := tool["environment"].(map[string]any)
	if env == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(asString(env["type"])), "local")
}

func reasoningDisabledBody(payload map[string]any) bool {
	if effort := strings.ToLower(strings.TrimSpace(asString(payload["reasoning_effort"]))); effort == "none" || effort == "off" {
		return true
	}
	for _, key := range []string{"reasoning", "output_config", "thinking"} {
		if value, ok := payload[key]; ok && disabledReasoningValue(value) {
			return true
		}
	}
	return false
}

func containsUnsafeContentType(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if kind := strings.ToLower(strings.TrimSpace(asString(typed["type"]))); isUnsafeContentType(kind) {
			return true
		}
		for _, child := range typed {
			if containsUnsafeContentType(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsUnsafeContentType(child) {
				return true
			}
		}
	}
	return false
}

func isUnsafeContentType(kind string) bool {
	switch kind {
	case "image", "image_url", "input_image", "audio", "input_audio", "file", "input_file", "video", "input_video", "speech", "realtime", "computer_screenshot":
		return true
	default:
		return false
	}
}

func hasNonTextModality(value any) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, raw := range values {
		switch strings.ToLower(strings.TrimSpace(asString(raw))) {
		case "", "text":
			continue
		default:
			return true
		}
	}
	return false
}
