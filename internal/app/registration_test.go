package app

import (
	"strings"
	"testing"

	"grok-quality-guard/internal/app/i18n"
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

func TestRegistrationDescriptionsUseRequestedLocale(t *testing.T) {
	for _, locale := range i18n.SupportedLocales {
		registration := pluginRegistrationForLocale(locale)
		for _, field := range registration.Metadata.ConfigFields {
			key := "registration.config." + strings.NewReplacer(".", "_", "-", "_").Replace(strings.TrimSpace(field.Name))
			if !i18n.Has(locale, key) {
				t.Fatalf("%s config field %s has no locale key %s", locale, field.Name, key)
			}
			if field.Description == key {
				t.Fatalf("%s config field %s description fell back to key %s", locale, field.Name, key)
			}
			if locale == "en" && containsHan(field.Description) {
				t.Fatalf("English config field %s contains Chinese text: %q", field.Name, field.Description)
			}
		}
		var plugin Plugin
		for _, resource := range plugin.ManagementRegistrationForLocale(locale).Resources {
			if !i18n.Has(locale, "registration.description") {
				t.Fatalf("%s management registration description key is missing", locale)
			}
			if resource.Description == "registration.description" {
				t.Fatalf("%s management registration description fell back to its key", locale)
			}
			if locale == "en" && containsHan(resource.Description) {
				t.Fatalf("English management registration description contains Chinese text: %q", resource.Description)
			}
		}
	}
}

func containsHan(value string) bool {
	for _, char := range value {
		if char >= '\u4e00' && char <= '\u9fff' {
			return true
		}
	}
	return false
}

func TestManagementRegistrationExposesLocaleResources(t *testing.T) {
	var plugin Plugin
	found := false
	for _, route := range plugin.ManagementRegistration().Routes {
		if route.Method == "GET" && route.Path == "/plugins/grok-quality-guard/locales" {
			found = true
		}
	}
	if !found {
		t.Fatal("management registration is missing GET /plugins/grok-quality-guard/locales")
	}
}
