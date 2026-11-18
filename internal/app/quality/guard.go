package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func isProbeErrorClass(class string) bool {
	return class == ClassProbeError || class == ClassProxyError
}

func positiveInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func (s *Service) scheduleVerdictCrossVerify(authID, class, source string) {
	if s == nil || s.env == nil || strings.TrimSpace(authID) == "" {
		return
	}
	if s.env.HasActiveTask("quality.cross_verify", authID) {
		return
	}
	s.env.StartTask("quality.cross_verify", []string{authID}, "正在交叉验证质量异常", func(ctx context.Context) (any, error) {
		if errProbe := s.env.ProbeAccount(ctx, authID, true, ""); errProbe != nil {
			return nil, errProbe
		}
		account, okAccount := s.env.FindAccount(authID)
		if !okAccount {
			return nil, fmt.Errorf("auth not found")
		}
		return s.env.AccountView(ctx, account)
	})
	_ = s.env.Store().AppendEvent(Event{
		Time:   time.Now().UTC(),
		AuthID: authID,
		Class:  class,
		Action: "cross_verify_schedule",
		Result: "scheduled",
		Detail: source,
	})
}

func (s *Service) applyAutoCooldown(authID string, verdict QualityVerdict, source string) error {
	lock := s.env.AccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	account, _ := s.env.StateAccount(authID)
	_, response, raw, errResolve := s.env.ResolveAuth(context.Background(), authID)
	if errResolve != nil {
		return errResolve
	}
	_, errApply := s.applyAutoCooldownWithAuthLocked(
		context.Background(),
		authID,
		verdict,
		source,
		raw,
		response,
		account.QualityStrikeMissingThinking,
		false,
	)
	return errApply
}

func (s *Service) applyAutoCooldownWithAuthLocked(
	ctx context.Context,
	authID string,
	verdict QualityVerdict,
	source string,
	raw map[string]json.RawMessage,
	response pluginapi.HostAuthGetResponse,
	previousMissingStrike bool,
	force bool,
) (bool, error) {
	account, _ := s.env.StateAccount(authID)
	now := time.Now().UTC()
	if !force && isCooldownState(account.State) && now.Before(account.CooldownUntil) {
		return false, nil
	}
	if account.State == StateDisabledByGuard || account.State == StateExternallyDisabled || account.State == StateCredentialInvalid {
		return false, nil
	}
	policy := s.env.Policy()
	kind := DisableKindCooldown
	duration := cooldownForClass(verdict.Class, policy.Quality)
	switch verdict.Class {
	case ClassCredentialInvalid:
		kind = DisableKindCredentialInvalid
		duration = 0
	case ClassQuotaExhausted:
		kind = DisableKindQuota
		duration = quotaExhaustedCooldown
	default:
		if isProbeErrorClass(verdict.Class) {
			duration = nextProbeBackoff(policy.Probe, account)
		}
		if duration <= 0 {
			duration = policy.Quality.MissingThinkingCooldown.Duration
		}
	}
	allowPermanent := policy.AutoAction == "cooldown_then_disable"
	if allowPermanent && kind == DisableKindCooldown && verdict.Class == ClassMissingThinking && previousMissingStrike {
		kind = DisableKindPermanent
		duration = 0
	}
	strike := verdict.Class == ClassMissingThinking
	if errDisable := s.saveGuardDisableLocked(ctx, authID, response, raw, kind, duration, verdict.Class, source, strike); errDisable != nil {
		return false, errDisable
	}
	return true, nil
}

func (s *Service) holdProbeRecoveryLocked(
	ctx context.Context,
	authID string,
	raw map[string]json.RawMessage,
	response pluginapi.HostAuthGetResponse,
	class string,
	backoff time.Duration,
) error {
	if raw == nil {
		_, resolvedResponse, resolvedRaw, errResolve := s.env.ResolveAuth(ctx, authID)
		if errResolve != nil {
			return errResolve
		}
		raw = resolvedRaw
		response = resolvedResponse
	}
	account, okAccount := s.env.StateAccount(authID)
	if !okAccount || account.DisableKind == "" || authDisabled(raw) {
		return nil
	}
	if account.DisableKind == DisableKindPermanent || account.DisableKind == DisableKindCredentialInvalid {
		return nil
	}
	policy := s.env.Policy()
	duration := backoff
	switch class {
	case ClassEmptyStream:
		duration = policy.Quality.EmptyStreamCooldown.Duration
	case ClassMissingThinking:
		duration = policy.Quality.MissingThinkingCooldown.Duration
	}
	if duration <= 0 {
		duration = policy.Quality.MissingThinkingCooldown.Duration
	}
	return s.saveGuardDisableLocked(ctx, authID, response, raw, DisableKindCooldown, duration, class, "probe", account.QualityStrikeMissingThinking)
}

