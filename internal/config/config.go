package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultEventsLimit              = 1000
	defaultProbeQueueSize           = 10000
	maxProbeQueueSize               = 1000000
	defaultProbeUnknownJitter       = 30 * time.Second
	defaultProbeScanInterval        = 15 * time.Second
	defaultTaskHistoryRetentionDays = 3
	defaultTaskHistoryMaxRecords    = 500
	defaultTaskHistoryMaxBytes      = int64(128 << 20)
	defaultTaskHistoryMaxItems      = 10000
	maxTaskHistoryRetentionDays     = 365
	maxTaskHistoryMaxRecords        = 10000
	maxTaskHistoryMaxBytes          = int64(4 << 30)
	maxTaskHistoryMaxItems          = 100000
)

// Duration accepts YAML and JSON duration strings while keeping a stable text form
type Duration struct {
	time.Duration
}

func NewDuration(value time.Duration) Duration {
	return Duration{Duration: value}
}

func (d Duration) String() string {
	return d.Duration.String()
}

func (d Duration) IsZero() bool {
	return d.Duration == 0
}

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a string")
	}
	parsed, errParse := time.ParseDuration(strings.TrimSpace(node.Value))
	if errParse != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, errParse)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	return d.String(), nil
}

func (d *Duration) UnmarshalJSON(data []byte) error {
	var raw string
	if errUnmarshal := json.Unmarshal(data, &raw); errUnmarshal != nil {
		return fmt.Errorf("duration must be a string: %w", errUnmarshal)
	}
	parsed, errParse := time.ParseDuration(strings.TrimSpace(raw))
	if errParse != nil {
		return fmt.Errorf("invalid duration %q: %w", raw, errParse)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

type QualityConfig struct {
	SoftTPS                         float64  `yaml:"soft_tps" json:"soft_tps"`
	HardTPS                         float64  `yaml:"hard_tps" json:"hard_tps"`
	ConsecutiveSoft                 int      `yaml:"consecutive_soft" json:"consecutive_soft"`
	ConsecutiveErrors               int      `yaml:"consecutive_errors" json:"consecutive_errors"`
	ConsecutiveMissingThinking      int      `yaml:"consecutive_missing_thinking" json:"consecutive_missing_thinking"`
	StrikeDecayAfterHours           int      `yaml:"strike_decay_after" json:"strike_decay_after"`
	StrikeDecayMinHealthy           int      `yaml:"strike_decay_min_healthy" json:"strike_decay_min_healthy"`
	ThinkingGuard                   bool     `yaml:"thinking_guard" json:"thinking_guard"`
	SoftCrossVerify                 bool     `yaml:"soft_cross_verify" json:"soft_cross_verify"`
	ThinkingCrossVerify             bool     `yaml:"thinking_cross_verify" json:"thinking_cross_verify"`
	MinGenerationMS                 int64    `yaml:"min_generation_ms" json:"min_generation_ms"`
	FailClosed                      bool     `yaml:"fail_closed" json:"fail_closed"`
	MinPassiveOutputTokens          int64    `yaml:"min_passive_output_tokens" json:"min_passive_output_tokens"`
	MinHoldOutputTokens             int64    `yaml:"min_hold_output_tokens" json:"min_hold_output_tokens"`
	HoldTimeout                     Duration `yaml:"hold_timeout" json:"hold_timeout"`
	HoldMaxBufferBytes              int64    `yaml:"hold_max_buffer_bytes" json:"hold_max_buffer_bytes"`
	MinEncryptedBytes               int64    `yaml:"min_encrypted_bytes" json:"min_encrypted_bytes"`
	EncryptedBytesPerReasoningToken int64    `yaml:"encrypted_bytes_per_reasoning_token" json:"encrypted_bytes_per_reasoning_token"`
	MissingThinkingCooldown         Duration `yaml:"missing_thinking_cooldown" json:"missing_thinking_cooldown"`
	EmptyStreamCooldown             Duration `yaml:"empty_stream_cooldown" json:"empty_stream_cooldown"`
	MaxAttempts                     int      `yaml:"max_attempts" json:"max_attempts"`
	OnExhausted                     string   `yaml:"on_exhausted" json:"on_exhausted"`
}

type ProbeConfig struct {
	RecoveryOnly    bool     `yaml:"recovery_only" json:"recovery_only"`
	Model           string   `yaml:"model" json:"model"`
	Timeout         Duration `yaml:"timeout" json:"timeout"`
	MaxOutputTokens int      `yaml:"max_output_tokens" json:"max_output_tokens"`
	Concurrency     int      `yaml:"concurrency" json:"concurrency"`
	QueueSize       int      `yaml:"queue_size" json:"queue_size"`
	ScanInterval    Duration `yaml:"scan_interval" json:"scan_interval"`
	UnknownDelay    Duration `yaml:"unknown_delay" json:"unknown_delay"`
	UnknownJitter   Duration `yaml:"unknown_jitter" json:"unknown_jitter"`
	SuspectBackoff  Duration `yaml:"suspect_backoff" json:"suspect_backoff"`
	MaxBackoff      Duration `yaml:"max_backoff" json:"max_backoff"`
	EntryProtocol   string   `yaml:"entry_protocol" json:"entry_protocol"`
	ExitProtocol    string   `yaml:"exit_protocol" json:"exit_protocol"`
}

type RoutingConfig struct {
	SelectionStrategy      string   `yaml:"selection_strategy" json:"selection_strategy"`
	PreferLastSuccess      bool     `yaml:"prefer_last_success" json:"prefer_last_success"`
	SessionAffinity        bool     `yaml:"session_affinity" json:"session_affinity"`
	SessionAffinityTTL     Duration `yaml:"session_affinity_ttl" json:"session_affinity_ttl"`
	SessionAffinityMaxSize int      `yaml:"session_affinity_max_entries" json:"session_affinity_max_entries"`
}

type TaskHistoryConfig struct {
	Enabled         bool  `yaml:"enabled" json:"enabled"`
	RetentionDays   int   `yaml:"retention_days" json:"retention_days"`
	MaxRecords      int   `yaml:"max_records" json:"max_records"`
	MaxBytes        int64 `yaml:"max_bytes" json:"max_bytes"`
	MaxItemsPerTask int   `yaml:"max_items_per_task" json:"max_items_per_task"`
}

func DefaultTaskHistoryConfig() TaskHistoryConfig {
	return TaskHistoryConfig{
		Enabled:         true,
		RetentionDays:   defaultTaskHistoryRetentionDays,
		MaxRecords:      defaultTaskHistoryMaxRecords,
		MaxBytes:        defaultTaskHistoryMaxBytes,
		MaxItemsPerTask: defaultTaskHistoryMaxItems,
	}
}

type Config struct {
	Enabled          bool          `yaml:"enabled" json:"enabled"`
	Priority         int           `yaml:"priority" json:"priority"`
	PassiveGuard     bool          `yaml:"passive_guard" json:"passive_guard"`
	TransparentGuard bool          `yaml:"transparent_guard" json:"transparent_guard"`
	AutoAction       string        `yaml:"auto_action" json:"auto_action"`
	ModelPrefixes    []string      `yaml:"model_prefixes" json:"model_prefixes"`
	Routing          RoutingConfig `yaml:"routing" json:"routing"`
	Quality          QualityConfig `yaml:"quality" json:"quality"`
	Probe            ProbeConfig   `yaml:"probe" json:"probe"`

	Proxy       ProxyConfig       `yaml:"proxy" json:"proxy"`
	TaskHistory TaskHistoryConfig `yaml:"task_history" json:"task_history"`
	StateFile   string            `yaml:"state_file" json:"state_file"`
	EventsLimit int               `yaml:"events_limit" json:"events_limit"`
	// Store is injected by CLIProxyAPI under plugins.configs.<plugin-id> when a
	// plugin is installed from the plugin store. It is host-owned metadata, not
	// plugin policy, but it must pass the strict decoder during registration.
	Store map[string]any `yaml:"store,omitempty" json:"-"`
}

// Policy is the runtime-editable subset persisted in state.json
type Policy struct {
	PassiveGuard     bool          `json:"passive_guard"`
	TransparentGuard bool          `json:"transparent_guard"`
	AutoAction       string        `json:"auto_action"`
	ModelPrefixes    []string      `json:"model_prefixes"`
	Routing          RoutingConfig `json:"routing"`
	Quality          QualityConfig `json:"quality"`
	Probe            ProbeConfig   `json:"probe"`

	ProxyAutoRotateAfterQualityFail bool              `json:"proxy_auto_rotate_after_quality_failure"`
	TaskHistory                     TaskHistoryConfig `json:"task_history"`
	EventsLimit                     int               `json:"events_limit"`
}

func DefaultRoutingConfig() RoutingConfig {
	return RoutingConfig{
		SelectionStrategy:      "round_robin",
		PreferLastSuccess:      true,
		SessionAffinity:        true,
		SessionAffinityTTL:     NewDuration(time.Hour),
		SessionAffinityMaxSize: 10000,
	}
}

func DefaultQualityConfig() QualityConfig {
	return QualityConfig{
		SoftTPS:                         500,
		HardTPS:                         1000,
		ConsecutiveSoft:                 2,
		ConsecutiveErrors:               2,
		ConsecutiveMissingThinking:      1,
		StrikeDecayAfterHours:           48,
		StrikeDecayMinHealthy:           10,
		ThinkingGuard:                   true,
		SoftCrossVerify:                 false,
		ThinkingCrossVerify:             false,
		MinGenerationMS:                 1000,
		FailClosed:                      false,
		MinPassiveOutputTokens:          32,
		MinHoldOutputTokens:             8,
		HoldTimeout:                     NewDuration(30 * time.Second),
		HoldMaxBufferBytes:              4 * 1024 * 1024,
		MinEncryptedBytes:               256,
		EncryptedBytesPerReasoningToken: 4,
		MissingThinkingCooldown:         NewDuration(12 * time.Hour),
		EmptyStreamCooldown:             NewDuration(15 * time.Minute),
		MaxAttempts:                     6,
		OnExhausted:                     "fail_closed",
	}
}

func DefaultProbeConfig() ProbeConfig {
	return ProbeConfig{
		RecoveryOnly:    true,
		Model:           "grok-4.5",
		Timeout:         NewDuration(90 * time.Second),
		MaxOutputTokens: 768,
		Concurrency:     1,
		QueueSize:       defaultProbeQueueSize,
		ScanInterval:    NewDuration(defaultProbeScanInterval),
		UnknownDelay:    NewDuration(30 * time.Second),
		UnknownJitter:   NewDuration(defaultProbeUnknownJitter),
		SuspectBackoff:  NewDuration(15 * time.Minute),
		MaxBackoff:      NewDuration(time.Hour),
		EntryProtocol:   "codex",
		ExitProtocol:    "codex",
	}
}

func DefaultConfig() Config {
	return Config{
		Enabled:          true,
		Priority:         10,
		PassiveGuard:     true,
		TransparentGuard: false,
		AutoAction:       "observe",
		ModelPrefixes:    []string{"grok-"},
		Routing:          DefaultRoutingConfig(),
		Quality:          DefaultQualityConfig(),
		Probe:            DefaultProbeConfig(),
		Proxy:            DefaultProxyConfig(),
		TaskHistory:      DefaultTaskHistoryConfig(),

		EventsLimit: defaultEventsLimit,
	}
}

func decodeConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(strings.TrimSpace(string(raw))) > 0 {
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if errUnmarshal := decoder.Decode(&cfg); errUnmarshal != nil {
			return Config{}, fmt.Errorf("decode plugin config: %w", errUnmarshal)
		}
	}
	if errNormalize := normalizeConfig(&cfg); errNormalize != nil {
		return Config{}, errNormalize
	}
	return cfg, nil
}

func normalizeConfig(cfg *Config) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	if strings.TrimSpace(cfg.AutoAction) == "" {
		cfg.AutoAction = "observe"
	}
	cfg.AutoAction = strings.ToLower(strings.TrimSpace(cfg.AutoAction))
	if cfg.AutoAction != "observe" && cfg.AutoAction != "cooldown_then_disable" && cfg.AutoAction != "cooldown_only" {
		return fmt.Errorf("auto_action must be observe, cooldown_then_disable, or cooldown_only")
	}
	if len(cfg.ModelPrefixes) == 0 {
		cfg.ModelPrefixes = []string{"grok-"}
	}
	for i := range cfg.ModelPrefixes {
		cfg.ModelPrefixes[i] = strings.ToLower(strings.TrimSpace(cfg.ModelPrefixes[i]))
	}
	if cfg.Priority < 0 {
		cfg.Priority = 0
	}
	if cfg.EventsLimit <= 0 {
		cfg.EventsLimit = defaultEventsLimit
	}
	if errTaskHistory := normalizeTaskHistory(&cfg.TaskHistory); errTaskHistory != nil {
		return errTaskHistory
	}
	if errRouting := normalizeRouting(&cfg.Routing); errRouting != nil {
		return errRouting
	}
	if errQuality := normalizeQuality(&cfg.Quality); errQuality != nil {
		return errQuality
	}
	if errProbe := normalizeProbe(&cfg.Probe); errProbe != nil {
		return errProbe
	}
	if errProxy := normalizeProxyConfig(&cfg.Proxy); errProxy != nil {
		return errProxy
	}

	return nil
}

