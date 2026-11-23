package routing

import (
	"container/heap"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	appstate "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/config"
)

type routingSelectionBucket struct {
	Key      string
	Accounts []Account
}

type routingSelectionLayer struct {
	Key         string
	QualityRank int
	Priority    int
	Accounts    []Account
	Buckets     []routingSelectionBucket
}

type routingSelectionSnapshot struct {
	layers          map[string]routingSelectionLayer
	layerByAuth     map[string]string
	bucketByAuth    map[string]string
	scoreByAuth     map[string]float64
	lastProbeByAuth map[string]time.Time
	order           []string
	total           int
	policy          config.Policy
}

type routingSelectionIndex struct {
	buildMu         sync.Mutex
	snapshot        atomic.Pointer[routingSelectionSnapshot]
	buildGeneration atomic.Uint64

	dirty           atomic.Bool
	policyInvalid   atomic.Bool
	policySeq       atomic.Uint64
	rebuildInFlight atomic.Bool
	driftBits       atomic.Uint64
	dirtySeq        atomic.Uint64
	lastRebuildAt   atomic.Int64

	activityMu      sync.Mutex
	activityCount   uint64
	activityStarted time.Time
	activityUpdated time.Time
	activityRate    float64
}

type bindingTimeEntry struct {
	sessionKey string
	at         time.Time
}

type bindingTimeHeap []bindingTimeEntry

