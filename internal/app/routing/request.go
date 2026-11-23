package routing

import (
	"encoding/json"
	execapp "grok-quality-guard/internal/app/executor"
	"net/http"
	"strings"
)

func (s *Service) prepareTransparentRequest(req Request) (Facts, error) {
	body, sourceFormat, errInput := execapp.ExecutorRequestInput(req)
	if errInput != nil {
		return Facts{}, errInput
	}
	payload := decodeRequestPayload(body)
	model := executionModelFromPayload(req, payload)
	var nonReasoning []string
	if s.env != nil {
		nonReasoning = s.env.Policy().Quality.NonReasoningModels
	}
	reasoningNeeded := reasoningRequiredForModelPayloadWithNonReasoning(model, payload, nonReasoning)
	body = ensureTransparentReasoningEvidence(body, sourceFormat, reasoningNeeded)
	return Facts{
		Body:              body,
		SourceFormat:      sourceFormat,
		Model:             model,
		ReasoningRequired: reasoningNeeded,
		Replayable:        safeReplayPayload(payload, sourceFormat),
		SessionSeed:       extractSelectionSessionSeedPayload(req.Headers, payload),
	}, nil
}

func executionModelFromPayload(req Request, payload map[string]any) string {
	bodyModel := ""
	if payload != nil {
		bodyModel = strings.TrimSpace(asString(payload["model"]))
	}
	return firstNonEmpty(req.Model, asString(req.Metadata["model"]), bodyModel, req.Alt)
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
