package catalog

import (
	"context"
	"errors"
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
	ListAuths(context.Context) ([]pluginapi.HostAuthFileEntry, error)
	ListAuthModels(context.Context) ([]pluginapi.HostAuthModels, error)
	GetAuth(string) (pluginapi.HostAuthGetResponse, error)
	GetAuthContext(context.Context, string) (pluginapi.HostAuthGetResponse, error)
	SelectionReady() bool
	RebuildSelectionIndex() error
	WakeProxyAutoBind()
	WakeResinLeaseDeleteLoop()
	PruneProxyObservations([]string)
}

type Service struct {
	env Environment
	// lifecycleCtx bounds background catalog enrichment. It is cancelled by
	// Close during plugin shutdown so file reads and host callbacks cannot
	// outlive the host that supplied them.
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc

	catalogMu                 sync.RWMutex
	catalog                   []model.Account
	catalogByID               map[string]int
	catalogByAuthIndex        map[string]int
	catalogAt                 time.Time
	catalogRefreshAttemptAt   time.Time
	catalogRoutingFingerprint uint64
	catalogModelsFingerprint  uint64
	catalogRefreshGate        chan struct{}
	authMetaRefreshMu         sync.Mutex
	authMetaRefreshGate       chan struct{}
	authMetaRefreshPending    bool
	authMetaRead              func(string) (string, string, bool)
	authMetaFailuresMu        sync.Mutex
	authMetaFailures          map[string]authMetaFailure
	authModelsMu              sync.RWMutex
	authModelsRefreshGate     chan struct{}
	grokModels                map[string]struct{}
	authModelsReady           bool
	authModelsAt              time.Time
	authModelsLastAttempt     time.Time
	authModelsGeneration      uint64
	authModelsRefreshPending  bool
}

func NewService(env Environment) *Service {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	service := &Service{
		env:                   env,
		lifecycleCtx:          lifecycleCtx,
		lifecycleCancel:       lifecycleCancel,
		catalogRefreshGate:    make(chan struct{}, 1),
		authMetaRefreshGate:   make(chan struct{}, 1),
		authMetaRead:          readAuthMeta,
		authMetaFailures:      make(map[string]authMetaFailure),
		authModelsRefreshGate: make(chan struct{}, 1),
		grokModels:            make(map[string]struct{}),
	}
	service.catalogRefreshGate <- struct{}{}
	service.authMetaRefreshGate <- struct{}{}
	service.authModelsRefreshGate <- struct{}{}
	return service
}

// Close stops background enrichment. Catalog snapshots remain readable so
// management shutdown can finish without waiting two minutes for auth files.
func (s *Service) Close() {
	if s != nil && s.lifecycleCancel != nil {
		s.lifecycleCancel()
	}
}

func (s *Service) backgroundContext() context.Context {
	if s != nil && s.lifecycleCtx != nil {
		return s.lifecycleCtx
	}
	return context.Background()
}

func (s *Service) stopped() bool {
	return s.backgroundContext().Err() != nil
}

func acquireGate(ctx context.Context, gate chan struct{}) error {
	if gate == nil {
		return errors.New("service gate is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func tryAcquireGate(gate chan struct{}) bool {
	if gate == nil {
		return false
	}
	select {
	case <-gate:
		return true
	default:
		return false
	}
}

func releaseGate(gate chan struct{}) {
	gate <- struct{}{}
}