func (q *QualityConfig) UnmarshalJSON(data []byte) error {
	if q == nil {
		return errors.New("quality config is nil")
	}
	defaults := DefaultQualityConfig()
	type qualityAlias QualityConfig
	decoded := qualityAlias(defaults)
	if errUnmarshal := decodeStrictJSON(data, &decoded); errUnmarshal != nil {
		return errUnmarshal
	}
	*q = QualityConfig(decoded)
	return nil
}

func normalizeTaskHistory(history *TaskHistoryConfig) error {
	if history == nil {
		return errors.New("task_history is nil")
	}
	defaults := DefaultTaskHistoryConfig()
	if history.RetentionDays <= 0 {
		history.RetentionDays = defaults.RetentionDays
	}
	if history.MaxRecords <= 0 {
		history.MaxRecords = defaults.MaxRecords
	}
	if history.MaxBytes <= 0 {
		history.MaxBytes = defaults.MaxBytes
	}
	if history.MaxItemsPerTask <= 0 {
		history.MaxItemsPerTask = defaults.MaxItemsPerTask
	}
	if history.RetentionDays > maxTaskHistoryRetentionDays {
		return fmt.Errorf("task_history.retention_days must be <= %d", maxTaskHistoryRetentionDays)
	}
	if history.MaxRecords > maxTaskHistoryMaxRecords {
		return fmt.Errorf("task_history.max_records must be <= %d", maxTaskHistoryMaxRecords)
	}
	if history.MaxBytes > maxTaskHistoryMaxBytes {
		return fmt.Errorf("task_history.max_bytes must be <= %d", maxTaskHistoryMaxBytes)
	}
	if history.MaxItemsPerTask > maxTaskHistoryMaxItems {
		return fmt.Errorf("task_history.max_items_per_task must be <= %d", maxTaskHistoryMaxItems)
	}
	return nil
}

