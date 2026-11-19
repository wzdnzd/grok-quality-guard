package catalog

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestEnsureCatalogRefreshesStaleSnapshotWithoutBlocking(t *testing.T) {
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
		authListStarted:   make(chan struct{}, 4),
		authListRelease:   make(chan struct{}),
		authListBlockFrom: 2,
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai",
	}})
	service := NewService(env)
	t.Cleanup(service.Close)
	if errEnsure := service.Ensure(context.Background()); errEnsure != nil {
		t.Fatal(errEnsure)
	}
	service.WaitAuthModelsRefresh()
	service.catalogMu.Lock()
	service.catalogAt = time.Now().UTC().Add(-time.Minute)
	service.catalogMu.Unlock()

	env.setAuths([]pluginapi.HostAuthFileEntry{{
		ID: "auth-2", AuthIndex: "auth-2", Type: "xai", Provider: "xai",
	}})
	released := false
	defer func() {
		if !released {
			close(env.authListRelease)
		}
	}()
	done := make(chan error, 1)
	go func() { done <- service.Ensure(context.Background()) }()
	select {
	case errEnsure := <-done:
		if errEnsure != nil {
			t.Fatal(errEnsure)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Ensure blocked on a stale snapshot refresh")
	}
	select {
	case <-env.authListStarted:
	case <-time.After(time.Second):
		t.Fatal("Ensure did not start a background catalog refresh")
	}
	close(env.authListRelease)
	released = true
	deadline := time.Now().Add(time.Second)
	for {
		if _, ok := service.FindAccount("auth-2"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background catalog refresh did not publish the new account")
		}
		time.Sleep(time.Millisecond)
	}
	service.WaitAuthModelsRefresh()
}

type coldCatalogEnvironment struct {
	*blockingAuthEnvironment
	auths       []pluginapi.HostAuthFileEntry
	started     chan struct{}
	release     chan struct{}
	hasDeadline chan bool
}

func (e *coldCatalogEnvironment) ListAuths(ctx context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	_, hasDeadline := ctx.Deadline()
	select {
	case e.hasDeadline <- hasDeadline:
	default:
	}
	close(e.started)
	select {
	case <-e.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return append([]pluginapi.HostAuthFileEntry(nil), e.auths...), nil
}

func TestEnsureCatalogColdStartWaitsForHostCatalog(t *testing.T) {
	env := &coldCatalogEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
		auths: []pluginapi.HostAuthFileEntry{{
			ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai",
		}},
		started:     make(chan struct{}),
		release:     make(chan struct{}),
		hasDeadline: make(chan bool, 1),
	}
	service := NewService(env)
	t.Cleanup(service.Close)

	done := make(chan error, 1)
	go func() { done <- service.Ensure(context.Background()) }()
	select {
	case <-env.started:
	case <-time.After(time.Second):
		t.Fatal("cold Ensure did not start host auth loading")
	}
	select {
	case hasDeadline := <-env.hasDeadline:
		if hasDeadline {
			t.Fatal("cold Ensure imposed a synthetic deadline on the full catalog refresh")
		}
	case <-time.After(time.Second):
		t.Fatal("host auth loading did not report its context")
	}
	select {
	case errEnsure := <-done:
		t.Fatalf("cold Ensure returned before the host catalog was released: %v", errEnsure)
	case <-time.After(20 * time.Millisecond):
	}
	close(env.release)
	select {
	case errEnsure := <-done:
		if errEnsure != nil {
			t.Fatal(errEnsure)
		}
	case <-time.After(time.Second):
		t.Fatal("cold Ensure did not finish after the host catalog was released")
	}
	account, ok := service.FindAccount("auth-1")
	if !ok || account.ID != "auth-1" {
		t.Fatalf("cold Ensure did not publish the loaded account: %#v", account)
	}
	service.WaitAuthModelsRefresh()
}
