package quality

import (
	"context"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"time"
)

type VerdictResult = verdictResult

func (s *Service) ApplyVerdictLocked(
	ctx context.Context,
	authID string,
	verdict QualityVerdict,
	source, callbackID string,
	raw map[string]json.RawMessage,
	response pluginapi.HostAuthGetResponse,
	reEnabled bool,
) (VerdictResult, error) {
	return s.applyVerdictLocked(ctx, authID, verdict, source, callbackID, raw, response, reEnabled)
}
func (s *Service) ApplyGuardDisable(authID, kind string, duration time.Duration, class, source string, strike bool) error {
	return s.applyGuardDisable(authID, kind, duration, class, source, strike)
}
func (s *Service) ApplyAutoCooldown(authID string, verdict QualityVerdict, source string) error {
	return s.applyAutoCooldown(authID, verdict, source)
}
func (s *Service) SetStateExternal(authID, class string) error {
	return s.setStateExternal(authID, class)
}
func NoteHealthyObservation(account *AccountState, now time.Time, cfg QualityConfig) *Event {
	return noteHealthyObservation(account, now, cfg)
}
