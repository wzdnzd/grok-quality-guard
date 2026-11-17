// Resin validation and immutable lease cleanup.
package proxy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"grok-quality-guard/internal/app/auth"
	resinpkg "grok-quality-guard/internal/app/proxy/resin"
	"grok-quality-guard/internal/app/redact"
	statepkg "grok-quality-guard/internal/app/state"
	proxy "grok-quality-guard/internal/proxy"
)

var resinPlatformIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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

type resinPlatformSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func normalizeResinAdminSettings(integration resinIntegration) (resinIntegration, error) {
	integration.Platform = strings.TrimSpace(integration.Platform)
	integration.AdminURL = strings.TrimRight(strings.TrimSpace(integration.AdminURL), "/")
	if integration.Platform == "" {
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin platform is required")
	}
	if integration.AdminURL == "" {
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url is required")
	}
	parsedAdmin, errAdmin := url.Parse(integration.AdminURL)
	if errAdmin != nil || parsedAdmin.Scheme == "" || parsedAdmin.Host == "" {
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url is invalid")
	}
	if parsedAdmin.Scheme != "http" && parsedAdmin.Scheme != "https" {
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url must use http or https")
	}
	if parsedAdmin.User != nil || parsedAdmin.RawQuery != "" || parsedAdmin.Fragment != "" {
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url must not contain userinfo, query, or fragment")
	}
	integration.AdminURL = strings.TrimRight(parsedAdmin.String(), "/")
	if integration.AdminToken == "" {
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_token is required")
	}
	if len(integration.AdminToken) > 4096 {
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_token is too long")
	}
	return integration, nil
}

func validateResinSourceIntegration(ctx context.Context, template proxy.Template, config proxy.ResinSourceConfig) (resinValidationResult, error) {
	result := resinValidationResult{}
	if config.LocalOnly {
		result.LocalOnly = true
		result.ValidatedAt = time.Now().UTC()
		return result, nil
	}
	integration, errAdmin := normalizeResinAdminSettings(resinIntegrationForSource(config))
	if errAdmin != nil {
		return result, errAdmin
	}
	if errAdmin := validateResinAdminAPI(ctx, integration); errAdmin != nil {
		return result, errAdmin
	}
	result.AdminAPIReachable = true
	result.AdminTokenValid = true
	resolved, errPlatform := resolveResinPlatformByName(ctx, integration)
	if errPlatform != nil {
		return result, errPlatform
	}
	result.Platform = resolved.Platform
	result.PlatformID = resolved.PlatformID
	result.PlatformValid = true
	validationConfig := config
	validationConfig.Platform = resolved.Platform
	validationConfig.PlatformID = resolved.PlatformID
	if errProxy := validateResinProxyToken(ctx, template, validationConfig); errProxy != nil {
		return result, errProxy
	}
	result.ProxyTokenValid = true
	result.ValidatedAt = time.Now().UTC()
	return result, nil
}

