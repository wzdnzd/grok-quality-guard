package model

import "testing"

func TestReasoningSupportUsesKnownFamiliesAndVersionFallback(t *testing.T) {
	for _, name := range []string{
		"grok-4.8",
		"grok-5.0-xhigh",
		"Build/grok-4.7",
		"grok-4.5-low",
		"grok-4.3-high",
		"grok-4.20-0309-reasoning",
		"grok-4.20-multi-agent-0309",
		"grok-3-mini-fast",
	} {
		known, supports := ReasoningSupport(name)
		if !known || !supports {
			t.Fatalf("ReasoningSupport(%q) = known=%t supports=%t", name, known, supports)
		}
	}
	for _, name := range []string{"grok-composer-2.5-fast", "grok-build-0.1", "grok-4.20-0309-non-reasoning", "grok-4.6-none", "custom-grok"} {
		known, supports := ReasoningSupport(name)
		if known && supports {
			t.Fatalf("ReasoningSupport(%q) unexpectedly requires thinking", name)
		}
	}
}

func TestReasoningSupportWithExclusionsOverridesVersionFallback(t *testing.T) {
	known, supports := ReasoningSupportWithExclusions("Build/grok-4.8", []string{"grok-4.8"})
	if !known || supports {
		t.Fatalf("configured non-reasoning model = known=%t supports=%t", known, supports)
	}
	known, supports = ReasoningSupportWithExclusions("grok-4.8", []string{"custom-other"})
	if !known || !supports {
		t.Fatalf("unrelated exclusion changed capability = known=%t supports=%t", known, supports)
	}
}

func TestReasoningSupportWithExclusionsMatchesEffortAliases(t *testing.T) {
	for _, test := range []struct {
		name       string
		configured string
	}{
		{name: "Build/grok-4.8-low", configured: "grok-4.8"},
		{name: "grok-4.8-xhigh", configured: "Grok-4.8"},
		{name: "grok-4.8-none", configured: "grok-4.8"},
	} {
		known, supports := ReasoningSupportWithExclusions(test.name, []string{test.configured})
		if !known || supports {
			t.Fatalf("ReasoningSupportWithExclusions(%q, %q) = known=%t supports=%t", test.name, test.configured, known, supports)
		}
	}
	known, supports := ReasoningSupportWithExclusions("grok-4.8-xhigh", []string{"grok-4.8-low"})
	if !known || !supports {
		t.Fatalf("effort-specific exclusion overmatched: known=%t supports=%t", known, supports)
	}
	known, supports = ReasoningSupportWithExclusions("grok-4.8-fast", []string{"grok-4.8"})
	if !known || !supports {
		t.Fatalf("non-effort suffix was overmatched: known=%t supports=%t", known, supports)
	}
}

func TestReasoningFamilyMatchUsesSlugBoundaries(t *testing.T) {
	cases := []struct {
		slug   string
		family string
		want   bool
	}{
		{slug: "grok-4.3", family: "grok-4.3", want: true},
		{slug: "grok-4.3-low", family: "grok-4.3", want: true},
		{slug: "grok-4.30", family: "grok-4.3", want: false},
		{slug: "grok-4.20-multi-agent-0309", family: "grok-4.20-multi-agent", want: true},
	}
	for _, test := range cases {
		if got := matchesReasoningFamily(test.slug, test.family); got != test.want {
			t.Fatalf("matchesReasoningFamily(%q, %q) = %t, want %t", test.slug, test.family, got, test.want)
		}
	}
}
