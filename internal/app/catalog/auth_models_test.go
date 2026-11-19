package catalog

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type authModelsRefreshEnvironment struct {
	*blockingAuthEnvironment
	models            []pluginapi.HostAuthModels
	auths             []pluginapi.HostAuthFileEntry
	authsMu           sync.Mutex
	started           chan struct{}
	release           chan struct{}
	calls             atomic.Int32
	authListCalls     atomic.Int32
	authListStarted   chan struct{}
	authListRelease   chan struct{}
	authListBlockFrom int32
}

type catalogChangingModelsEnvironment struct {
	*blockingAuthEnvironment
	authsMu       sync.Mutex
	auths         []pluginapi.HostAuthFileEntry
	models        []pluginapi.HostAuthModels
	calls         atomic.Int32
	firstStarted  chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
}

func (e *catalogChangingModelsEnvironment) ListAuths(context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	e.authsMu.Lock()
	defer e.authsMu.Unlock()
	return append([]pluginapi.HostAuthFileEntry(nil), e.auths...), nil
}

func (e *catalogChangingModelsEnvironment) setAuths(auths []pluginapi.HostAuthFileEntry) {
	e.authsMu.Lock()
	e.auths = append([]pluginapi.HostAuthFileEntry(nil), auths...)
	e.authsMu.Unlock()
}

func (e *catalogChangingModelsEnvironment) ListAuthModels(context.Context) ([]pluginapi.HostAuthModels, error) {
	switch e.calls.Add(1) {
	case 1:
		close(e.firstStarted)
		<-e.releaseFirst
	case 2:
		close(e.secondStarted)
	}
	return append([]pluginapi.HostAuthModels(nil), e.models...), nil
}

func (e *authModelsRefreshEnvironment) ListAuths(ctx context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	call := e.authListCalls.Add(1)
	if e.authListStarted != nil && (e.authListBlockFrom == 0 || call >= e.authListBlockFrom) {
		select {
		case e.authListStarted <- struct{}{}:
		default:
		}
	}
	if e.authListRelease != nil && call >= e.authListBlockFrom {
		select {
		case <-e.authListRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	e.authsMu.Lock()
	defer e.authsMu.Unlock()
	return append([]pluginapi.HostAuthFileEntry(nil), e.auths...), nil
}

func (e *authModelsRefreshEnvironment) setAuths(auths []pluginapi.HostAuthFileEntry) {
	e.authsMu.Lock()
	e.auths = append([]pluginapi.HostAuthFileEntry(nil), auths...)
	e.authsMu.Unlock()
}

func (e *authModelsRefreshEnvironment) ListAuthModels(context.Context) ([]pluginapi.HostAuthModels, error) {
	e.calls.Add(1)
	if e.started != nil {
		select {
		case e.started <- struct{}{}:
		default:
		}
	}
	if e.release != nil {
		<-e.release
	}
	return append([]pluginapi.HostAuthModels(nil), e.models...), nil
}

func TestCatalogModelRefreshIsAsyncAndAliasLookupWaitsForFirstResult(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
		models:  []pluginapi.HostAuthModels{{Provider: "xai", Models: []string{"xai-grok-alias"}}},
		started: started,
		release: release,
	}
	service := NewService(env)
	done := make(chan error, 1)
	go func() { done <- service.RefreshCatalog(context.Background()) }()
	select {
	case errRefresh := <-done:
		if errRefresh != nil {
			close(release)
			t.Fatal(errRefresh)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("catalog refresh waited for host.auth.models")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("async host.auth.models refresh did not start")
	}
	close(release)
	if !service.IsGrokModel("xai-grok-alias", []string{"grok-"}) {
		t.Fatal("alias lookup did not use the first async auth model result")
	}
	if got := env.calls.Load(); got != 1 {
		t.Fatalf("host.auth.models calls = %d, want 1", got)
	}
}

func TestWaitAuthModelsRefreshWaitsForInFlightRefresh(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
		models:  []pluginapi.HostAuthModels{{Provider: "xai", Models: []string{"xai-grok-alias"}}},
		started: started,
		release: release,
	}
	service := NewService(env)
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("async host.auth.models refresh did not start")
	}
	done := make(chan struct{})
	go func() {
		service.WaitAuthModelsRefresh()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("WaitAuthModelsRefresh returned before the in-flight refresh completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	released = true
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitAuthModelsRefresh did not return after the refresh completed")
	}
}

func TestCatalogModelRefreshRetriesWhenCatalogChangesDuringBackoff(t *testing.T) {
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
		models: []pluginapi.HostAuthModels{{Provider: "xai", Models: []string{"xai-grok-alias"}}},
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai"}})
	service := NewService(env)
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	service.WaitAuthModelsRefresh()
	if got := env.calls.Load(); got != 1 {
		t.Fatalf("initial host.auth.models calls = %d, want 1", got)
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{ID: "auth-2", AuthIndex: "auth-2", Type: "xai", Provider: "xai"}})
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	service.WaitAuthModelsRefresh()
	if got := env.calls.Load(); got != 2 {
		t.Fatalf("host.auth.models calls after catalog change = %d, want 2", got)
	}
}