func (h bindingTimeHeap) Len() int { return len(h) }
func (h bindingTimeHeap) Less(i, j int) bool {
	if h[i].at.Equal(h[j].at) {
		return h[i].sessionKey < h[j].sessionKey
	}
	return h[i].at.Before(h[j].at)
}
func (h bindingTimeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *bindingTimeHeap) Push(value any) {
	*h = append(*h, value.(bindingTimeEntry))
}
func (h *bindingTimeHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

type routingSelectionRuntime struct {
	mu                 sync.Mutex
	cursors            map[string]int
	activeAttempts     map[string]int
	preferredAuthID    string
	preferredSuccessAt time.Time
	bindings           map[string]routingSessionBinding
	bindingExpiry      bindingTimeHeap
	bindingAge         bindingTimeHeap
}

type routingSessionBinding struct {
	AuthID    string
	ExpiresAt time.Time
	BoundAt   time.Time
}

func newRoutingSelectionRuntime() routingSelectionRuntime {
	return routingSelectionRuntime{
		cursors:        make(map[string]int),
		activeAttempts: make(map[string]int),
		bindings:       make(map[string]routingSessionBinding),
	}
}

func selectionLayerForState(state string, priority int) (string, int, bool) {
	switch state {
	case "", StateUnknown:
		return fmt.Sprintf("1:%d", priority), 1, true
	case StateHealthy:
		return fmt.Sprintf("0:%d", priority), 0, true
	default:
		return "", 0, false
	}
}

func selectionStateForAccount(states map[string]string, account Account) string {
	if value, ok := states[account.ID]; ok {
		return value
	}
	authIndex := strings.TrimSpace(account.AuthIndex)
	if authIndex != "" && authIndex != account.ID {
		if value, ok := states[authIndex]; ok {
			return value
		}
	}
	return ""
}

func accountEligibleForSelection(account Account, state string) bool {
	if account.Disabled || account.Unavailable {
		return false
	}
	if account.Provider != "" && !isGrokProvider(account.Provider) && !isGrokProvider(account.Type) {
		return false
	}
	return isTransparentCandidateState(state)
}

func (index *routingSelectionIndex) Load() *routingSelectionSnapshot {
	if index == nil {
		return nil
	}
	return index.snapshot.Load()
}

func (index *routingSelectionIndex) Ready() bool {
	return index.Load() != nil
}

func (index *routingSelectionIndex) BuildGeneration() uint64 {
	if index == nil {
		return 0
	}
	return index.buildGeneration.Load()
}

func (snapshot *routingSelectionSnapshot) Order() []string {
	if snapshot == nil {
		return nil
	}
	return snapshot.order
}

func (snapshot *routingSelectionSnapshot) Layer(key string) []Account {
	if snapshot == nil {
		return nil
	}
	layer := snapshot.layers[key]
	return layer.Accounts
}

func (snapshot *routingSelectionSnapshot) Buckets(key string) []routingSelectionBucket {
	if snapshot == nil {
		return nil
	}
	return snapshot.layers[key].Buckets
}

func (snapshot *routingSelectionSnapshot) ScoreForAuth(authID string) (float64, bool) {
	if snapshot == nil {
		return 0, false
	}
	score, ok := snapshot.scoreByAuth[authID]
	return score, ok
}

func (snapshot *routingSelectionSnapshot) LayerForAuth(authID string) (string, bool) {
	if snapshot == nil {
		return "", false
	}
	key, ok := snapshot.layerByAuth[authID]
	return key, ok
}

func (snapshot *routingSelectionSnapshot) BucketForAuth(authID string) (string, bool) {
	if snapshot == nil {
		return "", false
	}
	key, ok := snapshot.bucketByAuth[authID]
	return key, ok
}

func (snapshot *routingSelectionSnapshot) Total() int {
	if snapshot == nil {
		return 0
	}
	return snapshot.total
}

func (index *routingSelectionIndex) Order() []string {
	return index.Load().Order()
}

func (index *routingSelectionIndex) Layer(key string) []Account {
	return index.Load().Layer(key)
}

func (index *routingSelectionIndex) LayerForAuth(authID string) (string, bool) {
	return index.Load().LayerForAuth(authID)
}

func (index *routingSelectionIndex) Total() int {
	return index.Load().Total()
}

// NeedsUpsert reports whether a state change can move the account between
// selection layers. Observation-only updates keep the same layer and must not
// copy the entire index.
func (index *routingSelectionIndex) NeedsUpsert(account Account, state string) bool {
	current := index.Load()
	if current == nil {
		return true
	}
	oldKey, oldEligible := current.layerByAuth[account.ID]
	stateValue := state
	if stateValue == "" {
		stateValue = StateUnknown
	}
	if !accountEligibleForSelection(account, stateValue) {
		return oldEligible
	}
	key, _, ok := selectionLayerForState(stateValue, account.Priority)
	if !ok {
		return oldEligible
	}
	return !oldEligible || oldKey != key
}

func (index *routingSelectionIndex) Build(accounts []Account, states map[string]string) {
	index.BuildWithPolicy(accounts, states, nil, config.PolicyFromConfig(config.DefaultConfig()))
}

func (index *routingSelectionIndex) BuildWithPolicy(accounts []Account, states map[string]string, routingStats map[string]appstate.RoutingStats, policy config.Policy) {
	policy = normalizeRoutingPolicy(policy)
	now := time.Now().UTC()
	groups := make(map[string][]Account)
	meta := make(map[string]routingSelectionLayer)
	scoreByAuth := make(map[string]float64, len(accounts))
	lastProbeByAuth := make(map[string]time.Time, len(accounts))
	for _, account := range accounts {
		state := StateUnknown
		if value := selectionStateForAccount(states, account); value != "" {
			state = value
		}
		if !accountEligibleForSelection(account, state) {
			continue
		}
		key, rank, ok := selectionLayerForState(state, account.Priority)
		if !ok {
			continue
		}
		if _, exists := meta[key]; !exists {
			meta[key] = routingSelectionLayer{Key: key, QualityRank: rank, Priority: account.Priority}
		}
		groups[key] = append(groups[key], account)
		if rank == 0 && policy.Routing.Scoring.Enabled {
			stats := selectionRoutingStats(routingStats, account)
			scoreByAuth[account.ID] = accountRoutingScore(account, stats, policy, now)
			lastProbeByAuth[account.ID] = routingLatestProbe(stats)
		}
	}

	layers := make(map[string]routingSelectionLayer, len(groups))
	layerByAuth := make(map[string]string, len(accounts))
	bucketByAuth := make(map[string]string, len(accounts))
	for key, values := range groups {
		layer := meta[key]
		if layer.QualityRank == 0 && policy.Routing.Scoring.Enabled {
			layer.Buckets = buildScoredBuckets(key, values, scoreByAuth, lastProbeByAuth, policy.Routing.Scoring.MaxAccountsPerBucket)
		} else {
			layer.Buckets = buildIDBuckets(key, values, policy.Routing.Scoring.MaxAccountsPerBucket)
		}
		layer.Accounts = flattenSelectionBuckets(layer.Buckets)
		layers[key] = layer
		for _, bucket := range layer.Buckets {
			for _, account := range bucket.Accounts {
				layerByAuth[account.ID] = key
				bucketByAuth[account.ID] = bucket.Key
			}
		}
	}
	order, total := buildSelectionOrder(layers)
	index.snapshot.Store(&routingSelectionSnapshot{
		layers:          layers,
		layerByAuth:     layerByAuth,
		bucketByAuth:    bucketByAuth,
		scoreByAuth:     scoreByAuth,
		lastProbeByAuth: lastProbeByAuth,
		order:           order,
		total:           total,
		policy:          policy,
	})
	index.buildGeneration.Add(1)
	index.lastRebuildAt.Store(now.UnixNano())
	index.driftBits.Store(0)
	index.dirty.Store(false)
	index.policyInvalid.Store(false)
}

func selectionRoutingStats(values map[string]appstate.RoutingStats, account Account) appstate.RoutingStats {
	if len(values) == 0 {
		return appstate.RoutingStats{}
	}
	if value, ok := values[account.ID]; ok {
		return value
	}
	authIndex := strings.TrimSpace(account.AuthIndex)
	if authIndex != "" && authIndex != account.ID {
		if value, ok := values[authIndex]; ok {
			return value
		}
	}
	return appstate.RoutingStats{}
}

// Upsert publishes a copy-on-write snapshot and must be called while buildMu is held
func (index *routingSelectionIndex) Upsert(account Account, state string) {
	index.upsert(account, state, routingNeutralScore, time.Time{})
}

func (index *routingSelectionIndex) upsert(account Account, state string, score float64, lastProbe time.Time) {
	current := index.Load()
	if current == nil {
		return
	}
	policy := current.policy
	layers := make(map[string]routingSelectionLayer, len(current.layers)+1)
	for key, layer := range current.layers {
		layers[key] = layer
	}
	layerByAuth := make(map[string]string, len(current.layerByAuth)+1)
	for authID, key := range current.layerByAuth {
		layerByAuth[authID] = key
	}
	bucketByAuth := make(map[string]string, len(current.bucketByAuth)+1)
	for authID, key := range current.bucketByAuth {
		bucketByAuth[authID] = key
	}
	scoreByAuth := make(map[string]float64, len(current.scoreByAuth)+1)
	for authID, value := range current.scoreByAuth {
		scoreByAuth[authID] = value
	}
	lastProbeByAuth := make(map[string]time.Time, len(current.lastProbeByAuth)+1)
	for authID, value := range current.lastProbeByAuth {
		lastProbeByAuth[authID] = value
	}
	if oldKey, ok := layerByAuth[account.ID]; ok {
		delete(layerByAuth, account.ID)
		delete(bucketByAuth, account.ID)
		layer := layers[oldKey]
		accounts := removeAccountFromLayer(layer.Accounts, account.ID)
		if len(accounts) == 0 {
			delete(layers, oldKey)
		} else {
			layer.Buckets = rebuildLayerBuckets(layer, accounts, scoreByAuth, lastProbeByAuth, policy)
			layer.Accounts = flattenSelectionBuckets(layer.Buckets)
			layers[oldKey] = layer
		}
	}
	delete(scoreByAuth, account.ID)
	delete(lastProbeByAuth, account.ID)
	stateValue := state
	if stateValue == "" {
		stateValue = StateUnknown
	}
	if accountEligibleForSelection(account, stateValue) {
		key, rank, ok := selectionLayerForState(stateValue, account.Priority)
		if ok {
			layer := layers[key]
			layer.Key = key
			layer.QualityRank = rank
			layer.Priority = account.Priority
			accounts := appendAccountUnlessPresent(layer.Accounts, account)
			if rank == 0 && policy.Routing.Scoring.Enabled {
				scoreByAuth[account.ID] = score
				lastProbeByAuth[account.ID] = lastProbe
			}
			layer.Buckets = rebuildLayerBuckets(layer, accounts, scoreByAuth, lastProbeByAuth, policy)
			layer.Accounts = flattenSelectionBuckets(layer.Buckets)
			layers[key] = layer
			layerByAuth[account.ID] = key
			if bucketKey, ok := bucketKeyForAccount(layer.Buckets, account.ID); ok {
				bucketByAuth[account.ID] = bucketKey
			}
		}
	}
	order, total := buildSelectionOrder(layers)
	index.snapshot.Store(&routingSelectionSnapshot{
		layers:          layers,
		layerByAuth:     layerByAuth,
		bucketByAuth:    bucketByAuth,
		scoreByAuth:     scoreByAuth,
		lastProbeByAuth: lastProbeByAuth,
		order:           order,
		total:           total,
		policy:          policy,
	})
}

// Remove publishes a copy-on-write snapshot and must be called while buildMu is held
func (index *routingSelectionIndex) Remove(authID string) {
	current := index.Load()
	if current == nil {
		return
	}
	oldKey, ok := current.layerByAuth[authID]
	if !ok {
		return
	}
	layers := make(map[string]routingSelectionLayer, len(current.layers))
	for key, layer := range current.layers {
		layers[key] = layer
	}
	layerByAuth := make(map[string]string, len(current.layerByAuth))
	for id, key := range current.layerByAuth {
		if id != authID {
			layerByAuth[id] = key
		}
	}
	bucketByAuth := make(map[string]string, len(current.bucketByAuth))
	for id, key := range current.bucketByAuth {
		if id != authID {
			bucketByAuth[id] = key
		}
	}
	scoreByAuth := make(map[string]float64, len(current.scoreByAuth))
	for id, score := range current.scoreByAuth {
		if id != authID {
			scoreByAuth[id] = score
		}
	}
	lastProbeByAuth := make(map[string]time.Time, len(current.lastProbeByAuth))
	for id, lastProbe := range current.lastProbeByAuth {
		if id != authID {
			lastProbeByAuth[id] = lastProbe
		}
	}
	layer := layers[oldKey]
	accounts := removeAccountFromLayer(layer.Accounts, authID)
	if len(accounts) == 0 {
		delete(layers, oldKey)
	} else {
		layer.Buckets = rebuildLayerBuckets(layer, accounts, scoreByAuth, lastProbeByAuth, current.policy)
		layer.Accounts = flattenSelectionBuckets(layer.Buckets)
		layers[oldKey] = layer
	}
	order, total := buildSelectionOrder(layers)
	index.snapshot.Store(&routingSelectionSnapshot{
		layers:          layers,
		layerByAuth:     layerByAuth,
		bucketByAuth:    bucketByAuth,
		scoreByAuth:     scoreByAuth,
		lastProbeByAuth: lastProbeByAuth,
		order:           order,
		total:           total,
		policy:          current.policy,
	})
}

func removeAccountFromLayer(accounts []Account, authID string) []Account {
	remaining := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if account.ID != authID {
			remaining = append(remaining, account)
		}
	}
	return remaining
}

