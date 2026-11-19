package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	appstate "grok-quality-guard/internal/app/state"
)

type blockingAuthEnvironment struct {
	getStarted chan struct{}
	getRelease chan struct{}
}

func (e *blockingAuthEnvironment) Store() *appstate.Store { return appstate.NewStore("") }
func (e *blockingAuthEnvironment) ListAuths(context.Context) ([]pluginapi.HostAuthFileEntry, error) {
	return nil, nil
}
func (e *blockingAuthEnvironment) ListAuthModels(context.Context) ([]pluginapi.HostAuthModels, error) {
	return nil, nil
}
func (e *blockingAuthEnvironment) GetAuth(index string) (pluginapi.HostAuthGetResponse, error) {
	return e.GetAuthContext(context.Background(), index)
}

func (e *blockingAuthEnvironment) GetAuthContext(ctx context.Context, index string) (pluginapi.HostAuthGetResponse, error) {
	select {
	case e.getStarted <- struct{}{}:
	default:
	}
	select {
	case <-e.getRelease:
	case <-ctx.Done():
		return pluginapi.HostAuthGetResponse{}, ctx.Err()
	}
	return pluginapi.HostAuthGetResponse{
		AuthIndex: index,
		Name:      index,
		JSON:      json.RawMessage(`{"type":"xai","access_token":"token"}`),
	}, nil
}
func (e *blockingAuthEnvironment) SelectionReady() bool            { return true }
func (e *blockingAuthEnvironment) RebuildSelectionIndex() error    { return nil }
func (e *blockingAuthEnvironment) WakeProxyAutoBind()              {}
func (e *blockingAuthEnvironment) WakeResinLeaseDeleteLoop()       {}
func (e *blockingAuthEnvironment) PruneProxyObservations([]string) {}

func TestResolveAuthHonorsContextWhenGetAuthBlocks(t *testing.T) {
	env := &blockingAuthEnvironment{
		getStarted: make(chan struct{}, 1),
		getRelease: make(chan struct{}),
	}
	service := NewService(env)
	service.catalog = []Account{{ID: "auth-1", AuthIndex: "auth-1"}}
	service.catalogByID = map[string]int{"auth-1": 0}
	service.catalogByAuthIndex = map[string]int{"auth-1": 0}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, _, errResolve := service.resolveAuth(ctx, "auth-1")
	if !errors.Is(errResolve, context.DeadlineExceeded) {
		close(env.getRelease)
		t.Fatalf("resolveAuth error = %v, want deadline exceeded", errResolve)
	}
	select {
	case <-env.getStarted:
	case <-time.After(time.Second):
		close(env.getRelease)
		t.Fatal("GetAuth was not called")
	}
	close(env.getRelease)
}