func TestCatalogModelRefreshRetriesStaleGeneration(t *testing.T) {
	env := &catalogChangingModelsEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
		models:        []pluginapi.HostAuthModels{{Provider: "xai", Models: []string{"xai-grok-alias"}}},
		firstStarted:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	env.setAuths([]pluginapi.HostAuthFileEntry{{ID: "auth-1", AuthIndex: "auth-1", Type: "xai", Provider: "xai"}})
	service := NewService(env)
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	select {
	case <-env.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first host.auth.models refresh did not start")
	}

	env.setAuths([]pluginapi.HostAuthFileEntry{{ID: "auth-2", AuthIndex: "auth-2", Type: "xai", Provider: "xai"}})
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	close(env.releaseFirst)
	select {
	case <-env.secondStarted:
	case <-time.After(time.Second):
		t.Fatal("stale model result did not trigger a follow-up refresh")
	}
	service.WaitAuthModelsRefresh()
	if got := env.calls.Load(); got != 2 {
		t.Fatalf("host.auth.models calls after stale generation = %d, want 2", got)
	}
	if !service.IsGrokModel("xai-grok-alias", []string{"grok-"}) {
		t.Fatal("alias lookup did not use the refreshed model result")
	}
}

func TestWaitAuthModelsRefreshContextHonorsDeadline(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
		models:  []pluginapi.HostAuthModels{{Provider: "xai", Models: []string{"xai-grok-alias"}}},
		started: started,
		release: release,
	}
	service := NewService(env)
	if errRefresh := service.RefreshCatalog(context.Background()); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("host.auth.models refresh did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if service.WaitAuthModelsRefreshContext(ctx) {
		t.Fatal("wait context returned true before the in-flight refresh completed")
	}
	close(release)
	service.WaitAuthModelsRefresh()
}

func TestWaitAuthModelsRefreshStartsQueuedPass(t *testing.T) {
	env := &authModelsRefreshEnvironment{
		blockingAuthEnvironment: &blockingAuthEnvironment{
			getStarted: make(chan struct{}, 1),
			getRelease: make(chan struct{}),
		},
		models: []pluginapi.HostAuthModels{{Provider: "xai", Models: []string{"xai-grok-alias"}}},
	}
	service := NewService(env)
	service.authModelsMu.Lock()
	service.authModelsRefreshPending = true
	service.authModelsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !service.WaitAuthModelsRefreshContext(ctx) {
		t.Fatal("queued host.auth.models pass was not started")
	}
	if got := env.calls.Load(); got != 1 {
		t.Fatalf("queued host.auth.models calls = %d, want 1", got)
	}
	if !service.IsGrokModel("xai-grok-alias", []string{"grok-"}) {
		t.Fatal("queued host.auth.models result was not applied")
	}
}
