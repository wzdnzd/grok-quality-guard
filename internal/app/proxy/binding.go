// Proxy binding models, source selection, provider generation and catalog cache.
package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"strings"
	"sync"
	"time"

	"grok-quality-guard/internal/app/auth"
	resinpkg "grok-quality-guard/internal/app/proxy/resin"
	"grok-quality-guard/internal/app/redact"
	statepkg "grok-quality-guard/internal/app/state"
	proxy "grok-quality-guard/internal/proxy"
)

const resinSourceID = "resin"

type passwordUpdate struct {
	Action string  `json:"action"`
	Value  *string `json:"value,omitempty"`
}

type proxyNodeInput struct {
	RetryExistingChange bool            `json:"-"`
	ID                  string          `json:"id,omitempty"`
	Name                string          `json:"name,omitempty"`
	URL                 string          `json:"url,omitempty"`
	Protocol            string          `json:"protocol,omitempty"`
	Host                string          `json:"host,omitempty"`
	Port                int             `json:"port,omitempty"`
	Username            string          `json:"username,omitempty"`
	Password            string          `json:"password,omitempty"`
	HasPassword         bool            `json:"has_password,omitempty"`
	PasswordUpdate      *passwordUpdate `json:"password_update,omitempty"`
	ImportFormat        string          `json:"import_format,omitempty"`
	Enabled             *bool           `json:"enabled,omitempty"`
	Priority            int             `json:"priority,omitempty"`
	MaxBindings         int             `json:"max_bindings,omitempty"`
	ExpectedVersion     uint64          `json:"expected_version,omitempty"`
	PrioritySet         bool            `json:"-"`
	MaxBindingsSet      bool            `json:"-"`
	UsernameSet         bool            `json:"-"`
}

type proxyTemplateInput struct {
	UsernamePatternEncoding string                     `json:"username_pattern_encoding,omitempty"`
	ID                      string                     `json:"id,omitempty"`
	Name                    string                     `json:"name,omitempty"`
	URL                     string                     `json:"url,omitempty"`
	Protocol                string                     `json:"protocol,omitempty"`
	Host                    string                     `json:"host,omitempty"`
	Port                    int                        `json:"port,omitempty"`
	UsernamePattern         *string                    `json:"username_pattern,omitempty"`
	PasswordUpdate          *passwordUpdate            `json:"password_update,omitempty"`
	Placeholders            []proxy.Placeholder        `json:"placeholders,omitempty"`
	ValueGroups             []proxy.ValueGroup         `json:"value_groups,omitempty"`
	Enabled                 *bool                      `json:"enabled,omitempty"`
	Priority                int                        `json:"priority,omitempty"`
	MaxBindings             int                        `json:"max_bindings,omitempty"`
	UpdatePolicy            proxy.TemplateUpdatePolicy `json:"update_policy,omitempty"`
	ExpectedVersion         uint64                     `json:"expected_version,omitempty"`
	PrioritySet             bool                       `json:"-"`
	MaxBindingsSet          bool                       `json:"-"`
}

type proxySourceView struct {
	ID               string                     `json:"id"`
	Kind             proxy.SourceKind           `json:"kind"`
	Name             string                     `json:"name"`
	Enabled          bool                       `json:"enabled"`
	Valid            bool                       `json:"valid"`
	ValidatedAt      time.Time                  `json:"validated_at,omitempty"`
	ConfigVersion    uint64                     `json:"config_version"`
	Revision         uint64                     `json:"revision"`
	Priority         int                        `json:"priority"`
	MaxBindings      int                        `json:"max_bindings"`
	BindingCount     int                        `json:"binding_count"`
	Health           proxy.HealthStatus         `json:"health,omitempty"`
	HealthChecked    time.Time                  `json:"health_checked,omitempty"`
	Lifecycle        string                     `json:"lifecycle,omitempty"`
	ChangeID         string                     `json:"change_id,omitempty"`
	Endpoint         string                     `json:"endpoint,omitempty"`
	UsernamePattern  string                     `json:"username_pattern,omitempty"`
	PasswordSet      bool                       `json:"password_set,omitempty"`
	UpdatePolicy     proxy.TemplateUpdatePolicy `json:"update_policy,omitempty"`
	PlaceholderCount int                        `json:"placeholder_count,omitempty"`
	GroupCount       int                        `json:"group_count,omitempty"`
	LocalOnly        bool                       `json:"local_only,omitempty"`
	Configured       bool                       `json:"configured,omitempty"`
	LastError        string                     `json:"last_error,omitempty"`
}

