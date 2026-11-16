package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	proxy "grok-quality-guard/internal/proxy"
)

func (p *Plugin) actionProxySummary(ctx context.Context) (any, int, error) {
	if err := p.ensureCatalog(ctx); err != nil {
		return nil, 500, err
	}
	sources := p.proxySourcesView()
	counts := map[string]int{"managed": 0, "unbound": 0, "external": 0, "drifted": 0, "unknown": 0, "needs_reconcile": 0}
	accountTotal := p.walkProxyCatalog(func(row proxyCatalogRow) { counts[row.Status]++ })
	sourceCounts := map[string]int{"node": 0, "template": 0, "resin": 0}
	available := 0
	catalog := p.proxyCatalog()
	bound := p.stateStore().ProxyCounts()
	for _, s := range sources {
		sourceCounts[string(s.Kind)]++
		entry := catalog.ByID[s.ID]
		if entry != nil && proxyCandidateEnabled(*entry) && entry.Error == nil && s.Lifecycle == "active" && (entry.Max == 0 || bound[s.ID] < entry.Max) {
			available++
		}
	}
	p.mu.RLock()
	rotate := p.cfg.Proxy.AutoRotateAfterQualityFailure
	p.mu.RUnlock()
	return map[string]any{"source_count": len(sources), "source_counts": sourceCounts, "available_source_count": available, "enabled_source_count": countEnabledProxySources(sources), "binding_counts": counts, "binding_total": accountTotal, "managed_binding_count": counts["managed"], "auto_bind": p.stateStore().ProxyAutoBindConfig(), "auto_bind_runtime": p.proxyAutoBindRuntime(), "auto_rotate_after_quality_failure": rotate}, 200, nil
}
func countEnabledProxySources(sources []proxySourceView) int {
	count := 0
	for _, s := range sources {
		if s.Enabled && s.Valid {
			count++
		}
	}
	return count
}
func (p *Plugin) actionProxySourcesList(params map[string]any) (any, int, error) {
	sources := p.proxySourcesView()
	filtered := make([]proxySourceView, 0, len(sources))
	for _, source := range sources {
		if proxySourceMatches(source, params) {
			filtered = append(filtered, source)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	page, size, start, end := proxyPage(params, len(filtered))
	return map[string]any{"items": filtered[start:end], "total": len(filtered), "page": page, "page_size": size}, 200, nil
}
func (p *Plugin) actionProxySourceGet(params map[string]any) (any, int, error) {
	id := strings.TrimSpace(asString(params["source_id"]))
	if id == "" {
		return nil, 400, errors.New("source_id is required")
	}
	view, err := p.proxySourceDetailsForEdit(id, boolFromParams(params, "edit_fields"))
	if err != nil {
		return nil, proxyErrorStatus(err), err
	}
	return view, 200, nil
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
	err = json.Unmarshal(encoded, &value)
	return value, err
}
func proxyValidationFailure(err error) map[string]any {
	issue := proxy.ValidationIssue{Code: "proxy_config_invalid", Message: sanitizeError(err)}
	var typed *proxy.ValidationError
	if errors.As(err, &typed) {
		issue.Code = typed.Code
		issue.Path = typed.Path
	}
	return map[string]any{"valid": false, "error": issue.Message, "errors": []proxy.ValidationIssue{issue}, "validation": proxy.ValidationReport{Valid: false, Errors: []proxy.ValidationIssue{issue}}}
}
func (p *Plugin) actionProxySourceValidate(params map[string]any) (any, int, error) {
	switch strings.ToLower(asString(params["kind"])) {
	case "node":
		input, err := decodeProxyParams[proxyNodeInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		var e proxy.Endpoint
		if input.URL == "" && input.Host == "" {
			if n, ok := p.proxyExistingNode(input.ID); ok {
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
		current, _ := p.proxyExistingTemplate(input.ID)
		t, err := buildProxyTemplate(input, current)
		if err != nil {
			return proxyValidationFailure(err), 200, nil
		}
		report := proxy.ValidateTemplate(t)
		return map[string]any{"valid": report.Valid, "validation": report, "source": proxyTemplateView(t, 0)}, 200, nil
	case "resin":
		return p.actionResinConfigValidate(context.Background(), params)
	default:
		return nil, 400, errors.New("kind must be node, template, or resin")
	}
}
func (p *Plugin) actionProxySourcePreview(params map[string]any) (any, int, error) {
	input, err := decodeProxyParams[proxyTemplateInput](params, "source")
	if err != nil {
		return nil, 400, err
	}
	current, _ := p.proxyExistingTemplate(input.ID)
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
			return nil, 400, errors.New("assignments must map placeholder names to strings")
		}
	}
	count := intFromParams(params, "count", 3)
	if count < 1 || count > 20 {
		count = 3
	}
	samples := make([]proxy.TemplateSample, 0, count)
	for i := 0; i < count; i++ {
		rendered, err := c.RenderWithReader(nil, assignments)
		if err != nil {
			return proxyValidationFailure(err), 200, nil
		}
		samples = append(samples, proxy.TemplateSample{MaskedURL: rendered.Endpoint.MaskedURL(), Values: rendered.Values, VariantKey: rendered.VariantKey})
	}
	return map[string]any{"valid": true, "samples": samples, "validation": proxy.ValidateTemplate(t), "source": proxyTemplateView(t, 0)}, 200, nil
}
func (p *Plugin) proxyExistingTemplate(id string) (*proxy.Template, bool) {
	if id == "" {
		return nil, false
	}
	t, ok := p.stateStore().ProxyTemplateVersion(id, 0)
	if !ok {
		return nil, false
	}
	return &t, true
}
func (p *Plugin) proxyExistingNode(id string) (proxy.Node, bool) {
	if id == "" {
		return proxy.Node{}, false
	}
	return p.stateStore().ProxyNodeVersion(id, 0)
}
func (p *Plugin) actionProxySourceCreate(params map[string]any) (any, int, error) {
	switch strings.ToLower(asString(params["kind"])) {
	case "node":
		input, err := decodeProxyParams[proxyNodeInput](params, "source")
		if err != nil {
			return nil, 400, err
		}
		n, err := p.createProxyNode(input)
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
		t, err := p.createProxyTemplate(input)
		if err != nil {
			return nil, proxyInputErrorStatus(err), err
		}
		return proxyTemplateView(t, 0), 201, nil
	default:
		return nil, 400, errors.New("kind must be node or template")
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
func (p *Plugin) actionProxyImportNodes(params map[string]any) (any, int, error) {
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
		return nil, 400, errors.New("nodes or urls is required")
	}
	format := strings.TrimSpace(asString(params["import_format"]))
	if format != "" && format != "standard" && format != "address_first" {
		return nil, 400, errors.New("import_format must be standard or address_first")
	}
	if format != "" {
		for i := range inputs {
			inputs[i].ImportFormat = format
		}
		for _, line := range strings.Split(strings.Join(lines, "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				inputs = append(inputs, proxyNodeInput{URL: strings.TrimSpace(line), ImportFormat: format})
			}
		}
		lines = nil
	}
	result, err := p.importProxyNodes(inputs, strings.Join(lines, "\n"))
	if err != nil {
		return result, proxyInputErrorStatus(err), err
	}
	return result, 200, nil
}
func (p *Plugin) actionProxySourceUpdate(params map[string]any) (any, int, error) {
	id := strings.TrimSpace(asString(params["source_id"]))
	kind := strings.ToLower(asString(params["kind"]))
	if kind == "" {
		kind = p.proxySourceKind(id)
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
		if _, ok := p.proxyExistingNode(input.ID); !ok {
			return nil, 404, errors.New("proxy node not found")
		}
		task := p.startTask("proxy.source.update", nil, "正在保存并同步代理节点", func(ctx context.Context) (any, error) {
			_, result, err := p.updateProxyNodeContext(ctx, input)
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
		current, ok := p.proxyExistingTemplate(input.ID)
		if !ok {
			return nil, 404, errors.New("proxy template not found")
		}
		if _, err := buildProxyTemplate(input, current); err != nil {
			return proxyValidationFailure(err), proxyInputErrorStatus(err), err
		}
		task := p.startTask("proxy.source.update", nil, "正在保存代理模板", func(ctx context.Context) (any, error) {
			_, result, err := p.updateProxyTemplateContext(ctx, input)
			return result, err
		})
		return task, 202, nil
	case "resin":
		values := params
		if nested, ok := params["source"].(map[string]any); ok {
			values = nested
		}
		if intFromParams(values, "expected_version", 0) < 1 {
			return nil, 400, proxyError("proxy_version_required", 400, "expected_version is required for updates")
		}
		return p.actionResinConfigSave(context.Background(), values)
	default:
		return nil, 404, errors.New("proxy source not found")
	}
}
func (p *Plugin) actionProxySourceDelete(params map[string]any) (any, int, error) {
	id := strings.TrimSpace(asString(params["source_id"]))
	kind := p.proxySourceKind(id)
	if kind == "" {
		return nil, 404, errors.New("proxy source not found")
	}
	if p.stateStore().ProxyBindingCount(id) > 0 && !boolFromParams(params, "force") {
		return nil, 409, proxyError("proxy_source_in_use", 409, "source has active bindings; use force=true to clear them and delete")
	}
	task := p.startTask("proxy.source.delete", nil, "正在清理绑定并删除代理来源", func(ctx context.Context) (any, error) {
		return p.deleteProxySource(ctx, id, proxy.SourceKind(kind), boolFromParams(params, "force"))
	})
	return task, 202, nil
}
func (p *Plugin) proxySourceKind(id string) string {
	if entry := p.proxyCatalog().ByID[id]; entry != nil {
		return string(entry.Kind)
	}
	return ""
}
func (p *Plugin) testProxySource(ctx context.Context, id, target string) (map[string]any, error) {
	entry := p.proxyCatalog().ByID[id]
	if entry == nil {
		return nil, proxyError("proxy_source_not_found", 404, "proxy source not found")
	}
	if entry.Error != nil {
		return nil, entry.Error
	}
	if entry.Provider == nil {
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
		rendered, lease, release, err = p.generateProxySource(ctx, *entry, "preview", nil, "", false)
		if release != nil {
			defer release()
		}
		if err == nil && lease != nil {
			if job, ok := p.cleanupForLease("preview", *lease); ok {
				job.NextAttemptAt = time.Now().UTC().Add(time.Minute)
				err = p.stateStore().CommitProxy(proxyCommit{Cleanup: []ResinLeaseDelete{job}})
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
		_ = p.stateStore().RecordProxyHealth(id, entry.Revision, health, checked, err)
		p.wakeProxyAutoBind()
	}
	result := map[string]any{"source_id": id, "reachable": probe.Reachable, "latency_ms": probe.LatencyMS, "protocol": probe.Protocol, "level": probe.Level, "target": probe.Target, "endpoint": rendered.Endpoint.MaskedURL(), "tested_at": checked, "health": health, "health_checked": checked}
	if err != nil {
		result["error"] = sanitizeError(err)
	}
	return result, nil
}
func (p *Plugin) actionProxySourceTest(ctx context.Context, params map[string]any) (any, int, error) {
	result, err := p.testProxySource(ctx, strings.TrimSpace(asString(params["source_id"])), strings.TrimSpace(asString(params["target"])))
	if err != nil {
		return result, proxyInputErrorStatus(err), err
	}
	return result, 200, nil
}
func (p *Plugin) recordProxyNodeHealth(node proxy.Node, health proxy.HealthStatus, checked time.Time) {
	_ = p.stateStore().RecordProxyHealth(node.ID, node.Revision, health, checked, nil)
	p.wakeProxyAutoBind()
}
func (p *Plugin) actionProxyBatch(action string, params map[string]any) (any, int, error) {
	ids := authIDsFromParams(params)
	if len(ids) == 0 || len(ids) > 10000 {
		return nil, 400, errors.New("provide 1 to 10000 auth IDs")
	}
	opts, err := proxyBindOptionsFromParams(params)
	if err != nil {
		return nil, 400, err
	}
	opts.ManualClearPolicy = p.stateStore().ProxyAutoBindConfig().ManualClearPolicy
	task := p.startTask("proxy."+action, ids, "正在更新代理绑定", func(ctx context.Context) (any, error) {
		results := make([]proxyBindingResult, len(ids))
		for i, id := range ids {
			results[i] = proxyBindingResult{AuthID: id, Status: "pending"}
		}
		err := runBoundedQueue(ctx, proxyIndices(len(ids)), min(256, len(ids)), p.proxyWorkerCount(), func(jobCtx context.Context, i int) {
			jobOpts := opts
			if len(ids) > 1 && opts.OperationID != "" {
				jobOpts.OperationID = opts.OperationID + "-" + proxyURLHash(ids[i])[:12]
			}
			var result proxyBindingResult
			var err error
			switch action {
			case "bind":
				result, err = p.bindProxy(jobCtx, ids[i], jobOpts)
			case "clear":
				result, err = p.clearProxy(jobCtx, ids[i], jobOpts)
			case "rotate":
				result, err = p.rotateProxy(jobCtx, ids[i], jobOpts)
			case "adopt":
				result, err = p.adoptProxy(jobCtx, ids[i], jobOpts)
			}
			if err != nil && result.Error == "" {
				result.Error = sanitizeError(err)
			}
			results[i] = result
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
func (p *Plugin) actionProxyBind(_ context.Context, params map[string]any) (any, int, error) {
	return p.actionProxyBatch("bind", params)
}
func (p *Plugin) actionProxyClear(_ context.Context, params map[string]any) (any, int, error) {
	return p.actionProxyBatch("clear", params)
}
func (p *Plugin) actionProxyRotate(_ context.Context, params map[string]any) (any, int, error) {
	return p.actionProxyBatch("rotate", params)
}
func (p *Plugin) actionProxyBindUnbound(_ context.Context, params map[string]any) (any, int, error) {
	opts, err := proxyBindOptionsFromParams(params)
	if err != nil {
		return nil, 400, err
	}
	task := p.startTask("proxy.bind_unbound", nil, "正在绑定未绑定账号", func(ctx context.Context) (any, error) {
		results, err := p.proxyBindUnbound(ctx, opts)
		return proxyBatchResult(results), err
	})
	return task, 202, nil
}
func proxyBindOptionsFromParams(params map[string]any) (proxyBindOptions, error) {
	scope, err := normalizeProxyScope(asString(params["source_scope"]))
	if err != nil {
		return proxyBindOptions{}, err
	}
	opts := proxyBindOptions{SourceScope: scope, SourceIDs: uniqueNonEmptyStrings(stringListFromAny(params["source_ids"])), DryRun: boolFromParams(params, "dry_run"), AllowExternal: boolFromParams(params, "allow_external"), RequireHealthy: boolFromParams(params, "require_healthy"), OperationID: strings.TrimSpace(asString(params["operation_id"])), Replace: boolFromParams(params, "replace")}
	if strings.TrimSpace(asString(params["source_scope"])) == "" {
		opts.SourceScope = ""
	}
	if len(opts.OperationID) > 100 {
		return opts, errors.New("operation_id must be at most 100 bytes")
	}
	if len(opts.SourceIDs) == 0 {
		if id := strings.TrimSpace(asString(params["source_id"])); id != "" {
			opts.SourceIDs = []string{id}
		}
	}
	if raw, ok := params["exclude_auto_bind"]; ok && raw != nil {
		v, ok := raw.(bool)
		if !ok {
			return opts, errors.New("exclude_auto_bind must be true, false, or null")
		}
		opts.ExcludeAutoBind = &v
	}
	if opts.RequireHealthy {
		value := firstNonEmptyString(asString(params["health_max_age"]), "10m")
		age, err := time.ParseDuration(value)
		if err != nil || age < time.Second || age > 24*time.Hour {
			return opts, errors.New("health_max_age must be between 1s and 24h")
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
func (p *Plugin) actionProxyAutoBindGet() (any, int, error) {
	p.mu.RLock()
	rotate := p.cfg.Proxy.AutoRotateAfterQualityFailure
	p.mu.RUnlock()
	return map[string]any{"config": p.stateStore().ProxyAutoBindConfig(), "runtime": p.proxyAutoBindRuntime(), "auto_rotate_after_quality_failure": rotate}, 200, nil
}
func (p *Plugin) actionProxyAutoBindUpdate(params map[string]any) (any, int, error) {
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
	config, rotate, err := p.updateProxyAutoBind(source)
	if err != nil {
		return nil, proxyInputErrorStatus(err), err
	}
	return map[string]any{"config": config, "runtime": p.proxyAutoBindRuntime(), "auto_rotate_after_quality_failure": rotate}, 200, nil
}
func (p *Plugin) actionProxyAutoBindRun() (any, int, error) {
	p.wakeProxyAutoBind()
	return map[string]any{"woken": true, "config": p.stateStore().ProxyAutoBindConfig(), "runtime": p.proxyAutoBindRuntime()}, 202, nil
}
func (p *Plugin) actionProxyBindingsList(ctx context.Context, params map[string]any) (any, int, error) {
	if err := p.ensureCatalog(ctx); err != nil {
		return nil, 500, err
	}
	page := max(1, intFromParams(params, "page", 1))
	size := intFromParams(params, "page_size", 50)
	if size < 1 || size > 500 {
		size = 50
	}
	start := int(^uint(0) >> 1)
	if page-1 <= start/size {
		start = (page - 1) * size
	}
	query := strings.ToLower(strings.TrimSpace(asString(params["query"])))
	source := strings.TrimSpace(asString(params["source_id"]))
	status := strings.ToLower(strings.TrimSpace(asString(params["status"])))
	excludedRaw, excludedSet := params["excluded"]
	excludedSet = excludedSet && excludedRaw != nil
	excluded := boolFromParams(params, "excluded")
	idsOnly := boolFromParams(params, "ids_only")
	ids := []string{}
	rows := make([]proxyCatalogRow, 0, size)
	total := 0
	p.walkProxyCatalog(func(row proxyCatalogRow) {
		if !proxyStatusMatches(status, row.Status) || source != "" && row.Binding.SourceID != source || query != "" && !catalogAccountMatches(row.Account, query) || excludedSet && row.AutoBind.Excluded != excluded {
			return
		}
		if idsOnly {
			ids = append(ids, row.Account.ID)
		} else if total >= start && total-start < size {
			row.Binding.ResolvedValues = nil
			row.Binding.Lease = nil
			rows = append(rows, row)
		}
		total++
	})
	if idsOnly {
		return map[string]any{"items": ids, "total": total}, 200, nil
	}
	items := make([]map[string]any, len(rows))
	for i, row := range rows {
		items[i] = proxyRowView(row)
	}
	return map[string]any{"items": items, "total": total, "page": page, "page_size": size}, 200, nil
}
func (p *Plugin) actionProxyBindingsAutoBind(params map[string]any) (any, int, error) {
	ids := authIDsFromParams(params)
	if len(ids) == 0 || len(ids) > 10000 {
		return nil, 400, errors.New("provide 1 to 10000 auth IDs")
	}
	result, err := p.setProxyAutoBindForAuths(ids, boolFromParams(params, "excluded"))
	if err != nil {
		return result, proxyErrorStatus(err), err
	}
	return result, 200, nil
}
func (p *Plugin) actionProxyTemplateApply(_ context.Context, params map[string]any) (any, int, error) {
	id := strings.TrimSpace(asString(params["source_id"]))
	if entry := p.proxyCatalog().ByID[id]; entry == nil || entry.Template == nil {
		return nil, 404, errors.New("proxy template not found")
	}
	count := p.stateStore().ProxyBindingCount(id)
	if boolFromParams(params, "dry_run") {
		return map[string]any{"source_id": id, "bound_count": count, "count": count, "dry_run": true}, 200, nil
	}
	changeID, err := p.createTemplateApplyChange(id)
	if err != nil {
		return nil, proxyErrorStatus(err), err
	}
	task := p.startTask("proxy.template.apply", nil, "正在应用模板到已绑定账号", func(ctx context.Context) (any, error) { return p.runProxyChange(ctx, changeID) })
	return task, 202, nil
}
func (p *Plugin) actionProxyChangesList(params map[string]any) (any, int, error) {
	items := p.stateStore().ProxyChangeSummaries()
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	page, size, start, end := proxyPage(params, len(items))
	return map[string]any{"items": items[start:end], "total": len(items), "page": page, "page_size": size}, 200, nil
}
func (p *Plugin) actionProxyChange(params map[string]any, action string) (any, int, error) {
	id := strings.TrimSpace(asString(params["change_id"]))
	if id == "" {
		return nil, 400, errors.New("change_id is required")
	}
	switch action {
	case "get":
		change, ok := p.stateStore().ProxyChange(id)
		if !ok {
			return nil, 404, errors.New("proxy change not found")
		}
		page, size, start, end := proxyPage(params, len(change.Items))
		return map[string]any{"change_id": change.ID, "source_id": change.SourceID, "status": change.Status, "target_revision": change.TargetRevision, "counts": change.Counts, "items": change.Items[start:end], "total": len(change.Items), "page": page, "page_size": size}, 200, nil
	case "cancel":
		err := p.cancelProxyChange(id)
		if err != nil {
			return nil, proxyErrorStatus(err), err
		}
		return map[string]any{"change_id": id, "status": "canceled"}, 200, nil
	case "retry":
		task := p.startTask("proxy.change.retry", nil, "正在重试未完成代理变更", func(ctx context.Context) (any, error) { return p.runProxyChange(ctx, id) })
		return task, 202, nil
	}
	return nil, http.StatusBadRequest, fmt.Errorf("unknown change action")
}
func proxySourceParamPresent(params map[string]any, key string) bool {
	raw := params
	if nested, ok := params["source"].(map[string]any); ok {
		raw = nested
	}
	value, ok := raw[key]
	return ok && value != nil
}
