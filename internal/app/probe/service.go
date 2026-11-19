package probe

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/model"
	qualityapp "grok-quality-guard/internal/app/quality"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
	domain "grok-quality-guard/internal/quality"
)

type Account = model.Account
type AccountState = appstate.AccountState
type ProbeAccountState = appstate.ProbeAccountState
type Event = appstate.Event
type State = appstate.State
type Store = appstate.Store
type Policy = config.Policy
type ProbeConfig = config.ProbeConfig
type QualityConfig = config.QualityConfig
type QualityVerdict = domain.QualityVerdict
type VerdictResult = qualityapp.VerdictResult
type Signals = domain.Signals

// QueueSnapshot is the management-facing view of the automatic probe
// scheduler. It intentionally excludes auth IDs and probe payloads.
type QueueSnapshot struct {
	Started       bool      `json:"started"`
	Waiting       int       `json:"waiting"`
	Active        int       `json:"active"`
	Succeeded     int       `json:"succeeded"`
	Failed        int       `json:"failed"`
	LastScanAt    time.Time `json:"last_scan_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	LastErrorCode string    `json:"last_error_code,omitempty"`
}

// QueueEntry identifies one account currently owned by the automatic probe
// scheduler. It is only returned by the explicit queue-detail action.
type QueueEntry struct {
	AuthID    string    `json:"auth_id"`
	Status    string    `json:"status"`
	QueuedAt  time.Time `json:"queued_at,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

// QueueDetail separates running probes from the bounded waiting queue.
type QueueDetail struct {
	Active  []QueueEntry `json:"active"`
	Waiting []QueueEntry `json:"waiting"`
}

const (
	ClassNone                  = domain.ClassNone
	ClassIgnored               = domain.ClassIgnored
	ClassProbeError            = domain.ClassProbeError
	ClassProxyError            = domain.ClassProxyError
	ClassCredentialInvalid     = domain.ClassCredentialInvalid
	ClassQuotaExhausted        = domain.ClassQuotaExhausted
	ClassRateLimited           = domain.ClassRateLimited
	ClassExpectedMarkerMissing = domain.ClassExpectedMarkerMissing
	ClassMissingThinking       = domain.ClassMissingThinking
	ClassBufferedBurst         = domain.ClassBufferedBurst
	ClassCipherDrool           = domain.ClassCipherDrool
	ClassFakeEncryptedDump     = domain.ClassFakeEncryptedDump
	ClassFastReasoningRatio    = domain.ClassFastReasoningRatio
	ClassHardTPS               = domain.ClassHardTPS
	ClassEmptyStream           = domain.ClassEmptyStream
)

const (
	StateUnknown            = appstate.StateUnknown
	StateHealthy            = appstate.StateHealthy
	StateSuspect            = appstate.StateSuspect
	StateDegraded           = appstate.StateDegraded
	StateCooling            = appstate.StateCooling
	StateQuotaExhausted     = appstate.StateQuotaExhausted
	StateCredentialInvalid  = appstate.StateCredentialInvalid
	StateDisabledByGuard    = appstate.StateDisabledByGuard
	StateExternallyDisabled = appstate.StateExternallyDisabled
)

type Environment interface {
	Store() *appstate.Store
	CatalogSnapshot() []model.Account
	Policy() config.Policy
	AccountLock(string) *sync.Mutex
	ResolveAuth(context.Context, string) (model.Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error)
	StateAccount(string) (appstate.AccountState, bool)
	SaveAuth(string, json.RawMessage) (pluginapi.HostAuthSaveResponse, error)
	SaveAuthDisabled(string, map[string]json.RawMessage, bool) error
	RefreshSelectionAccount(string)
	ExecuteModelStream(context.Context, pluginapi.HostModelExecutionRequest, string) (pluginapi.HostModelStreamResponse, error)
	CloseModelStream(string) error
	ReadModelStream(context.Context, string) (pluginapi.HostModelStreamReadResponse, error)
	ReadModelStreamErrorBody(context.Context, string) []byte
	Reconcile(context.Context) error
	Quality() *qualityapp.Service
	RecordInternalError(string, error) error
}

type Service struct {
	env Environment

	schedulerMu          sync.RWMutex
	scheduler            *probeScheduler
	probeGate            sync.Mutex
	probeCond            *sync.Cond
	probeActive          int
	probePriorityWaiters int
	probing              map[string]struct{}

	probeSkipMu sync.Mutex
	probeSkip   map[string]time.Time
}

func NewService(env Environment) *Service {
	service := &Service{
		env:       env,
		probing:   make(map[string]struct{}),
		probeSkip: make(map[string]time.Time),
	}
	service.probeCond = sync.NewCond(&service.probeGate)
	return service
}
