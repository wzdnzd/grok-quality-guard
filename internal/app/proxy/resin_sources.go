package proxy

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	resinpkg "grok-quality-guard/internal/app/proxy/resin"

	proxy "grok-quality-guard/internal/proxy"
)

type proxyResinInput struct {
	ID              string                     `json:"id,omitempty"`
	Name            string                     `json:"name,omitempty"`
	Enabled         *bool                      `json:"enabled,omitempty"`
	LocalOnly       bool                       `json:"local_only,omitempty"`
	Platform        string                     `json:"platform,omitempty"`
	AdminURL        string                     `json:"admin_url,omitempty"`
	AdminToken      string                     `json:"admin_token,omitempty"`
	ProxyToken      string                     `json:"proxy_token,omitempty"`
	AccountFormat   string                     `json:"account_format,omitempty"`
	Placeholders    []proxy.Placeholder        `json:"placeholders,omitempty"`
	ValueGroups     []proxy.ValueGroup         `json:"value_groups,omitempty"`
	Priority        int                        `json:"priority,omitempty"`
	MaxBindings     int                        `json:"max_bindings,omitempty"`
	UpdatePolicy    proxy.TemplateUpdatePolicy `json:"update_policy,omitempty"`
	ExpectedVersion uint64                     `json:"expected_version,omitempty"`
}

type resinIntegration struct {
	Platform   string
	PlatformID string
	AdminURL   string
	AdminToken string
	LocalOnly  bool
}

func resinIntegrationForSource(config proxy.ResinSourceConfig) resinIntegration {
	return resinIntegration{
		Platform:   strings.TrimSpace(config.Platform),
		PlatformID: strings.TrimSpace(config.PlatformID),
		AdminURL:   strings.TrimRight(strings.TrimSpace(config.AdminURL), "/"),
		AdminToken: strings.TrimSpace(config.AdminToken),
		LocalOnly:  config.LocalOnly,
	}
}

func resinProviderForSource(template proxy.Template, config proxy.ResinSourceConfig) (*resinpkg.TemplateProvider, error) {
	if !config.LocalOnly && (config.Platform == "" || config.PlatformID == "" || config.AdminURL == "" || config.AdminToken == "" || template.Password == "") {
		return nil, proxyError("resin_validation_required", 409, "validate and save the Resin connection before binding")
	}
	return resinpkg.NewTemplateProvider(template, resinpkg.ProviderSettings{
		Platform:  config.Platform,
		LocalOnly: config.LocalOnly,
	})
}
func normalizeResinAccountFormat(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", resinConfigErrorf(http.StatusBadRequest, "resin account_format is required")
	}
	if strings.Contains(value, "{platform}") {
		return "", resinConfigErrorf(http.StatusBadRequest, "resin account_format must not contain {platform}")
	}
	if strings.ContainsAny(value, "/?#@:") {
		return "", resinConfigErrorf(http.StatusBadRequest, "resin account_format must contain only the username pattern")
	}
	return value, nil
}