func appendAccountUnlessPresent(accounts []Account, account Account) []Account {
	updated := make([]Account, 0, len(accounts)+1)
	for _, existing := range accounts {
		if existing.ID != account.ID {
			updated = append(updated, existing)
		}
	}
	updated = append(updated, account)
	return updated
}

func rebuildLayerBuckets(layer routingSelectionLayer, accounts []Account, scores map[string]float64, lastProbe map[string]time.Time, policy config.Policy) []routingSelectionBucket {
	if layer.QualityRank == 0 && policy.Routing.Scoring.Enabled {
		return buildScoredBuckets(layer.Key, accounts, scores, lastProbe, policy.Routing.Scoring.MaxAccountsPerBucket)
	}
	return buildIDBuckets(layer.Key, accounts, policy.Routing.Scoring.MaxAccountsPerBucket)
}

func bucketKeyForAccount(buckets []routingSelectionBucket, authID string) (string, bool) {
	for _, bucket := range buckets {
		for _, account := range bucket.Accounts {
			if account.ID == authID {
				return bucket.Key, true
			}
		}
	}
	return "", false
}

func buildSelectionOrder(layers map[string]routingSelectionLayer) ([]string, int) {
	ordered := make([]routingSelectionLayer, 0, len(layers))
	total := 0
	for _, layer := range layers {
		if len(layer.Accounts) == 0 {
			continue
		}
		ordered = append(ordered, layer)
		total += len(layer.Accounts)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].QualityRank != ordered[j].QualityRank {
			return ordered[i].QualityRank < ordered[j].QualityRank
		}
		return ordered[i].Priority > ordered[j].Priority
	})
	order := make([]string, 0, len(ordered))
	for _, layer := range ordered {
		order = append(order, layer.Key)
	}
	return order, total
}

