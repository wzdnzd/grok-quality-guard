package config

import (
	"encoding/json"
	"strings"
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
	if cfg.Guard != "passive" {
		t.Fatalf("unexpected guard defaults: guard=%q", cfg.Guard)
	}
	if cfg.AutoAction != "cooldown_then_disable" {
		t.Fatalf("auto_action = %q", cfg.AutoAction)
	}
	if cfg.Quality.SoftTPS != 500 || cfg.Quality.HardTPS != 1000 || cfg.Quality.MinGenerationMS != 1000 {
		t.Fatalf("unexpected quality defaults: %#v", cfg.Quality)
	}
	if cfg.Quality.HoldTimeout.Duration != 30*time.Second || cfg.Quality.StreamIdleTimeout.Duration != 90*time.Second || cfg.Quality.AttemptTimeout.Duration != 120*time.Second || cfg.Quality.RequestTimeout.Duration != 240*time.Second || cfg.Quality.HoldKeepalive.Duration != 5*time.Second || cfg.Quality.MissingThinkingCooldown.Duration != 12*time.Hour {
		t.Fatalf("unexpected durations: %#v", cfg.Quality)
	}
	if cfg.Quality.ShadowMode {
		t.Fatalf("shadow mode defaulted to enabled: %#v", cfg.Quality)
	}
	if cfg.Quality.MinEncryptedBytes != 256 || cfg.Quality.EncryptedBytesPerReasoningToken != 2 {
		t.Fatalf("unexpected encrypted floor defaults: min=%d per_token=%d", cfg.Quality.MinEncryptedBytes, cfg.Quality.EncryptedBytesPerReasoningToken)
	}
}