type proxyBindingResult struct {
	AuthID                  string           `json:"auth_id"`
	SourceID                string           `json:"source_id,omitempty"`
	SourceKind              proxy.SourceKind `json:"source_kind,omitempty"`
	Status                  string           `json:"status"`
	Code                    string           `json:"code,omitempty"`
	OperationID             string           `json:"operation_id,omitempty"`
	ProxyURL                string           `json:"proxy_url,omitempty"`
	Generation              uint64           `json:"generation,omitempty"`
	DryRun                  bool             `json:"dry_run,omitempty"`
	Committed               bool             `json:"committed,omitempty"`
	CommitUnknown           bool             `json:"commit_unknown,omitempty"`
	NeedsReconcile          bool             `json:"needs_reconcile,omitempty"`
	RemoteLeaseDeleteQueued bool             `json:"remote_lease_delete_queued,omitempty"`
	Error                   string           `json:"error,omitempty"`
	LeaseAccount            string           `json:"-"`
}

type proxyBindOptions struct {
	SourceScope        string
	SourceIDs          []string
	DryRun             bool
	Auto               bool
	AllowExternal      bool
	ExcludeAutoBind    *bool
	RequireHealthy     bool
	HealthMaxAge       time.Duration
	OperationID        string
	ChangeID           string
	ExpectedSourceID   string
	ExpectedGeneration *uint64
	Replace            bool
	Action             string
	AuthID             string
	ManualClearPolicy  string
}

type proxySourceCandidate struct {
	ID            string
	Kind          proxy.SourceKind
	Name          string
	Priority      int
	Max           int
	Revision      uint64
	ConfigVersion uint64
	Node          *proxy.Node
	Template      *proxy.Template
	Resin         bool
	ResinSettings *ResinSettings
	Provider      proxy.Provider
	Runtime       proxy.SourceRuntime
	Error         error
}

type proxySourceCatalog struct {
	Store   *StateStore
	Epoch   uint64
	Sources []proxySourceCandidate
	ByID    map[string]*proxySourceCandidate
}

type proxyObservation struct {
	URL       string
	Hash      string
	Known     bool
	CheckedAt time.Time
}

type proxyManager struct {
	mu             sync.Mutex
	buildMu        sync.Mutex
	scanMu         sync.Mutex
	changeMu       sync.Mutex
	runningChanges map[string]struct{}
	reservations   map[string]int
	reservedByAuth map[string]string
	resinReserved  map[string]struct{}
	queue          []string
	queueHead      int
	queued         map[string]struct{}
	inFlight       map[string]struct{}
	observations   map[string]proxyObservation
	runtime        proxy.AutoBindRuntime
	scanOffset     int
	wake           chan struct{}
	slots          chan struct{}
	catalog        *proxySourceCatalog
}

func newProxyManager() proxyManager {
	return proxyManager{
		reservations: make(map[string]int), reservedByAuth: make(map[string]string), resinReserved: make(map[string]struct{}),
		queued: make(map[string]struct{}), inFlight: make(map[string]struct{}),
		observations: make(map[string]proxyObservation), wake: make(chan struct{}, 1),
		slots: make(chan struct{}, 16),
	}
}

type proxyManagementError struct {
	Code    string
	Status  int
	Message string
}

func (e *proxyManagementError) Error() string { return e.Message }
func proxyError(code string, status int, message string) error {
	return &proxyManagementError{code, status, message}
}
func proxyErrorStatus(err error) int {
	var typed *proxyManagementError
	if errors.As(err, &typed) {
		return typed.Status
	}
	return 500
}
func failedProxyResult(authID, status string, err error) proxyBindingResult {
	result := proxyBindingResult{AuthID: authID, Status: status}
	if err != nil {
		result.Error = redact.SanitizeError(err)
		var typed *proxyManagementError
		if errors.As(err, &typed) {
			result.Code = typed.Code
		}
	}
	return result
}

