package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const (
	transparentSourceSession   = "session"
	transparentSourcePreferred = "preferred"
	transparentSourceStrategy  = "strategy"
)

type transparentSelection struct {
	plugin  *Service
	routing RoutingConfig

	sessionKey         string
	sessionAuthID      string
	preferredID        string
	snapshot           *routingSelectionSnapshot
	topLayerKey        string
	layerOrder         []string
	layerIndex         int
	currentLayerKey    string
	currentBuckets     []routingSelectionBucket
	currentBucketIndex int
	currentBucketKey   string
	currentLayer       []Account
	currentLayerLoaded bool
	specialsDone       bool
	normalStart        int
	normalOffset       int
	busyPass           bool
	tried              map[string]struct{}
	totalCandidates    int
	unknownAttempts    int
	unknownLimit       int
	currentLayerRank   int
}

type transparentCandidateAttempt struct {
	selection *transparentSelection
	Account   Account
	Raw       map[string]json.RawMessage
	Source    string
	released  bool
}

func (attempt *transparentCandidateAttempt) Release() {
	if attempt == nil || attempt.released {
		return
	}
	attempt.selection.plugin.selectionRuntime.ReleaseAttempt(attempt.Account.ID)
	attempt.released = true
}

func (s *Service) newTransparentSelection(req Request, body []byte, sourceFormat string) (*transparentSelection, error) {
	return s.newTransparentSelectionForModel(req, body, sourceFormat, executionModel(req, body))
}

func (s *Service) newTransparentSelectionForModel(req Request, body []byte, sourceFormat, model string) (*transparentSelection, error) {
	return s.newTransparentSelectionWithSession(s.selectionSessionKeyForModel(req, body, sourceFormat, model))
}

func (s *Service) newTransparentSelectionForFacts(req Request, facts Facts) (*transparentSelection, error) {
	sessionKey := ""
	if facts.SessionSeed != "" {
		sessionKey = s.selectionSessionKeyForSeed(req, facts.SourceFormat, facts.Model, facts.SessionSeed)
	}
	return s.newTransparentSelectionWithSession(sessionKey)
}

func (s *Service) newTransparentSelectionWithSession(sessionKey string) (*transparentSelection, error) {
	if errEnsure := s.ensureSelectionIndex(); errEnsure != nil {
		return nil, errEnsure
	}
	s.selectionRuntime.pruneStaleAttempts(time.Now().UTC(), 10*time.Minute)
	s.maybeRefreshSelectionIndex(time.Now().UTC())
	policy := normalizeRoutingPolicy(s.env.Policy())
	snapshot := s.selectionIndex.Load()
	if snapshot == nil {
		return nil, fmt.Errorf("selection index is not ready")
	}
	selection := &transparentSelection{
		plugin:          s,
		routing:         policy.Routing,
		snapshot:        snapshot,
		layerOrder:      snapshot.Order(),
		layerIndex:      0,
		normalStart:     -1,
		tried:           make(map[string]struct{}),
		totalCandidates: snapshot.Total(),
	}
	for _, key := range selection.layerOrder {
		if len(snapshot.Layer(key)) > 0 {
			selection.topLayerKey = key
			break
		}
	}
	// With verified accounts available, one unverified account per request is
	// enough exposure. An all-unknown pool keeps normal failover.
	if selection.topLayerKey != "" && snapshot.Rank(selection.topLayerKey) == 0 {
		selection.unknownLimit = 1
	}
	if selection.routing.Affinity.Enabled {
		selection.sessionKey = sessionKey
		if selection.sessionKey != "" {
			if authID, ok := s.selectionRuntime.LookupBinding(selection.sessionKey, time.Now().UTC()); ok {
				selection.sessionAuthID = authID
			}
		}
	}
	if selection.routing.Strategy.PreferLastSuccess {
		if authID, _ := s.selectionRuntime.Preferred(); authID != "" {
			layerKey, ok := snapshot.LayerForAuth(authID)
			if !ok || layerKey != selection.topLayerKey {
				s.selectionRuntime.ClearPreferred(authID)
			} else {
				selection.preferredID = authID
			}
		}
	}
	return selection, nil
}

