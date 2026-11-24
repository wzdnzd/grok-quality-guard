package config

import (
	"fmt"
	"math"
	"time"
)

// FieldType describes how a settings value is parsed and rendered.
type FieldType string

const (
	FieldTypeString     FieldType = "string"
	FieldTypeInteger    FieldType = "integer"
	FieldTypeNumber     FieldType = "number"
	FieldTypeBoolean    FieldType = "boolean"
	FieldTypeEnum       FieldType = "enum"
	FieldTypeDuration   FieldType = "duration"
	FieldTypeStringList FieldType = "string_list"
)

// FieldSpec is the single source of truth for one editable setting: the
// runtime validation rule, the default, and the bounds the settings page
// renders. Backends validate against it and the page reads it over
// /settings/schema, so neither side keeps a second copy of the ranges.
type FieldSpec struct {
	Path  string    `json:"path"`
	Group string    `json:"group"`
	Type  FieldType `json:"type"`
	// Min and Max apply to integer, number and duration fields. Durations are
	// expressed in seconds. HasMin and HasMax distinguish an explicit bound of
	// zero from "no bound", which matters for values that may be negative.
	Min    float64  `json:"min,omitempty"`
	Max    float64  `json:"max,omitempty"`
	HasMin bool     `json:"-"`
	HasMax bool     `json:"-"`
	Enum   []string `json:"enum,omitempty"`
	// Default is the built-in value applied when the field is missing.
	Default any `json:"default,omitempty"`
	// ExclusiveMin marks a lower bound that must be strictly greater.
	ExclusiveMin bool `json:"exclusive_min,omitempty"`
}

// DurationSeconds returns Min or Max as a duration.
func (f FieldSpec) DurationMin() time.Duration { return secondsToDuration(f.Min) }

// DurationMax returns Max as a duration.
func (f FieldSpec) DurationMax() time.Duration { return secondsToDuration(f.Max) }

func secondsToDuration(value float64) time.Duration {
	if value <= 0 {
		return 0
	}
	return time.Duration(value * float64(time.Second))
}

// FieldGroups are the settings-page sections, in display order.
var FieldGroups = []struct {
	Key  string `json:"key"`
	Spec string `json:"-"`
}{
	{Key: "mode", Spec: "ui.operating_mode_and_actions"},
	{Key: "routing", Spec: "ui.transparent_auth_selection_and_session_affinity"},
	{Key: "scoring", Spec: "ui.ewma_health_ranking"},
	{Key: "quality", Spec: "ui.quality_thresholds_and_reasoning_evidence"},
	{Key: "transparent", Spec: "ui.transparent_stream_protection"},
	{Key: "probe", Spec: "ui.active_probes"},
	{Key: "history", Spec: "ui.task_center_and_history"},
}