func (s *Service) wakeProxyAutoBind() {
	if s == nil {
		return
	}
	select {
	case s.mgr.wake <- struct{}{}:
	default:
	}
}

func bindingForAccount(account AccountState) (proxy.Binding, bool) {
	if account.Proxy.SourceID != "" {
		return proxy.CloneBinding(account.Proxy), true
	}
	if account.Resin.Managed || account.Resin.Account != "" {
		return proxy.Binding{AuthID: account.AuthID, SourceID: resinSourceID, SourceKind: proxy.SourceResin,
			Generation: uint64(max(account.Resin.Generation, 0)), Lease: &proxy.LeaseRef{Provider: "resin", Account: account.Resin.Account}}, true
	}
	return proxy.CloneBinding(account.Proxy), false
}
func (s *Service) proxyBindingForAccount(account AccountState) (proxy.Binding, bool) {
	return bindingForAccount(account)
}
func proxyBindingSource(account AccountState) (string, bool) {
	if account.Proxy.SourceID != "" {
		return account.Proxy.SourceID, true
	}
	if account.Resin.Managed || account.Resin.Account != "" {
		return resinSourceID, true
	}
	return "", false
}
func newProxyID(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}
func randomIndex(length int) int {
	if length <= 1 {
		return 0
	}
	return mathrand.IntN(length)
}

func normalizeProxyScope(scope string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "all":
		return "all", nil
	case "node", "nodes":
		return "nodes", nil
	case "template", "templates":
		return "templates", nil
	case "resin":
		return "resin", nil
	default:
		return "", proxyError("proxy_scope_invalid", 400, "source_scope must be all, nodes, templates, or resin")
	}
}
func proxyScopeContains(scope string, kind proxy.SourceKind) bool {
	return scope == "all" || scope == string(kind)+"s" || (scope == "resin" && kind == proxy.SourceResin)
}