func (s *Service) rebuildSelectionIndex() error {
	if s == nil {
		return nil
	}
	s.selectionIndex.buildMu.Lock()
	defer s.selectionIndex.buildMu.Unlock()
	seqBefore := s.selectionIndex.dirtySeq.Load()
	policySeqBefore := s.selectionIndex.policySeq.Load()
	store := s.env.Store()
	var states map[string]string
	var stats map[string]appstate.RoutingStats
	if store != nil {
		states = store.AccountStateValues()
		stats = store.RoutingStatsValues()
	}
	policy := s.env.Policy()
	s.selectionIndex.BuildWithPolicy(s.env.CatalogSnapshot(), states, stats, policy)
	if s.selectionIndex.policySeq.Load() != policySeqBefore {
		s.selectionIndex.policyInvalid.Store(true)
		s.selectionIndex.dirty.Store(true)
	}
	if s.selectionIndex.dirtySeq.Load() != seqBefore {
		s.selectionIndex.dirty.Store(true)
		s.selectionIndex.addDrift(policy.Routing.Scoring.Refresh.DriftThreshold)
	}
	return nil
}

func (s *Service) ensureSelectionIndex() error {
	if s == nil {
		return nil
	}
	if s.selectionIndex.Ready() && !s.selectionIndex.policyInvalid.Load() {
		return nil
	}
	return s.rebuildSelectionIndex()
}

