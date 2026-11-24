package proxy

import (
	"grok-quality-guard/internal/app/model"
	"grok-quality-guard/internal/app/state"
)

type Account = model.Account
type State = state.State
type Store = state.Store
type StateStore = state.Store
type AccountState = state.AccountState
type AccountStateSummary = state.AccountStateSummary
type ResinLeaseDelete = state.ResinLeaseDelete
type ProxyCommit = state.ProxyCommit
type ProxyAccountCommit = state.ProxyAccountCommit
type proxyCommit = state.ProxyCommit
type proxyAccountCommit = state.ProxyAccountCommit

const (
	StateUnknown            = state.StateUnknown
	StateHealthy            = state.StateHealthy
	StateSuspect            = state.StateSuspect
	StateDegraded           = state.StateDegraded
	StateCooling            = state.StateCooling
	StateQuotaExhausted     = state.StateQuotaExhausted
	StateCredentialInvalid  = state.StateCredentialInvalid
	StateDisabledByGuard    = state.StateDisabledByGuard
	StateExternallyDisabled = state.StateExternallyDisabled
)
