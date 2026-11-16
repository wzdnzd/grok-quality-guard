package routing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	maxSessionSeedBytes      = 1024
	maxCodexTurnMetadataSize = 16 << 10
)

func (s *Service) selectionSessionKey(req Request, body []byte, sourceFormat string) string {
	return s.selectionSessionKeyForModel(req, body, sourceFormat, executionModel(req, body))
}

func (s *Service) selectionSessionKeyForModel(req Request, body []byte, sourceFormat, model string) string {
	return s.selectionSessionKeyForSeed(req, sourceFormat, model, extractSelectionSessionSeed(req.Headers, body))
}

func (s *Service) selectionSessionKeyForSeed(req Request, sourceFormat, model, seed string) string {
	if seed == "" {
		return ""
	}
	callerScope := strings.TrimSpace(asString(req.Metadata[pluginapi.CallerScopeMetadataKey]))
	if callerScope == "" {
		callerScope = callerScopeFromHeaders(req.Headers)
	}
	source := "grok-quality-guard:affinity:v1\x00" +
		callerScope + "\x00" +
		"xai\x00" +
		strings.ToLower(strings.TrimSpace(model)) + "\x00" +
		strings.ToLower(strings.TrimSpace(sourceFormat)) + "\x00" +
		seed
	digest := sha256.Sum256([]byte(source))
	return hex.EncodeToString(digest[:])
}

func callerScopeFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	value := firstNonEmpty(headers.Get("Authorization"), headers.Get("X-Api-Key"), headers.Get("X-Goog-Api-Key"))
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return "auth:" + hex.EncodeToString(digest[:])
}

func extractSelectionSessionSeed(headers http.Header, body []byte) string {
	return extractSelectionSessionSeedPayload(headers, decodeRequestPayload(body))
}

func codexSessionSeedFromHeaders(headers http.Header) string {
	if headers == nil {
		return ""
	}
	if seed := codexSessionSeedFromTurnMetadata(headers.Get("X-Codex-Turn-Metadata")); seed != "" {
		return seed
	}
	if seed := normalizeSessionSeed(headers.Get("X-Codex-Window-Id")); seed != "" {
		return "codex:window:" + seed
	}
	return ""
}

func codexSessionSeedFromTurnMetadata(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxCodexTurnMetadataSize {
		return ""
	}
	var metadata struct {
		PromptCacheKey string `json:"prompt_cache_key"`
		WindowID       string `json:"window_id"`
	}
	if json.Unmarshal([]byte(value), &metadata) != nil {
		return ""
	}
	if seed := normalizeSessionSeed(metadata.PromptCacheKey); seed != "" {
		return seed
	}
	if seed := normalizeSessionSeed(metadata.WindowID); seed != "" {
		return "codex:window:" + seed
	}
	return ""
}

func normalizeSessionSeed(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxSessionSeedBytes {
		return ""
	}
	return value
}
