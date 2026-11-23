package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

func IsGrokProvider(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(normalized, "xai") || strings.Contains(normalized, "grok")
}

func RawString(raw map[string]json.RawMessage, key string) string {
	if raw == nil {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw[key], &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func RawBool(raw map[string]json.RawMessage, key string) bool {
	if raw == nil {
		return false
	}
	var value bool
	if err := json.Unmarshal(raw[key], &value); err == nil {
		return value
	}
	var text string
	if err := json.Unmarshal(raw[key], &text); err == nil {
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(text))
		return errParse == nil && parsed
	}
	return false
}

func RawMap(raw map[string]json.RawMessage, key string) map[string]any {
	if raw == nil {
		return nil
	}
	var value map[string]any
	if err := json.Unmarshal(raw[key], &value); err != nil {
		return nil
	}
	return value
}

func SetRawBool(raw map[string]json.RawMessage, key string, value bool) {
	if raw == nil {
		return
	}
	encoded, _ := json.Marshal(value)
	raw[key] = encoded
}

func SetRawString(raw map[string]json.RawMessage, key, value string) {
	if raw == nil {
		return
	}
	encoded, _ := json.Marshal(value)
	raw[key] = encoded
}

func IsProbeHeader(headers map[string][]string) bool {
	for key, values := range headers {
		if !strings.EqualFold(key, "X-Grok-Quality-Probe") {
			continue
		}
		for _, value := range values {
			if strings.EqualFold(strings.TrimSpace(value), "1") || strings.EqualFold(strings.TrimSpace(value), "true") {
				return true
			}
		}
	}
	return false
}
func Disabled(raw map[string]json.RawMessage) bool {
	return RawBool(raw, "disabled")
}

// Email returns the trimmed top-level or metadata email from auth JSON.
func Email(raw map[string]json.RawMessage) string {
	if email := RawString(raw, "email"); email != "" {
		return email
	}
	if meta := RawMap(raw, "metadata"); meta != nil {
		if email := strings.TrimSpace(stringValue(meta["email"])); email != "" {
			return email
		}
	}
	return ""
}

// Label returns the first available account label from common auth fields.
func Label(raw map[string]json.RawMessage) string {
	for _, key := range []string{"label", "name", "account"} {
		if value := RawString(raw, key); value != "" {
			return value
		}
	}
	if meta := RawMap(raw, "metadata"); meta != nil {
		for _, key := range []string{"label", "name", "account"} {
			if value := strings.TrimSpace(stringValue(meta[key])); value != "" {
				return value
			}
		}
	}
	return ""
}

// AccountHash is the short stable identity used for generated account names.
func AccountHash(raw map[string]json.RawMessage) string {
	seed := strings.Join([]string{
		RawString(raw, "id"),
		RawString(raw, "email"),
		RawString(raw, "sub"),
		RawString(raw, "subject"),
	}, "|")
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])[:8]
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	default:
		return ""
	}
}
func IsGrokAuth(raw map[string]json.RawMessage) bool {
	if raw == nil {
		return false
	}
	for _, key := range []string{"type", "provider", "oauth_provider", "base_url"} {
		if IsGrokProvider(RawString(raw, key)) {
			return true
		}
	}
	return false
}
