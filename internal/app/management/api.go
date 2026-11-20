package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/action"
	"grok-quality-guard/internal/app/dashboard"
	"grok-quality-guard/internal/app/i18n"
	proxy "grok-quality-guard/internal/proxy"
)

type rpcManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type apiActionError struct {
	Code       string
	MessageKey string
	Args       map[string]any
	Status     int
}

func (e *apiActionError) Error() string { return e.MessageKey }

func (e *apiActionError) ErrorCode() string { return e.Code }

func newAPIActionError(code, messageKey string, status int, args map[string]any) error {
	return &apiActionError{Code: code, MessageKey: messageKey, Args: args, Status: status}
}

func requestLocale(headers http.Header) string {
	if headers == nil {
		return i18n.DefaultLocale
	}
	values := make([]string, 0, 1)
	for name, list := range headers {
		if strings.EqualFold(name, "Accept-Language") {
			values = append(values, list...)
		}
	}
	return i18n.Negotiate(strings.Join(values, ","))
}

func localizedErrorMessage(locale, code string, status int, messageKey string, args map[string]any) string {
	candidates := []string{messageKey, "api.error." + code, statusErrorKey(status)}
	for _, key := range candidates {
		if key != "" && i18n.Has(locale, key) {
			return i18n.T(locale, key, args)
		}
	}
	return i18n.T(locale, "api.error.internal", nil)
}

func statusErrorKey(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "api.error.invalid_request"
	case http.StatusForbidden:
		return "api.error.forbidden"
	case http.StatusNotFound:
		return "api.error.not_found"
	case http.StatusConflict:
		return "api.error.conflict"
	case http.StatusTooManyRequests:
		return "api.error.rate_limited"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "api.error.upstream_error"
	default:
		return "api.error.internal"
	}
}

type restoreBatchItem struct {
	AuthID      string `json:"auth_id"`
	StateBefore string `json:"state_before"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
}

type restoreBatchResult struct {
	Items                []restoreBatchItem `json:"items"`
	Count                int                `json:"count"`
	Restored             int                `json:"restored"`
	Skipped              int                `json:"skipped"`
	ExternalRestored     int                `json:"external_restored"`
	ExternalLeftDisabled int                `json:"external_left_disabled"`
	Failed               int                `json:"failed"`
}

type restorePreviewResult struct {
	Count            int `json:"count"`
	Restorable       int `json:"restorable"`
	Healthy          int `json:"healthy"`
	ExternalDisabled int `json:"external_disabled"`
}

type disableBatchItem struct {
	AuthID      string `json:"auth_id"`
	StateBefore string `json:"state_before"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
}

type disableBatchResult struct {
	Items        []disableBatchItem `json:"items"`
	Count        int                `json:"count"`
	Disabled     int                `json:"disabled"`
	Skipped      int                `json:"skipped"`
	Failed       int                `json:"failed"`
	ModifiedAuth bool               `json:"modified_auth"`
}