func (s *Service) applyGuardDisable(authID, kind string, duration time.Duration, class, source string, strike bool) error {
	return s.applyGuardDisableWithOptions(authID, kind, duration, class, source, strike, false)
}

func (s *Service) applyGuardDisableWithOptions(authID, kind string, duration time.Duration, class, source string, strike, pluginOnly bool) error {
	lock := s.env.AccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	return s.applyGuardDisableLockedWithOptions(context.Background(), authID, kind, duration, class, source, strike, pluginOnly)
}

// applyGuardDisableLocked records the desired state before touching the auth
// file so an interrupted write can be repaired by the reconciler. Callers must
// hold the per-auth lock.
func (s *Service) applyGuardDisableLocked(ctx context.Context, authID, kind string, duration time.Duration, class, source string, strike bool) error {
	return s.applyGuardDisableLockedWithOptions(ctx, authID, kind, duration, class, source, strike, false)
}

func (s *Service) applyGuardDisableLockedWithOptions(ctx context.Context, authID, kind string, duration time.Duration, class, source string, strike, pluginOnly bool) error {
	_, response, raw, errResolve := s.env.ResolveAuth(ctx, authID)
	if errResolve != nil {
		return errResolve
	}
	return s.saveGuardDisableLockedWithOptions(ctx, authID, response, raw, kind, duration, class, source, strike, pluginOnly)
}

func (s *Service) saveGuardDisableLocked(
	_ context.Context,
	authID string,
	response pluginapi.HostAuthGetResponse,
	raw map[string]json.RawMessage,
	kind string,
	duration time.Duration,
	class, source string,
	strike bool,
) error {
	return s.saveGuardDisableLockedWithOptions(context.Background(), authID, response, raw, kind, duration, class, source, strike, false)
}

func (s *Service) saveGuardDisableLockedWithOptions(
	_ context.Context,
	authID string,
	response pluginapi.HostAuthGetResponse,
	raw map[string]json.RawMessage,
	kind string,
	duration time.Duration,
	class, source string,
	strike, pluginOnly bool,
) error {
	defer s.env.RefreshSelectionAccount(authID)
	store := s.env.Store()
	if store == nil {
		return errors.New("state store is not initialized")
	}
	previousState, _ := s.env.StateAccount(authID)
	if authDisabled(raw) && previousState.DisableKind == "" {
		return s.setStateExternal(authID, class)
	}
	if kind == "" {
		kind = DisableKindCooldown
	}
	previousDisabled := previousState.PreviousDisabled
	if previousState.DisableKind == "" {
		previousDisabled = authDisabled(raw)
	}
	var until time.Time
	if kind == DisableKindCooldown || kind == DisableKindQuota {
		if duration <= 0 {
			duration = s.env.Policy().Quality.MissingThinkingCooldown.Duration
		}
		until = time.Now().UTC().Add(duration)
	}
	errState := store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
		account.State = stateForDisableKind(kind)
		account.CooldownUntil = until
		account.DisableKind = kind
		account.DisableReason = class
		account.PluginOnlyDisable = pluginOnly && !authDisabled(raw)
		account.PreviousDisabled = previousDisabled
		if strike {
			noteQualityFailure(account, time.Now().UTC())
		}
		account.QualityStrikeMissingThinking = account.QualityStrikeMissingThinking || strike
		if class != "" {
			account.LastReason = class
		}
		if !pluginOnly {
			intent := true
			account.AuthDisabledIntent = &intent
		} else {
			account.AuthDisabledIntent = nil
		}
		return nil, nil
	})
	if errState != nil {
		return errState
	}
	if !pluginOnly && !authDisabled(raw) {
		if errSave := s.env.SaveAuthDisabled(response.Name, raw, true); errSave != nil {
			return fmt.Errorf("save guard-disabled auth: %w", errSave)
		}
	}
	return store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
		account.AuthDisabledIntent = nil
		return newEvent(authID, class, "guard_disable", kind, source), nil
	})
}

