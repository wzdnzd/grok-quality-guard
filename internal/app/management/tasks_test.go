package management

import (
	"testing"

	proxy "grok-quality-guard/internal/proxy"
)

func TestResidentQueueVisibility(t *testing.T) {
	if shouldShowAutoBindQueue(false, 0, 0, false) {
		t.Fatal("disabled idle auto-bind queue should be hidden")
	}
	if !shouldShowAutoBindQueue(false, 1, 0, false) {
		t.Fatal("auto-bind queue with pending work should be visible")
	}
	if !shouldShowAutoBindQueue(true, 0, 0, false) {
		t.Fatal("enabled auto-bind queue should be visible")
	}
	if shouldShowProxyHealthQueue(true, false) || !shouldShowProxyHealthQueue(true, true) {
		t.Fatal("proxy health queue visibility does not follow require_healthy")
	}
	if shouldShowProxyChangeQueue([]proxy.SourceChange{{Status: "succeeded"}}) {
		t.Fatal("completed proxy synchronization history should not remain resident")
	}
	if !shouldShowProxyChangeQueue([]proxy.SourceChange{{Status: "partial", Counts: proxy.ChangeCounts{Failed: 1}}}) {
		t.Fatal("failed proxy synchronization should remain actionable in the center")
	}
	if shouldShowResinCleanupQueue(0, 0) {
		t.Fatal("idle Resin cleanup queue should be hidden")
	}
	if !shouldShowResinCleanupQueue(1, 0) || !shouldShowResinCleanupQueue(0, 1) {
		t.Fatal("Resin cleanup queue should show pending jobs or active Resin bindings")
	}
}
