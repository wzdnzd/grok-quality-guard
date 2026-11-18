package app

import (
	"strings"
	"testing"
)

func TestRegistrationCopyHasNoTerminalPunctuation(t *testing.T) {
	registration := pluginRegistration()
	for _, field := range registration.Metadata.ConfigFields {
		if registrationCopyEndsWithPunctuation(field.Description) {
			t.Fatalf("config field %s description ends with punctuation: %q", field.Name, field.Description)
		}
	}
	var plugin Plugin
	for _, resource := range plugin.ManagementRegistration().Resources {
		if registrationCopyEndsWithPunctuation(resource.Description) {
			t.Fatalf("resource %s description ends with punctuation: %q", resource.Path, resource.Description)
		}
	}
}

func registrationCopyEndsWithPunctuation(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	var last rune
	for _, char := range value {
		last = char
	}
	return strings.ContainsRune("。！？…；：，、,.!?;:", last)
}