// HasRemainingCandidates reports whether another currently eligible account
// can be tried without reusing one that already failed in this request.
func (selection *transparentSelection) HasRemainingCandidates() bool {
	if selection == nil || selection.snapshot == nil {
		return false
	}
	for _, key := range selection.layerOrder {
		for _, bucket := range selection.snapshot.Buckets(key) {
			for _, candidate := range bucket.Accounts {
				if _, tried := selection.tried[candidate.ID]; tried {
					continue
				}
				if selection.candidateEligible(candidate.ID) {
					return true
				}
			}
		}
	}
	return false
}

func (selection *transparentSelection) Next(ctx context.Context) (transparentCandidateAttempt, bool) {
	if selection == nil || selection.totalCandidates <= 0 {
		return transparentCandidateAttempt{}, false
	}
	for {
		if !selection.currentLayerLoaded {
			if selection.layerIndex >= len(selection.layerOrder) {
				return transparentCandidateAttempt{}, false
			}
			key := selection.layerOrder[selection.layerIndex]
			buckets := selection.snapshot.Buckets(key)
			if len(buckets) == 0 {
				selection.layerIndex++
				continue
			}
			selection.currentLayerKey = key
			selection.currentLayerRank = selection.snapshot.Rank(key)
			selection.currentBuckets = buckets
			selection.currentBucketIndex = 0
			selection.busyPass = false
			selection.loadCurrentBucket()
			selection.currentLayerLoaded = true
			selection.specialsDone = false
		}
		if !selection.specialsDone {
			selection.specialsDone = true
			if attempt, ok := selection.trySessionSpecial(ctx); ok {
				return attempt, true
			}
			if attempt, ok := selection.tryPreferredSpecial(ctx); ok {
				return attempt, true
			}
		}
		if attempt, ok := selection.tryNormalCandidate(ctx, selection.busyPass); ok {
			return attempt, true
		}
		selection.currentBucketIndex++
		if selection.currentBucketIndex < len(selection.currentBuckets) {
			selection.loadCurrentBucket()
			selection.specialsDone = false
			continue
		}
		if !selection.busyPass {
			selection.busyPass = true
			selection.currentBucketIndex = 0
			selection.loadCurrentBucket()
			selection.specialsDone = true
			continue
		}
		selection.currentLayerLoaded = false
		selection.layerIndex++
	}
}

func (selection *transparentSelection) loadCurrentBucket() {
	if selection == nil || selection.currentBucketIndex < 0 || selection.currentBucketIndex >= len(selection.currentBuckets) {
		selection.currentLayer = nil
		selection.currentBucketKey = ""
		return
	}
	bucket := selection.currentBuckets[selection.currentBucketIndex]
	selection.currentBucketKey = bucket.Key
	selection.currentLayer = bucket.Accounts
	selection.normalStart = -1
	selection.normalOffset = 0
}

func (selection *transparentSelection) tryNormalCandidate(ctx context.Context, busyPass bool) (transparentCandidateAttempt, bool) {
	if selection.normalStart < 0 {
		selection.normalStart = selection.plugin.selectionRuntime.StrategyStart(
			selection.currentBucketKey,
			selection.routing.Strategy.Selection,
			len(selection.currentLayer),
		)
	}
	for selection.normalOffset < len(selection.currentLayer) {
		position := (selection.normalStart + selection.normalOffset) % len(selection.currentLayer)
		selection.normalOffset++
		candidate := selection.currentLayer[position]
		if _, tried := selection.tried[candidate.ID]; tried {
			continue
		}
		if !selection.candidateEligible(candidate.ID) {
			selection.tried[candidate.ID] = struct{}{}
			selection.plugin.refreshSelectionAccount(candidate.ID)
			continue
		}
		if busyPass {
			selection.plugin.selectionRuntime.ReserveAttempt(candidate.ID)
		} else if !selection.plugin.selectionRuntime.TryReserveIdle(candidate.ID) {
			continue
		}
		account, raw, ok := selection.plugin.env.ExecutableAuth(ctx, candidate.ID)
		if !ok {
			selection.plugin.selectionRuntime.ReleaseAttempt(candidate.ID)
			selection.tried[candidate.ID] = struct{}{}
			selection.plugin.refreshSelectionAccount(candidate.ID)
			continue
		}
		if selection.currentLayerRank > 0 && selection.unknownLimit > 0 {
			if selection.unknownAttempts >= selection.unknownLimit {
				selection.tried[candidate.ID] = struct{}{}
				selection.plugin.selectionRuntime.ReleaseAttempt(candidate.ID)
				continue
			}
			selection.unknownAttempts++
		}
		attempt := transparentCandidateAttempt{
			selection: selection,
			Account:   account,
			Raw:       raw,
			Source:    transparentSourceStrategy,
		}
		selection.applySessionBinding(ctx, &attempt)
		selection.tried[attempt.Account.ID] = struct{}{}
		return attempt, true
	}
	return transparentCandidateAttempt{}, false
}