func TestDecodeConfigNormalizesUserNonReasoningModels(t *testing.T) {
	cfg, errDecode := Decode([]byte("quality:\n  non_reasoning_models:\n    - Build/Grok-4.8\n    - build/grok-4.8\n    - '  grok-9  '\n    - ''\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	want := []string{"build/grok-4.8", "grok-9"}
	if len(cfg.Quality.NonReasoningModels) != len(want) {
		t.Fatalf("non_reasoning_models = %#v, want %#v", cfg.Quality.NonReasoningModels, want)
	}
	for index := range want {
		if cfg.Quality.NonReasoningModels[index] != want[index] {
			t.Fatalf("non_reasoning_models = %#v, want %#v", cfg.Quality.NonReasoningModels, want)
		}
	}
}

func TestDecodeConfigAcceptsHostManagedStoreMetadata(t *testing.T) {
	raw := []byte(`enabled: true
priority: 10
store:
  schema-version: 2
  id: grok-quality-guard
  version: 0.2.9
  release-tag: v0.2.9
  source-id: source-example
  install:
    type: github-release
`)
	cfg, errDecode := Decode(raw)
	if errDecode != nil {
		t.Fatalf("host-managed store metadata was rejected: %v", errDecode)
	}
	if !cfg.Enabled || cfg.Priority != 10 {
		t.Fatalf("host-managed config was not decoded: enabled=%v priority=%d", cfg.Enabled, cfg.Priority)
	}
}
func TestDecodeConfigRejectsRemovedResinBlock(t *testing.T) {
	_, errDecode := Decode([]byte("resin:\n  enabled: true\n"))
	if errDecode == nil || !strings.Contains(errDecode.Error(), "field resin not found") {
		t.Fatalf("removed Resin config block was accepted: %v", errDecode)
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
	if cfg.Probe.ScanInterval.Duration != defaultProbeScanInterval {
		t.Fatalf("probe.scan_interval = %s, want %s", cfg.Probe.ScanInterval.Duration, defaultProbeScanInterval)
	}
	if cfg.Probe.HealthyIntervalHours != defaultProbeHealthyIntervalHours {
		t.Fatalf("probe.healthy_interval_hours = %d, want %d", cfg.Probe.HealthyIntervalHours, defaultProbeHealthyIntervalHours)
	}
	configured, errConfigured := Decode([]byte("probe:\n  healthy_interval_hours: 6\n"))
	if errConfigured != nil {
		t.Fatalf("Decode() configured healthy interval error = %v", errConfigured)
	}
	if configured.Probe.HealthyIntervalHours != 6 {
		t.Fatalf("probe.healthy_interval_hours = %d, want 6", configured.Probe.HealthyIntervalHours)
	}
	if _, errNegative := Decode([]byte("probe:\n  healthy_interval_hours: -1\n")); errNegative == nil {
		t.Fatal("expected negative probe healthy interval error")
	}

	tooLarge := DefaultConfig()
	tooLarge.Probe.QueueSize = maxProbeQueueSize + 1
	if errNormalize := normalizeConfig(&tooLarge); errNormalize == nil {
		t.Fatal("expected oversized probe queue error")
	}
	tooFast := DefaultConfig()
	tooFast.Probe.ScanInterval = NewDuration(500 * time.Millisecond)
	if errNormalize := normalizeConfig(&tooFast); errNormalize == nil {
		t.Fatal("expected undersized probe scan interval error")
	}
}

func TestTaskHistoryConfigDefaultsAndBounds(t *testing.T) {
	cfg, errDecode := Decode([]byte("task_history:\n  enabled: false\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if cfg.TaskHistory.Enabled || cfg.TaskHistory.RetentionDays != 3 || cfg.TaskHistory.MaxRecords != 500 || cfg.TaskHistory.MaxBytes != 128<<20 || cfg.TaskHistory.MaxItemsPerTask != 10000 {
		t.Fatalf("unexpected task history config: %#v", cfg.TaskHistory)
	}
	for _, raw := range []string{
		"task_history:\n  retention_days: 366\n",
		"task_history:\n  max_records: 10001\n",
		"task_history:\n  max_bytes: 4294967297\n",
		"task_history:\n  max_items_per_task: 100001\n",
	} {
		if _, errDecode := Decode([]byte(raw)); errDecode == nil {
			t.Fatalf("expected task history bounds error for %q", raw)
		}
	}
}

func TestDecodeConfigRoutingDefaults(t *testing.T) {
	cfg, errDecode := Decode(nil)
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if cfg.Routing.Strategy.Selection != "round_robin" || !cfg.Routing.Strategy.PreferLastSuccess || !cfg.Routing.Affinity.Enabled {
		t.Fatalf("unexpected routing defaults: %#v", cfg.Routing)
	}
	if cfg.Routing.Affinity.TTL.Duration != time.Hour || cfg.Routing.Affinity.MaxEntries != 10000 {
		t.Fatalf("unexpected routing affinity defaults: %#v", cfg.Routing)
	}
	if !cfg.Routing.Scoring.Enabled || cfg.Routing.Scoring.MaxAccountsPerBucket != defaultRoutingMaxAccountsPerBucket {
		t.Fatalf("unexpected routing scoring defaults: %#v", cfg.Routing.Scoring)
	}
	if cfg.Routing.Scoring.Refresh.MinInterval.Duration != defaultRoutingRefreshMinInterval || cfg.Routing.Scoring.Refresh.MaxAge.Duration != defaultRoutingRefreshMaxAge {
		t.Fatalf("unexpected routing refresh defaults: %#v", cfg.Routing.Scoring.Refresh)
	}
}

func TestDecodeConfigRoutingValidation(t *testing.T) {
	for _, raw := range []string{
		"routing:\n  strategy:\n    selection: unsupported\n",
		"routing:\n  affinity:\n    ttl: 169h\n",
		"routing:\n  affinity:\n    max_entries: 100001\n",
		"routing:\n  scoring:\n    max_accounts_per_bucket: 15\n",
		"routing:\n  scoring:\n    max_accounts_per_bucket: 4097\n",
		"routing:\n  scoring:\n    weights:\n      success: 0\n      ttft: 0\n      tps: 0\n",
		"routing:\n  scoring:\n    weights:\n      success: 1.1\n",
		"routing:\n  scoring:\n    evidence:\n      probe: 0\n      transparent: 0\n      passive: 0\n",
		"routing:\n  scoring:\n    evidence:\n      probe: 1.1\n",
		"routing:\n  scoring:\n    refresh:\n      min_interval: 5m\n      max_age: 1m\n",
		"routing:\n  scoring:\n    refresh:\n      drift_threshold: 1.1\n",
	} {
		if _, errDecode := Decode([]byte(raw)); errDecode == nil {
			t.Fatalf("expected routing config error for %q", raw)
		}
	}
}

func TestDecodeConfigRoutingWeightsAllowInclusiveUpperBound(t *testing.T) {
	cfg, errDecode := Decode([]byte("routing:\n  scoring:\n    weights:\n      success: 1\n      ttft: 0\n      tps: 0\n    evidence:\n      probe: 0\n      transparent: 1\n      passive: 0\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if cfg.Routing.Scoring.Weights.Success != 1 || cfg.Routing.Scoring.Evidence.Transparent != 1 {
		t.Fatalf("inclusive weight bounds were not preserved: %#v", cfg.Routing.Scoring)
	}
}

func TestDecodeConfigRejectsLegacyFlatRouting(t *testing.T) {
	if _, errDecode := Decode([]byte("routing:\n  selection_strategy: round_robin\n")); errDecode == nil {
		t.Fatal("legacy flat routing fields must be rejected instead of migrated")
	}
}

func TestDecodeConfigPartialRoutingKeepsBooleanDefaults(t *testing.T) {
	cfg, errDecode := Decode([]byte("routing:\n  strategy:\n    selection: fill_first\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	if cfg.Routing.Strategy.Selection != "fill_first" || !cfg.Routing.Strategy.PreferLastSuccess || !cfg.Routing.Affinity.Enabled {
		t.Fatalf("partial routing config lost defaults: %#v", cfg.Routing)
	}
}

func TestDecodeConfigPartialRoutingScoringKeepsDefaults(t *testing.T) {
	cfg, errDecode := Decode([]byte("routing:\n  scoring:\n    max_accounts_per_bucket: 64\n    weights:\n      tps: 0.7\n"))
	if errDecode != nil {
		t.Fatalf("Decode() error = %v", errDecode)
	}
	scoring := cfg.Routing.Scoring
	if scoring.MaxAccountsPerBucket != 64 || scoring.Weights.Success != 0.5 || scoring.Weights.TTFT != 0.3 || scoring.Weights.TPS != 0.7 {
		t.Fatalf("partial scoring config lost defaults: %#v", scoring)
	}
	if scoring.Evidence.Probe != 0.65 || scoring.Evidence.Transparent != 0.35 || scoring.Evidence.Passive != 0 {
		t.Fatalf("partial scoring config lost evidence defaults: %#v", scoring.Evidence)
	}
	if scoring.Refresh.MinInterval.Duration != defaultRoutingRefreshMinInterval || scoring.Refresh.MaxAge.Duration != defaultRoutingRefreshMaxAge {
		t.Fatalf("partial scoring config lost refresh defaults: %#v", scoring.Refresh)
	}
}

func TestPolicyJSONWithoutRoutingUsesRoutingDefaults(t *testing.T) {
	var policy Policy
	if errUnmarshal := json.Unmarshal([]byte(`{"guard":"passive","auto_action":"observe"}`), &policy); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if policy.Routing.Strategy.Selection != "round_robin" || !policy.Routing.Strategy.PreferLastSuccess || !policy.Routing.Affinity.Enabled {
		t.Fatalf("policy without routing did not inherit routing defaults: %#v", policy.Routing)
	}
	if policy.Routing.Affinity.TTL.Duration != time.Hour || policy.Routing.Affinity.MaxEntries != 10000 {
		t.Fatalf("policy without affinity settings did not inherit affinity defaults: %#v", policy.Routing)
	}
}

func TestRoutingEWMAHalfLifeUsesScoringHalfLife(t *testing.T) {
	for _, test := range []struct {
		hours int
		want  time.Duration
	}{
		{hours: 0, want: 168 * time.Hour},
		{hours: 6, want: 6 * time.Hour},
		{hours: 240, want: 240 * time.Hour},
	} {
		if got := RoutingEWMAHalfLife(test.hours); got != test.want {
			t.Fatalf("RoutingEWMAHalfLife(%d) = %s, want %s", test.hours, got, test.want)
		}
	}
}
func TestPolicyQualityMissingFieldsUseDefaults(t *testing.T) {
	var policy Policy
	if errUnmarshal := json.Unmarshal([]byte(`{"quality":{"soft_tps":500,"hard_tps":1000,"min_generation_ms":1000}}`), &policy); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	normalized, errNormalize := normalizePolicy(policy)
	if errNormalize != nil {
		t.Fatal(errNormalize)
	}
	quality := normalized.Quality
	if quality.ConsecutiveSoft != 2 || quality.ConsecutiveErrors != 2 || quality.ConsecutiveMissingThinking != 2 {
		t.Fatalf("unexpected consecutive defaults: %#v", quality)
	}
	if !quality.ThinkingGuard || quality.CrossVerify != "thinking" {
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
