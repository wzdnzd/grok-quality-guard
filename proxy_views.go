package main

import (
	"context"
	"strings"
	"time"

	proxy "grok-quality-guard/internal/proxy"
)

func proxyNodeView(node proxy.Node, count int) proxySourceView {
	return proxySourceView{ID: node.ID, Kind: proxy.SourceNode, Name: node.Name, Enabled: node.Enabled, Valid: true, ConfigVersion: node.ConfigVersion, Revision: node.Revision, Priority: node.Priority, MaxBindings: node.MaxBindings, BindingCount: count, Health: node.Health, HealthChecked: node.HealthChecked, Endpoint: node.Endpoint.MaskedURL(), PasswordSet: node.Endpoint.HasPassword}
}
func proxyNodesView(nodes []proxy.Node) []proxySourceView {
	views := make([]proxySourceView, len(nodes))
	for i, node := range nodes {
		views[i] = proxyNodeView(node, 0)
	}
	return views
}
func proxyTemplateView(template proxy.Template, count int) proxySourceView {
	e := proxy.Endpoint{Protocol: template.Protocol, Host: template.Host, Port: template.Port}
	return proxySourceView{ID: template.ID, Kind: proxy.SourceTemplate, Name: template.Name, Enabled: template.Enabled, Valid: true, ValidatedAt: template.ValidatedAt, ConfigVersion: template.ConfigVersion, Revision: template.Revision, Priority: template.Priority, MaxBindings: template.MaxBindings, BindingCount: count, Endpoint: e.MaskedURL(), UsernamePattern: template.UsernamePattern, PasswordSet: template.HasPassword, UpdatePolicy: template.UpdatePolicy, PlaceholderCount: len(template.Placeholders), GroupCount: len(template.ValueGroups)}
}
func (p *Plugin) proxySourcesView() []proxySourceView {
	catalog := p.proxyCatalog()
	counts := p.stateStore().ProxyCounts()
	views := make([]proxySourceView, 0, len(catalog.Sources))
	for _, entry := range catalog.Sources {
		var view proxySourceView
		switch entry.Kind {
		case proxy.SourceNode:
			view = proxyNodeView(*entry.Node, counts[entry.ID])
		case proxy.SourceTemplate:
			view = proxyTemplateView(*entry.Template, counts[entry.ID])
		case proxy.SourceResin:
			s := *entry.ResinSettings
			view = proxySourceView{ID: entry.ID, Kind: entry.Kind, Name: entry.Name, Enabled: s.Enabled, Valid: entry.Error == nil, ConfigVersion: entry.ConfigVersion, Revision: entry.Revision, BindingCount: counts[entry.ID], LocalOnly: s.LocalOnly, Configured: s.Configured, PasswordSet: s.ProxyToken != ""}
			if rendered, err := renderResinProxy(s, "preview"); err == nil {
				view.Endpoint = maskProxyURL(rendered)
			}
		}
		view.Valid = entry.Error == nil
		if entry.Error != nil {
			view.LastError = sanitizeError(entry.Error)
		}
		view.Lifecycle = firstNonEmptyString(entry.Runtime.Lifecycle, "active")
		view.ChangeID = entry.Runtime.ChangeID
		if entry.Runtime.Health != "" {
			view.Health = entry.Runtime.Health
			view.HealthChecked = entry.Runtime.HealthChecked
		}
		views = append(views, view)
	}
	return views
}

