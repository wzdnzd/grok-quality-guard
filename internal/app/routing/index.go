package routing

import (
	"container/heap"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type routingSelectionLayer struct {
	Key         string
	QualityRank int
	Priority    int
	Accounts    []Account
}

type routingSelectionSnapshot struct {
	layers      map[string]routingSelectionLayer
	layerByAuth map[string]string
	order       []string
	total       int
}

type routingSelectionIndex struct {
	buildMu         sync.Mutex
	snapshot        atomic.Pointer[routingSelectionSnapshot]
	buildGeneration atomic.Uint64
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

func (snapshot *routingSelectionSnapshot) LayerForAuth(authID string) (string, bool) {
	if snapshot == nil {
		return "", false
	}
	key, ok := snapshot.layerByAuth[authID]
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
	groups := make(map[string][]Account)
	meta := make(map[string]routingSelectionLayer)
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
	}

	layers := make(map[string]routingSelectionLayer, len(groups))
	layerByAuth := make(map[string]string, len(accounts))
	for key, values := range groups {
		sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
		layer := meta[key]
		layer.Accounts = values
		layers[key] = layer
		for _, account := range values {
			layerByAuth[account.ID] = key
		}
	}
	order, total := buildSelectionOrder(layers)
	index.snapshot.Store(&routingSelectionSnapshot{
		layers:      layers,
		layerByAuth: layerByAuth,
		order:       order,
		total:       total,
	})
	index.buildGeneration.Add(1)
}

// Upsert publishes a copy-on-write snapshot and must be called while buildMu is held
func (index *routingSelectionIndex) Upsert(account Account, state string) {
	current := index.Load()
	if current == nil {
		return
	}
	layers := make(map[string]routingSelectionLayer, len(current.layers)+1)
	for key, layer := range current.layers {
		layers[key] = layer
	}
	layerByAuth := make(map[string]string, len(current.layerByAuth)+1)
	for authID, key := range current.layerByAuth {
		layerByAuth[authID] = key
	}
	if oldKey, ok := layerByAuth[account.ID]; ok {
		delete(layerByAuth, account.ID)
		layer := layers[oldKey]
		accounts := removeAccountFromLayer(layer.Accounts, account.ID)
		if len(accounts) == 0 {
			delete(layers, oldKey)
		} else {
			layer.Accounts = accounts
			layers[oldKey] = layer
		}
	}
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
			layer.Accounts = insertAccountSorted(layer.Accounts, account)
			layers[key] = layer
			layerByAuth[account.ID] = key
		}
	}
	order, total := buildSelectionOrder(layers)
	index.snapshot.Store(&routingSelectionSnapshot{
		layers:      layers,
		layerByAuth: layerByAuth,
		order:       order,
		total:       total,
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
	layer := layers[oldKey]
	accounts := removeAccountFromLayer(layer.Accounts, authID)
	if len(accounts) == 0 {
		delete(layers, oldKey)
	} else {
		layer.Accounts = accounts
		layers[oldKey] = layer
	}
	order, total := buildSelectionOrder(layers)
	index.snapshot.Store(&routingSelectionSnapshot{
		layers:      layers,
		layerByAuth: layerByAuth,
		order:       order,
		total:       total,
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

func insertAccountSorted(accounts []Account, account Account) []Account {
	position := sort.Search(len(accounts), func(index int) bool {
		return accounts[index].ID >= account.ID
	})
	if position < len(accounts) && accounts[position].ID == account.ID {
		updated := append([]Account(nil), accounts...)
		updated[position] = account
		return updated
	}
	updated := make([]Account, 0, len(accounts)+1)
	updated = append(updated, accounts[:position]...)
	updated = append(updated, account)
	updated = append(updated, accounts[position:]...)
	return updated
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
	store := s.env.Store()
	var states map[string]string
	if store != nil {
		states = store.AccountStateValues()
	}
	s.selectionIndex.Build(s.env.CatalogSnapshot(), states)
	return nil
}

func (s *Service) ensureSelectionIndex() error {
	if s == nil || s.selectionIndex.Ready() {
		return nil
	}
	return s.rebuildSelectionIndex()
}

func (s *Service) refreshSelectionAccount(authID string) {
	if s == nil || authID == "" {
		return
	}
	index := &s.selectionIndex
	index.buildMu.Lock()
	defer index.buildMu.Unlock()
	if !index.Ready() {
		return
	}
	account, ok := s.env.FindAccount(authID)
	if !ok {
		index.Remove(authID)
		return
	}
	state, okState := s.env.StateAccount(account.ID)
	if !okState && account.AuthIndex != "" {
		state, _ = s.env.StateAccount(account.AuthIndex)
	}
	stateValue := state.State
	if !index.NeedsUpsert(account, stateValue) {
		return
	}
	index.Upsert(account, stateValue)
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
	if maxEntries > 0 && len(runtime.bindings) >= maxEntries {
		runtime.evictOldestBindingLocked()
	}
	runtime.setBindingLocked(sessionKey, routingSessionBinding{
		AuthID:    proposedAuthID,
		ExpiresAt: now.Add(ttl),
		BoundAt:   now,
	})
	return proposedAuthID
}

func (runtime *routingSelectionRuntime) RefreshBinding(sessionKey, authID string, now time.Time, ttl time.Duration) {
	if sessionKey == "" || authID == "" {
		return
	}
	runtime.mu.Lock()
	runtime.pruneBindingsLocked(now)
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

func (runtime *routingSelectionRuntime) evictOldestBindingLocked() {
	for runtime.bindingAge.Len() > 0 {
		entry := heap.Pop(&runtime.bindingAge).(bindingTimeEntry)
		binding, ok := runtime.bindings[entry.sessionKey]
		if ok && binding.BoundAt.Equal(entry.at) {
			delete(runtime.bindings, entry.sessionKey)
			return
		}
	}
	// The heaps can only lag the map after lazy deletions. Rebuild once if a
	// caller still needs to enforce the size limit.
	if len(runtime.bindings) == 0 {
		return
	}
	runtime.compactBindingHeapsLocked()
	for runtime.bindingAge.Len() > 0 {
		entry := heap.Pop(&runtime.bindingAge).(bindingTimeEntry)
		if _, ok := runtime.bindings[entry.sessionKey]; ok {
			delete(runtime.bindings, entry.sessionKey)
			return
		}
	}
}
