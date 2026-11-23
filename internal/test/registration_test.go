package test

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"grok-quality-guard/internal/app"
	appconfig "grok-quality-guard/internal/config"
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

func TestRegistrationIncludesAllPluginConfigFields(t *testing.T) {
	registration := app.PluginRegistration()
	registered := make(map[string]struct{}, len(registration.Metadata.ConfigFields))
	for _, field := range registration.Metadata.ConfigFields {
		registered[field.Name] = struct{}{}
	}
	// enabled and store are host-owned plugin instance fields, not plugin policy.
	excluded := map[string]struct{}{"enabled": {}, "store": {}}
	var missing []string
	for _, name := range configFieldPaths(reflect.TypeOf(appconfig.Config{}), "") {
		if _, skip := excluded[name]; skip {
			continue
		}
		if _, ok := registered[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("registration is missing config fields: %s", strings.Join(missing, ", "))
	}
}

func configFieldPaths(value reflect.Type, prefix string) []string {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil
	}
	var paths []string
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if hasYAMLFields(field.Type) {
			paths = append(paths, configFieldPaths(field.Type, path)...)
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

func hasYAMLFields(value reflect.Type) bool {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	for index := 0; index < value.NumField(); index++ {
		field := value.Field(index)
		if field.PkgPath != "" {
			continue
		}
		name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			return true
		}
	}
	return false
}
