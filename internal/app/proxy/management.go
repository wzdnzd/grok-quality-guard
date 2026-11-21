// Proxy and Resin management API, response projections and cleanup controls.
package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	actionpkg "grok-quality-guard/internal/app/action"
	resinpkg "grok-quality-guard/internal/app/proxy/resin"
	"grok-quality-guard/internal/app/redact"
	statepkg "grok-quality-guard/internal/app/state"
	"grok-quality-guard/internal/app/tasks"
	"grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
)

// BindingState is the single binding projection shared by the account and
// proxy-management responses. Both pages must use this type so a row cannot
// be classified as unbound in one view and read_failed in another.
type BindingState struct {
	AuthID         string           `json:"auth_id"`
	Status         string           `json:"proxy_status"`
	Proxy          string           `json:"proxy,omitempty"`
	SourceID       string           `json:"proxy_source_id,omitempty"`
	SourceKind     proxy.SourceKind `json:"proxy_source_kind,omitempty"`
	SourceRevision uint64           `json:"proxy_source_revision,omitempty"`
	Generation     uint64           `json:"proxy_generation,omitempty"`
	LeaseAccount   string           `json:"proxy_lease_account,omitempty"`
	Pending        bool             `json:"proxy_pending,omitempty"`
	Inspected      bool             `json:"proxy_inspected,omitempty"`
	Known          bool             `json:"-"`
	Version        uint64           `json:"binding_version"`
}

func (s *Service) actionProxySummary(ctx context.Context) (any, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.env.ProxyEnsureCatalog(ctx); err != nil {
		return nil, 0, err
	}
	catalog := s.proxyCatalog()
	bound := s.env.ProxyStore().ProxyCounts()
	sourceCount, sourceCounts, available, enabled := s.proxySourceSummary(catalog, bound)
	counts, accountTotal, bindingVersion, ok := s.recentBindingCounts()
	if !ok {
		counts = emptyBindingCounts()
		versionBefore := s.BindingVersion()
		var err error
		accountTotal, bindingVersion, err = s.walkProxyCatalogWithVersionContext(ctx, func(row proxyCatalogRow) { counts[row.Status]++ })
		if err != nil {
			return nil, 0, err
		}
		if bindingVersion == versionBefore {
			s.rememberBindingCounts(counts, accountTotal, bindingVersion)
		}
	}
	_, rotate := s.env.ProxyConfigSnapshot()
	return map[string]any{"source_count": sourceCount, "source_counts": sourceCounts, "available_source_count": available, "enabled_source_count": enabled, "binding_counts": counts, "binding_total": accountTotal, "binding_version": bindingVersion, "auto_bind": s.env.ProxyStore().ProxyAutoBindConfig(), "auto_bind_runtime": s.proxyAutoBindRuntime(), "auto_rotate_after_quality_failure": rotate}, 200, nil
}

func emptyBindingCounts() map[string]int {
	return map[string]int{"bound": 0, "unbound": 0, "external": 0, "drifted": 0, "unknown": 0, "checking": 0, "read_failed": 0, "reconcile_required": 0}
}

// proxySourceSummary preserves the previous source-view filtering semantics
// without retaining a full slice of projected source views. Each view is
// materialized and discarded immediately, so summary memory is O(1) in S.
func (s *Service) proxySourceSummary(catalog *proxySourceCatalog, bound map[string]int) (total int, byKind map[string]int, available, enabled int) {
	byKind = map[string]int{"node": 0, "template": 0, "resin": 0}
	if catalog == nil {
		return 0, byKind, 0, 0
	}
	for _, catalogIndex := range proxySourceCatalogOrder(catalog) {
		entry := catalog.Sources[catalogIndex]
		view, ok := s.proxySourceViewForEntry(entry, bound)
		if !ok {
			continue
		}
		total++
		byKind[string(view.Kind)]++
		if view.Enabled && view.Valid {
			enabled++
		}
		if proxyCandidateEnabled(entry) && entry.Error == nil && view.Lifecycle == "active" && (entry.Max == 0 || bound[entry.ID] < entry.Max) {
			available++
		}
	}
	return total, byKind, available, enabled
}

