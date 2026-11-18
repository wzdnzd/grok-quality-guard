package catalog

import (
	"os"
	"path/filepath"
	"testing"
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
