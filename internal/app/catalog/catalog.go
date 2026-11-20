package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	authModelsRetryBackoff       = 30 * time.Second
	authModelsCallTimeout        = 30 * time.Second
	authMetaRefreshTimeout       = 2 * time.Minute
	authMetaFailureBackoff       = 5 * time.Minute
	guardCatalogMissingRetention = 24 * time.Hour
)

func (s *Service) ensureCatalog(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.catalogMu.RLock()
	hasCatalog := !s.catalogAt.IsZero()
	stale := !hasCatalog || time.Since(s.catalogAt) >= catalogRefreshTTL
	s.catalogMu.RUnlock()
	if hasCatalog {
		if stale {
			// Management reads must never wait behind host.auth.list. If the
			// periodic worker is disabled or behind, start one non-blocking
			// refresh and serve the current snapshot to this request.
			s.startCatalogRefresh()
		}
		return nil
	}
	// Cold reads cannot be served meaningfully without a catalog, but they
	// must not wait for the host callback indefinitely. Start one refresh and
	// wait only for the bounded cold-start window; the caller receives a
	// retryable JSON 503 if the catalog is still unavailable.
	s.startColdCatalogRefresh()
	waitCtx, cancel := context.WithTimeout(ctx, catalogColdWait)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s.catalogAvailable() {
			return nil
		}
		select {
		case <-s.catalogPublished:
		case <-ticker.C:
		case <-waitCtx.Done():
			// When the request budget expires while the catalog is still cold,
			// report the actionable catalog state instead of a generic timeout.
			if errCtx := ctx.Err(); errCtx != nil && !errors.Is(errCtx, context.DeadlineExceeded) {
				return errCtx
			}
			return CatalogLoadingError{}
		}
	}
}

const catalogColdWait = 2 * time.Second

// CatalogLoadingError is a retryable cold-catalog response. The management
// envelope recognizes ErrorCode and HTTPStatus so the browser receives JSON
// rather than a proxy-generated HTML 502.
type CatalogLoadingError struct{}

func (CatalogLoadingError) Error() string     { return "catalog loading" }
func (CatalogLoadingError) ErrorCode() string { return "catalog_loading" }
func (CatalogLoadingError) HTTPStatus() int   { return 503 }

func (s *Service) catalogAvailable() bool {
	if s == nil {
		return false
	}
	s.catalogMu.RLock()
	available := !s.catalogAt.IsZero()
	s.catalogMu.RUnlock()
	return available
}

func (s *Service) notifyCatalogPublished() {
	if s == nil || s.catalogPublished == nil {
		return
	}
	select {
	case s.catalogPublished <- struct{}{}:
	default:
	}
}

func (s *Service) startColdCatalogRefresh() {
	if s == nil || s.env == nil || s.stopped() || !tryAcquireGate(s.catalogRefreshGate) {
		return
	}
	s.catalogMu.Lock()
	if !s.catalogAt.IsZero() {
		s.catalogMu.Unlock()
		releaseGate(s.catalogRefreshGate)
		return
	}
	s.catalogMu.Unlock()
	go func() {
		defer s.finishCatalogRefresh()
		_ = s.refreshCatalogLocked(s.backgroundContext())
	}()
}

func (s *Service) startCatalogRefresh() {
	if s == nil || s.env == nil || s.stopped() {
		return
	}
	if !tryAcquireGate(s.catalogRefreshGate) {
		s.catalogMu.Lock()
		s.catalogRefreshPending = true
		s.catalogMu.Unlock()
		return
	}
	s.catalogMu.Lock()
	if !s.catalogRefreshAttemptAt.IsZero() && time.Since(s.catalogRefreshAttemptAt) < catalogRefreshTTL {
		s.catalogMu.Unlock()
		releaseGate(s.catalogRefreshGate)
		return
	}
	s.catalogRefreshAttemptAt = time.Now().UTC()
	s.catalogRefreshPending = false
	s.catalogMu.Unlock()
	go func() {
		defer s.finishCatalogRefresh()
		_ = s.refreshCatalogLocked(s.backgroundContext())
	}()
}