// FieldSpecs returns every editable settings field with its bounds and
// default. Enum values match the YAML vocabulary so the API, YAML and the
// settings page cannot disagree.
func FieldSpecs() []FieldSpec {
	return []FieldSpec{
		{Path: "guard", Group: "mode", Type: FieldTypeEnum, Enum: []string{"off", "passive", "transparent"}, Default: "passive"},
		{Path: "auto_action", Group: "mode", Type: FieldTypeEnum, Enum: []string{"observe", "cooldown_only", "cooldown_then_disable"}, Default: "observe"},
		{Path: "events_limit", Group: "mode", Type: FieldTypeInteger, Min: 1, Max: 10000, Default: defaultEventsLimit},
		{Path: "model_prefixes", Group: "mode", Type: FieldTypeStringList, Default: []string{"grok-"}},
		{Path: "state_file", Group: "mode", Type: FieldTypeString},

		{Path: "routing.strategy.selection", Group: "routing", Type: FieldTypeEnum, Enum: []string{"round_robin", "random", "fill_first"}, Default: "round_robin"},
		{Path: "routing.strategy.prefer_last_success", Group: "routing", Type: FieldTypeBoolean, Default: true},
		{Path: "routing.affinity.enabled", Group: "routing", Type: FieldTypeBoolean, Default: true},
		{Path: "routing.affinity.ttl", Group: "routing", Type: FieldTypeDuration, Min: 1, Max: 604800, Default: "1h"},
		{Path: "routing.affinity.max_entries", Group: "routing", Type: FieldTypeInteger, Min: 1, Max: 100000, Default: 10000},

		{Path: "routing.scoring.enabled", Group: "scoring", Type: FieldTypeBoolean, Default: true},
		{Path: "routing.scoring.max_accounts_per_bucket", Group: "scoring", Type: FieldTypeInteger, Min: float64(minRoutingMaxAccountsPerBucket), Max: float64(maxRoutingMaxAccountsPerBucket), Default: defaultRoutingMaxAccountsPerBucket},
		{Path: "routing.scoring.half_life_hours", Group: "scoring", Type: FieldTypeInteger, Min: 1, Max: 8760, Default: defaultRoutingHalfLifeHours},
		{Path: "routing.scoring.weights.success", Group: "scoring", Type: FieldTypeNumber, Min: 0, Max: 1, Default: 0.50},
		{Path: "routing.scoring.weights.ttft", Group: "scoring", Type: FieldTypeNumber, Min: 0, Max: 1, Default: 0.30},
		{Path: "routing.scoring.weights.tps", Group: "scoring", Type: FieldTypeNumber, Min: 0, Max: 1, Default: 0.20},
		{Path: "routing.scoring.evidence.probe", Group: "scoring", Type: FieldTypeNumber, Min: 0, Max: 1, Default: 0.65},
		{Path: "routing.scoring.evidence.transparent", Group: "scoring", Type: FieldTypeNumber, Min: 0, Max: 1, Default: 0.35},
		{Path: "routing.scoring.evidence.passive", Group: "scoring", Type: FieldTypeNumber, Min: 0, Max: 1, Default: 0},
		{Path: "routing.scoring.refresh.min_interval", Group: "scoring", Type: FieldTypeDuration, Min: 10, Max: 604800, Default: "5m"},
		{Path: "routing.scoring.refresh.max_age", Group: "scoring", Type: FieldTypeDuration, Min: 10, Max: 604800, Default: "30m"},
		{Path: "routing.scoring.refresh.drift_threshold", Group: "scoring", Type: FieldTypeNumber, Min: 0, Max: 1, Default: defaultRoutingDriftThreshold},

		{Path: "quality.soft_tps", Group: "quality", Type: FieldTypeNumber, Min: 0, Max: 1000000, ExclusiveMin: true, Default: 500},
		{Path: "quality.hard_tps", Group: "quality", Type: FieldTypeNumber, Min: 0, Max: 1000000, ExclusiveMin: true, Default: 1000},
		{Path: "quality.consecutive_soft", Group: "quality", Type: FieldTypeInteger, Min: 1, Max: 10, Default: 2},
		{Path: "quality.consecutive_errors", Group: "quality", Type: FieldTypeInteger, Min: 1, Max: 10, Default: 2},
		{Path: "quality.consecutive_missing_thinking", Group: "quality", Type: FieldTypeInteger, Min: 1, Max: 10, Default: 2},
		{Path: "quality.strike_decay_after", Group: "quality", Type: FieldTypeInteger, Min: 0, Max: 8760, Default: 48},
		{Path: "quality.strike_decay_min_healthy", Group: "quality", Type: FieldTypeInteger, Min: 0, Max: 1000, Default: 10},
		{Path: "quality.min_generation_ms", Group: "quality", Type: FieldTypeInteger, Min: 100, Max: 60000, Default: 1000},
		{Path: "quality.cross_verify", Group: "quality", Type: FieldTypeEnum, Enum: []string{"off", "soft", "thinking", "all"}, Default: "thinking"},
		{Path: "quality.min_passive_output_tokens", Group: "quality", Type: FieldTypeInteger, Min: 1, Max: 10000, Default: 32},
		{Path: "quality.min_encrypted_bytes", Group: "quality", Type: FieldTypeInteger, Min: 64, Max: 65536, Default: 256},
		{Path: "quality.encrypted_bytes_per_reasoning_token", Group: "quality", Type: FieldTypeInteger, Min: 1, Max: 64, Default: 2},
		{Path: "quality.missing_thinking_cooldown", Group: "quality", Type: FieldTypeDuration, Min: 1, Max: 2592000, Default: "12h"},
		{Path: "quality.empty_stream_cooldown", Group: "quality", Type: FieldTypeDuration, Min: 1, Max: 86400, Default: "15m"},
		{Path: "quality.non_reasoning_models", Group: "quality", Type: FieldTypeStringList, Default: []string{}},
		{Path: "quality.thinking_guard", Group: "quality", Type: FieldTypeBoolean, Default: true},
		{Path: "quality.burst_guard", Group: "quality", Type: FieldTypeBoolean, Default: false},
		{Path: "quality.shadow_mode", Group: "quality", Type: FieldTypeBoolean, Default: false},
		{Path: "quality.debug_logging", Group: "quality", Type: FieldTypeBoolean, Default: false},

		{Path: "quality.min_hold_output_tokens", Group: "transparent", Type: FieldTypeInteger, Min: 1, Max: 10000, Default: 8},
		{Path: "quality.hold_timeout", Group: "transparent", Type: FieldTypeDuration, Min: 5, Max: 600, Default: "30s"},
		{Path: "quality.attempt_timeout", Group: "transparent", Type: FieldTypeDuration, Min: 5, Max: 600, Default: "60s"},
		{Path: "quality.request_timeout", Group: "transparent", Type: FieldTypeDuration, Min: 5, Max: 1800, Default: "120s"},
		{Path: "quality.hold_keepalive", Group: "transparent", Type: FieldTypeDuration, Min: 1, Max: 60, Default: "5s"},
		{Path: "quality.hold_max_buffer_bytes", Group: "transparent", Type: FieldTypeInteger, Min: 1048576, Max: 67108864, Default: 4194304},
		{Path: "quality.max_attempts", Group: "transparent", Type: FieldTypeInteger, Min: 1, Max: 8, Default: 4},
		{Path: "quality.on_exhausted", Group: "transparent", Type: FieldTypeEnum, Enum: []string{"fail_unverified", "fail_closed"}, Default: "fail_unverified"},

		{Path: "probe.healthy_interval_hours", Group: "probe", Type: FieldTypeInteger, Min: 0, Max: 8760, Default: 0},
		{Path: "probe.model", Group: "probe", Type: FieldTypeString, Default: "grok-4.5"},
		{Path: "probe.timeout", Group: "probe", Type: FieldTypeDuration, Min: 1, Max: 600, Default: "90s"},
		{Path: "probe.max_output_tokens", Group: "probe", Type: FieldTypeInteger, Min: 1, Max: 100000, Default: 768},
		{Path: "probe.concurrency", Group: "probe", Type: FieldTypeInteger, Min: 1, Max: 64, Default: 2},
		{Path: "probe.queue_size", Group: "probe", Type: FieldTypeInteger, Min: 1, Max: float64(maxProbeQueueSize), Default: defaultProbeQueueSize},
		{Path: "probe.scan_interval", Group: "probe", Type: FieldTypeDuration, Min: 1, Max: 86400, Default: "15s"},
		{Path: "probe.unknown_delay", Group: "probe", Type: FieldTypeDuration, Min: 1, Max: 86400, Default: "30s"},
		{Path: "probe.unknown_jitter", Group: "probe", Type: FieldTypeDuration, Min: 0, Max: 86400, Default: "30s"},
		{Path: "probe.suspect_backoff", Group: "probe", Type: FieldTypeDuration, Min: 1, Max: 86400, Default: "2m"},
		{Path: "probe.max_backoff", Group: "probe", Type: FieldTypeDuration, Min: 1, Max: 604800, Default: "1h"},

		{Path: "task_history.enabled", Group: "history", Type: FieldTypeBoolean, Default: true},
		{Path: "task_history.retention_days", Group: "history", Type: FieldTypeInteger, Min: 1, Max: float64(maxTaskHistoryRetentionDays), Default: defaultTaskHistoryRetentionDays},
		{Path: "task_history.max_records", Group: "history", Type: FieldTypeInteger, Min: 1, Max: float64(maxTaskHistoryMaxRecords), Default: defaultTaskHistoryMaxRecords},
		{Path: "task_history.max_bytes", Group: "history", Type: FieldTypeInteger, Min: 1048576, Max: float64(maxTaskHistoryMaxBytes), Default: float64(defaultTaskHistoryMaxBytes)},
		{Path: "task_history.max_items_per_task", Group: "history", Type: FieldTypeInteger, Min: 1, Max: float64(maxTaskHistoryMaxItems), Default: defaultTaskHistoryMaxItems},
	}
}

