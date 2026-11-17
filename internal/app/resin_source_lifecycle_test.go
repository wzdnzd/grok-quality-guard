package app

import (
	"encoding/json"
	"net/http"
	"testing"

	proxy "grok-quality-guard/internal/proxy"
)

func TestResinSourcesUseTemplateLifecycleAndSupportMultipleEntries(t *testing.T) {
	entries, auths := singleGrokAuth(t, "auth-1", "alice@example.com", nil)
	plugin := newTestPlugin(t, newMockHostCall(entries, auths), nil)

	create := func(name, format string, placeholders []proxy.Placeholder) map[string]any {
		t.Helper()
		response := actionManagementForTest(t, plugin, "proxy.sources.create", map[string]any{
			"kind": "resin",
			"source": map[string]any{
				"name": name, "enabled": true, "local_only": true,
				"account_format": format, "placeholders": placeholders,
			},
		})
		if response.StatusCode != http.StatusOK {
			t.Fatalf("create %s status=%d body=%s", name, response.StatusCode, response.Body)
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(response.Body, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}

	first := create("Resin A", "xai-{account}-{region}", []proxy.Placeholder{{
		Name: "region", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "us,jp"},
	}})
	second := create("Resin B", "svc-{account}-{suffix}", []proxy.Placeholder{{
		Name: "suffix", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 6},
	}})
	if first["id"] == "" || second["id"] == "" || first["id"] == second["id"] {
		t.Fatalf("resin sources did not receive stable unique IDs: first=%#v second=%#v", first, second)
	}
	if first["kind"] != "resin" || second["kind"] != "resin" || first["local_only"] != true {
		t.Fatalf("unexpected resin source views: first=%#v second=%#v", first, second)
	}

	list := actionManagementForTest(t, plugin, "proxy.sources.list", map[string]any{"kind": "resin"})
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", list.StatusCode, list.Body)
	}
	var listed struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body, &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Data.Total != 2 || len(listed.Data.Items) != 2 {
		t.Fatalf("expected two Resin sources, got total=%d items=%d", listed.Data.Total, len(listed.Data.Items))
	}

	update := actionManagementForTest(t, plugin, "proxy.sources.update", map[string]any{
		"kind": "resin",
		"source": map[string]any{
			"id": first["id"], "name": "Resin A", "enabled": true, "local_only": true,
			"account_format": "xai-{account}-{plan}-{suffix}",
			"placeholders": []proxy.Placeholder{
				{Name: "plan", Kind: proxy.PlaceholderEnum, Enum: &proxy.EnumSpec{Separator: ",", Input: "basic,pro"}},
				{Name: "suffix", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 4}},
			},
			"expected_version": first["config_version"],
		},
	})
	if update.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.StatusCode, update.Body)
	}

	remove := actionManagementForTest(t, plugin, "proxy.sources.delete", map[string]any{"source_id": second["id"], "force": true})
	if remove.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", remove.StatusCode, remove.Body)
	}
	state := plugin.stateStore().Snapshot()
	if len(state.Proxy.ResinSources) != 1 {
		t.Fatalf("resin source map was not cleaned up: %#v", state.Proxy.ResinSources)
	}
	resinTemplates := 0
	for _, template := range state.Proxy.Templates {
		if template.Kind == proxy.TemplateResin {
			resinTemplates++
			if template.ID != first["id"] {
				t.Fatalf("unexpected remaining resin template: %#v", template)
			}
			if template.UsernamePattern != "xai-{account}-{plan}-{suffix}" {
				t.Fatalf("updated account format was not stored in the template: %q", template.UsernamePattern)
			}
		}
	}
	if resinTemplates != 1 {
		t.Fatalf("resin template count=%d, want 1", resinTemplates)
	}
}