func (p *Plugin) proxySourceDetails(id string) (any, error) {
	return p.proxySourceDetailsForEdit(id, false)
}
func (p *Plugin) proxySourceDetailsForEdit(id string, edit bool) (any, error) {
	entry := p.proxyCatalog().ByID[id]
	if entry == nil {
		return nil, proxyError("proxy_source_not_found", 404, "proxy source not found")
	}
	if entry.Kind == proxy.SourceResin {
		return p.resinConfigView(*entry.ResinSettings), nil
	}
	count := p.stateStore().ProxyBindingCount(id)
	var view proxySourceView
	var protocol proxy.Protocol
	var host, username, encoding string
	var port int
	var placeholders []proxy.Placeholder
	var groups []proxy.ValueGroup
	if entry.Node != nil {
		n := entry.Node
		view = proxyNodeView(*n, count)
		protocol = n.Endpoint.Protocol
		host = n.Endpoint.Host
		port = n.Endpoint.Port
		username = n.Endpoint.Username
		if !edit {
			username = ""
		}
	} else {
		t := entry.Template
		view = proxyTemplateView(*t, count)
		protocol = t.Protocol
		host = t.Host
		port = t.Port
		username = t.UsernamePattern
		encoding = t.UsernamePatternEncoding
		placeholders = t.Placeholders
		groups = t.ValueGroups
	}
	return struct {
		proxySourceView
		Protocol                proxy.Protocol      `json:"protocol"`
		Host                    string              `json:"host"`
		Port                    int                 `json:"port"`
		Username                string              `json:"username,omitempty"`
		UsernamePatternEncoding string              `json:"username_pattern_encoding,omitempty"`
		Placeholders            []proxy.Placeholder `json:"placeholders,omitempty"`
		ValueGroups             []proxy.ValueGroup  `json:"value_groups,omitempty"`
	}{view, protocol, host, port, username, encoding, placeholders, groups}, nil
}

func (s *StateStore) ProxyBindingCount(id string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxyCounts[id]
}

// Bulk readers copy only proxy metadata; quality metrics and generated values
// do not belong in a binding table or scan.
type proxyAccountSummary struct {
	AuthID      string
	State       string
	DisableKind string
	Proxy       proxy.Binding
	Resin       ResinState
	AutoBind    proxy.AutoBindAccountState
}

func (s proxyAccountSummary) managed() bool {
	return s.Proxy.SourceID != "" || s.Resin.Managed || s.Resin.Account != ""
}
func (s *StateStore) ProxyAccountsForIDs(ids []string) []proxyAccountSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	values := make([]proxyAccountSummary, len(ids))
	for i, id := range ids {
		if a := s.state.Accounts[id]; a != nil {
			b := a.Proxy
			b.ResolvedValues = nil
			if b.Lease != nil {
				lease := *b.Lease
				b.Lease = &lease
			}
			values[i] = proxyAccountSummary{AuthID: id, State: a.State, DisableKind: a.DisableKind, Proxy: b, Resin: a.Resin, AutoBind: a.AutoBind}
		} else {
			values[i].AuthID = id
		}
	}
	return values
}

func (p *Plugin) proxyAccountBindingView(state AccountState, value string) (string, string, proxy.SourceKind, bool) {
	binding, managed := bindingForAccount(state)
	if value == "" {
		if managed {
			return "drifted", binding.SourceID, binding.SourceKind, true
		}
		return "unbound", "", "", false
	}
	if !managed {
		return "external", "", "", false
	}
	if !bindingMatchesURL(binding, value) {
		return "drifted", binding.SourceID, binding.SourceKind, true
	}
	return "managed", binding.SourceID, binding.SourceKind, true
}

func (p *Plugin) proxyObservationFor(account Account) proxyObservation {
	p.proxyMgr.mu.Lock()
	observation, ok := p.proxyMgr.observations[account.ID]
	p.proxyMgr.mu.Unlock()
	if ok && time.Since(observation.CheckedAt) < catalogRefreshTTL && (account.ModTime.IsZero() || !account.ModTime.After(observation.CheckedAt)) {
		return observation
	}
	return proxyObservation{URL: account.ProxyURL, Hash: account.ProxyHash, Known: account.ProxyURLKnown || account.ProxyURL != "", CheckedAt: account.ModTime}
}

func (p *Plugin) confirmProxyObservation(ctx context.Context, account Account) proxyObservation {
	observation := p.proxyObservationFor(account)
	if observation.Known {
		return observation
	}
	_, _, raw, err := p.resolveAuth(ctx, account.ID)
	if err == nil {
		observation.URL, err = proxyURLFromRaw(raw)
	}
	observation.Known = err == nil
	if err == nil && observation.URL != "" {
		observation.Hash = proxyURLHash(observation.URL)
	}
	observation.CheckedAt = time.Now().UTC()
	p.proxyMgr.mu.Lock()
	p.proxyMgr.observations[account.ID] = observation
	p.proxyMgr.mu.Unlock()
	return observation
}

