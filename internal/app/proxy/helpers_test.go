package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestResinAccountBaseAndMasking(t *testing.T) {
	raw := map[string]json.RawMessage{}
	for key, value := range map[string]any{
		"type":          "xai",
		"email":         "ab@example.com",
		"access_token":  "access",
		"refresh_token": "refresh",
		"disabled":      true,
		"proxy_url":     "socks5h://Default.grok-alice-1abc:secret@resin:2260",
	} {
		encoded, _ := json.Marshal(value)
		raw[key] = encoded
	}
	if base := resinAccountBase(raw); base != "ab" {
		t.Fatalf("resin base = %q, want ab", base)
	}
	if mask := maskEmail("ab@example.com"); mask != "a***@example.com" {
		t.Fatalf("masked email = %q", mask)
	}
	if !strings.Contains(maskProxyURL("socks5h://user:pass@example.com:1080/path"), "socks5h://example.com:1080") {
		t.Fatal("proxy mask did not preserve scheme and host")
	}
}

func TestResinAccountBaseFallbacks(t *testing.T) {
	raw := map[string]json.RawMessage{}
	encoded, _ := json.Marshal("label with Spaces!!")
	raw["label"] = encoded
	if got := resinAccountBase(raw); got != "label_with_spaces" {
		t.Fatalf("resin base = %q", got)
	}
	raw = map[string]json.RawMessage{}
	encoded, _ = json.Marshal("subject-only")
	raw["sub"] = encoded
	if got := resinAccountBase(raw); !strings.HasPrefix(got, "auth_") {
		t.Fatalf("fallback base = %q", got)
	}
}
