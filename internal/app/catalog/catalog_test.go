package catalog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestReadAuthMetaExtractsOAuthIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xai.json")
	raw := []byte(`{"type":"xai","sub":"oauth-sub-1","proxy_url":"http://proxy.example:8080"}`)
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	proxyURL, oauthID, loaded := readAuthMeta(path)
	if !loaded || oauthID != "oauth-sub-1" || proxyURL != "http://proxy.example:8080" {
		t.Fatalf("readAuthMeta = %q, %q, %v", proxyURL, oauthID, loaded)
	}
}

func TestReadAuthMetaFallsBackToNestedSubject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "xai.json")
	raw := []byte(`{"type":"xai","metadata":{"subject":"oauth-sub-2"}}`)
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	_, oauthID, loaded := readAuthMeta(path)
	if !loaded || oauthID != "oauth-sub-2" {
		t.Fatalf("readAuthMeta oauth ID = %q, loaded=%v", oauthID, loaded)
	}
}

func TestReadAuthMetaTreatsNullProxyURLAsEmpty(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantURL string
		wantID  string
		loaded  bool
	}{
		{name: "null", raw: `{"sub":"oauth-sub-1","proxy_url":null}`, wantID: "oauth-sub-1", loaded: true},
		{name: "empty", raw: `{"sub":"oauth-sub-1","proxy_url":""}`, wantID: "oauth-sub-1", loaded: true},
		{name: "blank", raw: `{"sub":"oauth-sub-1","proxy_url":"   "}`, wantID: "oauth-sub-1", loaded: true},
		{name: "omitted", raw: `{"sub":"oauth-sub-1"}`, wantID: "oauth-sub-1", loaded: true},
		{name: "number", raw: `{"sub":"oauth-sub-1","proxy_url":123}`},
		{name: "object", raw: `{"sub":"oauth-sub-1","proxy_url":{"url":"http://proxy.example"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth.json")
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			proxyURL, oauthID, loaded := readAuthMeta(path)
			if loaded != tc.loaded || proxyURL != tc.wantURL || oauthID != tc.wantID {
				t.Fatalf("readAuthMeta = %q, %q, %v", proxyURL, oauthID, loaded)
			}
		})
	}
}

func TestCatalogRefreshPublishesNullProxyURLAsKnownEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"type":"xai","sub":"oauth-sub-1","proxy_url":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai",
		Path: path, Size: info.Size(), ModTime: info.ModTime(),
	}})
	service := NewService(env)
	t.Cleanup(service.Close)
	if err := service.RefreshCatalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	service.WaitAuthMetaRefresh()
	account, ok := service.FindAccount("auth-1")
	if !ok || !account.ProxyURLKnown || !account.ProxyMetaInspected || account.ProxyMasked != "" || account.ProxyHash != "" || account.OAuthID != "oauth-sub-1" {
		t.Fatalf("null proxy_url was not published as a known empty proxy: %#v", account)
	}
	service.WaitAuthModelsRefresh()
}