func (s *Service) finishCatalogRefresh() {
	releaseGate(s.catalogRefreshGate)
	s.catalogMu.Lock()
	pending := s.catalogRefreshPending
	s.catalogRefreshPending = false
	s.catalogMu.Unlock()
	if pending {
		s.startCatalogRefresh()
	}
}

func (s *Service) RefreshCatalog(ctx context.Context) error {
	if errAcquire := acquireGate(ctx, s.catalogRefreshGate); errAcquire != nil {
		return errAcquire
	}
	defer releaseGate(s.catalogRefreshGate)
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

	now := time.Now().UTC()
	var firstSeenByID map[string]time.Time
	if store := s.env.Store(); store != nil {
		firstSeenByID = store.AccountFirstSeenValues()
	}
	entries, errList := s.env.ListAuths(ctx)
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
		var firstSeenAt time.Time
		if previousIndex, exists := previousByID[id]; exists && previousIndex >= 0 && previousIndex < len(previousCatalog) {
			firstSeenAt = previousCatalog[previousIndex].FirstSeenAt
		}
		if firstSeenAt.IsZero() {
			firstSeenAt = firstSeenByID[id]
		}
		if firstSeenAt.IsZero() {
			firstSeenAt = now
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
			FirstSeenAt: firstSeenAt,
		}
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].Priority != accounts[j].Priority {
			return accounts[i].Priority > accounts[j].Priority
		}
		return accounts[i].ID < accounts[j].ID
	})
	copyCachedAuthMeta(accounts, previousCatalog, previousByID)
	s.markAuthMetaInspected(accounts)
	fingerprints := catalogFingerprints(accounts)

	s.catalogMu.Lock()
	routingChanged := fingerprints.routing != s.catalogRoutingFingerprint
	modelsChanged := fingerprints.models != s.catalogModelsFingerprint
	s.catalog = accounts
	byID := make(map[string]int, len(accounts))
	byAuthIndex := make(map[string]int, len(accounts))
	publishedIDs := make([]string, 0, len(accounts))
	for index, account := range accounts {
		byID[account.ID] = index
		publishedIDs = append(publishedIDs, account.ID)
		if authIndex := strings.TrimSpace(account.AuthIndex); authIndex != "" && authIndex != account.ID {
			byAuthIndex[authIndex] = index
		}
	}
	idOrder := make([]int32, len(accounts))
	for index := range accounts {
		idOrder[index] = int32(index)
	}
	sort.Slice(idOrder, func(i, j int) bool { return accounts[idOrder[i]].ID < accounts[idOrder[j]].ID })
	s.catalogIDOrder = idOrder
	s.catalogByID = byID
	s.catalogByAuthIndex = byAuthIndex
	s.catalogRoutingFingerprint = fingerprints.routing
	s.catalogModelsFingerprint = fingerprints.models
	s.catalogAt = time.Now().UTC()
	if routingChanged || s.catalogRevision == 0 {
		s.catalogRevision++
	}
	s.catalogMu.Unlock()
	s.notifyCatalogPublished()

	// Drop proxy observations for auths that disappeared from the host catalog.
	// Observations are runtime-only overlays and must not accumulate across
	// account churn.
	s.env.PruneProxyObservations(publishedIDs)
	if modelsChanged {
		s.invalidateGrokAuthModels()
	}
	if s.grokAuthModelsNeedRefresh(modelsChanged) {
		s.startGrokAuthModelsRefresh(modelsChanged)
	}
	// Retention cleanup is time-based even when the catalog fingerprint is
	// unchanged, so a missing auth must still be re-evaluated on every refresh.
	// Do not gate this on catalog/state key equality: sparse state intentionally
	// omits catalog-only accounts, so that check would always be false and would
	// only add an O(catalog) scan. UpdateIfChanged keeps this O(sparse state) and
	// skips persistence when nothing is due.
	_ = s.syncCatalogState(accounts)
	if routingChanged || !s.env.SelectionReady() {
		_ = s.env.RebuildSelectionIndex()
	}
	// Auth-file metadata is enrichment only. Publish the host catalog first so
	// the account tab can render immediately with proxy status unknown, then
	// fill proxy/OAuth fields without blocking the request that triggered the
	// catalog refresh.
	s.startAuthMetaRefresh()
	s.env.WakeProxyAutoBind()
	return nil
}