func normalizeRouting(r *RoutingConfig) error {
	if r == nil {
		return errors.New("routing config is nil")
	}
	defaults := DefaultRoutingConfig()
	r.SelectionStrategy = strings.ToLower(strings.TrimSpace(r.SelectionStrategy))
	if r.SelectionStrategy == "" {
		r.SelectionStrategy = defaults.SelectionStrategy
	}
	switch r.SelectionStrategy {
	case "round_robin", "random", "fill_first":
	default:
		return fmt.Errorf("routing.selection_strategy must be round_robin, random, or fill_first")
	}
	if r.SessionAffinityTTL.Duration <= 0 {
		r.SessionAffinityTTL = defaults.SessionAffinityTTL
	}
	if r.SessionAffinityTTL.Duration > 168*time.Hour {
		return fmt.Errorf("routing.session_affinity_ttl must be <= 168h")
	}
	if r.SessionAffinityMaxSize <= 0 {
		r.SessionAffinityMaxSize = defaults.SessionAffinityMaxSize
	}
	if r.SessionAffinityMaxSize > 100000 {
		return fmt.Errorf("routing.session_affinity_max_entries must be <= 100000")
	}
	return nil
}

func normalizeQuality(q *QualityConfig) error {
	if q == nil {
		return errors.New("quality config is nil")
	}
	defaults := DefaultQualityConfig()
	if q.SoftTPS <= 0 {
		q.SoftTPS = defaults.SoftTPS
	}
	if q.HardTPS <= 0 {
		q.HardTPS = defaults.HardTPS
	}
	if q.HardTPS <= q.SoftTPS {
		return fmt.Errorf("quality.hard_tps must be greater than soft_tps")
	}
	if q.ConsecutiveSoft <= 0 {
		q.ConsecutiveSoft = defaults.ConsecutiveSoft
	}
	if q.ConsecutiveErrors <= 0 {
		q.ConsecutiveErrors = defaults.ConsecutiveErrors
	}
	if q.ConsecutiveMissingThinking <= 0 {
		q.ConsecutiveMissingThinking = defaults.ConsecutiveMissingThinking
	}
	if q.StrikeDecayAfterHours < 0 {
		return fmt.Errorf("quality.strike_decay_after must be >= 0")
	}
	if q.StrikeDecayMinHealthy < 0 {
		return fmt.Errorf("quality.strike_decay_min_healthy must be >= 0")
	}
	if q.StrikeDecayMinHealthy == 0 {
		q.StrikeDecayMinHealthy = defaults.StrikeDecayMinHealthy
	}
	if q.MinGenerationMS <= 0 {
		q.MinGenerationMS = defaults.MinGenerationMS
	}
	if q.MinPassiveOutputTokens <= 0 {
		q.MinPassiveOutputTokens = defaults.MinPassiveOutputTokens
	}
	if q.MinHoldOutputTokens <= 0 {
		q.MinHoldOutputTokens = defaults.MinHoldOutputTokens
	}
	if q.HoldTimeout.Duration <= 0 {
		q.HoldTimeout = defaults.HoldTimeout
	}
	if q.HoldMaxBufferBytes <= 0 {
		q.HoldMaxBufferBytes = defaults.HoldMaxBufferBytes
	}
	if q.MinEncryptedBytes <= 0 {
		q.MinEncryptedBytes = defaults.MinEncryptedBytes
	}
	if q.EncryptedBytesPerReasoningToken <= 0 {
		q.EncryptedBytesPerReasoningToken = defaults.EncryptedBytesPerReasoningToken
	}
	if q.MissingThinkingCooldown.Duration <= 0 {
		q.MissingThinkingCooldown = defaults.MissingThinkingCooldown
	}
	if q.EmptyStreamCooldown.Duration <= 0 {
		q.EmptyStreamCooldown = defaults.EmptyStreamCooldown
	}
	if q.MaxAttempts <= 0 {
		q.MaxAttempts = defaults.MaxAttempts
	}
	q.OnExhausted = strings.ToLower(strings.TrimSpace(q.OnExhausted))
	if q.OnExhausted == "" {
		q.OnExhausted = "fail_closed"
	}
	if q.OnExhausted != "fail_closed" && q.OnExhausted != "fail_open" {
		return fmt.Errorf("quality.on_exhausted must be fail_closed or fail_open")
	}
	return nil
}

