package catalog

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestGrokModelPrefixesAreStrict(t *testing.T) {
	service := NewService(nil)
	if service.IsGrokModel("my-grok-proxy", []string{"grok-"}) {
		t.Fatal("unmatched model matched through a grok substring")
	}
	if service.IsGrokModel("grok-4.5", []string{"gpt-"}) {
		t.Fatal("configured prefix list was ignored")
	}
	if !service.IsGrokModel("grok-4.5", []string{"grok-"}) {
		t.Fatal("configured grok prefix did not match")
	}
}
func TestPassiveAliasUsesAuthModelCache(t *testing.T) {
	service := NewService(nil)
	prefixes := []string{"grok-"}
	if service.IsGrokModel("xai-grok-alias", prefixes) {
		t.Fatal("alias unexpectedly matched the configured prefix")
	}
	service.SetGrokAuthModels([]pluginapi.HostAuthModels{{Provider: "xai", Models: []string{"xai-grok-alias"}}})
	if !service.IsGrokModel("xai-grok-alias", prefixes) {
		t.Fatal("alias did not match the auth model cache")
	}
}