func (s *Service) proxyCatalog() *proxySourceCatalog {
	store := s.env.ProxyStore()
	epoch := store.ProxySourcesEpoch()
	s.mgr.mu.Lock()
	cached := s.mgr.catalog
	s.mgr.mu.Unlock()
	if cached != nil && cached.Store == store && cached.Epoch == epoch {
		return cached
	}
	s.mgr.buildMu.Lock()
	defer s.mgr.buildMu.Unlock()
	s.mgr.mu.Lock()
	cached = s.mgr.catalog
	s.mgr.mu.Unlock()
	if cached != nil && cached.Store == store && cached.Epoch == epoch {
		return cached
	}
	state := store.ProxySourcesSnapshot()
	catalog := &proxySourceCatalog{Store: store, Epoch: epoch, ByID: make(map[string]*proxySourceCandidate)}
	for i := range state.Nodes {
		node := state.Nodes[i]
		entry := proxySourceCandidate{ID: node.ID, Kind: proxy.SourceNode, Name: node.Name, Priority: node.Priority, Max: node.MaxBindings, Revision: node.Revision, Node: &node, Runtime: state.SourceRuntime[node.ID]}
		entry.Provider = proxy.NodeProvider{Node: node}
		entry.Error = node.Endpoint.Validate()
		catalog.Sources = append(catalog.Sources, entry)
	}
	for i := range state.Templates {
		template := state.Templates[i]
		entry := proxySourceCandidate{ID: template.ID, Kind: proxy.SourceTemplate, Name: template.Name, Priority: template.Priority, Max: template.MaxBindings, Revision: template.Revision, Template: &template, Runtime: state.SourceRuntime[template.ID]}
		if template.Kind == proxy.TemplateResin {
			entry.Kind = proxy.SourceResin
			entry.Resin = true
			config, ok := state.ResinSources[template.ID]
			if !ok {
				settings := resinSettingsForSource(template, proxy.ResinSourceConfig{AccountFormat: template.UsernamePattern})
				entry.ResinSettings = &settings
				entry.Error = proxyError("proxy_source_not_found", 404, "Resin configuration is missing")
				catalog.Sources = append(catalog.Sources, entry)
				continue
			}
			settings := resinSettingsForSource(template, config)
			entry.ResinSettings = &settings
			cachedEntry := (*proxySourceCandidate)(nil)
			if cached != nil && cached.Store == store {
				cachedEntry = cached.ByID[template.ID]
			}
			if cachedEntry != nil && cachedEntry.Revision == template.Revision && cachedEntry.ResinSettings != nil &&
				statepkg.ResinSnapshotID(*cachedEntry.ResinSettings) == statepkg.ResinSnapshotID(settings) {
				entry.Provider = cachedEntry.Provider
				entry.Error = cachedEntry.Error
			} else {
				entry.Provider, entry.Error = resinProviderForSource(template, config)
			}
			catalog.Sources = append(catalog.Sources, entry)
			continue
		}
		if cached != nil && cached.Store == store && cached.ByID[template.ID] != nil && cached.ByID[template.ID].Revision == template.Revision {
			entry.Provider = cached.ByID[template.ID].Provider
			entry.Error = cached.ByID[template.ID].Error
		} else {
			compiled, err := proxy.CompileTemplate(template)
			entry.Error = err
			if err == nil {
				entry.Provider = proxy.TemplateProvider{Compiled: compiled}
			}
		}
		catalog.Sources = append(catalog.Sources, entry)
	}
	settings := s.ResinConfig()
	if !state.ResinSourcesInitialized && !state.ResinSource.Deleted && (settings.Configured || settings.Enabled || settings.LocalOnly) {
		revision := max(state.ResinSource.Revision, 1)
		entry := proxySourceCandidate{ID: resinSourceID, Kind: proxy.SourceResin, Name: firstNonEmptyString(state.ResinSource.Name, "Resin"), Revision: revision, Resin: true, ResinSettings: &settings, Runtime: state.SourceRuntime[resinSourceID]}
		entry.ConfigVersion = state.ResinSource.ConfigVersion
		if !settings.LocalOnly {
			previous := (*proxySourceCandidate)(nil)
			if cached != nil && cached.Store == store {
				previous = cached.ByID[resinSourceID]
			}
			if previous != nil && previous.ResinSettings != nil && statepkg.ResinSnapshotID(*previous.ResinSettings) == statepkg.ResinSnapshotID(settings) {
				entry.Provider = previous.Provider
				entry.Error = previous.Error
			} else if settings.PlatformID == "" || settings.AdminURL == "" || settings.AdminToken == "" {
				entry.Error = proxyError("resin_validation_required", 409, "validate and save the Resin connection before binding")
			} else {
				entry.Provider, entry.Error = resinpkg.NewProvider(resinpkg.ProviderSettings{ProxyTemplate: settings.ProxyTemplate, Platform: settings.Platform, PlatformID: settings.PlatformID, ProxyToken: settings.ProxyToken, SnapshotID: statepkg.ResinSnapshotID(settings)}, nil)
			}
		}
		catalog.Sources = append(catalog.Sources, entry)
	}
	for i := range catalog.Sources {
		catalog.ByID[catalog.Sources[i].ID] = &catalog.Sources[i]
	}
	s.mgr.mu.Lock()
	s.mgr.catalog = catalog
	s.mgr.mu.Unlock()
	return catalog
}

