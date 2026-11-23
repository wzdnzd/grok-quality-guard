package routing

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSelectionIndexOrdersHealthyThenUnknownAndPriorityDescending(t *testing.T) {
	accounts := []Account{
		{ID: "healthy-low", Provider: "xai", Priority: 1},
		{ID: "healthy-high", Provider: "xai", Priority: 9},
		{ID: "unknown-high", Provider: "xai", Priority: 100},
		{ID: "suspect", Provider: "xai", Priority: 1000},
	}
	states := map[string]string{
		"healthy-low":  StateHealthy,
		"healthy-high": StateHealthy,
		"unknown-high": StateUnknown,
		"suspect":      StateSuspect,
	}
	var index SelectionIndex
	index.Build(accounts, states)

	order := index.Order()
	want := []string{"0:9", "0:1", "1:100"}
	if len(order) != len(want) {
		t.Fatalf("layer order = %#v, want %#v", order, want)
	}
	for position := range want {
		if order[position] != want[position] {
			t.Fatalf("layer order = %#v, want %#v", order, want)
		}
	}
	if index.Total() != 3 {
		t.Fatalf("candidate total = %d, want 3", index.Total())
	}
	if layer, ok := index.LayerForAuth("unknown-high"); !ok || layer != "1:100" {
		t.Fatalf("unknown account layer = %q, %v", layer, ok)
	}
}
func TestBindingHeapPrunesExpiredBindings(t *testing.T) {
	runtime := NewSelectionRuntime()
	now := time.Now().UTC()
	for index := 0; index < 2000; index++ {
		runtime.GetOrBind(fmt.Sprintf("session-%d", index), "auth-1", now, time.Second, 10000)
	}
	runtime.PruneBindings(now.Add(2 * time.Second))
	remaining := runtime.BindingCount()
	if remaining != 0 {
		t.Fatalf("expired bindings remaining = %d, want 0", remaining)
	}
}
func TestSelectionRuntimeBindingTTLAndMaxEntries(t *testing.T) {
	runtime := NewSelectionRuntime()
	now := time.Now().UTC()
	if bound := runtime.GetOrBind("session-a", "auth-a", now, time.Minute, 2); bound != "auth-a" {
		t.Fatalf("initial binding = %q", bound)
	}
	if bound := runtime.GetOrBind("session-b", "auth-b", now.Add(time.Second), time.Minute, 2); bound != "auth-b" {
		t.Fatalf("second binding = %q", bound)
	}
	if bound := runtime.GetOrBind("session-c", "auth-c", now.Add(2*time.Second), time.Minute, 2); bound != "auth-c" {
		t.Fatalf("third binding = %q", bound)
	}
	if _, ok := runtime.LookupBinding("session-a", now.Add(3*time.Second)); ok {
		t.Fatal("oldest binding was not evicted at max entries")
	}
	if _, ok := runtime.LookupBinding("session-c", now.Add(2*time.Minute)); ok {
		t.Fatal("expired binding was not removed")
	}
}

func TestSelectionRuntimeRoundRobinCursorAdvancesExactlyOnce(t *testing.T) {
	runtime := NewSelectionRuntime()
	const goroutines = 200
	counts := make([]int, 10)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for index := 0; index < goroutines; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := runtime.StrategyStart("layer", "round_robin", len(counts))
			mu.Lock()
			counts[start]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	for index, count := range counts {
		if count != goroutines/len(counts) {
			t.Fatalf("round-robin bucket %d = %d, want %d: %#v", index, count, goroutines/len(counts), counts)
		}
	}
}
func TestSelectionRuntimeShrinksBindingsWhenLimitChanges(t *testing.T) {
	runtime := NewSelectionRuntime()
	now := time.Now().UTC()
	for index := 0; index < 10; index++ {
		sessionKey := fmt.Sprintf("session-%d", index)
		authID := fmt.Sprintf("auth-%d", index)
		runtime.GetOrBind(sessionKey, authID, now.Add(time.Duration(index)*time.Second), time.Hour, 10)
	}
	runtime.TrimBindings(3, now.Add(time.Minute))
	if count := runtime.BindingCount(); count != 3 {
		t.Fatalf("binding count after shrinking limit = %d, want 3", count)
	}
	if bound := runtime.GetOrBind("session-new", "auth-new", now.Add(2*time.Minute), time.Hour, 2); bound != "auth-new" {
		t.Fatalf("new binding = %q", bound)
	}
	if count := runtime.BindingCount(); count != 2 {
		t.Fatalf("binding count after adding at the new limit = %d, want 2", count)
	}
	runtime.RefreshBinding("session-new", "auth-new", now.Add(3*time.Minute), time.Hour, 1)
	if count := runtime.BindingCount(); count != 1 {
		t.Fatalf("binding count after refresh with lower limit = %d, want 1", count)
	}

	runtime = NewSelectionRuntime()
	runtime.GetOrBind("session-a", "auth-a", now, time.Hour, 2)
	runtime.GetOrBind("session-b", "auth-b", now.Add(time.Second), time.Hour, 2)
	runtime.RefreshBinding("session-a", "auth-a", now.Add(2*time.Second), time.Hour, 2)
	if bound, ok := runtime.LookupBinding("session-b", now.Add(3*time.Second)); !ok || bound != "auth-b" {
		t.Fatalf("refreshing one binding evicted another: %q, %v", bound, ok)
	}
}
