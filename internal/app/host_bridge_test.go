package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSharedHostCallCoalescesConcurrentCallers(t *testing.T) {
	previous := hostCall
	defer SetHostCall(previous)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	SetHostCall(func(string, any) (json.RawMessage, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return json.RawMessage(`{"ok":true}`), nil
	})
	plugin := NewPlugin()
	const callers = 8
	begin := make(chan struct{})
	results := make(chan error, callers)
	for index := 0; index < callers; index++ {
		go func() {
			<-begin
			_, errCall := plugin.sharedHostCall(context.Background(), "test.host.shared", map[string]any{})
			results <- errCall
		}()
	}
	close(begin)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("host callback did not start")
	}
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("host callback calls = %d, want 1", got)
	}
	close(release)
	for index := 0; index < callers; index++ {
		if errCall := <-results; errCall != nil {
			t.Fatal(errCall)
		}
	}
}

func TestSharedHostCallTimeoutAllowsRetryAfterCallbackCompletes(t *testing.T) {
	previous := hostCall
	defer SetHostCall(previous)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int32
	SetHostCall(func(string, any) (json.RawMessage, error) {
		call := calls.Add(1)
		if call == 1 {
			close(started)
			<-release
			close(finished)
		}
		return json.RawMessage(`{"call":` + []string{"1", "2"}[min(int(call), 2)-1] + `}`), nil
	})
	plugin := NewPlugin()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, errCall := plugin.sharedHostCall(ctx, "test.host.timeout", nil)
	if errCall == nil {
		t.Fatal("shared host call did not return its caller timeout")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("host callback did not start")
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("host callback did not finish after release")
	}
	method := "test.host.timeout"
	deadline := time.Now().Add(time.Second)
	for {
		plugin.hostCallsMu.Lock()
		_, pending := plugin.hostCalls[method]
		plugin.hostCallsMu.Unlock()
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed-out host flight was not released after callback completion")
		}
		time.Sleep(time.Millisecond)
	}
	raw, errRetry := plugin.sharedHostCall(context.Background(), method, nil)
	if errRetry != nil {
		t.Fatalf("retry host call: %v", errRetry)
	}
	if string(raw) != `{"call":2}` {
		t.Fatalf("retry response = %s, want second callback response", raw)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("host callback calls = %d, want 2", got)
	}
}

func TestSharedHostCallRecoversPanicsAndReleasesFlight(t *testing.T) {
	previous := hostCall
	defer SetHostCall(previous)
	var calls atomic.Int32
	SetHostCall(func(string, any) (json.RawMessage, error) {
		if calls.Add(1) == 1 {
			panic("test panic")
		}
		return json.RawMessage(`{"call":2}`), nil
	})
	plugin := NewPlugin()
	_, errCall := plugin.sharedHostCall(context.Background(), "test.host.panic", nil)
	if errCall == nil || !strings.Contains(errCall.Error(), "panicked") {
		t.Fatalf("panic host callback error = %v", errCall)
	}
	raw, errRetry := plugin.sharedHostCall(context.Background(), "test.host.panic", nil)
	if errRetry != nil {
		t.Fatalf("retry host call: %v", errRetry)
	}
	if string(raw) != `{"call":2}` {
		t.Fatalf("retry response = %s, want second callback response", raw)
	}
}

func TestSharedHostCallWaiterTimeoutKeepsFlightJoinable(t *testing.T) {
	previous := hostCall
	defer SetHostCall(previous)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	SetHostCall(func(string, any) (json.RawMessage, error) {
		calls.Add(1)
		close(started)
		<-release
		return json.RawMessage(`{"call":"first"}`), nil
	})
	plugin := NewPlugin()
	method := "test.host.waiter-timeout"
	firstResult := make(chan error, 1)
	go func() {
		_, errCall := plugin.sharedHostCall(context.Background(), method, nil)
		firstResult <- errCall
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("host callback did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, errCall := plugin.sharedHostCall(ctx, method, nil); errCall == nil {
		t.Fatal("second caller did not observe its own timeout")
	}
	plugin.hostCallsMu.Lock()
	_, pending := plugin.hostCalls[method]
	plugin.hostCallsMu.Unlock()
	if !pending {
		t.Fatal("waiter timeout removed a host flight that was still running")
	}
	close(release)
	select {
	case errCall := <-firstResult:
		if errCall != nil {
			t.Fatalf("first caller failed after callback completed: %v", errCall)
		}
	case <-time.After(time.Second):
		t.Fatal("first caller did not receive the callback result")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("host callback calls = %d, want 1", got)
	}
}

func TestSharedHostCallExpiresHungCallbackForFutureRetry(t *testing.T) {
	previous := hostCall
	defer SetHostCall(previous)
	started := make(chan struct{})
	releaseFirst := make(chan struct{})
	finishedFirst := make(chan struct{})
	var calls atomic.Int32
	SetHostCall(func(string, any) (json.RawMessage, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-releaseFirst
			close(finishedFirst)
			return json.RawMessage(`{"call":"first"}`), nil
		}
		return json.RawMessage(`{"call":"second"}`), nil
	})
	plugin := NewPlugin()
	method := "test.host.expired-flight"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, errCall := plugin.sharedHostCall(ctx, method, nil)
	cancel()
	if errCall == nil {
		t.Fatal("shared host call did not return its caller timeout")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("host callback did not start")
	}
	deadline := time.Now().Add(time.Second)
	for {
		plugin.hostCallsMu.Lock()
		_, pending := plugin.hostCalls[method]
		plugin.hostCallsMu.Unlock()
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hung host flight was not expired for future callers")
		}
		time.Sleep(time.Millisecond)
	}
	raw, errRetry := plugin.sharedHostCall(context.Background(), method, nil)
	if errRetry != nil {
		close(releaseFirst)
		t.Fatalf("retry host call: %v", errRetry)
	}
	if string(raw) != `{"call":"second"}` {
		close(releaseFirst)
		t.Fatalf("retry response = %s, want second callback response", raw)
	}
	close(releaseFirst)
	select {
	case <-finishedFirst:
	case <-time.After(time.Second):
		t.Fatal("first host callback did not finish")
	}
}
