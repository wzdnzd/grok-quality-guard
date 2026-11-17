package resin

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"grok-quality-guard/internal/proxy"
)

type ProviderSettings struct {
	Platform  string
	LocalOnly bool
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
		return rendered, nil
	}
	return proxy.RenderedProxy{}, fmt.Errorf("resin rotation did not generate a different endpoint")
}

var _ proxy.Provider = (*TemplateProvider)(nil)
