package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	proxyapp "grok-quality-guard/internal/app/proxy"
)

func (s *Service) refreshLoop(ctx context.Context) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	_ = s.RefreshCatalog(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.RefreshCatalog(ctx)
		}
	}
}

const (
	catalogRefreshTTL            = 30 * time.Second
	authModelsRefreshTTL         = 5 * time.Minute
	guardCatalogMissingRetention = 24 * time.Hour
)

func (s *Service) ensureCatalog(ctx context.Context) error {
	s.catalogMu.RLock()
	fresh := !s.catalogAt.IsZero() && time.Since(s.catalogAt) < catalogRefreshTTL
	s.catalogMu.RUnlock()
	if fresh {
		return nil
	}

	s.catalogRefreshMu.Lock()
	defer s.catalogRefreshMu.Unlock()
	s.catalogMu.RLock()
	fresh = !s.catalogAt.IsZero() && time.Since(s.catalogAt) < catalogRefreshTTL
	s.catalogMu.RUnlock()
	if fresh {
		return nil
	}
	return s.refreshCatalogLocked(ctx)
}

func (s *Service) RefreshCatalog(ctx context.Context) error {
	s.catalogRefreshMu.Lock()
	defer s.catalogRefreshMu.Unlock()
	return s.refreshCatalogLocked(ctx)
}

func (s *Service) refreshCatalogLocked(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.catalogMu.RLock()
	previousCatalog := s.catalog
	previousByID := s.catalogByID
	s.catalogMu.RUnlock()

	entries, errList := s.env.ListAuths()
	if errList != nil {
		return errList
	}
	accounts := make([]Account, 0, len(entries))
	for _, entry := range entries {
		provider := strings.TrimSpace(entry.Provider)
		if provider == "" {
			provider = strings.TrimSpace(entry.Type)
		}
		if provider != "" && !isGrokProvider(provider) && !isGrokProvider(entry.Type) {
			continue
		}
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			id = strings.TrimSpace(entry.AuthIndex)
		}
		if id == "" {
			continue
		}
		account := Account{
			ID:          id,
			AuthIndex:   strings.TrimSpace(entry.AuthIndex),
			Type:        entry.Type,
			Provider:    provider,
			Label:       entry.Label,
			Email:       entry.Email,
			Disabled:    entry.Disabled,
			Unavailable: entry.Unavailable,
			Priority:    entry.Priority,
			Path:        strings.TrimSpace(entry.Path),
			Size:        entry.Size,
			ModTime:     entry.ModTime,
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Priority != accounts[j].Priority {
			return accounts[i].Priority > accounts[j].Priority
		}
		return accounts[i].ID < accounts[j].ID
	})
	s.refreshAuthMeta(ctx, accounts, previousCatalog, previousByID)
	fingerprints := catalogFingerprints(accounts)

	s.catalogMu.Lock()
	stateChanged := fingerprints.state != s.catalogStateFingerprint
	routingChanged := fingerprints.routing != s.catalogRoutingFingerprint
	modelsChanged := fingerprints.models != s.catalogModelsFingerprint
	s.catalog = accounts
	byID := make(map[string]int, len(accounts))
	byAuthIndex := make(map[string]int, len(accounts))
	for index, account := range accounts {
		byID[account.ID] = index
		if authIndex := strings.TrimSpace(account.AuthIndex); authIndex != "" && authIndex != account.ID {
			byAuthIndex[authIndex] = index
		}
	}
	s.catalogByID = byID
	s.catalogByAuthIndex = byAuthIndex
	s.catalogStateFingerprint = fingerprints.state
	s.catalogRoutingFingerprint = fingerprints.routing
	s.catalogModelsFingerprint = fingerprints.models
	s.catalogAt = time.Now().UTC()
	s.catalogMu.Unlock()

	if s.grokAuthModelsNeedRefresh(modelsChanged) {
		if authModels, errModels := s.env.ListAuthModels(); errModels == nil {
			s.setGrokAuthModels(authModels)
		}
	}
	if stateChanged || !s.catalogStateMatches(accounts) {
		_ = s.syncCatalogState(accounts)
	}
	if routingChanged || !s.env.SelectionReady() {
		_ = s.env.RebuildSelectionIndex()
	}
	s.env.WakeProxyAutoBind()
	return nil
}

type catalogFingerprintSet struct {
	state   uint64
	routing uint64
	models  uint64
}

const (
	fingerprintOffset64 uint64 = 14695981039346656037
	fingerprintPrime64  uint64 = 1099511628211
)

type fingerprint uint64

func (value *fingerprint) writeString(text string) {
	current := uint64(*value)
	for index := 0; index < len(text); index++ {
		current ^= uint64(text[index])
		current *= fingerprintPrime64
	}
	current ^= 0
	current *= fingerprintPrime64
	*value = fingerprint(current)
}

func (value *fingerprint) writeBool(flag bool) {
	current := uint64(*value)
	if flag {
		current ^= 1
	} else {
		current ^= 0
	}
	current *= fingerprintPrime64
	*value = fingerprint(current)
}

