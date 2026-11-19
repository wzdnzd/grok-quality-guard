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
