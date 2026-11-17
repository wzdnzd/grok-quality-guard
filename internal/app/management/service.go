package management

import (
	"context"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/model"
	proxyapp "grok-quality-guard/internal/app/proxy"
	qualityapp "grok-quality-guard/internal/app/quality"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
	"grok-quality-guard/internal/config"
)

type Account = model.Account
type AccountState = appstate.AccountState
type AccountStateSummary = appstate.AccountStateSummary
type AttemptEvidence = appstate.AttemptEvidence
type Event = appstate.Event
type QualityEvidence = appstate.QualityEvidence
type ResinSettings = appstate.ResinSettings
type State = appstate.State
type Store = appstate.Store
type Policy = config.Policy
type Task = tasks.Task

type Environment interface {
	Store() *appstate.Store
	EnsureCatalog(context.Context) error
	RefreshCatalog(context.Context) error
	CatalogSnapshot() []model.Account
	FindAccount(string) (model.Account, bool)
	ResolveAuth(context.Context, string) (model.Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error)
	StateAccount(string) (appstate.AccountState, bool)
	ConfigEnabled() bool
	Policy() config.Policy
	YAMLPolicy() (config.Policy, string)
	SetPolicy(config.Policy)
	WakeProbeWaiters()
	ProbeAccount(context.Context, string, bool, string) error
	ProbeQueueSize() int
	ProbeConcurrency() int
	StartTask(string, []string, string, func(context.Context) (any, error)) tasks.Task
	HasActiveTask(string, string) bool
	TaskByID(string) (tasks.Task, bool)
	CancelTask(string) (tasks.Task, error)
	ListTasks(int) []tasks.Task
	UpdateStatePath(string) (string, error)
	Proxy() *proxyapp.Service
	Quality() *qualityapp.Service
	AccountProxyURL(context.Context, model.Account, bool) string
}

type Service struct {
	env Environment
}

func NewService(env Environment) *Service { return &Service{env: env} }

type proxyManagementError = proxyapp.ManagementError