func (s *Service) actionProxySourcesList(ctx context.Context, params map[string]any) (any, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.env.ProxyEnsureCatalog(ctx); err != nil {
		return nil, 0, err
	}
	page := max(1, actionpkg.Int(params, "page", 1))
	size := actionpkg.Int(params, "page_size", 50)
	if size < 1 || size > 500 {
		size = 50
	}
	start := int(^uint(0) >> 1)
	if page-1 <= start/size {
		start = (page - 1) * size
	}
	catalog := s.proxyCatalog()
	counts := s.env.ProxyStore().ProxyCounts()
	order := proxySourceCatalogOrder(catalog)
	items := make([]proxySourceView, 0, size)
	total := 0
	for index, catalogIndex := range order {
		if index%256 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
		}
		entry := catalog.Sources[catalogIndex]
		if !s.proxySourceEntryMatches(entry, params) {
			continue
		}
		if total >= start && len(items) < size {
			if view, ok := s.proxySourceViewForEntry(entry, counts); ok {
				items = append(items, view)
			}
		}
		total++
	}
	return map[string]any{"items": items, "total": total, "page": page, "page_size": size}, 200, nil
}
func (s *Service) actionProxySourceGet(params map[string]any) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["source_id"]))
	if id == "" {
		return nil, 400, proxyError("source_id_required", 400, "source_id is required")
	}
	view, err := s.proxySourceDetailsForEdit(id, actionpkg.Bool(params, "edit_fields"))
	if err != nil {
		return nil, proxyErrorStatus(err), err
	}
	return view, 200, nil
}
func (s *Service) actionProxySourceEndpoint(params map[string]any) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["source_id"]))
	if id == "" {
		return nil, 400, proxyError("source_id_required", 400, "source_id is required")
	}
	full, err := s.proxySourceEndpoint(id)
	if err != nil {
		return nil, proxyErrorStatus(err), err
	}
	return map[string]any{"source_id": id, "proxy": maskProxyURL(full), "proxy_full": full}, 200, nil
}
func decodeProxyParams[T any](params map[string]any, nestedKey string) (T, error) {
	var value T
	var raw any = params
	if v, ok := params[nestedKey]; nestedKey != "" && ok && v != nil {
		raw = v
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return value, err
	}
	if len(encoded) > 2<<20 {
		return value, proxyError("proxy_config_limit", 400, "proxy configuration exceeds 2 MiB")
	}
	var fields map[string]json.RawMessage
	if errFields := json.Unmarshal(encoded, &fields); errFields == nil {
		if _, retired := fields["password_update"]; retired {
			return value, proxyError("proxy_password_update_unsupported", 400, "password_update is not supported; provide the password in url")
		}
	}
	err = json.Unmarshal(encoded, &value)
	return value, err
}
func proxyValidationFailure(err error) map[string]any {
	issue := proxy.ValidationIssue{Code: "proxy_config_invalid", Message: redact.SanitizeError(err)}
	var typed *proxy.ValidationError
	if errors.As(err, &typed) {
		issue.Code = typed.Code
		issue.Path = typed.Path
	}
	return map[string]any{"valid": false, "error": issue.Message, "error_code": issue.Code, "errors": []proxy.ValidationIssue{issue}, "validation": proxy.ValidationReport{Valid: false, Errors: []proxy.ValidationIssue{issue}}}
}
func (s *Service) actionProxySourceValidate(params map[string]any) (any, int, error) {
	switch strings.ToLower(actionpkg.String(params["kind"])) {
	case "node":
		input, err := decodeProxyParams[proxyNodeInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		var e proxy.Endpoint
		if input.URL == "" && input.Host == "" {
			if n, ok := s.proxyExistingNode(input.ID); ok {
				e = n.Endpoint
			} else {
				e, err = proxyInputEndpoint(input)
			}
		} else {
			e, err = proxyInputEndpoint(input)
		}
		if err != nil {
			return proxyValidationFailure(err), 200, nil
		}
		return map[string]any{"valid": true, "endpoint": e.MaskedURL(), "protocol": e.Protocol}, 200, nil
	case "template":
		input, err := decodeProxyParams[proxyTemplateInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		current, _ := s.proxyExistingTemplate(input.ID)
		t, err := buildProxyTemplate(input, current)
		if err != nil {
			return proxyValidationFailure(err), 200, nil
		}
		report := proxy.ValidateTemplate(t)
		return map[string]any{"valid": report.Valid, "validation": report, "source": proxyTemplateView(t, 0)}, 200, nil
	case "resin":
		input, err := decodeProxyParams[proxyResinInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		var current *proxy.Template
		var currentConfig *proxy.ResinSourceConfig
		if input.ID != "" {
			if existing, ok := s.proxyExistingTemplate(input.ID); ok && existing.Kind == proxy.TemplateResin {
				current = existing
				if config, ok := s.env.ProxyStore().ProxySourcesSnapshot().ResinSources[input.ID]; ok {
					currentConfig = &config
				}
			}
		}
		template, config, errBuild := buildResinSource(input, current, currentConfig)
		if errBuild != nil {
			return proxyValidationFailure(errBuild), 200, nil
		}
		report := proxy.ValidateTemplate(template)
		return map[string]any{"valid": report.Valid, "validation": report, "source": s.resinSourceView(template, config, 0)}, 200, nil
	default:
		return nil, 400, proxyError("kind_invalid", 400, "kind must be node, template, or resin")
	}
}
func (s *Service) actionProxySourcePreview(params map[string]any) (any, int, error) {
	if strings.EqualFold(strings.TrimSpace(actionpkg.String(params["kind"])), "resin") {
		input, err := decodeProxyParams[proxyResinInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		var current *proxy.Template
		var currentConfig *proxy.ResinSourceConfig
		if input.ID != "" {
			if existing, ok := s.proxyExistingTemplate(input.ID); ok && existing.Kind == proxy.TemplateResin {
				current = existing
				if snapshot, ok := s.env.ProxyStore().ProxySourcesSnapshot().ResinSources[input.ID]; ok {
					currentConfig = &snapshot
				}
			}
		}
		template, config, errBuild := buildResinSource(input, current, currentConfig)
		if errBuild != nil {
			return proxyValidationFailure(errBuild), 200, nil
		}
		provider, errProvider := resinpkg.NewTemplateProvider(template, resinpkg.ProviderSettings{
			Platform: config.Platform, LocalOnly: config.LocalOnly,
		})
		if errProvider != nil {
			return proxyValidationFailure(errProvider), 200, nil
		}
		count := actionpkg.Int(params, "count", 3)
		if count < 1 || count > 20 {
			count = 3
		}
		samples := make([]proxy.TemplateSample, 0, count)
		for i := 0; i < count; i++ {
			rendered, errRender := provider.Generate(context.Background(), proxy.GenerateRequest{
				SourceID: template.ID, SourceKind: proxy.SourceResin, Revision: template.Revision, AccountBase: "preview",
			})
			if errRender != nil {
				return proxyValidationFailure(errRender), 200, nil
			}
			samples = append(samples, proxy.TemplateSample{
				MaskedURL:  rendered.MaskedURL,
				DisplayURL: rendered.Endpoint.MaskedPasswordURL(),
				Values:     rendered.Values,
				VariantKey: rendered.VariantKey,
			})
		}
		return map[string]any{"valid": true, "samples": samples, "validation": proxy.ValidateTemplate(template), "source": s.resinSourceView(template, config, 0)}, 200, nil
	}
	input, err := decodeProxyParams[proxyTemplateInput](params, "source")
	if err != nil {
		return nil, 400, err
	}
	current, _ := s.proxyExistingTemplate(input.ID)
	t, err := buildProxyTemplate(input, current)
	if err != nil {
		return proxyValidationFailure(err), 200, nil
	}
	c, err := proxy.CompileTemplate(t)
	if err != nil {
		return proxyValidationFailure(err), 200, nil
	}
	assignments := map[string]string{}
	if raw, ok := params["assignments"]; ok {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, 400, err
		}
		if err = json.Unmarshal(encoded, &assignments); err != nil {
			return nil, 400, proxyError("assignment_invalid", 400, "assignments must map placeholder names to strings")
		}
	}
	count := actionpkg.Int(params, "count", 3)
	if count < 1 || count > 20 {
		count = 3
	}
	samples := make([]proxy.TemplateSample, 0, count)
	for i := 0; i < count; i++ {
		rendered, err := c.RenderWithReader(nil, assignments)
		if err != nil {
			return proxyValidationFailure(err), 200, nil
		}
		samples = append(samples, proxy.TemplateSample{
			MaskedURL:  rendered.Endpoint.MaskedURL(),
			DisplayURL: rendered.Endpoint.MaskedPasswordURL(),
			Values:     rendered.Values,
			VariantKey: rendered.VariantKey,
		})
	}
	return map[string]any{"valid": true, "samples": samples, "validation": proxy.ValidateTemplate(t), "source": proxyTemplateView(t, 0)}, 200, nil
}
func (s *Service) proxyExistingTemplate(id string) (*proxy.Template, bool) {
	if id == "" {
		return nil, false
	}
	t, ok := s.env.ProxyStore().ProxyTemplateVersion(id, 0)
	if !ok {
		return nil, false
	}
	return &t, true
}
func (s *Service) proxyExistingNode(id string) (proxy.Node, bool) {
	if id == "" {
		return proxy.Node{}, false
	}
	return s.env.ProxyStore().ProxyNodeVersion(id, 0)
}
func (s *Service) actionProxySourceCreate(params map[string]any) (any, int, error) {
	switch strings.ToLower(actionpkg.String(params["kind"])) {
	case "node":
		input, err := decodeProxyParams[proxyNodeInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		n, err := s.createProxyNode(input)
		if errors.Is(err, proxy.ErrMissingScheme) {
			return map[string]any{"ignored": true, "reason": "missing_scheme"}, 200, nil
		}
		if err != nil {
			return nil, proxyInputErrorStatus(err), err
		}
		return proxyNodeView(n, 0), 201, nil
	case "template":
		input, err := decodeProxyParams[proxyTemplateInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		t, err := s.createProxyTemplate(input)
		if err != nil {
			return nil, proxyInputErrorStatus(err), err
		}
		return proxyTemplateView(t, 0), 201, nil
	case "resin":
		input, err := decodeProxyParams[proxyResinInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		task := s.env.ProxyStartTask("proxy.source.create", nil, "task.progress.proxy_source_create_resin", func(ctx context.Context) (any, error) {
			return s.createResinSource(ctx, input)
		})
		return task, 202, nil
	default:
		return nil, 400, proxyError("kind_invalid", 400, "kind must be node, template, or resin")
	}
}
func proxyInputErrorStatus(err error) int {
	var typed *proxyManagementError
	if errors.As(err, &typed) {
		return typed.Status
	}
	var invalid *proxy.ValidationError
	if errors.As(err, &invalid) {
		return 400
	}
	return 500
}

func (s *Service) actionProxyImportNodes(params map[string]any) (any, int, error) {
	inputs := []proxyNodeInput{}
	lines := []string{}
	for _, key := range []string{"nodes", "urls"} {
		switch raw := params[key].(type) {
		case string:
			lines = append(lines, raw)
		case []any:
			for _, item := range raw {
				switch v := item.(type) {
				case string:
					lines = append(lines, v)
				case map[string]any:
					input, err := decodeProxyParams[proxyNodeInput](v, "")
					if err != nil {
						return nil, 400, err
					}
					inputs = append(inputs, input)
				}
			}
		}
	}
	if len(inputs) == 0 && len(lines) == 0 {
		return nil, 400, proxyError("nodes_or_urls_required", 400, "nodes or urls is required")
	}
	result, err := s.importProxyNodes(inputs, strings.Join(lines, "\n"))
	if err != nil {
		return result, proxyInputErrorStatus(err), err
	}
	return result, 200, nil
}
func (s *Service) actionProxySourceUpdate(params map[string]any) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["source_id"]))
	kind := strings.ToLower(actionpkg.String(params["kind"]))
	if kind == "" {
		kind = s.proxySourceKind(id)
	}
	switch kind {
	case "node":
		input, err := decodeProxyParams[proxyNodeInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		if input.ID == "" {
			input.ID = id
		}
		if input.ExpectedVersion == 0 {
			return nil, 400, proxyError("proxy_version_required", 400, "expected_version is required for updates")
		}
		input.PrioritySet = proxySourceParamPresent(params, "priority")
		input.MaxBindingsSet = proxySourceParamPresent(params, "max_bindings")
		input.UsernameSet = proxySourceParamPresent(params, "username")
		current, ok := s.proxyExistingNode(input.ID)
		if !ok {
			return nil, 404, proxyError("proxy_source_not_found", 404, "proxy node not found")
		}
		if _, _, changed, _, err := buildProxyNodeUpdate(input, current); err != nil {
			return nil, proxyInputErrorStatus(err), err
		} else if !changed {
			return nil, 400, proxySourceUnchangedError()
		}
		task := s.env.ProxyStartTask("proxy.source.update", nil, "task.progress.proxy_source_update_node", func(ctx context.Context) (any, error) {
			_, result, err := s.updateProxyNodeContext(ctx, input)
			return result, err
		})
		return task, 202, nil
	case "template":
		input, err := decodeProxyParams[proxyTemplateInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		if input.ID == "" {
			input.ID = id
		}
		if input.ExpectedVersion == 0 {
			return nil, 400, proxyError("proxy_version_required", 400, "expected_version is required for updates")
		}
		input.PrioritySet = proxySourceParamPresent(params, "priority")
		input.MaxBindingsSet = proxySourceParamPresent(params, "max_bindings")
		current, ok := s.proxyExistingTemplate(input.ID)
		if !ok {
			return nil, 404, proxyError("proxy_source_not_found", 404, "proxy template not found")
		}
		if current.Kind == proxy.TemplateResin {
			return nil, 409, proxyError("proxy_source_kind_mismatch", 409, "Resin sources must be updated with kind=resin")
		}
		next, err := buildProxyTemplate(input, current)
		if err != nil {
			return proxyValidationFailure(err), proxyInputErrorStatus(err), err
		}
		if templateEditorConfigEqual(next, *current) {
			return nil, 400, proxySourceUnchangedError()
		}
		task := s.env.ProxyStartTask("proxy.source.update", nil, "task.progress.proxy_source_update_template", func(ctx context.Context) (any, error) {
			_, result, err := s.updateProxyTemplateContext(ctx, input)
			return result, err
		})
		return task, 202, nil
	case "resin":
		input, err := decodeProxyParams[proxyResinInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		if input.ID == "" {
			input.ID = id
		}
		if input.ExpectedVersion < 1 {
			return nil, 400, proxyError("proxy_version_required", 400, "expected_version is required for updates")
		}
		task := s.env.ProxyStartTask("proxy.source.update", nil, "task.progress.proxy_source_update_resin", func(ctx context.Context) (any, error) {
			_, result, err := s.updateResinSource(ctx, input)
			return result, err
		})
		return task, 202, nil
	default:
		return nil, 404, proxyError("proxy_source_not_found", 404, "proxy source not found")
	}
}
func (s *Service) actionProxySourceDelete(params map[string]any) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["source_id"]))
	kind := s.proxySourceKind(id)
	if kind == "" {
		return nil, 404, proxyError("proxy_source_not_found", 404, "proxy source not found")
	}
	if s.env.ProxyStore().ProxyBindingCount(id) > 0 && !actionpkg.Bool(params, "force") {
		return nil, 409, proxyError("proxy_source_in_use", 409, "source has active bindings; use force=true to clear them and delete")
	}
	task := s.env.ProxyStartTask("proxy.source.delete", nil, "task.progress.proxy_source_delete", func(ctx context.Context) (any, error) {
		return s.deleteProxySource(ctx, id, actionpkg.Bool(params, "force"))
	})
	return task, 202, nil
}
func (s *Service) proxySourceKind(id string) string {
	if entry := s.proxyCatalog().ByID[id]; entry != nil {
		return string(entry.Kind)
	}
	return ""
}
func (s *Service) testProxySource(ctx context.Context, id, target string) (map[string]any, error) {
	entry := s.proxyCatalog().ByID[id]
	if entry == nil {
		return nil, proxyError("proxy_source_not_found", 404, "proxy source not found")
	}
	if entry.Error != nil {
		return nil, entry.Error
	}
	if entry.Provider == nil {
		return nil, proxyError("proxy_test_unavailable", 409, "local Resin rotation has no independent endpoint")
	}
	if entry.Kind == proxy.SourceResin && entry.ResinConfig != nil && entry.ResinConfig.LocalOnly {
		return nil, proxyError("proxy_test_unavailable", 409, "local Resin rotation has no independent endpoint")
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var rendered proxy.RenderedProxy
	var err error
	if entry.Kind == proxy.SourceResin {
		var lease *proxy.LeaseRef
		var release func()
		rendered, lease, release, err = s.generateProxySource(ctx, *entry, "preview", nil, "", false)
		if release != nil {
			defer release()
		}
		if err == nil && lease != nil {
			if job, ok := s.cleanupForLease("preview", *lease); ok {
				job.NextAttemptAt = time.Now().UTC().Add(time.Minute)
				err = s.env.ProxyStore().CommitProxy(proxyCommit{Cleanup: []ResinLeaseDelete{job}})
			}
		}
	} else {
		rendered, err = entry.Provider.Generate(ctx, proxy.GenerateRequest{AuthID: "preview", SourceID: id, SourceKind: entry.Kind, Revision: entry.Revision})
	}
	if err != nil {
		return nil, err
	}
	probe, err := proxy.ProbeEndpointTo(ctx, rendered.Endpoint, target)
	checked := time.Now().UTC()
	health := proxy.HealthHealthy
	if err != nil {
		health = proxy.HealthUnhealthy
	}
	if parentCtx.Err() == nil {
		_ = s.env.ProxyStore().RecordProxyHealth(id, entry.Revision, health, checked, err)
		s.wakeProxyAutoBind()
	}
	result := map[string]any{"source_id": id, "reachable": probe.Reachable, "latency_ms": probe.LatencyMS, "protocol": probe.Protocol, "level": probe.Level, "target": probe.Target, "endpoint": rendered.Endpoint.MaskedURL(), "tested_at": checked, "health": health, "health_checked": checked}
	if err != nil {
		result["error"] = redact.SanitizeError(err)
		result["error_code"] = proxyErrorCode(err)
	}
	return result, nil
}

type proxySourceBatchItem struct {
	SourceID  string             `json:"source_id"`
	Kind      string             `json:"kind,omitempty"`
	Status    string             `json:"status"`
	Reachable bool               `json:"reachable,omitempty"`
	LatencyMS int64              `json:"latency_ms,omitempty"`
	Health    proxy.HealthStatus `json:"health,omitempty"`
	Endpoint  string             `json:"endpoint,omitempty"`
	Error     string             `json:"error,omitempty"`
	Code      string             `json:"code,omitempty"`
}

func proxySourceIDs(params map[string]any) []string {
	ids := config.UniqueNonEmptyStrings(stringListFromAny(params["source_ids"]))
	if len(ids) == 0 {
		if id := strings.TrimSpace(actionpkg.String(params["source_id"])); id != "" {
			ids = []string{id}
		}
	}
	return ids
}

func proxySourceBatchResult(items []proxySourceBatchItem) map[string]any {
	counts := map[string]int{"total": len(items), "pending": 0, "reachable": 0, "unreachable": 0, "deleted": 0, "failed": 0}
	for _, item := range items {
		if item.Status != "" {
			counts[item.Status]++
		}
	}
	return map[string]any{"items": items, "count": len(items), "counts": counts, "reachable": counts["reachable"], "unreachable": counts["unreachable"], "deleted": counts["deleted"], "failed": counts["failed"]}
}

func (s *Service) actionProxySourcesTestSelected(_ context.Context, params map[string]any) (any, int, error) {
	ids := proxySourceIDs(params)
	if len(ids) == 0 || len(ids) > 10000 {
		return nil, 400, proxyError("source_ids_required", 400, "provide 1 to 10000 source IDs")
	}
	if s.env.ProxyHasActivePrefix("proxy.sources.test_selected") {
		return nil, 409, proxyError("proxy_source_test_busy", 409, "api.error.proxy_source_test_busy")
	}
	task := s.env.ProxyStartTask("proxy.sources.test_selected", ids, "task.progress.proxy_sources_test_selected", func(taskCtx context.Context) (any, error) {
		items := make([]proxySourceBatchItem, len(ids))
		for index, id := range ids {
			items[index] = proxySourceBatchItem{SourceID: id, Kind: s.proxySourceKind(id), Status: "pending"}
		}
		err := tasks.RunBoundedQueue(taskCtx, proxyIndices(len(ids)), min(64, len(ids)), min(4, s.proxyWorkerCount()), func(jobCtx context.Context, index int) {
			id := ids[index]
			item := proxySourceBatchItem{SourceID: id, Kind: s.proxySourceKind(id), Status: "failed"}
			result, errTest := s.testProxySource(jobCtx, id, "")
			if errTest != nil {
				item.Error = redact.SanitizeError(errTest)
				item.Code = proxyErrorCode(errTest)
			} else {
				item.Reachable, _ = result["reachable"].(bool)
				item.LatencyMS, _ = result["latency_ms"].(int64)
				item.Health, _ = result["health"].(proxy.HealthStatus)
				item.Endpoint, _ = result["endpoint"].(string)
				if item.Reachable {
					item.Status = "reachable"
				} else {
					item.Status = "unreachable"
					item.Error, _ = result["error"].(string)
					item.Code, _ = result["error_code"].(string)
				}
			}
			items[index] = item
			tasks.ReportItem(jobCtx, item)
		})
		return proxySourceBatchResult(items), err
	})
	return task, 202, nil
}

func (s *Service) actionProxySourcesDeleteSelected(_ context.Context, params map[string]any) (any, int, error) {
	ids := proxySourceIDs(params)
	if len(ids) == 0 || len(ids) > 10000 {
		return nil, 400, proxyError("source_ids_required", 400, "provide 1 to 10000 source IDs")
	}
	if s.env.ProxyHasActivePrefix("proxy.sources.delete_selected") {
		return nil, 409, proxyError("proxy_source_delete_busy", 409, "api.error.proxy_source_delete_busy")
	}
	force := actionpkg.Bool(params, "force")
	task := s.env.ProxyStartTask("proxy.sources.delete_selected", ids, "task.progress.proxy_sources_delete_selected", func(taskCtx context.Context) (any, error) {
		items := make([]proxySourceBatchItem, len(ids))
		for index, id := range ids {
			items[index] = proxySourceBatchItem{SourceID: id, Kind: s.proxySourceKind(id), Status: "pending"}
		}
		err := tasks.RunBoundedQueue(taskCtx, proxyIndices(len(ids)), min(64, len(ids)), 1, func(jobCtx context.Context, index int) {
			id := ids[index]
			item := proxySourceBatchItem{SourceID: id, Kind: s.proxySourceKind(id), Status: "failed"}
			result, errDelete := s.deleteProxySource(jobCtx, id, force)
			if errDelete != nil {
				item.Error = redact.SanitizeError(errDelete)
				item.Code = proxyErrorCode(errDelete)
			} else {
				item.Status = "deleted"
				item.Endpoint, _ = result["proxy"].(string)
			}
			items[index] = item
			tasks.ReportItem(jobCtx, item)
		})
		return proxySourceBatchResult(items), err
	})
	return task, 202, nil
}

func (s *Service) actionProxySourceTest(ctx context.Context, params map[string]any) (any, int, error) {
	result, err := s.testProxySource(ctx, strings.TrimSpace(actionpkg.String(params["source_id"])), strings.TrimSpace(actionpkg.String(params["target"])))
	if err != nil {
		return result, proxyInputErrorStatus(err), err
	}
	return result, 200, nil
}

func (s *Service) actionProxyBatch(action string, params map[string]any) (any, int, error) {
	ids := actionpkg.AuthIDs(params)
	if len(ids) == 0 || len(ids) > 10000 {
		return nil, 400, proxyError("auth_ids_limit", 400, "provide 1 to 10000 auth IDs")
	}
	opts, err := proxyBindOptionsFromParams(params)
	if err != nil {
		return nil, 400, err
	}
	opts.ManualClearPolicy = s.env.ProxyStore().ProxyAutoBindConfig().ManualClearPolicy
	task := s.env.ProxyStartTask("proxy."+action, ids, "task.progress.proxy_bindings_update", func(ctx context.Context) (any, error) {
		results := make([]proxyBindingResult, len(ids))
		for i, id := range ids {
			results[i] = proxyBindingResult{AuthID: id, Status: "pending"}
		}
		err := tasks.RunBoundedQueue(ctx, proxyIndices(len(ids)), min(256, len(ids)), s.proxyWorkerCount(), func(jobCtx context.Context, i int) {
			jobOpts := opts
			if len(ids) > 1 && opts.OperationID != "" {
				jobOpts.OperationID = opts.OperationID + "-" + proxyURLHash(ids[i])[:12]
			}
			var result proxyBindingResult
			var err error
			switch action {
			case "bind":
				result, err = s.bindProxy(jobCtx, ids[i], jobOpts)
			case "clear":
				result, err = s.clearProxy(jobCtx, ids[i], jobOpts)
			case "rotate":
				result, err = s.rotateProxy(jobCtx, ids[i], jobOpts)
			case "adopt":
				result, err = s.adoptProxy(jobCtx, ids[i], jobOpts)
			}
			if err != nil && result.Error == "" {
				result.Error = redact.SanitizeError(err)
			}
			if err != nil && result.Code == "" {
				result.Code = proxyErrorCode(err)
			}
			results[i] = result
			tasks.ReportItem(jobCtx, result)
		})
		return proxyBatchResult(results), err
	})
	return task, 202, nil
}
func proxyBatchResult(results []proxyBindingResult) map[string]any {
	counts := proxy.ChangeCounts{Total: len(results)}
	for _, r := range results {
		switch r.Status {
		case "bound", "cleared", "rotated", "adopted", "succeeded", "already_bound", "skipped", "superseded":
			counts.Succeeded++
		case "pending", "":
			counts.Pending++
		default:
			counts.Failed++
		}
	}
	items := results
	if len(items) > 1000 {
		items = append([]proxyBindingResult(nil), items[:1000]...)
	}
	return map[string]any{"items": items, "count": len(results), "counts": counts, "failed": counts.Failed, "items_truncated": len(results) > len(items)}
}
func (s *Service) actionProxyBind(_ context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyBatch("bind", params)
}
func (s *Service) actionProxyClear(_ context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyBatch("clear", params)
}
func (s *Service) actionProxyRotate(_ context.Context, params map[string]any) (any, int, error) {
	return s.actionProxyBatch("rotate", params)
}
func (s *Service) actionProxyBindUnbound(_ context.Context, params map[string]any) (any, int, error) {
	opts, err := proxyBindOptionsFromParams(params)
	if err != nil {
		return nil, 400, err
	}
	task := s.env.ProxyStartTask("proxy.bind_unbound", nil, "task.progress.proxy_bind_unbound", func(ctx context.Context) (any, error) {
		results, err := s.proxyBindUnbound(ctx, opts)
		return proxyBatchResult(results), err
	})
	return task, 202, nil
}
func proxyBindOptionsFromParams(params map[string]any) (proxyBindOptions, error) {
	scope, err := normalizeProxyScope(actionpkg.String(params["source_scope"]))
	if err != nil {
		return proxyBindOptions{}, err
	}
	opts := proxyBindOptions{SourceScope: scope, SourceIDs: config.UniqueNonEmptyStrings(stringListFromAny(params["source_ids"])), DryRun: actionpkg.Bool(params, "dry_run"), AllowExternal: actionpkg.Bool(params, "allow_external"), RequireHealthy: actionpkg.Bool(params, "require_healthy"), OperationID: strings.TrimSpace(actionpkg.String(params["operation_id"])), Replace: actionpkg.Bool(params, "replace")}
	if strings.TrimSpace(actionpkg.String(params["source_scope"])) == "" {
		opts.SourceScope = ""
	}
	if len(opts.OperationID) > 100 {
		return opts, proxyError("operation_id_invalid", 400, "operation_id must be at most 100 bytes")
	}
	if len(opts.SourceIDs) == 0 {
		if id := strings.TrimSpace(actionpkg.String(params["source_id"])); id != "" {
			opts.SourceIDs = []string{id}
		}
	}
	if raw, ok := params["exclude_auto_bind"]; ok && raw != nil {
		v, ok := raw.(bool)
		if !ok {
			return opts, proxyError("exclude_auto_bind_invalid", 400, "exclude_auto_bind must be true, false, or null")
		}
		opts.ExcludeAutoBind = &v
	}
	if opts.RequireHealthy {
		value := firstNonEmptyString(actionpkg.String(params["health_max_age"]), "10m")
		age, err := time.ParseDuration(value)
		if err != nil || age < time.Second || age > 24*time.Hour {
			return opts, proxyError("health_max_age_invalid", 400, "health_max_age must be between 1s and 24h")
		}
		opts.HealthMaxAge = age
	}
	return opts, nil
}
func stringListFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, i := range v {
			if s, ok := i.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
func (s *Service) actionProxyAutoBindGet() (any, int, error) {
	_, rotate := s.env.ProxyConfigSnapshot()
	return map[string]any{"config": s.env.ProxyStore().ProxyAutoBindConfig(), "runtime": s.proxyAutoBindRuntime(), "auto_rotate_after_quality_failure": rotate}, 200, nil
}
func (s *Service) actionProxyAutoBindUpdate(params map[string]any) (any, int, error) {
	source := params
	if nested, ok := params["config"].(map[string]any); ok {
		source = make(map[string]any, len(nested)+1)
		for key, value := range nested {
			source[key] = value
		}
		if value, ok := params["auto_rotate_after_quality_failure"]; ok {
			source["auto_rotate_after_quality_failure"] = value
		}
	}
	config, rotate, err := s.updateProxyAutoBind(source)
	if err != nil {
		return nil, proxyInputErrorStatus(err), err
	}
	return map[string]any{"config": config, "runtime": s.proxyAutoBindRuntime(), "auto_rotate_after_quality_failure": rotate}, 200, nil
}
func (s *Service) actionProxyAutoBindRun() (any, int, error) {
	s.wakeProxyAutoBind()
	return map[string]any{"woken": true, "config": s.env.ProxyStore().ProxyAutoBindConfig(), "runtime": s.proxyAutoBindRuntime()}, 202, nil
}
func (s *Service) actionProxyBindingsList(ctx context.Context, params map[string]any) (any, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.env.ProxyEnsureCatalog(ctx); err != nil {
		return nil, 0, err
	}
	page := max(1, actionpkg.Int(params, "page", 1))
	size := actionpkg.Int(params, "page_size", 50)
	if size < 1 || size > 500 {
		size = 50
	}
	start := int(^uint(0) >> 1)
	if page-1 <= start/size {
		start = (page - 1) * size
	}
	query := strings.ToLower(strings.TrimSpace(actionpkg.String(params["query"])))
	source := strings.TrimSpace(actionpkg.String(params["source_id"]))
	status := strings.ToLower(strings.TrimSpace(actionpkg.String(params["status"])))
	excludedRaw, excludedSet := params["excluded"]
	excludedSet = excludedSet && excludedRaw != nil
	excluded := actionpkg.Bool(params, "excluded")
	idsOnly := actionpkg.Bool(params, "ids_only")
	ids := []string{}
	rows := make([]proxyCatalogRow, 0, size)
	total := 0
	bindingCounts := emptyBindingCounts()
	bindingTotal, bindingVersion, errWalk := s.walkProxyCatalogWithVersionContext(ctx, func(row proxyCatalogRow) {
		bindingCounts[row.Status]++
		if !proxyStatusMatches(status, row.Status) || source != "" && row.Binding.SourceID != source || query != "" && !catalogAccountMatches(row.Account, query) || excludedSet && row.AutoBind.Excluded != excluded {
			return
		}
		if idsOnly {
			ids = append(ids, row.Account.ID)
		} else if total >= start && total-start < size {
			rows = append(rows, row)
		}
		total++
	})
	if errWalk != nil {
		return nil, 0, errWalk
	}
	if !idsOnly && len(rows) > 0 {
		pageAccounts := make([]Account, len(rows))
		for index := range rows {
			pageAccounts[index] = rows[index].Account
		}
		resolved := s.BindingStatesForAccountsContext(ctx, pageAccounts)
		if len(resolved) == len(rows) {
			if len(resolved) > 0 {
				bindingVersion = resolved[0].Version
			}
			for index := range rows {
				previous := rows[index].Status
				next := resolved[index]
				next.Version = bindingVersion
				if previous != next.Status {
					bindingCounts[previous]--
					bindingCounts[next.Status]++
				}
				rows[index].State = next
				rows[index].Status = next.Status
			}
		}
	}
	if bindingVersion == s.BindingVersion() {
		s.rememberBindingCounts(bindingCounts, bindingTotal, bindingVersion)
	}
	if idsOnly {
		return map[string]any{"items": ids, "total": total, "binding_counts": bindingCounts, "binding_version": bindingVersion}, 200, nil
	}
	items := make([]map[string]any, len(rows))
	for i, row := range rows {
		items[i] = s.proxyRowView(row)
	}
	return map[string]any{"items": items, "total": total, "page": page, "page_size": size, "binding_counts": bindingCounts, "binding_total": bindingTotal, "binding_version": bindingVersion}, 200, nil
}

func (s *Service) actionProxyBindingEndpoint(ctx context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(actionpkg.String(params["auth_id"]))
	if authID == "" {
		return nil, 400, proxyError("auth_id_required", 400, "auth_id is required")
	}
	_, _, raw, err := s.env.ProxyResolveAuth(ctx, authID)
	if err != nil {
		return nil, 502, err
	}
	proxyURL, err := proxyURLFromRaw(raw)
	if err != nil {
		return nil, proxyInputErrorStatus(err), err
	}
	if proxyURL == "" {
		return nil, 404, proxyError("endpoint_empty", 404, "proxy endpoint is empty")
	}
	return map[string]any{"auth_id": authID, "proxy": maskProxyURL(proxyURL), "proxy_full": proxyURL}, 200, nil
}

func (s *Service) actionProxyBindingsAutoBind(params map[string]any) (any, int, error) {
	ids := actionpkg.AuthIDs(params)
	if len(ids) == 0 || len(ids) > 10000 {
		return nil, 400, proxyError("auth_ids_limit", 400, "provide 1 to 10000 auth IDs")
	}
	result, err := s.setProxyAutoBindForAuths(ids, actionpkg.Bool(params, "excluded"))
	if err != nil {
		return result, proxyErrorStatus(err), err
	}
	return result, 200, nil
}
func (s *Service) actionProxyTemplateApply(_ context.Context, params map[string]any) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["source_id"]))
	if entry := s.proxyCatalog().ByID[id]; entry == nil || entry.Template == nil || entry.Template.Kind == proxy.TemplateResin {
		return nil, 404, proxyError("proxy_source_not_found", 404, "proxy template not found")
	}
	count := s.env.ProxyStore().ProxyBindingCount(id)
	if actionpkg.Bool(params, "dry_run") {
		return map[string]any{"source_id": id, "bound_count": count, "count": count, "dry_run": true}, 200, nil
	}
	changeID, err := s.createTemplateApplyChange(id)
	if err != nil {
		return nil, proxyErrorStatus(err), err
	}
	task := s.env.ProxyStartTask("proxy.template.apply", nil, "task.progress.proxy_template_apply", func(ctx context.Context) (any, error) { return s.runProxyChange(ctx, changeID) })
	return task, 202, nil
}
func (s *Service) actionProxyChangesList(params map[string]any) (any, int, error) {
	items := s.env.ProxyStore().ProxyChangeSummaries()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	page, size, start, end := proxyPage(params, len(items))
	return map[string]any{"items": items[start:end], "total": len(items), "page": page, "page_size": size}, 200, nil
}
func (s *Service) actionProxyChange(params map[string]any, action string) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["change_id"]))
	if id == "" {
		return nil, 400, proxyError("change_id_required", 400, "change_id is required")
	}
	switch action {
	case "get":
		change, ok := s.env.ProxyStore().ProxyChange(id)
		if !ok {
			return nil, 404, proxyError("proxy_change_not_found", 404, "proxy change not found")
		}
		page, size, start, end := proxyPage(params, len(change.Items))
		return map[string]any{"change_id": change.ID, "source_id": change.SourceID, "status": change.Status, "target_revision": change.TargetRevision, "counts": change.Counts, "items": change.Items[start:end], "total": len(change.Items), "page": page, "page_size": size}, 200, nil
	case "cancel":
		err := s.cancelProxyChange(id)
		if err != nil {
			return nil, proxyErrorStatus(err), err
		}
		return map[string]any{"change_id": id, "status": "canceled"}, 200, nil
	case "retry":
		task := s.env.ProxyStartTask("proxy.change.retry", nil, "task.progress.proxy_change_retry", func(ctx context.Context) (any, error) { return s.runProxyChange(ctx, id) })
		return task, 202, nil
	}
	return nil, http.StatusBadRequest, proxyError("unknown_change_action", 400, "unknown change action")
}
func proxySourceParamPresent(params map[string]any, key string) bool {
	raw := params
	if nested, ok := params["source"].(map[string]any); ok {
		raw = nested
	}
	value, ok := raw[key]
	return ok && value != nil
}

