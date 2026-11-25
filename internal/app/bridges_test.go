// Test-only seams into internal services. Production code does not use these wrappers.
package app

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	appauth "grok-quality-guard/internal/app/auth"
	catalogapp "grok-quality-guard/internal/app/catalog"
	execapp "grok-quality-guard/internal/app/executor"
	managementapp "grok-quality-guard/internal/app/management"
	appmodel "grok-quality-guard/internal/app/model"
	probeapp "grok-quality-guard/internal/app/probe"
	proxyapp "grok-quality-guard/internal/app/proxy"
	routingapp "grok-quality-guard/internal/app/routing"
	appstate "grok-quality-guard/internal/app/state"
	appconfig "grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
	"grok-quality-guard/internal/quality"
)

// Catalog seams.

const guardCatalogMissingRetention = catalogapp.GuardCatalogMissingRetention

func (p *Plugin) syncCatalogState(accounts []appmodel.Account) error {
	return p.catalogSvc.SyncState(accounts)
}

func readAuthProxyURL(path string) (string, bool) { return catalogapp.ReadAuthProxyURL(path) }

// Executor seams.

func classifyProbeStatus(status int) string { return execapp.ClassifyProbeStatus(status) }
func classifyUpstreamStatus(status int, body []byte) (string, string) {
	return execapp.ClassifyUpstreamStatus(status, body)
}
func classifyTransportError(err error) string { return execapp.ClassifyTransportError(err) }

func ensureTransparentReasoningEvidence(body []byte, sourceFormat string, reasoningRequired bool) []byte {
	return execapp.EnsureTransparentReasoningEvidence(body, sourceFormat, reasoningRequired)
}

func (p *Plugin) runTransparentStreamBridge(ctx context.Context, req execapp.Request, streamID string) {
	p.executorSvc.RunTransparentStreamBridge(ctx, req, streamID)
	// Verdict bookkeeping settles off the request path, and tests assert on it.
	p.waitSettled()
}

// waitSettled blocks until background verdict bookkeeping has finished.
func (p *Plugin) waitSettled() { p.taskWG.Wait() }

func (p *Plugin) startTransparentStreamBridge(req execapp.Request, streamID string) {
	p.executorSvc.StartTransparentStreamBridge(req, streamID)
}

func (p *Plugin) recordAttemptError(authID, class string, errValue error) error {
	return p.executorSvc.RecordAttemptError(authID, class, errValue)
}

// Management seams.

func (p *Plugin) actionPolicyUpdate(params map[string]any) (any, int, error) {
	return p.managementSvc.ActionPolicyUpdate(params)
}

func (p *Plugin) accountSummary(account appmodel.Account, stateAccount appstate.AccountState, proxyURL string) managementapp.AccountSummaryView {
	return p.managementSvc.AccountSummary(account, stateAccount, proxyURL)
}

func jsonHeaders() http.Header {
	return http.Header{"Content-Type": []string{"application/json; charset=utf-8"}, "Cache-Control": []string{"no-store"}}
}
func actionErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "upstream_error"
	default:
		return "internal_error"
	}
}

// Probe seams.

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
func probeBody(cfg appconfig.ProbeConfig, reasoning bool) []byte {
	return probeapp.ProbeBody(cfg, reasoning)
}
func nextProbeBackoff(cfg appconfig.ProbeConfig, previous appstate.AccountState) time.Duration {
	return probeapp.NextProbeBackoff(cfg, previous)
}

func (s *probeScheduler) queueEntries() []string {
	if s == nil || s.inner == nil {
		return nil
	}
	return s.inner.QueueEntries()
}

func (p *Plugin) markProbeSuccess(authID string, sig quality.Signals) error {
	return p.probeSvc.MarkProbeSuccess(authID, sig)
}
func (p *Plugin) handleProbeFailure(authID string, raw map[string]json.RawMessage, response pluginapi.HostAuthGetResponse, reEnabled bool, verdict quality.QualityVerdict, errProbe error) error {
	return p.probeSvc.HandleProbeFailure(authID, raw, response, reEnabled, verdict, errProbe)
}

// Proxy seams.

func proxyURLHash(value string) string { return proxyapp.URLHash(value) }

func proxyBindOptionsFromParams(params map[string]any) (proxyapp.BindOptions, error) {
	return proxyapp.BindOptionsFromParams(params)
}

func (p *Plugin) selectProxySource(opts proxyapp.BindOptions, excludeHash string) (*proxyapp.SourceCandidate, []string) {
	return p.proxySvc.SelectSource(opts, excludeHash)
}

