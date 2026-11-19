package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/auth"
	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
	"grok-quality-guard/internal/model"
)

func isGrokProvider(value string) bool { return auth.IsGrokProvider(value) }
func runBoundedQueue[T any](ctx context.Context, values []T, queueSize, workers int, fn func(context.Context, T)) error {
	return tasks.RunBoundedQueue(ctx, values, queueSize, workers, fn)
}
func (s *Service) setGrokAuthModels(credentials []pluginapi.HostAuthModels) {
	if s == nil {
		return
	}
	models := make(map[string]struct{})
	for _, credential := range credentials {
		if !auth.IsGrokProvider(credential.Provider) {
			continue
		}
		for _, name := range credential.Models {
			if slug := model.NormalizeSlug(name); slug != "" {
				models[slug] = struct{}{}
			}
		}
	}
	s.authModelsMu.Lock()
	s.authModelsGeneration++
	s.grokModels = models
	s.authModelsReady = true
	s.authModelsAt = time.Now().UTC()
	s.authModelsLastAttempt = s.authModelsAt
	s.authModelsMu.Unlock()
}

func (s *Service) invalidateGrokAuthModels() {
	s.authModelsMu.Lock()
	s.grokModels = make(map[string]struct{})
	s.authModelsReady = false
	s.authModelsAt = time.Time{}
	s.authModelsLastAttempt = time.Time{}
	s.authModelsGeneration++
	s.authModelsMu.Unlock()
}

