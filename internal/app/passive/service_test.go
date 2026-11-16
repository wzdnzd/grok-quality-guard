package passive

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestUsageReasoningRequiredUsesModelEffortAndBody(t *testing.T) {
	cases := []struct {
		name   string
		record pluginapi.UsageRecord
		body   []byte
		want   bool
	}{
		{name: "known reasoning model", record: pluginapi.UsageRecord{Model: "grok-4.7"}, want: true},
		{name: "response model wins over alias", record: pluginapi.UsageRecord{Model: "vendor-alias", ResponseModel: "grok-4.7"}, want: true},
		{name: "unknown alias stays outside capability gate", record: pluginapi.UsageRecord{Model: "vendor-alias", ReasoningEffort: "xhigh"}, want: false},
		{name: "explicit none disables", record: pluginapi.UsageRecord{Model: "grok-4.7", ReasoningEffort: "none"}, want: false},
		{name: "non reasoning family", record: pluginapi.UsageRecord{Model: "grok-composer-2.5-fast"}, want: false},
		{name: "body none disables", record: pluginapi.UsageRecord{Model: "grok-4.7"}, body: []byte(`{"reasoning":{"effort":"none"}}`), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UsageReasoningRequired(tc.record, tc.body); got != tc.want {
				t.Fatalf("UsageReasoningRequired() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReviewCompactPendingBodyDropsPrompt(t *testing.T) {
	body := []byte(`{"model":"grok-4.5","messages":[{"role":"user","content":"` + strings.Repeat("prompt ", 1000) + `"}],"reasoning":{"effort":"high"},"thinking":{"type":"enabled"}}`)
	compact := CompactReasoningBody(body)
	if len(compact) == 0 {
		t.Fatal("compact body was empty")
	}
	if strings.Contains(string(compact), "messages") || strings.Contains(string(compact), "prompt") {
		t.Fatalf("compact body retained the prompt: %s", compact)
	}
	if !strings.Contains(string(compact), "reasoning") || !strings.Contains(string(compact), "thinking") {
		t.Fatalf("compact body dropped reasoning fields: %s", compact)
	}
}