func (selection *transparentSelection) applySessionBinding(ctx context.Context, attempt *transparentCandidateAttempt) {
	if selection.sessionKey == "" || attempt == nil {
		return
	}
	boundID := selection.plugin.selectionRuntime.GetOrBind(
		selection.sessionKey,
		attempt.Account.ID,
		time.Now().UTC(),
		selection.routing.Affinity.TTL.Duration,
		selection.routing.Affinity.MaxEntries,
	)
	if boundID == "" || boundID == attempt.Account.ID {
		return
	}
	boundLayer, exists := selection.snapshot.LayerForAuth(boundID)
	if !exists || !selection.candidateEligible(boundID) {
		selection.plugin.selectionRuntime.DeleteIfBoundTo(selection.sessionKey, boundID)
		return
	}
	if boundLayer != selection.currentLayerKey {
		// Keep a valid lower-layer binding until this request falls back there
		return
	}
	if _, tried := selection.tried[boundID]; tried {
		return
	}
	boundAccount, boundRaw, executable := selection.plugin.env.ExecutableAuth(ctx, boundID)
	if !executable {
		selection.plugin.selectionRuntime.DeleteIfBoundTo(selection.sessionKey, boundID)
		return
	}
	selection.plugin.selectionRuntime.ReleaseAttempt(attempt.Account.ID)
	// Session affinity intentionally permits more than one in-flight request
	// to use the bound auth so a conversation is not split across accounts
	selection.plugin.selectionRuntime.ReserveAttempt(boundID)
	attempt.Account = boundAccount
	attempt.Raw = boundRaw
	attempt.Source = transparentSourceSession
}

func (selection *transparentSelection) trySessionSpecial(ctx context.Context) (transparentCandidateAttempt, bool) {
	if selection.sessionAuthID == "" {
		return transparentCandidateAttempt{}, false
	}
	layerKey, ok := selection.snapshot.LayerForAuth(selection.sessionAuthID)
	if !ok {
		selection.plugin.selectionRuntime.DeleteIfBoundTo(selection.sessionKey, selection.sessionAuthID)
		selection.sessionAuthID = ""
		return transparentCandidateAttempt{}, false
	}
	if layerKey != selection.currentLayerKey {
		return transparentCandidateAttempt{}, false
	}
	authID := selection.sessionAuthID
	if _, tried := selection.tried[authID]; tried {
		return transparentCandidateAttempt{}, false
	}
	selection.tried[authID] = struct{}{}
	if !selection.candidateEligible(authID) {
		selection.plugin.selectionRuntime.DeleteIfBoundTo(selection.sessionKey, authID)
		selection.sessionAuthID = ""
		selection.plugin.refreshSelectionAccount(authID)
		return transparentCandidateAttempt{}, false
	}
	account, raw, ok := selection.plugin.env.ExecutableAuth(ctx, authID)
	if !ok {
		selection.plugin.selectionRuntime.DeleteIfBoundTo(selection.sessionKey, authID)
		selection.sessionAuthID = ""
		selection.plugin.refreshSelectionAccount(authID)
		return transparentCandidateAttempt{}, false
	}
	selection.plugin.selectionRuntime.ReserveAttempt(account.ID)
	return transparentCandidateAttempt{
		selection: selection,
		Account:   account,
		Raw:       raw,
		Source:    transparentSourceSession,
	}, true
}

