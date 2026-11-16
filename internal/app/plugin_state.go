package app

import (
	"errors"
	appstate "grok-quality-guard/internal/app/state"
	"sync"
	"time"
)

func (p *Plugin) accountLock(authID string) *sync.Mutex {
	p.accountLocksMu.Lock()
	defer p.accountLocksMu.Unlock()
	lock := p.accountLocks[authID]
	if lock == nil {
		lock = &sync.Mutex{}
		p.accountLocks[authID] = lock
	}
	return lock
}

func (p *Plugin) ensureStateAccount(state *appstate.State, authID string) *appstate.AccountState {
	if state.Accounts == nil {
		state.Accounts = make(map[string]*appstate.AccountState)
	}
	account := state.Accounts[authID]
	if account == nil {
		account = &appstate.AccountState{AuthID: authID, State: appstate.StateUnknown, FirstSeenAt: time.Now().UTC()}
		state.Accounts[authID] = account
	}
	if account.FirstSeenAt.IsZero() {
		account.FirstSeenAt = time.Now().UTC()
	}
	return account
}

func (p *Plugin) stateAccount(authID string) (appstate.AccountState, bool) {
	store := p.stateStore()
	if store == nil {
		return appstate.AccountState{}, false
	}
	return store.AccountSnapshot(authID)
}

func (p *Plugin) updateStateAccount(authID string, fn func(*appstate.AccountState) error) error {
	if p.stateStore() == nil {
		return errors.New("state store is not initialized")
	}
	errUpdate := p.stateStore().UpdateAccount(authID, func(account *appstate.AccountState) (*appstate.Event, error) {
		return nil, fn(account)
	})
	if errUpdate == nil {
		p.routingSvc.RefreshAccount(authID)
	}
	return errUpdate
}

func (p *Plugin) recordEvent(state *appstate.State, authID, class, action, result, detail string) {
	appstate.AppendEvent(state, appstate.Event{
		Time:   time.Now().UTC(),
		AuthID: authID,
		Class:  class,
		Action: action,
		Result: result,
		Detail: detail,
	})
}
