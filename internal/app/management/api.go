package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"grok-quality-guard/internal/app/action"
	"grok-quality-guard/internal/app/dashboard"
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

type restoreBatchItem struct {
	AuthID      string `json:"auth_id"`
	StateBefore string `json:"state_before"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
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
	if len(request) > 0 {
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return okEnvelope(managementResponse{StatusCode: http.StatusBadRequest, Headers: jsonHeaders(), Body: jsonError("invalid_request", "decode management request")})
		}
	}
	if strings.EqualFold(req.Method, http.MethodGet) && strings.HasSuffix(strings.TrimRight(req.Path, "/"), "/dashboard") {
		return okEnvelope(managementResponse{StatusCode: http.StatusOK, Headers: htmlHeaders(), Body: []byte(dashboard.HTML())})
	}
	if !strings.EqualFold(req.Method, http.MethodPost) {
		return okEnvelope(managementResponse{StatusCode: http.StatusMethodNotAllowed, Headers: jsonHeaders(), Body: jsonError("method_not_allowed", "only POST is accepted")})
	}
	var payload action.Request
	if len(req.Body) > 0 {
		if errUnmarshal := json.Unmarshal(req.Body, &payload); errUnmarshal != nil {
			return okEnvelope(managementResponse{StatusCode: http.StatusBadRequest, Headers: jsonHeaders(), Body: jsonError("invalid_json", "action body must be valid JSON")})
		}
	}

	if payload.Params == nil {
		payload.Params = map[string]any{}
	}
	ctx := context.Background()
	data, status, errAction := s.handleAction(ctx, payload)
	if errAction != nil {
		if status == 0 {
			status = http.StatusBadRequest
		}
		code := actionErrorCode(status)
		detail := map[string]any{"message": sanitizeError(errAction)}
		var proxyErr *proxyManagementError
		if errors.As(errAction, &proxyErr) {
			code = proxyErr.Code
		}
		var invalid *proxy.ValidationError
		if errors.As(errAction, &invalid) {
			code = invalid.Code
			detail["path"] = invalid.Path
		}
		detail["code"] = code
		body, _ := json.Marshal(map[string]any{"ok": false, "error": detail})
		return okEnvelope(managementResponse{StatusCode: status, Headers: jsonHeaders(), Body: body})
	}
	if status == 0 {
		status = http.StatusOK
	}
	return okEnvelope(managementResponse{StatusCode: status, Headers: jsonHeaders(), Body: jsonOK(data)})
}

func (s *Service) handleAction(ctx context.Context, request action.Request) (any, int, error) {
	switch strings.ToLower(strings.TrimSpace(request.Action)) {
	case "overview":
		return s.actionOverview(ctx)
	case "accounts.list":
		return s.actionAccountsList(ctx, request.Params)
	case "accounts.get":
		return s.actionAccountGet(ctx, request.Params)
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
		return s.actionProxySourcesList(request.Params)
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
		return s.actionEventsList(request.Params)
	case "tasks.get":
		return s.actionTaskGet(request.Params)
	case "tasks.cancel":
		return s.actionTaskCancel(request.Params)
	case "tasks.list":
		return s.actionTaskList(request.Params)
	case "tasks.center.list":
		return s.actionTaskCenterList(request.Params)
	case "tasks.center.queue":
		return s.actionTaskCenterQueue(request.Params)
	case "tasks.center.get", "tasks.center.items":
		return s.actionTaskCenterDetail(request.Params)
	case "tasks.center.cancel":
		return s.actionTaskCancel(request.Params)
	case "tasks.center.delete":
		return s.actionTaskCenterDelete(request.Params)
	default:
		return nil, http.StatusBadRequest, fmt.Errorf("unknown action")
	}
}

func (s *Service) actionOverview(ctx context.Context) (any, int, error) {
	_ = s.ensureCatalog(ctx)
	catalog := s.catalogSnapshot()
	store := s.stateStore()
	counts := catalogStateCounts(store, catalog)
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

func (s *Service) actionAccountsList(ctx context.Context, params map[string]any) (any, int, error) {
	_ = s.ensureCatalog(ctx)
	store := s.stateStore()
	catalog := s.catalogSnapshot()
	filterState := strings.ToLower(strings.TrimSpace(asString(params["state"])))
	query := strings.ToLower(strings.TrimSpace(asString(params["query"])))
	var stateValues []string
	if filterState != "" {
		stateValues = store.AccountStateValuesForIDs(catalogAccountIDs(catalog))
	}
	page := action.Int(params, "page", 1)
	pageSize := action.Int(params, "page_size", 50)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 10000 {
		pageSize = 50
	}
	filtered := make([]Account, 0, len(catalog))
	for index, account := range catalog {
		if filterState != "" && catalogAccountState(account, stateValues[index]) != filterState {
			continue
		}
		if query != "" && !catalogAccountMatches(account, query) {
			continue
		}
		filtered = append(filtered, account)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	allowProxyFallback := len(catalog) <= catalogProxyFallbackLimit
	pageAccounts := filtered[start:end]
	pageIDs := catalogAccountIDs(pageAccounts)
	var views any
	if action.Bool(params, "summary") {
		pageSummaries := store.AccountStateSummariesForIDs(pageIDs)
		items := make([]accountSummaryView, 0, len(pageAccounts))
		for index, account := range pageAccounts {
			items = append(items, s.accountSummary(account, accountStateFromSummary(pageSummaries[index]), s.accountProxyURL(ctx, account, allowProxyFallback)))
		}
		views = items
	} else {
		pageStates := store.AccountStatesForIDs(pageIDs)
		items := make([]accountView, 0, len(pageAccounts))
		for index, account := range pageAccounts {
			items = append(items, s.accountView(account, pageStates[index], s.accountProxyURL(ctx, account, allowProxyFallback)))
		}
		views = items
	}
	stateCounts := map[string]int(nil)
	stateCounts = catalogStateCounts(store, catalog)
	return map[string]any{
		"items":        views,
		"page":         page,
		"page_size":    pageSize,
		"total":        len(filtered),
		"state_counts": stateCounts,
	}, http.StatusOK, nil
}

func catalogStateCounts(store *Store, catalog []Account) map[string]int {
	counts := make(map[string]int, 8)
	values := []string(nil)
	if store != nil {
		values = store.AccountStateValuesForIDs(catalogAccountIDs(catalog))
	}
	for index, account := range catalog {
		state := ""
		if index < len(values) {
			state = values[index]
		}
		if state == "" {
			state = catalogAccountState(account, "")
		}
		counts[state]++
	}
	return counts
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
	text := strings.ToLower(strings.Join([]string{
		firstNonEmpty(account.Email, account.Label, account.ID),
		account.ID,
		account.Label,
		account.Email,
	}, " "))
	return strings.Contains(text, query)
}

func (s *Service) selectedAccountCandidates(ctx context.Context, authIDs []string) ([]accountCandidate, error) {
	if errRefresh := s.RefreshCatalog(ctx); errRefresh != nil {
		return nil, fmt.Errorf("refresh auth catalog: %w", errRefresh)
	}
	if s.stateStore() == nil {
		return nil, fmt.Errorf("state store is not initialized")
	}
	candidates := make([]accountCandidate, 0, len(authIDs))
	for _, authID := range authIDs {
		account, okAccount := s.findAccount(authID)
		if !okAccount {
			return nil, fmt.Errorf("auth not found: %s", authID)
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
		return nil, http.StatusBadRequest, fmt.Errorf("auth_id is required")
	}
	account, okAccount := s.findAccount(authID)
	if !okAccount {
		return nil, http.StatusNotFound, fmt.Errorf("auth not found")
	}
	_, _, raw, errResolve := s.resolveAuth(ctx, account.ID)
	if errResolve != nil {
		return nil, http.StatusBadGateway, errResolve
	}
	stateAccount, _ := s.stateAccount(account.ID)
	rawProxyURL := strings.TrimSpace(authRawString(raw, "proxy_url"))
	return s.accountView(account, stateAccount, rawProxyURL), http.StatusOK, nil
}