type proxyCatalogRow struct {
	Account     Account
	Status      string
	Binding     proxy.Binding
	AutoBind    proxy.AutoBindAccountState
	Observation proxyObservation
}

// Count/filter under one pair of read locks. Only selected page rows are copied;
// no per-account maps, quality state snapshots, or lock acquisitions are needed.
func (p *Plugin) walkProxyCatalog(visit func(proxyCatalogRow)) int {
	accounts := p.catalogSnapshot()
	store := p.stateStore()
	store.mu.RLock()
	defer store.mu.RUnlock()
	p.proxyMgr.mu.Lock()
	defer p.proxyMgr.mu.Unlock()
	now := time.Now()
	for _, a := range accounts {
		row := proxyCatalogRow{Account: a, Status: "unbound", Observation: proxyObservation{URL: a.ProxyURL, Hash: a.ProxyHash, Known: a.ProxyURLKnown || a.ProxyURL != "", CheckedAt: a.ModTime}}
		if observation, ok := p.proxyMgr.observations[a.ID]; ok && now.Sub(observation.CheckedAt) < catalogRefreshTTL && (a.ModTime.IsZero() || !a.ModTime.After(observation.CheckedAt)) {
			row.Observation = observation
		}
		if state := store.state.Accounts[a.ID]; state != nil {
			row.Binding = state.Proxy
			row.AutoBind = state.AutoBind
			if row.Binding.SourceID == "" && (state.Resin.Managed || state.Resin.Account != "") {
				row.Binding.SourceID = resinSourceID
				row.Binding.SourceKind = proxy.SourceResin
				row.Binding.Generation = uint64(maxInt64(state.Resin.Generation, 0))
			}
		}
		if !row.Observation.Known {
			row.Status = "unknown"
		} else if row.Binding.SourceID != "" {
			if row.Binding.EndpointHash == "" {
				row.Status = "unknown"
			} else if row.Observation.URL == "" {
				row.Status = "drifted"
			} else {
				hash := row.Observation.Hash
				if hash == "" {
					hash = proxyURLHash(row.Observation.URL)
				}
				if hash == row.Binding.EndpointHash {
					row.Status = "managed"
				} else {
					row.Status = "drifted"
				}
			}
		} else if row.Observation.URL != "" {
			row.Status = "external"
		}
		if store.proxyPending[a.ID] != "" {
			row.Status = "needs_reconcile"
		}
		visit(row)
	}
	return len(accounts)
}

func proxyRowView(row proxyCatalogRow) map[string]any {
	a, b := row.Account, row.Binding
	return map[string]any{"auth_id": a.ID, "auth_id_display": maskAuthID(a.ID), "display": maskAccountIdentity(firstNonEmptyString(a.Email, a.Label, a.ID)), "proxy": maskProxyURL(row.Observation.URL), "proxy_status": row.Status, "source_id": b.SourceID, "source_kind": b.SourceKind, "generation": b.Generation, "source_revision": b.SourceRevision, "auto_bind_excluded": row.AutoBind.Excluded, "next_attempt_at": row.AutoBind.NextAttemptAt, "last_error": row.AutoBind.LastError}
}

func proxyStatusMatches(filter, status string) bool {
	if filter == "bound" {
		filter = "managed"
	}
	if filter == "conflict" {
		filter = "external"
	}
	if filter == "reconcile_required" {
		filter = "needs_reconcile"
	}
	return filter == "" || filter == "all" || filter == status
}

func proxyPage(params map[string]any, total int) (page, size, start, end int) {
	page = max(1, intFromParams(params, "page", 1))
	size = intFromParams(params, "page_size", 50)
	if size < 1 || size > 500 {
		size = 50
	}
	if total == 0 || page > (total-1)/size+1 {
		start = total
	} else {
		start = (page - 1) * size
	}
	end = min(total, start+size)
	return
}

func proxySourceMatches(view proxySourceView, params map[string]any) bool {
	kind := strings.ToLower(strings.TrimSpace(asString(params["kind"])))
	query := strings.ToLower(strings.TrimSpace(asString(params["query"])))
	return (kind == "" || kind == "all" || kind == string(view.Kind)) && (query == "" || strings.Contains(strings.ToLower(view.Name+" "+view.Endpoint+" "+view.ID), query))
}
