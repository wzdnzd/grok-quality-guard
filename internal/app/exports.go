package app

import (
	"grok-quality-guard/internal/app/i18n"
	appredact "grok-quality-guard/internal/app/redact"
)

// PluginRegistration returns the ABI registration payload assembled from the
// application capabilities and configuration schema.
func PluginRegistration() registration {
	return pluginRegistrationForLocale(i18n.DefaultLocale)
}

// PluginRegistrationForLocale returns the ABI registration payload with
// user-visible metadata localized for the requested locale.
func PluginRegistrationForLocale(locale string) registration {
	return pluginRegistrationForLocale(locale)
}

// PluginIdentifier is the stable executor and management namespace.
func PluginIdentifier() string {
	return pluginID
}

// SanitizeError produces the bounded, user-facing error text used by the ABI.
func SanitizeError(errValue error) string {
	return appredact.SanitizeError(errValue)
}
