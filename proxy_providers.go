package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	proxy "grok-quality-guard/internal/proxy"
	resinpkg "grok-quality-guard/internal/proxy/resin"
)

type proxyNodeProvider struct {
	node proxy.Node
}

func (provider proxyNodeProvider) Generate(ctx context.Context, _ proxy.GenerateRequest) (proxy.RenderedProxy, error) {
	if err := ctx.Err(); err != nil {
		return proxy.RenderedProxy{}, err
	}
	endpoint := provider.node.Endpoint
	return proxy.RenderedProxy{
		Endpoint:       endpoint,
		URL:            endpoint.CanonicalURL(),
		MaskedURL:      endpoint.MaskedURL(),
		UsernameLength: len(endpoint.Username),
	}, nil
}

type proxyTemplateProvider struct {
	compiled *proxy.CompiledTemplate
}

func (provider proxyTemplateProvider) Generate(ctx context.Context, request proxy.GenerateRequest) (proxy.RenderedProxy, error) {
	if err := ctx.Err(); err != nil {
		return proxy.RenderedProxy{}, err
	}
	if provider.compiled == nil {
		return proxy.RenderedProxy{}, errors.New("template generator is unavailable")
	}
	if request.AvoidEndpointHash != "" {
		return provider.compiled.RenderAvoiding(nil, request.AvoidEndpointHash)
	}
	return provider.compiled.RenderWithReader(nil, nil)
}

func proxyProviderForCandidate(candidate proxySourceCandidate) (proxy.Provider, error) {
	if candidate.Provider == nil {
		return nil, errors.New("proxy generator is unavailable")
	}
	return candidate.Provider, nil
}

func validateResinGenerationSettings(settings ResinSettings) error {
	if settings.LocalOnly {
		return nil
	}
	if settings.PlatformID == "" || settings.AdminURL == "" || settings.AdminToken == "" {
		return proxyError("resin_validation_required", 409, "validate and save the Resin connection before creating bindings")
	}
	_, err := resinpkg.NewProvider(resinpkg.ProviderSettings{ProxyTemplate: settings.ProxyTemplate, Platform: settings.Platform, PlatformID: settings.PlatformID, ProxyToken: settings.ProxyToken, SnapshotID: resinSnapshotID(settings)}, nil)
	return err
}

func (p *Plugin) generateProxySource(ctx context.Context, candidate proxySourceCandidate, authID string, raw map[string]json.RawMessage, avoidHash string, dryRun bool) (proxy.RenderedProxy, *proxy.LeaseRef, func(), error) {
	request := proxy.GenerateRequest{AuthID: authID, SourceID: candidate.ID, SourceKind: candidate.Kind, Revision: candidate.Revision, AvoidEndpointHash: avoidHash}
	if candidate.Kind != proxy.SourceResin {
		if candidate.Provider == nil {
			return proxy.RenderedProxy{}, nil, nil, errors.New("proxy generator is unavailable")
		}
		rendered, err := candidate.Provider.Generate(ctx, request)
		return rendered, nil, nil, err
	}
	settings := *candidate.ResinSettings
	base := resinAccountBase(raw)
	if base == "" {
		base = "grok"
	}
	for attempt := 0; attempt < 16; attempt++ {
		suffix, err := randomBase36(12)
		if err != nil {
			return proxy.RenderedProxy{}, nil, nil, err
		}
		identity := "grok-" + base + "-" + suffix
		store := p.stateStore()
		store.mu.RLock()
		p.proxyMgr.mu.Lock()
		_, reserved := p.proxyMgr.resinReserved[identity]
		used := store.proxyLeaseAccounts[identity] > 0
		if !used && !reserved && !dryRun {
			p.proxyMgr.resinReserved[identity] = struct{}{}
		}
		p.proxyMgr.mu.Unlock()
		store.mu.RUnlock()
		if used || reserved {
			continue
		}
		release := func() { p.proxyMgr.mu.Lock(); delete(p.proxyMgr.resinReserved, identity); p.proxyMgr.mu.Unlock() }
		if settings.LocalOnly {
			current, err := proxyURLFromRaw(raw)
			if err != nil {
				release()
				return proxy.RenderedProxy{}, nil, nil, err
			}
			value, err := rewriteResinProxyAccount(current, identity)
			if err != nil {
				release()
				return proxy.RenderedProxy{}, nil, nil, err
			}
			e, err := proxy.ParseEndpoint(value, proxy.ParseOptions{})
			if err != nil {
				release()
				return proxy.RenderedProxy{}, nil, nil, err
			}
			return proxy.RenderedProxy{Endpoint: e, URL: e.CanonicalURL(), MaskedURL: e.MaskedURL(), Values: map[string]string{"account": identity}}, &proxy.LeaseRef{Provider: "resin-local", Account: identity}, release, nil
		}
		snapshotID := resinSnapshotID(settings)
		if !dryRun {
			snapshotID, err = store.EnsureResinSnapshot(settings)
		}
		if err != nil {
			release()
			return proxy.RenderedProxy{}, nil, nil, err
		}
		provider, ok := candidate.Provider.(*resinpkg.Provider)
		if !ok {
			provider, err = resinpkg.NewProvider(resinpkg.ProviderSettings{ProxyTemplate: settings.ProxyTemplate, Platform: settings.Platform, PlatformID: settings.PlatformID, ProxyToken: settings.ProxyToken, SnapshotID: snapshotID}, nil)
			if err != nil {
				release()
				return proxy.RenderedProxy{}, nil, nil, err
			}
		}
		rendered, err := provider.RenderAccount(identity)
		if err != nil {
			release()
			return proxy.RenderedProxy{}, nil, nil, err
		}
		if rendered.Endpoint.Fingerprint() == avoidHash {
			release()
			continue
		}
		return rendered, rendered.Cleanup, release, nil
	}
	return proxy.RenderedProxy{}, nil, nil, errors.New("unable to generate a different proxy identity")
}