func proxyNodeView(node proxy.Node, count int) proxySourceView {
	return proxySourceView{ID: node.ID, Kind: proxy.SourceNode, Name: node.Name, Enabled: node.Enabled, Valid: true, ConfigVersion: node.ConfigVersion, Revision: node.Revision, Priority: node.Priority, MaxBindings: node.MaxBindings, BindingCount: count, Health: node.Health, HealthChecked: node.HealthChecked, Endpoint: node.Endpoint.MaskedCredentialURL(), PasswordSet: node.Endpoint.HasPassword}
}
func proxyNodesView(nodes []proxy.Node) []proxySourceView {
	views := make([]proxySourceView, len(nodes))
	for i, node := range nodes {
		views[i] = proxyNodeView(node, 0)
	}
	return views
}
func proxyTemplateView(template proxy.Template, count int) proxySourceView {
	return proxySourceView{ID: template.ID, Kind: proxy.SourceTemplate, Name: template.Name, Enabled: template.Enabled, Valid: true, ValidatedAt: template.ValidatedAt, ConfigVersion: template.ConfigVersion, Revision: template.Revision, Priority: template.Priority, MaxBindings: template.MaxBindings, BindingCount: count, Endpoint: template.URLForDisplay(), UsernamePattern: template.UsernamePattern, PasswordSet: template.HasPassword, UpdatePolicy: template.UpdatePolicy, PlaceholderCount: len(template.Placeholders), GroupCount: len(template.ValueGroups)}
}
func (s *Service) proxySourcesView() []proxySourceView {
	catalog := s.proxyCatalog()
	return s.proxySourcesViewFromCatalog(catalog, s.env.ProxyStore().ProxyCounts())
}