func (s *Service) selectProxySource(opts proxyBindOptions, excludeHash string) (*proxySourceCandidate, []string) {
	scope, err := normalizeProxyScope(opts.SourceScope)
	if err != nil {
		return nil, []string{err.Error()}
	}
	catalog := s.proxyCatalog()
	allowed := make(map[string]struct{}, len(opts.SourceIDs))
	for _, id := range opts.SourceIDs {
		allowed[id] = struct{}{}
	}
	// Lock order: state read lock then the short allocation lock. Neither is held
	// during generation, filesystem operations, or Host calls.
	store := s.env.ProxyStore()
	indexes := store.ProxyIndexSnapshot()
	s.mgr.mu.Lock()
	defer s.mgr.mu.Unlock()
	transient := make(map[string]int, len(s.mgr.reservations))
	for id, count := range s.mgr.reservations {
		transient[id] = count
	}
	for authID, sourceID := range s.mgr.reservedByAuth {
		if indexes.PendingReserved[authID] == sourceID {
			transient[sourceID]--
		}
	}
	bestPriority, bestLoad := -int(^uint(0)>>1), int(^uint(0)>>1)
	var selected *proxySourceCandidate
	ties := 0
	reasons := make([]string, 0, min(8, len(catalog.Sources)))
	for i := range catalog.Sources {
		entry := &catalog.Sources[i]
		if !proxyScopeContains(scope, entry.Kind) {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[entry.ID]; !ok {
				continue
			}
		}
		enabled := (entry.Node != nil && entry.Node.Enabled) || (entry.Template != nil && entry.Template.Enabled) || (entry.ResinSettings != nil && entry.ResinSettings.Enabled && !entry.ResinSettings.LocalOnly)
		lifecycle := entry.Runtime.Lifecycle
		if !enabled || entry.Error != nil || (lifecycle != "" && lifecycle != "active") || entry.Runtime.RetryAt.After(time.Now()) {
			if len(reasons) < 8 {
				reasons = append(reasons, entry.Name+": disabled, invalid, or updating")
			}
			continue
		}
		if opts.RequireHealthy && !proxySourceHealthFresh(*entry, opts.HealthMaxAge) {
			if len(reasons) < 8 {
				reasons = append(reasons, entry.Name+": fresh health check required")
			}
			continue
		}
		if excludeHash != "" && entry.Node != nil && entry.Node.Endpoint.Fingerprint() == excludeHash {
			continue
		}
		load := indexes.Counts[entry.ID] + indexes.PendingCounts[entry.ID] + transient[entry.ID]
		if entry.Max > 0 && load >= entry.Max {
			if len(reasons) < 8 {
				reasons = append(reasons, entry.Name+": binding limit reached")
			}
			continue
		}
		if entry.Priority > bestPriority || (entry.Priority == bestPriority && load < bestLoad) {
			selected = entry
			bestPriority = entry.Priority
			bestLoad = load
			ties = 1
		} else if entry.Priority == bestPriority && load == bestLoad {
			ties++
			if randomIndex(ties) == 0 {
				selected = entry
			}
		}
	}
	if selected != nil && !opts.DryRun {
		s.mgr.reservations[selected.ID]++
		if opts.AuthID != "" {
			s.mgr.reservedByAuth[opts.AuthID] = selected.ID
		}
	}
	return selected, reasons
}
func proxySourceHealthFresh(entry proxySourceCandidate, maxAge time.Duration) bool {
	health, checked, revision := entry.Runtime.Health, entry.Runtime.HealthChecked, entry.Runtime.HealthRevision
	if entry.Node != nil && health == "" {
		health = entry.Node.Health
		checked = entry.Node.HealthChecked
		revision = entry.Node.Revision
	}
	age := time.Since(checked)
	return health == proxy.HealthHealthy && revision == entry.Revision && !checked.IsZero() && age >= 0 && maxAge > 0 && age <= maxAge
}
func (s *Service) releaseProxySource(id, authID string) {
	s.mgr.mu.Lock()
	if s.mgr.reservations[id] > 0 {
		s.mgr.reservations[id]--
	}
	delete(s.mgr.reservedByAuth, authID)
	s.mgr.mu.Unlock()
}
func (s *Service) enterProxyOperation(ctx context.Context) error {
	select {
	case s.mgr.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Service) leaveProxyOperation() { <-s.mgr.slots }

func proxyURLFromRaw(raw map[string]json.RawMessage) (string, error) {
	value, ok := raw["proxy_url"]
	if !ok || string(value) == "null" {
		return "", nil
	}
	var proxyURL string
	if err := json.Unmarshal(value, &proxyURL); err != nil {
		return "", proxyError("auth_proxy_unknown", 409, "auth proxy URL is not a string")
	}
	return strings.TrimSpace(proxyURL), nil
}

func (s *Service) bindProxy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		err := proxyError("auth_id_required", 400, "auth_id is required")
		return failedProxyResult(authID, "failed", err), err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer s.leaveProxyOperation()
	lock := s.env.ProxyAccountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if result, found, err := s.retryProxyAction(ctx, authID, opts, "proxy_bind"); found {
		return result, err
	}
	if s.env.ProxyStore().ProxyPending(authID) != "" {
		err := proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
		return failedProxyResult(authID, "reconcile_required", err), err
	}
	account, response, raw, err := s.env.ProxyResolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := s.env.ProxyStore().ProxyAccount(authID)
	binding, managed, verifyErr := s.verifiedProxyBinding(state, current, opts.DryRun)
	if verifyErr != nil {
		return failedProxyResult(authID, "unknown", verifyErr), verifyErr
	}
	if opts.Auto {
		config := s.env.ProxyStore().ProxyAutoBindConfig()
		provider := firstNonEmptyString(auth.RawString(raw, "type"), auth.RawString(raw, "provider"), account.Provider, account.Type)
		if !auth.IsGrokProvider(provider) {
			return proxyBindingResult{AuthID: authID, Status: "skipped", Code: "auto_bind_provider_mismatch"}, nil
		}
		account.Disabled = auth.Disabled(raw)
		opts.SourceScope = config.SourceScope
		opts.SourceIDs = config.SourceIDs
		opts.RequireHealthy = config.RequireHealthy
		opts.HealthMaxAge, _ = time.ParseDuration(config.HealthMaxAge)
		if !config.Enabled || state.AutoBind.Excluded || !proxyAccountAllowedAuto(account, state.State, state.DisableKind, config) {
			return proxyBindingResult{AuthID: authID, Status: "skipped", Code: "auto_bind_ineligible"}, nil
		}
	}
	if current != "" {
		if managed && bindingMatchesURL(binding, current) {
			return proxyBindingResult{AuthID: authID, SourceID: binding.SourceID, SourceKind: binding.SourceKind, Status: "already_bound", ProxyURL: proxy.MaskProxyURL(current), Generation: binding.Generation}, nil
		}
		status := "external"
		if managed {
			status = "drifted"
		}
		err := proxyError("external_proxy_present", 409, "auth already has a proxy; explicit adoption or clearing is required")
		return failedProxyResult(authID, status, err), err
	}
	if managed {
		err := proxyError("proxy_reconcile_required", 409, "binding exists but auth proxy URL is empty")
		return failedProxyResult(authID, "reconcile_required", err), err
	}
	opts.AuthID = authID
	candidate, reasons := s.selectProxySource(opts, "")
	if candidate == nil {
		return proxyBindingResult{AuthID: authID, Status: "unavailable", Code: "no_available_sources", Error: firstNonEmptyString(strings.Join(reasons, "; "), "no available proxy source")}, nil
	}
	if !opts.DryRun {
		defer s.releaseProxySource(candidate.ID, authID)
	}
	rendered, lease, release, err := s.generateProxySource(ctx, *candidate, authID, raw, "", opts.DryRun)
	if release != nil {
		defer release()
	}
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	if opts.DryRun {
		account := ""
		if lease != nil {
			account = lease.Account
		}
		return proxyBindingResult{AuthID: authID, SourceID: candidate.ID, SourceKind: candidate.Kind, Status: "dry_run", ProxyURL: rendered.Endpoint.MaskedURL(), DryRun: true, LeaseAccount: account}, nil
	}
	now := time.Now().UTC()
	generation := max(binding.Generation, uint64(max(state.Resin.Generation, 0))) + 1
	next := proxy.Binding{AuthID: authID, SourceID: candidate.ID, SourceKind: candidate.Kind, SourceRevision: candidate.Revision, Generation: generation, EndpointHash: rendered.Endpoint.Fingerprint(), ResolvedValues: rendered.Values, Lease: lease, BoundAt: now, UpdatedAt: now}
	operation := proxy.Operation{ID: firstNonEmptyString(opts.OperationID, newProxyID("op")), AuthID: authID, Action: firstNonEmptyString(opts.Action, "proxy_bind"), Auto: opts.Auto, ExpectedHash: proxyURLHash(current), ExpectedGeneration: binding.Generation, PreviousSourceID: binding.SourceID, DesiredURL: rendered.URL, Binding: next, ExcludeAutoBind: state.AutoBind.Excluded, Status: "prepared", CreatedAt: now, UpdatedAt: now}
	return s.commitProxyOperationLocked(ctx, response, raw, operation, opts.Auto)
}

