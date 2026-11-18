package main

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"grok-quality-guard/internal/app/i18n"
)

func TestRequestLocaleFromPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "traditional chinese", payload: `{"headers":{"Accept-Language":["zh-TW,zh;q=0.9,en;q=0.8"]}}`, want: "zh-TW"},
		{name: "english", payload: `{"headers":{"accept-language":["en-US,en;q=0.9"]}}`, want: "en"},
		{name: "multiple header values honor quality", payload: `{"headers":{"Accept-Language":["en-US;q=0.5","zh-TW;q=0.9"]}}`, want: "zh-TW"},
		{name: "unsupported falls back", payload: `{"headers":{"Accept-Language":["fr-FR"]}}`, want: i18n.DefaultLocale},
		{name: "missing headers", payload: `{}`, want: i18n.DefaultLocale},
		{name: "invalid payload", payload: `{`, want: i18n.DefaultLocale},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := requestLocaleFromPayload([]byte(test.payload)); got != test.want {
				t.Fatalf("requestLocaleFromPayload() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestManagementRegistrationUsesPayloadLocale(t *testing.T) {
	raw, errHandle := handleMethod(pluginabi.MethodManagementRegister, []byte(`{"headers":{"accept-language":["en-US,en;q=0.9"]}}`))
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			Resources []struct {
				Description string `json:"description"`
			} `json:"resources"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || len(envelope.Result.Resources) != 1 {
		t.Fatalf("unexpected registration response: %s", raw)
	}
	want := globalApp.ManagementRegistrationForLocale("en").Resources[0].Description
	if got := envelope.Result.Resources[0].Description; got != want {
		t.Fatalf("description = %q, want %q", got, want)
	}
	if regexp.MustCompile(`\p{Han}`).MatchString(envelope.Result.Resources[0].Description) {
		t.Fatalf("English registration description contains Han characters: %q", envelope.Result.Resources[0].Description)
	}
}
