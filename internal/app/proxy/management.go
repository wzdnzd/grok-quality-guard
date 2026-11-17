// Proxy and Resin management API, response projections and cleanup controls.
package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	actionpkg "grok-quality-guard/internal/app/action"
	"grok-quality-guard/internal/app/redact"
	"grok-quality-guard/internal/app/tasks"
	"grok-quality-guard/internal/config"
	proxy "grok-quality-guard/internal/proxy"
)

func (s *Service) actionProxySummary(ctx context.Context) (any, int, error) {
	if err := s.env.ProxyEnsureCatalog(ctx); err != nil {
		return nil, 500, err
	}
	sources := s.proxySourcesView()
	counts := map[string]int{"managed": 0, "unbound": 0, "external": 0, "drifted": 0, "unknown": 0, "needs_reconcile": 0}
	accountTotal := s.walkProxyCatalog(func(row proxyCatalogRow) { counts[row.Status]++ })
	sourceCounts := map[string]int{"node": 0, "template": 0, "resin": 0}
	available := 0
	catalog := s.proxyCatalog()
	bound := s.env.ProxyStore().ProxyCounts()
	for _, view := range sources {
		sourceCounts[string(view.Kind)]++
		entry := catalog.ByID[view.ID]
		if entry != nil && proxyCandidateEnabled(*entry) && entry.Error == nil && view.Lifecycle == "active" && (entry.Max == 0 || bound[view.ID] < entry.Max) {
			available++
		}
	}
	_, _, rotate := s.env.ProxyConfigSnapshot()
	return map[string]any{"source_count": len(sources), "source_counts": sourceCounts, "available_source_count": available, "enabled_source_count": countEnabledProxySources(sources), "binding_counts": counts, "binding_total": accountTotal, "managed_binding_count": counts["managed"], "auto_bind": s.env.ProxyStore().ProxyAutoBindConfig(), "auto_bind_runtime": s.proxyAutoBindRuntime(), "auto_rotate_after_quality_failure": rotate}, 200, nil
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
func (s *Service) actionProxySourcesList(params map[string]any) (any, int, error) {
	sources := s.proxySourcesView()
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
func (s *Service) actionProxySourceGet(params map[string]any) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["source_id"]))
	if id == "" {
		return nil, 400, errors.New("source_id is required")
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
		return nil, 400, errors.New("source_id is required")
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
	return map[string]any{"valid": false, "error": issue.Message, "errors": []proxy.ValidationIssue{issue}, "validation": proxy.ValidationReport{Valid: false, Errors: []proxy.ValidationIssue{issue}}}
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
		return s.actionResinConfigValidate(context.Background(), params)
	default:
		return nil, 400, errors.New("kind must be node, template, or resin")
	}
}
func (s *Service) actionProxySourcePreview(params map[string]any) (any, int, error) {
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
			return nil, 400, errors.New("assignments must map placeholder names to strings")
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
		samples = append(samples, proxy.TemplateSample{MaskedURL: rendered.Endpoint.MaskedURL(), Values: rendered.Values, VariantKey: rendered.VariantKey})
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
		return nil, 400, errors.New("nodes or urls is required")
	}
	format := strings.TrimSpace(actionpkg.String(params["import_format"]))
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
			return nil, 404, errors.New("proxy node not found")
		}
		if _, _, changed, _, err := buildProxyNodeUpdate(input, current); err != nil {
			return nil, proxyInputErrorStatus(err), err
		} else if !changed {
			return nil, 400, proxySourceUnchangedError()
		}
		task := s.env.ProxyStartTask("proxy.source.update", nil, "正在保存并同步代理节点", func(ctx context.Context) (any, error) {
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
			return nil, 404, errors.New("proxy template not found")
		}
		next, err := buildProxyTemplate(input, current)
		if err != nil {
			return proxyValidationFailure(err), proxyInputErrorStatus(err), err
		}
		if templateEditorConfigEqual(next, *current) {
			return nil, 400, proxySourceUnchangedError()
		}
		task := s.env.ProxyStartTask("proxy.source.update", nil, "正在保存代理模板", func(ctx context.Context) (any, error) {
			_, result, err := s.updateProxyTemplateContext(ctx, input)
			return result, err
		})
		return task, 202, nil
	case "resin":
		values := params
		if nested, ok := params["source"].(map[string]any); ok {
			values = nested
		}
		if actionpkg.Int(values, "expected_version", 0) < 1 {
			return nil, 400, proxyError("proxy_version_required", 400, "expected_version is required for updates")
		}
		return s.actionResinConfigSave(context.Background(), values)
	default:
		return nil, 404, errors.New("proxy source not found")
	}
}
func (s *Service) actionProxySourceDelete(params map[string]any) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["source_id"]))
	kind := s.proxySourceKind(id)
	if kind == "" {
		return nil, 404, errors.New("proxy source not found")
	}
	if s.env.ProxyStore().ProxyBindingCount(id) > 0 && !actionpkg.Bool(params, "force") {
		return nil, 409, proxyError("proxy_source_in_use", 409, "source has active bindings; use force=true to clear them and delete")
	}
	task := s.env.ProxyStartTask("proxy.source.delete", nil, "正在清理绑定并删除代理来源", func(ctx context.Context) (any, error) {
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
		return nil, 400, errors.New("provide 1 to 10000 source IDs")
	}
	if s.env.ProxyHasActivePrefix("proxy.sources.test_selected") {
		return nil, 409, proxyError("proxy_source_test_busy", 409, "代理来源批量检测任务已在执行")
	}
	task := s.env.ProxyStartTask("proxy.sources.test_selected", ids, "正在检测已选代理来源", func(taskCtx context.Context) (any, error) {
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
				}
			}
			items[index] = item
		})
		return proxySourceBatchResult(items), err
	})
	return task, 202, nil
}

