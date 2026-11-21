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
		"routing.strategy.selection",
		"routing.strategy.prefer_last_success",
		"routing.affinity.enabled",
		"routing.affinity.ttl",
		"routing.affinity.max_entries",
		"routing.scoring.enabled",
		"routing.scoring.max_accounts_per_bucket",
		"routing.scoring.weights.success",
		"routing.scoring.weights.ttft",
		"routing.scoring.weights.tps",
		"routing.scoring.evidence.probe",
		"routing.scoring.evidence.transparent",
		"routing.scoring.evidence.passive",
		"routing.scoring.refresh.min_interval",
		"routing.scoring.refresh.max_age",
		"routing.scoring.refresh.drift_threshold",
	} {
		if !fields[name] {
			t.Fatalf("registration is missing config field %q", name)
		}
	}
}
