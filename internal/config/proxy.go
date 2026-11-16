package config

import (
	"errors"
	"fmt"
	proxy "grok-quality-guard/internal/proxy"
	"strings"
	"time"
)

type ProxyConfig struct {
	AutoRotateAfterQualityFailure bool                 `yaml:"auto_rotate_after_quality_failure" json:"auto_rotate_after_quality_failure"`
	AutoBind                      proxy.AutoBindConfig `yaml:"auto_bind" json:"auto_bind"`
}

func DefaultProxyConfig() ProxyConfig {
	return ProxyConfig{
		AutoRotateAfterQualityFailure: true,
		AutoBind:                      proxy.DefaultAutoBindConfig(),
	}
}
func normalizeProxyConfig(cfg *ProxyConfig) error {
	if cfg == nil {
		return errors.New("proxy config is nil")
	}
	autoBind, errNormalize := cfg.AutoBind.Normalize()
	if errNormalize != nil {
		return errNormalize
	}
	cfg.AutoBind = autoBind
	if errValidate := validateProxyDuration("proxy.auto_bind.scan_interval", cfg.AutoBind.ScanInterval, time.Second, 24*time.Hour); errValidate != nil {
		return errValidate
	}
	if errValidate := validateProxyDuration("proxy.auto_bind.health_max_age", cfg.AutoBind.HealthMaxAge, time.Second, 24*time.Hour); errValidate != nil {
		return errValidate
	}
	if errValidate := validateProxyDuration("proxy.auto_bind.retry_initial", cfg.AutoBind.RetryInitial, time.Second, 24*time.Hour); errValidate != nil {
		return errValidate
	}
	if errValidate := validateProxyDuration("proxy.auto_bind.retry_max", cfg.AutoBind.RetryMax, time.Second, 7*24*time.Hour); errValidate != nil {
		return errValidate
	}
	initial, _ := time.ParseDuration(cfg.AutoBind.RetryInitial)
	maximum, _ := time.ParseDuration(cfg.AutoBind.RetryMax)
	if maximum < initial {
		return fmt.Errorf("proxy.auto_bind.retry_max must be >= retry_initial")
	}
	if cfg.AutoBind.MaxPerCycle > cfg.AutoBind.QueueSize {
		cfg.AutoBind.MaxPerCycle = cfg.AutoBind.QueueSize
	}
	for index := range cfg.AutoBind.SourceIDs {
		cfg.AutoBind.SourceIDs[index] = strings.TrimSpace(cfg.AutoBind.SourceIDs[index])
	}
	cfg.AutoBind.SourceIDs = uniqueNonEmptyStrings(cfg.AutoBind.SourceIDs)
	return nil
}
func validateProxyDuration(name, value string, minimum, maximum time.Duration) error {
	parsed, errParse := time.ParseDuration(strings.TrimSpace(value))
	if errParse != nil {
		return fmt.Errorf("%s is invalid: %w", name, errParse)
	}
	if parsed < minimum || parsed > maximum {
		return fmt.Errorf("%s must be between %s and %s", name, minimum, maximum)
	}
	return nil
}
func uniqueNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