// FieldSpecByPath indexes FieldSpecs by path.
func FieldSpecByPath(path string) (FieldSpec, bool) {
	for _, spec := range FieldSpecs() {
		if spec.Path == path {
			normalizeSpecBounds(&spec)
			return spec, true
		}
	}
	return FieldSpec{}, false
}

// zeroIsLowerBound lists fields whose valid range starts exactly at zero.
func zeroIsLowerBound(path string) bool {
	switch path {
	case "probe.healthy_interval_hours", "quality.strike_decay_after",
		"quality.strike_decay_min_healthy", "probe.unknown_jitter",
		"routing.scoring.weights.success", "routing.scoring.weights.ttft",
		"routing.scoring.weights.tps", "routing.scoring.evidence.probe",
		"routing.scoring.evidence.transparent", "routing.scoring.evidence.passive",
		"routing.scoring.refresh.drift_threshold":
		return true
	default:
		return false
	}
}

// normalizeSpecBounds fills HasMin/HasMax so an explicit zero bound is
// enforced instead of being read as "unbounded". FieldSpecs itself stays a
// plain literal table, so the bounds are declared once.
func normalizeSpecBounds(spec *FieldSpec) {
	switch spec.Type {
	case FieldTypeInteger, FieldTypeNumber, FieldTypeDuration:
		spec.HasMin = spec.Min != 0 || zeroIsLowerBound(spec.Path)
		spec.HasMax = spec.Max != 0
	}
}

// ValidateField checks one numeric, enum or duration value against its spec.
func ValidateField(spec FieldSpec, value float64) error {
	switch spec.Type {
	case FieldTypeInteger, FieldTypeNumber, FieldTypeDuration:
	default:
		return nil
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fmt.Errorf("%s must be a finite number", spec.Path)
	}
	if spec.ExclusiveMin {
		if value <= spec.Min {
			return fmt.Errorf("%s must be greater than %g", spec.Path, spec.Min)
		}
	} else if spec.HasMin && value < spec.Min {
		return fmt.Errorf("%s must be at least %g", spec.Path, spec.Min)
	}
	if spec.HasMax && value > spec.Max {
		return fmt.Errorf("%s must be at most %g", spec.Path, spec.Max)
	}
	return nil
}