func normalizeProbe(p *ProbeConfig) error {
	if p == nil {
		return errors.New("probe config is nil")
	}
	defaults := DefaultProbeConfig()
	if strings.TrimSpace(p.Model) == "" {
		p.Model = defaults.Model
	}
	if p.Timeout.Duration <= 0 {
		p.Timeout = defaults.Timeout
	}
	if p.MaxOutputTokens <= 0 {
		p.MaxOutputTokens = defaults.MaxOutputTokens
	}
	if p.Concurrency <= 0 {
		p.Concurrency = defaults.Concurrency
	}
	if p.QueueSize <= 0 {
		p.QueueSize = defaults.QueueSize
	}
	if p.QueueSize > maxProbeQueueSize {
		return fmt.Errorf("probe.queue_size must be <= %d", maxProbeQueueSize)
	}
	if p.ScanInterval.Duration <= 0 {
		p.ScanInterval = defaults.ScanInterval
	}
	if p.ScanInterval.Duration < time.Second || p.ScanInterval.Duration > 24*time.Hour {
		return fmt.Errorf("probe.scan_interval must be between 1s and 24h")
	}
	if p.UnknownDelay.Duration <= 0 {
		p.UnknownDelay = defaults.UnknownDelay
	}
	if p.UnknownJitter.Duration < 0 {
		return fmt.Errorf("probe.unknown_jitter must be >= 0")
	}
	if p.SuspectBackoff.Duration <= 0 {
		p.SuspectBackoff = defaults.SuspectBackoff
	}
	if p.MaxBackoff.Duration <= 0 {
		p.MaxBackoff = defaults.MaxBackoff
	}
	if strings.TrimSpace(p.EntryProtocol) == "" {
		p.EntryProtocol = defaults.EntryProtocol
	}
	if strings.TrimSpace(p.ExitProtocol) == "" {
		p.ExitProtocol = defaults.ExitProtocol
	}
	return nil
}

