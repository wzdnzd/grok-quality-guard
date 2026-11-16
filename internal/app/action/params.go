package action

import (
	"strings"

	"grok-quality-guard/internal/quality"
)

func String(value any) string {
	return quality.AsString(value)
}

func Int(values map[string]any, key string, fallback int) int {
	if value, ok := quality.Numeric(values[key]); ok {
		return int(value)
	}
	return fallback
}

func Bool(values map[string]any, key string) bool {
	if value, ok := values[key].(bool); ok {
		return value
	}
	if text := strings.ToLower(strings.TrimSpace(String(values[key]))); text != "" {
		return text == "1" || text == "true" || text == "yes"
	}
	return false
}

func AuthIDs(values map[string]any) []string {
	if rawIDs, ok := values["auth_ids"].([]any); ok {
		out := make([]string, 0, len(rawIDs))
		seen := make(map[string]struct{}, len(rawIDs))
		for _, rawID := range rawIDs {
			authID := strings.TrimSpace(String(rawID))
			if authID == "" {
				continue
			}
			if _, exists := seen[authID]; exists {
				continue
			}
			seen[authID] = struct{}{}
			out = append(out, authID)
		}
		return out
	}
	authID := strings.TrimSpace(String(values["auth_id"]))
	if authID == "" {
		return nil
	}
	return []string{authID}
}
