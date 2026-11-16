package executor

import "strings"

func safeReplayPayload(payload map[string]any, sourceFormat string) bool {
	if payload == nil {
		return false
	}
	format := strings.ToLower(strings.TrimSpace(sourceFormat))
	if strings.Contains(format, "image") || strings.Contains(format, "video") || strings.Contains(format, "audio") || strings.Contains(format, "speech") || strings.Contains(format, "realtime") || strings.Contains(format, "websocket") {
		return false
	}
	if nonEmptyJSONValue(payload["previous_response_id"]) || nonEmptyJSONValue(payload["conversation"]) {
		return false
	}
	for _, key := range []string{"store", "background"} {
		if value, ok := payload[key].(bool); ok && value {
			return false
		}
	}
	if containsCompactionTrigger(payload) || containsUnsafeContentType(payload) {
		return false
	}
	if hasNonTextModality(payload["modalities"]) {
		return false
	}
	if value, ok := payload["web_search_options"]; ok && value != nil {
		return false
	}
	if nonEmptyJSONValue(payload["mcp_servers"]) {
		return false
	}
	if rawTools, okTools := payload["tools"]; okTools {
		tools, ok := rawTools.([]any)
		if !ok {
			return false
		}
		for _, rawTool := range tools {
			tool, _ := rawTool.(map[string]any)
			if tool == nil {
				continue
			}
			kind := strings.ToLower(asString(tool["type"]))
			switch kind {
			case "function", "custom", "local_shell", "apply_patch", "tool_search", "shell":
				if kind == "shell" && !localShellTool(tool) {
					return false
				}
			default:
				return false
			}
		}
	}
	return !reasoningDisabledBody(payload)
}
