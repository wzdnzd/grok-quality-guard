package resin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"grok-quality-guard/internal/proxy"
)

type ProviderSettings struct {
	ProxyTemplate string
	Platform      string
	PlatformID    string
	ProxyToken    string
	SnapshotID    string
	LocalOnly     bool
}

type IdentityGenerator func(context.Context, proxy.GenerateRequest) (string, error)

type Provider struct {
	compiled *proxy.CompiledTemplate
	settings ProviderSettings
	identity IdentityGenerator
}

// TemplateProvider renders a Resin source whose username is a regular proxy
// template. The account placeholder is reserved and receives the auth email
// local-part from the caller; every other placeholder follows template rules.
type TemplateProvider struct {
	compiled       *proxy.CompiledTemplate
	settings       ProviderSettings
	platformPrefix string
	usesAccount    bool
}

func NewTemplateProvider(template proxy.Template, settings ProviderSettings) (*TemplateProvider, error) {
	if template.Kind != proxy.TemplateResin {
		return nil, fmt.Errorf("resin template kind is invalid")
	}
	if template.Protocol != proxy.ProtocolSOCKS5 && template.Protocol != proxy.ProtocolSOCKS5H {
		return nil, fmt.Errorf("resin proxy protocol is unsupported")
	}
	if settings.Platform == "" && !settings.LocalOnly {
		return nil, fmt.Errorf("resin platform is empty")
	}
	compiled, err := proxy.CompileTemplate(template)
	if err != nil {
		return nil, err
	}
	usesAccount := false
	for _, name := range template.UsernamePlaceholders() {
		if name == proxy.ReservedAccountPlaceholder {
			usesAccount = true
			break
		}
	}
	return &TemplateProvider{
		compiled: compiled,
		settings: settings,
		platformPrefix: func() string {
			if settings.Platform == "" {
				return ""
			}
			return settings.Platform + "."
		}(),
		usesAccount: usesAccount,
	}, nil
}

func (provider *TemplateProvider) Generate(ctx context.Context, request proxy.GenerateRequest) (proxy.RenderedProxy, error) {
	if err := ctx.Err(); err != nil {
		return proxy.RenderedProxy{}, err
	}
	base := strings.TrimSpace(request.AccountBase)
	if provider.usesAccount && base == "" {
		return proxy.RenderedProxy{}, fmt.Errorf("resin account base is empty")
	}
	var fixed map[string]string
	if provider.usesAccount {
		fixed = map[string]string{proxy.ReservedAccountPlaceholder: base}
	}
	for attempt := 0; attempt < 16; attempt++ {
		rendered, err := provider.compiled.RenderWithReader(nil, fixed)
		if err != nil {
			return proxy.RenderedProxy{}, err
		}
		if request.AvoidEndpointHash != "" && rendered.Endpoint.Fingerprint() == request.AvoidEndpointHash {
			continue
		}
		username := rendered.Endpoint.Username
		if !strings.HasPrefix(username, provider.platformPrefix) {
			return proxy.RenderedProxy{}, fmt.Errorf("resin username does not match the configured platform")
		}
		account := strings.TrimPrefix(username, provider.platformPrefix)
		if account == "" || len(account) > 128 || !utf8.ValidString(account) || strings.ContainsFunc(account, unicode.IsControl) || strings.ContainsRune(account, '.') {
			return proxy.RenderedProxy{}, fmt.Errorf("resin account identity is invalid")
		}
		rendered.GeneratedAccount = account
		if provider.settings.SnapshotID != "" && provider.settings.PlatformID != "" {
			rendered.Cleanup = &proxy.LeaseRef{
				Provider: "resin", PlatformID: provider.settings.PlatformID,
				Account: account, SnapshotID: provider.settings.SnapshotID,
			}
		}
		return rendered, nil
	}
	return proxy.RenderedProxy{}, fmt.Errorf("resin rotation did not generate a different endpoint")
}

var _ proxy.Provider = (*TemplateProvider)(nil)

