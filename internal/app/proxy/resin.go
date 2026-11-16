// Resin configuration, binding lifecycle and durable lease cleanup.
package proxy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	actionpkg "grok-quality-guard/internal/app/action"
	"grok-quality-guard/internal/app/auth"
	resinpkg "grok-quality-guard/internal/app/proxy/resin"
	"grok-quality-guard/internal/app/redact"
	proxy "grok-quality-guard/internal/proxy"
)

var resinPlatformIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type resinConfigView struct {
	ConfigVersion                 uint64    `json:"config_version"`
	Configured                    bool      `json:"configured"`
	Name                          string    `json:"name"`
	Enabled                       bool      `json:"enabled"`
	LocalOnly                     bool      `json:"local_only"`
	Platform                      string    `json:"platform"`
	PlatformID                    string    `json:"platform_id"`
	AdminURL                      string    `json:"admin_url"`
	AccountFormat                 string    `json:"account_format"`
	ProxyTemplate                 string    `json:"proxy_template"`
	AutoRotateAfterQualityFailure bool      `json:"auto_rotate_after_quality_failure"`
	AdminTokenSet                 bool      `json:"admin_token_set"`
	ProxyTokenSet                 bool      `json:"proxy_token_set"`
	ValidatedAt                   time.Time `json:"validated_at,omitempty"`
}

type resinValidationResult struct {
	LocalOnly         bool      `json:"local_only"`
	AdminAPIReachable bool      `json:"admin_api_reachable"`
	AdminTokenValid   bool      `json:"admin_token_valid"`
	PlatformValid     bool      `json:"platform_valid"`
	Platform          string    `json:"platform"`
	PlatformID        string    `json:"platform_id,omitempty"`
	ProxyTokenValid   bool      `json:"proxy_token_valid"`
	ValidatedAt       time.Time `json:"validated_at"`
}

type resinConfigError struct {
	Status  int
	Message string
}

func (e *resinConfigError) HTTPStatus() int {
	if e == nil {
		return 0
	}
	return e.Status
}

