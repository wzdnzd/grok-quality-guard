package resin

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"grok-quality-guard/internal/proxy"
)

// AccountFormat returns the username pattern without the fixed Resin scheme,
// password, host, or port.
func AccountFormat(rawTemplate string) (string, error) {
	rawTemplate = strings.TrimSpace(rawTemplate)
	_, rest, ok := strings.Cut(rawTemplate, "://")
	if !ok {
		return "", fmt.Errorf("resin proxy template is missing scheme")
	}
	authority := rest
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		return "", fmt.Errorf("resin proxy template requires userinfo")
	}
	username, _, _ := strings.Cut(authority[:at], ":")
	decoded, err := url.PathUnescape(username)
	if err != nil || strings.TrimSpace(decoded) == "" {
		return "", fmt.Errorf("resin proxy template username is invalid")
	}
	return decoded, nil
}

// AccountFromProxyURL extracts an identity; callers must separately verify
// persisted management evidence before treating that identity as a managed lease.
func AccountFromProxyURL(proxyURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || parsed.User == nil {
		return ""
	}
	username := parsed.User.Username()
	dot := strings.LastIndexByte(username, '.')
	if dot <= 0 || dot >= len(username)-1 {
		return ""
	}
	return username[dot+1:]
}

// RewriteAccount preserves the existing URL structure for explicit local
// rotation while replacing only the account suffix after the final dot.
func RewriteAccount(proxyURL, account string) (string, error) {
	if account == "" || len(account) > 128 || !utf8.ValidString(account) || strings.ContainsFunc(account, unicode.IsControl) || strings.ContainsRune(account, '.') {
		return "", fmt.Errorf("resin account identity is invalid")
	}
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User == nil {
		return "", fmt.Errorf("proxy_url is invalid")
	}
	username := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	if username == "" || !hasPassword {
		return "", fmt.Errorf("proxy_url must contain username and password")
	}
	dot := strings.LastIndexByte(username, '.')
	if dot <= 0 || dot >= len(username)-1 {
		return "", fmt.Errorf("proxy_url username does not contain a resin account")
	}
	username = username[:dot+1] + account
	if (parsed.Scheme == "socks5" || parsed.Scheme == "socks5h") && (len(username) > 255 || len(password) > 255) {
		return "", fmt.Errorf("resin credentials exceed the SOCKS5 byte limit")
	}
	parsed.User = url.UserPassword(username, password)
	return parsed.String(), nil
}

// RenderProxy is the migration/validation convenience entry point. Repeated
// binding generation uses Provider, whose compiled template is reused.
func RenderProxy(rawTemplate, platform, account, proxyToken string) (string, error) {
	template, err := MigrateTemplate(rawTemplate, platform, proxyToken)
	if err != nil {
		return "", err
	}
	compiled, err := proxy.CompileTemplate(template)
	if err != nil {
		return "", err
	}
	if account == "" || len(account) > 128 || !utf8.ValidString(account) || strings.ContainsFunc(account, unicode.IsControl) {
		return "", fmt.Errorf("resin account identity is invalid")
	}
	rendered, err := compiled.RenderResolved(map[string]string{"account": account})
	if err != nil {
		return "", err
	}
	return rendered.URL, nil
}