func resinPlaceholderDefinitions(pattern string, input []proxy.Placeholder) ([]proxy.Placeholder, error) {
	available := make(map[string]proxy.Placeholder, len(input))
	for _, placeholder := range input {
		if placeholder.Name == proxy.ReservedAccountPlaceholder {
			continue
		}
		if _, exists := available[placeholder.Name]; exists {
			return nil, proxyError("placeholder_duplicate", 400, "placeholder is defined more than once")
		}
		available[placeholder.Name] = placeholder
	}
	tokens, err := compileResinPatternNames(pattern)
	if err != nil {
		return nil, err
	}
	definitions := make([]proxy.Placeholder, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, name := range tokens {
		if seen[name] {
			continue
		}
		seen[name] = true
		if name == proxy.ReservedAccountPlaceholder {
			definitions = append(definitions, proxy.Placeholder{Name: name, Kind: proxy.PlaceholderReserved})
			continue
		}
		placeholder, ok := available[name]
		if !ok {
			return nil, proxyError("placeholder_undefined", 400, "username pattern contains an undefined placeholder: "+name)
		}
		definitions = append(definitions, placeholder)
	}
	return definitions, nil
}

func compileResinPatternNames(pattern string) ([]string, error) {
	// ParseTemplateURL provides the same brace grammar as normal templates.
	template, err := proxy.ParseTemplateURL("socks5h://" + pattern + "@resin:2260")
	if err != nil {
		return nil, err
	}
	return template.UsernamePlaceholders(), nil
}

func buildResinSource(input proxyResinInput, currentTemplate *proxy.Template, currentConfig *proxy.ResinSourceConfig) (proxy.Template, proxy.ResinSourceConfig, error) {
	accountFormat, err := normalizeResinAccountFormat(input.AccountFormat)
	if err != nil {
		return proxy.Template{}, proxy.ResinSourceConfig{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "Resin"
	}
	if err := validateSourceMetadata(name, input.Priority, input.MaxBindings); err != nil {
		return proxy.Template{}, proxy.ResinSourceConfig{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	} else if currentTemplate != nil {
		enabled = currentTemplate.Enabled
	}
	config := proxy.ResinSourceConfig{
		Platform:      strings.TrimSpace(input.Platform),
		AccountFormat: accountFormat,
		LocalOnly:     input.LocalOnly,
		AdminURL:      strings.TrimRight(strings.TrimSpace(input.AdminURL), "/"),
		AdminToken:    strings.TrimSpace(input.AdminToken),
	}
	if currentConfig != nil {
		if config.AdminToken == "" {
			config.AdminToken = currentConfig.AdminToken
		}
	}
	proxyToken := strings.TrimSpace(input.ProxyToken)
	if proxyToken == "" && currentTemplate != nil {
		proxyToken = currentTemplate.Password
	}
	template := proxy.Template{Kind: proxy.TemplateResin}
	if currentTemplate != nil {
		template = proxy.CloneTemplate(*currentTemplate)
		template.Kind = proxy.TemplateResin
	}
	template.Name = name
	template.Enabled = enabled
	template.Priority = input.Priority
	template.MaxBindings = input.MaxBindings
	template.UpdatePolicy = input.UpdatePolicy
	if template.UpdatePolicy == "" {
		template.UpdatePolicy = proxy.TemplateUpdateKeepBound
	}
	if config.LocalOnly {
		config.Platform = ""
		config.PlatformID = ""
		config.AdminURL = ""
		config.AdminToken = ""
		template.Protocol = proxy.ProtocolSOCKS5H
		template.Host = "resin"
		template.Port = 2260
		template.UsernamePattern = accountFormat
		template.UsernamePatternEncoding = "literal"
		template.HasUser = true
		template.Password = ""
		template.HasPassword = false
	} else {
		if config.Platform == "" {
			return proxy.Template{}, proxy.ResinSourceConfig{}, resinConfigErrorf(http.StatusBadRequest, "resin platform is required")
		}
		if config.AdminURL == "" {
			return proxy.Template{}, proxy.ResinSourceConfig{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url is required")
		}
		normalizedAdmin, errAdmin := normalizeResinAdminSettings(resinIntegration{AdminURL: config.AdminURL, Platform: config.Platform, AdminToken: config.AdminToken})
		if errAdmin != nil {
			return proxy.Template{}, proxy.ResinSourceConfig{}, errAdmin
		}
		config.AdminURL = normalizedAdmin.AdminURL
		if proxyToken == "" {
			return proxy.Template{}, proxy.ResinSourceConfig{}, resinConfigErrorf(http.StatusBadRequest, "resin proxy_token is required")
		}
		if len(proxyToken) > 255 {
			return proxy.Template{}, proxy.ResinSourceConfig{}, resinConfigErrorf(http.StatusBadRequest, "resin proxy_token must not exceed 255 bytes")
		}
		parsedAdmin, _ := url.Parse(config.AdminURL)
		host := parsedAdmin.Hostname()
		port := parsedAdmin.Port()
		if port == "" {
			if parsedAdmin.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		template.Protocol = proxy.ProtocolSOCKS5H
		template.Host = host
		template.Port, err = strconv.Atoi(port)
		if err != nil || template.Port < 1 || template.Port > 65535 {
			return proxy.Template{}, proxy.ResinSourceConfig{}, resinConfigErrorf(http.StatusBadRequest, "resin admin_url has an invalid port")
		}
		template.UsernamePattern = config.Platform + "." + accountFormat
		template.UsernamePatternEncoding = "literal"
		template.HasUser = true
		template.Password = proxyToken
		template.HasPassword = true
	}
	definitions, errDefs := resinPlaceholderDefinitions(template.UsernamePattern, input.Placeholders)
	if errDefs != nil {
		return proxy.Template{}, proxy.ResinSourceConfig{}, errDefs
	}
	template.Placeholders = definitions
	template.ValueGroups = proxy.CloneTemplate(proxy.Template{ValueGroups: input.ValueGroups}).ValueGroups
	compiled, errCompile := proxy.CompileTemplate(template)
	if errCompile != nil {
		return proxy.Template{}, proxy.ResinSourceConfig{}, errCompile
	}
	template = compiled.NormalizedTemplate()
	now := time.Now().UTC()
	if currentTemplate != nil {
		template.ID = currentTemplate.ID
		template.CreatedAt = currentTemplate.CreatedAt
		template.ConfigVersion = currentTemplate.ConfigVersion
		template.Revision = currentTemplate.Revision
	} else {
		template.ID = newProxyID("resin")
		template.ConfigVersion = 1
		template.Revision = 1
		template.CreatedAt = now
	}
	template.ValidatedAt = now
	template.UpdatedAt = now
	return template, config, nil
}

func validateResinSource(ctx context.Context, template proxy.Template, config proxy.ResinSourceConfig) (resinValidationResult, error) {
	if config.LocalOnly {
		return resinValidationResult{LocalOnly: true, ValidatedAt: time.Now().UTC()}, nil
	}
	return validateResinSourceIntegration(ctx, template, config)
}

func (s *Service) createResinSource(ctx context.Context, input proxyResinInput) (proxySourceView, error) {
	template, config, err := buildResinSource(input, nil, nil)
	if err != nil {
		return proxySourceView{}, err
	}
	if !config.LocalOnly {
		result, errValidate := validateResinSource(ctx, template, config)
		if errValidate != nil {
			return proxySourceView{}, errValidate
		}
		config.Platform, config.PlatformID = result.Platform, result.PlatformID
		template.UsernamePattern = config.Platform + "." + config.AccountFormat
		compiled, errCompile := proxy.CompileTemplate(template)
		if errCompile != nil {
			return proxySourceView{}, errCompile
		}
		template = compiled.NormalizedTemplate()
		template.ValidatedAt = result.ValidatedAt
	}
	err = s.env.ProxyStore().Update(func(state *State) error {
		if proxySourceNameTaken(state, template.Name, template.ID) {
			return proxyDuplicateNameError()
		}
		if len(state.Proxy.Nodes)+len(state.Proxy.Templates) >= maxProxySourceCount {
			return proxyError("proxy_source_limit", 400, "maximum proxy source count reached")
		}
		if state.Proxy.ResinSources == nil {
			state.Proxy.ResinSources = make(map[string]proxy.ResinSourceConfig)
		}
		state.Proxy.Templates = append(state.Proxy.Templates, template)
		state.Proxy.ResinSources[template.ID] = config
		s.env.ProxyRecordEvent(state, "", "", "proxy_resin_create", "created", "source="+template.ID)
		return nil
	})
	if err != nil {
		return proxySourceView{}, err
	}
	s.wakeProxyAutoBind()
	return s.resinSourceView(template, config, 0), nil
}

func (s *Service) updateResinSource(ctx context.Context, input proxyResinInput) (proxySourceView, map[string]any, error) {
	s.mgr.changeMu.Lock()
	current, ok := s.proxyExistingTemplate(input.ID)
	if !ok || current.Kind != proxy.TemplateResin {
		s.mgr.changeMu.Unlock()
		return proxySourceView{}, nil, proxyError("proxy_source_not_found", 404, "Resin source not found")
	}
	if input.ExpectedVersion > 0 && input.ExpectedVersion != current.ConfigVersion {
		s.mgr.changeMu.Unlock()
		return proxySourceView{}, nil, proxyError("proxy_source_version_conflict", 409, "Resin configuration changed; reload before saving")
	}
	state := s.env.ProxyStore().ProxySourcesSnapshot()
	config, ok := state.ResinSources[current.ID]
	if !ok {
		s.mgr.changeMu.Unlock()
		return proxySourceView{}, nil, proxyError("proxy_source_not_found", 404, "Resin configuration not found")
	}
	next, nextConfig, err := buildResinSource(input, current, &config)
	if err != nil {
		s.mgr.changeMu.Unlock()
		return proxySourceView{}, nil, err
	}
	s.mgr.changeMu.Unlock()
	if !nextConfig.LocalOnly {
		result, errValidate := validateResinSource(ctx, next, nextConfig)
		if errValidate != nil {
			return proxySourceView{}, nil, errValidate
		}
		nextConfig.Platform, nextConfig.PlatformID = result.Platform, result.PlatformID
		next.UsernamePattern = nextConfig.Platform + "." + nextConfig.AccountFormat
		compiled, errCompile := proxy.CompileTemplate(next)
		if errCompile != nil {
			return proxySourceView{}, nil, errCompile
		}
		next = compiled.NormalizedTemplate()
		next.ValidatedAt = result.ValidatedAt
	}
	endpointChanged := !resinSourceEndpointEqual(*current, next)
	generatedChanged := !resinSourceGeneratedEqual(*current, next) || config.Platform != nextConfig.Platform || config.AdminURL != nextConfig.AdminURL || config.LocalOnly != nextConfig.LocalOnly
	connectionChanged := config.AdminToken != nextConfig.AdminToken || config.PlatformID != nextConfig.PlatformID
	next.ConfigVersion = current.ConfigVersion
	if endpointChanged {
		next.ConfigVersion++
	}
	next.Revision = current.Revision
	if generatedChanged {
		next.Revision++
	}
	s.mgr.changeMu.Lock()
	defer s.mgr.changeMu.Unlock()
	if !endpointChanged && !generatedChanged && !connectionChanged && current.Name == next.Name && current.Enabled == next.Enabled && current.Priority == next.Priority && current.MaxBindings == next.MaxBindings {
		return proxySourceView{}, nil, proxySourceUnchangedError()
	}
	err = s.env.ProxyStore().Update(func(state *State) error {
		if proxySourceNameTaken(state, next.Name, next.ID) {
			return proxyDuplicateNameError()
		}
		for index := range state.Proxy.Templates {
			if state.Proxy.Templates[index].ID != next.ID {
				continue
			}
			if state.Proxy.Templates[index].ConfigVersion != current.ConfigVersion {
				return proxyError("proxy_source_version_conflict", 409, "Resin configuration changed before save")
			}
			if state.Proxy.ResinSources == nil {
				state.Proxy.ResinSources = make(map[string]proxy.ResinSourceConfig)
			}
			state.Proxy.Templates[index] = next
			state.Proxy.ResinSources[next.ID] = nextConfig
			s.env.ProxyRecordEvent(state, "", "", "proxy_resin_update", "saved", "source="+next.ID)
			return nil
		}
		return proxyError("proxy_source_not_found", 404, "Resin source not found")
	})
	if err != nil {
		return proxySourceView{}, nil, err
	}
	s.wakeProxyAutoBind()
	result := map[string]any{"source": s.resinSourceView(next, nextConfig, s.env.ProxyStore().ProxyBindingCount(next.ID)), "saved": true, "config_changed": endpointChanged || generatedChanged || connectionChanged}
	return s.resinSourceView(next, nextConfig, s.env.ProxyStore().ProxyBindingCount(next.ID)), result, nil
}

func resinSourceEndpointEqual(left, right proxy.Template) bool {
	return left.Protocol == right.Protocol && left.Host == right.Host && left.Port == right.Port &&
		left.Password == right.Password && left.HasPassword == right.HasPassword
}

func resinSourceGeneratedEqual(left, right proxy.Template) bool {
	return left.UsernamePattern == right.UsernamePattern && left.UsernamePatternEncoding == right.UsernamePatternEncoding &&
		proxy.TemplateDefinitionsEqual(left, right) && len(left.ValueGroups) == len(right.ValueGroups)
}

func (s *Service) resinSourceView(template proxy.Template, config proxy.ResinSourceConfig, count int) proxySourceView {
	view := proxySourceView{
		ID: template.ID, Kind: proxy.SourceResin, Name: template.Name, Enabled: template.Enabled,
		Valid: true, ValidatedAt: template.ValidatedAt, ConfigVersion: template.ConfigVersion,
		Revision: template.Revision, Priority: template.Priority, MaxBindings: template.MaxBindings,
		BindingCount: count, UsernamePattern: config.AccountFormat, PasswordSet: template.HasPassword,
		UpdatePolicy: template.UpdatePolicy, PlaceholderCount: len(template.Placeholders),
		GroupCount: len(template.ValueGroups), LocalOnly: config.LocalOnly, Configured: true,
	}
	if config.LocalOnly {
		return view
	}
	if provider, err := resinProviderForSource(template, config); err != nil {
		view.Valid = false
		view.LastError = err.Error()
	} else {
		preview, errRender := provider.Generate(context.Background(), proxy.GenerateRequest{
			SourceID: template.ID, SourceKind: proxy.SourceResin, Revision: template.Revision,
			AccountBase: "preview",
		})
		if errRender != nil {
			view.Valid = false
			view.LastError = errRender.Error()
		} else {
			view.Endpoint = preview.MaskedURL
		}
	}
	return view
}

func (s *Service) resinSourceDetails(template proxy.Template, config proxy.ResinSourceConfig) any {
	view := s.resinSourceView(template, config, s.env.ProxyStore().ProxyBindingCount(template.ID))
	return struct {
		proxySourceView
		Protocol                proxy.Protocol      `json:"protocol"`
		Host                    string              `json:"host"`
		Port                    int                 `json:"port"`
		UsernamePattern         string              `json:"username_pattern"`
		UsernamePatternEncoding string              `json:"username_pattern_encoding,omitempty"`
		Placeholders            []proxy.Placeholder `json:"placeholders,omitempty"`
		ValueGroups             []proxy.ValueGroup  `json:"value_groups,omitempty"`
		Platform                string              `json:"platform"`
		PlatformID              string              `json:"platform_id"`
		AdminURL                string              `json:"admin_url"`
		AccountFormat           string              `json:"account_format"`
		LocalOnly               bool                `json:"local_only"`
		AdminTokenSet           bool                `json:"admin_token_set"`
		ProxyTokenSet           bool                `json:"proxy_token_set"`
	}{view, template.Protocol, template.Host, template.Port, template.UsernamePattern, template.UsernamePatternEncoding,
		template.Placeholders, template.ValueGroups, config.Platform, config.PlatformID, config.AdminURL,
		config.AccountFormat, config.LocalOnly, config.AdminToken != "", template.HasPassword}
}
