package routing

import (
	"context"
	"encoding/json"

	"grok-quality-guard/internal/app/executor"
	"grok-quality-guard/internal/app/model"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
)

type Account = model.Account
type AccountState = appstate.AccountState
type State = appstate.State
type Store = appstate.Store
type Policy = config.Policy
type RoutingConfig = config.RoutingConfig
type Request = executor.Request
type Facts = executor.Facts

type Environment interface {
	Store() *appstate.Store
	CatalogSnapshot() []model.Account
	FindAccount(string) (model.Account, bool)
	StateAccount(string) (appstate.AccountState, bool)
	Policy() config.Policy
	ExecutableAuth(context.Context, string) (model.Account, map[string]json.RawMessage, bool)
}

type Service struct {
	env              Environment
	selectionIndex   routingSelectionIndex
	selectionRuntime routingSelectionRuntime
}

func NewService(env Environment) *Service {
	return &Service{env: env, selectionRuntime: newRoutingSelectionRuntime()}
}
