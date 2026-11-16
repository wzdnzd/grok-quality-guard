package app

import appredact "grok-quality-guard/internal/app/redact"

// PluginRegistration returns the ABI registration payload assembled from the
// application capabilities and configuration schema.
func PluginRegistration() registration {
	return pluginRegistration()
}

// PluginIdentifier is the stable executor and management namespace.
func PluginIdentifier() string {
	return pluginID
}

// SanitizeError produces the bounded, user-facing error text used by the ABI.
func SanitizeError(errValue error) string {
	return appredact.SanitizeError(errValue)
}
