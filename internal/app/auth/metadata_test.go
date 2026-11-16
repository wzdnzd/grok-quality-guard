package auth

import (
	"encoding/json"
	"testing"
)

func TestAuthIdentityHelpers(t *testing.T) {
	raw := map[string]json.RawMessage{}
	for key, value := range map[string]any{
		"type":          "xai",
		"email":         "alice@example.com",
		"access_token":  "access",
		"refresh_token": "refresh",
		"disabled":      true,
		"proxy_url":     "socks5h://Default.grok-alice-1abc:secret@resin:2260",
	} {
		encoded, _ := json.Marshal(value)
		raw[key] = encoded
	}
	if !IsGrokProvider(RawString(raw, "type")) {
		t.Fatal("xai auth not recognized")
	}
	if !Disabled(raw) || Email(raw) == "" {
		t.Fatalf("unexpected auth identity: disabled=%t email=%q", Disabled(raw), Email(raw))
	}
	if Label(raw) != "" {
		t.Fatalf("unexpected auth label: %q", Label(raw))
	}
}
