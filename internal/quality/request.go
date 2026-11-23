package quality

import (
	"encoding/json"
	"strings"

	"grok-quality-guard/internal/model"
)

// DecodeRequestPayload parses a JSON object without failing the caller on
// malformed or non-object input.
func DecodeRequestPayload(body []byte) map[string]any {
	if len(body) == 0 {
		return nil
	}
	var payload map[string]any
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return nil
	}
	return payload
}

func decodeRequestPayload(body []byte) map[string]any {
	return DecodeRequestPayload(body)
}

// ReasoningRequiredPayload reports whether the request did not explicitly
// disable reasoning.
func ReasoningRequiredPayload(payload map[string]any) bool {
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

// ReasoningRequiredForModelPayload combines the request flag with model
// capability detection.
func ReasoningRequiredForModelPayload(modelName string, payload map[string]any) bool {
	return ReasoningRequiredForModelPayloadWithNonReasoning(modelName, payload, nil)
}

// ReasoningRequiredForModelPayloadWithNonReasoning applies user-declared
// non-reasoning models before falling back to the requested or body model.
func ReasoningRequiredForModelPayloadWithNonReasoning(modelName string, payload map[string]any, nonReasoning []string) bool {
	if !ReasoningRequiredPayload(payload) {
		return false
	}
	known, supports := model.ReasoningSupportWithExclusions(modelName, nonReasoning)
	if !known && payload != nil {
		if bodyModel := strings.TrimSpace(asString(payload["model"])); bodyModel != "" && bodyModel != modelName {
			known, supports = model.ReasoningSupportWithExclusions(bodyModel, nonReasoning)
		}
	}
	return known && supports
}

func reasoningRequiredPayload(payload map[string]any) bool {
	return ReasoningRequiredPayload(payload)
}

func reasoningRequiredForModelPayload(modelName string, payload map[string]any) bool {
	return ReasoningRequiredForModelPayload(modelName, payload)
}

func reasoningRequiredForModelPayloadWithNonReasoning(modelName string, payload map[string]any, nonReasoning []string) bool {
	return ReasoningRequiredForModelPayloadWithNonReasoning(modelName, payload, nonReasoning)
}
