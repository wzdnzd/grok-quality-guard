package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestDockerStatePathUsesPluginsDataLayout(t *testing.T) {
	const want = "/CLIProxyAPI/plugins/data/grok-quality-guard/state.json"
	if dockerStatePath != want {
		t.Fatalf("dockerStatePath = %q, want %q", dockerStatePath, want)
	}
}

func TestDecodeConfigPartialKeepsDefaults(t *testing.T) {
	cfg, errDecode := Decode([]byte("auto_action: cooldown_then_disable\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if !cfg.PassiveGuard || cfg.TransparentGuard {
		t.Fatalf("unexpected guard defaults: passive=%v transparent=%v", cfg.PassiveGuard, cfg.TransparentGuard)
	}
	if cfg.AutoAction != "cooldown_then_disable" {
		t.Fatalf("auto_action = %q", cfg.AutoAction)
	}
	if cfg.Quality.SoftTPS != 500 || cfg.Quality.HardTPS != 1000 || cfg.Quality.MinGenerationMS != 1000 {
		t.Fatalf("unexpected quality defaults: %#v", cfg.Quality)
	}
	if cfg.Quality.HoldTimeout.Duration != 30*time.Second || cfg.Quality.MissingThinkingCooldown.Duration != 12*time.Hour {
		t.Fatalf("unexpected durations: %#v", cfg.Quality)
	}
}

func TestDecodeConfigResinLocalOnly(t *testing.T) {
	cfg, errDecode := Decode([]byte("resin:\n  enabled: true\n  local_only: true\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if !cfg.Resin.Enabled || !cfg.Resin.LocalOnly {
		t.Fatalf("unexpected local-only resin config: %#v", cfg.Resin)
	}
}
func TestNormalizePolicyRejectsInvalidTPS(t *testing.T) {
	policy := PolicyFromConfig(DefaultConfig())
	policy.Quality.SoftTPS = 1000
	policy.Quality.HardTPS = 500
	if _, errNormalize := NormalizePolicy(policy); errNormalize == nil {
		t.Fatal("expected invalid TPS policy error")
	}
}

func TestConfigRevisionStable(t *testing.T) {
	firstRevision := Revision([]byte("a: 1"))
	secondRevision := Revision([]byte("a: 1"))
	if firstRevision != secondRevision {
		t.Fatal("same config should produce same revision")
	}
	if Revision([]byte("a: 1")) == Revision([]byte("a: 2")) {
		t.Fatal("different config should produce different revision")
	}
}

func TestDecodeConfigCooldownOnly(t *testing.T) {
	cfg, errDecode := Decode([]byte("auto_action: cooldown_only\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if cfg.AutoAction != "cooldown_only" {
		t.Fatalf("auto_action = %q", cfg.AutoAction)
	}
}

func TestProbeQueueConfigDefaultsAndBounds(t *testing.T) {
	cfg, errDecode := Decode([]byte("probe:\n  concurrency: 2\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if cfg.Probe.QueueSize != defaultProbeQueueSize {
		t.Fatalf("probe.queue_size = %d, want %d", cfg.Probe.QueueSize, defaultProbeQueueSize)
	}
	if cfg.Probe.UnknownJitter.Duration != defaultProbeUnknownJitter {
		t.Fatalf("probe.unknown_jitter = %s, want %s", cfg.Probe.UnknownJitter.Duration, defaultProbeUnknownJitter)
	}

	tooLarge := DefaultConfig()
	tooLarge.Probe.QueueSize = maxProbeQueueSize + 1
	if errNormalize := normalizeConfig(&tooLarge); errNormalize == nil {
		t.Fatal("expected oversized probe queue error")
	}
}
func TestDecodeConfigRoutingDefaults(t *testing.T) {
	cfg, errDecode := Decode(nil)
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if cfg.Routing.SelectionStrategy != "round_robin" || !cfg.Routing.PreferLastSuccess || !cfg.Routing.SessionAffinity {
		t.Fatalf("unexpected routing defaults: %#v", cfg.Routing)
	}
	if cfg.Routing.SessionAffinityTTL.Duration != time.Hour || cfg.Routing.SessionAffinityMaxSize != 10000 {
		t.Fatalf("unexpected routing affinity defaults: %#v", cfg.Routing)
	}
}

func TestDecodeConfigRoutingValidation(t *testing.T) {
	for _, raw := range []string{
		"routing:\n  selection_strategy: unsupported\n",
		"routing:\n  session_affinity_ttl: 169h\n",
		"routing:\n  session_affinity_max_entries: 100001\n",
	} {
		if _, errDecode := Decode([]byte(raw)); errDecode == nil {
			t.Fatalf("expected routing config error for %q", raw)
		}
	}
}
func TestDecodeConfigPartialRoutingKeepsBooleanDefaults(t *testing.T) {
	cfg, errDecode := Decode([]byte("routing:\n  selection_strategy: fill_first\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if cfg.Routing.SelectionStrategy != "fill_first" || !cfg.Routing.PreferLastSuccess || !cfg.Routing.SessionAffinity {
		t.Fatalf("partial routing config lost defaults: %#v", cfg.Routing)
	}
}
func TestPolicyJSONWithoutRoutingUsesRoutingDefaults(t *testing.T) {
	var policy Policy
	if errUnmarshal := json.Unmarshal([]byte(`{"passive_guard":true,"auto_action":"observe"}`), &policy); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if policy.Routing.SelectionStrategy != "round_robin" || !policy.Routing.PreferLastSuccess || !policy.Routing.SessionAffinity {
		t.Fatalf("old state policy did not inherit routing defaults: %#v", policy.Routing)
	}
	if policy.Routing.SessionAffinityTTL.Duration != time.Hour || policy.Routing.SessionAffinityMaxSize != 10000 {
		t.Fatalf("old state policy did not inherit affinity defaults: %#v", policy.Routing)
	}
}
func TestLegacyPolicyQualityUsesNewDefaults(t *testing.T) {
	var policy Policy
	if errUnmarshal := json.Unmarshal([]byte(`{"quality":{"soft_tps":500,"hard_tps":1000,"min_generation_ms":1000}}`), &policy); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	normalized, errNormalize := normalizePolicy(policy)
	if errNormalize != nil {
		t.Fatal(errNormalize)
	}
	quality := normalized.Quality
	if quality.ConsecutiveSoft != 2 || quality.ConsecutiveErrors != 2 || quality.ConsecutiveMissingThinking != 1 {
		t.Fatalf("unexpected consecutive defaults: %#v", quality)
	}
	if !quality.ThinkingGuard || quality.SoftCrossVerify || quality.ThinkingCrossVerify {
		t.Fatalf("unexpected thinking defaults: %#v", quality)
	}
}

func TestStrikeDecayDefaultsAndDisable(t *testing.T) {
	defaults := DefaultQualityConfig()
	if defaults.StrikeDecayAfterHours != 48 || defaults.StrikeDecayMinHealthy != 10 {
		t.Fatalf("unexpected strike decay defaults: %#v", defaults)
	}
	custom := defaults
	custom.StrikeDecayAfterHours = 0
	custom.StrikeDecayMinHealthy = 0
	if errNormalize := normalizeQuality(&custom); errNormalize != nil {
		t.Fatal(errNormalize)
	}
	if custom.StrikeDecayAfterHours != 0 || custom.StrikeDecayMinHealthy != 10 {
		t.Fatalf("unexpected disabled decay normalization: %#v", custom)
	}
}
