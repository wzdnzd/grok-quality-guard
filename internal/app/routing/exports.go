package routing

import (
	"context"
	"time"

	execapp "grok-quality-guard/internal/app/executor"
)

type Selection = transparentSelection
type CandidateAttempt = transparentCandidateAttempt
type SelectionRuntime = routingSelectionRuntime
type SelectionSnapshot = routingSelectionSnapshot

func NewSelectionRuntime() routingSelectionRuntime   { return newRoutingSelectionRuntime() }
func (s *Service) Runtime() *routingSelectionRuntime { return &s.selectionRuntime }
func (s *Service) Index() *routingSelectionIndex     { return &s.selectionIndex }
func (s *Service) EnsureIndex() error                { return s.ensureSelectionIndex() }
func (s *Service) RebuildIndex() error               { return s.rebuildSelectionIndex() }
func (s *Service) RefreshAccount(authID string)      { s.refreshSelectionAccount(authID) }
func (s *Service) IndexReady() bool                  { return s.selectionIndex.Ready() }
func (s *Service) IndexTotal() int                   { return s.selectionIndex.Total() }
func (s *Service) BuildGeneration() uint64           { return s.selectionIndex.BuildGeneration() }
func (s *Service) LayerForAuth(authID string) (string, bool) {
	return s.selectionIndex.LayerForAuth(authID)
}
func (s *Service) SessionKey(req Request, body []byte, sourceFormat string) string {
	return s.selectionSessionKey(req, body, sourceFormat)
}
func (s *Service) SessionKeyForModel(req Request, body []byte, sourceFormat, model string) string {
	return s.selectionSessionKeyForModel(req, body, sourceFormat, model)
}
func (s *Service) SessionKeyForSeed(req Request, sourceFormat, model, seed string) string {
	return s.selectionSessionKeyForSeed(req, sourceFormat, model, seed)
}
func (s *Service) PrepareRequest(req Request) (Facts, error) { return s.prepareTransparentRequest(req) }
func (s *Service) NewSelection(req Request, facts Facts) (execapp.Selection, error) {
	selection, errSelection := s.newTransparentSelectionForFacts(req, facts)
	if errSelection != nil {
		return nil, errSelection
	}
	return selectionAdapter{selection: selection}, nil
}
func (s *Service) Candidates(ctx context.Context) ([]Account, error) {
	return s.transparentCandidates(ctx)
}

type selectionAdapter struct{ selection *transparentSelection }

func (a selectionAdapter) Next(ctx context.Context) (execapp.Attempt, bool) {
	attempt, ok := a.selection.Next(ctx)
	if !ok {
		return execapp.Attempt{}, false
	}
	return execapp.Attempt{Account: attempt.Account, Raw: attempt.Raw, Source: attempt.Source, Release: attempt.Release}, true
}
func (a selectionAdapter) MarkSuccess(authID, source string) { a.selection.MarkSuccess(authID, source) }
func (a selectionAdapter) MarkFailure(authID, source, class string) {
	a.selection.MarkFailure(authID, source, class)
}
func (a selectionAdapter) TotalCandidates() int { return a.selection.totalCandidates }
func (a selectionAdapter) HasRemainingCandidates() bool {
	return a.selection.HasRemainingCandidates()
}

func (s *Service) NewTransparentSelection(req Request, body []byte, sourceFormat string) (*Selection, error) {
	return s.newTransparentSelection(req, body, sourceFormat)
}
func (s *Service) NewTransparentSelectionForFacts(req Request, facts Facts) (*Selection, error) {
	return s.newTransparentSelectionForFacts(req, facts)
}

type SelectionIndex = routingSelectionIndex

func (runtime *routingSelectionRuntime) PruneBindings(now time.Time) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.pruneBindingsLocked(now)
}
func (runtime *routingSelectionRuntime) BindingCount() int {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return len(runtime.bindings)
}
func IsTransparentSelectionHardFailure(class string) bool {
	return isTransparentSelectionHardFailure(class)
}

const (
	TransparentSourceSession   = transparentSourceSession
	TransparentSourcePreferred = transparentSourcePreferred
	TransparentSourceStrategy  = transparentSourceStrategy
)
