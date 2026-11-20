package config

const (
	DefaultEventsLimit               = defaultEventsLimit
	DefaultProbeQueueSize            = defaultProbeQueueSize
	MaxProbeQueueSize                = maxProbeQueueSize
	DefaultProbeUnknownJitter        = defaultProbeUnknownJitter
	DefaultProbeScanInterval         = defaultProbeScanInterval
	DefaultProbeHealthyIntervalHours = defaultProbeHealthyIntervalHours
)

// Decode parses YAML over the built-in defaults and normalizes the result.
func Decode(raw []byte) (Config, error) {
	return decodeConfig(raw)
}

// NormalizePolicy validates and fills defaults in a runtime policy.
func NormalizePolicy(policy Policy) (Policy, error) {
	return normalizePolicy(policy)
}

// Revision returns the stable content hash used to detect YAML changes.
func Revision(raw []byte) string {
	return configRevision(raw)
}

// DefaultStatePath picks the first writable persistent state location.
func DefaultStatePath() string {
	return defaultStatePath()
}

// NormalizeProxyConfig validates proxy auto-bind configuration.
func NormalizeProxyConfig(cfg *ProxyConfig) error {
	return normalizeProxyConfig(cfg)
}

// UniqueNonEmptyStrings trims values and removes duplicates while preserving order.
func UniqueNonEmptyStrings(values []string) []string {
	return uniqueNonEmptyStrings(values)
}