func proxySourceCatalogOrder(catalog *proxySourceCatalog) []int32 {
	if catalog == nil {
		return nil
	}
	if len(catalog.IDOrder) == len(catalog.Sources) {
		return catalog.IDOrder
	}
	order := make([]int32, len(catalog.Sources))
	for index := range catalog.Sources {
		order[index] = int32(index)
	}
	sort.Slice(order, func(i, j int) bool {
		return catalog.Sources[order[i]].ID < catalog.Sources[order[j]].ID
	})
	return order
}

func (s *Service) proxySourceViewForEntry(entry proxySourceCandidate, counts map[string]int) (proxySourceView, bool) {
	var view proxySourceView
	switch entry.Kind {
	case proxy.SourceNode:
		if entry.Node == nil {
			return proxySourceView{}, false
		}
		view = proxyNodeView(*entry.Node, counts[entry.ID])
	case proxy.SourceTemplate:
		if entry.Template == nil {
			return proxySourceView{}, false
		}
		view = proxyTemplateView(*entry.Template, counts[entry.ID])
	case proxy.SourceResin:
		if entry.ResinConfig == nil || entry.Template == nil {
			return proxySourceView{}, false
		}
		view = s.resinSourceView(*entry.Template, *entry.ResinConfig, counts[entry.ID])
	default:
		return proxySourceView{}, false
	}
	view.Valid = entry.Error == nil
	if entry.Error != nil {
		view.LastError = redact.SanitizeError(entry.Error)
		view.LastErrorCode = proxyErrorCode(entry.Error)
	} else if entry.Runtime.LastError != "" {
		view.LastError = entry.Runtime.LastError
		view.LastErrorCode = entry.Runtime.LastErrorCode
	}
	view.Lifecycle = firstNonEmptyString(entry.Runtime.Lifecycle, "active")
	view.ChangeID = entry.Runtime.ChangeID
	if entry.Runtime.Health != "" {
		if entry.Runtime.HealthRevision == entry.Revision {
			view.Health = entry.Runtime.Health
			view.HealthChecked = entry.Runtime.HealthChecked
		} else if entry.Node == nil {
			view.Health = proxy.HealthUnknown
			view.HealthChecked = time.Time{}
		}
	}
	return view, true
}