func (value *fingerprint) writeInt(number int) {
	current := uint64(*value)
	encoded := uint64(number)
	for shift := 0; shift < 64; shift += 8 {
		current ^= encoded & 0xff
		current *= fingerprintPrime64
		encoded >>= 8
	}
	*value = fingerprint(current)
}

func catalogFingerprints(accounts []Account) catalogFingerprintSet {
	state := fingerprint(fingerprintOffset64)
	routing := fingerprint(fingerprintOffset64)
	models := fingerprint(fingerprintOffset64)
	for _, account := range accounts {
		state.writeString(account.ID)
		state.writeBool(account.Disabled)

		models.writeString(account.ID)
		models.writeString(account.AuthIndex)
		models.writeString(account.Type)
		models.writeString(account.Provider)

		routing.writeString(account.ID)
		routing.writeString(account.AuthIndex)
		routing.writeString(account.Type)
		routing.writeString(account.Provider)
		routing.writeBool(account.Disabled)
		routing.writeBool(account.Unavailable)
		routing.writeInt(account.Priority)
	}
	return catalogFingerprintSet{state: uint64(state), routing: uint64(routing), models: uint64(models)}
}

func (s *Service) catalogStateMatches(accounts []Account) bool {
	store := s.env.Store()
	return store != nil && store.CatalogMatches(accounts)
}

func (s *Service) grokAuthModelsNeedRefresh(modelsChanged bool) bool {
	s.authModelsMu.RLock()
	defer s.authModelsMu.RUnlock()
	if modelsChanged || !s.authModelsReady || s.authModelsAt.IsZero() {
		return true
	}
	return time.Since(s.authModelsAt) >= authModelsRefreshTTL
}

type authMetaJob struct {
	accountIndex int
	path         string
}

type authMetaValue struct {
	proxyURL string
	loaded   bool
}

func (s *Service) refreshAuthMeta(ctx context.Context, accounts, previousCatalog []Account, previousByID map[string]int) {
	if len(accounts) == 0 {
		return
	}
	jobs := make([]authMetaJob, 0)
	for index := range accounts {
		account := &accounts[index]
		path := strings.TrimSpace(account.Path)
		if path == "" {
			continue
		}
		if previousIndex, exists := previousByID[account.ID]; exists && previousIndex >= 0 && previousIndex < len(previousCatalog) {
			cached := previousCatalog[previousIndex]
			if cached.ProxyURLKnown && cached.Path == path && cached.Size == account.Size && cached.ModTime.Equal(account.ModTime) {
				account.ProxyURL = cached.ProxyURL
				account.ProxyURLKnown = true
				account.ProxyHash = cached.ProxyHash
				continue
			}
		}
		jobs = append(jobs, authMetaJob{accountIndex: index, path: path})
	}
	if len(jobs) == 0 {
		return
	}

	values := make([]authMetaValue, len(accounts))
	_ = runBoundedQueue(ctx, jobs, 64, 8, func(_ context.Context, job authMetaJob) {
		proxyURL, loaded := readAuthProxyURL(job.path)
		values[job.accountIndex] = authMetaValue{proxyURL: proxyURL, loaded: loaded}
	})
	for _, job := range jobs {
		value := values[job.accountIndex]
		if value.loaded {
			accounts[job.accountIndex].ProxyURL = value.proxyURL
			accounts[job.accountIndex].ProxyURLKnown = true
			if value.proxyURL != "" {
				accounts[job.accountIndex].ProxyHash = proxyapp.URLHash(value.proxyURL)
			}
		}
	}
}