func (s *Service) refreshSelectionAccount(authID string) {
	if s == nil || authID == "" {
		return
	}
	index := &s.selectionIndex
	current := index.Load()
	if current == nil {
		return
	}
	account, ok := s.env.FindAccount(authID)
	if !ok {
		index.buildMu.Lock()
		index.Remove(authID)
		index.buildMu.Unlock()
		return
	}
	state, okState := s.env.StateAccount(account.ID)
	if !okState && account.AuthIndex != "" {
		state, _ = s.env.StateAccount(account.AuthIndex)
	}
	stateValue := state.State
	if index.NeedsUpsert(account, stateValue) {
		policy := normalizeRoutingPolicy(s.env.Policy())
		stats := s.routingStatsForAccount(account)
		score := accountRoutingScore(account, stats, policy, time.Now().UTC())
		lastProbe := routingLatestProbe(stats)
		index.buildMu.Lock()
		if current := index.Load(); current != nil && index.NeedsUpsert(account, stateValue) {
			index.upsert(account, stateValue, score, lastProbe)
		}
		index.buildMu.Unlock()
		return
	}
	policy := normalizeRoutingPolicy(s.env.Policy())
	if !policy.Routing.Scoring.Enabled {
		return
	}
	stats := s.routingStatsForAccount(account)
	score := accountRoutingScore(account, stats, policy, time.Now().UTC())
	if oldScore, ok := current.ScoreForAuth(account.ID); ok && math.Abs(oldScore-score) > 1e-9 {
		index.markScoreChange(math.Abs(oldScore - score))
	}
}

