package app

import (
	"strings"

	"grok-quality-guard/internal/app/i18n"

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
	return pluginRegistrationForLocale(i18n.DefaultLocale)
}

func pluginRegistrationForLocale(locale string) registration {
	locale = registrationLocale(locale)
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "wzdnzd",
			GitHubRepository: "https://github.com/wzdnzd/grok-quality-guard",
			Logo:             pluginLogo,
			ConfigFields: []pluginapi.ConfigField{
				{Name: "guard", Type: pluginapi.ConfigFieldTypeEnum, Default: "passive", EnumValues: []string{"off", "passive", "transparent"}, Description: configDescription(locale, "guard")},
				{Name: "auto_action", Type: pluginapi.ConfigFieldTypeEnum, Default: "observe", EnumValues: []string{"observe", "cooldown_then_disable", "cooldown_only"}, Description: configDescription(locale, "auto_action")},
				{Name: "model_prefixes", Type: pluginapi.ConfigFieldTypeArray, Default: []string{"grok-"}, Description: configDescription(locale, "model_prefixes")},
				{Name: "priority", Type: pluginapi.ConfigFieldTypeInteger, Default: 10, Description: configDescription(locale, "priority")},
				{Name: "routing.strategy.selection", Type: pluginapi.ConfigFieldTypeEnum, Default: "round_robin", EnumValues: []string{"round_robin", "random", "fill_first"}, Description: configDescription(locale, "routing.strategy.selection")},
				{Name: "routing.strategy.prefer_last_success", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: configDescription(locale, "routing.strategy.prefer_last_success")},
				{Name: "routing.affinity.enabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: configDescription(locale, "routing.affinity.enabled")},
				{Name: "routing.affinity.ttl", Type: pluginapi.ConfigFieldTypeString, Default: "1h", Description: configDescription(locale, "routing.affinity.ttl")},
				{Name: "routing.affinity.max_entries", Type: pluginapi.ConfigFieldTypeInteger, Default: 10000, Description: configDescription(locale, "routing.affinity.max_entries")},
				{Name: "routing.scoring.enabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: configDescription(locale, "routing.scoring.enabled")},
				{Name: "routing.scoring.max_accounts_per_bucket", Type: pluginapi.ConfigFieldTypeInteger, Default: appconfig.DefaultRoutingMaxAccountsPerBucket, Description: configDescription(locale, "routing.scoring.max_accounts_per_bucket")},
				{Name: "routing.scoring.half_life_hours", Type: pluginapi.ConfigFieldTypeInteger, Default: appconfig.DefaultRoutingHalfLifeHours, Description: configDescription(locale, "routing.scoring.half_life_hours")},
				{Name: "routing.scoring.weights.success", Type: pluginapi.ConfigFieldTypeNumber, Default: 0.50, Description: configDescription(locale, "routing.scoring.weights.success")},
				{Name: "routing.scoring.weights.ttft", Type: pluginapi.ConfigFieldTypeNumber, Default: 0.30, Description: configDescription(locale, "routing.scoring.weights.ttft")},
				{Name: "routing.scoring.weights.tps", Type: pluginapi.ConfigFieldTypeNumber, Default: 0.20, Description: configDescription(locale, "routing.scoring.weights.tps")},
				{Name: "routing.scoring.evidence.probe", Type: pluginapi.ConfigFieldTypeNumber, Default: 0.65, Description: configDescription(locale, "routing.scoring.evidence.probe")},
				{Name: "routing.scoring.evidence.transparent", Type: pluginapi.ConfigFieldTypeNumber, Default: 0.35, Description: configDescription(locale, "routing.scoring.evidence.transparent")},
				{Name: "routing.scoring.evidence.passive", Type: pluginapi.ConfigFieldTypeNumber, Default: 0.0, Description: configDescription(locale, "routing.scoring.evidence.passive")},
				{Name: "routing.scoring.refresh.min_interval", Type: pluginapi.ConfigFieldTypeString, Default: appconfig.DefaultRoutingRefreshMinInterval.String(), Description: configDescription(locale, "routing.scoring.refresh.min_interval")},
				{Name: "routing.scoring.refresh.max_age", Type: pluginapi.ConfigFieldTypeString, Default: appconfig.DefaultRoutingRefreshMaxAge.String(), Description: configDescription(locale, "routing.scoring.refresh.max_age")},
				{Name: "routing.scoring.refresh.drift_threshold", Type: pluginapi.ConfigFieldTypeNumber, Default: appconfig.DefaultRoutingDriftThreshold, Description: configDescription(locale, "routing.scoring.refresh.drift_threshold")},
				{Name: "quality.soft_tps", Type: pluginapi.ConfigFieldTypeNumber, Default: 500, Description: configDescription(locale, "quality.soft_tps")},
				{Name: "quality.hard_tps", Type: pluginapi.ConfigFieldTypeNumber, Default: 1000, Description: configDescription(locale, "quality.hard_tps")},
				{Name: "quality.consecutive_soft", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: configDescription(locale, "quality.consecutive_soft")},
				{Name: "quality.consecutive_errors", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: configDescription(locale, "quality.consecutive_errors")},
				{Name: "quality.consecutive_missing_thinking", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: configDescription(locale, "quality.consecutive_missing_thinking")},
				{Name: "quality.strike_decay_after", Type: pluginapi.ConfigFieldTypeInteger, Default: 48, Description: configDescription(locale, "quality.strike_decay_after")},
				{Name: "quality.strike_decay_min_healthy", Type: pluginapi.ConfigFieldTypeInteger, Default: 10, Description: configDescription(locale, "quality.strike_decay_min_healthy")},
				{Name: "quality.thinking_guard", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: configDescription(locale, "quality.thinking_guard")},
				{Name: "quality.non_reasoning_models", Type: pluginapi.ConfigFieldTypeArray, Default: []string{}, Description: configDescription(locale, "quality.non_reasoning_models")},
				{Name: "quality.cross_verify", Type: pluginapi.ConfigFieldTypeEnum, Default: "thinking", EnumValues: []string{"off", "soft", "thinking", "all"}, Description: configDescription(locale, "quality.cross_verify")},
				{Name: "quality.min_generation_ms", Type: pluginapi.ConfigFieldTypeInteger, Default: 1000, Description: configDescription(locale, "quality.min_generation_ms")},
				{Name: "quality.burst_guard", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: configDescription(locale, "quality.burst_guard")},
				{Name: "quality.min_passive_output_tokens", Type: pluginapi.ConfigFieldTypeInteger, Default: 32, Description: configDescription(locale, "quality.min_passive_output_tokens")},
				{Name: "quality.min_hold_output_tokens", Type: pluginapi.ConfigFieldTypeInteger, Default: 8, Description: configDescription(locale, "quality.min_hold_output_tokens")},
				{Name: "quality.hold_timeout", Type: pluginapi.ConfigFieldTypeString, Default: "30s", Description: configDescription(locale, "quality.hold_timeout")},
				{Name: "quality.attempt_timeout", Type: pluginapi.ConfigFieldTypeString, Default: "60s", Description: configDescription(locale, "quality.attempt_timeout")},
				{Name: "quality.request_timeout", Type: pluginapi.ConfigFieldTypeString, Default: "120s", Description: configDescription(locale, "quality.request_timeout")},
				{Name: "quality.hold_keepalive", Type: pluginapi.ConfigFieldTypeString, Default: "5s", Description: configDescription(locale, "quality.hold_keepalive")},
				{Name: "quality.shadow_mode", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: configDescription(locale, "quality.shadow_mode")},
				{Name: "quality.hold_max_buffer_bytes", Type: pluginapi.ConfigFieldTypeInteger, Default: 4194304, Description: configDescription(locale, "quality.hold_max_buffer_bytes")},
				{Name: "quality.min_encrypted_bytes", Type: pluginapi.ConfigFieldTypeInteger, Default: 256, Description: configDescription(locale, "quality.min_encrypted_bytes")},
				{Name: "quality.encrypted_bytes_per_reasoning_token", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: configDescription(locale, "quality.encrypted_bytes_per_reasoning_token")},
				{Name: "quality.missing_thinking_cooldown", Type: pluginapi.ConfigFieldTypeString, Default: "12h", Description: configDescription(locale, "quality.missing_thinking_cooldown")},
				{Name: "quality.empty_stream_cooldown", Type: pluginapi.ConfigFieldTypeString, Default: "15m", Description: configDescription(locale, "quality.empty_stream_cooldown")},
				{Name: "quality.max_attempts", Type: pluginapi.ConfigFieldTypeInteger, Default: 4, Description: configDescription(locale, "quality.max_attempts")},
				{Name: "quality.debug_logging", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: configDescription(locale, "quality.debug_logging")},
				{Name: "quality.on_exhausted", Type: pluginapi.ConfigFieldTypeEnum, Default: "fail_unverified", EnumValues: []string{"fail_unverified", "fail_closed"}, Description: configDescription(locale, "quality.on_exhausted")},
				{Name: "probe.model", Type: pluginapi.ConfigFieldTypeString, Default: "grok-4.5", Description: configDescription(locale, "probe.model")},
				{Name: "probe.timeout", Type: pluginapi.ConfigFieldTypeString, Default: "90s", Description: configDescription(locale, "probe.timeout")},
				{Name: "probe.max_output_tokens", Type: pluginapi.ConfigFieldTypeInteger, Default: 768, Description: configDescription(locale, "probe.max_output_tokens")},
				{Name: "probe.concurrency", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: configDescription(locale, "probe.concurrency")},
				{Name: "probe.queue_size", Type: pluginapi.ConfigFieldTypeInteger, Default: appconfig.DefaultProbeQueueSize, Description: configDescription(locale, "probe.queue_size")},
				{Name: "probe.scan_interval", Type: pluginapi.ConfigFieldTypeString, Default: appconfig.DefaultProbeScanInterval.String(), Description: configDescription(locale, "probe.scan_interval")},
				{Name: "probe.healthy_interval_hours", Type: pluginapi.ConfigFieldTypeInteger, Default: appconfig.DefaultProbeHealthyIntervalHours, Description: configDescription(locale, "probe.healthy_interval_hours")},
				{Name: "probe.unknown_delay", Type: pluginapi.ConfigFieldTypeString, Default: "30s", Description: configDescription(locale, "probe.unknown_delay")},
				{Name: "probe.unknown_jitter", Type: pluginapi.ConfigFieldTypeString, Default: appconfig.DefaultProbeUnknownJitter.String(), Description: configDescription(locale, "probe.unknown_jitter")},
				{Name: "probe.suspect_backoff", Type: pluginapi.ConfigFieldTypeString, Default: "2m", Description: configDescription(locale, "probe.suspect_backoff")},
				{Name: "probe.max_backoff", Type: pluginapi.ConfigFieldTypeString, Default: "1h", Description: configDescription(locale, "probe.max_backoff")},
				{Name: "probe.entry_protocol", Type: pluginapi.ConfigFieldTypeString, Default: "codex", Description: configDescription(locale, "probe.entry_protocol")},
				{Name: "probe.exit_protocol", Type: pluginapi.ConfigFieldTypeString, Default: "codex", Description: configDescription(locale, "probe.exit_protocol")},
				{Name: "state_file", Type: pluginapi.ConfigFieldTypeString, Description: configDescription(locale, "state_file")},
				{Name: "task_history.enabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: configDescription(locale, "task_history.enabled")},
				{Name: "task_history.retention_days", Type: pluginapi.ConfigFieldTypeInteger, Default: 3, Description: configDescription(locale, "task_history.retention_days")},
				{Name: "task_history.max_records", Type: pluginapi.ConfigFieldTypeInteger, Default: 500, Description: configDescription(locale, "task_history.max_records")},
				{Name: "task_history.max_bytes", Type: pluginapi.ConfigFieldTypeInteger, Default: 134217728, Description: configDescription(locale, "task_history.max_bytes")},
				{Name: "task_history.max_items_per_task", Type: pluginapi.ConfigFieldTypeInteger, Default: 10000, Description: configDescription(locale, "task_history.max_items_per_task")},
				{Name: "proxy.auto_rotate_after_quality_failure", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: configDescription(locale, "proxy.auto_rotate_after_quality_failure")},
				{Name: "proxy.rotate_after_errors", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: configDescription(locale, "proxy.rotate_after_errors")},
				{Name: "proxy.auto_bind.enabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: configDescription(locale, "proxy.auto_bind.enabled")},
				{Name: "proxy.auto_bind.source_scope", Type: pluginapi.ConfigFieldTypeEnum, Default: "all", EnumValues: []string{"all", "nodes", "templates", "resin"}, Description: configDescription(locale, "proxy.auto_bind.source_scope")},
				{Name: "proxy.auto_bind.source_ids", Type: pluginapi.ConfigFieldTypeArray, Default: []string{}, Description: configDescription(locale, "proxy.auto_bind.source_ids")},
				{Name: "proxy.auto_bind.concurrency", Type: pluginapi.ConfigFieldTypeInteger, Default: 2, Description: configDescription(locale, "proxy.auto_bind.concurrency")},
				{Name: "proxy.auto_bind.queue_size", Type: pluginapi.ConfigFieldTypeInteger, Default: 1000, Description: configDescription(locale, "proxy.auto_bind.queue_size")},
				{Name: "proxy.auto_bind.max_per_cycle", Type: pluginapi.ConfigFieldTypeInteger, Default: 100, Description: configDescription(locale, "proxy.auto_bind.max_per_cycle")},
				{Name: "proxy.auto_bind.scan_interval", Type: pluginapi.ConfigFieldTypeString, Default: "30s", Description: configDescription(locale, "proxy.auto_bind.scan_interval")},
				{Name: "proxy.auto_bind.require_healthy", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: configDescription(locale, "proxy.auto_bind.require_healthy")},
				{Name: "proxy.auto_bind.health_max_age", Type: pluginapi.ConfigFieldTypeString, Default: "10m", Description: configDescription(locale, "proxy.auto_bind.health_max_age")},
				{Name: "proxy.auto_bind.include_guard_disabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: configDescription(locale, "proxy.auto_bind.include_guard_disabled")},
				{Name: "proxy.auto_bind.include_externally_disabled", Type: pluginapi.ConfigFieldTypeBoolean, Default: false, Description: configDescription(locale, "proxy.auto_bind.include_externally_disabled")},
				{Name: "proxy.auto_bind.manual_clear_policy", Type: pluginapi.ConfigFieldTypeEnum, Default: "rebind", EnumValues: []string{"rebind", "pause"}, Description: configDescription(locale, "proxy.auto_bind.manual_clear_policy")},
				{Name: "proxy.auto_bind.retry_initial", Type: pluginapi.ConfigFieldTypeString, Default: "1m", Description: configDescription(locale, "proxy.auto_bind.retry_initial")},
				{Name: "proxy.auto_bind.retry_max", Type: pluginapi.ConfigFieldTypeString, Default: "1h", Description: configDescription(locale, "proxy.auto_bind.retry_max")},
				{Name: "events_limit", Type: pluginapi.ConfigFieldTypeInteger, Default: 1000, Description: configDescription(locale, "events_limit")},
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

func configDescription(locale, name string) string {
	key := "registration.config." + strings.NewReplacer(".", "_", "-", "_").Replace(strings.TrimSpace(name))
	return i18n.T(locale, key, nil)
}

func registrationLocale(locale string) string {
	if normalized := i18n.NormalizeLocale(locale); normalized != "" {
		return normalized
	}
	return i18n.DefaultLocale
}

func (p *Plugin) ManagementRegistration() managementRegistrationResponse {
	return p.ManagementRegistrationForLocale(i18n.DefaultLocale)
}

func (p *Plugin) ManagementRegistrationForLocale(locale string) managementRegistrationResponse {
	locale = registrationLocale(locale)
	return managementRegistrationResponse{
		Routes: []pluginapi.ManagementRoute{
			{
				Method: "POST",
				Path:   "/plugins/" + pluginID + "/api",
			},
			{
				Method: "GET",
				Path:   "/plugins/" + pluginID + "/locales",
			},
			{
				Method: "GET",
				Path:   "/plugins/" + pluginID + "/settings/schema",
			},
		},
		Resources: []pluginapi.ResourceRoute{{
			Path:        "/dashboard",
			Menu:        pluginName,
			Description: i18n.T(locale, "registration.description", nil),
		}},
	}
}
