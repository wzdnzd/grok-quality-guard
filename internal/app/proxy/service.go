package proxy

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/model"
	"grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
	"grok-quality-guard/internal/config"
)

// Environment is the application-owned state and host surface used by the
// proxy runtime. Keeping it explicit prevents this package from importing the
// composition package and creating a cycle.
type Environment interface {
	ProxyStore() *state.Store
	ProxyCatalogSnapshot() []model.Account
	ProxyEnsureCatalog(context.Context) error
	ProxyFindAccount(string) (model.Account, bool)
	ProxyResolveAuth(context.Context, string) (model.Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error)
	ProxySaveAuth(string, json.RawMessage) (pluginapi.HostAuthSaveResponse, error)
	ProxyAccountLock(string) *sync.Mutex
	ProxyStateAccount(string) (state.AccountState, bool)
	ProxyUpdateStateAccount(string, func(*state.AccountState) error) error
	ProxyRecordEvent(*state.State, string, string, string, string, string)
	ProxyStartTask(string, []string, string, func(context.Context) (any, error)) tasks.Task
	ProxyHasActivePrefix(...string) bool
	ProxyConfigSnapshot() (config.ProxyConfig, bool)
	ProxyApplyConfig(config.ProxyConfig, bool)
}

// Service owns proxy binding, source management, scheduling and Resin runtime.
type Service struct {
	env                  Environment
	mgr                  proxyManager
	resinLeaseDeleteWake chan struct{}
}

func NewService(env Environment) *Service {
	return &Service{env: env, mgr: newProxyManager(), resinLeaseDeleteWake: make(chan struct{}, 1)}
}