func readAuthProxyURL(path string) (string, bool) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return "", false
	}
	if strings.TrimSpace(string(data)) == "null" {
		return "", false
	}
	var metadata struct {
		ProxyURL json.RawMessage `json:"proxy_url"`
	}
	if errUnmarshal := json.Unmarshal(data, &metadata); errUnmarshal != nil {
		return "", false
	}
	var proxyURL string
	if len(metadata.ProxyURL) == 0 || string(metadata.ProxyURL) == "null" {
		return "", true
	}
	if json.Unmarshal(metadata.ProxyURL, &proxyURL) != nil {
		return "", false
	}
	return strings.TrimSpace(proxyURL), true
}
func (s *Service) syncCatalogState(accounts []Account) error {
	store := s.env.Store()
	if store == nil {
		return nil
	}
	now := time.Now().UTC()
	live := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		live[account.ID] = struct{}{}
	}
	leaseDeleteQueued := false
	errSync := store.UpdateIfChanged(func(state *State) (bool, error) {
		changed := false
		cleanupKeys := make(map[string]struct{}, len(state.ResinLeaseDeletes))
		for _, job := range state.ResinLeaseDeletes {
			cleanupKeys[proxyapp.LeaseDeleteKey(job.AdminURL, job.PlatformID, job.Account)] = struct{}{}
		}
		for authID, account := range state.Accounts {
			if _, ok := live[authID]; ok {
				if account != nil && !account.CatalogMissingSince.IsZero() {
					account.CatalogMissingSince = time.Time{}
					changed = true
				}
				continue
			}
			if account == nil {
				delete(state.Accounts, authID)
				changed = true
				continue
			}
			// A host catalog can be temporarily partial while CPA reloads auth
			// records. Keep guard-owned state long enough to preserve ownership.
			if account.DisableKind != "" || account.AuthDisabledIntent != nil {
				if account.CatalogMissingSince.IsZero() {
					account.CatalogMissingSince = now
					changed = true
					continue
				}
				if now.Sub(account.CatalogMissingSince) < guardCatalogMissingRetention {
					continue
				}
			}
			if store.ProxyPendingLocked(authID) != "" {
				continue
			}
			binding, managed := proxyapp.BindingForAccount(*account)
			if managed && binding.Lease != nil && binding.Lease.Provider == "resin" && binding.Lease.SnapshotID != "" {
				if settings, ok := state.ResinSnapshots[binding.Lease.SnapshotID]; ok && proxyapp.ResinCleanupConfigured(settings) {
					job := proxyapp.NewResinLeaseDelete(authID, settings, binding.Lease.Account)
					key := proxyapp.LeaseDeleteKey(job.AdminURL, job.PlatformID, job.Account)
					if _, exists := cleanupKeys[key]; !exists {
						state.ResinLeaseDeletes = append(state.ResinLeaseDeletes, job)
						cleanupKeys[key] = struct{}{}
						leaseDeleteQueued = true
						changed = true
					}
				}
			}
			delete(state.Accounts, authID)
			changed = true
		}
		for _, account := range accounts {
			existing := state.Accounts[account.ID]
			if existing == nil {
				existing = &AccountState{
					AuthID:      account.ID,
					State:       StateUnknown,
					FirstSeenAt: now,
				}
				state.Accounts[account.ID] = existing
				changed = true
			}
			if !existing.CatalogMissingSince.IsZero() {
				existing.CatalogMissingSince = time.Time{}
				changed = true
			}
			if account.Disabled {
				if existing.DisableKind != "" {
					expected := stateForDisableKind(existing.DisableKind)
					if existing.State != expected {
						existing.State = expected
						changed = true
					}
					if existing.DisableReason == "" && existing.LastReason != "" {
						existing.DisableReason = existing.LastReason
						changed = true
					}
				} else {
					if existing.State != StateExternallyDisabled {
						existing.State = StateExternallyDisabled
						changed = true
					}
					if existing.DisableReason != "" {
						existing.DisableReason = ""
						changed = true
					}
				}
			}
			if !account.Disabled && existing.State == StateExternallyDisabled {
				existing.State = StateUnknown
				changed = true
			}
		}
		return changed, nil
	})
	if errSync == nil && leaseDeleteQueued {
		s.env.WakeResinLeaseDeleteLoop()
	}
	return errSync
}

func (s *Service) catalogSnapshot() []Account {
	s.catalogMu.RLock()
	defer s.catalogMu.RUnlock()
	// Published catalog slices are immutable; account values only contain
	// immutable strings and scalar fields, so sharing the backing array avoids
	// copying several hundred kilobytes on every management request.
	return s.catalog
}

func (s *Service) findAccount(authID string) (Account, bool) {
	normalized := strings.TrimSpace(authID)
	if normalized == "" {
		return Account{}, false
	}
	s.catalogMu.RLock()
	if index, ok := s.catalogByID[normalized]; ok && index >= 0 && index < len(s.catalog) {
		account := s.catalog[index]
		s.catalogMu.RUnlock()
		return account, true
	}
	if index, ok := s.catalogByAuthIndex[normalized]; ok && index >= 0 && index < len(s.catalog) {
		account := s.catalog[index]
		s.catalogMu.RUnlock()
		return account, true
	}
	s.catalogMu.RUnlock()
	return Account{}, false
}

func (s *Service) resolveAuth(ctx context.Context, authID string) (Account, pluginapi.HostAuthGetResponse, map[string]json.RawMessage, error) {
	account, okAccount := s.findAccount(authID)
	if !okAccount {
		if errRefresh := s.RefreshCatalog(ctx); errRefresh != nil {
			return Account{}, pluginapi.HostAuthGetResponse{}, nil, errRefresh
		}
		account, okAccount = s.findAccount(authID)
	}
	if !okAccount {
		return Account{}, pluginapi.HostAuthGetResponse{}, nil, errors.New("auth not found")
	}
	index := account.AuthIndex
	if index == "" {
		index = account.ID
	}
	response, errGet := s.env.GetAuth(index)
	if errGet != nil {
		return account, response, nil, errGet
	}
	raw, errMap := rawAuthToMap(response.JSON)
	if errMap != nil {
		return account, response, nil, errMap
	}
	return account, response, raw, nil
}
