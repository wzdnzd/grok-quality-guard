package app

import (
	"context"
	"testing"
	"time"

	appauth "grok-quality-guard/internal/app/auth"
	proxyapp "grok-quality-guard/internal/app/proxy"
	appstate "grok-quality-guard/internal/app/state"
	proxy "grok-quality-guard/internal/proxy"
)

func proxyURLHash(value string) string { return proxyapp.URLHash(value) }

func proxyBindOptionsFromParams(params map[string]any) (proxyapp.BindOptions, error) {
	return proxyapp.BindOptionsFromParams(params)
}

func (p *Plugin) selectProxySource(opts proxyapp.BindOptions, excludeHash string) (*proxyapp.SourceCandidate, []string) {
	return p.proxySvc.SelectSource(opts, excludeHash)
}

func (p *Plugin) createProxyNode(input proxyapp.NodeInput) (proxy.Node, error) {
	return p.proxySvc.CreateNode(input)
}

func (p *Plugin) importProxyNodes(inputs []proxyapp.NodeInput, text string) (map[string]any, error) {
	return p.proxySvc.ImportNodes(inputs, text)
}
func (p *Plugin) updateProxyNode(input proxyapp.NodeInput) (proxy.Node, map[string]any, error) {
	return p.proxySvc.UpdateNode(input)
}
func (p *Plugin) createProxyTemplate(input proxyapp.TemplateInput) (proxy.Template, error) {
	return p.proxySvc.CreateTemplate(input)
}
func (p *Plugin) updateProxyTemplate(input proxyapp.TemplateInput) (proxy.Template, map[string]any, error) {
	return p.proxySvc.UpdateTemplate(input)
}
func (p *Plugin) bindProxy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.Bind(ctx, authID, opts)
}
func (p *Plugin) clearProxy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.Clear(ctx, authID, opts)
}
func (p *Plugin) clearProxyWithPolicy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.ClearWithPolicy(ctx, authID, opts)
}
func (p *Plugin) rotateProxy(ctx context.Context, authID string, opts proxyapp.BindOptions) (proxyapp.BindingResult, error) {
	return p.proxySvc.Rotate(ctx, authID, opts)
}
func (p *Plugin) updateProxyAutoBind(params map[string]any) (proxy.AutoBindConfig, bool, error) {
	return p.proxySvc.UpdateAutoBind(params)
}
func (p *Plugin) proxyApplyTemplateToAccounts(ctx context.Context, template proxy.Template, ids []string) []proxyapp.BindingResult {
	return p.proxySvc.ApplyTemplateToAccounts(ctx, template, ids)
}
func (p *Plugin) actionProxySourcePreview(params map[string]any) (any, int, error) {
	return p.proxySvc.ActionSourcePreview(params)
}
func (p *Plugin) actionProxyImportNodes(params map[string]any) (any, int, error) {
	return p.proxySvc.ActionImportNodes(params)
}
func (p *Plugin) proxyCatalog() *proxyapp.SourceCatalog { return p.proxySvc.Catalog() }
func (p *Plugin) proxyAutoBindScan(ctx context.Context) { p.proxySvc.AutoBindScan(ctx) }
func (p *Plugin) deleteProxyNode(id string, force bool) (map[string]any, error) {
	return p.proxySvc.DeleteProxyNode(id, force)
}
func (p *Plugin) proxySourceDetails(id string) (any, error)      { return p.proxySvc.SourceDetails(id) }
func (p *Plugin) reconcileProxyOperations(ctx context.Context)   { p.proxySvc.ReconcileOperations(ctx) }
func (p *Plugin) proxyExistingNode(id string) (proxy.Node, bool) { return p.proxySvc.ExistingNode(id) }

func seedResinBinding(t *testing.T, plugin *Plugin, account string, generation int64) {
	t.Helper()
	_, _, raw, err := plugin.ResolveAuth(context.Background(), "auth-1")
	if err != nil {
		t.Fatal(err)
	}
	current := appauth.RawString(raw, "proxy_url")
	const sourceID = "resin-test"
	now := time.Now().UTC()
	template := proxy.Template{
		ID:                      sourceID,
		Kind:                    proxy.TemplateResin,
		Name:                    "Resin test",
		Enabled:                 true,
		Revision:                1,
		ConfigVersion:           1,
		Protocol:                proxy.ProtocolSOCKS5H,
		Host:                    "resin",
		Port:                    2260,
		UsernamePattern:         "grok-{account}-{suffix}",
		UsernamePatternEncoding: "literal",
		HasUser:                 true,
		Placeholders: []proxy.Placeholder{
			{Name: proxy.ReservedAccountPlaceholder, Kind: proxy.PlaceholderReserved},
			{Name: "suffix", Kind: proxy.PlaceholderRandom, Random: &proxy.RandomSpec{Length: 12}},
		},
		UpdatePolicy: proxy.TemplateUpdateKeepBound,
		CreatedAt:    now,
		UpdatedAt:    now,
		ValidatedAt:  now,
	}
	if err := plugin.stateStore().Update(func(state *appstate.State) error {
		state.Proxy.Templates = append(state.Proxy.Templates, template)
		if state.Proxy.ResinSources == nil {
			state.Proxy.ResinSources = make(map[string]proxy.ResinSourceConfig)
		}
		state.Proxy.ResinSources[sourceID] = proxy.ResinSourceConfig{
			AccountFormat: "grok-{account}-{suffix}",
			LocalOnly:     true,
		}
		stateAccount := plugin.ensureStateAccount(state, "auth-1")
		stateAccount.Proxy = proxy.Binding{
			AuthID:         "auth-1",
			SourceID:       sourceID,
			SourceKind:     proxy.SourceResin,
			SourceRevision: 1,
			Generation:     uint64(generation),
			EndpointHash:   proxyURLHash(current),
			Lease:          &proxy.LeaseRef{Provider: "resin-local", Account: account},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
