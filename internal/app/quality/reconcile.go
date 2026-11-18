package quality

import (
	"context"
	"time"
)

func (s *Service) Reconcile(ctx context.Context) error {
	store := s.env.Store()
	if store == nil {
		return nil
	}
	snapshot := store.Snapshot()
	catalog := s.env.CatalogSnapshot()
	accounts := make(map[string]Account, len(catalog))
	for _, account := range catalog {
		accounts[account.ID] = account
	}
	for authID, stateAccount := range snapshot.Accounts {
		if stateAccount == nil {
			continue
		}
		if stateAccount.AuthDisabledIntent != nil || isIsolatedState(stateAccount.State) {
			s.reconcileAccount(ctx, authID)
			continue
		}
		if stateAccount.State != StateUnknown {
			continue
		}
		account, okAccount := accounts[authID]
		if !okAccount || !account.Disabled {
			continue
		}
		s.reconcileAccount(ctx, authID)
	}
	return nil
}

func (s *Service) reconcileAccount(ctx context.Context, authID string) {
	defer s.env.RefreshSelectionAccount(authID)
	lock := s.env.AccountLock(authID)
	lock.Lock()
	defer lock.Unlock()

	_, response, raw, errResolve := s.env.ResolveAuth(ctx, authID)
	if errResolve != nil {
		return
	}
	store := s.env.Store()
	if store == nil {
		return
	}
	stateAccount, okState := s.env.StateAccount(authID)
	if !okState {
		return
	}
	if stateAccount.AuthDisabledIntent != nil {
		target := *stateAccount.AuthDisabledIntent
		if target != authDisabled(raw) {
			if errSave := s.env.SaveAuthDisabled(response.Name, raw, target); errSave != nil {
				return
			}
		}
		if target {
			_ = store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
				account.AuthDisabledIntent = nil
				return nil, nil
			})
			return
		}
		_ = store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
			resetGuardDisableState(account)
			return newEvent(authID, "", "restore", "restored", "reconcile"), nil
		})
		return
	}
	if authDisabled(raw) {
		if stateAccount.PluginOnlyDisable {
			_ = s.setStateExternal(authID, "")
			return
		}
		if stateAccount.DisableKind == "" {
			if stateAccount.State != StateExternallyDisabled {
				_ = s.setStateExternal(authID, "")
			}
			return
		}
		expectedState := stateForDisableKind(stateAccount.DisableKind)
		if stateAccount.State != expectedState {
			_ = s.env.UpdateStateAccount(authID, func(account *AccountState) error {
				account.State = expectedState
				return nil
			})
		}
		return
	}
	if stateAccount.DisableKind == "" {
		if stateAccount.State == StateExternallyDisabled {
			_ = s.env.UpdateStateAccount(authID, func(account *AccountState) error {
				account.State = StateUnknown
				return nil
			})
		}
		return
	}
	if stateAccount.PluginOnlyDisable {
		expectedState := stateForDisableKind(stateAccount.DisableKind)
		if stateAccount.State != expectedState {
			_ = s.env.UpdateStateAccount(authID, func(account *AccountState) error {
				account.State = expectedState
				return nil
			})
		}
		return
	}
	if isCooldownState(stateAccount.State) && time.Now().UTC().Before(stateAccount.CooldownUntil) {
		_ = s.env.SaveAuthDisabled(response.Name, raw, true)
		return
	}
	_ = store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
		resetGuardDisableState(account)
		return newEvent(authID, "", "restore", "restored", "reconcile"), nil
	})
}
