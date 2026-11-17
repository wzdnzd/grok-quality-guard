package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"grok-quality-guard/internal/config"
)

func writeStateWithRetiredMutation(t *testing.T, mutate func(map[string]any)) string {
	t.Helper()
	base := State{
		SchemaVersion: stateSchemaVersion,
		UpdatedAt:     time.Now().UTC(),
		Accounts:      map[string]*AccountState{},
		Events:        []Event{},
	}
	raw, errMarshal := json.Marshal(base)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var object map[string]any
	if errUnmarshal := json.Unmarshal(raw, &object); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	mutate(object)
	raw, errMarshal = json.Marshal(object)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	return path
}

func TestStateStoreRejectsRetiredResinShape(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "account resin field",
			mutate: func(state map[string]any) {
				state["accounts"] = map[string]any{
					"auth-1": map[string]any{"auth_id": "auth-1", "state": "unknown", "resin": map[string]any{}},
				}
			},
			want: "unknown field",
		},
		{
			name: "policy resin auto rotate field",
			mutate: func(state map[string]any) {
				state["policy"] = map[string]any{"resin_auto_rotate_after_quality_failure": true}
			},
			want: "unknown field",
		},
		{
			name: "quality unknown field",
			mutate: func(state map[string]any) {
				state["policy"] = map[string]any{"quality": map[string]any{"retired_quality_flag": true}}
			},
			want: "unknown field",
		},
		{
			name: "proxy initialized flag",
			mutate: func(state map[string]any) {
				state["proxy"] = map[string]any{"resin_sources_initialized": true}
			},
			want: "unknown field",
		},
		{
			name: "cleanup without snapshot",
			mutate: func(state map[string]any) {
				state["resin_lease_deletes"] = []any{
					map[string]any{"account": "lease-1", "admin_url": "http://resin.test", "platform_id": "platform-1"},
				}
			},
			want: "incomplete",
		},
		{
			name: "remote lease without snapshot",
			mutate: func(state map[string]any) {
				state["accounts"] = map[string]any{
					"auth-1": map[string]any{
						"auth_id": "auth-1",
						"state":   "unknown",
						"proxy_binding": map[string]any{
							"auth_id":     "auth-1",
							"source_id":   "resin-1",
							"source_kind": "resin",
							"lease":       map[string]any{"provider": "resin", "account": "lease-1"},
						},
					},
				}
			},
			want: "Resin lease without a snapshot",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeStateWithRetiredMutation(t, test.mutate)
			errLoad := NewStore(path).Load(config.PolicyFromConfig(config.DefaultConfig()), "revision")
			if errLoad == nil || !strings.Contains(errLoad.Error(), test.want) {
				t.Fatalf("load error = %v, want substring %q", errLoad, test.want)
			}
		})
	}
}
