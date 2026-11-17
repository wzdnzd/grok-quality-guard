package proxy

import (
	"testing"
	"time"

	statepkg "grok-quality-guard/internal/app/state"
	coreproxy "grok-quality-guard/internal/proxy"
)

func TestResinLeaseDeleteUsesImmutableSnapshot(t *testing.T) {
	snapshot := coreproxy.ResinSnapshot{
		AdminURL:   "https://resin.example/",
		PlatformID: "platform-1",
		AdminToken: "admin-token",
	}
	job := newResinLeaseDelete("auth-1", snapshot, "account-1")
	if job.AuthID != "auth-1" || job.AdminURL != snapshot.AdminURL || job.PlatformID != snapshot.PlatformID || job.Account != "account-1" {
		t.Fatalf("lease job did not preserve its snapshot target: %#v", job)
	}
	if job.SnapshotID != statepkg.ResinSnapshotID(snapshot) || job.SnapshotID == "" {
		t.Fatalf("lease job snapshot id = %q, want %q", job.SnapshotID, statepkg.ResinSnapshotID(snapshot))
	}
	if job.Status != "pending" || job.NextAttemptAt.IsZero() {
		t.Fatalf("lease job was not scheduled: %#v", job)
	}
}

func TestResinLeaseDeleteBackoff(t *testing.T) {
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 0, want: 30 * time.Second},
		{attempts: 1, want: 30 * time.Second},
		{attempts: 2, want: time.Minute},
		{attempts: 3, want: 2 * time.Minute},
		{attempts: 8, want: time.Hour},
		{attempts: 100, want: time.Hour},
	}
	for _, tc := range cases {
		if got := resinLeaseDeleteBackoff(tc.attempts); got != tc.want {
			t.Fatalf("backoff(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}
