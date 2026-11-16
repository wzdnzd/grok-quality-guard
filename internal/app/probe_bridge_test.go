package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	appmodel "grok-quality-guard/internal/app/model"
	probeapp "grok-quality-guard/internal/app/probe"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	qualitypkg "grok-quality-guard/internal/quality"
)

type probeScheduler struct {
	inner *probeapp.Scheduler
}

func newProbeScheduler(plugin *Plugin, ctx context.Context) *probeScheduler {
	if plugin == nil {
		return &probeScheduler{}
	}
	return &probeScheduler{inner: probeapp.NewScheduler(plugin.probeSvc, ctx)}
}
func (s *probeScheduler) enqueueDue(accounts []appmodel.Account, state appstate.State, policy appconfig.Policy, now time.Time) int {
	if s == nil || s.inner == nil {
		return 0
	}
	return s.inner.EnqueueDue(accounts, state, policy, now)
}
func (s *probeScheduler) dispatch() {
	if s != nil && s.inner != nil {
		s.inner.Dispatch()
	}
}

func (s *probeScheduler) waitIdle() {
	if s != nil && s.inner != nil {
		s.inner.WaitIdle()
	}
}

func (p *Plugin) probeDue(authID string, force bool) (bool, error) {
	return p.probeSvc.ProbeDue(authID, force)
}

func (p *Plugin) acquireProbeSlot(ctx context.Context, priority bool) error {
	return p.probeSvc.AcquireSlot(ctx, priority)
}
func (p *Plugin) releaseProbeSlot() { p.probeSvc.ReleaseSlot() }

func (p *Plugin) probePriorityWaitersCount() int { return p.probeSvc.PriorityWaiterCount() }
func (p *Plugin) markProbeAuth(authID string, ttl time.Duration) {
	p.probeSvc.MarkProbeAuth(authID, ttl)
}
func probeDueForAccount(account *appstate.AccountState, okState bool, authID string, policy appconfig.Policy, now time.Time) bool {
	return probeapp.ProbeDueForAccount(account, okState, authID, policy, now)
}
func unknownProbeJitter(authID string, max time.Duration) time.Duration {
	return probeapp.UnknownProbeJitter(authID, max)
}
func probeBody(cfg appconfig.ProbeConfig) []byte { return probeapp.ProbeBody(cfg) }
func nextProbeBackoff(cfg appconfig.ProbeConfig, previous appstate.AccountState) time.Duration {
	return probeapp.NextProbeBackoff(cfg, previous)
}

func (s *probeScheduler) queueEntries() []string {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.QueueEntries()
}

func (p *Plugin) markProbeSuccess(authID string, sig qualitypkg.Signals) error {
	return p.probeSvc.MarkProbeSuccess(authID, sig)
}
func (p *Plugin) handleProbeFailure(authID string, raw map[string]json.RawMessage, response pluginapi.HostAuthGetResponse, reEnabled bool, verdict qualitypkg.QualityVerdict, errProbe error) error {
	return p.probeSvc.HandleProbeFailure(authID, raw, response, reEnabled, verdict, errProbe)
}
