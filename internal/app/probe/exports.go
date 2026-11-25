package probe

import (
	"context"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"time"
)

type Scheduler = probeScheduler

func NewScheduler(service *Service, ctx context.Context) *Scheduler {
	return newProbeScheduler(service, ctx)
}
func (s *Scheduler) EnqueueDue(accounts []Account, state State, policy Policy, now time.Time) int {
	return s.enqueueDue(accounts, state, policy, now)
}
func (s *Scheduler) Dispatch() { s.dispatch() }

func (s *Scheduler) WaitIdle()                                      { s.waitIdle() }
func (s *Service) RunLoop(ctx context.Context)                      { s.probeLoop(ctx) }
func (s *Service) ProbeDue(authID string, force bool) (bool, error) { return s.probeDue(authID, force) }
func (s *Service) ProbeConcurrency() int                            { return s.probeConcurrency() }
func (s *Service) ProbeQueueSize() int                              { return s.probeQueueSize() }
func (s *Service) AcquireSlot(ctx context.Context, priority bool) error {
	return s.acquireProbeSlot(ctx, priority)
}
func (s *Service) ReleaseSlot() { s.releaseProbeSlot() }
func (s *Service) WakeWaiters() { s.wakeProbeWaiters() }
func (s *Service) PriorityWaiterCount() int {
	s.probeGate.Lock()
	defer s.probeGate.Unlock()
	return s.probePriorityWaiters
}
func ProbeDueForAccount(account *AccountState, okState bool, authID string, policy Policy, now time.Time) bool {
	return probeDueForAccount(account, okState, authID, policy, now)
}
func UnknownProbeJitter(authID string, max time.Duration) time.Duration {
	return unknownProbeJitter(authID, max)
}
func ProbeBody(cfg ProbeConfig, reasoning bool) []byte { return probeBody(cfg, reasoning) }
func NextProbeBackoff(cfg ProbeConfig, previous AccountState) time.Duration {
	return nextProbeBackoff(cfg, previous)
}
func MatchProbeMarker(text, expected string) bool { return matchProbeMarker(text, expected) }

func (s *Scheduler) QueueEntries() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queue...)
}

func (s *Service) MarkProbeSuccess(authID string, sig Signals) error {
	return s.markProbeSuccess(authID, sig)
}
func (s *Service) HandleProbeFailure(authID string, raw map[string]json.RawMessage, response pluginapi.HostAuthGetResponse, reEnabled bool, verdict QualityVerdict, errProbe error) error {
	return s.handleProbeFailure(authID, raw, response, reEnabled, verdict, errProbe)
}
