package proxy

import (
	"context"
	"errors"
	"strings"
	"time"
)

type SourceKind string

const (
	SourceNode     SourceKind = "node"
	SourceTemplate SourceKind = "template"
	SourceResin    SourceKind = "resin"
)

type GenerateRequest struct {
	AuthID            string
	SourceID          string
	SourceKind        SourceKind
	Revision          uint64
	AvoidEndpointHash string
}

// Provider is the common generation seam for every proxy source. Host and
// state adapters remain in the main package, while source-specific generation
// can be extended without changing binding or dashboard code.
type Provider interface {
	Generate(context.Context, GenerateRequest) (RenderedProxy, error)
}

type HealthStatus string

const (
	HealthUnknown   HealthStatus = "unknown"
	HealthHealthy   HealthStatus = "healthy"
	HealthUnhealthy HealthStatus = "unhealthy"
)

type Node struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Endpoint      Endpoint     `json:"endpoint"`
	Enabled       bool         `json:"enabled"`
	Priority      int          `json:"priority"`
	MaxBindings   int          `json:"max_bindings"`
	Health        HealthStatus `json:"health,omitempty"`
	HealthChecked time.Time    `json:"health_checked,omitempty"`
	ConfigVersion uint64       `json:"config_version,omitempty"`
	Revision      uint64       `json:"revision,omitempty"`
	CreatedAt     time.Time    `json:"created_at,omitempty"`
	UpdatedAt     time.Time    `json:"updated_at,omitempty"`
}

type Binding struct {
	AuthID         string            `json:"auth_id"`
	SourceID       string            `json:"source_id"`
	SourceKind     SourceKind        `json:"source_kind"`
	SourceRevision uint64            `json:"source_revision,omitempty"`
	Generation     uint64            `json:"generation,omitempty"`
	EndpointHash   string            `json:"endpoint_hash,omitempty"`
	ResolvedValues map[string]string `json:"resolved_values,omitempty"`
	Lease          *LeaseRef         `json:"lease,omitempty"`
	BoundAt        time.Time         `json:"bound_at,omitempty"`
	UpdatedAt      time.Time         `json:"updated_at,omitempty"`
}

type LeaseRef struct {
	Provider   string `json:"provider,omitempty"`
	PlatformID string `json:"platform_id,omitempty"`
	Account    string `json:"account,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
}

type AutoBindAccountState struct {
	Excluded      bool      `json:"excluded,omitempty"`
	Attempts      int       `json:"attempts,omitempty"`
	NextAttemptAt time.Time `json:"next_attempt_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastSourceID  string    `json:"last_source_id,omitempty"`
}

type AutoBindConfig struct {
	Enabled                   bool     `json:"enabled"`
	SourceScope               string   `json:"source_scope"`
	SourceIDs                 []string `json:"source_ids,omitempty"`
	Concurrency               int      `json:"concurrency"`
	QueueSize                 int      `json:"queue_size"`
	MaxPerCycle               int      `json:"max_per_cycle"`
	ScanInterval              string   `json:"scan_interval"`
	RequireHealthy            bool     `json:"require_healthy"`
	HealthMaxAge              string   `json:"health_max_age"`
	IncludeGuardDisabled      bool     `json:"include_guard_disabled"`
	IncludeExternallyDisabled bool     `json:"include_externally_disabled"`
	ManualClearPolicy         string   `json:"manual_clear_policy"`
	RetryInitial              string   `json:"retry_initial"`
	RetryMax                  string   `json:"retry_max"`
}

func DefaultAutoBindConfig() AutoBindConfig {
	return AutoBindConfig{SourceScope: "all", Concurrency: 2, QueueSize: 1000, MaxPerCycle: 100, ScanInterval: "30s", HealthMaxAge: "10m", ManualClearPolicy: "rebind", RetryInitial: "1m", RetryMax: "1h"}
}

func (c AutoBindConfig) Normalize() (AutoBindConfig, error) {
	defaults := DefaultAutoBindConfig()
	c.SourceScope = strings.ToLower(strings.TrimSpace(c.SourceScope))
	if c.SourceScope == "" {
		c.SourceScope = defaults.SourceScope
	}
	switch c.SourceScope {
	case "all", "nodes", "templates", "resin":
	default:
		return c, errors.New("proxy.auto_bind.source_scope must be all, nodes, templates, or resin")
	}
	if c.Concurrency < 1 {
		c.Concurrency = defaults.Concurrency
	}
	if c.Concurrency > 32 {
		return c, errors.New("proxy.auto_bind.concurrency must be <= 32")
	}
	if c.QueueSize < 1 {
		c.QueueSize = defaults.QueueSize
	}
	if c.QueueSize > 1000000 {
		return c, errors.New("proxy.auto_bind.queue_size is too large")
	}
	if c.MaxPerCycle < 1 {
		c.MaxPerCycle = defaults.MaxPerCycle
	}
	if c.ScanInterval == "" {
		c.ScanInterval = defaults.ScanInterval
	}
	if c.HealthMaxAge == "" {
		c.HealthMaxAge = defaults.HealthMaxAge
	}
	if c.RetryInitial == "" {
		c.RetryInitial = defaults.RetryInitial
	}
	if c.RetryMax == "" {
		c.RetryMax = defaults.RetryMax
	}
	c.ManualClearPolicy = strings.ToLower(strings.TrimSpace(c.ManualClearPolicy))
	if c.ManualClearPolicy == "" {
		c.ManualClearPolicy = defaults.ManualClearPolicy
	}
	if c.ManualClearPolicy != "rebind" && c.ManualClearPolicy != "pause" {
		return c, errors.New("proxy.auto_bind.manual_clear_policy must be rebind or pause")
	}
	return c, nil
}

