package test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"grok-quality-guard/internal/app"
)

func TestPluginLogoConfiguration(t *testing.T) {
	registration := app.PluginRegistration()
	if !strings.HasSuffix(registration.Metadata.Logo, "/assets/logo.png") {
		t.Fatalf("registration logo = %q, want assets/logo.png", registration.Metadata.Logo)
	}
	_, filename, _, _ := runtime.Caller(0)
	logoPath := filepath.Join(filepath.Dir(filename), "..", "..", "assets", "logo.png")
	if info, errStat := os.Stat(logoPath); errStat != nil {
		t.Fatalf("logo asset is missing: %v", errStat)
	} else if info.Size() == 0 {
		t.Fatal("logo asset is empty")
	}
}

func TestRegistrationIncludesRoutingSelectionConfig(t *testing.T) {
	registration := app.PluginRegistration()
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