// MigrateTemplate recognizes the old password marker only at the password
// location, then resolves platform to a literal and token to a fixed field.
// The ordinary template compiler never gives these markers special meaning.
func MigrateTemplate(rawTemplate, platform, proxyToken string) (proxy.Template, error) {
	if proxyToken == "" {
		return proxy.Template{}, fmt.Errorf("resin proxy token is not configured")
	}
	if platform == "" || !utf8.ValidString(platform) || strings.ContainsFunc(platform, unicode.IsControl) {
		return proxy.Template{}, fmt.Errorf("resin platform name is invalid")
	}
	rawTemplate = strings.TrimSpace(rawTemplate)
	scheme, rest, hasScheme := strings.Cut(rawTemplate, "://")
	if !hasScheme {
		return proxy.Template{}, proxy.ErrMissingScheme
	}
	authority := rest
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	at := strings.LastIndexByte(authority, '@')
	if at < 0 {
		return proxy.Template{}, fmt.Errorf("resin proxy template requires a username and account marker")
	}
	username, password, hasPassword := strings.Cut(authority[:at], ":")
	legacyPassword := hasPassword && password == "{proxy_token}"
	if legacyPassword {
		encodedPassword := strings.TrimPrefix(url.UserPassword("x", proxyToken).String(), "x:")
		rawTemplate = scheme + "://" + username + ":" + encodedPassword + "@" + rest[at+1:]
	}
	template, err := proxy.ParseTemplateURL(rawTemplate)
	if err != nil {
		return proxy.Template{}, err
	}
	hasAccount := false
	for _, name := range template.UsernamePlaceholders() {
		switch name {
		case "account":
			hasAccount = true
		case "platform":
		default:
			return proxy.Template{}, fmt.Errorf("resin proxy template has an unsupported username marker")
		}
	}
	if !hasAccount {
		return proxy.Template{}, fmt.Errorf("resin proxy template must contain a username account marker")
	}
	if !legacyPassword && template.HasPassword && template.Password != proxyToken {
		return proxy.Template{}, fmt.Errorf("resin proxy template password conflicts with the fixed proxy token")
	}
	template.UsernamePattern = strings.ReplaceAll(template.UsernamePattern, "{platform}", url.User(platform).String())
	template.Password = proxyToken
	template.HasPassword = true
	template.Kind = proxy.TemplateResin
	template.Placeholders = []proxy.Placeholder{{
		Name: "account", Kind: proxy.PlaceholderRandom,
		Random: &proxy.RandomSpec{Length: 128},
	}}
	if _, err := proxy.CompileTemplate(template); err != nil {
		return proxy.Template{}, err
	}
	return template, nil
}

func NewProvider(settings ProviderSettings, identity IdentityGenerator) (*Provider, error) {
	template, err := MigrateTemplate(settings.ProxyTemplate, settings.Platform, settings.ProxyToken)
	if err != nil {
		return nil, err
	}
	// The existing Resin deployment uses SOCKS5. Other protocols need an
	// explicit deployment capability rather than the generic URL allowlist.
	if template.Protocol != proxy.ProtocolSOCKS5 && template.Protocol != proxy.ProtocolSOCKS5H {
		return nil, fmt.Errorf("resin proxy protocol is unsupported")
	}
	compiled, err := proxy.CompileTemplate(template)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		identity = randomIdentity
	}
	return &Provider{compiled: compiled, settings: settings, identity: identity}, nil
}

func randomIdentity(ctx context.Context, request proxy.GenerateRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(request.AuthID))
	return "grok-" + hex.EncodeToString(sum[:4]) + "-" + hex.EncodeToString(suffix[:]), nil
}

// RenderAccount performs no API call or lease deletion. Manager supplies the
// identity after checking its used and in-flight identity index.
func (provider *Provider) RenderAccount(account string) (proxy.RenderedProxy, error) {
	if account == "" || len(account) > 128 || !utf8.ValidString(account) || strings.ContainsFunc(account, unicode.IsControl) || strings.ContainsRune(account, '.') {
		return proxy.RenderedProxy{}, fmt.Errorf("resin account identity is invalid")
	}
	rendered, err := provider.compiled.RenderResolved(map[string]string{"account": account})
	if err != nil {
		return proxy.RenderedProxy{}, err
	}
	if provider.settings.SnapshotID != "" && provider.settings.PlatformID != "" {
		rendered.Cleanup = &proxy.LeaseRef{
			Provider: "resin", PlatformID: provider.settings.PlatformID,
			Account: account, SnapshotID: provider.settings.SnapshotID,
		}
	}
	return rendered, nil
}

func (provider *Provider) Generate(ctx context.Context, request proxy.GenerateRequest) (proxy.RenderedProxy, error) {
	if err := ctx.Err(); err != nil {
		return proxy.RenderedProxy{}, err
	}
	if provider.settings.SnapshotID == "" || provider.settings.PlatformID == "" {
		return proxy.RenderedProxy{}, fmt.Errorf("resin generation requires a validated cleanup snapshot")
	}
	account, err := provider.identity(ctx, request)
	if err != nil {
		return proxy.RenderedProxy{}, err
	}
	rendered, err := provider.RenderAccount(account)
	if err != nil {
		return proxy.RenderedProxy{}, err
	}
	if request.AvoidEndpointHash != "" && rendered.Endpoint.Fingerprint() == request.AvoidEndpointHash {
		return proxy.RenderedProxy{}, fmt.Errorf("resin rotation did not generate a different endpoint")
	}
	return rendered, nil
}

var _ proxy.Provider = (*Provider)(nil)