type AutoBindRuntime struct {
	Enabled       bool      `json:"enabled"`
	Running       bool      `json:"running"`
	WaitingReason string    `json:"waiting_reason,omitempty"`
	QueueDepth    int       `json:"queue_depth"`
	InFlight      int       `json:"in_flight"`
	LastScanAt    time.Time `json:"last_scan_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	BoundTotal    int64     `json:"bound_total"`
	FailedTotal   int64     `json:"failed_total"`
	SkippedTotal  int64     `json:"skipped_total"`
}

type SourceChange struct {
	ID             string       `json:"id"`
	SourceID       string       `json:"source_id"`
	Kind           string       `json:"kind"`
	TargetRevision uint64       `json:"target_revision,omitempty"`
	Status         string       `json:"status"`
	AuthIDs        []string     `json:"auth_ids,omitempty"`
	CreatedAt      time.Time    `json:"created_at,omitempty"`
	Error          string       `json:"error,omitempty"`
	Items          []ChangeItem `json:"items,omitempty"`
	Counts         ChangeCounts `json:"counts"`
	CompletedAt    time.Time    `json:"completed_at,omitempty"`
	ItemsTruncated bool         `json:"items_truncated,omitempty"`
}

type ChangeCounts struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Pending   int `json:"pending"`
}

type ChangeItem struct {
	AuthID       string `json:"auth_id"`
	Generation   uint64 `json:"generation,omitempty"`
	EndpointHash string `json:"endpoint_hash,omitempty"`
	OperationID  string `json:"operation_id,omitempty"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}

// Operation is durable before touching an auth. Secret recovery material never
// goes through management views; those return dedicated DTOs.
type Operation struct {
	ID                 string    `json:"id"`
	AuthID             string    `json:"auth_id"`
	Action             string    `json:"action"`
	ChangeID           string    `json:"change_id,omitempty"`
	ExpectedHash       string    `json:"expected_hash"`
	ExpectedGeneration uint64    `json:"expected_generation"`
	PreviousSourceID   string    `json:"previous_source_id,omitempty"`
	DesiredURL         string    `json:"desired_url,omitempty"`
	Clear              bool      `json:"clear,omitempty"`
	Auto               bool      `json:"auto,omitempty"`
	Binding            Binding   `json:"binding"`
	OldLease           *LeaseRef `json:"old_lease,omitempty"`
	ExcludeAutoBind    bool      `json:"exclude_auto_bind"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	NextAttemptAt      time.Time `json:"next_attempt_at,omitempty"`
	Error              string    `json:"error,omitempty"`
}

type SourceRuntime struct {
	Lifecycle      string       `json:"lifecycle,omitempty"`
	ChangeID       string       `json:"change_id,omitempty"`
	Health         HealthStatus `json:"health,omitempty"`
	HealthRevision uint64       `json:"health_revision,omitempty"`
	HealthChecked  time.Time    `json:"health_checked,omitempty"`
	RetryAt        time.Time    `json:"retry_at,omitempty"`
	LastError      string       `json:"last_error,omitempty"`
}

type ProxyState struct {
	Nodes            []Node                   `json:"nodes,omitempty"`
	Templates        []Template               `json:"templates,omitempty"`
	AutoBind         AutoBindConfig           `json:"auto_bind,omitempty"`
	AutoBindRuntime  AutoBindRuntime          `json:"auto_bind_runtime,omitempty"`
	Changes          []SourceChange           `json:"changes,omitempty"`
	ResinSource      ResinSourceState         `json:"resin_source,omitempty"`
	Operations       map[string]Operation     `json:"operations,omitempty"`
	SourceRuntime    map[string]SourceRuntime `json:"source_runtime,omitempty"`
	TemplateVersions []Template               `json:"template_versions,omitempty"`
	NodeVersions     []Node                   `json:"node_versions,omitempty"`
	JournalSequence  uint64                   `json:"journal_sequence,omitempty"`
}

type ResinSourceState struct {
	ID            string `json:"id,omitempty"`
	Name          string `json:"name,omitempty"`
	Enabled       bool   `json:"enabled,omitempty"`
	Revision      uint64 `json:"revision,omitempty"`
	ConfigVersion uint64 `json:"config_version,omitempty"`
	Deleted       bool   `json:"deleted,omitempty"`
}

type ValidationReport struct {
	Valid    bool              `json:"valid"`
	Errors   []ValidationIssue `json:"errors,omitempty"`
	Warnings []ValidationIssue `json:"warnings,omitempty"`
	Summary  map[string]any    `json:"summary,omitempty"`
	Samples  []TemplateSample  `json:"samples,omitempty"`
}

type ValidationIssue struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

func ValidateTemplate(t Template) ValidationReport {
	report := ValidationReport{Valid: true}
	compiled, err := CompileTemplate(t)
	if err != nil {
		report.Valid = false
		issue := ValidationIssue{Code: "template_invalid", Path: "template", Message: err.Error()}
		var validationError *ValidationError
		if errors.As(err, &validationError) {
			issue.Code = validationError.Code
			issue.Path = validationError.Path
		}
		report.Errors = append(report.Errors, issue)
		return report
	}
	report.Warnings = compiled.Warnings()
	report.Summary = map[string]any{
		"placeholder_count":      compiled.PlaceholderCount(),
		"group_row_count":        compiled.GroupRowCount(),
		"maximum_username_bytes": compiled.MaximumUsernameBytes(),
		"combination_count":      compiled.CombinationCount(),
		"has_random":             compiled.HasRandom(),
	}
	if compiled.CombinationCountCapped() {
		report.Summary["combination_count_capped"] = true
		report.Summary["combination_count_exact"] = compiled.CombinationCountExact()
	}
	report.Samples = compiled.Preview(3)
	return report
}