type probeBatchItem struct {
	AuthID      string `json:"auth_id"`
	StateBefore string `json:"state_before"`
	StateAfter  string `json:"state_after,omitempty"`
	Status      string `json:"status"`
	Restored    bool   `json:"restored,omitempty"`
	Error       string `json:"error,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
}

type probeBatchResult struct {
	Items    []probeBatchItem `json:"items"`
	Count    int              `json:"count"`
	Probed   int              `json:"probed"`
	Restored int              `json:"restored"`
	Skipped  int              `json:"skipped"`
	Failed   int              `json:"failed"`
}

type accountCandidate struct {
	authID  string
	account Account
}

type accountSummaryView struct {
	AuthID            string           `json:"auth_id"`
	AuthIDDisplay     string           `json:"auth_id_display"`
	Display           string           `json:"display"`
	DisplayFull       string           `json:"display_full"`
	Email             string           `json:"email,omitempty"`
	Label             string           `json:"label,omitempty"`
	State             string           `json:"state"`
	LastReason        string           `json:"last_reason,omitempty"`
	DisableReason     string           `json:"disable_reason,omitempty"`
	LastAttempt       *AttemptEvidence `json:"last_attempt,omitempty"`
	LastObservedAt    time.Time        `json:"last_observed_at,omitzero"`
	CooldownUntil     time.Time        `json:"cooldown_until,omitzero"`
	ProbeBackoffUntil time.Time        `json:"probe_backoff_until,omitzero"`
	ProxyStatus       string           `json:"proxy_status"`
	Proxy             string           `json:"proxy,omitempty"`
	ProxySourceID     string           `json:"proxy_source_id,omitempty"`
	ProxySourceKind   string           `json:"proxy_source_kind,omitempty"`
	ProxySourceName   string           `json:"proxy_source_name,omitempty"`
	ProxyManaged      bool             `json:"proxy_managed,omitempty"`
	ProxyGeneration   uint64           `json:"proxy_generation,omitempty"`
	ProxyLeaseAccount string           `json:"proxy_lease_account,omitempty"`
	ProxyPending      bool             `json:"proxy_pending,omitempty"`
	ProxyInspected    bool             `json:"proxy_inspected,omitempty"`
	BindingVersion    uint64           `json:"binding_version"`
	AutoBindExcluded  bool             `json:"auto_bind_excluded,omitempty"`
	TPS               float64          `json:"tps,omitempty"`
	OutputTokens      int64            `json:"output_tokens,omitempty"`
	ReasoningTokens   int64            `json:"reasoning_tokens,omitempty"`
	EncryptedBytes    int64            `json:"encrypted_bytes,omitempty"`
}

type accountView struct {
	accountSummaryView
	Evidence               *QualityEvidence `json:"evidence,omitempty"`
	LastProbeAt            time.Time        `json:"last_probe_at,omitzero"`
	DisableKind            string           `json:"disable_kind,omitempty"`
	StrikeMissingThinking  bool             `json:"strike_missing_thinking,omitempty"`
	QualityFailureAt       time.Time        `json:"quality_failure_at,omitzero"`
	HealthyStreak          int              `json:"healthy_streak,omitempty"`
	HealthyStreakStartedAt time.Time        `json:"healthy_streak_started_at,omitzero"`
	SoftStrikes            int              `json:"soft_strikes,omitempty"`
	ErrorStrikes           int              `json:"error_strikes,omitempty"`
	ThinkingStrikes        int              `json:"thinking_strikes,omitempty"`
	TTFTMS                 int64            `json:"ttft_ms,omitempty"`
	VisibleTokens          int64            `json:"visible_tokens,omitempty"`
}

func (s *Service) HandleManagement(request []byte) ([]byte, error) {
	var req rpcManagementRequest
	locale := i18n.DefaultLocale
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return okEnvelope(managementResponse{StatusCode: http.StatusBadRequest, Headers: jsonHeadersFor(locale), Body: jsonError("invalid_request", localizedErrorMessage(locale, "invalid_request", http.StatusBadRequest, "", nil))})
		}
		locale = requestLocale(req.Headers)
	}
	if strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(strings.TrimRight(req.Path, "/"), "/locales") {
		return okEnvelope(managementResponse{StatusCode: http.StatusOK, Headers: jsonHeadersFor(locale), Body: i18n.ResourcesJSON()})
	}
	if strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(strings.TrimRight(req.Path, "/"), "/dashboard") {
		return okEnvelope(managementResponse{StatusCode: http.StatusOK, Headers: htmlHeadersFor(locale), Body: []byte(dashboard.HTML())})
	}
	if !strings.EqualFold(req.Method, http.MethodPost) {
		return okEnvelope(managementResponse{StatusCode: http.StatusMethodNotAllowed, Headers: jsonHeadersFor(locale), Body: jsonError("method_not_allowed", localizedErrorMessage(locale, "method_not_allowed", http.StatusMethodNotAllowed, "", nil))})
	}
	var payload action.Request
	if len(req.Body) > 0 {
		if errUnmarshal := json.Unmarshal(req.Body, &payload); errUnmarshal != nil {
			return okEnvelope(managementResponse{StatusCode: http.StatusBadRequest, Headers: jsonHeadersFor(locale), Body: jsonError("invalid_json", localizedErrorMessage(locale, "invalid_json", http.StatusBadRequest, "", nil))})
		}
	}

	if payload.Params == nil {
		payload.Params = map[string]any{}
	}
	ctx := withRequestLocale(context.Background(), locale)
	data, status, errAction := s.handleAction(ctx, payload)
	if errAction != nil {
		if status == 0 {
			status = http.StatusBadRequest
		}
		if errors.Is(errAction, context.DeadlineExceeded) || errors.Is(errAction, context.Canceled) {
			status = http.StatusServiceUnavailable
			errAction = newAPIActionError("busy", "api.error.busy", status, nil)
		}
		code := actionErrorCode(status)
		messageKey := ""
		var args map[string]any
		var actionErr *apiActionError
		if errors.As(errAction, &actionErr) {
			if actionErr.Code != "" {
				code = actionErr.Code
			}
			if actionErr.Status != 0 {
				status = actionErr.Status
			}
			messageKey = actionErr.MessageKey
			args = actionErr.Args
		}
		var proxyErr *proxyManagementError
		if errors.As(errAction, &proxyErr) {
			code = proxyErr.Code
			if proxyErr.Status != 0 {
				status = proxyErr.Status
			}
		}
		var codedError interface{ ErrorCode() string }
		if errors.As(errAction, &codedError) {
			if value := strings.TrimSpace(codedError.ErrorCode()); value != "" {
				code = value
			}
		}
		var statusError interface{ HTTPStatus() int }
		if errors.As(errAction, &statusError) && statusError.HTTPStatus() != 0 {
			status = statusError.HTTPStatus()
		}
		detail := map[string]any{"message": localizedErrorMessage(locale, code, status, messageKey, args)}
		var invalid *proxy.ValidationError
		if errors.As(errAction, &invalid) {
			code = invalid.Code
			detail["message"] = localizedErrorMessage(locale, code, status, "", nil)
			detail["path"] = invalid.Path
		}
		detail["code"] = code
		body, _ := json.Marshal(map[string]any{"ok": false, "error": detail})
		headers := jsonHeadersFor(locale)
		if code == "catalog_loading" || code == "busy" {
			headers.Set("Retry-After", "1")
		}
		return okEnvelope(managementResponse{StatusCode: status, Headers: headers, Body: body})
	}
	if status == 0 {
		status = http.StatusOK
	}
	return okEnvelope(managementResponse{StatusCode: status, Headers: jsonHeadersFor(locale), Body: jsonOK(localizeResponseData(locale, data))})
}

const heavyActionBudget = 2 * time.Second

func isHeavyManagementAction(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "accounts.list", "proxy.summary", "proxy.bindings.list", "proxy.sources.list", "tasks.center.list":
		return true
	default:
		return false
	}
}

func (s *Service) acquireHeavyAction() bool {
	if s == nil || s.heavyGate == nil {
		return true
	}
	select {
	case s.heavyGate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releaseHeavyAction() {
	if s == nil || s.heavyGate == nil {
		return
	}
	<-s.heavyGate
}

func (s *Service) heavyFlightKey(ctx context.Context, request action.Request, actionName string) (string, bool) {
	raw, err := json.Marshal(request.Params)
	if err != nil {
		return "", false
	}
	return actionName + "\x00" + requestLocaleFromContext(ctx) + "\x00" + string(raw), true
}

func (s *Service) beginHeavyFlight(key string) (*heavyFlight, bool) {
	s.heavyMu.Lock()
	defer s.heavyMu.Unlock()
	if s.heavyFlights == nil {
		s.heavyFlights = make(map[string]*heavyFlight)
	}
	if flight := s.heavyFlights[key]; flight != nil {
		return flight, false
	}
	flight := &heavyFlight{done: make(chan struct{})}
	s.heavyFlights[key] = flight
	return flight, true
}

func (s *Service) finishHeavyFlight(key string, flight *heavyFlight, data any, status int, err error) {
	s.heavyMu.Lock()
	if s.heavyFlights[key] == flight {
		delete(s.heavyFlights, key)
	}
	flight.data = data
	flight.status = status
	flight.err = err
	close(flight.done)
	s.heavyMu.Unlock()
}

func (s *Service) awaitHeavyFlight(ctx context.Context, flight *heavyFlight) (any, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, http.StatusServiceUnavailable, ctx.Err()
	case <-flight.done:
		return flight.data, flight.status, flight.err
	}
}

func (s *Service) handleAction(ctx context.Context, request action.Request) (data any, status int, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	actionName := strings.ToLower(strings.TrimSpace(request.Action))
	if !isHeavyManagementAction(actionName) {
		return s.executeAction(ctx, request, actionName)
	}
	key, shareable := s.heavyFlightKey(ctx, request, actionName)
	if !shareable {
		if !s.acquireHeavyAction() {
			return nil, http.StatusServiceUnavailable, newAPIActionError("busy", "api.error.busy", http.StatusServiceUnavailable, nil)
		}
		defer s.releaseHeavyAction()
		actionCtx, cancel := context.WithTimeout(ctx, heavyActionBudget)
		defer cancel()
		return s.executeAction(actionCtx, request, actionName)
	}
	flight, leader := s.beginHeavyFlight(key)
	if !leader {
		return s.awaitHeavyFlight(ctx, flight)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.finishHeavyFlight(key, flight, nil, http.StatusInternalServerError, errors.New("management action panic"))
			panic(recovered)
		}
		s.finishHeavyFlight(key, flight, data, status, err)
	}()
	if !s.acquireHeavyAction() {
		return nil, http.StatusServiceUnavailable, newAPIActionError("busy", "api.error.busy", http.StatusServiceUnavailable, nil)
	}
	defer s.releaseHeavyAction()
	actionCtx, cancel := context.WithTimeout(ctx, heavyActionBudget)
	defer cancel()
	data, status, err = s.executeAction(actionCtx, request, actionName)
	return data, status, err
}

func (s *Service) executeAction(ctx context.Context, request action.Request, actionName string) (any, int, error) {
	switch actionName {
	case "overview":
		return s.actionOverview(ctx)
	case "accounts.list":
		return s.actionAccountsList(ctx, request.Params)
	case "accounts.get":
		return s.actionAccountGet(ctx, request.Params)
	case "accounts.proxy_metadata":
		return s.actionAccountProxyMetadata(ctx, request.Params)
	case "accounts.probe":
		return s.actionProbe(ctx, request.Params)
	case "accounts.probe_selected":
		return s.actionProbeSelected(ctx, request.Params)
	case "accounts.cooldown":
		return s.actionCooldown(ctx, request.Params)
	case "accounts.disable":
		return s.actionDisable(ctx, request.Params)
	case "accounts.restore":
		return s.actionRestore(ctx, request.Params)
	case "accounts.restore_preview":
		return s.actionRestorePreview(ctx, request.Params)
	case "accounts.restore_selected":
		return s.actionRestoreSelected(ctx, request.Params)
	case "accounts.disable_selected":
		return s.actionDisableSelected(ctx, request.Params)
	case "proxy.summary":
		return s.actionProxySummary(ctx)
	case "proxy.sources.list":
		return s.actionProxySourcesList(ctx, request.Params)
	case "proxy.sources.get":
		return s.actionProxySourceGet(request.Params)
	case "proxy.sources.endpoint":
		return s.actionProxySourceEndpoint(request.Params)
	case "proxy.sources.validate":
		return s.actionProxySourceValidate(request.Params)
	case "proxy.sources.preview":
		return s.actionProxySourcePreview(request.Params)
	case "proxy.sources.test":
		return s.actionProxySourceTest(ctx, request.Params)
	case "proxy.sources.test_selected":
		return s.actionProxySourcesTestSelected(ctx, request.Params)
	case "proxy.sources.delete_selected":
		return s.actionProxySourcesDeleteSelected(ctx, request.Params)
	case "proxy.sources.create":
		return s.actionProxySourceCreate(request.Params)
	case "proxy.sources.import_nodes":
		return s.actionProxyImportNodes(request.Params)
	case "proxy.sources.update":
		return s.actionProxySourceUpdate(request.Params)
	case "proxy.sources.delete":
		return s.actionProxySourceDelete(request.Params)
	case "proxy.bindings.list":
		return s.actionProxyBindingsList(ctx, request.Params)
	case "proxy.bindings.endpoint":
		return s.actionProxyBindingEndpoint(ctx, request.Params)
	case "proxy.bindings.bind":
		return s.actionProxyBind(ctx, request.Params)
	case "proxy.bindings.clear":
		return s.actionProxyClear(ctx, request.Params)
	case "proxy.bindings.rotate":
		return s.actionProxyRotate(ctx, request.Params)
	case "proxy.bindings.adopt":
		return s.actionProxyBatch("adopt", request.Params)
	case "proxy.bindings.set_auto_bind":
		return s.actionProxyBindingsAutoBind(request.Params)
	case "proxy.template.apply":
		return s.actionProxyTemplateApply(ctx, request.Params)
	case "proxy.bind_unbound":
		return s.actionProxyBindUnbound(ctx, request.Params)
	case "proxy.auto_bind.get":
		return s.actionProxyAutoBindGet()
	case "proxy.auto_bind.update":
		return s.actionProxyAutoBindUpdate(request.Params)
	case "proxy.auto_bind.run":
		return s.actionProxyAutoBindRun()
	case "proxy.changes.list":
		return s.actionProxyChangesList(request.Params)
	case "proxy.changes.get":
		return s.actionProxyChange(request.Params, "get")
	case "proxy.changes.retry":
		return s.actionProxyChange(request.Params, "retry")
	case "proxy.changes.cancel":
		return s.actionProxyChange(request.Params, "cancel")
	case "resin.cleanup.list":
		return s.actionResinCleanup(request.Params, "list")
	case "resin.cleanup.retry":
		return s.actionResinCleanup(request.Params, "retry")
	case "resin.cleanup.pause":
		return s.actionResinCleanup(request.Params, "pause")

	case "policy.get":
		return s.actionPolicyGet()
	case "policy.update":
		return s.actionPolicyUpdate(request.Params)
	case "policy.reset":
		return s.actionPolicyReset()
	case "state_file.update":
		return s.actionStateFileUpdate(request.Params)
	case "events.list":
		return s.actionEventsList(request.Params, requestLocaleFromContext(ctx))
	case "tasks.get":
		return s.actionTaskGet(request.Params, requestLocaleFromContext(ctx))
	case "tasks.cancel":
		return s.actionTaskCancel(request.Params, requestLocaleFromContext(ctx))
	case "tasks.list":
		return s.actionTaskList(request.Params, requestLocaleFromContext(ctx))
	case "tasks.center.list":
		return s.actionTaskCenterList(ctx, request.Params, requestLocaleFromContext(ctx))
	case "tasks.center.queues":
		return s.actionTaskCenterQueues(requestLocaleFromContext(ctx))
	case "tasks.center.queue":
		return s.actionTaskCenterQueue(request.Params)
	case "tasks.center.get", "tasks.center.items":
		return s.actionTaskCenterDetail(request.Params, requestLocaleFromContext(ctx))
	case "tasks.center.cancel":
		return s.actionTaskCancel(request.Params, requestLocaleFromContext(ctx))
	case "tasks.center.delete":
		return s.actionTaskCenterDelete(request.Params)
	default:
		return nil, http.StatusBadRequest, newAPIActionError("unknown_action", "api.error.unknown_action", http.StatusBadRequest, nil)
	}

}

const overviewCountsTTL = 5 * time.Second

func cloneIntMap(value map[string]int) map[string]int {
	cloned := make(map[string]int, len(value))
	for key, count := range value {
		cloned[key] = count
	}
	return cloned
}

func (s *Service) overviewStateCounts(store *Store, catalog []Account, order []int32, key overviewCountsKey) map[string]int {
	s.overviewMu.Lock()
	defer s.overviewMu.Unlock()
	if s.overviewCounts != nil && s.overviewKey == key && time.Since(s.overviewAt) < overviewCountsTTL {
		return cloneIntMap(s.overviewCounts)
	}
	counts := catalogStateCountsForOrder(store, catalog, order)
	if store == nil || store.OverviewRevision() == key.StateRevision {
		s.overviewKey = key
		s.overviewCounts = cloneIntMap(counts)
		s.overviewAt = time.Now()
	}
	return counts
}

func (s *Service) rememberOverviewStateCounts(store *Store, key overviewCountsKey, counts map[string]int) {
	if counts == nil || store != nil && store.OverviewRevision() != key.StateRevision {
		return
	}
	s.overviewMu.Lock()
	s.overviewKey = key
	s.overviewCounts = cloneIntMap(counts)
	s.overviewAt = time.Now()
	s.overviewMu.Unlock()
}

func (s *Service) actionOverview(ctx context.Context) (any, int, error) {
	if errCatalog := s.ensureCatalog(ctx); errCatalog != nil {
		return nil, http.StatusBadGateway, errCatalog
	}
	catalog, idOrder, catalogRevision := s.env.CatalogIDOrderSnapshot()
	store := s.stateStore()
	key := overviewCountsKey{CatalogRevision: catalogRevision, StateRevision: store.OverviewRevision(), AuthCount: len(catalog)}
	counts := s.overviewStateCounts(store, catalog, catalogIDOrderFallback(catalog, idOrder), key)
	recentAnomalies := 0
	if store != nil {
		recentAnomalies = store.RecentEventCount(time.Now().UTC().Add(-24 * time.Hour))
	}
	enabled := s.env.ConfigEnabled()
	policy := s.env.Policy()
	stateFile := ""
	updatedAt := time.Time{}
	if store != nil {
		stateFile = store.Path()
		updatedAt = store.UpdatedAt()
	}
	return map[string]any{
		"enabled":              enabled,
		"updated_at":           updatedAt,
		"auth_count":           len(catalog),
		"states":               counts,
		"recent_anomalies_24h": recentAnomalies,
		"policy":               policy,
		"state_file":           stateFile,
	}, http.StatusOK, nil
}

const accountListChunk = 1024

func (s *Service) actionAccountsList(ctx context.Context, params map[string]any) (any, int, error) {
	if errCatalog := s.ensureCatalog(ctx); errCatalog != nil {
		return nil, http.StatusBadGateway, errCatalog
	}
	store := s.stateStore()
	catalog, idOrder, catalogRevision := s.env.CatalogIDOrderSnapshot()
	overviewKey := overviewCountsKey{CatalogRevision: catalogRevision, StateRevision: store.OverviewRevision(), AuthCount: len(catalog)}
	idOrder = catalogIDOrderFallback(catalog, idOrder)
	filterState := strings.ToLower(strings.TrimSpace(asString(params["state"])))
	query := strings.ToLower(strings.TrimSpace(asString(params["query"])))
	page := action.Int(params, "page", 1)
	pageSize := action.Int(params, "page_size", 50)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 10000 {
		pageSize = 50
	}
	idsOnly := action.Bool(params, "ids_only")
	allIDs := idsOnly && action.Bool(params, "all_ids")
	stateCounts := make(map[string]int, 8)
	pageCap := min(pageSize, len(catalog))
	pageIndexes := make([]int, 0, pageCap)
	pageIDs := make([]string, 0, pageCap)
	allMatchedIDs := make([]string, 0)
	chunkIDs := make([]string, accountListChunk)
	chunkStates := make([]string, accountListChunk)
	start := int64(page-1) * int64(pageSize)
	matched := 0
	for base := 0; base < len(idOrder); base += accountListChunk {
		if ctx != nil && ctx.Err() != nil {
			return nil, http.StatusServiceUnavailable, newAPIActionError("busy", "api.error.busy", http.StatusServiceUnavailable, nil)
		}
		end := min(base+accountListChunk, len(idOrder))
		count := end - base
		ids := chunkIDs[:count]
		states := chunkStates[:count]
		for offset, catalogIndex := range idOrder[base:end] {
			ids[offset] = catalog[int(catalogIndex)].ID
		}
		store.FillRoutingStates(ids, states)
		for offset, catalogIndex := range idOrder[base:end] {
			account := catalog[int(catalogIndex)]
			state := catalogAccountState(account, states[offset])
			stateCounts[state]++
			if filterState != "" && state != filterState {
				continue
			}
			if query != "" && !catalogAccountMatches(account, query) {
				continue
			}
			matchIndex := int64(matched)
			matched++
			if allIDs {
				allMatchedIDs = append(allMatchedIDs, account.ID)
				continue
			}
			if matchIndex < start || len(pageIndexes)+len(pageIDs) >= pageSize {
				continue
			}
			if idsOnly {
				pageIDs = append(pageIDs, account.ID)
			} else {
				pageIndexes = append(pageIndexes, int(catalogIndex))
			}
		}
	}
	s.rememberOverviewStateCounts(store, overviewKey, stateCounts)
	proxyService := s.env.Proxy()
	if idsOnly {
		if allIDs {
			pageIDs = allMatchedIDs
		}
		return map[string]any{
			"items":           pageIDs,
			"page":            page,
			"page_size":       pageSize,
			"total":           matched,
			"state_counts":    stateCounts,
			"binding_version": proxyService.BindingVersion(),
		}, http.StatusOK, nil
	}
	pageAccounts := make([]Account, len(pageIndexes))
	for index, catalogIndex := range pageIndexes {
		pageAccounts[index] = catalog[catalogIndex]
	}
	proxyMetadataPending := false
	proxyMetadataPendingIDs := make([]string, 0)
	for _, account := range pageAccounts {
		if authProxyMetadataPending(account, "") {
			proxyMetadataPending = true
			proxyMetadataPendingIDs = append(proxyMetadataPendingIDs, account.ID)
		}
	}
	pageIDs = catalogAccountIDs(pageAccounts)
	bindings := proxyService.BindingStatesForAccounts(pageAccounts)
	bindingVersion := proxyService.BindingVersion()
	if len(bindings) > 0 {
		bindingVersion = bindings[0].Version
	}
	var views any
	if action.Bool(params, "summary") {
		pageSummaries := store.AccountStateSummariesForIDs(pageIDs)
		items := make([]accountSummaryView, 0, len(pageAccounts))
		for index, account := range pageAccounts {
			items = append(items, s.accountSummaryWithBinding(account, accountStateFromSummary(pageSummaries[index]), bindings[index]))
		}
		views = items
	} else {
		pageStates := store.AccountStatesForIDs(pageIDs)
		items := make([]accountView, 0, len(pageAccounts))
		for index, account := range pageAccounts {
			items = append(items, s.accountViewWithBinding(account, pageStates[index], bindings[index]))
		}
		views = items
	}
	return map[string]any{
		"items":                      views,
		"page":                       page,
		"page_size":                  pageSize,
		"total":                      matched,
		"state_counts":               stateCounts,
		"proxy_metadata_pending":     proxyMetadataPending,
		"proxy_metadata_pending_ids": proxyMetadataPendingIDs,
		"binding_version":            bindingVersion,
	}, http.StatusOK, nil
}

func catalogIDOrderFallback(catalog []Account, order []int32) []int32 {
	if len(order) == len(catalog) {
		valid := true
		for _, index := range order {
			if index < 0 || int(index) >= len(catalog) {
				valid = false
				break
			}
		}
		if valid {
			return order
		}
	}
	order = make([]int32, len(catalog))
	for index := range catalog {
		order[index] = int32(index)
	}
	sort.Slice(order, func(i, j int) bool { return catalog[order[i]].ID < catalog[order[j]].ID })
	return order
}

func catalogStateCountsForOrder(store *Store, catalog []Account, order []int32) map[string]int {
	counts := make(map[string]int, 8)
	if len(order) != len(catalog) {
		order = make([]int32, len(catalog))
		for index := range catalog {
			order[index] = int32(index)
		}
	}
	chunkIDs := make([]string, accountListChunk)
	chunkStates := make([]string, accountListChunk)
	for base := 0; base < len(order); base += accountListChunk {
		end := min(base+accountListChunk, len(order))
		count := end - base
		ids := chunkIDs[:count]
		states := chunkStates[:count]
		for offset, catalogIndex := range order[base:end] {
			ids[offset] = catalog[int(catalogIndex)].ID
		}
		store.FillRoutingStates(ids, states)
		for offset, catalogIndex := range order[base:end] {
			counts[catalogAccountState(catalog[int(catalogIndex)], states[offset])]++
		}
	}
	return counts
}

func catalogStateCounts(store *Store, catalog []Account) map[string]int {
	order := make([]int32, len(catalog))
	for index := range catalog {
		order[index] = int32(index)
	}
	return catalogStateCountsForOrder(store, catalog, order)
}

func catalogAccountIDs(accounts []Account) []string {
	ids := make([]string, len(accounts))
	for index, account := range accounts {
		ids[index] = account.ID
	}
	return ids
}

func catalogAccountState(account Account, state string) string {
	if state != "" {
		return state
	}
	if account.Disabled {
		return StateExternallyDisabled
	}
	return StateUnknown
}

func catalogAccountMatches(account Account, query string) bool {
	if query == "" {
		return true
	}
	// Keep the historical search domain exactly: the four fields were joined
	// with spaces before matching, so a query may cross a field boundary.
	parts := [4]string{
		firstNonEmpty(account.Email, account.Label, account.ID),
		account.ID,
		account.Label,
		account.Email,
	}
	for _, part := range parts {
		for index := 0; index < len(part); index++ {
			if part[index] >= 0x80 {
				return strings.Contains(strings.ToLower(strings.Join(parts[:], " ")), query)
			}
		}
	}
	for index := 0; index < len(query); index++ {
		if query[index] >= 0x80 {
			return strings.Contains(strings.ToLower(strings.Join(parts[:], " ")), query)
		}
	}
	return containsFoldPartsASCII(parts[:], query)
}

// containsFoldPartsASCII matches a case-insensitive ASCII needle against the
// virtual string parts[0] + " " + parts[1] + ... without allocating a joined
// lowercase copy for every account.
func containsFoldPartsASCII(parts []string, needle string) bool {
	if needle == "" {
		return true
	}
	total := len(parts) - 1
	for _, part := range parts {
		total += len(part)
	}
	if len(needle) > total {
		return false
	}
	byteAt := func(index int) byte {
		for _, part := range parts {
			if index < len(part) {
				return part[index]
			}
			index -= len(part)
			if index == 0 {
				return ' '
			}
			index--
		}
		return 0
	}
	for start := 0; start+len(needle) <= total; start++ {
		matched := true
		for offset := 0; offset < len(needle); offset++ {
			haystack := byteAt(start + offset)
			needleByte := needle[offset]
			if haystack >= 'A' && haystack <= 'Z' {
				haystack += 'a' - 'A'
			}
			if needleByte >= 'A' && needleByte <= 'Z' {
				needleByte += 'a' - 'A'
			}
			if haystack != needleByte {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func (s *Service) selectedAccountCandidates(ctx context.Context, authIDs []string) ([]accountCandidate, error) {
	if errRefresh := s.RefreshCatalog(ctx); errRefresh != nil {
		return nil, newAPIActionError("upstream_error", "api.error.upstream_error", http.StatusBadGateway, nil)
	}
	if s.stateStore() == nil {
		return nil, newAPIActionError("state_store_unavailable", "api.error.state_store_unavailable", http.StatusInternalServerError, nil)
	}
	candidates := make([]accountCandidate, 0, len(authIDs))
	for _, authID := range authIDs {
		account, okAccount := s.findAccount(authID)
		if !okAccount {
			return nil, newAPIActionError("auth_not_found", "api.error.auth_not_found", http.StatusNotFound, nil)
		}
		candidates = append(candidates, accountCandidate{
			authID:  authID,
			account: account,
		})
	}
	return candidates, nil
}

func (s *Service) accountCandidateState(candidate accountCandidate) string {
	if stateAccount, okState := s.stateAccount(candidate.authID); okState && stateAccount.State != "" {
		return stateAccount.State
	}
	if candidate.account.Disabled {
		return StateExternallyDisabled
	}
	return StateUnknown
}

func (s *Service) actionAccountGet(ctx context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(asString(params["auth_id"]))
	if authID == "" {
		return nil, http.StatusBadRequest, newAPIActionError("auth_id_required", "api.error.auth_id_required", http.StatusBadRequest, nil)
	}
	if errCatalog := s.ensureCatalog(ctx); errCatalog != nil {
		return nil, http.StatusBadGateway, errCatalog
	}
	account, okAccount := s.findAccount(authID)
	if !okAccount {
		return nil, http.StatusNotFound, newAPIActionError("auth_not_found", "api.error.auth_not_found", http.StatusNotFound, nil)
	}
	view, errView := s.accountViewForAccount(ctx, account)
	if errView != nil {
		return nil, http.StatusBadGateway, errView
	}
	return view, http.StatusOK, nil
}

// actionAccountProxyMetadata refreshes only the visible rows' auth-file proxy
// metadata and returns the shared binding projection. It replaces the old
// whole-table polling loop with one bounded, page-sized request.
func (s *Service) actionAccountProxyMetadata(ctx context.Context, params map[string]any) (any, int, error) {
	authIDs := action.AuthIDs(params)
	if len(authIDs) == 0 {
		return nil, http.StatusBadRequest, newAPIActionError("auth_ids_required", "api.error.auth_ids_required", http.StatusBadRequest, nil)
	}
	if len(authIDs) > 200 {
		return nil, http.StatusBadRequest, newAPIActionError("auth_ids_limit", "api.error.auth_ids_limit", http.StatusBadRequest, nil)
	}
	if errCatalog := s.ensureCatalog(ctx); errCatalog != nil {
		return nil, http.StatusBadGateway, errCatalog
	}
	uniqueIDs := make([]string, 0, len(authIDs))
	seen := make(map[string]struct{}, len(authIDs))
	accounts := make([]Account, 0, len(authIDs))
	for _, authID := range authIDs {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		if _, exists := seen[authID]; exists {
			continue
		}
		account, ok := s.findAccount(authID)
		if !ok {
			continue
		}
		seen[authID] = struct{}{}
		uniqueIDs = append(uniqueIDs, authID)
		accounts = append(accounts, account)
	}
	if len(accounts) == 0 {
		return map[string]any{"items": []any{}, "binding_version": s.env.Proxy().BindingVersion()}, http.StatusOK, nil
	}
	if inspector, ok := s.env.(interface {
		EnsureAuthMetaForIDs(context.Context, []string, time.Duration) error
	}); ok {
		_ = inspector.EnsureAuthMetaForIDs(ctx, uniqueIDs, 450*time.Millisecond)
		accounts = accounts[:0]
		for _, authID := range uniqueIDs {
			if account, found := s.findAccount(authID); found {
				accounts = append(accounts, account)
			}
		}
	}
	bindings := s.env.Proxy().BindingStatesForAccounts(accounts)
	bindingVersion := s.env.Proxy().BindingVersion()
	if len(bindings) > 0 {
		bindingVersion = bindings[0].Version
	}
	items := make([]map[string]any, len(bindings))
	for index, binding := range bindings {
		items[index] = map[string]any{
			"auth_id":               binding.AuthID,
			"proxy_status":          binding.Status,
			"proxy":                 binding.Proxy,
			"proxy_source_id":       binding.SourceID,
			"proxy_source_kind":     binding.SourceKind,
			"proxy_managed":         binding.SourceID != "",
			"proxy_source_revision": binding.SourceRevision,
			"proxy_generation":      binding.Generation,
			"proxy_lease_account":   binding.LeaseAccount,
			"proxy_pending":         binding.Pending,
			"proxy_inspected":       binding.Inspected,
			"binding_version":       binding.Version,
		}
	}
	return map[string]any{"items": items, "binding_version": bindingVersion}, http.StatusOK, nil
}