func (p *Policy) UnmarshalJSON(data []byte) error {
	if p == nil {
		return errors.New("policy is nil")
	}
	type policyAlias Policy
	decoded := policyAlias(PolicyFromConfig(DefaultConfig()))
	if errUnmarshal := decodeStrictJSON(data, &decoded); errUnmarshal != nil {
		return errUnmarshal
	}
	*p = Policy(decoded)
	return nil
}

func PolicyFromConfig(cfg Config) Policy {
	return Policy{
		PassiveGuard:     cfg.PassiveGuard,
		TransparentGuard: cfg.TransparentGuard,
		AutoAction:       cfg.AutoAction,
		ModelPrefixes:    append([]string(nil), cfg.ModelPrefixes...),
		Routing:          cfg.Routing,
		Quality:          cfg.Quality,
		Probe:            cfg.Probe,

		ProxyAutoRotateAfterQualityFail: cfg.Proxy.AutoRotateAfterQualityFailure,
		TaskHistory:                     cfg.TaskHistory,
		EventsLimit:                     cfg.EventsLimit,
	}
}

func ApplyPolicy(cfg *Config, policy Policy) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	cfg.PassiveGuard = policy.PassiveGuard
	cfg.TransparentGuard = policy.TransparentGuard
	cfg.AutoAction = policy.AutoAction
	cfg.ModelPrefixes = append([]string(nil), policy.ModelPrefixes...)
	cfg.Routing = policy.Routing
	cfg.Quality = policy.Quality
	cfg.Probe = policy.Probe

	cfg.Proxy.AutoRotateAfterQualityFailure = policy.ProxyAutoRotateAfterQualityFail
	cfg.TaskHistory = policy.TaskHistory
	cfg.EventsLimit = policy.EventsLimit
	return normalizeConfig(cfg)
}