type catalogFingerprintSet struct {
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
	routing := fingerprint(fingerprintOffset64)
	models := fingerprint(fingerprintOffset64)
	for _, account := range accounts {
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
	return catalogFingerprintSet{routing: uint64(routing), models: uint64(models)}
}

func (s *Service) grokAuthModelsNeedRefresh(modelsChanged bool) bool {
	if modelsChanged {
		return true
	}
	s.authModelsMu.RLock()
	defer s.authModelsMu.RUnlock()
	if s.authModelsReady {
		return s.authModelsAt.IsZero() || time.Since(s.authModelsAt) >= authModelsRefreshTTL
	}
	if s.authModelsLastAttempt.IsZero() {
		return true
	}
	return time.Since(s.authModelsLastAttempt) >= authModelsRetryBackoff
}

type authMetaJob struct {
	valueIndex int
	authID     string
	path       string
	size       int64
	modTime    time.Time
}

type authMetaFileSignature struct {
	path    string
	size    int64
	modTime time.Time
}

type authMetaFailure struct {
	signature authMetaFileSignature
	retryAt   time.Time
}

type authMetaValue struct {
	proxyURL  string
	OAuthID   string
	loaded    bool
	completed bool
}

// copyCachedAuthMeta reuses file-derived fields only while the backing file
// signature is unchanged. Raw proxy URLs are deliberately never cached in the
// catalog; the masked value and hash are sufficient for list and binding views.
func copyCachedAuthMeta(accounts, previousCatalog []Account, previousByID map[string]int) {
	for index := range accounts {
		account := &accounts[index]
		path := strings.TrimSpace(account.Path)
		if path == "" {
			continue
		}
		previousIndex, exists := previousByID[account.ID]
		if !exists || previousIndex < 0 || previousIndex >= len(previousCatalog) {
			continue
		}
		cached := previousCatalog[previousIndex]
		if cached.Path != path || cached.Size != account.Size || !cached.ModTime.Equal(account.ModTime) {
			continue
		}
		account.ProxyURLKnown = cached.ProxyURLKnown
		account.ProxyHash = cached.ProxyHash
		account.ProxyMasked = cached.ProxyMasked
		account.ProxyMetaInspected = cached.ProxyMetaInspected
		account.OAuthID = cached.OAuthID
	}
}

// markAuthMetaInspected keeps the list-view retry hint honest. A file that is
// in failure backoff has already been inspected for its current signature, so
// the dashboard should keep the proxy status unknown without polling as if the
// read is still in flight.
func (s *Service) markAuthMetaInspected(accounts []Account) {
	now := time.Now().UTC()
	s.authMetaFailuresMu.Lock()
	defer s.authMetaFailuresMu.Unlock()
	for index := range accounts {
		account := &accounts[index]
		if account.ProxyURLKnown {
			account.ProxyMetaInspected = true
			continue
		}
		path := strings.TrimSpace(account.Path)
		if path == "" {
			account.ProxyMetaInspected = false
			continue
		}
		signature := authMetaFileSignature{path: path, size: account.Size, modTime: account.ModTime}
		failure, failed := s.authMetaFailures[account.ID]
		account.ProxyMetaInspected = failed && failure.signature == signature && now.Before(failure.retryAt)
	}
}

func (s *Service) pendingAuthMetaJobs(accounts []Account) []authMetaJob {
	return s.pendingAuthMetaJobsForAccounts(accounts, true)
}

func (s *Service) pendingAuthMetaJobsForAccounts(accounts []Account, pruneFailures bool) []authMetaJob {
	now := time.Now().UTC()
	live := make(map[string]authMetaFileSignature, len(accounts))
	jobs := make([]authMetaJob, 0)
	s.authMetaFailuresMu.Lock()
	defer s.authMetaFailuresMu.Unlock()
	for index := range accounts {
		account := accounts[index]
		path := strings.TrimSpace(account.Path)
		if path == "" {
			continue
		}
		signature := authMetaFileSignature{path: path, size: account.Size, modTime: account.ModTime}
		live[account.ID] = signature
		if account.ProxyURLKnown {
			continue
		}
		if failure, failed := s.authMetaFailures[account.ID]; failed && failure.signature == signature && now.Before(failure.retryAt) {
			continue
		}
		jobs = append(jobs, authMetaJob{
			valueIndex: len(jobs),
			authID:     account.ID,
			path:       path,
			size:       account.Size,
			modTime:    account.ModTime,
		})
	}
	if pruneFailures {
		for authID, failure := range s.authMetaFailures {
			signature, exists := live[authID]
			if !exists || signature != failure.signature {
				delete(s.authMetaFailures, authID)
			}
		}
	}
	return jobs
}

func (s *Service) recordAuthMetaResults(jobs []authMetaJob, values []authMetaValue) {
	s.authMetaFailuresMu.Lock()
	defer s.authMetaFailuresMu.Unlock()
	if s.authMetaFailures == nil {
		s.authMetaFailures = make(map[string]authMetaFailure)
	}
	now := time.Now().UTC()
	for index, job := range jobs {
		if index >= len(values) || !values[index].completed {
			continue
		}
		if index < len(values) && values[index].loaded {
			delete(s.authMetaFailures, job.authID)
			continue
		}
		s.authMetaFailures[job.authID] = authMetaFailure{
			signature: authMetaFileSignature{path: job.path, size: job.size, modTime: job.modTime},
			retryAt:   now.Add(authMetaFailureBackoff),
		}
	}
}

// startAuthMetaRefresh schedules one bounded enrichment pass. The gate and
// pending flag serialize passes across catalog generations without making the
// host.list request wait for filesystem reads.
func (s *Service) startAuthMetaRefresh() {
	if s == nil || s.env == nil || s.stopped() {
		return
	}
	s.authMetaRefreshMu.Lock()
	s.authMetaRefreshPending = true
	if !tryAcquireGate(s.authMetaRefreshGate) {
		s.authMetaRefreshMu.Unlock()
		return
	}
	s.authMetaRefreshPending = false
	s.authMetaRefreshMu.Unlock()
	go s.runAuthMetaRefresh()
}

// releaseAuthMetaRefreshGate clears a follow-up request only while the
// enrichment coordinator still owns the gate. Callers must hold the gate.
func (s *Service) releaseAuthMetaRefreshGate() {
	s.authMetaRefreshMu.Lock()
	s.authMetaRefreshPending = false
	releaseGate(s.authMetaRefreshGate)
	s.authMetaRefreshMu.Unlock()
}

func (s *Service) runAuthMetaRefresh() {
	released := false
	defer func() {
		if !released {
			// This also handles a panic or an early lifecycle stop. In the
			// normal path the gate is released while holding authMetaRefreshMu
			// below, so a queued request cannot be lost.
			s.releaseAuthMetaRefreshGate()
		}
	}()
	for {
		if s.stopped() {
			return
		}
		s.catalogMu.RLock()
		accounts := s.catalog
		s.catalogMu.RUnlock()

		jobs := s.pendingAuthMetaJobs(accounts)
		if len(jobs) > 0 {
			values := s.loadAuthMeta(jobs)
			s.recordAuthMetaResults(jobs, values)
			// Apply the values that still match their per-file signature. A
			// catalog generation change may be unrelated to these files; do not
			// discard and reread the entire set for that reason.
			changed := s.applyAuthMeta(jobs, values)
			s.notifyProxyBindingsChanged(changed)
		}

		s.authMetaRefreshMu.Lock()
		pending := s.authMetaRefreshPending
		s.authMetaRefreshPending = false
		if pending {
			s.authMetaRefreshMu.Unlock()
			continue
		}
		releaseGate(s.authMetaRefreshGate)
		s.authMetaRefreshMu.Unlock()
		released = true
		return
	}
}

func (s *Service) loadAuthMeta(jobs []authMetaJob) []authMetaValue {
	ctx, cancel := context.WithTimeout(s.backgroundContext(), authMetaRefreshTimeout)
	defer cancel()
	return s.loadAuthMetaContext(ctx, jobs)
}

func (s *Service) loadAuthMetaContext(ctx context.Context, jobs []authMetaJob) []authMetaValue {
	values := make([]authMetaValue, len(jobs))
	read := s.authMetaRead
	if read == nil {
		read = readAuthMeta
	}
	_ = runBoundedQueue(ctx, jobs, 64, 8, func(_ context.Context, job authMetaJob) {
		proxyURL, OAuthID, loaded := read(job.path)
		values[job.valueIndex] = authMetaValue{proxyURL: proxyURL, OAuthID: OAuthID, loaded: loaded, completed: true}
	})
	return values
}

// EnsureAuthMetaForIDs performs a bounded, page-scoped auth-file metadata read.
// The dashboard only asks for rows that are currently visible (at most a few
// dozen), so cold starts no longer need to wait for a full-catalog enrichment
// pass before showing bound/unbound state. Incomplete work remains uninspected
// and is rendered as checking, never as a failed read.
func (s *Service) EnsureAuthMetaForIDs(ctx context.Context, authIDs []string, budget time.Duration) error {
	if s == nil || s.env == nil || s.stopped() || len(authIDs) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if budget <= 0 {
		budget = 450 * time.Millisecond
	}
	if budget > 2*time.Second {
		budget = 2 * time.Second
	}
	wanted := make(map[string]struct{}, len(authIDs))
	for _, authID := range authIDs {
		if authID = strings.TrimSpace(authID); authID != "" {
			wanted[authID] = struct{}{}
		}
	}
	s.catalogMu.RLock()
	selected := make([]Account, 0, len(wanted))
	for authID := range wanted {
		if index, ok := s.catalogByID[authID]; ok && index >= 0 && index < len(s.catalog) {
			selected = append(selected, s.catalog[index])
		}
	}
	s.catalogMu.RUnlock()
	jobs := s.pendingAuthMetaJobsForAccounts(selected, false)
	if len(jobs) == 0 {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	values := s.loadAuthMetaContext(readCtx, jobs)
	completedJobs := make([]authMetaJob, 0, len(jobs))
	completedValues := make([]authMetaValue, 0, len(values))
	for index, value := range values {
		if value.completed {
			completedJobs = append(completedJobs, jobs[index])
			completedValues = append(completedValues, value)
		}
	}
	if len(completedJobs) == 0 {
		return nil
	}
	s.recordAuthMetaResults(completedJobs, completedValues)
	changed := s.applyAuthMeta(completedJobs, completedValues)
	s.notifyProxyBindingsChanged(changed)
	return nil
}

// applyAuthMeta applies values only to catalog rows whose backing file still has
// the same path, size, and modification time. Per-row validation preserves useful
// results across unrelated catalog refreshes without applying stale data.
func (s *Service) applyAuthMeta(jobs []authMetaJob, values []authMetaValue) []string {
	s.catalogMu.Lock()
	next := append([]Account(nil), s.catalog...)
	changed := false
	changedIDs := make([]string, 0, len(jobs))
	for index, job := range jobs {
		value := values[index]
		if !value.completed {
			continue
		}
		catalogIndex, exists := s.catalogByID[job.authID]
		if !exists || catalogIndex < 0 || catalogIndex >= len(next) {
			continue
		}
		account := &next[catalogIndex]
		if account.Path != job.path || account.Size != job.size || !account.ModTime.Equal(job.modTime) {
			continue
		}
		if !value.loaded {
			if !account.ProxyMetaInspected {
				account.ProxyMetaInspected = true
				changed = true
				changedIDs = append(changedIDs, job.authID)
			}
			continue
		}
		masked := proxyapp.MaskProxyURL(value.proxyURL)
		hash := ""
		if value.proxyURL != "" {
			hash = proxyapp.URLHash(value.proxyURL)
		}
		if account.ProxyURLKnown && account.ProxyMasked == masked && account.ProxyHash == hash && account.OAuthID == value.OAuthID && account.ProxyMetaInspected {
			continue
		}
		account.ProxyURLKnown = true
		account.ProxyMetaInspected = true
		account.ProxyMasked = masked
		account.ProxyHash = hash
		account.OAuthID = value.OAuthID
		changed = true
		changedIDs = append(changedIDs, job.authID)
	}
	if !changed {
		s.catalogMu.Unlock()
		return nil
	}
	s.catalog = next
	s.catalogMu.Unlock()
	s.env.WakeProxyAutoBind()
	return changedIDs
}

func (s *Service) notifyProxyBindingsChanged(authIDs []string) {
	if len(authIDs) == 0 {
		return
	}
	if notifier, ok := s.env.(interface{ NotifyProxyBindingsChanged([]string) }); ok {
		notifier.NotifyProxyBindingsChanged(authIDs)
	}
}
func readAuthMeta(path string) (string, string, bool) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		return "", "", false
	}
	defer file.Close()
	var metadata *struct {
		// A pointer keeps a missing or JSON null proxy readable. Both mean the
		// account has no proxy URL, so the binding stays unbound. A non-string
		// value still fails the decode and remains a genuine read failure.
		ProxyURL *string         `json:"proxy_url"`
		Sub      string          `json:"sub"`
		Subject  string          `json:"subject"`
		Metadata json.RawMessage `json:"metadata"`
	}
	decoder := json.NewDecoder(file)
	if errDecode := decoder.Decode(&metadata); errDecode != nil || metadata == nil {
		return "", "", false
	}
	if errTrailing := decoder.Decode(&struct{}{}); !errors.Is(errTrailing, io.EOF) {
		return "", "", false
	}
	oauthID := strings.TrimSpace(metadata.Sub)
	if oauthID == "" {
		oauthID = strings.TrimSpace(metadata.Subject)
	}
	if oauthID == "" && len(metadata.Metadata) > 0 {
		var nested struct {
			Sub     string `json:"sub"`
			Subject string `json:"subject"`
		}
		if json.Unmarshal(metadata.Metadata, &nested) == nil {
			oauthID = strings.TrimSpace(nested.Sub)
			if oauthID == "" {
				oauthID = strings.TrimSpace(nested.Subject)
			}
		}
	}
	proxyURL := ""
	if metadata.ProxyURL != nil {
		proxyURL = strings.TrimSpace(*metadata.ProxyURL)
	}
	return proxyURL, oauthID, true
}
func catalogStateNeedsRetention(account *AccountState) bool {
	if account == nil {
		return false
	}
	if account.State == StateExternallyDisabled || account.DisableKind != "" || account.AuthDisabledIntent != nil || account.Proxy.SourceID != "" {
		return true
	}
	autoBind := account.AutoBind
	return autoBind.Excluded || autoBind.Attempts > 0 || !autoBind.NextAttemptAt.IsZero() || autoBind.LastError != "" || autoBind.LastSourceID != ""
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
			// records. Keep guard-owned state, including managed proxy bindings,
			// long enough to preserve ownership before scheduling lease cleanup.
			if catalogStateNeedsRetention(account) {
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
				if snapshot, ok := state.ResinSnapshots[binding.Lease.SnapshotID]; ok && proxyapp.ResinCleanupConfigured(snapshot) {
					job := proxyapp.NewResinLeaseDelete(authID, snapshot, binding.Lease.Account)
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
				// Sparse state: a catalog-only account with no quality, guard,
				// probe, or proxy state is represented by the catalog itself.
				continue
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

func (s *Service) catalogWithIDOrder() ([]Account, []int32) {
	s.catalogMu.RLock()
	defer s.catalogMu.RUnlock()
	// Both published slices are immutable until the next catalog swap. The
	// management list can scan the four-byte index without sorting or copying.
	return s.catalog, s.catalogIDOrder
}

func (s *Service) catalogWithIDOrderRevision() ([]Account, []int32, uint64) {
	s.catalogMu.RLock()
	defer s.catalogMu.RUnlock()
	// Return the immutable published slices and the revision under one read
	// lock so aggregate caches cannot pair an old catalog with a new revision.
	return s.catalog, s.catalogIDOrder, s.catalogRevision
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
	response, errGet := s.env.GetAuthContext(ctx, index)
	if errGet != nil {
		return account, response, nil, errGet
	}
	raw, errMap := rawAuthToMap(response.JSON)
	if errMap != nil {
		return account, response, nil, errMap
	}
	return account, response, raw, nil
}
