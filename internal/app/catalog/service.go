package catalog

import (
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/model"
	appstate "grok-quality-guard/internal/app/state"
)

type Account = model.Account
type AccountState = appstate.AccountState
type Event = appstate.Event
type State = appstate.State
type Store = appstate.Store

type Environment interface {
	Store() *appstate.Store
	ListAuths() ([]pluginapi.HostAuthFileEntry, error)
	ListAuthModels() ([]pluginapi.HostAuthModels, error)
	GetAuth(string) (pluginapi.HostAuthGetResponse, error)
	SelectionReady() bool
	RebuildSelectionIndex() error
	WakeProxyAutoBind()
	WakeResinLeaseDeleteLoop()
}

type Service struct {
	env Environment

	catalogMu                 sync.RWMutex
	catalog                   []model.Account
	catalogByID               map[string]int
	catalogByAuthIndex        map[string]int
	catalogAt                 time.Time
	catalogStateFingerprint   uint64
	catalogRoutingFingerprint uint64
	catalogModelsFingerprint  uint64
	catalogRefreshMu          sync.Mutex
	authModelsMu              sync.RWMutex
	grokModels                map[string]struct{}
	authModelsReady           bool
	authModelsAt              time.Time
}

func NewService(env Environment) *Service {
	return &Service{env: env, grokModels: make(map[string]struct{})}
}