func (s *Service) updateCatalogProxy(authID, proxyURL string) {
	// Catalog slices are shared immutable snapshots. Keep writes in a small
	// observation overlay instead of modifying an array visible to other readers.
	s.mgr.mu.Lock()
	s.mgr.observations[authID] = proxyObservation{URL: proxyURL, Hash: proxyURLHash(proxyURL), Known: true, CheckedAt: time.Now().UTC()}
	s.mgr.mu.Unlock()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (s *Service) generateProxySource(ctx context.Context, candidate proxySourceCandidate, authID string, raw map[string]json.RawMessage, avoidHash string, dryRun bool) (proxy.RenderedProxy, *proxy.LeaseRef, func(), error) {
	request := proxy.GenerateRequest{AuthID: authID, SourceID: candidate.ID, SourceKind: candidate.Kind, Revision: candidate.Revision, AvoidEndpointHash: avoidHash}
	if candidate.Kind != proxy.SourceResin {
		if candidate.Provider == nil {
			return proxy.RenderedProxy{}, nil, nil, errors.New("proxy generator is unavailable")
		}
		rendered, err := candidate.Provider.Generate(ctx, request)
		return rendered, nil, nil, err
	}
	settings := *candidate.ResinSettings
	base := resinAccountBase(raw)
	if base == "" {
		base = "grok"
	}
	if candidate.Template != nil {
		if candidate.Provider == nil {
			return proxy.RenderedProxy{}, nil, nil, errors.New("resin generator is unavailable")
		}
		request.AccountBase = base
		store := s.env.ProxyStore()
		for attempt := 0; attempt < 16; attempt++ {
			rendered, err := candidate.Provider.Generate(ctx, request)
			if err != nil {
				return proxy.RenderedProxy{}, nil, nil, err
			}
			identity := rendered.GeneratedAccount
			s.mgr.mu.Lock()
			_, reserved := s.mgr.resinReserved[identity]
			used := store.ProxyLeaseUsage(identity) > 0
			if !used && !reserved && !dryRun {
				s.mgr.resinReserved[identity] = struct{}{}
			}
			s.mgr.mu.Unlock()
			if used || reserved {
				continue
			}
			release := func() {
				s.mgr.mu.Lock()
				delete(s.mgr.resinReserved, identity)
				s.mgr.mu.Unlock()
			}
			if settings.LocalOnly {
				current, err := proxyURLFromRaw(raw)
				if err != nil {
					release()
					return proxy.RenderedProxy{}, nil, nil, err
				}
				value, err := rewriteResinProxyAccount(current, identity)
				if err != nil {
					release()
					return proxy.RenderedProxy{}, nil, nil, err
				}
				e, err := proxy.ParseEndpoint(value, proxy.ParseOptions{})
				if err != nil {
					release()
					return proxy.RenderedProxy{}, nil, nil, err
				}
				rendered.Endpoint = e
				rendered.URL = e.CanonicalURL()
				rendered.MaskedURL = e.MaskedURL()
				return rendered, &proxy.LeaseRef{Provider: "resin-local", Account: identity}, release, nil
			}
			if !dryRun {
				if _, err = store.EnsureResinSnapshot(settings); err != nil {
					release()
					return proxy.RenderedProxy{}, nil, nil, err
				}
			}
			return rendered, rendered.Cleanup, release, nil
		}
		return proxy.RenderedProxy{}, nil, nil, errors.New("unable to generate a different proxy identity")
	}
	for attempt := 0; attempt < 16; attempt++ {
		suffix, err := randomBase36(12)
		if err != nil {
			return proxy.RenderedProxy{}, nil, nil, err
		}
		identity := "grok-" + base + "-" + suffix
		store := s.env.ProxyStore()
		s.mgr.mu.Lock()
		_, reserved := s.mgr.resinReserved[identity]
		used := store.ProxyLeaseUsage(identity) > 0
		if !used && !reserved && !dryRun {
			s.mgr.resinReserved[identity] = struct{}{}
		}
		s.mgr.mu.Unlock()
		if used || reserved {
			continue
		}
		release := func() { s.mgr.mu.Lock(); delete(s.mgr.resinReserved, identity); s.mgr.mu.Unlock() }
		if settings.LocalOnly {
			current, err := proxyURLFromRaw(raw)
			if err != nil {
				release()
				return proxy.RenderedProxy{}, nil, nil, err
			}
			value, err := rewriteResinProxyAccount(current, identity)
			if err != nil {
				release()
				return proxy.RenderedProxy{}, nil, nil, err
			}
			e, err := proxy.ParseEndpoint(value, proxy.ParseOptions{})
			if err != nil {
				release()
				return proxy.RenderedProxy{}, nil, nil, err
			}
			return proxy.RenderedProxy{Endpoint: e, URL: e.CanonicalURL(), MaskedURL: e.MaskedURL(), Values: map[string]string{"account": identity}}, &proxy.LeaseRef{Provider: "resin-local", Account: identity}, release, nil
		}
		snapshotID := statepkg.ResinSnapshotID(settings)
		if !dryRun {
			snapshotID, err = store.EnsureResinSnapshot(settings)
		}
		if err != nil {
			release()
			return proxy.RenderedProxy{}, nil, nil, err
		}
		provider, ok := candidate.Provider.(*resinpkg.Provider)
		if !ok {
			provider, err = resinpkg.NewProvider(resinpkg.ProviderSettings{ProxyTemplate: settings.ProxyTemplate, Platform: settings.Platform, PlatformID: settings.PlatformID, ProxyToken: settings.ProxyToken, SnapshotID: snapshotID}, nil)
			if err != nil {
				release()
				return proxy.RenderedProxy{}, nil, nil, err
			}
		}
		rendered, err := provider.RenderAccount(identity)
		if err != nil {
			release()
			return proxy.RenderedProxy{}, nil, nil, err
		}
		if rendered.Endpoint.Fingerprint() == avoidHash {
			release()
			continue
		}
		return rendered, rendered.Cleanup, release, nil
	}
	return proxy.RenderedProxy{}, nil, nil, errors.New("unable to generate a different proxy identity")
}

func (s *Service) captureLegacyLease(binding proxy.Binding, current string) (*proxy.LeaseRef, error) {
	if binding.Lease == nil {
		return nil, nil
	}
	lease := *binding.Lease
	if binding.SourceID != "" {
		if entry := s.proxyCatalog().ByID[binding.SourceID]; entry != nil && entry.Template != nil && entry.ResinSettings != nil {
			if entry.ResinSettings.LocalOnly {
				lease.Provider = "resin-local"
				return &lease, nil
			}
			if resinCleanupConfigured(*entry.ResinSettings) {
				id, err := s.env.ProxyStore().EnsureResinSnapshot(*entry.ResinSettings)
				if err != nil {
					return nil, err
				}
				lease.Provider = "resin"
				lease.PlatformID = entry.ResinSettings.PlatformID
				lease.SnapshotID = id
				return &lease, nil
			}
		}
	}
	settings := s.ResinConfig()
	// A shared hostname is insufficient evidence for deleting a historical lease.
	if settings.LocalOnly {
		lease.Provider = "resin-local"
		return &lease, nil
	}
	if lease.Account == "" {
		return &lease, nil
	}
	expected, err := renderResinProxy(settings, lease.Account)
	if err != nil || proxyURLHash(expected) != proxyURLHash(current) {
		return &lease, nil
	}
	if !resinCleanupConfigured(settings) {
		return &lease, nil
	}
	id, err := s.env.ProxyStore().EnsureResinSnapshot(settings)
	if err != nil {
		return nil, err
	}
	lease.Provider = "resin"
	lease.PlatformID = settings.PlatformID
	lease.SnapshotID = id
	return &lease, nil
}

func resinCleanupConfigured(settings ResinSettings) bool {
	return !settings.LocalOnly && strings.TrimSpace(settings.AdminURL) != "" && strings.TrimSpace(settings.PlatformID) != "" && strings.TrimSpace(settings.AdminToken) != ""
}

func (s *Service) cleanupForLease(authID string, lease proxy.LeaseRef) (ResinLeaseDelete, bool) {
	if lease.Provider != "resin" || lease.Account == "" || lease.SnapshotID == "" {
		return ResinLeaseDelete{}, false
	}
	if s.env.ProxyStore().ProxyLeaseUsage(lease.Account) > 1 {
		return ResinLeaseDelete{}, false
	}
	settings, ok := s.env.ProxyStore().ResinSnapshot(lease.SnapshotID)
	if !ok || !resinCleanupConfigured(settings) {
		return ResinLeaseDelete{}, false
	}
	return newResinLeaseDelete(authID, settings, lease.Account), true
}