func (p *Plugin) createProxyNode(input proxyapp.NodeInput) (proxy.Node, error) {
	return p.proxySvc.CreateNode(input)
}

func (p *Plugin) importProxyNodes(inputs []proxyapp.NodeInput, text string) (map[string]any, error) {
	return p.proxySvc.ImportNodes(inputs, text)
}
func (p *Plugin) updateProxyNode(input proxyapp.NodeInput) (proxy.Node, map[string]any, error) {
	return p.proxySvc.UpdateNode(input)
}
func (p *Plugin) createProxyTemplate(input proxyapp.TemplateInput) (proxy.Template, error) {
	return p.proxySvc.CreateTemplate(input)
}
func (p *Plugin) updateProxyTemplate(input proxyapp.TemplateInput) (proxy.Template, map[string]any, error) {
	return p.proxySvc.UpdateTemplate(input)
}
func (p *Plugin) bindProxy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.Bind(ctx, authID, opts)
}
func (p *Plugin) clearProxy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.Clear(ctx, authID, opts)
}
func (p *Plugin) clearProxyWithPolicy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.ClearWithPolicy(ctx, authID, opts)
}
func (p *Plugin) rotateProxy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.Rotate(ctx, authID, opts)
}
func (p *Plugin) updateProxyAutoBind(params map[string]any) (proxy.AutoBindConfig, bool, error) {
	return p.proxySvc.UpdateAutoBind(params)
}
func (p *Plugin) proxyApplyTemplateToAccounts(ctx context.Context, template proxy.Template, ids []string) []proxyapp.BindingResult {
	return p.proxySvc.ApplyTemplateToAccounts(ctx, template, ids)
}
func (p *Plugin) actionProxySourcePreview(params map[string]any) (any, int, error) {
	return p.proxySvc.ActionSourcePreview(params)
}
func (p *Plugin) actionProxyImportNodes(params map[string]any) (any, int, error) {
	return p.proxySvc.ActionImportNodes(params)
}
func (p *Plugin) proxyCatalog() *proxyapp.SourceCatalog { return p.proxySvc.Catalog() }
func (p *Plugin) proxyAutoBindScan(ctx context.Context) { p.proxySvc.AutoBindScan(ctx) }
func (p *Plugin) deleteProxyNode(id string, force bool) (map[string]any, error) {
	return p.proxySvc.DeleteProxyNode(id, force)
}
func (p *Plugin) proxySourceDetails(id string) (any, error)      { return p.proxySvc.SourceDetails(id) }
func (p *Plugin) reconcileProxyOperations(ctx context.Context)   { p.proxySvc.ReconcileOperations(ctx) }
func (p *Plugin) proxyExistingNode(id string) (proxy.Node, bool) { return p.proxySvc.ExistingNode(id) }