func (selection *transparentSelection) tryPreferredSpecial(ctx context.Context) (transparentCandidateAttempt, bool) {
	if selection.preferredID == "" || !selection.routing.Strategy.PreferLastSuccess {
		return transparentCandidateAttempt{}, false
	}
	if selection.currentLayerKey != selection.topLayerKey {
		return transparentCandidateAttempt{}, false
	}
	authID := selection.preferredID
	layerKey, ok := selection.snapshot.LayerForAuth(authID)
	if !ok || layerKey != selection.topLayerKey {
		selection.plugin.selectionRuntime.ClearPreferred(authID)
		selection.preferredID = ""
		return transparentCandidateAttempt{}, false
	}
	if _, tried := selection.tried[authID]; tried {
		return transparentCandidateAttempt{}, false
	}
	if !selection.preferredEligible(authID) {
		selection.plugin.selectionRuntime.ClearPreferred(authID)
		selection.preferredID = ""
		return transparentCandidateAttempt{}, false
	}
	if !selection.plugin.selectionRuntime.TryReservePreferred(authID) {
		return transparentCandidateAttempt{}, false
	}
	selection.tried[authID] = struct{}{}
	account, raw, ok := selection.plugin.env.ExecutableAuth(ctx, authID)
	if !ok {
		selection.plugin.selectionRuntime.ReleaseAttempt(authID)
		selection.plugin.selectionRuntime.ClearPreferred(authID)
		selection.preferredID = ""
		selection.plugin.refreshSelectionAccount(authID)
		return transparentCandidateAttempt{}, false
	}
	return transparentCandidateAttempt{
		selection: selection,
		Account:   account,
		Raw:       raw,
		Source:    transparentSourcePreferred,
	}, true
}

func (selection *transparentSelection) candidateEligible(authID string) bool {
	account, ok := selection.plugin.env.FindAccount(authID)
	if !ok {
		return false
	}
	state, okState := selection.plugin.env.StateAccount(authID)
	if !okState {
		return accountEligibleForSelection(account, StateUnknown)
	}
	return accountEligibleForSelection(account, state.State)
}

func (selection *transparentSelection) preferredEligible(authID string) bool {
	layerKey, ok := selection.snapshot.LayerForAuth(authID)
	if !ok || layerKey != selection.topLayerKey {
		return false
	}
	state, ok := selection.plugin.env.StateAccount(authID)
	return ok && state.State == StateHealthy && selection.candidateEligible(authID)
}

func (selection *transparentSelection) MarkSuccess(authID, source string) {
	if selection == nil || authID == "" {
		return
	}
	now := time.Now().UTC()
	if selection.routing.Affinity.Enabled && selection.sessionKey != "" {
		selection.plugin.selectionRuntime.RefreshBinding(
			selection.sessionKey,
			authID,
			now,
			selection.routing.Affinity.TTL.Duration,
			selection.routing.Affinity.MaxEntries,
		)
	}
	switch source {
	case transparentSourcePreferred:
		selection.plugin.selectionRuntime.RefreshPreferred(authID, now)
	case transparentSourceStrategy:
		if !selection.routing.Strategy.PreferLastSuccess {
			return
		}
		currentPreferred, _ := selection.plugin.selectionRuntime.Preferred()
		if currentPreferred == "" || currentPreferred == authID || !selection.preferredEligible(currentPreferred) {
			selection.plugin.selectionRuntime.SetPreferred(authID, now)
			selection.preferredID = authID
		}
	}
}

func (selection *transparentSelection) MarkFailure(authID, _ string, class string) {
	if selection == nil || authID == "" || !isTransparentSelectionHardFailure(class) {
		return
	}
	if selection.sessionKey != "" {
		selection.plugin.selectionRuntime.DeleteIfBoundTo(selection.sessionKey, authID)
		if selection.sessionAuthID == authID {
			selection.sessionAuthID = ""
		}
	}
	// A hard failure is authoritative even when another request has already
	// replaced this selection's local preferred snapshot.
	selection.plugin.selectionRuntime.ClearPreferred(authID)
	if selection.preferredID == authID {
		selection.preferredID = ""
	}
}

func isTransparentSelectionHardFailure(class string) bool {
	// Only authoritative account failures break stickiness. A single quality
	// miss, a rate limit or a transport blip must not move the whole
	// conversation to another account; selection re-checks eligibility and
	// drops the binding when the account really leaves the pool.
	switch class {
	case ClassCredentialInvalid, ClassQuotaExhausted:
		return true
	default:
		return false
	}
}

func (s *Service) transparentCandidates(ctx context.Context) ([]Account, error) {
	if errEnsure := s.ensureSelectionIndex(); errEnsure != nil {
		return nil, errEnsure
	}
	// Diagnostic/test helper only. Request execution uses the incremental selector.
	_ = ctx
	_ = s.rebuildSelectionIndex()
	candidates := make([]Account, 0, s.selectionIndex.Total())
	for _, key := range s.selectionIndex.Order() {
		candidates = append(candidates, s.selectionIndex.Layer(key)...)
	}
	return candidates, nil
}
