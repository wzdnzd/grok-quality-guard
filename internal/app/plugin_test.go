// Plugin tests cover account-lock sharding and stale model stream cleanup.
package app

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

// Account lock sharding.

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

// Model stream lifecycle.

func TestSweepStaleModelStreamsClosesAndRemovesExpiredEntries(t *testing.T) {
	previous := hostCall
	defer SetHostCall(previous)
	var closes atomic.Int32
	SetHostCall(func(method string, _ any) (json.RawMessage, error) {
		if method == pluginabi.MethodHostModelStreamClose {
			closes.Add(1)
		}
		return nil, nil
	})
	plugin := NewPlugin()
	now := time.Now()
	plugin.trackModelStream("expired")
	plugin.trackModelStream("live")
	plugin.modelStreamsMu.Lock()
	plugin.modelStreams["expired"] = now.Add(-modelStreamMaxAge - time.Second)
	plugin.modelStreams["live"] = now
	plugin.modelStreamsMu.Unlock()

	plugin.sweepStaleModelStreams(now)

	if got := closes.Load(); got != 1 {
		t.Fatalf("stale stream close calls = %d, want 1", got)
	}
	plugin.modelStreamsMu.Lock()
	_, expiredExists := plugin.modelStreams["expired"]
	_, liveExists := plugin.modelStreams["live"]
	plugin.modelStreamsMu.Unlock()
	if expiredExists || !liveExists {
		t.Fatalf("stream map after sweep: expired=%t live=%t", expiredExists, liveExists)
	}
}