func (s *Service) routingStatsForAccount(account Account) appstate.RoutingStats {
	store := s.env.Store()
	if store == nil {
		return appstate.RoutingStats{}
	}
	if stats := store.RoutingStatsValue(account.ID); stats != (appstate.RoutingStats{}) {
		return stats
	}
	if account.AuthIndex != "" && account.AuthIndex != account.ID {
		return store.RoutingStatsValue(account.AuthIndex)
	}
	return appstate.RoutingStats{}
}

func (index *routingSelectionIndex) markScoreChange(drift float64) {
	if drift < 0 {
		drift = 0
	}
	index.dirtySeq.Add(1)
	index.dirty.Store(true)
	index.addDrift(drift)
}

func (index *routingSelectionIndex) addDrift(delta float64) {
	if index == nil || delta <= 0 {
		return
	}
	for {
		oldBits := index.driftBits.Load()
		oldValue := math.Float64frombits(oldBits)
		if math.IsNaN(oldValue) || math.IsInf(oldValue, 0) {
			oldValue = 0
		}
		if index.driftBits.CompareAndSwap(oldBits, math.Float64bits(oldValue+delta)) {
			return
		}
	}
}

func (index *routingSelectionIndex) InvalidatePolicy() {
	if index == nil {
		return
	}
	index.policySeq.Add(1)
	index.policyInvalid.Store(true)
	index.dirty.Store(true)
}

func (s *Service) maybeRefreshSelectionIndex(now time.Time) {
	if s == nil {
		return
	}
	index := &s.selectionIndex
	snapshot := index.Load()
	if snapshot == nil || !snapshot.policy.Routing.Scoring.Enabled {
		return
	}
	s.noteSelectionRequest(now)
	lastNanos := index.lastRebuildAt.Load()
	if lastNanos <= 0 {
		return
	}
	age := now.Sub(time.Unix(0, lastNanos))
	minInterval := index.adaptiveRefreshInterval(snapshot.policy.Routing.Scoring.Refresh, now)
	maxAge := snapshot.policy.Routing.Scoring.Refresh.MaxAge.Duration
	drift := math.Float64frombits(index.driftBits.Load())
	threshold := snapshot.policy.Routing.Scoring.Refresh.DriftThreshold
	if !selectionRefreshDue(age, index.dirty.Load(), drift, threshold, minInterval, maxAge) {
		return
	}
	if !index.rebuildInFlight.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer index.rebuildInFlight.Store(false)
		_ = s.rebuildSelectionIndex()
	}()
}

func selectionRefreshDue(age time.Duration, dirty bool, drift, threshold float64, minInterval, maxAge time.Duration) bool {
	if age < minInterval {
		return false
	}
	if age >= maxAge {
		return true
	}
	return dirty && drift >= threshold
}

func (s *Service) noteSelectionRequest(now time.Time) {
	index := &s.selectionIndex
	index.activityMu.Lock()
	defer index.activityMu.Unlock()
	if index.activityStarted.IsZero() {
		index.activityStarted = now
	}
	index.activityCount++
	elapsed := now.Sub(index.activityStarted)
	if elapsed < 2*time.Second {
		return
	}
	rate := float64(index.activityCount) / math.Max(elapsed.Seconds(), 0.001)
	if index.activityUpdated.IsZero() {
		index.activityRate = rate
	} else {
		index.activityRate = index.activityRate*0.75 + rate*0.25
	}
	index.activityCount = 0
	index.activityStarted = now
	index.activityUpdated = now
}

func (index *routingSelectionIndex) adaptiveRefreshInterval(refresh config.RoutingScoringRefreshConfig, now time.Time) time.Duration {
	minInterval := refresh.MinInterval.Duration
	maxAge := refresh.MaxAge.Duration
	if maxAge <= minInterval {
		return minInterval
	}
	index.activityMu.Lock()
	rate := index.activityRate
	updated := index.activityUpdated
	started := index.activityStarted
	count := index.activityCount
	index.activityMu.Unlock()
	if !updated.IsZero() {
		idle := now.Sub(updated)
		if idle > 0 {
			rate *= math.Exp(-idle.Seconds() / 60)
		}
	}
	if !started.IsZero() && count > 0 && now.After(started) {
		intervalRate := float64(count) / math.Max(now.Sub(started).Seconds(), 0.001)
		if intervalRate > rate {
			rate = intervalRate
		}
	}
	fraction := 1 / (1 + math.Max(0, rate)*120)
	interval := minInterval + time.Duration(float64(maxAge-minInterval)*fraction)
	if interval < minInterval {
		return minInterval
	}
	if interval > maxAge {
		return maxAge
	}
	return interval
}