func (s *Service) proxySourcesViewFromCatalog(catalog *proxySourceCatalog, counts map[string]int) []proxySourceView {
	if catalog == nil {
		return nil
	}
	views := make([]proxySourceView, 0, len(catalog.Sources))
	for _, catalogIndex := range proxySourceCatalogOrder(catalog) {
		if view, ok := s.proxySourceViewForEntry(catalog.Sources[catalogIndex], counts); ok {
			views = append(views, view)
		}
	}
	return views
}
func (s *Service) proxySourceDetails(id string) (any, error) {
	return s.proxySourceDetailsForEdit(id, false)
}

func (s *Service) proxySourceEndpoint(id string) (string, error) {
	entry := s.proxyCatalog().ByID[id]
	if entry == nil {
		return "", proxyError("proxy_source_not_found", 404, "proxy source not found")
	}
	switch entry.Kind {
	case proxy.SourceNode:
		if entry.Node == nil {
			return "", proxyError("proxy_source_not_found", 404, "proxy node not found")
		}
		return entry.Node.Endpoint.CanonicalURL(), nil
	case proxy.SourceTemplate:
		if entry.Template == nil {
			return "", proxyError("proxy_source_not_found", 404, "proxy template not found")
		}
		return entry.Template.URLForReveal(), nil
	case proxy.SourceResin:
		if entry.Template == nil {
			return "", proxyError("proxy_source_not_found", 404, "Resin source not found")
		}
		return entry.Template.URLForReveal(), nil
	default:
		return "", proxyError("proxy_source_not_found", 404, "proxy source not found")
	}
}
func (s *Service) proxySourceDetailsForEdit(id string, edit bool) (any, error) {
	entry := s.proxyCatalog().ByID[id]
	if entry == nil {
		return nil, proxyError("proxy_source_not_found", 404, "proxy source not found")
	}
	if entry.Kind == proxy.SourceResin {
		if entry.Template == nil || entry.ResinConfig == nil {
			return nil, proxyError("proxy_source_details_missing", 409, "Resin source details are missing")
		}
		return s.resinSourceDetails(*entry.Template, *entry.ResinConfig), nil
	}
	count := s.env.ProxyStore().ProxyBindingCount(id)
	var view proxySourceView
	var protocol proxy.Protocol
	var host, username, encoding string
	var port int
	var placeholders []proxy.Placeholder
	var groups []proxy.ValueGroup
	var editURL string
	if entry.Node != nil {
		n := entry.Node
		view = proxyNodeView(*n, count)
		protocol = n.Endpoint.Protocol
		host = n.Endpoint.Host
		port = n.Endpoint.Port
		username = n.Endpoint.Username
		if !edit {
			username = ""
		} else {
			editURL = n.Endpoint.CanonicalURL()
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
		if edit {
			editURL = t.URLForReveal()
		}
	}
	// edit_url is only populated for the explicit edit_fields request so the
	// editor can round-trip the URL without a separate password control.
	return struct {
		proxySourceView
		Protocol                proxy.Protocol      `json:"protocol"`
		Host                    string              `json:"host"`
		Port                    int                 `json:"port"`
		Username                string              `json:"username,omitempty"`
		UsernamePatternEncoding string              `json:"username_pattern_encoding,omitempty"`
		Placeholders            []proxy.Placeholder `json:"placeholders,omitempty"`
		ValueGroups             []proxy.ValueGroup  `json:"value_groups,omitempty"`
		EditURL                 string              `json:"edit_url,omitempty"`
	}{view, protocol, host, port, username, encoding, placeholders, groups, editURL}, nil
}

// Bulk readers copy only proxy metadata; quality metrics and generated values
// do not belong in a binding table or scan.
func (s *Service) proxySourceInfo(sourceID string) (string, proxy.SourceKind) {
	if sourceID == "" {
		return "", ""
	}
	entry := s.proxyCatalog().ByID[sourceID]
	if entry == nil {
		return "", ""
	}
	return entry.Name, entry.Kind
}

// proxyAccountBindingViewWithHash is used by bulk account projections. Those
// projections carry a precomputed URL hash and masked display value so they do
// not need to retain or re-read the raw proxy URL.
func (s *Service) proxyAccountBindingViewWithHash(state AccountState, value, hash string) (string, string, proxy.SourceKind, bool) {
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
	if hash != "" && binding.EndpointHash != "" {
		if hash == binding.EndpointHash {
			return "bound", binding.SourceID, binding.SourceKind, true
		}
		return "drifted", binding.SourceID, binding.SourceKind, true
	}
	if strings.Contains(value, "***") || value == "[redacted]" {
		return "drifted", binding.SourceID, binding.SourceKind, true
	}
	if !bindingMatchesURL(binding, value) {
		return "drifted", binding.SourceID, binding.SourceKind, true
	}
	return "bound", binding.SourceID, binding.SourceKind, true
}

// PruneProxyObservations removes overlay entries that are no longer usable.
// The catalog refresh calls this with the complete live auth set so account
// deletion immediately releases memory instead of waiting for process exit.
func (s *Service) PruneProxyObservations(authIDs []string) {
	s.pruneProxyObservations(authIDs, true)
}

func (s *Service) maybePruneProxyObservations(authIDs []string) {
	s.pruneProxyObservations(authIDs, false)
}

// proxyCatalogFingerprint keeps the cached aggregate valid only for the same
// accounting population. Observation expiry/deletion also advances the
// binding revision below, so a stale total cannot survive either event.
func proxyCatalogFingerprint(authIDs []string) uint64 {
	hash := fnv.New64a()
	for _, authID := range authIDs {
		_, _ = hash.Write([]byte(authID))
		_, _ = hash.Write([]byte{0})
	}
	return hash.Sum64()
}

func (s *Service) pruneProxyObservations(authIDs []string, force bool) {
	if s == nil {
		return
	}
	now := time.Now().UTC()
	fingerprint := proxyCatalogFingerprint(authIDs)
	s.mgr.mu.Lock()
	changed := false
	if fingerprint != s.mgr.bindingCatalogFingerprint {
		s.mgr.bindingCatalogFingerprint = fingerprint
		changed = true
	}
	if !force && now.Sub(s.mgr.observationsPruned) < proxyObservationPruneInterval && len(s.mgr.observations) <= maxProxyObservations {
		if changed {
			s.mgr.bindingRevision++
		}
		s.mgr.mu.Unlock()
		return
	}
	live := make(map[string]struct{}, len(authIDs))
	for _, authID := range authIDs {
		if authID = strings.TrimSpace(authID); authID != "" {
			live[authID] = struct{}{}
		}
	}
	for authID, observation := range s.mgr.observations {
		if _, ok := live[authID]; !ok || observation.CheckedAt.IsZero() || now.Sub(observation.CheckedAt) > proxyObservationRetention {
			delete(s.mgr.observations, authID)
			changed = true
		}
	}
	if len(s.mgr.observations) > maxProxyObservations {
		type staleObservation struct {
			authID string
			at     time.Time
		}
		stale := make([]staleObservation, 0, len(s.mgr.observations))
		for authID, observation := range s.mgr.observations {
			stale = append(stale, staleObservation{authID: authID, at: observation.CheckedAt})
		}
		sort.Slice(stale, func(i, j int) bool { return stale[i].at.Before(stale[j].at) })
		for index := 0; index < len(stale)-maxProxyObservations; index++ {
			delete(s.mgr.observations, stale[index].authID)
			changed = true
		}
	}
	if changed {
		s.mgr.bindingRevision++
	}
	s.mgr.observationsPruned = now
	s.mgr.mu.Unlock()
}

func (s *Service) proxyObservationFor(account Account) proxyObservation {
	s.mgr.mu.RLock()
	observation, ok := s.mgr.observations[account.ID]
	s.mgr.mu.RUnlock()
	if ok && proxyObservationFreshAt(account, observation, time.Now()) {
		return observation
	}
	return proxyObservation{Masked: account.ProxyMasked, Hash: account.ProxyHash, Known: account.ProxyURLKnown || account.ProxyMasked != "", Inspected: account.ProxyMetaInspected, CheckedAt: account.ModTime}
}

func (s *Service) confirmProxyObservation(ctx context.Context, account Account) proxyObservation {
	if ctx == nil {
		ctx = context.Background()
	}
	observation := s.proxyObservationFor(account)
	if observation.Known || (observation.Inspected && proxyObservationFreshAt(account, observation, time.Now())) {
		return observation
	}
	_, _, raw, err := s.env.ProxyResolveAuth(ctx, account.ID)
	var proxyURL string
	if err == nil {
		proxyURL, err = proxyURLFromRaw(raw)
	}
	observation.Known = err == nil
	if err == nil {
		observation.Masked = ""
		observation.Hash = ""
		if proxyURL != "" {
			observation.Masked = maskProxyURL(proxyURL)
			observation.Hash = proxyURLHash(proxyURL)
		}
	}
	observation.Inspected = true
	observation.CheckedAt = time.Now().UTC()
	s.mgr.mu.Lock()
	s.mgr.observations[account.ID] = observation
	s.mgr.bindingRevision++
	s.mgr.mu.Unlock()
	return observation
}

type proxyCatalogRow struct {
	Account  Account
	Status   string
	Binding  proxy.Binding
	State    BindingState
	AutoBind proxy.AutoBindAccountState
}

// BindingStatesForAccounts resolves the same binding projection used by both
// management pages. Callers should pass the visible page only; the resolver
// copies one compact store projection per requested account instead of every
// lease and generated value in the catalog.
func (s *Service) BindingStatesForAccounts(accounts []Account) []BindingState {
	if len(accounts) == 0 {
		return nil
	}
	if s == nil || s.env == nil || s.env.ProxyStore() == nil {
		return make([]BindingState, len(accounts))
	}
	ids := make([]string, len(accounts))
	for i, account := range accounts {
		ids[i] = account.ID
	}
	projections := s.env.ProxyStore().ProxyBindingProjectionsForIDs(ids)
	return s.resolveBindingStates(accounts, projections)
}

const (
	visibleBindingConfirmationLimit   = 200
	visibleBindingConfirmationWorkers = 4
	visibleBindingConfirmationBudget  = 750 * time.Millisecond
)

// BindingStatesForAccountsContext resolves a visible management page and
// confirms runtime-only managed auths when their cached observation has
// expired. Runtime-only auths have no auth-file metadata job to recover them,
// so without this bounded fallback they remain "checking" indefinitely.
func (s *Service) BindingStatesForAccountsContext(ctx context.Context, accounts []Account) []BindingState {
	states := s.BindingStatesForAccounts(accounts)
	if len(states) == 0 {
		return states
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return states
	}
	candidates := make([]Account, 0, min(len(accounts), visibleBindingConfirmationLimit))
	for index, state := range states {
		if index >= len(accounts) || len(candidates) >= visibleBindingConfirmationLimit {
			break
		}
		account := accounts[index]
		if state.Status != "checking" || strings.TrimSpace(account.Path) != "" || state.SourceID == "" {
			continue
		}
		candidates = append(candidates, account)
	}
	if len(candidates) == 0 {
		return states
	}
	confirmCtx, cancel := context.WithTimeout(ctx, visibleBindingConfirmationBudget)
	defer cancel()
	sem := make(chan struct{}, visibleBindingConfirmationWorkers)
	var wait sync.WaitGroup
	for _, account := range candidates {
		if confirmCtx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wait.Add(1)
		go func(account Account) {
			defer wait.Done()
			defer func() { <-sem }()
			s.confirmProxyObservation(confirmCtx, account)
		}(account)
	}
	wait.Wait()
	return s.BindingStatesForAccounts(accounts)
}

func (s *Service) BindingVersion() uint64 {
	if s == nil {
		return 0
	}
	s.mgr.mu.RLock()
	version := s.mgr.bindingRevision
	s.mgr.mu.RUnlock()
	return version
}

const proxyBindingCountsTTL = 5 * time.Second

// rememberBindingCounts keeps the last full-catalog aggregation only long
// enough for the immediately following runtime summary request. The summary
// can then reuse the bindings list walk instead of scanning every account a
// second time. A newer binding revision invalidates the cache automatically.
func (s *Service) rememberBindingCounts(counts map[string]int, total int, version uint64) {
	if s == nil {
		return
	}
	cloned := make(map[string]int, len(counts))
	for key, value := range counts {
		cloned[key] = value
	}
	s.mgr.mu.Lock()
	s.mgr.bindingCounts = cloned
	s.mgr.bindingCountsTotal = total
	s.mgr.bindingCountsVersion = version
	s.mgr.bindingCountsAt = time.Now()
	s.mgr.mu.Unlock()
}

func (s *Service) recentBindingCounts() (map[string]int, int, uint64, bool) {
	if s == nil {
		return nil, 0, 0, false
	}
	s.mgr.mu.RLock()
	defer s.mgr.mu.RUnlock()
	if s.mgr.bindingCounts == nil || s.mgr.bindingCountsVersion != s.mgr.bindingRevision || time.Since(s.mgr.bindingCountsAt) > proxyBindingCountsTTL {
		return nil, 0, 0, false
	}
	cloned := make(map[string]int, len(s.mgr.bindingCounts))
	for key, value := range s.mgr.bindingCounts {
		cloned[key] = value
	}
	return cloned, s.mgr.bindingCountsTotal, s.mgr.bindingCountsVersion, true
}

// TouchBindingStates advances the cross-view binding version when catalog
// metadata changes without creating a new managed binding. This lets the
// dashboard discard a late response from an older catalog generation.
func (s *Service) TouchBindingStates(_ []string) {
	if s == nil {
		return
	}
	s.mgr.mu.Lock()
	s.mgr.bindingRevision++
	s.mgr.mu.Unlock()
}

func (s *Service) resolveBindingStates(accounts []Account, projections []statepkg.ProxyBindingProjection) []BindingState {
	if len(accounts) == 0 {
		return nil
	}
	states := make([]BindingState, len(accounts))
	s.mgr.mu.RLock()
	defer s.mgr.mu.RUnlock()
	now := time.Now()
	version := s.mgr.bindingRevision
	for i, account := range accounts {
		// ProxyBindingProjectionsForIDs preserves request order. Keeping this
		// path index-based avoids a second full-catalog map allocation for the
		// bindings list and summary walks.
		var projection statepkg.ProxyBindingProjection
		if i < len(projections) {
			projection = projections[i]
		}
		observation := proxyObservation{
			Masked:    account.ProxyMasked,
			Hash:      account.ProxyHash,
			Known:     account.ProxyURLKnown || account.ProxyMasked != "",
			Inspected: account.ProxyMetaInspected,
			CheckedAt: account.ModTime,
		}
		if current, ok := s.mgr.observations[account.ID]; ok && proxyObservationFreshAt(account, current, now) {
			observation = current
		}
		state := bindingStateFromProjection(account, projection, observation)
		state.Version = version
		states[i] = state
	}
	return states
}

func bindingStateFromProjection(account Account, projection statepkg.ProxyBindingProjection, observation proxyObservation) BindingState {
	state := BindingState{
		AuthID:         account.ID,
		Proxy:          observation.Masked,
		SourceID:       projection.SourceID,
		SourceKind:     projection.SourceKind,
		SourceRevision: projection.SourceRevision,
		Generation:     projection.Generation,
		LeaseAccount:   projection.LeaseAccount,
		Pending:        projection.Pending,
		Inspected:      observation.Known || observation.Inspected || account.ProxyMetaInspected,
		Known:          observation.Known,
	}
	switch {
	case projection.Pending:
		state.Status = "reconcile_required"
	case !observation.Known:
		if strings.TrimSpace(account.Path) == "" && projection.SourceID == "" && strings.TrimSpace(observation.Masked) == "" {
			state.Status = "unbound"
		} else if account.ProxyMetaInspected || observation.Inspected {
			state.Status = "read_failed"
		} else {
			state.Status = "checking"
		}
	case projection.SourceID != "":
		switch {
		case projection.EndpointHash == "":
			state.Status = "unknown"
		case strings.TrimSpace(observation.Masked) == "" || observation.Hash != projection.EndpointHash:
			state.Status = "drifted"
		default:
			state.Status = "bound"
		}
	case strings.TrimSpace(observation.Masked) != "":
		state.Status = "external"
	default:
		state.Status = "unbound"
	}
	return state
}

// Count/filter from immutable state projections; proxy observations remain
// protected by the runtime lock while rows are assembled.
func (s *Service) walkProxyCatalog(visit func(proxyCatalogRow)) int {
	total, _ := s.walkProxyCatalogWithVersion(visit)
	return total
}

const proxyBindingChunk = 1024

func (s *Service) walkProxyCatalogWithVersion(visit func(proxyCatalogRow)) (int, uint64) {
	total, version, _ := s.walkProxyCatalogWithVersionContext(context.Background(), visit)
	return total, version
}

func (s *Service) walkProxyCatalogWithVersionContext(ctx context.Context, visit func(proxyCatalogRow)) (int, uint64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	accounts := s.env.ProxyCatalogSnapshot()
	store := s.env.ProxyStore()
	version := s.BindingVersion()
	chunkIDs := make([]string, proxyBindingChunk)
	for base := 0; base < len(accounts); base += proxyBindingChunk {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		end := min(base+proxyBindingChunk, len(accounts))
		chunk := accounts[base:end]
		ids := chunkIDs[:len(chunk)]
		for index, account := range chunk {
			ids[index] = account.ID
		}
		projections := store.ProxyBindingProjectionsForIDs(ids)
		resolved := s.resolveBindingStates(chunk, projections)
		if len(resolved) > 0 {
			version = resolved[0].Version
		}
		for index, account := range chunk {
			projection := statepkg.ProxyBindingProjection{}
			if index < len(projections) {
				projection = projections[index]
			}
			state := BindingState{}
			if index < len(resolved) {
				state = resolved[index]
			}
			row := proxyCatalogRow{
				Account: account,
				Status:  state.Status,
				Binding: proxy.Binding{
					AuthID:         account.ID,
					SourceID:       projection.SourceID,
					SourceKind:     projection.SourceKind,
					SourceRevision: projection.SourceRevision,
					Generation:     projection.Generation,
					EndpointHash:   projection.EndpointHash,
				},
				State: state,
				AutoBind: proxy.AutoBindAccountState{
					Excluded:      projection.AutoBindExcluded,
					NextAttemptAt: projection.NextAttemptAt,
					LastError:     projection.LastError,
					LastErrorCode: projection.LastErrorCode,
				},
			}
			visit(row)
		}
	}
	return len(accounts), version, nil
}

func (s *Service) proxyRowView(row proxyCatalogRow) map[string]any {
	view := proxyRowView(row)
	view["proxy_source_name"], _ = s.proxySourceInfo(row.State.SourceID)
	return view
}

func proxyRowView(row proxyCatalogRow) map[string]any {
	a, state := row.Account, row.State
	displayFull := strings.TrimSpace(firstNonEmptyString(a.Email, a.Label, a.ID))
	return map[string]any{"auth_id": a.ID, "auth_id_display": maskAuthID(a.ID), "display": maskAccountIdentity(displayFull), "display_full": displayFull, "proxy": state.Proxy, "proxy_status": state.Status, "proxy_source_id": state.SourceID, "proxy_source_kind": state.SourceKind, "proxy_managed": state.SourceID != "", "proxy_generation": state.Generation, "proxy_source_revision": state.SourceRevision, "proxy_lease_account": state.LeaseAccount, "proxy_pending": state.Pending, "proxy_inspected": state.Inspected, "binding_version": state.Version, "auto_bind_excluded": row.AutoBind.Excluded, "next_attempt_at": row.AutoBind.NextAttemptAt, "last_error": row.AutoBind.LastError, "last_error_code": row.AutoBind.LastErrorCode}
}

func proxyStatusMatches(filter, status string) bool {
	return filter == "" || filter == "all" || filter == status
}

func proxyPage(params map[string]any, total int) (page, size, start, end int) {
	page = max(1, actionpkg.Int(params, "page", 1))
	size = actionpkg.Int(params, "page_size", 50)
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

func (s *Service) proxySourceEntryMatches(entry proxySourceCandidate, params map[string]any) bool {
	switch entry.Kind {
	case proxy.SourceNode, proxy.SourceTemplate, proxy.SourceResin:
	default:
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(actionpkg.String(params["kind"])))
	if kind != "" && kind != "all" && kind != string(entry.Kind) {
		return false
	}
	if entry.Kind == proxy.SourceResin && (entry.ResinConfig == nil || entry.Template == nil) {
		return false
	}
	if entry.Kind == proxy.SourceNode && entry.Node == nil || entry.Kind == proxy.SourceTemplate && entry.Template == nil {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(actionpkg.String(params["query"])))
	if query != "" {
		parts := [3]string{entry.Name, proxySourceEndpointForEntry(entry), entry.ID}
		if !proxyContainsFoldPartsASCII(parts[:], query) {
			return false
		}
	}
	switch enabled := strings.ToLower(strings.TrimSpace(actionpkg.String(params["enabled"]))); enabled {
	case "enabled", "true":
		if !proxyCandidateEnabled(entry) {
			return false
		}
	case "disabled", "false":
		if proxyCandidateEnabled(entry) {
			return false
		}
	}
	health := strings.ToLower(strings.TrimSpace(actionpkg.String(params["health"])))
	if health == "" || health == "all" {
		return true
	}
	current := string(proxySourceHealthForEntry(entry))
	if current == "" {
		current = string(proxy.HealthUnknown)
	}
	switch health {
	case "healthy", "unhealthy", "unknown":
		return current == health
	case "reachable":
		return current == string(proxy.HealthHealthy)
	case "unreachable":
		return current == string(proxy.HealthUnhealthy)
	case "untested":
		return current == string(proxy.HealthUnknown)
	default:
		return false
	}
}

func proxySourceEndpointForEntry(entry proxySourceCandidate) string {
	if entry.Node != nil {
		return entry.Node.Endpoint.MaskedCredentialURL()
	}
	if entry.Template != nil {
		return entry.Template.URLForDisplay()
	}
	return ""
}

func proxySourceHealthForEntry(entry proxySourceCandidate) proxy.HealthStatus {
	if entry.Node != nil {
		health := entry.Node.Health
		if entry.Runtime.Health != "" && entry.Runtime.HealthRevision == entry.Revision {
			return entry.Runtime.Health
		}
		return health
	}
	if entry.Runtime.Health != "" && entry.Runtime.HealthRevision == entry.Revision {
		return entry.Runtime.Health
	}
	return proxy.HealthUnknown
}

func proxyContainsFoldPartsASCII(parts []string, needle string) bool {
	if needle == "" {
		return true
	}
	for _, part := range parts {
		for index := 0; index < len(part); index++ {
			if part[index] >= 0x80 {
				return strings.Contains(strings.ToLower(strings.Join(parts, " ")), needle)
			}
		}
	}
	for index := 0; index < len(needle); index++ {
		if needle[index] >= 0x80 {
			return strings.Contains(strings.ToLower(strings.Join(parts, " ")), needle)
		}
	}
	total := len(parts) - 1
	for _, part := range parts {
		total += len(part)
	}
	if len(needle) > total {
		return false
	}
	byteAt := func(index int) byte {
		for _, part := range parts {
			if index < len(part) {
				return part[index]
			}
			index -= len(part)
			if index == 0 {
				return ' '
			}
			index--
		}
		return 0
	}
	for start := 0; start+len(needle) <= total; start++ {
		matched := true
		for offset := 0; offset < len(needle); offset++ {
			haystack := byteAt(start + offset)
			needleByte := needle[offset]
			if haystack >= 'A' && haystack <= 'Z' {
				haystack += 'a' - 'A'
			}
			if needleByte >= 'A' && needleByte <= 'Z' {
				needleByte += 'a' - 'A'
			}
			if haystack != needleByte {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func proxySourceMatches(view proxySourceView, params map[string]any) bool {
	kind := strings.ToLower(strings.TrimSpace(actionpkg.String(params["kind"])))
	query := strings.ToLower(strings.TrimSpace(actionpkg.String(params["query"])))
	if kind != "" && kind != "all" && kind != string(view.Kind) {
		return false
	}
	if query != "" && !strings.Contains(strings.ToLower(view.Name+" "+view.Endpoint+" "+view.ID), query) {
		return false
	}
	switch enabled := strings.ToLower(strings.TrimSpace(actionpkg.String(params["enabled"]))); enabled {
	case "enabled", "true":
		if !view.Enabled {
			return false
		}
	case "disabled", "false":
		if view.Enabled {
			return false
		}
	}
	health := strings.ToLower(strings.TrimSpace(actionpkg.String(params["health"])))
	if health == "" || health == "all" {
		return true
	}
	current := string(view.Health)
	if current == "" {
		current = string(proxy.HealthUnknown)
	}
	switch health {
	case "healthy", "unhealthy", "unknown":
		return current == health
	case "reachable":
		return current == string(proxy.HealthHealthy)
	case "unreachable":
		return current == string(proxy.HealthUnhealthy)
	case "untested":
		return current == string(proxy.HealthUnknown)
	default:
		return false
	}
}

func resinCleanupID(job ResinLeaseDelete) string {
	sum := sha256.Sum256([]byte(resinLeaseDeleteKey(job.AdminURL, job.PlatformID, job.Account)))
	return hex.EncodeToString(sum[:])
}

func (s *Service) actionResinCleanup(params map[string]any, action string) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["id"]))
	if id == "" {
		id = strings.TrimSpace(actionpkg.String(params["cleanup_id"]))
	}
	if action == "list" {
		page := max(1, actionpkg.Int(params, "page", 1))
		size := actionpkg.Int(params, "page_size", 50)
		if size < 1 || size > 500 {
			size = 50
		}
		start := int(^uint(0) >> 1)
		if page-1 <= start/size {
			start = (page - 1) * size
		}
		status := strings.TrimSpace(actionpkg.String(params["status"]))
		items := make([]map[string]any, 0, size)
		total := 0
		jobs := s.env.ProxyStore().ResinLeaseDeletesSnapshot()
		for i := len(jobs) - 1; i >= 0; i-- {
			job := jobs[i]
			jobStatus := firstNonEmptyString(job.Status, "pending")
			if job.Paused {
				jobStatus = "paused"
			}
			if status != "" && status != "all" && status != jobStatus {
				continue
			}
			if total >= start && total-start < size {
				items = append(items, map[string]any{"id": resinCleanupID(job), "auth_id": job.AuthID, "account": job.Account, "admin_url": job.AdminURL, "platform_id": job.PlatformID, "status": jobStatus, "paused": job.Paused, "attempts": job.Attempts, "next_attempt_at": job.NextAttemptAt, "last_error": job.LastError, "last_error_code": job.LastErrorCode, "created_at": job.CreatedAt})
			}
			total++
		}
		return map[string]any{"items": items, "total": total, "page": page, "page_size": size}, 200, nil
	}
	if action != "retry" && action != "pause" {
		return nil, 400, proxyError("unknown_cleanup_action", 400, "unknown cleanup action")
	}
	count := 0
	paused := actionpkg.Bool(params, "paused")
	err := s.env.ProxyStore().Update(func(state *State) error {
		for i := range state.ResinLeaseDeletes {
			job := &state.ResinLeaseDeletes[i]
			if id != "" && resinCleanupID(*job) != id {
				continue
			}
			if action == "pause" {
				job.Paused = paused
			} else {
				job.Status = "pending"
				job.NextAttemptAt = time.Now().UTC()
				job.LastError = ""
			}
			count++
		}
		if id != "" && count == 0 {
			return proxyError("resin_cleanup_not_found", 404, "cleanup job not found")
		}
		return nil
	})
	if err != nil {
		return nil, proxyErrorStatus(err), err
	}
	s.wakeResinLeaseDeleteLoop()
	return map[string]any{"count": count, "paused": paused}, 200, nil
}
