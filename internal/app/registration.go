package app

import (
	appconfig "grok-quality-guard/internal/config"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	pluginID   = "grok-quality-guard"
	pluginName = "Grok Quality Guard"
	pluginLogo = "https://raw.githubusercontent.com/wzdnzd/grok-quality-guard/main/assets/logo.png"
)

var pluginVersion = "0.1.0"

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRouter            bool                         `json:"model_router"`
	Executor               bool                         `json:"executor"`
	ExecutorModelScope     pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats   []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats  []string                     `json:"executor_output_formats,omitempty"`
	ResponseInterceptor    bool                         `json:"response_interceptor"`
	StreamChunkInterceptor bool                         `json:"response_stream_interceptor"`
	UsagePlugin            bool                         `json:"usage_plugin"`
	ManagementAPI          bool                         `json:"management_api"`
}

type managementRegistrationResponse struct {
	Routes    []pluginapi.ManagementRoute `json:"routes,omitempty"`
	Resources []pluginapi.ResourceRoute   `json:"resources,omitempty"`
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "wzdnzd",
			GitHubRepository: "https://github.com/wzdnzd/grok-quality-guard",
			Logo:             pluginLogo,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "passive_guard", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Observe request quality without changing routing."},
				{Name: "transparent_guard", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Gate all Grok/xAI OAuth requests before emitting; unsafe replay gets one attempt."},
				{Name: "auto_action", Type: pluginapi.ConfigFieldTypeEnum, Default: "observe", EnumValues: []string{"observe", "cooldown_then_disable", "cooldown_only"}, Description: "Quality policy action for confirmed anomalies."},
				{Name: "model_prefixes", Type: pluginapi.ConfigFieldTypeArray, Default: []string{"grok-"}, Description: "Model prefixes considered Grok traffic."},
				{Name: "priority", Type: pluginapi.ConfigFieldTypeInteger, Default: 10, Description: "Plugin priority when multiple routing plugins are installed."},
				{Name: "routing.selection_strategy", Type: pluginapi.ConfigFieldTypeEnum, Default: "round_robin", EnumValues: []string{"round_robin", "random", "fill_first"}, Description: "Strategy used inside each transparent auth quality and priority layer."},
				{Name: "routing.prefer_last_success", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Prefer the last healthy idle transparent auth before applying the layer strategy."},
				{Name: "routing.session_affinity", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Keep a stable session-to-auth binding while the auth remains eligible."},
				{Name: "routing.session_affinity_ttl", Type: pluginapi.ConfigFieldTypeString, Default: "1h", Description: "Session affinity binding TTL, refreshed after successful requests."},
				{Name: "routing.session_affinity_max_entries", Type: pluginapi.ConfigFieldTypeInteger, Default: 10000, Description: "Maximum retained session affinity bindings."},
				{Name: "quality.soft_tps", Type: pluginapi.ConfigFieldTypeNumber, Default: 500, Description: "Soft output tokens-per-second warning threshold."},
				{Name: "quality.hard_tps", Type: pluginapi.ConfigFieldTypeNumber, Default: 1000, Description: "Hard output tokens-per-second anomaly threshold."},
				{Name: "quality.consecutive_soft", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: "Consecutive soft-TPS observations required before isolation or cross-verify."},
				{Name: "quality.consecutive_errors", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: "Consecutive probe or proxy errors required before isolation."},
				{Name: "quality.consecutive_missing_thinking", Type: pluginapi.ConfigFieldTypeInteger, Default: 1, Description: "Consecutive missing-thinking observations required before isolation or cross-verify."},
				{Name: "quality.thinking_guard", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Enable missing-thinking degradation detection."},
				{Name: "quality.soft_cross_verify", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Confirm consecutive soft-TPS anomalies with an asynchronous active probe before isolation."},
				{Name: "quality.thinking_cross_verify", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Confirm consecutive missing-thinking anomalies with an asynchronous active probe before isolation."},
				{Name: "quality.min_generation_ms", Type: pluginapi.ConfigFieldTypeInteger, Default: 1000, Description: "Minimum generation window before a fast burst is considered anomalous."},
				{Name: "quality.min_passive_output_tokens", Type: pluginapi.ConfigFieldTypeInteger, Default: 32, Description: "Minimum output tokens for passive speed auditing."},
				{Name: "quality.min_hold_output_tokens", Type: pluginapi.ConfigFieldTypeInteger, Default: 8, Description: "Minimum visible output before transparent streaming release."},
				{Name: "quality.hold_timeout", Type: pluginapi.ConfigFieldTypeString, Default: "30s", Description: "Maximum transparent stream hold time."},
				{Name: "quality.hold_max_buffer_bytes", Type: pluginapi.ConfigFieldTypeInteger, Default: 4194304, Description: "Maximum transparent stream hold buffer."},
				{Name: "quality.min_encrypted_bytes", Type: pluginapi.ConfigFieldTypeInteger, Default: 256, Description: "Minimum encrypted reasoning bytes."},
				{Name: "quality.encrypted_bytes_per_reasoning_token", Type: pluginapi.ConfigFieldTypeInteger, Default: 4, Description: "Required encrypted bytes per billed reasoning token."},
				{Name: "quality.missing_thinking_cooldown", Type: pluginapi.ConfigFieldTypeString, Default: "12h", Description: "Cooldown after the first confirmed missing-thinking anomaly."},
				{Name: "quality.empty_stream_cooldown", Type: pluginapi.ConfigFieldTypeString, Default: "15m", Description: "Cooldown after an empty stream."},
				{Name: "quality.max_attempts", Type: pluginapi.ConfigFieldTypeInteger, Default: 6, Description: "Maximum transparent upstream attempts per request."},
				{Name: "quality.on_exhausted", Type: pluginapi.ConfigFieldTypeEnum, Default: "fail_closed", EnumValues: []string{"fail_closed", "fail_open"}, Description: "Behavior after every transparent candidate fails the quality gate."},
				{Name: "probe.recovery_only", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Probe only unknown, suspect, and cooling accounts."},
				{Name: "probe.model", Type: pluginapi.ConfigFieldTypeString, Default: "grok-4.5", Description: "Model used by active recovery probes."},
				{Name: "probe.timeout", Type: pluginapi.ConfigFieldTypeString, Default: "90s", Description: "Active probe timeout."},
				{Name: "probe.max_output_tokens", Type: pluginapi.ConfigFieldTypeInteger, Default: 768, Description: "Active probe max output tokens."},
				{Name: "probe.concurrency", Type: pluginapi.ConfigFieldTypeInteger, Default: 1, Description: "Maximum worker count for an active probe or batch account task."},
				{Name: "probe.queue_size", Type: pluginapi.ConfigFieldTypeInteger, Default: appconfig.DefaultProbeQueueSize, Description: "Maximum number of accounts waiting in bounded background queues."},
				{Name: "probe.unknown_delay", Type: pluginapi.ConfigFieldTypeString, Default: "30s", Description: "Delay before the first probe of a new account."},
				{Name: "probe.unknown_jitter", Type: pluginapi.ConfigFieldTypeString, Default: appconfig.DefaultProbeUnknownJitter.String(), Description: "Deterministic jitter added to the first probe delay of new accounts."},
				{Name: "probe.suspect_backoff", Type: pluginapi.ConfigFieldTypeString, Default: "15m", Description: "Initial suspect probe backoff."},
				{Name: "probe.max_backoff", Type: pluginapi.ConfigFieldTypeString, Default: "1h", Description: "Maximum suspect probe backoff."},
				{Name: "state_file", Type: pluginapi.ConfigFieldTypeString, Description: "Persistent state file path. Defaults to /CLIProxyAPI/plugins/data/grok-quality-guard/state.json when writable; mount that directory as a Docker volume."},
				{Name: "task_history.enabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Persist completed background tasks and task item details outside state.json."},
				{Name: "task_history.retention_days", Type: pluginapi.ConfigFieldTypeInteger, Default: 3, Description: "Number of days to retain completed task history."},
				{Name: "task_history.max_records", Type: pluginapi.ConfigFieldTypeInteger, Default: 500, Description: "Maximum number of retained task history records."},
				{Name: "task_history.max_bytes", Type: pluginapi.ConfigFieldTypeInteger, Default: 134217728, Description: "Maximum compressed task history disk usage in bytes."},
				{Name: "task_history.max_items_per_task", Type: pluginapi.ConfigFieldTypeInteger, Default: 10000, Description: "Maximum item details retained for one task."},
				{Name: "proxy.auto_rotate_after_quality_failure", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Rotate any managed proxy after a confirmed quality failure."},
				{Name: "proxy.auto_bind.enabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Automatically bind discovered unbound Grok/xAI auth files."},
				{Name: "proxy.auto_bind.source_scope", Type: pluginapi.ConfigFieldTypeEnum, Default: "all", EnumValues: []string{"all", "nodes", "templates", "resin"}, Description: "Proxy source kinds eligible for automatic binding."},
				{Name: "proxy.auto_bind.source_ids", Type: pluginapi.ConfigFieldTypeArray, Default: []string{}, Description: "Optional source ID allowlist for automatic binding."},
				{Name: "proxy.auto_bind.concurrency", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: "Maximum concurrent automatic proxy bindings."},
				{Name: "proxy.auto_bind.queue_size", Type: pluginapi.ConfigFieldTypeInteger, Default: 1000, Description: "Maximum automatic binding queue size."},
				{Name: "proxy.auto_bind.max_per_cycle", Type: pluginapi.ConfigFieldTypeInteger, Default: 100, Description: "Maximum unbound auth files discovered per scan."},
				{Name: "proxy.auto_bind.scan_interval", Type: pluginapi.ConfigFieldTypeString, Default: "30s", Description: "Automatic unbound-auth scan interval."},
				{Name: "proxy.auto_bind.require_healthy", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Only use sources with fresh successful health checks."},
				{Name: "proxy.auto_bind.health_max_age", Type: pluginapi.ConfigFieldTypeString, Default: "10m", Description: "Maximum accepted proxy health-check age."},
				{Name: "proxy.auto_bind.include_guard_disabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Include accounts disabled by this guard in automatic binding."},
				{Name: "proxy.auto_bind.include_externally_disabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: "Include externally disabled auth files in automatic binding."},
				{Name: "proxy.auto_bind.manual_clear_policy", Type: pluginapi.ConfigFieldTypeEnum, Default: "rebind", EnumValues: []string{"rebind", "pause"}, Description: "Default automatic-binding behavior after a manual proxy clear."},
				{Name: "proxy.auto_bind.retry_initial", Type: pluginapi.ConfigFieldTypeString, Default: "1m", Description: "Initial automatic binding retry delay."},
				{Name: "proxy.auto_bind.retry_max", Type: pluginapi.ConfigFieldTypeString, Default: "1h", Description: "Maximum automatic binding retry delay."},
				{Name: "events_limit", Type: pluginapi.ConfigFieldTypeInteger, Default: 1000, Description: "Maximum retained quality events."},
			},
		},
		Capabilities: registrationCapability{
			ModelRouter:            true,
			Executor:               true,
			ExecutorModelScope:     pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats:   []string{"codex"},
			ExecutorOutputFormats:  []string{"codex", "openai-response"},
			ResponseInterceptor:    true,
			StreamChunkInterceptor: true,
			UsagePlugin:            true,
			ManagementAPI:          true,
		},
	}
}

func (p *Plugin) ManagementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{{
			Method: "POST",
			Path:   "/plugins/" + pluginID + "/api",
		}},
		Resources: []pluginapi.ResourceRoute{{
			Path:        "/dashboard",
			Menu:        pluginName,
			Description: "管理 Grok 账号质量、Resin 租约、主动探测与守护策略。",
		}},
	}
}
