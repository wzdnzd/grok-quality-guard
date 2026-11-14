package main

import (
	"os"
	"testing"
)

func TestPluginLogoConfiguration(t *testing.T) {
	registration := pluginRegistration()
	if registration.Metadata.Logo != pluginLogo {
		t.Fatalf("registration logo = %q, want %q", registration.Metadata.Logo, pluginLogo)
	}
	if info, errStat := os.Stat("assets/logo.png"); errStat != nil {
		t.Fatalf("logo asset is missing: %v", errStat)
	} else if info.Size() == 0 {
		t.Fatal("logo asset is empty")
	}
}
func TestRegistrationIncludesRoutingSelectionConfig(t *testing.T) {
	registration := pluginRegistration()
	fields := make(map[string]bool, len(registration.Metadata.ConfigFields))
	for _, field := range registration.Metadata.ConfigFields {
		fields[field.Name] = true
	}
	for _, name := range []string{
		"routing.selection_strategy",
		"routing.prefer_last_success",
		"routing.session_affinity",
		"routing.session_affinity_ttl",
		"routing.session_affinity_max_entries",
	} {
		if !fields[name] {
			t.Fatalf("registration is missing config field %q", name)
		}
	}
}