func (s *StateStore) EnsureResinSnapshot(settings ResinSettings) (string, error) {
	id := resinSnapshotID(settings)
	if _, ok := s.ResinSnapshot(id); ok {
		return id, nil
	}
	if err := s.CommitProxy(proxyCommit{Snapshots: map[string]ResinSettings{id: settings}}); err != nil {
		return "", err
	}
	return id, nil
}

func (p *Plugin) captureLegacyLease(binding proxy.Binding, current string) (*proxy.LeaseRef, error) {
	if binding.Lease == nil {
		return nil, nil
	}
	lease := *binding.Lease
	settings := p.ResinConfig()
	// A shared hostname is insufficient evidence for deleting a historical lease.
	if settings.LocalOnly {
		lease.Provider = "resin-local"
		return &lease, nil
	}
	if lease.Account == "" {
		return &lease, nil
	}
	expected, err := renderResinProxy(settings, lease.Account)
	if err != nil || proxyURLHash(expected) != proxyURLHash(current) {
		return &lease, nil
	}
	if !resinCleanupConfigured(settings) {
		return &lease, nil
	}
	id, err := p.stateStore().EnsureResinSnapshot(settings)
	if err != nil {
		return nil, err
	}
	lease.Provider = "resin"
	lease.PlatformID = settings.PlatformID
	lease.SnapshotID = id
	return &lease, nil
}

func resinCleanupConfigured(settings ResinSettings) bool {
	return !settings.LocalOnly && strings.TrimSpace(settings.AdminURL) != "" && strings.TrimSpace(settings.PlatformID) != "" && strings.TrimSpace(settings.AdminToken) != ""
}

func (p *Plugin) cleanupForLease(authID string, lease proxy.LeaseRef) (ResinLeaseDelete, bool) {
	if lease.Provider != "resin" || lease.Account == "" || lease.SnapshotID == "" {
		return ResinLeaseDelete{}, false
	}
	if p.stateStore().ProxyLeaseUsage(lease.Account) > 1 {
		return ResinLeaseDelete{}, false
	}
	settings, ok := p.stateStore().ResinSnapshot(lease.SnapshotID)
	if !ok || !resinCleanupConfigured(settings) {
		return ResinLeaseDelete{}, false
	}
	return newResinLeaseDelete(authID, settings, lease.Account), true
}

func (s *StateStore) ProxyLeaseUsage(account string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxyLeaseAccounts[account]
}
