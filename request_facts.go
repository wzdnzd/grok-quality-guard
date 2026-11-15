package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

type transparentRequestFacts struct {
	Body              []byte
	SourceFormat      string
	Model             string
	ReasoningRequired bool
	Replayable        bool
	SessionSeed       string
}

func (p *Plugin) prepareTransparentRequest(req rpcExecutorRequest) (transparentRequestFacts, error) {
	body, sourceFormat, errInput := executorRequestInput(req)
	if errInput != nil {
		return transparentRequestFacts{}, errInput
	}
	payload := decodeRequestPayload(body)
	model := executionModelFromPayload(req, payload)
	reasoningNeeded := reasoningRequiredForModelPayload(model, payload)
	body = ensureTransparentReasoningEvidence(body, sourceFormat, reasoningNeeded)
	return transparentRequestFacts{
		Body:              body,
		SourceFormat:      sourceFormat,
		Model:             model,
		ReasoningRequired: reasoningNeeded,
		Replayable:        safeReplayPayload(payload, sourceFormat),
		SessionSeed:       extractSelectionSessionSeedPayload(req.Headers, payload),
	}, nil
}

func decodeRequestPayload(body []byte) map[string]any {
	if len(body) == 0 {
		return nil
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil
	}
	return payload
}

func executionModelFromPayload(req rpcExecutorRequest, payload map[string]any) string {
	bodyModel := ""
	if payload != nil {
		bodyModel = strings.TrimSpace(asString(payload["model"]))
	}
	return firstNonEmpty(req.Model, asString(req.Metadata["model"]), bodyModel, req.Alt)
}

func reasoningRequiredPayload(payload map[string]any) bool {
	if payload == nil {
		return true
	}
	if effort := strings.ToLower(strings.TrimSpace(asString(payload["reasoning_effort"]))); effort == "none" || effort == "off" {
		return false
	}
	for _, key := range []string{"reasoning", "output_config", "thinking"} {
		if raw, ok := payload[key]; ok && disabledReasoningValue(raw) {
			return false
		}
	}
	return true
}

func reasoningRequiredForModelPayload(model string, payload map[string]any) bool {
	if !reasoningRequiredPayload(payload) {
		return false
	}
	known, supports := modelReasoningSupport(model)
	if !known && payload != nil {
		if bodyModel := strings.TrimSpace(asString(payload["model"])); bodyModel != "" && bodyModel != model {
			known, supports = modelReasoningSupport(bodyModel)
		}
	}
	return known && supports
}

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

func extractSelectionSessionSeedPayload(headers http.Header, payload map[string]any) string {
	if headers != nil {
		if seed := normalizeSessionSeed(headers.Get("X-Claude-Code-Session-Id")); seed != "" {
			return seed
		}
		if seed := codexSessionSeedFromHeaders(headers); seed != "" {
			return seed
		}
		for _, name := range []string{
			"X-Session-Id", "Session-Id", "Session_id",
			"X-Conversation-Id", "Conversation-Id", "Conversation_id",
			"X-Client-Session-Id", "X-Grok-Conv-Id",
		} {
			if seed := normalizeSessionSeed(headers.Get(name)); seed != "" {
				return seed
			}
		}
	}
	if payload == nil {
		return ""
	}
	if seed := normalizeSessionSeed(asString(payload["prompt_cache_key"])); seed != "" {
		return seed
	}
	metadata, _ := payload["metadata"].(map[string]any)
	if seed := normalizeSessionSeed(asString(metadata["session_id"])); seed != "" {
		return seed
	}
	if seed := normalizeSessionSeed(asString(metadata["sessionId"])); seed != "" {
		return seed
	}
	clientMetadata, _ := payload["client_metadata"].(map[string]any)
	if seed := codexSessionSeedFromAny(clientMetadata["x-codex-turn-metadata"]); seed != "" {
		return seed
	}
	if seed := normalizeSessionSeed(asString(clientMetadata["x-codex-window-id"])); seed != "" {
		return "codex:window:" + seed
	}
	if seed := normalizeSessionSeed(asString(payload["session_id"])); seed != "" {
		return seed
	}
	if seed := normalizeSessionSeed(asString(payload["sessionId"])); seed != "" {
		return seed
	}
	if seed := normalizeSessionSeed(asString(payload["conversation_id"])); seed != "" {
		return seed
	}
	if seed := normalizeSessionSeed(asString(payload["conversationId"])); seed != "" {
		return seed
	}
	return conversationSessionSeedFromAny(payload["conversation"])
}

func codexSessionSeedFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return codexSessionSeedFromTurnMetadata(typed)
	case map[string]any:
		encoded, errMarshal := json.Marshal(typed)
		if errMarshal != nil {
			return ""
		}
		return codexSessionSeedFromTurnMetadata(string(encoded))
	default:
		return ""
	}
}

func conversationSessionSeedFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return normalizeSessionSeed(typed)
	case map[string]any:
		return normalizeSessionSeed(asString(typed["id"]))
	default:
		return ""
	}
}
