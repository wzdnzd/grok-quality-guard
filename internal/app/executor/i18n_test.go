package executor

import (
	"net/http"
	"testing"
)

func TestExecutorRequestLocaleCombinesMultipleAcceptLanguageHeaders(t *testing.T) {
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
			if got := executorRequestLocale(test.headers); got != test.want {
				t.Fatalf("executorRequestLocale() = %q, want %q", got, test.want)
			}
		})
	}
}
