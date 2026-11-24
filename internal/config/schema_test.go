package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFieldSpecsMatchNormalizedDefaults(t *testing.T) {
	// Every declared default must survive normalization unchanged, otherwise
	// the settings page would show values the backend immediately rewrites.
	policy, errPolicy := NormalizePolicy(PolicyFromConfig(DefaultConfig()))
	if errPolicy != nil {
		t.Fatal(errPolicy)
	}
	for _, spec := range FieldSpecs() {
		if spec.Default == nil {
			continue
		}
		got, ok := policyValue(policy, spec.Path)
		if !ok {
			t.Fatalf("schema path %q does not resolve on Policy", spec.Path)
		}
		if !comparableEqual(spec.Default, got) {
			t.Fatalf("schema default for %q = %#v, normalized policy has %#v", spec.Path, spec.Default, got)
		}
	}
}

func TestFieldSpecsCoverSettingsPageFields(t *testing.T) {
	declared := make(map[string]struct{}, len(FieldSpecs()))
	for _, spec := range FieldSpecs() {
		if _, exists := declared[spec.Path]; exists {
			t.Fatalf("schema declares %q twice", spec.Path)
		}
		declared[spec.Path] = struct{}{}
	}
	for _, path := range []string{
		"guard", "auto_action", "events_limit", "state_file", "model_prefixes",
		"routing.strategy.selection", "routing.strategy.prefer_last_success",
		"routing.affinity.enabled", "routing.affinity.ttl", "routing.affinity.max_entries",
		"routing.scoring.enabled", "routing.scoring.half_life_hours",
		"quality.thinking_guard", "quality.burst_guard", "quality.shadow_mode", "quality.debug_logging",
		"quality.cross_verify", "quality.on_exhausted", "quality.attempt_timeout", "quality.request_timeout",
		"probe.scan_interval", "task_history.enabled", "task_history.max_bytes",
	} {
		if _, ok := declared[path]; !ok {
			t.Fatalf("schema is missing %q", path)
		}
	}
}

func TestFieldSpecsDeclareBoundsForNumericTypes(t *testing.T) {
	for _, spec := range FieldSpecs() {
		switch spec.Type {
		case FieldTypeInteger, FieldTypeNumber, FieldTypeDuration:
		default:
			if spec.Min != 0 || spec.Max != 0 {
				t.Fatalf("%q is %s but declares bounds", spec.Path, spec.Type)
			}
			continue
		}
		if spec.Max == 0 {
			t.Fatalf("%q has no upper bound", spec.Path)
		}
		if spec.Type == FieldTypeDuration && spec.Min <= 0 && !zeroIsLowerBound(spec.Path) {
			t.Fatalf("%q has no positive lower bound", spec.Path)
		}
	}
}

func TestNormalizeRejectsOutOfRangeValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"hard not above soft", func(c *Config) { c.Quality.HardTPS = c.Quality.SoftTPS }, "quality.hard_tps"},
		{"attempt below hold", func(c *Config) { c.Quality.AttemptTimeout = NewDuration(c.Quality.HoldTimeout.Duration) }, "quality.attempt_timeout"},
		{"attempt above ceiling", func(c *Config) { c.Quality.AttemptTimeout = NewDuration(20 * time.Minute) }, "quality.attempt_timeout"},
		{"request below attempt", func(c *Config) {
			c.Quality.AttemptTimeout = NewDuration(90 * time.Second)
			c.Quality.RequestTimeout = NewDuration(60 * time.Second)
		}, "quality.request_timeout"},
		{"negative healthy interval", func(c *Config) { c.Probe.HealthyIntervalHours = -1 }, "probe.healthy_interval_hours"},
		{"scan interval below one second", func(c *Config) { c.Probe.ScanInterval = NewDuration(time.Millisecond) }, "probe.scan_interval"},
		{"queue over ceiling", func(c *Config) { c.Probe.QueueSize = maxProbeQueueSize + 1 }, "probe.queue_size"},
		{"max age below min interval", func(c *Config) { c.Routing.Scoring.Refresh.MaxAge = NewDuration(time.Second) }, "routing.scoring.refresh.max_age"},
		{"half life over ceiling", func(c *Config) { c.Routing.Scoring.HalfLifeHours = 100000 }, "routing.scoring.half_life_hours"},
		{"bucket too small", func(c *Config) { c.Routing.Scoring.MaxAccountsPerBucket = 4 }, "routing.scoring.max_accounts_per_bucket"},
		{"max attempts over ceiling", func(c *Config) { c.Quality.MaxAttempts = 9 }, "quality.max_attempts"},
		{"buffers below floor", func(c *Config) { c.Quality.HoldMaxBufferBytes = 1024 }, "quality.hold_max_buffer_bytes"},
		{"history over ceiling", func(c *Config) { c.TaskHistory.RetentionDays = 1000 }, "task_history.retention_days"},
		{"unknown cross verify", func(c *Config) { c.Quality.CrossVerify = "sometimes" }, "quality.cross_verify"},
		{"unknown exhaustion", func(c *Config) { c.Quality.OnExhausted = "fail_open" }, "quality.on_exhausted"},
	}
	for _, test := range tests {
		cfg := DefaultConfig()
		test.mutate(&cfg)
		errNormalize := normalizeConfig(&cfg)
		if errNormalize == nil {
			t.Fatalf("%s: expected an error", test.name)
		}
		if !strings.Contains(errNormalize.Error(), test.want) {
			t.Fatalf("%s: error = %v, want it to mention %s", test.name, errNormalize, test.want)
		}
	}
}

func TestNormalizeAcceptsZeroLowerBounds(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Probe.HealthyIntervalHours = 0
	cfg.Quality.StrikeDecayAfterHours = 0
	cfg.Quality.StrikeDecayMinHealthy = 0
	cfg.Routing.Scoring.Weights.Success = 0
	cfg.Routing.Scoring.Refresh.DriftThreshold = 0
	if errNormalize := normalizeConfig(&cfg); errNormalize != nil {
		t.Fatalf("zero lower bounds were rejected: %v", errNormalize)
	}
}

// policyValue resolves a dotted schema path against the Policy struct using
// JSON field names.
func policyValue(policy Policy, path string) (any, bool) {
	current := reflect.ValueOf(policy)
	for _, segment := range strings.Split(path, ".") {
		for current.Kind() == reflect.Pointer {
			current = current.Elem()
		}
		if current.Kind() != reflect.Struct {
			return nil, false
		}
		field, ok := jsonField(current, segment)
		if !ok {
			return nil, false
		}
		current = field
	}
	for current.Kind() == reflect.Pointer {
		if current.IsNil() {
			return nil, false
		}
		current = current.Elem()
	}
	if duration, ok := current.Interface().(Duration); ok {
		return duration.Duration, true
	}
	return current.Interface(), true
}

func jsonField(value reflect.Value, name string) (reflect.Value, bool) {
	kind := value.Type()
	for index := 0; index < kind.NumField(); index++ {
		tag := kind.Field(index).Tag.Get("json")
		if tag == "" {
			continue
		}
		if strings.Split(tag, ",")[0] == name {
			return value.Field(index), true
		}
	}
	return reflect.Value{}, false
}

// comparableEqual compares defaults across the YAML/JSON type differences:
// durations arrive as strings, integers as whichever width the struct uses.
func comparableEqual(want, got any) bool {
	if duration, ok := got.(time.Duration); ok {
		// Schema defaults write durations as strings so the settings page can
		// render them; compare against the canonical form.
		text, _ := want.(string)
		parsed, errParse := time.ParseDuration(text)
		return errParse == nil && parsed == duration
	}
	wantNumber, wantOK := asFloat(want)
	gotNumber, gotOK := asFloat(got)
	if wantOK && gotOK {
		return wantNumber == gotNumber
	}
	if wantList, ok := want.([]string); ok {
		gotList, okGot := got.([]string)
		if !okGot {
			return false
		}
		return reflect.DeepEqual(wantList, gotList)
	}
	return reflect.DeepEqual(want, got)
}

func asFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

var _ = Duration{}
