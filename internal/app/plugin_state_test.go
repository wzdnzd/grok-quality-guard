package app

import (
	"fmt"
	"testing"
)

func TestAccountLocksUseBoundedShards(t *testing.T) {
	plugin := NewPlugin()
	if len(plugin.accountLocks) != 256 {
		t.Fatalf("account lock shards = %d, want 256", len(plugin.accountLocks))
	}
	first := plugin.accountLock("auth-1")
	for index := 0; index < 10000; index++ {
		_ = plugin.accountLock(fmt.Sprintf("auth-%d", index))
	}
	if plugin.accountLock("auth-1") != first {
		t.Fatal("same auth ID resolved to a different lock shard")
	}
}