func seedResinBinding(t *testing.T, plugin *Plugin, account string, generation int64) {
	t.Helper()
	_, _, raw, err := plugin.ResolveAuth(context.Background(), "auth-1")
	if err != nil {
		t.Fatal(err)
	}
	current := appauth.RawString(raw, "proxy_url")
	const sourceID = "resin-test"
	now := time.Now().UTC()
	template := proxy.Template{
		ID:                      sourceID,
		Kind:                    proxy.TemplateResin,
		Name:                    "Resin test",
		Enabled:                 true,
		Revision:                1,
		ConfigVersion:           1,
		Protocol:                proxy.ProtocolSOCKS5H,
		Host:                    "resin",
		Port:                    2260,
		UsernamePattern:         "grok-{account}-{suffix}",
		UsernamePatternEncoding: "literal",
		HasUser:                 true,
		Placeholders: []proxy.Placeholder{
			{Name: proxy.ReservedAccountPlaceholder, Kind: proxy.PlaceholderReserved},
			{Name: "suffix", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 12}},
		},
		UpdatePolicy: proxy.TemplateUpdateKeepBound,
		CreatedAt:    now,
		UpdatedAt:    now,
		ValidatedAt:  now,
	}
	if err := plugin.stateStore().Update(func(state *appstate.State) error {
		state.Proxy.Templates = append(state.Proxy.Templates, template)
		if state.Proxy.ResinSources == nil {
			state.Proxy.ResinSources = make(map[string]proxy.ResinSourceConfig)
		}
		state.Proxy.ResinSources[sourceID] = proxy.ResinSourceConfig{
			AccountFormat: "grok-{account}-{suffix}",
			LocalOnly:     true,
		}
		stateAccount := plugin.ensureStateAccount(state, "auth-1")
		stateAccount.Proxy = proxy.Binding{
			AuthID:         "auth-1",
			SourceID:       sourceID,
			SourceKind:     proxy.SourceResin,
			SourceRevision: 1,
			Generation:     uint64(generation),
			EndpointHash:   proxyURLHash(current),
			Lease:          &proxy.LeaseRef{Provider: "resin-local", Account: account},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Quality seams.

func NewStreamAnalyzer() *quality.StreamAnalyzer {
	return quality.NewStreamAnalyzer()
}

func NewStreamAnalyzerWithProtocol(protocol string) *quality.StreamAnalyzer {
	return quality.NewStreamAnalyzerWithProtocol(protocol)
}

func NewStreamAnalyzerWithProtocolAt(protocol string, startedAt time.Time) *quality.StreamAnalyzer {
	return quality.NewStreamAnalyzerWithProtocolAt(protocol, startedAt)
}

func ClassifyQualityHold(sig quality.Signals, minOutput int64, burstGuard bool) quality.QualityVerdict {
	return quality.ClassifyQualityHold(sig, minOutput, burstGuard)
}

func qualityIsBurstDump(sig quality.Signals, minOutput int64) bool {
	return quality.QualityIsBurstDump(sig, minOutput)
}

func isDumpClass(class string) bool {
	return quality.IsDumpClass(class)
}

func classifyFinal(sig quality.Signals, cfg appconfig.QualityConfig, reasoningRequired bool, minOutput int64, streaming bool) quality.QualityVerdict {
	return quality.ClassifyFinal(sig, cfg, reasoningRequired, minOutput, streaming)
}

func classifyProbeResult(sig quality.Signals, cfg appconfig.QualityConfig, expectedMatched, reasoningRequired bool) quality.QualityVerdict {
	return quality.ClassifyProbeResult(sig, cfg, expectedMatched, reasoningRequired)
}

func classifyPassiveAudit(sig quality.Signals, cfg appconfig.QualityConfig) quality.QualityVerdict {
	return quality.ClassifyPassiveAudit(sig, cfg)
}

func classifyNonStreamResult(sig quality.Signals, cfg appconfig.QualityConfig, reasoningRequired bool, minOutput int64) quality.QualityVerdict {
	return quality.ClassifyNonStreamResult(sig, cfg, reasoningRequired, minOutput)
}

func reasoningRequiredForModel(modelName string, body []byte) bool {
	return quality.ReasoningRequiredForModel(modelName, body)
}

func (p *Plugin) applyGuardDisable(authID, kind string, duration time.Duration, class, source string, strike bool) error {
	return p.qualitySvc.ApplyGuardDisable(authID, kind, duration, class, source, strike)
}

func (p *Plugin) applyAutoCooldown(authID string, verdict quality.QualityVerdict, source string) error {
	return p.qualitySvc.ApplyAutoCooldown(authID, verdict, source)
}

func (p *Plugin) ManualCooldown(ctx context.Context, authID string, duration time.Duration) error {
	return p.qualitySvc.ManualCooldown(ctx, authID, duration)
}

func (p *Plugin) ManualDisable(ctx context.Context, authID, reason string) error {
	return p.qualitySvc.ManualDisable(ctx, authID, reason)
}

func (p *Plugin) ManualRestore(ctx context.Context, authID string) error {
	return p.qualitySvc.ManualRestore(ctx, authID)
}

// Routing seams.

func (p *Plugin) rebuildSelectionIndex() error { return p.routingSvc.RebuildIndex() }
func (p *Plugin) selectionSessionKey(req execapp.Request, body []byte, sourceFormat string) string {
	return p.routingSvc.SessionKey(req, body, sourceFormat)
}

func (p *Plugin) newTransparentSelection(req execapp.Request, body []byte, sourceFormat string) (*routingapp.Selection, error) {
	return p.routingSvc.NewTransparentSelection(req, body, sourceFormat)
}

func (p *Plugin) transparentCandidates(ctx context.Context) ([]appmodel.Account, error) {
	return p.routingSvc.Candidates(ctx)
}

func isTransparentSelectionHardFailure(class string) bool {
	return routingapp.IsTransparentSelectionHardFailure(class)
}

const (
	transparentSourcePreferred = routingapp.TransparentSourcePreferred
)