func (s *Service) actionProxySourcesDeleteSelected(_ context.Context, params map[string]any) (any, int, error) {
	ids := proxySourceIDs(params)
	if len(ids) == 0 || len(ids) > 10000 {
		return nil, 400, errors.New("provide 1 to 10000 source IDs")
	}
	if s.env.ProxyHasActivePrefix("proxy.sources.delete_selected") {
		return nil, 409, proxyError("proxy_source_delete_busy", 409, "代理来源批量删除任务已在执行")
	}
	force := actionpkg.Bool(params, "force")
	task := s.env.ProxyStartTask("proxy.sources.delete_selected", ids, "正在清理并删除已选代理来源", func(taskCtx context.Context) (any, error) {
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
			} else {
				item.Status = "deleted"
				item.Endpoint, _ = result["proxy"].(string)
			}
			items[index] = item
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
		return nil, 400, errors.New("provide 1 to 10000 auth IDs")
	}
	opts, err := proxyBindOptionsFromParams(params)
	if err != nil {
		return nil, 400, err
	}
	opts.ManualClearPolicy = s.env.ProxyStore().ProxyAutoBindConfig().ManualClearPolicy
	task := s.env.ProxyStartTask("proxy."+action, ids, "正在更新代理绑定", func(ctx context.Context) (any, error) {
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
	task := s.env.ProxyStartTask("proxy.bind_unbound", nil, "正在绑定未绑定账号", func(ctx context.Context) (any, error) {
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
		return opts, errors.New("operation_id must be at most 100 bytes")
	}
	if len(opts.SourceIDs) == 0 {
		if id := strings.TrimSpace(actionpkg.String(params["source_id"])); id != "" {
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
		value := firstNonEmptyString(actionpkg.String(params["health_max_age"]), "10m")
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
func (s *Service) actionProxyAutoBindGet() (any, int, error) {
	_, _, rotate := s.env.ProxyConfigSnapshot()
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
	if err := s.env.ProxyEnsureCatalog(ctx); err != nil {
		return nil, 500, err
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
	s.walkProxyCatalog(func(row proxyCatalogRow) {
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

func (s *Service) actionProxyBindingEndpoint(ctx context.Context, params map[string]any) (any, int, error) {
	authID := strings.TrimSpace(actionpkg.String(params["auth_id"]))
	if authID == "" {
		return nil, 400, errors.New("auth_id is required")
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
		return nil, 404, errors.New("proxy endpoint is empty")
	}
	return map[string]any{"auth_id": authID, "proxy": maskProxyURL(proxyURL), "proxy_full": proxyURL}, 200, nil
}

func (s *Service) actionProxyBindingsAutoBind(params map[string]any) (any, int, error) {
	ids := actionpkg.AuthIDs(params)
	if len(ids) == 0 || len(ids) > 10000 {
		return nil, 400, errors.New("provide 1 to 10000 auth IDs")
	}
	result, err := s.setProxyAutoBindForAuths(ids, actionpkg.Bool(params, "excluded"))
	if err != nil {
		return result, proxyErrorStatus(err), err
	}
	return result, 200, nil
}
func (s *Service) actionProxyTemplateApply(_ context.Context, params map[string]any) (any, int, error) {
	id := strings.TrimSpace(actionpkg.String(params["source_id"]))
	if entry := s.proxyCatalog().ByID[id]; entry == nil || entry.Template == nil {
		return nil, 404, errors.New("proxy template not found")
	}
	count := s.env.ProxyStore().ProxyBindingCount(id)
	if actionpkg.Bool(params, "dry_run") {
		return map[string]any{"source_id": id, "bound_count": count, "count": count, "dry_run": true}, 200, nil
	}
	changeID, err := s.createTemplateApplyChange(id)
	if err != nil {
		return nil, proxyErrorStatus(err), err
	}
	task := s.env.ProxyStartTask("proxy.template.apply", nil, "正在应用模板到已绑定账号", func(ctx context.Context) (any, error) { return s.runProxyChange(ctx, changeID) })
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
		return nil, 400, errors.New("change_id is required")
	}
	switch action {
	case "get":
		change, ok := s.env.ProxyStore().ProxyChange(id)
		if !ok {
			return nil, 404, errors.New("proxy change not found")
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
		task := s.env.ProxyStartTask("proxy.change.retry", nil, "正在重试未完成代理变更", func(ctx context.Context) (any, error) { return s.runProxyChange(ctx, id) })
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
	counts := s.env.ProxyStore().ProxyCounts()
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
			if !s.Configured && !s.LocalOnly {
				continue
			}
			view = proxySourceView{ID: entry.ID, Kind: entry.Kind, Name: entry.Name, Enabled: s.Enabled, Valid: entry.Error == nil, ConfigVersion: entry.ConfigVersion, Revision: entry.Revision, BindingCount: counts[entry.ID], LocalOnly: s.LocalOnly, Configured: s.Configured, PasswordSet: s.ProxyToken != ""}
			if rendered, err := renderResinProxy(s, "preview"); err == nil {
				view.Endpoint = maskProxyURL(rendered)
			}
		}
		view.Valid = entry.Error == nil
		if entry.Error != nil {
			view.LastError = redact.SanitizeError(entry.Error)
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
		views = append(views, view)
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
		if entry.ResinSettings == nil {
			return "", proxyError("proxy_source_not_found", 404, "Resin source not found")
		}
		return renderResinProxy(*entry.ResinSettings, "preview")
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
		return s.resinConfigView(*entry.ResinSettings), nil
	}
	count := s.env.ProxyStore().ProxyBindingCount(id)
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

// Bulk readers copy only proxy metadata; quality metrics and generated values
// do not belong in a binding table or scan.
func (s *Service) proxyAccountBindingView(state AccountState, value string) (string, string, proxy.SourceKind, bool) {
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

func (s *Service) proxyObservationFor(account Account) proxyObservation {
	s.mgr.mu.Lock()
	observation, ok := s.mgr.observations[account.ID]
	s.mgr.mu.Unlock()
	if ok && time.Since(observation.CheckedAt) < catalogRefreshTTL && (account.ModTime.IsZero() || !account.ModTime.After(observation.CheckedAt)) {
		return observation
	}
	return proxyObservation{URL: account.ProxyURL, Hash: account.ProxyHash, Known: account.ProxyURLKnown || account.ProxyURL != "", CheckedAt: account.ModTime}
}

func (s *Service) confirmProxyObservation(ctx context.Context, account Account) proxyObservation {
	observation := s.proxyObservationFor(account)
	if observation.Known {
		return observation
	}
	_, _, raw, err := s.env.ProxyResolveAuth(ctx, account.ID)
	if err == nil {
		observation.URL, err = proxyURLFromRaw(raw)
	}
	observation.Known = err == nil
	if err == nil && observation.URL != "" {
		observation.Hash = proxyURLHash(observation.URL)
	}
	observation.CheckedAt = time.Now().UTC()
	s.mgr.mu.Lock()
	s.mgr.observations[account.ID] = observation
	s.mgr.mu.Unlock()
	return observation
}

type proxyCatalogRow struct {
	Account     Account
	Status      string
	Binding     proxy.Binding
	AutoBind    proxy.AutoBindAccountState
	Observation proxyObservation
}

// Count/filter from immutable state projections; proxy observations remain
// protected by the runtime lock while rows are assembled.
func (s *Service) walkProxyCatalog(visit func(proxyCatalogRow)) int {
	accounts := s.env.ProxyCatalogSnapshot()
	ids := make([]string, len(accounts))
	for i, account := range accounts {
		ids[i] = account.ID
	}
	summaries := s.env.ProxyStore().ProxyAccountsForIDs(ids)
	byID := make(map[string]proxyAccountSummary, len(summaries))
	for _, summary := range summaries {
		byID[summary.AuthID] = summary
	}
	s.mgr.mu.Lock()
	defer s.mgr.mu.Unlock()
	now := time.Now()
	for _, a := range accounts {
		row := proxyCatalogRow{Account: a, Status: "unbound", Observation: proxyObservation{URL: a.ProxyURL, Hash: a.ProxyHash, Known: a.ProxyURLKnown || a.ProxyURL != "", CheckedAt: a.ModTime}}
		if observation, ok := s.mgr.observations[a.ID]; ok && now.Sub(observation.CheckedAt) < catalogRefreshTTL && (a.ModTime.IsZero() || !a.ModTime.After(observation.CheckedAt)) {
			row.Observation = observation
		}
		if summary, ok := byID[a.ID]; ok {
			row.Binding = summary.Proxy
			row.AutoBind = summary.AutoBind
			if row.Binding.SourceID == "" && (summary.Resin.Managed || summary.Resin.Account != "") {
				row.Binding.SourceID = resinSourceID
				row.Binding.SourceKind = proxy.SourceResin
				row.Binding.Generation = uint64(max(summary.Resin.Generation, 0))
			}
			if summary.Pending {
				row.Status = "needs_reconcile"
			}
		}
		if row.Status != "needs_reconcile" {
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
		}
		visit(row)
	}
	return len(accounts)
}
func proxyRowView(row proxyCatalogRow) map[string]any {
	a, b := row.Account, row.Binding
	displayFull := strings.TrimSpace(firstNonEmptyString(a.Email, a.Label, a.ID))
	return map[string]any{"auth_id": a.ID, "auth_id_display": maskAuthID(a.ID), "display": maskAccountIdentity(displayFull), "display_full": displayFull, "proxy": maskProxyURL(row.Observation.URL), "proxy_status": row.Status, "source_id": b.SourceID, "source_kind": b.SourceKind, "generation": b.Generation, "source_revision": b.SourceRevision, "auto_bind_excluded": row.AutoBind.Excluded, "next_attempt_at": row.AutoBind.NextAttemptAt, "last_error": row.AutoBind.LastError}
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
				items = append(items, map[string]any{"id": resinCleanupID(job), "auth_id": job.AuthID, "account": job.Account, "admin_url": job.AdminURL, "platform_id": job.PlatformID, "status": jobStatus, "paused": job.Paused, "attempts": job.Attempts, "next_attempt_at": job.NextAttemptAt, "last_error": job.LastError, "created_at": job.CreatedAt})
			}
			total++
		}
		return map[string]any{"items": items, "total": total, "page": page, "page_size": size}, 200, nil
	}
	if action != "retry" && action != "pause" {
		return nil, 400, errors.New("unknown cleanup action")
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
