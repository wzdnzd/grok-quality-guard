package quality

import (
	"context"
	"strings"
	"time"

	"grok-quality-guard/internal/app/redact"
)

const proxyAutoRotateTaskKind = "proxy.auto_rotate"

func (s *Service) queueProxyErrorRotation(authID string, verdict QualityVerdict) {
	if strings.TrimSpace(authID) == "" || !s.env.AutoRotateAfterQualityFailure() {
		return
	}
	if s.env.HasActiveTask(proxyAutoRotateTaskKind, authID) {
		return
	}
	stateAccount, ok := s.env.StateAccount(authID)
	if !ok {
		return
	}
	if _, managed := s.env.ProxyBindingForAccount(stateAccount); !managed {
		return
	}
	s.env.StartTask(proxyAutoRotateTaskKind, []string{authID}, "正在自动轮换代理", func(ctx context.Context) (any, error) {
		if errRotate := s.finishProxyErrorRotation(ctx, authID, verdict); errRotate != nil {
			return nil, errRotate
		}
		return map[string]any{"auth_id": authID, "status": "finished"}, nil
	})
}

func (s *Service) finishProxyErrorRotation(ctx context.Context, authID string, verdict QualityVerdict) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(authID) == "" {
		return nil
	}
	if !s.env.AutoRotateAfterQualityFailure() {
		return s.applyProxyErrorFallback(authID, verdict)
	}
	stateAccount, ok := s.env.StateAccount(authID)
	if !ok {
		return nil
	}
	switch stateAccount.State {
	case StateExternallyDisabled, StateDisabledByGuard, StateCredentialInvalid, StateQuotaExhausted:
		return nil
	}
	binding, managed := s.env.ProxyBindingForAccount(stateAccount)
	if !managed {
		return s.applyProxyErrorFallback(authID, verdict)
	}
	status, errRotate := s.env.RotateProxy(ctx, authID, string(binding.SourceKind))
	if errRotate == nil && (status == "rotated" || status == "succeeded") {
		return s.clearProxyFailureStrikes(ctx, authID)
	}
	s.recordProxyRotationFailure(authID, verdict.Class, status, errRotate)
	return s.applyProxyErrorFallback(authID, verdict)
}

func (s *Service) applyProxyErrorFallback(authID string, verdict QualityVerdict) error {
	if s.env.Policy().AutoAction == "observe" {
		return nil
	}
	return s.applyAutoCooldown(authID, verdict, "probe")
}

func (s *Service) recordProxyRotationFailure(authID, class, status string, errRotate error) {
	store := s.env.Store()
	if store == nil {
		return
	}
	detail := strings.TrimSpace(status)
	if errRotate != nil {
		detail = redact.SanitizeError(errRotate)
	}
	if detail == "" {
		detail = "proxy rotation failed"
	}
	_ = store.AppendEvent(Event{
		Time:   time.Now().UTC(),
		AuthID: authID,
		Class:  class,
		Action: "proxy_rotate",
		Result: "failed",
		Detail: detail,
	})
}

// clearProxyFailureStrikes restores a guard-owned cooldown after the managed
// proxy was actually replaced. Other quality strikes and auth evidence remain.
func (s *Service) clearProxyFailureStrikes(ctx context.Context, authID string) error {
	lock := s.env.AccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	store := s.env.Store()
	if store == nil {
		return nil
	}
	_, response, raw, errResolve := s.env.ResolveAuth(ctx, authID)
	if errResolve != nil {
		return errResolve
	}
	account, ok := s.env.StateAccount(authID)
	if !ok {
		return nil
	}
	switch account.State {
	case StateExternallyDisabled, StateDisabledByGuard, StateCredentialInvalid, StateQuotaExhausted:
		return nil
	}
	guardOwned := account.DisableKind != ""
	if authDisabled(raw) && !guardOwned {
		return nil
	}
	if guardOwned {
		intent := false
		if errIntent := store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
			account.AuthDisabledIntent = &intent
			return nil, nil
		}); errIntent != nil {
			return errIntent
		}
		if authDisabled(raw) {
			if errSave := s.env.SaveAuthDisabled(response.Name, raw, false); errSave != nil {
				return errSave
			}
		}
	}
	defer s.env.RefreshSelectionAccount(authID)
	return store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
		account.ErrorStrikes = 0
		account.CooldownUntil = time.Time{}
		account.ProbeBackoffUntil = time.Time{}
		account.DisableKind = ""
		account.DisableReason = ""
		account.PluginOnlyDisable = false
		account.PreviousDisabled = false
		account.AuthDisabledIntent = nil
		if account.LastReason == ClassProbeError || account.LastReason == ClassProxyError {
			account.LastReason = ""
		}
		if isIsolatedState(account.State) || account.State == StateSuspect || account.State == StateDegraded {
			account.State = StateUnknown
		}
		return &Event{
			Time:   time.Now().UTC(),
			AuthID: authID,
			Action: "proxy_error_rotation",
			Result: "strikes_reset",
			Detail: "managed proxy replaced",
		}, nil
	})
}
