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

func TestRawBoolAcceptsCPAStringBooleans(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  bool
	}{
		{name: "bool true", value: true, want: true},
		{name: "bool false", value: false, want: false},
		{name: "string true", value: "true", want: true},
		{name: "string true uppercase", value: "TRUE", want: true},
		{name: "string one", value: "1", want: true},
		{name: "string false", value: "false", want: false},
		{name: "string zero", value: "0", want: false},
		{name: "string invalid", value: "not-a-bool", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, errMarshal := json.Marshal(test.value)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			raw := map[string]json.RawMessage{"disabled": encoded}
			if got := RawBool(raw, "disabled"); got != test.want {
				t.Fatalf("RawBool(%v) = %t, want %t", test.value, got, test.want)
			}
			if got := Disabled(raw); got != test.want {
				t.Fatalf("Disabled(%v) = %t, want %t", test.value, got, test.want)
			}
		})
	}
}
