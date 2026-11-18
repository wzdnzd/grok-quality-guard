package management

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestManagementResponseUsesAcceptLanguage(t *testing.T) {
	tests := map[string]string{
		"zh-CN": "未知的操作",
		"zh-TW": "未知的操作",
		"en":    "Unknown action",
	}
	for locale, want := range tests {
		rpcRequest := rpcManagementRequest{
			ManagementRequest: pluginapi.ManagementRequest{
				Method:  http.MethodPost,
				Path:    "/v0/management/plugins/grok-quality-guard/api",
				Headers: http.Header{"Accept-Language": []string{locale}},
			},
		}
		rpcRequest.Body = []byte(`{"action":"unknown.action","params":{}}`)
		request, errMarshal := json.Marshal(rpcRequest)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		raw, errHandle := (&Service{}).HandleManagement(request)
		if errHandle != nil {
			t.Fatal(errHandle)
		}
		var outer struct {
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &outer); err != nil {
			t.Fatal(err)
		}
		var response managementResponse
		if err := json.Unmarshal(outer.Result, &response); err != nil {
			t.Fatal(err)
		}
		if got := response.Headers.Get("Content-Language"); got != locale {
			t.Fatalf("Content-Language = %q, want %q", got, locale)
		}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Error.Message != want {
			t.Fatalf("%s message = %q, want %q", locale, body.Error.Message, want)
		}
	}
}

func TestDashboardContentLanguageFollowsAcceptLanguage(t *testing.T) {
	request, _ := json.Marshal(rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method:  http.MethodGet,
			Path:    "/v0/management/plugins/grok-quality-guard/dashboard",
			Headers: http.Header{"Accept-Language": []string{"zh-TW,zh;q=0.9,en;q=0.8"}},
		},
	})
	raw, errHandle := (&Service{}).HandleManagement(request)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var outer struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	if err := json.Unmarshal(outer.Result, &response); err != nil {
		t.Fatal(err)
	}
	if got := response.Headers.Get("Content-Language"); got != "zh-TW" {
		t.Fatalf("Content-Language = %q, want zh-TW", got)
	}
}

func TestRequestLocaleCombinesMultipleAcceptLanguageHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers http.Header
		want    string
	}{
		{
			name:    "canonical header casing",
			headers: http.Header{"Accept-Language": []string{"en-US;q=0.5", "zh-TW;q=0.9"}},
			want:    "zh-TW",
		},
		{
			name:    "lowercase header casing",
			headers: http.Header{"accept-language": []string{"en-US;q=0.5", "zh-TW;q=0.9"}},
			want:    "zh-TW",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := requestLocale(test.headers); got != test.want {
				t.Fatalf("requestLocale() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestManagementResponseAcceptsLowercaseAcceptLanguage(t *testing.T) {
	rpcRequest := rpcManagementRequest{
		ManagementRequest: pluginapi.ManagementRequest{
			Method:  http.MethodPost,
			Path:    "/v0/management/plugins/grok-quality-guard/api",
			Headers: http.Header{"accept-language": []string{"en-US;q=0.5", "zh-TW;q=0.9"}},
		},
	}
	rpcRequest.Body = []byte(`{"action":"unknown.action","params":{}}`)
	request, errMarshal := json.Marshal(rpcRequest)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	raw, errHandle := (&Service{}).HandleManagement(request)
	if errHandle != nil {
		t.Fatal(errHandle)
	}
	var outer struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatal(err)
	}
	var response managementResponse
	if err := json.Unmarshal(outer.Result, &response); err != nil {
		t.Fatal(err)
	}
	if got := response.Headers.Get("Content-Language"); got != "zh-TW" {
		t.Fatalf("Content-Language = %q, want zh-TW", got)
	}
}
