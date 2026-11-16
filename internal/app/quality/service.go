package quality

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/model"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
	"grok-quality-guard/internal/config"
	domain "grok-quality-guard/internal/quality"
)

type Account = model.Account
type AccountState = appstate.AccountState
type AttemptEvidence = appstate.AttemptEvidence
type Event = appstate.Event
type Metrics = appstate.Metrics
type QualityEvidence = appstate.QualityEvidence
type State = appstate.State
type Store = appstate.Store
type QualityConfig = config.QualityConfig
type ProbeConfig = config.ProbeConfig
type QualityVerdict = domain.QualityVerdict
type Signals = domain.Signals

const (
	ClassBufferedBurst          = domain.ClassBufferedBurst
	ClassCipherDrool            = domain.ClassCipherDrool
	ClassFakeEncryptedDump      = domain.ClassFakeEncryptedDump
	ClassFastReasoningRatio     = domain.ClassFastReasoningRatio
	ClassMissingThinking        = domain.ClassMissingThinking
	ClassHardTPS                = domain.ClassHardTPS
	ClassSoftTPS                = domain.ClassSoftTPS
	ClassEmptyStream            = domain.ClassEmptyStream
	ClassProbeError             = domain.ClassProbeError
	ClassProxyError             = domain.ClassProxyError
	ClassCredentialInvalid      = domain.ClassCredentialInvalid
	ClassQuotaExhausted         = domain.ClassQuotaExhausted
	ClassRateLimited            = domain.ClassRateLimited
	ClassIgnored                = domain.ClassIgnored
	ClassInsufficientOutput     = domain.ClassInsufficientOutput
	ClassInsufficientGeneration = domain.ClassInsufficientGeneration
	ClassExpectedMarkerMissing  = domain.ClassExpectedMarkerMissing
	ClassNone                   = domain.ClassNone
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

const (
	DisableKindCooldown          = appstate.DisableKindCooldown
	DisableKindQuota             = appstate.DisableKindQuota
	DisableKindCredentialInvalid = appstate.DisableKindCredentialInvalid
	DisableKindPermanent         = appstate.DisableKindPermanent
)

const quotaExhaustedCooldown = 24 * time.Hour

type Environment interface {
	AccountLock(string) *sync.Mutex
	Store() *appstate.Store
	Policy() config.Policy
	CatalogSnapshot() []model.Account
	StateAccount(string) (appstate.AccountState, bool)
	UpdateStateAccount(string, func(*appstate.AccountState) error) error
	RefreshSelectionAccount(string)
	ResolveAuth(context.Context, string) (model.Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error)
	SaveAuthDisabled(string, map[string]json.RawMessage, bool) error
	FindAccount(string) (model.Account, bool)
	AccountView(context.Context, model.Account) (any, error)
	ProbeAccount(context.Context, string, bool, string) error
	HasActiveTask(string, string) bool
	StartTask(string, []string, string, func(context.Context) (any, error)) tasks.Task
}

type Service struct {
	env Environment
}

func NewService(env Environment) *Service { return &Service{env: env} }
