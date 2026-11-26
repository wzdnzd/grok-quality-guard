package model

import "strings"

// Known reasoning families keep the built-in capability table explicit for
// current Grok models. Newer Grok 4+ names still fall back to the version
// heuristic so a model release does not silently disable the thinking gate.
var knownReasoningFamilies = []string{
	"grok-3-mini",
	"grok-4.3",
	"grok-4.5",
	"grok-4.6",
	"grok-4.7",
	"grok-4.20-0309-reasoning",
	"grok-4.20-multi-agent",
}

// knownReasoningEffortSuffixes mirrors the upstream gateway's effort aliases so a configured
// base model also covers aliases such as grok-4.8-low.
var knownReasoningEffortSuffixes = []string{
	"-minimal",
	"-xhigh",
	"-medium",
	"-high",
	"-low",
	"-none",
	"-max",
}

// ReasoningSupport infers reasoning capability from the built-in capability
// table, the Grok version fallback, and known non-reasoning families.
func ReasoningSupport(name string) (known bool, supports bool) {
	return ReasoningSupportWithExclusions(name, nil)
}

// ReasoningSupportWithExclusions lets users override the inferred capability
// for models that do not emit reasoning. This follows the upstream gateway's catalog
// profile idea without requiring a code change for every new model.
func ReasoningSupportWithExclusions(name string, nonReasoning []string) (known bool, supports bool) {
	slug := NormalizeSlug(name)
	if slug == "" {
		return false, false
	}
	if matchesConfiguredNonReasoning(slug, nonReasoning) {
		return true, false
	}
	if IsKnownNonReasoning(slug) {
		return true, false
	}
	if IsReasoningDisabledAlias(slug) {
		return true, false
	}
	for _, family := range knownReasoningFamilies {
		if matchesReasoningFamily(slug, family) {
			return true, true
		}
	}
	if major, ok := MajorVersion(slug); ok && major >= 4 {
		// Prefer catching degraded output when a new Grok 4+ model is not in
		// the table yet. Users can add exceptions through non_reasoning_models.
		return true, true
	}
	return false, false
}

func matchesReasoningFamily(slug, family string) bool {
	if slug == family {
		return true
	}
	return len(slug) > len(family) && strings.HasPrefix(slug, family) && slug[len(family)] == '-'
}

func matchesConfiguredNonReasoning(slug string, configured []string) bool {
	base := stripKnownReasoningEffortSuffix(slug)
	for _, value := range configured {
		normalized := NormalizeSlug(value)
		if normalized == slug || normalized == base {
			return true
		}
	}
	return false
}

func stripKnownReasoningEffortSuffix(slug string) string {
	for _, suffix := range knownReasoningEffortSuffixes {
		if len(slug) > len(suffix) && strings.HasSuffix(slug, suffix) {
			return strings.TrimSuffix(slug, suffix)
		}
	}
	return slug
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
