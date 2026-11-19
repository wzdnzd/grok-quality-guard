package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"gopkg.in/yaml.v3"

	appconfig "grok-quality-guard/internal/config"
)

func TestCatalogDependentManagementActionsSurfaceLoadFailure(t *testing.T) {
	host := newMockHostCall(nil, nil)
	previousHostCall := hostCall
	hostCall = func(method string, payload any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostAuthList {
			return nil, errors.New("catalog backend unavailable")
		}
		return host.call(method, payload)
	}
	t.Cleanup(func() { hostCall = previousHostCall })

	cfg := appconfig.DefaultConfig()
	cfg.Enabled = false
	cfg.StateFile = filepath.Join(t.TempDir(), "state", "state.json")
	rawConfig, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	plugin := NewPlugin()
	if errConfigure := plugin.Configure(rawConfig); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	t.Cleanup(plugin.Shutdown)

	for _, action := range []string{"overview", "accounts.list"} {
		response := actionManagementForTest(t, plugin, action, map[string]any{
			"page": 1, "page_size": 50, "summary": true,
		})
		if response.StatusCode != http.StatusBadGateway {
			t.Fatalf("%s status = %d, want %d; body=%s", action, response.StatusCode, http.StatusBadGateway, response.Body)
		}
		if !strings.Contains(string(response.Body), `"code":"upstream_error"`) {
			t.Fatalf("%s did not expose upstream_error: %s", action, response.Body)
		}
	}
}