func (runtime *routingSelectionRuntime) StrategyStart(layerKey, strategy string, count int) int {
	if count <= 0 {
		return 0
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	switch strategy {
	case "random":
		return rand.IntN(count)
	case "fill_first":
		return 0
	default:
		start := runtime.cursors[layerKey] % count
		runtime.cursors[layerKey] = (start + 1) % count
		return start
	}
}

func (runtime *routingSelectionRuntime) ReserveAttempt(authID string) {
	if authID == "" {
		return
	}
	runtime.mu.Lock()
	runtime.activeAttempts[authID]++
	runtime.mu.Unlock()
}

func (runtime *routingSelectionRuntime) TryReserveIdle(authID string) bool {
	if authID == "" {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.activeAttempts[authID] > 0 {
		return false
	}
	runtime.activeAttempts[authID] = 1
	return true
}

func (runtime *routingSelectionRuntime) ReleaseAttempt(authID string) {
	if authID == "" {
		return
	}
	runtime.mu.Lock()
	if runtime.activeAttempts[authID] <= 1 {
		delete(runtime.activeAttempts, authID)
	} else {
		runtime.activeAttempts[authID]--
	}
	runtime.mu.Unlock()
}

func (runtime *routingSelectionRuntime) TryReservePreferred(authID string) bool {
	return runtime.TryReserveIdle(authID)
}

func (runtime *routingSelectionRuntime) Preferred() (string, time.Time) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.preferredAuthID, runtime.preferredSuccessAt
}

func (runtime *routingSelectionRuntime) SetPreferred(authID string, now time.Time) {
	runtime.mu.Lock()
	runtime.preferredAuthID = authID
	runtime.preferredSuccessAt = now
	runtime.mu.Unlock()
}

func (runtime *routingSelectionRuntime) RefreshPreferred(authID string, now time.Time) {
	runtime.mu.Lock()
	if runtime.preferredAuthID == authID {
		runtime.preferredSuccessAt = now
	}
	runtime.mu.Unlock()
}

func (runtime *routingSelectionRuntime) ClearPreferred(authID string) {
	runtime.mu.Lock()
	if runtime.preferredAuthID == authID {
		runtime.preferredAuthID = ""
		runtime.preferredSuccessAt = time.Time{}
	}
	runtime.mu.Unlock()
}

func (runtime *routingSelectionRuntime) LookupBinding(sessionKey string, now time.Time) (string, bool) {
	if sessionKey == "" {
		return "", false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	binding, ok := runtime.bindings[sessionKey]
	if !ok {
		return "", false
	}
	if !now.Before(binding.ExpiresAt) {
		delete(runtime.bindings, sessionKey)
		return "", false
	}
	return binding.AuthID, true
}

func (runtime *routingSelectionRuntime) GetOrBind(sessionKey, proposedAuthID string, now time.Time, ttl time.Duration, maxEntries int) string {
	if sessionKey == "" || proposedAuthID == "" {
		return proposedAuthID
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.pruneBindingsLocked(now)
	if binding, ok := runtime.bindings[sessionKey]; ok && now.Before(binding.ExpiresAt) {
		return binding.AuthID
	}
	runtime.trimBindingsLocked(maxEntries, true)
	runtime.setBindingLocked(sessionKey, routingSessionBinding{
		AuthID:    proposedAuthID,
		ExpiresAt: now.Add(ttl),
		BoundAt:   now,
	})
	return proposedAuthID
}

func (runtime *routingSelectionRuntime) RefreshBinding(sessionKey, authID string, now time.Time, ttl time.Duration, maxEntries int) {
	if sessionKey == "" || authID == "" {
		return
	}
	runtime.mu.Lock()
	runtime.pruneBindingsLocked(now)
	// Remove the old entry before enforcing the cap so refreshing an existing
	// binding at capacity cannot evict an unrelated session.
	delete(runtime.bindings, sessionKey)
	runtime.trimBindingsLocked(maxEntries, true)
	runtime.setBindingLocked(sessionKey, routingSessionBinding{
		AuthID:    authID,
		ExpiresAt: now.Add(ttl),
		BoundAt:   now,
	})
	runtime.mu.Unlock()
}

func (runtime *routingSelectionRuntime) DeleteIfBoundTo(sessionKey, authID string) {
	if sessionKey == "" || authID == "" {
		return
	}
	runtime.mu.Lock()
	if binding, ok := runtime.bindings[sessionKey]; ok && binding.AuthID == authID {
		delete(runtime.bindings, sessionKey)
	}
	runtime.mu.Unlock()
}

func (runtime *routingSelectionRuntime) setBindingLocked(sessionKey string, binding routingSessionBinding) {
	runtime.bindings[sessionKey] = binding
	heap.Push(&runtime.bindingExpiry, bindingTimeEntry{sessionKey: sessionKey, at: binding.ExpiresAt})
	heap.Push(&runtime.bindingAge, bindingTimeEntry{sessionKey: sessionKey, at: binding.BoundAt})
}

func (runtime *routingSelectionRuntime) pruneBindingsLocked(now time.Time) {
	for runtime.bindingExpiry.Len() > 0 {
		entry := runtime.bindingExpiry[0]
		if entry.at.After(now) {
			break
		}
		heap.Pop(&runtime.bindingExpiry)
		binding, ok := runtime.bindings[entry.sessionKey]
		if ok && binding.ExpiresAt.Equal(entry.at) && !now.Before(binding.ExpiresAt) {
			delete(runtime.bindings, entry.sessionKey)
		}
	}
	if runtime.bindingExpiry.Len() > len(runtime.bindings)*2+64 {
		runtime.compactBindingHeapsLocked()
	}
}

func (runtime *routingSelectionRuntime) compactBindingHeapsLocked() {
	runtime.bindingExpiry = runtime.bindingExpiry[:0]
	runtime.bindingAge = runtime.bindingAge[:0]
	for sessionKey, binding := range runtime.bindings {
		heap.Push(&runtime.bindingExpiry, bindingTimeEntry{sessionKey: sessionKey, at: binding.ExpiresAt})
		heap.Push(&runtime.bindingAge, bindingTimeEntry{sessionKey: sessionKey, at: binding.BoundAt})
	}
}

func (runtime *routingSelectionRuntime) trimBindingsLocked(maxEntries int, reserveSlot bool) {
	if maxEntries <= 0 {
		return
	}
	limit := maxEntries
	if reserveSlot {
		limit--
	}
	if limit < 0 {
		limit = 0
	}
	for len(runtime.bindings) > limit {
		if !runtime.evictOldestBindingLocked() {
			break
		}
	}
}

func (runtime *routingSelectionRuntime) evictOldestBindingLocked() bool {
	for runtime.bindingAge.Len() > 0 {
		entry := heap.Pop(&runtime.bindingAge).(bindingTimeEntry)
		binding, ok := runtime.bindings[entry.sessionKey]
		if ok && binding.BoundAt.Equal(entry.at) {
			delete(runtime.bindings, entry.sessionKey)
			return true
		}
	}
	// The heaps can only lag the map after lazy deletions. Rebuild once if a
	// caller still needs to enforce the size limit.
	if len(runtime.bindings) == 0 {
		return false
	}
	runtime.compactBindingHeapsLocked()
	for runtime.bindingAge.Len() > 0 {
		entry := heap.Pop(&runtime.bindingAge).(bindingTimeEntry)
		if _, ok := runtime.bindings[entry.sessionKey]; ok {
			delete(runtime.bindings, entry.sessionKey)
			return true
		}
	}
	return false
}