func (s *Service) setStateExternal(authID, class string) error {
	defer s.env.RefreshSelectionAccount(authID)
	return s.env.Store().UpdateAccount(authID, func(account *AccountState) (*Event, error) {
		account.State = StateExternallyDisabled
		account.DisableKind = ""
		account.DisableReason = ""
		account.PluginOnlyDisable = false
		account.CooldownUntil = time.Time{}
		account.PreviousDisabled = false
		account.AuthDisabledIntent = nil
		return newEvent(authID, class, "external_disable", "observed", "认证文件已被外部系统禁用，原因未知"), nil
	})
}

func (s *Service) ManualCooldown(ctx context.Context, authID string, duration time.Duration) error {
	if duration <= 0 {
		duration = s.env.Policy().Quality.MissingThinkingCooldown.Duration
	}
	return s.applyGuardDisable(authID, DisableKindCooldown, duration, "manual", "manual", false)
}

func (s *Service) ManualDisable(ctx context.Context, authID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "manual"
	}
	return s.applyGuardDisable(authID, DisableKindPermanent, 0, reason, "manual", false)
}

func (s *Service) ManualDisablePluginOnly(ctx context.Context, authID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "manual"
	}
	return s.applyGuardDisableWithOptions(authID, DisableKindPermanent, 0, reason, "manual_plugin", false, true)
}

func (s *Service) ManualRestore(ctx context.Context, authID string) error {
	return s.ManualRestoreExternal(ctx, authID, false)
}

func (s *Service) ManualRestoreExternal(ctx context.Context, authID string, includeExternal bool) error {
	defer s.env.RefreshSelectionAccount(authID)
	lock := s.env.AccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	_, response, raw, errResolve := s.env.ResolveAuth(ctx, authID)
	if errResolve != nil {
		return errResolve
	}
	store := s.env.Store()
	if store == nil {
		return errors.New("state store is not initialized")
	}
	stateAccount, hasState := s.env.StateAccount(authID)
	guardOwned := hasState && stateAccount.DisableKind != "" && !stateAccount.PluginOnlyDisable
	if authDisabled(raw) && !guardOwned {
		if includeExternal {
			if errIntent := store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
				intent := false
				account.AuthDisabledIntent = &intent
				return nil, nil
			}); errIntent != nil {
				return errIntent
			}
			if errSave := s.env.SaveAuthDisabled(response.Name, raw, false); errSave != nil {
				return fmt.Errorf("save externally restored auth: %w", errSave)
			}
			return store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
				resetGuardDisableState(account)
				resetStrikeDecayState(account)
				return newEvent(authID, "", "restore", "restored_external", "manual"), nil
			})
		}
		if errUpdate := s.env.Store().UpdateAccount(authID, func(account *AccountState) (*Event, error) {
			resetGuardDisableState(account)
			resetStrikeDecayState(account)
			return newEvent(authID, "", "restore", "external_left_disabled", "manual"), nil
		}); errUpdate != nil {
			return errUpdate
		}
		return nil
	}
	if guardOwned {
		if errIntent := store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
			intent := false
			account.AuthDisabledIntent = &intent
			return nil, nil
		}); errIntent != nil {
			return errIntent
		}
	}
	if authDisabled(raw) {
		if errSave := s.env.SaveAuthDisabled(response.Name, raw, false); errSave != nil {
			return fmt.Errorf("save restored auth: %w", errSave)
		}
	}
	return store.UpdateAccount(authID, func(account *AccountState) (*Event, error) {
		resetGuardDisableState(account)
		resetStrikeDecayState(account)
		return newEvent(authID, "", "restore", "restored", "manual"), nil
	})
}

func resetStrikeDecayState(account *AccountState) {
	if account == nil {
		return
	}
	account.QualityStrikeMissingThinking = false
	account.QualityFailureAt = time.Time{}
	account.HealthyStreak = 0
	account.HealthyStreakStartedAt = time.Time{}
}

func resetGuardDisableState(account *AccountState) {
	if account == nil {
		return
	}
	account.State = StateUnknown
	account.LastReason = ""
	account.CooldownUntil = time.Time{}
	account.ProbeBackoffUntil = time.Time{}
	account.DisableKind = ""
	account.DisableReason = ""
	account.PluginOnlyDisable = false
	account.PreviousDisabled = false
	account.AuthDisabledIntent = nil
	account.SoftStrikes = 0
	account.ErrorStrikes = 0
	account.ThinkingStrikes = 0
}