func normalizePolicy(policy Policy) (Policy, error) {
	cfg := DefaultConfig()
	cfg.PassiveGuard = policy.PassiveGuard
	cfg.TransparentGuard = policy.TransparentGuard
	cfg.AutoAction = policy.AutoAction
	cfg.ModelPrefixes = append([]string(nil), policy.ModelPrefixes...)
	cfg.Routing = policy.Routing
	cfg.Quality = policy.Quality
	cfg.Probe = policy.Probe

	cfg.Proxy.AutoRotateAfterQualityFailure = policy.ProxyAutoRotateAfterQualityFail
	cfg.TaskHistory = policy.TaskHistory
	cfg.EventsLimit = policy.EventsLimit
	if errNormalize := normalizeConfig(&cfg); errNormalize != nil {
		return Policy{}, errNormalize
	}
	return PolicyFromConfig(cfg), nil
}

func configRevision(raw []byte) string {
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

const dockerStatePath = "/CLIProxyAPI/plugins/data/grok-quality-guard/state.json"

// defaultStatePath picks the first writable persistent location. The Docker
// layout comes first so the default survives CPA container restarts when the
// plugins/data directory is mounted as a volume.
func defaultStatePath() string {
	candidates := []string{
		dockerStatePath,
		filepath.Join("plugins", "data", "grok-quality-guard", "state.json"),
	}
	if dir, errDir := os.UserConfigDir(); errDir == nil && strings.TrimSpace(dir) != "" {
		candidates = append(candidates, filepath.Join(dir, "cliproxyapi", "grok-quality-guard", "state.json"))
	}
	for _, candidate := range candidates {
		if writableStateDir(filepath.Dir(candidate)) {
			return candidate
		}
	}
	return filepath.Join(os.TempDir(), "grok-quality-guard", "state.json")
}

func writableStateDir(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return false
	}
	probe := filepath.Join(dir, fmt.Sprintf(".write-probe-%d", os.Getpid()))
	if errWrite := os.WriteFile(probe, []byte("ok"), 0o600); errWrite != nil {
		return false
	}
	_ = os.Remove(probe)
	return true
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