func (s *Service) ensureGrokAuthModels(ctx context.Context, force bool) error {
	if s == nil || s.env == nil {
		return errors.New("catalog service is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errAcquire := acquireGate(ctx, s.authModelsRefreshGate); errAcquire != nil {
		return errAcquire
	}
	currentForce := force
	for {
		errRefresh := s.ensureGrokAuthModelsLocked(ctx, currentForce)
		if errRefresh != nil {
			s.authModelsMu.Lock()
			s.authModelsRefreshPending = false
			releaseGate(s.authModelsRefreshGate)
			s.authModelsMu.Unlock()
			return errRefresh
		}
		s.authModelsMu.Lock()
		pending := s.authModelsRefreshPending
		s.authModelsRefreshPending = false
		if !pending {
			releaseGate(s.authModelsRefreshGate)
			s.authModelsMu.Unlock()
			return nil
		}
		s.authModelsMu.Unlock()
		if errCtx := ctx.Err(); errCtx != nil {
			s.authModelsMu.Lock()
			releaseGate(s.authModelsRefreshGate)
			s.authModelsMu.Unlock()
			return errCtx
		}
		currentForce = true
	}
}

func (s *Service) ensureGrokAuthModelsLocked(ctx context.Context, force bool) error {
	if !force && !s.grokAuthModelsNeedRefresh(false) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	callCtx, cancel := context.WithTimeout(ctx, authModelsCallTimeout)
	defer cancel()
	s.authModelsMu.RLock()
	generation := s.authModelsGeneration
	s.authModelsMu.RUnlock()
	credentials, errList := s.env.ListAuthModels(callCtx)
	if errList != nil {
		s.authModelsMu.Lock()
		s.authModelsLastAttempt = time.Now().UTC()
		s.authModelsMu.Unlock()
		return errList
	}
	models := make(map[string]struct{})
	for _, credential := range credentials {
		if !auth.IsGrokProvider(credential.Provider) {
			continue
		}
		for _, name := range credential.Models {
			if slug := model.NormalizeSlug(name); slug != "" {
				models[slug] = struct{}{}
			}
		}
	}
	now := time.Now().UTC()
	s.authModelsMu.Lock()
	if generation == s.authModelsGeneration {
		s.grokModels = models
		s.authModelsReady = true
		s.authModelsAt = now
		s.authModelsLastAttempt = now
	} else {
		// The catalog changed while host.auth.models was in flight. Discard the
		// stale result, avoid starting the retry backoff, and let the refresh
		// coordinator run the queued follow-up immediately.
		s.authModelsRefreshPending = true
	}
	s.authModelsMu.Unlock()
	return nil
}

func (s *Service) startGrokAuthModelsRefresh(force bool) {
	if s == nil || s.env == nil || s.stopped() {
		return
	}
	s.authModelsMu.Lock()
	s.authModelsRefreshPending = s.authModelsRefreshPending || force
	if !tryAcquireGate(s.authModelsRefreshGate) {
		s.authModelsMu.Unlock()
		return
	}
	s.authModelsRefreshPending = false
	s.authModelsMu.Unlock()
	go s.runGrokAuthModelsRefresh(force)
}

// releaseAuthModelsRefreshGate clears a follow-up request only while the
// coordinator still owns the gate. Callers must hold the gate.
func (s *Service) releaseAuthModelsRefreshGate() {
	s.authModelsMu.Lock()
	s.authModelsRefreshPending = false
	releaseGate(s.authModelsRefreshGate)
	s.authModelsMu.Unlock()
}

func (s *Service) runGrokAuthModelsRefresh(force bool) {
	released := false
	defer func() {
		if !released {
			// This also handles a panic or an early lifecycle stop. In the
			// normal path the gate is released while holding authModelsMu
			// below, so a queued request cannot be lost.
			s.releaseAuthModelsRefreshGate()
		}
	}()
	currentForce := force
	for {
		if s.stopped() {
			return
		}
		ctx, cancel := context.WithTimeout(s.backgroundContext(), authModelsCallTimeout)
		errRefresh := s.ensureGrokAuthModelsLocked(ctx, currentForce)
		cancel()

		// Keep the gate held across queued follow-up refreshes so lifecycle
		// waits observe the complete sequence. Release while holding
		// authModelsMu to prevent a request from becoming stranded between the
		// final pending check and releasing the gate.
		s.authModelsMu.Lock()
		pending := s.authModelsRefreshPending
		s.authModelsRefreshPending = false
		if errRefresh != nil || !pending {
			releaseGate(s.authModelsRefreshGate)
			s.authModelsMu.Unlock()
			released = true
			return
		}
		s.authModelsMu.Unlock()
		currentForce = true
	}
}

func (s *Service) IsGrokModel(name string, prefixes []string) bool {
	if s == nil {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized != "" {
		for _, prefix := range prefixes {
			prefix = strings.ToLower(strings.TrimSpace(prefix))
			if prefix != "" && strings.HasPrefix(normalized, prefix) {
				return true
			}
		}
	}
	slug := model.NormalizeSlug(name)
	if slug == "" {
		return false
	}
	s.authModelsMu.RLock()
	_, ok := s.grokModels[slug]
	ready := s.authModelsReady
	s.authModelsMu.RUnlock()
	if ok || ready {
		return ok
	}
	if s.env == nil {
		return false
	}
	// Alias routing must not silently miss a model while the post-catalog model
	// refresh is still in flight. This wait is limited to the first alias check;
	// account list reads never wait for host.auth.models.
	_ = s.ensureGrokAuthModels(s.backgroundContext(), false)
	s.authModelsMu.RLock()
	_, ok = s.grokModels[slug]
	s.authModelsMu.RUnlock()
	return ok
}

func stateForDisableKind(kind string) string {
	switch kind {
	case appstate.DisableKindCooldown:
		return appstate.StateCooling
	case appstate.DisableKindQuota:
		return appstate.StateQuotaExhausted
	case appstate.DisableKindCredentialInvalid:
		return appstate.StateCredentialInvalid
	case appstate.DisableKindPermanent:
		return appstate.StateDisabledByGuard
	default:
		return appstate.StateCooling
	}
}

const (
	StateUnknown            = appstate.StateUnknown
	StateExternallyDisabled = appstate.StateExternallyDisabled
)

func rawAuthToMap(raw []byte) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("auth JSON must be an object")
	}
	return value, nil
}
