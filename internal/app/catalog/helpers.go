package catalog

import (
	"context"
	"encoding/json"
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
	s.grokModels = models
	s.authModelsReady = true
	s.authModelsAt = time.Now().UTC()
	s.authModelsMu.Unlock()
}

func (s *Service) IsGrokModel(name string, prefixes []string) bool {
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
