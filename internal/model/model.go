package model

import "strings"

// ReasoningSupport infers reasoning capability from the model name instead of
// maintaining a table that must be updated for every model release. Grok 4+
// text models reason by default; explicit non-reasoning families and aliases
// override that default. Unknown families stay outside the thinking gate.
func ReasoningSupport(name string) (known bool, supports bool) {
	slug := NormalizeSlug(name)
	if slug == "" {
		return false, false
	}
	if IsKnownNonReasoning(slug) {
		return true, false
	}
	if IsReasoningDisabledAlias(slug) {
		return true, false
	}
	if major, ok := MajorVersion(slug); ok && major >= 4 {
		return true, true
	}
	// Grok 3 Mini is the only pre-4 family that emits reasoning tokens.
	if strings.HasPrefix(slug, "grok-3-mini") {
		return true, true
	}
	return false, false
}

// NormalizeSlug keeps the last provider/model path segment in lowercase.
func NormalizeSlug(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if idx := strings.LastIndexByte(normalized, '/'); idx >= 0 {
		normalized = strings.TrimSpace(normalized[idx+1:])
	}
	return normalized
}

// IsKnownNonReasoning reports model families that do not emit reasoning.
func IsKnownNonReasoning(slug string) bool {
	switch {
	case strings.HasPrefix(slug, "grok-composer-"),
		strings.HasPrefix(slug, "grok-build-"),
		strings.HasPrefix(slug, "grok-imagine-"):
		return true
	}
	for _, marker := range []string{"non-reasoning", "non_reasoning", "nonreasoning", "no-reasoning"} {
		if strings.Contains(slug, marker) {
			return true
		}
	}
	return false
}

// IsReasoningDisabledAlias reports names that explicitly disable reasoning.
func IsReasoningDisabledAlias(slug string) bool {
	return strings.HasSuffix(slug, "-none") || strings.HasSuffix(slug, "-off")
}

// MajorVersion extracts the leading Grok major version from a normalized slug.
func MajorVersion(slug string) (int, bool) {
	const prefix = "grok-"
	if !strings.HasPrefix(slug, prefix) {
		return 0, false
	}
	rest := slug[len(prefix):]
	end := 0
	major := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		major = major*10 + int(rest[end]-'0')
		end++
	}
	if end == 0 {
		return 0, false
	}
	if end < len(rest) && rest[end] != '.' && rest[end] != '-' && rest[end] != '_' {
		return 0, false
	}
	return major, true
}