func (e *resinConfigError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func resinConfigErrorf(status int, format string, args ...any) error {
	return &resinConfigError{Status: status, Message: fmt.Sprintf(format, args...)}
}

func (s *Service) ResinConfig() ResinSettings {
	_, resinCfg, rotate := s.env.ProxyConfigSnapshot()

	settings := resinSettingsFromConfig(resinCfg)
	store := s.env.ProxyStore()
	if store != nil {
		if stored, configured := store.ResinSettings(); configured {
			settings = stored
			settings.Configured = true
			if !settings.LocalOnly {
				if strings.TrimSpace(settings.AdminToken) == "" {
					settings.AdminToken = os.Getenv(resinCfg.AdminTokenEnv)
				}
				if strings.TrimSpace(settings.ProxyToken) == "" {
					settings.ProxyToken = os.Getenv(resinCfg.ProxyTokenEnv)
				}
			}

		}
	}
	settings.AutoRotateAfterQualityFailure = rotate
	return normalizeResinSettingsBestEffort(settings)
}

func resinSettingsFromConfig(cfg ResinConfig) ResinSettings {
	return normalizeResinSettingsBestEffort(ResinSettings{
		Enabled:                       cfg.Enabled,
		LocalOnly:                     cfg.LocalOnly,
		Platform:                      cfg.Platform,
		PlatformID:                    cfg.PlatformID,
		AdminURL:                      cfg.AdminURL,
		ProxyTemplate:                 cfg.ProxyTemplate,
		AutoRotateAfterQualityFailure: cfg.AutoRotateAfterQualityFailure,
		AdminToken:                    os.Getenv(cfg.AdminTokenEnv),
		ProxyToken:                    os.Getenv(cfg.ProxyTokenEnv),
	})
}

func normalizeResinSettingsBestEffort(settings ResinSettings) ResinSettings {
	settings.Name = strings.TrimSpace(settings.Name)
	if settings.Name == "" {
		settings.Name = "Resin"
	}
	settings.Platform = strings.TrimSpace(settings.Platform)
	if settings.Platform == "" {
		settings.Platform = "Default"
	}
	settings.PlatformID = strings.TrimSpace(settings.PlatformID)
	settings.AdminURL = strings.TrimRight(strings.TrimSpace(settings.AdminURL), "/")
	settings.ProxyTemplate = strings.TrimSpace(settings.ProxyTemplate)
	if settings.ProxyTemplate == "" {
		settings.ProxyTemplate = "socks5h://{platform}.{account}:{proxy_token}@resin:2260"
	}
	settings.AdminToken = strings.TrimSpace(settings.AdminToken)
	settings.ProxyToken = strings.TrimSpace(settings.ProxyToken)
	if settings.LocalOnly {
		settings.PlatformID = ""
		settings.AdminURL = ""
		settings.AdminToken = ""
		settings.ProxyToken = ""
	}

	return settings
}

func (s *Service) resinConfigView(settings ResinSettings) resinConfigView {
	version := uint64(1)
	if store := s.env.ProxyStore(); store != nil {
		version = max(1, store.ProxyResinSource().ConfigVersion)
	}
	displayTemplate := settings.ProxyTemplate
	if template, err := resinpkg.MigrateTemplate(settings.ProxyTemplate, settings.Platform, settings.ProxyToken); err == nil {
		template.HasPassword = false
		template.Password = ""
		displayTemplate = template.URLForDisplay()
	}
	if scheme, rest, ok := strings.Cut(displayTemplate, "://"); ok {
		if at := strings.LastIndexByte(rest, '@'); at >= 0 {
			user, _, _ := strings.Cut(rest[:at], ":")
			displayTemplate = scheme + "://" + user + "@" + rest[at+1:]
		}
	}
	accountFormat, _ := resinpkg.AccountFormat(settings.ProxyTemplate)
	accountFormat = resinAccountSuffix(accountFormat, settings.Platform)
	return resinConfigView{
		ConfigVersion:                 version,
		Configured:                    settings.Configured,
		Name:                          settings.Name,
		Enabled:                       settings.Enabled,
		LocalOnly:                     settings.LocalOnly,
		Platform:                      settings.Platform,
		PlatformID:                    settings.PlatformID,
		AdminURL:                      settings.AdminURL,
		AccountFormat:                 accountFormat,
		ProxyTemplate:                 displayTemplate,
		AutoRotateAfterQualityFailure: settings.AutoRotateAfterQualityFailure,
		AdminTokenSet:                 strings.TrimSpace(settings.AdminToken) != "",
		ProxyTokenSet:                 strings.TrimSpace(settings.ProxyToken) != "",
		ValidatedAt:                   settings.ValidatedAt,
	}
}

func (s *Service) actionResinConfigGet() (any, int, error) {
	return s.resinConfigView(s.ResinConfig()), http.StatusOK, nil
}

func (s *Service) actionResinConfigValidate(_ context.Context, params map[string]any) (any, int, error) {
	candidate, errCandidate := resinCandidateFromParams(params, s.ResinConfig())
	if errCandidate != nil {
		return nil, http.StatusBadRequest, errCandidate
	}
	normalized, errNormalize := normalizeResinSettings(candidate)
	if errNormalize != nil {
		return nil, http.StatusBadRequest, errNormalize
	}
	task := s.env.ProxyStartTask("resin.config.validate", nil, "正在验证 Resin 配置", func(taskCtx context.Context) (any, error) {
		return validateResinSettings(taskCtx, normalized)
	})
	return task, http.StatusAccepted, nil
}

func (s *Service) actionResinConfigSave(_ context.Context, params map[string]any) (any, int, error) {
	if s.env.ProxyStore() == nil {
		return nil, http.StatusInternalServerError, errors.New("state store is not initialized")
	}
	version := s.env.ProxyStore().ProxyResinSource().ConfigVersion
	if expected := actionpkg.Int(params, "expected_version", 0); expected > 0 && uint64(expected) != version {
		return nil, 409, proxyError("proxy_source_version_conflict", 409, "Resin configuration changed; reload before saving")
	}
	candidate, errCandidate := resinCandidateFromParams(params, s.ResinConfig())
	if errCandidate != nil {
		return nil, http.StatusBadRequest, errCandidate
	}
	normalized, errNormalize := normalizeResinSettings(candidate)
	if errNormalize != nil {
		return nil, http.StatusBadRequest, errNormalize
	}
	task := s.env.ProxyStartTask("resin.config.save", nil, "正在验证并保存 Resin 配置", func(taskCtx context.Context) (any, error) {
		return s.saveResinConfigVersioned(taskCtx, normalized, version)
	})
	return task, http.StatusAccepted, nil
}

func (s *Service) saveResinConfigVersioned(ctx context.Context, normalized ResinSettings, expected uint64) (any, error) {
	store := s.env.ProxyStore()
	if store == nil {
		return nil, errors.New("state store is not initialized")
	}
	previous := s.ResinConfig()
	version := store.ProxyResinSource().ConfigVersion
	if expected > 0 && version != expected {
		return nil, proxyError("proxy_source_version_conflict", 409, "Resin configuration changed before validation")
	}
	var result resinValidationResult
	var errValidate error
	if previous.Configured && !previous.ValidatedAt.IsZero() && resinGeneratedConfigEqual(previous, normalized) {
		result = resinValidationResult{Platform: previous.Platform, PlatformID: previous.PlatformID, LocalOnly: previous.LocalOnly, ValidatedAt: previous.ValidatedAt}
	} else {
		result, errValidate = validateResinSettings(ctx, normalized)
	}
	if errValidate != nil {
		return nil, errValidate
	}
	if result.Platform != "" {
		normalized.Platform = result.Platform
	}
	if result.PlatformID != "" {
		normalized.PlatformID = result.PlatformID
	}
	normalized.LocalOnly = result.LocalOnly
	normalized.Configured = true
	normalized.ValidatedAt = result.ValidatedAt
	if !normalized.LocalOnly {
		template, err := resinpkg.MigrateTemplate(normalized.ProxyTemplate, normalized.Platform, normalized.ProxyToken)
		if err != nil {
			return nil, err
		}
		template.HasPassword = false
		template.Password = ""
		normalized.ProxyTemplate = template.URLForDisplay()
	}
	eventResult := "validated"
	eventDetail := "platform_id=" + normalized.PlatformID
	if normalized.LocalOnly {
		eventResult = "local_mode"
		eventDetail = "local_only"
	}
	if errStore := store.Update(func(state *State) error {
		if state.Proxy.ResinSource.ConfigVersion != version {
			return proxyError("proxy_source_version_conflict", 409, "Resin configuration changed during validation")
		}
		if hasPendingSourceOperation(*state, resinSourceID) {
			return proxyError("proxy_source_busy", 409, "complete pending Resin operations before changing configuration")
		}
		if state.Proxy.SourceRuntime[resinSourceID].ChangeID != "" {
			return proxyError("proxy_source_busy", 409, "Resin source change is unfinished")
		}
		if state.ResinSnapshots == nil {
			state.ResinSnapshots = make(map[string]ResinSettings)
		}
		if resinCleanupConfigured(previous) {
			id := resinSnapshotID(previous)
			state.ResinSnapshots[id] = previous
			for authID, account := range state.Accounts {
				binding, managed := bindingForAccount(*account)
				if managed && binding.SourceKind == proxy.SourceResin && binding.Lease != nil && binding.Lease.SnapshotID == "" {
					lease := *binding.Lease
					lease.SnapshotID = id
					lease.PlatformID = previous.PlatformID
					binding.Lease = &lease
					binding.AuthID = authID
					account.Proxy = binding
				}
			}
		}
		state.Resin = normalized
		if resinCleanupConfigured(normalized) {
			state.ResinSnapshots[resinSnapshotID(normalized)] = normalized
		}
		state.Proxy.ResinSource.ID = resinSourceID
		state.Proxy.ResinSource.Name = normalized.Name
		state.Proxy.ResinSource.Enabled = normalized.Enabled
		state.Proxy.ResinSource.Deleted = false
		if !resinGeneratedConfigEqual(previous, normalized) {
			state.Proxy.ResinSource.Revision++
		}
		if state.Proxy.ResinSource.Revision == 0 {
			state.Proxy.ResinSource.Revision = 1
		}
		state.Proxy.ResinSource.ConfigVersion++
		if state.Proxy.ResinSource.ConfigVersion == 0 {
			state.Proxy.ResinSource.ConfigVersion = 1
		}
		state.Policy.ResinAutoRotateAfterQualityFail = normalized.AutoRotateAfterQualityFailure
		state.Policy.ProxyAutoRotateAfterQualityFail = normalized.AutoRotateAfterQualityFailure
		s.env.ProxyRecordEvent(state, "", "", "resin_config_save", eventResult, eventDetail)
		return nil
	}); errStore != nil {
		return nil, errStore
	}
	proxyCfg, resinCfg, _ := s.env.ProxyConfigSnapshot()
	resinCfg.Enabled = normalized.Enabled
	resinCfg.LocalOnly = normalized.LocalOnly
	resinCfg.Platform = normalized.Platform
	resinCfg.PlatformID = normalized.PlatformID
	resinCfg.AdminURL = normalized.AdminURL
	resinCfg.ProxyTemplate = normalized.ProxyTemplate
	resinCfg.AutoRotateAfterQualityFailure = normalized.AutoRotateAfterQualityFailure
	proxyCfg.AutoRotateAfterQualityFailure = normalized.AutoRotateAfterQualityFailure
	s.env.ProxyApplyConfig(proxyCfg, resinCfg, normalized.AutoRotateAfterQualityFailure)
	s.wakeProxyAutoBind()
	return s.resinConfigView(normalized), nil
}

func resinGeneratedConfigEqual(left, right ResinSettings) bool {
	return left.LocalOnly == right.LocalOnly && left.Platform == right.Platform && left.PlatformID == right.PlatformID && left.AdminURL == right.AdminURL && left.AdminToken == right.AdminToken && left.ProxyTemplate == right.ProxyTemplate && left.ProxyToken == right.ProxyToken
}
func resinSettingsFromParams(params map[string]any, current ResinSettings) ResinSettings {
	settings := current
	settings.Name = optionalStringParam(params, "name", settings.Name)
	settings.Enabled = optionalBoolParam(params, "enabled", settings.Enabled)
	settings.LocalOnly = optionalBoolParam(params, "local_only", settings.LocalOnly)
	settings.Platform = optionalStringParam(params, "platform", settings.Platform)
	settings.PlatformID = optionalStringParam(params, "platform_id", settings.PlatformID)
	settings.AdminURL = optionalStringParam(params, "admin_url", settings.AdminURL)
	settings.ProxyTemplate = optionalStringParam(params, "proxy_template", settings.ProxyTemplate)
	settings.AutoRotateAfterQualityFailure = optionalBoolParam(params, "auto_rotate_after_quality_failure", settings.AutoRotateAfterQualityFailure)
	if value, ok := params["admin_token"]; ok {
		if token := strings.TrimSpace(actionpkg.String(value)); token != "" {
			settings.AdminToken = token
		}
	}
	if value, ok := params["proxy_token"]; ok {
		if token := strings.TrimSpace(actionpkg.String(value)); token != "" {
			settings.ProxyToken = token
		}
	}
	if settings.LocalOnly {
		settings.PlatformID = ""
		settings.AdminURL = ""
		settings.AdminToken = ""
		settings.ProxyToken = ""
	}
	return settings
}

func resinCandidateFromParams(params map[string]any, current ResinSettings) (ResinSettings, error) {
	settings := resinSettingsFromParams(params, current)
	rawFormat, supplied := params["account_format"]
	if !supplied || settings.LocalOnly {
		return settings, nil
	}
	template, err := resinProxyTemplateFromAccountFormat(actionpkg.String(rawFormat), settings.AdminURL)
	if err != nil {
		return ResinSettings{}, err
	}
	settings.ProxyTemplate = template
	return settings, nil
}

func resinProxyTemplateFromAccountFormat(accountFormat, adminURL string) (string, error) {
	accountFormat = strings.TrimSpace(accountFormat)
	if accountFormat == "" {
		return "", resinConfigErrorf(http.StatusBadRequest, "resin account_format is required")
	}
	if strings.Contains(accountFormat, "{platform}") {
		return "", resinConfigErrorf(http.StatusBadRequest, "resin account_format must not contain {platform}")
	}
	if strings.ContainsAny(accountFormat, "/?#@:") {
		return "", resinConfigErrorf(http.StatusBadRequest, "resin account_format must contain only the username pattern")
	}
	parsedAdmin, errAdmin := url.Parse(strings.TrimSpace(adminURL))
	if errAdmin != nil || parsedAdmin.Scheme == "" || parsedAdmin.Hostname() == "" {
		return "", resinConfigErrorf(http.StatusBadRequest, "resin admin_url is invalid")
	}
	if parsedAdmin.Scheme != "http" && parsedAdmin.Scheme != "https" {
		return "", resinConfigErrorf(http.StatusBadRequest, "resin admin_url must use http or https")
	}
	host := parsedAdmin.Hostname()
	port := parsedAdmin.Port()
	if port == "" {
		if parsedAdmin.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return "socks5h://{platform}." + accountFormat + ":{proxy_token}@" + net.JoinHostPort(host, port), nil
}

func resinAccountSuffix(accountFormat, platform string) string {
	accountFormat = strings.TrimSpace(accountFormat)
	if strings.HasPrefix(accountFormat, "{platform}.") {
		return strings.TrimPrefix(accountFormat, "{platform}.")
	}
	platform = strings.TrimSpace(platform)
	if platform != "" && strings.HasPrefix(accountFormat, platform+".") {
		return strings.TrimPrefix(accountFormat, platform+".")
	}
	return accountFormat
}

func optionalStringParam(params map[string]any, key, fallback string) string {
	value, ok := params[key]
	if !ok || value == nil {
		return fallback
	}
	return actionpkg.String(value)
}

func optionalBoolParam(params map[string]any, key string, fallback bool) bool {
	value, ok := params[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}

func normalizeResinAdminSettings(settings ResinSettings) (ResinSettings, error) {
	settings = normalizeResinSettingsBestEffort(settings)
	if settings.Platform == "" {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin platform is required")
	}
	if settings.AdminURL == "" {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url is required")
	}
	parsedAdmin, errAdmin := url.Parse(settings.AdminURL)
	if errAdmin != nil || parsedAdmin.Scheme == "" || parsedAdmin.Host == "" {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url is invalid")
	}
	if parsedAdmin.Scheme != "http" && parsedAdmin.Scheme != "https" {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url must use http or https")
	}
	if parsedAdmin.User != nil || parsedAdmin.RawQuery != "" || parsedAdmin.Fragment != "" {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url must not contain userinfo, query, or fragment")
	}
	settings.AdminURL = strings.TrimRight(parsedAdmin.String(), "/")
	if settings.AdminToken == "" {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_token is required")
	}
	if len(settings.AdminToken) > 4096 {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_token is too long")
	}
	return settings, nil
}

func normalizeResinSettings(settings ResinSettings) (ResinSettings, error) {
	settings = normalizeResinSettingsBestEffort(settings)
	if len(settings.Name) > 256 {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin name must not exceed 256 bytes")
	}
	if settings.LocalOnly {
		return settings, nil
	}
	normalized, errAdmin := normalizeResinAdminSettings(settings)
	if errAdmin != nil {
		return ResinSettings{}, errAdmin
	}
	settings = normalized
	if settings.ProxyToken == "" {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin proxy_token is required")
	}
	if len(settings.ProxyToken) > 255 {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin proxy_token must not exceed 255 bytes")
	}
	if _, err := resinpkg.NewProvider(resinpkg.ProviderSettings{ProxyTemplate: settings.ProxyTemplate, Platform: settings.Platform, PlatformID: settings.PlatformID, ProxyToken: settings.ProxyToken, SnapshotID: "validation"}, nil); err != nil {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin proxy template is invalid: %s", err.Error())
	}
	return settings, nil
}

func validateResinSettings(ctx context.Context, settings ResinSettings) (resinValidationResult, error) {
	result := resinValidationResult{}
	if settings.LocalOnly {
		result.LocalOnly = true
		result.ValidatedAt = time.Now().UTC()
		return result, nil
	}
	if errAdmin := validateResinAdminAPI(ctx, settings); errAdmin != nil {
		return result, errAdmin
	}
	result.AdminAPIReachable = true
	result.AdminTokenValid = true
	resolved, errPlatform := resolveResinPlatformByName(ctx, settings)
	if errPlatform != nil {
		return result, errPlatform
	}
	settings = resolved
	result.Platform = settings.Platform
	result.PlatformID = settings.PlatformID
	result.PlatformValid = true
	if errProxy := validateResinProxyToken(ctx, settings); errProxy != nil {
		return result, errProxy
	}
	result.ProxyTokenValid = true
	result.ValidatedAt = time.Now().UTC()
	return result, nil
}

func validateResinAdminAPI(ctx context.Context, settings ResinSettings) error {
	status, _, errRequest := resinAdminRequest(ctx, settings, http.MethodGet, "/api/v1/system/info")
	if errRequest != nil {
		return resinConfigErrorf(http.StatusBadGateway, "resin admin API is unreachable: %s", redact.SanitizeError(errRequest))
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return resinConfigErrorf(http.StatusBadRequest, "resin admin token was rejected with status %d", status)
	default:
		if status >= 500 {
			return resinConfigErrorf(http.StatusBadGateway, "resin admin API returned status %d", status)
		}
		return resinConfigErrorf(http.StatusBadRequest, "resin admin API returned status %d", status)
	}
}

func validateResinPlatform(ctx context.Context, settings ResinSettings) (string, error) {
	path := "/api/v1/platforms/" + url.PathEscape(settings.PlatformID)
	status, body, errRequest := resinAdminRequest(ctx, settings, http.MethodGet, path)
	if errRequest != nil {
		return "", resinConfigErrorf(http.StatusBadGateway, "resin platform validation failed: %s", redact.SanitizeError(errRequest))
	}
	switch status {
	case http.StatusOK:
		var platform struct {
			Name string `json:"name"`
		}
		if errUnmarshal := json.Unmarshal(body, &platform); errUnmarshal != nil || strings.TrimSpace(platform.Name) == "" {
			return "", resinConfigErrorf(http.StatusBadGateway, "resin platform API returned an invalid response")
		}
		return platform.Name, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", resinConfigErrorf(http.StatusBadRequest, "resin admin token was rejected with status %d", status)
	case http.StatusNotFound:
		return "", resinConfigErrorf(http.StatusBadRequest, "resin platform_id was not found")
	default:
		if status >= 500 {
			return "", resinConfigErrorf(http.StatusBadGateway, "resin platform API returned status %d", status)
		}
		return "", resinConfigErrorf(http.StatusBadRequest, "resin platform API returned status %d", status)
	}
}

type resinPlatformSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func resolveResinPlatformByName(ctx context.Context, settings ResinSettings) (ResinSettings, error) {
	requested := strings.TrimSpace(settings.Platform)
	if requested == "" {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin platform is required")
	}
	query := url.Values{}
	query.Set("keyword", requested)
	query.Set("sort_by", "name")
	query.Set("sort_order", "asc")
	query.Set("limit", "1000")
	query.Set("offset", "0")
	status, body, errRequest := resinAdminRequest(ctx, settings, http.MethodGet, "/api/v1/platforms?"+query.Encode())
	if errRequest != nil {
		return ResinSettings{}, resinConfigErrorf(http.StatusBadGateway, "resin platform validation failed: %s", redact.SanitizeError(errRequest))
	}
	switch status {
	case http.StatusOK:
		var page struct {
			Items []resinPlatformSummary `json:"items"`
		}
		if errUnmarshal := json.Unmarshal(body, &page); errUnmarshal != nil {
			return ResinSettings{}, resinConfigErrorf(http.StatusBadGateway, "resin platform API returned an invalid response")
		}
		exact := make([]resinPlatformSummary, 0, 1)
		folded := make([]resinPlatformSummary, 0, 1)
		for _, platform := range page.Items {
			if platform.Name == requested {
				exact = append(exact, platform)
			}
			if strings.EqualFold(platform.Name, requested) {
				folded = append(folded, platform)
			}
		}
		matches := exact
		if len(matches) == 0 {
			matches = folded
		}
		if len(matches) == 0 {
			return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin platform name was not found")
		}
		if len(matches) > 1 {
			return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin platform name is ambiguous")
		}
		matched := matches[0]
		if !resinPlatformIDPattern.MatchString(matched.ID) || strings.TrimSpace(matched.Name) == "" {
			return ResinSettings{}, resinConfigErrorf(http.StatusBadGateway, "resin platform API returned an invalid response")
		}
		settings.Platform = matched.Name
		settings.PlatformID = matched.ID
		return settings, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin admin token was rejected with status %d", status)
	default:
		if status >= 500 {
			return ResinSettings{}, resinConfigErrorf(http.StatusBadGateway, "resin platform API returned status %d", status)
		}
		return ResinSettings{}, resinConfigErrorf(http.StatusBadRequest, "resin platform API returned status %d", status)
	}
}

func resinAdminRequest(ctx context.Context, settings ResinSettings, method, path string) (int, []byte, error) {
	base := strings.TrimRight(settings.AdminURL, "/")
	req, errNew := http.NewRequestWithContext(ctx, method, base+path, nil)
	if errNew != nil {
		return 0, nil, errNew
	}
	req.Header.Set("Authorization", "Bearer "+settings.AdminToken)
	req.Header.Set("Accept", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Second, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return 0, nil, errDo
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return resp.StatusCode, body, nil
}

func validateResinProxyToken(ctx context.Context, settings ResinSettings) error {
	proxyURL, errRender := renderResinProxy(settings, "grok-validation")
	if errRender != nil {
		return resinConfigErrorf(http.StatusBadRequest, "resin proxy_template is invalid")
	}
	parsed, errParse := url.Parse(proxyURL)
	if errParse != nil || parsed.Host == "" || parsed.User == nil {
		return resinConfigErrorf(http.StatusBadRequest, "resin proxy_template is invalid")
	}
	username := parsed.User.Username()
	password, okPassword := parsed.User.Password()
	if !okPassword || username == "" || password == "" {
		return resinConfigErrorf(http.StatusBadRequest, "resin proxy_token is required")
	}
	hostPort := parsed.Host
	if parsed.Port() == "" {
		hostPort = net.JoinHostPort(parsed.Hostname(), "1080")
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, errDial := dialer.DialContext(ctx, "tcp", hostPort)
	if errDial != nil {
		return resinConfigErrorf(http.StatusBadGateway, "resin proxy is unreachable: %s", redact.SanitizeError(errDial))
	}
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if errDeadline := conn.SetDeadline(deadline); errDeadline != nil {
		return resinConfigErrorf(http.StatusBadGateway, "set resin proxy deadline: %s", redact.SanitizeError(errDeadline))
	}
	if errHandshake := socks5Authenticate(conn, username, password); errHandshake != nil {
		if errors.Is(errHandshake, errSocks5AuthRejected) {
			return resinConfigErrorf(http.StatusBadRequest, "resin proxy token was rejected")
		}
		return resinConfigErrorf(http.StatusBadGateway, "resin proxy authentication failed: %s", redact.SanitizeError(errHandshake))
	}
	return nil
}

var errSocks5AuthRejected = errors.New("socks5 authentication rejected")

func socks5Authenticate(conn net.Conn, username, password string) error {
	if conn == nil {
		return errors.New("socks5 connection is nil")
	}
	if len(username) == 0 || len(username) > 255 || len(password) == 0 || len(password) > 255 {
		return errors.New("socks5 credentials have invalid length")
	}
	if errWrite := writeAll(conn, []byte{0x05, 0x01, 0x02}); errWrite != nil {
		return errWrite
	}
	methodReply := make([]byte, 2)
	if _, errRead := io.ReadFull(conn, methodReply); errRead != nil {
		return errRead
	}
	if methodReply[0] != 0x05 {
		return errors.New("socks5 proxy returned an invalid version")
	}
	if methodReply[1] != 0x02 {
		return errors.New("socks5 proxy did not accept username/password authentication")
	}
	authRequest := make([]byte, 0, 3+len(username)+len(password))
	authRequest = append(authRequest, 0x01, byte(len(username)))
	authRequest = append(authRequest, username...)
	authRequest = append(authRequest, byte(len(password)))
	authRequest = append(authRequest, password...)
	if errWrite := writeAll(conn, authRequest); errWrite != nil {
		return errWrite
	}
	authReply := make([]byte, 2)
	if _, errRead := io.ReadFull(conn, authReply); errRead != nil {
		return errRead
	}
	if authReply[0] != 0x01 {
		return errors.New("socks5 proxy returned an invalid auth version")
	}
	if authReply[1] != 0x00 {
		return errSocks5AuthRejected
	}
	return nil
}

func writeAll(conn net.Conn, payload []byte) error {
	for len(payload) > 0 {
		written, errWrite := conn.Write(payload)
		if errWrite != nil {
			return errWrite
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}

var resinAccountChars = regexp.MustCompile(`[^a-z0-9_-]+`)

const base36Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"

type resinDeleteError = resinpkg.DeleteError

type ResinClearResult struct {
	AuthID                  string `json:"auth_id"`
	Account                 string `json:"account,omitempty"`
	ProxyURL                string `json:"proxy_url,omitempty"`
	Cleared                 bool   `json:"cleared"`
	RemoteLeaseDeleteQueued bool   `json:"remote_lease_delete_queued,omitempty"`
	Skipped                 bool   `json:"skipped,omitempty"`
	Error                   string `json:"error,omitempty"`
	Committed               bool   `json:"committed,omitempty"`
	NeedsReconcile          bool   `json:"needs_reconcile,omitempty"`
}

type ResinBindResult struct {
	AuthID   string `json:"auth_id"`
	Account  string `json:"account"`
	ProxyURL string `json:"proxy_url,omitempty"`
	DryRun   bool   `json:"dry_run"`
	Conflict bool   `json:"conflict,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (s *Service) BindResin(ctx context.Context, authID string, dryRun bool) (ResinBindResult, error) {
	cfg := s.ResinConfig()
	if !cfg.Enabled {
		return ResinBindResult{AuthID: authID, DryRun: dryRun, Error: "resin is disabled"}, proxyError("resin_disabled", 409, "resin is disabled")
	}
	if cfg.LocalOnly {
		return ResinBindResult{AuthID: authID, DryRun: dryRun, Error: "local rotation does not create new proxy bindings"}, proxyError("resin_local_only", 409, "resin API mode is required to bind new proxies")
	}
	result, err := s.bindProxy(ctx, authID, proxyBindOptions{SourceScope: "resin", SourceIDs: []string{resinSourceID}, DryRun: dryRun})
	account := result.LeaseAccount
	if account == "" {
		state := s.env.ProxyStore().ProxyAccount(authID)
		if state.Proxy.Lease != nil {
			account = state.Proxy.Lease.Account
		} else {
			account = state.Resin.Account
		}
	}
	return ResinBindResult{AuthID: authID, Account: account, ProxyURL: result.ProxyURL, DryRun: dryRun, Conflict: result.Status == "external" || result.Status == "drifted", Error: result.Error}, err
}

func (s *Service) ClearResinProxy(ctx context.Context, authID string) (ResinClearResult, error) {
	state := s.env.ProxyStore().ProxyAccount(authID)
	binding, _ := bindingForAccount(state)
	account := ""
	if binding.Lease != nil {
		account = binding.Lease.Account
	}
	result, err := s.clearProxy(ctx, authID, proxyBindOptions{AllowExternal: true, Action: "resin_clear"})
	return ResinClearResult{AuthID: authID, Account: account, ProxyURL: result.ProxyURL, Cleared: result.Committed, Skipped: result.Status == "skipped", RemoteLeaseDeleteQueued: result.RemoteLeaseDeleteQueued, Error: result.Error, Committed: result.Committed, NeedsReconcile: result.NeedsReconcile}, err
}

func (s *Service) BindUnboundResin(ctx context.Context, dryRun bool) ([]ResinBindResult, error) {
	cfg := s.ResinConfig()
	if !cfg.Enabled {
		return nil, proxyError("resin_disabled", 409, "resin is disabled")
	}
	if cfg.LocalOnly {
		return nil, proxyError("resin_local_only", 409, "resin API mode is required to bind new proxies")
	}
	items, err := s.proxyBindUnbound(ctx, proxyBindOptions{SourceScope: "resin", SourceIDs: []string{resinSourceID}, DryRun: dryRun})
	results := make([]ResinBindResult, len(items))
	for i, item := range items {
		state := s.env.ProxyStore().ProxyAccount(item.AuthID)
		account := item.LeaseAccount
		if account == "" && state.Proxy.Lease != nil {
			account = state.Proxy.Lease.Account
		}
		results[i] = ResinBindResult{AuthID: item.AuthID, Account: account, ProxyURL: item.ProxyURL, DryRun: dryRun, Error: item.Error}
	}
	return results, err
}

func (s *Service) RotateResin(ctx context.Context, authID string) error {
	if !s.ResinConfig().Enabled {
		return proxyError("resin_disabled", 409, "resin is disabled")
	}
	_, err := s.rotateProxy(ctx, authID, proxyBindOptions{SourceScope: "resin", ExpectedSourceID: resinSourceID})
	return err
}

func resinAccountFromProxyURL(proxyURL string) string {
	return resinpkg.AccountFromProxyURL(proxyURL)
}

func managedResinBinding(raw map[string]json.RawMessage, stateAccount AccountState, cfg ResinSettings) (string, bool) {
	return managedResinBindingWithProxy(auth.RawString(raw, "proxy_url"), stateAccount, cfg)
}

func managedResinBindingWithProxy(proxyURL string, stateAccount AccountState, cfg ResinSettings) (string, bool) {
	return managedResinBindingForState(proxyURL, stateAccount.Resin, cfg)
}

func managedResinBindingForState(proxyURL string, resinState ResinState, cfg ResinSettings) (string, bool) {
	storedAccount := strings.TrimSpace(resinState.Account)
	proxyURL = strings.TrimSpace(proxyURL)
	proxyAccount := resinAccountFromProxyURL(proxyURL)
	managed := resinState.Managed || storedAccount != ""
	if managed {
		if proxyAccount != "" {
			return proxyAccount, true
		}
		return storedAccount, true
	}
	if resinProxyMatchesSettings(proxyURL, cfg) {
		return proxyAccount, proxyAccount != ""
	}
	return "", false
}

func resinProxyMatchesSettings(proxyURL string, cfg ResinSettings) bool {
	if strings.TrimSpace(proxyURL) == "" || cfg.LocalOnly {
		return false
	}
	parsedProxy, errProxy := url.Parse(strings.TrimSpace(proxyURL))
	if errProxy != nil || parsedProxy.Scheme == "" || parsedProxy.Host == "" {
		return false
	}
	rendered, errRender := renderResinProxy(cfg, "grok-validation")
	if errRender != nil {
		return false
	}
	parsedSettings, errSettings := url.Parse(rendered)
	if errSettings != nil || parsedSettings.Scheme == "" || parsedSettings.Host == "" {
		return false
	}
	return parsedProxy.Scheme == parsedSettings.Scheme && strings.EqualFold(parsedProxy.Host, parsedSettings.Host)
}

func rewriteResinProxyAccount(proxyURL, account string) (string, error) {
	return resinpkg.RewriteAccount(proxyURL, account)
}

func resinAccountBase(raw map[string]json.RawMessage) string {
	source := auth.Email(raw)
	if source == "" {
		source = auth.Label(raw)
	}
	if source == "" {
		source = "auth_" + auth.AccountHash(raw)
	}
	if at := strings.IndexByte(source, '@'); at >= 0 {
		source = source[:at]
	}
	source = strings.ToLower(strings.TrimSpace(source))
	source = resinAccountChars.ReplaceAllString(source, "_")
	for strings.Contains(source, "__") {
		source = strings.ReplaceAll(source, "__", "_")
	}
	source = strings.Trim(source, "_")
	if source == "" {
		source = "auth_" + auth.AccountHash(raw)
	}
	if len(source) > 48 {
		source = source[:48]
		source = strings.TrimRight(source, "_")
	}
	return source
}

func randomBase36(length int) (string, error) {
	if length <= 0 {
		return "", nil
	}
	limit := big.NewInt(int64(len(base36Alphabet)))
	result := make([]byte, length)
	for i := 0; i < length; i++ {
		value, errValue := rand.Int(rand.Reader, limit)
		if errValue != nil {
			return "", errValue
		}
		result[i] = base36Alphabet[value.Int64()]
	}
	return string(result), nil
}

func renderResinProxy(cfg ResinSettings, account string) (string, error) {
	return resinpkg.RenderProxy(cfg.ProxyTemplate, cfg.Platform, account, cfg.ProxyToken)
}

func deleteResinLease(ctx context.Context, adminURL, platformID, adminToken, account string) error {
	return resinpkg.DeleteLease(ctx, adminURL, platformID, adminToken, account)
}

func maskProxyURL(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parsed, errParse := url.Parse(value)
	if errParse != nil {
		return "[redacted]"
	}
	if parsed.Host == "" {
		return parsed.Scheme + "://[redacted]"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func maskAccountIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "@") {
		return maskEmail(value)
	}
	return maskAuthID(value)
}

func maskEmail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	at := strings.IndexByte(value, '@')
	if at <= 0 {
		return maskAuthID(value)
	}
	local := value[:at]
	domain := value[at:]
	switch {
	case len(local) <= 2:
		return local[:1] + "***" + domain
	case len(local) >= 8:
		return local[:3] + "***" + local[len(local)-2:] + domain
	default:
		return local[:2] + "***" + local[len(local)-1:] + domain
	}
}

func maskAuthID(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12] + "..." + value[len(value)-4:]
}

const (
	resinLeaseDeleteScanInterval = 5 * time.Second
	resinLeaseDeleteRetryBase    = 30 * time.Second
	resinLeaseDeleteRetryMax     = time.Hour
)

func resinLeaseDeleteKey(adminURL, platformID, account string) string {
	return strings.TrimRight(strings.TrimSpace(adminURL), "/") + "\x00" +
		strings.TrimSpace(platformID) + "\x00" + strings.TrimSpace(account)
}

func resinAdminTokenKey(adminURL, token string) string {
	normalizedURL := strings.ToLower(strings.TrimRight(strings.TrimSpace(adminURL), "/"))
	sum := sha256.Sum256([]byte(normalizedURL + "\x00" + strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func gcResinAdminTokens(state *State) {
	if state == nil || len(state.ResinAdminTokens) == 0 {
		return
	}
	used := make(map[string]struct{}, len(state.ResinLeaseDeletes))
	for _, job := range state.ResinLeaseDeletes {
		if job.AdminTokenRef != "" {
			used[job.AdminTokenRef] = struct{}{}
		}
	}
	for ref := range state.ResinAdminTokens {
		if _, ok := used[ref]; !ok {
			delete(state.ResinAdminTokens, ref)
		}
	}
}

func newResinLeaseDelete(authID string, settings ResinSettings, account string) ResinLeaseDelete {
	now := time.Now().UTC()
	return ResinLeaseDelete{AuthID: authID, AdminURL: settings.AdminURL, PlatformID: settings.PlatformID, Account: account, SnapshotID: resinSnapshotID(settings), Status: "pending", CreatedAt: now, NextAttemptAt: now}
}

func queueResinLeaseDelete(state *State, authID string, settings ResinSettings, account string) bool {
	account = strings.TrimSpace(account)
	if state == nil || account == "" {
		return false
	}
	key := resinLeaseDeleteKey(settings.AdminURL, settings.PlatformID, account)
	for index := range state.ResinLeaseDeletes {
		job := state.ResinLeaseDeletes[index]
		if resinLeaseDeleteKey(job.AdminURL, job.PlatformID, job.Account) == key {
			return false
		}
	}
	now := time.Now().UTC()
	adminTokenRef := ""
	if token := strings.TrimSpace(settings.AdminToken); token != "" {
		if state.ResinAdminTokens == nil {
			state.ResinAdminTokens = make(map[string]string)
		}
		adminTokenRef = resinAdminTokenKey(settings.AdminURL, token)
		state.ResinAdminTokens[adminTokenRef] = token
	}
	state.ResinLeaseDeletes = append(state.ResinLeaseDeletes, ResinLeaseDelete{
		AuthID:        strings.TrimSpace(authID),
		AdminURL:      strings.TrimSpace(settings.AdminURL),
		AdminTokenRef: adminTokenRef,
		PlatformID:    strings.TrimSpace(settings.PlatformID),
		Account:       account,
		CreatedAt:     now,
		NextAttemptAt: now,
	})
	return true
}

func queueResinLeaseDeleteAndRecord(state *State, authID string, settings ResinSettings, account string) bool {
	added := queueResinLeaseDelete(state, authID, settings, account)
	if added {
		appendEvent(state, Event{
			Time:   time.Now().UTC(),
			AuthID: strings.TrimSpace(authID),
			Action: "resin_lease_delete",
			Result: "queued",
			Detail: "account=" + strings.TrimSpace(account),
		})
	}
	return added
}

func (s *Service) enqueueResinLeaseDelete(authID string, settings ResinSettings, account string) (bool, error) {
	if s == nil || s.env == nil || s.env.ProxyStore() == nil {
		return false, errors.New("state store is not initialized")
	}
	added := false
	errUpdate := s.env.ProxyStore().UpdateIfChanged(func(state *State) (bool, error) {
		added = queueResinLeaseDeleteAndRecord(state, authID, settings, account)
		return added, nil
	})
	if errUpdate != nil {
		return false, errUpdate
	}
	if added {
		s.wakeResinLeaseDeleteLoop()
	}
	return added, nil
}

func (s *Service) wakeResinLeaseDeleteLoop() {
	if s == nil || s.env == nil || s.resinLeaseDeleteWake == nil {
		return
	}
	select {
	case s.resinLeaseDeleteWake <- struct{}{}:
	default:
	}
}

func (s *Service) resinLeaseDeleteLoop(ctx context.Context) {
	if s == nil || s.env == nil || ctx == nil {
		return
	}
	_ = s.processDueResinLeaseDeletes(ctx, time.Now().UTC())
	ticker := time.NewTicker(resinLeaseDeleteScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.resinLeaseDeleteWake:
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		_ = s.processDueResinLeaseDeletes(ctx, time.Now().UTC())
	}
}

func (s *Service) processDueResinLeaseDeletes(ctx context.Context, now time.Time) int {
	if s == nil || s.env == nil || s.env.ProxyStore() == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	jobs := s.env.ProxyStore().ResinLeaseDeletesSnapshot()
	processed := 0
	for _, job := range jobs {
		if ctx.Err() != nil {
			break
		}
		if !job.NextAttemptAt.IsZero() && job.NextAttemptAt.After(now) {
			continue
		}
		if job.Paused || job.Status == "permanent_failure" {
			continue
		}
		s.processResinLeaseDelete(ctx, job)
		processed++
		if processed >= 64 {
			break
		}
	}
	return processed
}

func (s *Service) processResinLeaseDelete(ctx context.Context, job ResinLeaseDelete) {
	if ctx == nil {
		ctx = context.Background()
	}
	adminURL := job.AdminURL
	platformID := job.PlatformID
	adminToken := strings.TrimSpace(job.AdminToken)
	if adminToken == "" {
		adminToken = s.env.ProxyStore().ResinAdminToken(job.AdminTokenRef)
	}
	var errDelete error
	if job.SnapshotID != "" {
		handler := resinpkg.CleanupHandler{Resolve: func(_ context.Context, id string) (resinpkg.IntegrationSnapshot, error) {
			settings, ok := s.env.ProxyStore().ResinSnapshot(id)
			if !ok {
				return resinpkg.IntegrationSnapshot{}, errors.New("resin cleanup snapshot is missing")
			}
			return resinpkg.IntegrationSnapshot{ID: id, AdminURL: settings.AdminURL, PlatformID: settings.PlatformID, AdminToken: settings.AdminToken}, nil
		}}
		errDelete = handler.Delete(ctx, proxy.LeaseRef{Provider: "resin", SnapshotID: job.SnapshotID, PlatformID: job.PlatformID, Account: job.Account})
	} else {
		switch {
		case strings.TrimSpace(adminURL) == "":
			errDelete = errors.New("resin admin URL is missing")
		case strings.TrimSpace(platformID) == "":
			errDelete = errors.New("resin platform ID is missing")
		case strings.TrimSpace(adminToken) == "":
			errDelete = errors.New("resin admin token is missing")
		default:
			errDelete = deleteResinLease(ctx, adminURL, platformID, adminToken, job.Account)
		}
	}
	if ctx.Err() != nil {
		return
	}
	if errDelete == nil {
		s.finishResinLeaseDelete(job)
		return
	}
	s.rescheduleResinLeaseDelete(job, errDelete, time.Now().UTC())
}

func (s *Service) finishResinLeaseDelete(job ResinLeaseDelete) {
	_ = s.env.ProxyStore().UpdateIfChanged(func(state *State) (bool, error) {
		removed := removeResinLeaseDelete(state, job)
		if removed {
			gcResinAdminTokens(state)
			s.env.ProxyRecordEvent(state, job.AuthID, "", "resin_lease_delete", "succeeded", "account="+job.Account)
		}
		return removed, nil
	})
}

func (s *Service) rescheduleResinLeaseDelete(job ResinLeaseDelete, errDelete error, now time.Time) {
	attempts := job.Attempts + 1
	delay := resinLeaseDeleteBackoff(attempts)
	status := "retry_wait"
	var remote *resinDeleteError
	if errors.As(errDelete, &remote) {
		if remote.RetryAfter > delay {
			delay = remote.RetryAfter
		}
		if remote.Permanent {
			status = "permanent_failure"
		}
		if remote.Status == 401 || remote.Status == 403 {
			status = "credential_blocked"
			delay = max(delay, time.Hour)
		}
	}
	nextAttemptAt := now.Add(delay)
	reason := redact.SanitizeError(errDelete)
	_ = s.env.ProxyStore().UpdateIfChanged(func(state *State) (bool, error) {
		found := false
		for index := range state.ResinLeaseDeletes {
			candidate := &state.ResinLeaseDeletes[index]
			if resinLeaseDeleteKey(candidate.AdminURL, candidate.PlatformID, candidate.Account) !=
				resinLeaseDeleteKey(job.AdminURL, job.PlatformID, job.Account) {
				continue
			}
			candidate.Attempts = attempts
			candidate.NextAttemptAt = nextAttemptAt
			candidate.LastError = reason
			candidate.Status = status
			s.env.ProxyRecordEvent(state, candidate.AuthID, "", "resin_lease_delete", "retry_scheduled",
				fmt.Sprintf("account=%s attempt=%d retry_in=%s error=%s", candidate.Account, attempts, delay.Round(time.Second), reason))
			found = true
			break
		}
		return found, nil
	})
}

func removeResinLeaseDelete(state *State, job ResinLeaseDelete) bool {
	if state == nil {
		return false
	}
	key := resinLeaseDeleteKey(job.AdminURL, job.PlatformID, job.Account)
	for index := range state.ResinLeaseDeletes {
		candidate := state.ResinLeaseDeletes[index]
		if resinLeaseDeleteKey(candidate.AdminURL, candidate.PlatformID, candidate.Account) != key {
			continue
		}
		state.ResinLeaseDeletes = append(state.ResinLeaseDeletes[:index], state.ResinLeaseDeletes[index+1:]...)
		return true
	}
	return false
}

func resinLeaseDeleteBackoff(attempts int) time.Duration {
	if attempts <= 1 {
		return resinLeaseDeleteRetryBase
	}
	delay := resinLeaseDeleteRetryBase
	for attempt := 1; attempt < attempts; attempt++ {
		if delay >= resinLeaseDeleteRetryMax/2 {
			return resinLeaseDeleteRetryMax
		}
		delay *= 2
	}
	if delay > resinLeaseDeleteRetryMax {
		return resinLeaseDeleteRetryMax
	}
	return delay
}
