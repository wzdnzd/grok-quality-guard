package test

import (
	"testing"

	"grok-quality-guard/internal/app"
)

// TestRegistrationDeclaresClientOutputFormats guards the executor format contract.
// The plugin re-emits upstream Responses SSE frames unchanged, so every client
// protocol it serves must be declared as an output format.
func TestRegistrationDeclaresClientOutputFormats(t *testing.T) {
	formats := app.PluginRegistration().Capabilities.ExecutorOutputFormats
	declared := make(map[string]bool, len(formats))
	for _, format := range formats {
		declared[format] = true
	}
	for _, required := range []string{"codex", "openai-response"} {
		if !declared[required] {
			t.Fatalf("executor output formats = %v, want %q declared", formats, required)
		}
	}
}
