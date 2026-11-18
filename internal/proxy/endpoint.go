package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

type Protocol string

const (
	ProtocolHTTP    Protocol = "http"
	ProtocolHTTPS   Protocol = "https"
	ProtocolSOCKS5  Protocol = "socks5"
	ProtocolSOCKS5H Protocol = "socks5h"
)

var ErrMissingScheme = errors.New("proxy url is missing scheme")

type Endpoint struct {
	Protocol    Protocol `json:"protocol"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	Username    string   `json:"username,omitempty"`
	Password    string   `json:"password,omitempty"`
	HasUser     bool     `json:"has_user,omitempty"`
	HasPassword bool     `json:"has_password,omitempty"`
}

func ParseEndpoint(raw string) (Endpoint, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Endpoint{}, invalid("endpoint_empty", "url", "proxy url is empty")
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return Endpoint{}, ErrMissingScheme
	}
	protocol := Protocol(strings.ToLower(scheme))
	if !validProtocol(protocol) {
		return Endpoint{}, invalid("protocol_unsupported", "protocol", "unsupported proxy protocol")
	}
	if len(raw) > maxTemplateBytes || hasControl(raw) || !utf8.ValidString(raw) {
		return Endpoint{}, invalid("endpoint_invalid", "url", "proxy url contains invalid characters or is too large")
	}
	// Standard URLs take precedence. Some providers place authentication after
	// the host or separate it with colons; try those shapes only as fallbacks.
	if endpoint, err := parseStandardEndpoint(string(protocol) + "://" + rest); err == nil {
		return endpoint, nil
	}
	if endpoint, err := parseAddressFirstEndpoint(protocol, rest); err == nil {
		return endpoint, nil
	} else if !strings.ContainsRune(rest, '@') {
		return Endpoint{}, err
	}
	return parseHostPortAtCredentialsEndpoint(protocol, rest)
}

func parseStandardEndpoint(raw string) (Endpoint, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		// net/url errors include their input, which may contain credentials.
		return Endpoint{}, invalid("endpoint_invalid", "url", "proxy url is invalid")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return Endpoint{}, invalid("path_not_allowed", "url", "proxy url must not contain a path")
	}
	if parsed.ForceQuery || parsed.RawQuery != "" || strings.Contains(raw, "#") {
		return Endpoint{}, invalid("parameters_not_allowed", "url", "proxy url must not contain query or fragment")
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil || portText == "" {
		return Endpoint{}, invalid("endpoint_invalid", "port", "proxy url requires a host and explicit port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return Endpoint{}, invalid("port_invalid", "port", "proxy url port is invalid")
	}
	endpoint := Endpoint{Protocol: Protocol(parsed.Scheme), Host: host, Port: port}
	if parsed.User != nil {
		endpoint.Username = parsed.User.Username()
		endpoint.Password, endpoint.HasPassword = parsed.User.Password()
		endpoint.HasUser = true
	}
	return endpoint.Normalize()
}

func parseAddressFirstEndpoint(protocol Protocol, rest string) (Endpoint, error) {
	if strings.ContainsAny(rest, "/?#") {
		return Endpoint{}, invalid("address_first_invalid", "url", "address-first proxy input cannot contain path, query, or fragment")
	}
	// Bracketed IPv6 is an address, not a sequence of credential separators.
	addressEnd := strings.IndexByte(rest, ':')
	if strings.HasPrefix(rest, "[") {
		bracket := strings.IndexByte(rest, ']')
		if bracket < 0 || bracket+1 >= len(rest) || rest[bracket+1] != ':' {
			return Endpoint{}, invalid("address_first_invalid", "host", "address-first proxy host is invalid")
		}
		addressEnd = bracket + 1
	}
	if addressEnd < 1 {
		return Endpoint{}, invalid("address_first_invalid", "url", "address-first proxy input must be host:port:username:password")
	}
	portText, credentials, ok := strings.Cut(rest[addressEnd+1:], ":")
	if !ok {
		return Endpoint{}, invalid("address_first_invalid", "url", "address-first proxy input must be host:port:username:password")
	}
	username, password, ok := strings.Cut(credentials, ":")
	if !ok || username == "" {
		return Endpoint{}, invalid("address_first_invalid", "username", "address-first proxy credentials are invalid")
	}
	// Decode URL input exactly once; field-based Endpoint values are literal.
	username, err := url.PathUnescape(username)
	if err != nil {
		return Endpoint{}, invalid("credential_encoding_invalid", "username", "proxy username encoding is invalid")
	}
	password, err = url.PathUnescape(password)
	if err != nil {
		return Endpoint{}, invalid("credential_encoding_invalid", "password", "proxy password encoding is invalid")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return Endpoint{}, invalid("port_invalid", "port", "proxy url port is invalid")
	}
	host := rest[:addressEnd]
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	return (Endpoint{Protocol: protocol, Host: host, Port: port, Username: username, Password: password, HasUser: true, HasPassword: true}).Normalize()
}

func parseHostPortAtCredentialsEndpoint(protocol Protocol, rest string) (Endpoint, error) {
	if strings.ContainsAny(rest, "/?#") {
		return Endpoint{}, invalid("host_port_at_invalid", "url", "host:port@user:pass input cannot contain path, query, or fragment")
	}
	address, credentials, ok := strings.Cut(rest, "@")
	if !ok || address == "" || credentials == "" {
		return Endpoint{}, invalid("host_port_at_invalid", "url", "proxy input must be host:port@username:password")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil || portText == "" {
		return Endpoint{}, invalid("host_port_at_invalid", "port", "proxy input requires host:port before @")
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return Endpoint{}, invalid("port_invalid", "port", "proxy url port is invalid")
	}
	username, password, ok := strings.Cut(credentials, ":")
	if !ok || username == "" {
		return Endpoint{}, invalid("host_port_at_invalid", "username", "proxy credentials must be username:password after @")
	}
	username, err = url.PathUnescape(username)
	if err != nil {
		return Endpoint{}, invalid("credential_encoding_invalid", "username", "proxy username encoding is invalid")
	}
	password, err = url.PathUnescape(password)
	if err != nil {
		return Endpoint{}, invalid("credential_encoding_invalid", "password", "proxy password encoding is invalid")
	}
	return (Endpoint{Protocol: protocol, Host: host, Port: port, Username: username, Password: password, HasUser: true, HasPassword: true}).Normalize()
}

func validProtocol(protocol Protocol) bool {
	return protocol == ProtocolHTTP || protocol == ProtocolHTTPS || protocol == ProtocolSOCKS5 || protocol == ProtocolSOCKS5H
}

func hasControl(value string) bool {
	return strings.ContainsFunc(value, unicode.IsControl)
}

func normalizeHost(host string) (string, error) {
	if host == "" || !utf8.ValidString(host) || strings.ContainsAny(host, "%/@\\?#{}[]") || strings.ContainsFunc(host, unicode.IsSpace) || hasControl(host) {
		return "", invalid("host_invalid", "host", "proxy host is invalid")
	}
	if address, err := netip.ParseAddr(host); err == nil {
		return address.String(), nil
	}
	if strings.ContainsRune(host, ':') {
		return "", invalid("host_invalid", "host", "proxy IPv6 address is invalid")
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", invalid("host_invalid", "host", "proxy hostname is invalid")
	}
	ascii = strings.ToLower(ascii)
	domain := strings.TrimSuffix(ascii, ".")
	if len(domain) == 0 || len(domain) > 253 {
		return "", invalid("host_invalid", "host", "proxy hostname is invalid")
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", invalid("host_invalid", "host", "proxy hostname label is invalid")
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return "", invalid("host_invalid", "host", "proxy hostname label is invalid")
			}
		}
	}
	return ascii, nil
}

// Normalize validates literal fields and normalizes only the scheme and host.
func (e Endpoint) Normalize() (Endpoint, error) {
	e.Protocol = Protocol(strings.ToLower(string(e.Protocol)))
	if !validProtocol(e.Protocol) {
		return Endpoint{}, invalid("protocol_unsupported", "protocol", "unsupported proxy protocol")
	}
	if e.Port < 1 || e.Port > 65535 {
		return Endpoint{}, invalid("port_invalid", "port", "proxy port is invalid")
	}
	var err error
	e.Host, err = normalizeHost(e.Host)
	if err != nil {
		return Endpoint{}, err
	}
	if e.HasPassword && (!e.HasUser || e.Username == "") {
		return Endpoint{}, invalid("username_required", "username", "proxy password requires a username")
	}
	if !e.HasUser && e.Username != "" || !e.HasPassword && e.Password != "" {
		return Endpoint{}, invalid("credential_flags_invalid", "credentials", "proxy credential presence flags are inconsistent")
	}
	if !utf8.ValidString(e.Username) || hasControl(e.Username) {
		return Endpoint{}, invalid("username_invalid", "username", "proxy username contains invalid characters")
	}
	if !utf8.ValidString(e.Password) || hasControl(e.Password) {
		return Endpoint{}, invalid("password_invalid", "password", "proxy password contains invalid characters")
	}
	limit := credentialLimit(e.Protocol)
	if len(e.Username) > limit {
		return Endpoint{}, invalid("username_too_long", "username", "proxy username exceeds the protocol byte limit")
	}
	if len(e.Password) > limit {
		return Endpoint{}, invalid("password_too_long", "password", "proxy password exceeds the protocol byte limit")
	}
	return e, nil
}

func credentialLimit(protocol Protocol) int {
	if protocol == ProtocolSOCKS5 || protocol == ProtocolSOCKS5H {
		return 255
	}
	return 1024
}

func (e Endpoint) CanonicalURL() string {
	if normalized, err := e.Normalize(); err == nil {
		e = normalized
	}
	return e.canonicalURL()
}

func (e Endpoint) canonicalURL() string {
	parsed := &url.URL{Scheme: string(e.Protocol), Host: net.JoinHostPort(e.Host, strconv.Itoa(e.Port))}
	if e.HasUser {
		if e.HasPassword {
			parsed.User = url.UserPassword(e.Username, e.Password)
		} else {
			parsed.User = url.User(e.Username)
		}
	}
	return parsed.String()
}

func (e Endpoint) MaskedURL() string {
	if normalized, err := e.Normalize(); err == nil {
		e = normalized
	}
	return e.maskedURL()
}

func (e Endpoint) maskedURL() string {
	return string(e.Protocol) + "://" + net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// MaskedCredentialURL keeps the standard proxy URL shape while hiding the
// username and replacing any password with a fixed marker.
func (e Endpoint) MaskedCredentialURL() string {
	if normalized, err := e.Normalize(); err == nil {
		e = normalized
	}
	userinfo := ""
	if e.HasUser {
		userinfo = maskProxyUsername(e.Username)
		if e.HasPassword {
			userinfo += ":******"
		}
		userinfo += "@"
	}
	return string(e.Protocol) + "://" + userinfo + net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

// MaskedPasswordURL keeps the complete proxy URL shape for previews while
// replacing only the password. The generated username remains visible so the
// user can verify template output; the credential is never returned.
func (e Endpoint) MaskedPasswordURL() string {
	if normalized, err := e.Normalize(); err == nil {
		e = normalized
	}
	userinfo := ""
	if e.HasUser {
		userinfo = url.User(e.Username).String()
		if e.HasPassword {
			userinfo += ":******"
		}
		userinfo += "@"
	}
	return string(e.Protocol) + "://" + userinfo + net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
}

func maskProxyUsername(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	switch {
	case len(runes) == 0:
		return ""
	case len(runes) <= 2:
		return string(runes[:1]) + "***"
	case len(runes) >= 8:
		return string(runes[:3]) + "***" + string(runes[len(runes)-2:])
	default:
		return string(runes[:2]) + "***" + string(runes[len(runes)-1:])
	}
}

func (e Endpoint) Fingerprint() string {
	sum := sha256.Sum256([]byte(e.CanonicalURL()))
	return hex.EncodeToString(sum[:])
}

func MaskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	candidate := raw
	if parsed, errParse := url.Parse(raw); errParse == nil && parsed.Scheme != "" && parsed.Host != "" {
		// Proxy URLs do not use paths, queries, or fragments for routing. Drop
		// them before strict validation so existing values still mask safely.
		candidate = parsed.Scheme + "://"
		if parsed.User != nil {
			candidate += parsed.User.String() + "@"
		}
		candidate += parsed.Host
	}
	endpoint, err := ParseEndpoint(candidate)
	if err != nil {
		// Invalid address-first imports can contain secrets without an '@'.
		return "[redacted]"
	}
	return endpoint.MaskedCredentialURL()
}

func (e Endpoint) Validate() error {
	_, err := e.Normalize()
	return err
}