func validateResinAdminAPI(ctx context.Context, integration resinIntegration) error {
	status, _, errRequest := resinAdminRequest(ctx, integration, http.MethodGet, "/api/v1/system/info")
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

func resolveResinPlatformByName(ctx context.Context, integration resinIntegration) (resinIntegration, error) {
	requested := strings.TrimSpace(integration.Platform)
	if requested == "" {
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin platform is required")
	}
	query := url.Values{}
	query.Set("keyword", requested)
	query.Set("sort_by", "name")
	query.Set("sort_order", "asc")
	query.Set("limit", "1000")
	query.Set("offset", "0")
	status, body, errRequest := resinAdminRequest(ctx, integration, http.MethodGet, "/api/v1/platforms?"+query.Encode())
	if errRequest != nil {
		return resinIntegration{}, resinConfigErrorf(http.StatusBadGateway, "resin platform validation failed: %s", redact.SanitizeError(errRequest))
	}
	switch status {
	case http.StatusOK:
		var page struct {
			Items []resinPlatformSummary `json:"items"`
		}
		if errUnmarshal := json.Unmarshal(body, &page); errUnmarshal != nil {
			return resinIntegration{}, resinConfigErrorf(http.StatusBadGateway, "resin platform API returned an invalid response")
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
			return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin platform name was not found")
		}
		if len(matches) > 1 {
			return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin platform name is ambiguous")
		}
		matched := matches[0]
		if !resinPlatformIDPattern.MatchString(matched.ID) || strings.TrimSpace(matched.Name) == "" {
			return resinIntegration{}, resinConfigErrorf(http.StatusBadGateway, "resin platform API returned an invalid response")
		}
		integration.Platform = matched.Name
		integration.PlatformID = matched.ID
		return integration, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin admin token was rejected with status %d", status)
	default:
		if status >= 500 {
			return resinIntegration{}, resinConfigErrorf(http.StatusBadGateway, "resin platform API returned status %d", status)
		}
		return resinIntegration{}, resinConfigErrorf(http.StatusBadRequest, "resin platform API returned status %d", status)
	}
}

func resinAdminRequest(ctx context.Context, integration resinIntegration, method, path string) (int, []byte, error) {
	base := strings.TrimRight(integration.AdminURL, "/")
	req, errNew := http.NewRequestWithContext(ctx, method, base+path, nil)
	if errNew != nil {
		return 0, nil, errNew
	}
	req.Header.Set("Authorization", "Bearer "+integration.AdminToken)
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

func validateResinProxyToken(ctx context.Context, template proxy.Template, config proxy.ResinSourceConfig) error {
	provider, errProvider := resinpkg.NewTemplateProvider(template, resinpkg.ProviderSettings{Platform: config.Platform, LocalOnly: config.LocalOnly})
	if errProvider != nil {
		return resinConfigErrorf(http.StatusBadRequest, "resin proxy configuration is invalid")
	}
	rendered, errRender := provider.Generate(ctx, proxy.GenerateRequest{SourceID: template.ID, SourceKind: proxy.SourceResin, Revision: template.Revision, AccountBase: "grok-validation"})
	if errRender != nil {
		return resinConfigErrorf(http.StatusBadRequest, "resin proxy configuration is invalid")
	}
	parsed := rendered.Endpoint
	if parsed.Username == "" || !parsed.HasPassword || parsed.Password == "" {
		return resinConfigErrorf(http.StatusBadRequest, "resin proxy_token is required")
	}
	hostPort := net.JoinHostPort(parsed.Host, fmt.Sprint(parsed.Port))
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
	if errHandshake := socks5Authenticate(conn, parsed.Username, parsed.Password); errHandshake != nil {
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

type resinDeleteError = resinpkg.DeleteError

func resinAccountFromProxyURL(proxyURL string) string {
	return resinpkg.AccountFromProxyURL(proxyURL)
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

func maskProxyURL(value string) string {
	return proxy.MaskProxyURL(value)
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

const (
	resinLeaseDeleteScanInterval = 5 * time.Second
	resinLeaseDeleteRetryBase    = 30 * time.Second
	resinLeaseDeleteRetryMax     = time.Hour
)

func resinLeaseDeleteKey(adminURL, platformID, account string) string {
	return strings.TrimRight(strings.TrimSpace(adminURL), "/") + "\x00" +
		strings.TrimSpace(platformID) + "\x00" + strings.TrimSpace(account)
}

func newResinLeaseDelete(authID string, snapshot proxy.ResinSnapshot, account string) ResinLeaseDelete {
	now := time.Now().UTC()
	return ResinLeaseDelete{
		AuthID:        authID,
		AdminURL:      snapshot.AdminURL,
		PlatformID:    snapshot.PlatformID,
		Account:       account,
		SnapshotID:    statepkg.ResinSnapshotID(snapshot),
		Status:        "pending",
		CreatedAt:     now,
		NextAttemptAt: now,
	}
}

func deleteResinLease(ctx context.Context, adminURL, platformID, adminToken, account string) error {
	return resinpkg.DeleteLease(ctx, adminURL, platformID, adminToken, account)
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
	if job.SnapshotID == "" {
		s.rescheduleResinLeaseDelete(job, errors.New("resin cleanup snapshot is missing"), time.Now().UTC())
		return
	}
	handler := resinpkg.CleanupHandler{Resolve: func(_ context.Context, id string) (resinpkg.IntegrationSnapshot, error) {
		snapshot, ok := s.env.ProxyStore().ResinSnapshot(id)
		if !ok {
			return resinpkg.IntegrationSnapshot{}, errors.New("resin cleanup snapshot is missing")
		}
		return resinpkg.IntegrationSnapshot{ID: id, AdminURL: snapshot.AdminURL, PlatformID: snapshot.PlatformID, AdminToken: snapshot.AdminToken}, nil
	}}
	errDelete := handler.Delete(ctx, proxy.LeaseRef{Provider: "resin", SnapshotID: job.SnapshotID, PlatformID: job.PlatformID, Account: job.Account})
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
