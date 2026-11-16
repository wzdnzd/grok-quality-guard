package main

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

	proxy "grok-quality-guard/internal/proxy"
	resinpkg "grok-quality-guard/internal/proxy/resin"
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
		result.Error = sanitizeError(err)
		var typed *proxyManagementError
		if errors.As(err, &typed) {
			result.Code = typed.Code
		}
	}
	return result
}

func (p *Plugin) wakeProxyAutoBind() {
	if p == nil {
		return
	}
	select {
	case p.proxyMgr.wake <- struct{}{}:
	default:
	}
}
func (p *Plugin) proxyAutoBindConfig(_ *State) proxy.AutoBindConfig {
	return p.stateStore().ProxyAutoBindConfig()
}
func bindingForAccount(account AccountState) (proxy.Binding, bool) {
	if account.Proxy.SourceID != "" {
		return proxy.CloneBinding(account.Proxy), true
	}
	if account.Resin.Managed || account.Resin.Account != "" {
		return proxy.Binding{AuthID: account.AuthID, SourceID: resinSourceID, SourceKind: proxy.SourceResin,
			Generation: uint64(maxInt64(account.Resin.Generation, 0)), Lease: &proxy.LeaseRef{Provider: "resin", Account: account.Resin.Account}}, true
	}
	return proxy.CloneBinding(account.Proxy), false
}
func (p *Plugin) proxyBindingForAccount(account AccountState) (proxy.Binding, bool) {
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
func boolPointer(value bool) *bool       { return &value }
func proxyMaxInt(value, minimum int) int { return max(value, minimum) }
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

func (p *Plugin) proxyCatalog() *proxySourceCatalog {
	store := p.stateStore()
	epoch := store.ProxySourcesEpoch()
	p.proxyMgr.mu.Lock()
	cached := p.proxyMgr.catalog
	p.proxyMgr.mu.Unlock()
	if cached != nil && cached.Store == store && cached.Epoch == epoch {
		return cached
	}
	p.proxyMgr.buildMu.Lock()
	defer p.proxyMgr.buildMu.Unlock()
	p.proxyMgr.mu.Lock()
	cached = p.proxyMgr.catalog
	p.proxyMgr.mu.Unlock()
	if cached != nil && cached.Store == store && cached.Epoch == epoch {
		return cached
	}
	state := store.ProxySourcesSnapshot()
	catalog := &proxySourceCatalog{Store: store, Epoch: epoch, ByID: make(map[string]*proxySourceCandidate)}
	for i := range state.Nodes {
		node := state.Nodes[i]
		entry := proxySourceCandidate{ID: node.ID, Kind: proxy.SourceNode, Name: node.Name, Priority: node.Priority, Max: node.MaxBindings, Revision: node.Revision, Node: &node, Runtime: state.SourceRuntime[node.ID]}
		entry.Provider = proxyNodeProvider{node: node}
		entry.Error = node.Endpoint.Validate()
		catalog.Sources = append(catalog.Sources, entry)
	}
	for i := range state.Templates {
		template := state.Templates[i]
		entry := proxySourceCandidate{ID: template.ID, Kind: proxy.SourceTemplate, Name: template.Name, Priority: template.Priority, Max: template.MaxBindings, Revision: template.Revision, Template: &template, Runtime: state.SourceRuntime[template.ID]}
		if cached != nil && cached.Store == store && cached.ByID[template.ID] != nil && cached.ByID[template.ID].Revision == template.Revision {
			entry.Provider = cached.ByID[template.ID].Provider
			entry.Error = cached.ByID[template.ID].Error
		} else {
			compiled, err := proxy.CompileTemplate(template)
			entry.Error = err
			if err == nil {
				entry.Provider = proxyTemplateProvider{compiled: compiled}
			}
		}
		catalog.Sources = append(catalog.Sources, entry)
	}
	settings := p.ResinConfig()
	if !state.ResinSource.Deleted && (settings.Configured || settings.Enabled || settings.LocalOnly) {
		revision := max(state.ResinSource.Revision, 1)
		entry := proxySourceCandidate{ID: resinSourceID, Kind: proxy.SourceResin, Name: firstNonEmptyString(state.ResinSource.Name, "Resin"), Revision: revision, Resin: true, ResinSettings: &settings, Runtime: state.SourceRuntime[resinSourceID]}
		entry.ConfigVersion = state.ResinSource.ConfigVersion
		if !settings.LocalOnly {
			previous := (*proxySourceCandidate)(nil)
			if cached != nil && cached.Store == store {
				previous = cached.ByID[resinSourceID]
			}
			if previous != nil && previous.ResinSettings != nil && resinSnapshotID(*previous.ResinSettings) == resinSnapshotID(settings) {
				entry.Provider = previous.Provider
				entry.Error = previous.Error
			} else if settings.PlatformID == "" || settings.AdminURL == "" || settings.AdminToken == "" {
				entry.Error = proxyError("resin_validation_required", 409, "validate and save the Resin connection before binding")
			} else {
				entry.Provider, entry.Error = resinpkg.NewProvider(resinpkg.ProviderSettings{ProxyTemplate: settings.ProxyTemplate, Platform: settings.Platform, PlatformID: settings.PlatformID, ProxyToken: settings.ProxyToken, SnapshotID: resinSnapshotID(settings)}, nil)
			}
		}
		catalog.Sources = append(catalog.Sources, entry)
	}
	for i := range catalog.Sources {
		catalog.ByID[catalog.Sources[i].ID] = &catalog.Sources[i]
	}
	p.proxyMgr.mu.Lock()
	p.proxyMgr.catalog = catalog
	p.proxyMgr.mu.Unlock()
	return catalog
}

func (p *Plugin) selectProxySource(opts proxyBindOptions, excludeHash string) (*proxySourceCandidate, []string) {
	scope, err := normalizeProxyScope(opts.SourceScope)
	if err != nil {
		return nil, []string{err.Error()}
	}
	catalog := p.proxyCatalog()
	allowed := make(map[string]struct{}, len(opts.SourceIDs))
	for _, id := range opts.SourceIDs {
		allowed[id] = struct{}{}
	}
	// Lock order: state read lock then the short allocation lock. Neither is held
	// during generation, filesystem operations, or Host calls.
	store := p.stateStore()
	store.mu.RLock()
	p.proxyMgr.mu.Lock()
	defer store.mu.RUnlock()
	defer p.proxyMgr.mu.Unlock()
	transient := make(map[string]int, len(p.proxyMgr.reservations))
	for id, count := range p.proxyMgr.reservations {
		transient[id] = count
	}
	for authID, sourceID := range p.proxyMgr.reservedByAuth {
		if store.proxyPendingReserved[authID] == sourceID {
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
		load := store.proxyCounts[entry.ID] + store.proxyPendingCounts[entry.ID] + transient[entry.ID]
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
		p.proxyMgr.reservations[selected.ID]++
		if opts.AuthID != "" {
			p.proxyMgr.reservedByAuth[opts.AuthID] = selected.ID
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
func (p *Plugin) releaseProxySource(id, authID string) {
	p.proxyMgr.mu.Lock()
	if p.proxyMgr.reservations[id] > 0 {
		p.proxyMgr.reservations[id]--
	}
	delete(p.proxyMgr.reservedByAuth, authID)
	p.proxyMgr.mu.Unlock()
}
func (p *Plugin) enterProxyOperation(ctx context.Context) error {
	select {
	case p.proxyMgr.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *Plugin) leaveProxyOperation() { <-p.proxyMgr.slots }

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

func (p *Plugin) bindProxy(ctx context.Context, authID string, opts proxyBindOptions) (proxyBindingResult, error) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		err := proxyError("auth_id_required", 400, "auth_id is required")
		return failedProxyResult(authID, "failed", err), err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := p.enterProxyOperation(ctx); err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	defer p.leaveProxyOperation()
	lock := p.accountLock(authID)
	lock.Lock()
	defer lock.Unlock()
	if result, found, err := p.retryProxyAction(ctx, authID, opts, "proxy_bind"); found {
		return result, err
	}
	if p.stateStore().ProxyPending(authID) != "" {
		err := proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
		return failedProxyResult(authID, "reconcile_required", err), err
	}
	account, response, raw, err := p.resolveAuth(ctx, authID)
	if err != nil {
		return failedProxyResult(authID, "failed", err), err
	}
	current, err := proxyURLFromRaw(raw)
	if err != nil {
		return failedProxyResult(authID, "unknown", err), err
	}
	state := p.stateStore().ProxyAccount(authID)
	binding, managed, verifyErr := p.verifiedProxyBinding(state, current, opts.DryRun)
	if verifyErr != nil {
		return failedProxyResult(authID, "unknown", verifyErr), verifyErr
	}
	if opts.Auto {
		config := p.stateStore().ProxyAutoBindConfig()
		provider := firstNonEmptyString(authRawString(raw, "type"), authRawString(raw, "provider"), account.Provider, account.Type)
		if !isGrokProvider(provider) {
			return proxyBindingResult{AuthID: authID, Status: "skipped", Code: "auto_bind_provider_mismatch"}, nil
		}
		account.Disabled = authDisabled(raw)
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
	candidate, reasons := p.selectProxySource(opts, "")
	if candidate == nil {
		return proxyBindingResult{AuthID: authID, Status: "unavailable", Code: "no_available_sources", Error: firstNonEmptyString(strings.Join(reasons, "; "), "no available proxy source")}, nil
	}
	if !opts.DryRun {
		defer p.releaseProxySource(candidate.ID, authID)
	}
	rendered, lease, release, err := p.generateProxySource(ctx, *candidate, authID, raw, "", opts.DryRun)
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
	generation := max(binding.Generation, uint64(maxInt64(state.Resin.Generation, 0))) + 1
	next := proxy.Binding{AuthID: authID, SourceID: candidate.ID, SourceKind: candidate.Kind, SourceRevision: candidate.Revision, Generation: generation, EndpointHash: rendered.Endpoint.Fingerprint(), ResolvedValues: rendered.Values, Lease: lease, BoundAt: now, UpdatedAt: now}
	operation := proxy.Operation{ID: firstNonEmptyString(opts.OperationID, newProxyID("op")), AuthID: authID, Action: firstNonEmptyString(opts.Action, "proxy_bind"), Auto: opts.Auto, ExpectedHash: proxyURLHash(current), ExpectedGeneration: binding.Generation, PreviousSourceID: binding.SourceID, DesiredURL: rendered.URL, Binding: next, ExcludeAutoBind: state.AutoBind.Excluded, Status: "prepared", CreatedAt: now, UpdatedAt: now}
	return p.commitProxyOperationLocked(ctx, response, raw, operation, opts.Auto)
}

func (p *Plugin) updateCatalogProxy(authID, proxyURL string) {
	// Catalog slices are shared immutable snapshots. Keep writes in a small
	// observation overlay instead of modifying an array visible to other readers.
	p.proxyMgr.mu.Lock()
	p.proxyMgr.observations[authID] = proxyObservation{URL: proxyURL, Hash: proxyURLHash(proxyURL), Known: true, CheckedAt: time.Now().UTC()}
	p.proxyMgr.mu.Unlock()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
