package resin

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"grok-quality-guard/internal/proxy"
)

type DeleteError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
	Permanent  bool
}

func (e *DeleteError) Error() string {
	return fmt.Sprintf("resin lease delete failed with status %d", e.Status)
}

var cleanupClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns: 16, MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second,
		ForceAttemptHTTP2: true, DisableCompression: true,
	},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

func deleteURL(adminURL, platformID, account string) (string, error) {
	if !validResource(platformID) || !validResource(account) {
		return "", fmt.Errorf("resin cleanup resource identity is invalid")
	}
	adminURL = strings.TrimSpace(adminURL)
	parsed, err := url.Parse(adminURL)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.ForceQuery || parsed.RawQuery != "" || strings.ContainsRune(adminURL, '#') || strings.ContainsFunc(adminURL, unicode.IsControl) {
		return "", fmt.Errorf("resin admin URL is invalid")
	}
	port := 80
	if parsed.Scheme == "https" {
		port = 443
	}
	portText := parsed.Port()
	if portText != "" {
		port, err = strconv.Atoi(portText)
		if err != nil {
			return "", fmt.Errorf("resin admin URL port is invalid")
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return "", fmt.Errorf("resin admin URL port is invalid")
	}
	endpoint, err := (proxy.Endpoint{Protocol: proxy.Protocol(parsed.Scheme), Host: parsed.Hostname(), Port: port}).Normalize()
	if err != nil {
		return "", fmt.Errorf("resin admin URL host or port is invalid")
	}
	if portText != "" {
		parsed.Host = net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
	} else if strings.ContainsRune(endpoint.Host, ':') {
		parsed.Host = "[" + endpoint.Host + "]"
	} else {
		parsed.Host = endpoint.Host
	}
	rawPrefix := strings.TrimRight(parsed.EscapedPath(), "/")
	prefix := strings.TrimRight(parsed.Path, "/")
	parsed.Path = prefix + "/api/v1/platforms/" + platformID + "/leases/" + account
	parsed.RawPath = rawPrefix + "/api/v1/platforms/" + url.PathEscape(platformID) + "/leases/" + url.PathEscape(account)
	return parsed.String(), nil
}

func validResource(value string) bool {
	return value != "" && value != "." && value != ".." && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

// DeleteLease is network-only cleanup. It uses an immutable caller-provided
// integration snapshot, never redirects credentials, and treats 204/404 as
// idempotent success. Response bodies are discarded rather than exposed.
func DeleteLease(ctx context.Context, adminURL, platformID, adminToken, account string) error {
	if adminToken == "" || !utf8.ValidString(adminToken) || strings.ContainsFunc(adminToken, unicode.IsControl) {
		return fmt.Errorf("resin cleanup credential is missing or invalid")
	}
	endpoint, err := deleteURL(adminURL, platformID, account)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("resin cleanup request is invalid")
	}
	request.Header.Set("Authorization", "Bearer "+adminToken)
	response, err := cleanupClient.Do(request)
	if err != nil {
		// No credentials occur in the URL; retain cancellation/timeout identity.
		return err
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 512))
	switch response.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		status := response.StatusCode
		permanent := status >= 300 && status < 500 && status != http.StatusUnauthorized && status != http.StatusForbidden && status != http.StatusTooManyRequests
		return &DeleteError{Status: status, RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now()), Permanent: permanent}
	}
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(min(seconds, 3600)) * time.Second
	}
	at, err := http.ParseTime(value)
	if err != nil || !at.After(now) {
		return 0
	}
	return min(at.Sub(now), time.Hour)
}
