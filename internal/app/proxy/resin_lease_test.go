package proxy

import (
	"strings"
	"testing"
)

func TestResinLeaseDeleteSharesAdminToken(t *testing.T) {
	settings := ResinSettings{
		Configured: true,
		Enabled:    true,
		PlatformID: "platform-1",
		AdminURL:   "https://resin.example",
	}
	settings.AdminToken = "shared" + "-" + "token"
	state := State{}
	queueResinLeaseDelete(&state, "auth-1", settings, "account-1")
	queueResinLeaseDelete(&state, "auth-2", settings, "account-2")
	if len(state.ResinAdminTokens) != 1 {
		t.Fatalf("admin token copies = %d, want 1: %#v", len(state.ResinAdminTokens), state.ResinAdminTokens)
	}
	if len(state.ResinLeaseDeletes) != 2 {
		t.Fatalf("lease delete jobs = %d, want 2", len(state.ResinLeaseDeletes))
	}
	ref := ""
	for _, job := range state.ResinLeaseDeletes {
		if strings.TrimSpace(job.AdminToken) != "" || job.AdminTokenRef == "" {
			t.Fatalf("job retained a token copy: %#v", job)
		}
		if ref == "" {
			ref = job.AdminTokenRef
		} else if ref != job.AdminTokenRef {
			t.Fatalf("jobs use different token refs: %s != %s", ref, job.AdminTokenRef)
		}
	}
}
