package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	proxy "grok-quality-guard/internal/proxy"
)

func (p *Plugin) runProxyChange(ctx context.Context, id string) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p.proxyMgr.changeMu.Lock()
	if p.proxyMgr.runningChanges == nil {
		p.proxyMgr.runningChanges = make(map[string]struct{})
	}
	if _, running := p.proxyMgr.runningChanges[id]; running {
		p.proxyMgr.changeMu.Unlock()
		return nil, proxyError("proxy_change_busy", 409, "proxy change is already running")
	}
	p.proxyMgr.runningChanges[id] = struct{}{}
	p.proxyMgr.changeMu.Unlock()
	defer func() {
		p.proxyMgr.changeMu.Lock()
		delete(p.proxyMgr.runningChanges, id)
		p.proxyMgr.changeMu.Unlock()
	}()
	change, ok := p.stateStore().ProxyChange(id)
	if !ok {
		return nil, proxyError("proxy_change_not_found", 404, "proxy change not found")
	}
	if change.Status == "succeeded" {
		return proxyChangeResult(change, nil), nil
	}
	var node proxy.Node
	var compiled *proxy.CompiledTemplate
	if change.Kind == "node_sync" {
		var found bool
		node, found = p.stateStore().ProxyNodeVersion(change.SourceID, change.TargetRevision)
		if !found {
			return nil, proxyError("proxy_source_version_missing", 409, "node target version is missing")
		}
	}
	if change.Kind != "node_sync" && change.Kind != "source_delete" {
		t, found := p.stateStore().ProxyTemplateVersion(change.SourceID, change.TargetRevision)
		if !found {
			return nil, proxyError("proxy_source_version_missing", 409, "template target version is missing")
		}
		var err error
		compiled, err = proxy.CompileTemplate(t)
		if err != nil {
			return nil, err
		}
	}
	if err := p.stateStore().Update(func(state *State) error {
		runtime := state.Proxy.SourceRuntime[change.SourceID]
		if runtime.ChangeID != "" && runtime.ChangeID != id {
			return proxyError("proxy_source_busy", 409, "another source change is in progress")
		}
		if runtime.ChangeID == "" && change.Kind != "source_delete" {
			revision := uint64(0)
			for _, n := range state.Proxy.Nodes {
				if n.ID == change.SourceID {
					revision = n.Revision
				}
			}
			for _, t := range state.Proxy.Templates {
				if t.ID == change.SourceID {
					revision = t.Revision
				}
			}
			if revision != change.TargetRevision {
				return proxyError("proxy_source_version_conflict", 409, "source changed after cancellation; apply its current version instead")
			}
		}
		if state.Proxy.SourceRuntime == nil {
			state.Proxy.SourceRuntime = make(map[string]proxy.SourceRuntime)
		}
		runtime.ChangeID = id
		runtime.Lifecycle = "syncing"
		if change.Kind == "source_delete" {
			runtime.Lifecycle = "deleting"
		}
		state.Proxy.SourceRuntime[change.SourceID] = runtime
		for i := range state.Proxy.Changes {
			if state.Proxy.Changes[i].ID == id {
				state.Proxy.Changes[i].Status = "running"
				return nil
			}
		}
		return proxyError("proxy_change_not_found", 404, "proxy change not found")
	}); err != nil {
		return nil, err
	}
	indices := make([]int, 0, len(change.Items))
	for i, item := range change.Items {
		if item.Status != "succeeded" && item.Status != "superseded" && item.Status != "skipped" {
			indices = append(indices, i)
		}
	}
	results := make([]proxyBindingResult, len(indices))
	for i, index := range indices {
		results[i] = proxyBindingResult{AuthID: change.Items[index].AuthID, Status: "pending"}
	}
	errRun := runBoundedQueue(ctx, proxyIndices(len(indices)), min(256, len(indices)), p.proxyWorkerCount(), func(jobCtx context.Context, position int) {
		if p.stateStore().ProxyChangeStatus(id) == "canceled" {
			results[position].Status = "canceled"
			return
		}
		item := change.Items[indices[position]]
		opts := proxyBindOptions{ChangeID: id, OperationID: item.OperationID, ExpectedSourceID: change.SourceID, ExpectedGeneration: &item.Generation}
		var result proxyBindingResult
		var err error
		if operation, found := p.stateStore().ProxyOperation(item.OperationID); item.OperationID != "" && found {
			if operation.ChangeID != id {
				err = proxyError("proxy_operation_conflict", 409, "operation belongs to another change")
			} else {
				result, err = p.resumeProxyOperation(jobCtx, item.AuthID, operation)
			}
		} else if change.Kind == "source_delete" {
			state := p.stateStore().ProxyAccount(item.AuthID)
			excluded := state.AutoBind.Excluded
			opts.ExcludeAutoBind = &excluded
			result, err = p.clearProxy(jobCtx, item.AuthID, opts)
		} else {
			endpoint := node.Endpoint
			var values map[string]string
			if compiled != nil {
				rendered, renderErr := compiled.RenderWithReader(nil, nil)
				if renderErr != nil {
					err = renderErr
				} else {
					endpoint = rendered.Endpoint
					values = rendered.Values
				}
			}
			if err == nil {
				kind := proxy.SourceNode
				if compiled != nil {
					kind = proxy.SourceTemplate
				}
				result, err = p.replaceProxyEndpoint(jobCtx, item.AuthID, kind, change.SourceID, change.TargetRevision, endpoint, values, "proxy_source_apply", opts)
			}
		}
		if err != nil && result.Error == "" {
			result.Error = sanitizeError(err)
		}
		if result.Status == "" {
			result.Status = "failed"
		}
		result.AuthID = item.AuthID
		if result.OperationID != "" {
			item.OperationID = result.OperationID
		}
		item.Status = result.Status
		item.Error = result.Error
		switch result.Status {
		case "succeeded", "bound", "rotated", "cleared", "unchanged":
			item.Status = "succeeded"
		}
		if !result.Committed || result.NeedsReconcile {
			if err := p.stateStore().CommitProxy(proxyCommit{ChangeID: id, ChangeItem: &item}); err != nil {
				result.NeedsReconcile = true
				result.Error = sanitizeError(err)
			}
		}
		results[position] = result
	})
	var final proxy.SourceChange
	errFinish := p.stateStore().Update(func(state *State) error {
		for i := range state.Proxy.Changes {
			current := &state.Proxy.Changes[i]
			if current.ID != id {
				continue
			}
			counts := proxy.ChangeCounts{Total: len(current.Items)}
			for _, item := range current.Items {
				switch item.Status {
				case "succeeded", "superseded", "skipped":
					counts.Succeeded++
				case "pending", "":
					counts.Pending++
				default:
					counts.Failed++
				}
			}
			current.Counts = counts
			if current.Status == "canceled" {
			} else if errRun != nil {
				current.Status = "pending"
				current.Error = sanitizeError(errRun)
			} else if counts.Failed > 0 || counts.Pending > 0 {
				current.Status = "partial"
			} else {
				current.Status = "succeeded"
				current.CompletedAt = time.Now().UTC()
			}
			runtime := state.Proxy.SourceRuntime[change.SourceID]
			if current.Status == "succeeded" {
				if change.Kind == "source_delete" {
					if len(proxyBindingAuthIDsFromState(*state, change.SourceID, proxy.SourceKind(p.proxyKindFromState(*state, change.SourceID)))) > 0 {
						current.Status = "partial"
						current.Error = "bindings changed during deletion; clear or reconcile the remaining bindings"
					} else {
						for j, n := range state.Proxy.Nodes {
							if n.ID == change.SourceID {
								state.Proxy.Nodes = append(state.Proxy.Nodes[:j], state.Proxy.Nodes[j+1:]...)
								break
							}
						}
						for j, t := range state.Proxy.Templates {
							if t.ID == change.SourceID {
								state.Proxy.Templates = append(state.Proxy.Templates[:j], state.Proxy.Templates[j+1:]...)
								break
							}
						}
						if change.SourceID == resinSourceID {
							state.Resin = ResinSettings{Configured: true}
							state.Proxy.ResinSource.Enabled = false
							state.Proxy.ResinSource.Deleted = true
						}
						delete(state.Proxy.SourceRuntime, change.SourceID)
					}
				} else if runtime.ChangeID == id {
					runtime.Lifecycle = "active"
					runtime.ChangeID = ""
					state.Proxy.SourceRuntime[change.SourceID] = runtime
				}
			}
			// Successful change items never retry; their generated secrets need not
			// remain in the operation journal. Failed prepared targets are retained.
			for opID, operation := range state.Proxy.Operations {
				if operation.ChangeID == id && operation.Status == "finalized" {
					delete(state.Proxy.Operations, opID)
				}
			}
			final = *current
			return nil
		}
		return proxyError("proxy_change_not_found", 404, "proxy change disappeared")
	})
	if errFinish == nil {
		p.wakeProxyAutoBind()
	}
	return proxyChangeResult(final, results), errors.Join(errRun, errFinish)
}

func (p *Plugin) proxyKindFromState(state State, id string) string {
	if id == resinSourceID {
		return "resin"
	}
	for _, n := range state.Proxy.Nodes {
		if n.ID == id {
			return "node"
		}
	}
	for _, t := range state.Proxy.Templates {
		if t.ID == id {
			return "template"
		}
	}
	return ""
}
func proxyChangeResult(change proxy.SourceChange, results []proxyBindingResult) map[string]any {
	if len(results) > 1000 {
		results = results[:1000]
	}
	return map[string]any{"change_id": change.ID, "source_id": change.SourceID, "status": change.Status, "target_revision": change.TargetRevision, "counts": change.Counts, "count": change.Counts.Total, "bound_count": change.Counts.Total, "items": results}
}
func (p *Plugin) proxyWorkerCount() int {
	config := p.stateStore().ProxyAutoBindConfig()
	return max(1, min(8, config.Concurrency))
}

func (p *Plugin) proxyApplyNodeToAccounts(ctx context.Context, node proxy.Node, ids []string) []proxyBindingResult {
	results := make([]proxyBindingResult, len(ids))
	_ = runBoundedQueue(ctx, proxyIndices(len(ids)), min(len(ids), 256), p.proxyWorkerCount(), func(jobCtx context.Context, i int) {
		result, err := p.proxyReplaceManagedEndpoint(jobCtx, ids[i], proxy.SourceNode, node.ID, node.Revision, node.Endpoint, nil, "proxy_node_sync")
		if err != nil && result.Error == "" {
			result.Error = sanitizeError(err)
		}
		results[i] = result
	})
	return results
}
func (p *Plugin) proxyApplyTemplateToAccounts(ctx context.Context, template proxy.Template, ids []string) []proxyBindingResult {
	compiled, err := proxy.CompileTemplate(template)
	if err != nil {
		return []proxyBindingResult{{Status: "failed", Error: sanitizeError(err)}}
	}
	results := make([]proxyBindingResult, len(ids))
	_ = runBoundedQueue(ctx, proxyIndices(len(ids)), min(len(ids), 256), p.proxyWorkerCount(), func(jobCtx context.Context, i int) {
		rendered, err := compiled.RenderWithReader(nil, nil)
		if err != nil {
			results[i] = failedProxyResult(ids[i], "failed", err)
			return
		}
		result, err := p.proxyReplaceManagedEndpoint(jobCtx, ids[i], proxy.SourceTemplate, template.ID, template.Revision, rendered.Endpoint, rendered.Values, "proxy_template_apply")
		if err != nil && result.Error == "" {
			result.Error = sanitizeError(err)
		}
		results[i] = result
	})
	return results
}
func proxyIndices(length int) []int {
	indices := make([]int, length)
	for i := range indices {
		indices[i] = i
	}
	return indices
}

func (p *Plugin) createTemplateApplyChange(id string) (string, error) {
	p.proxyMgr.changeMu.Lock()
	defer p.proxyMgr.changeMu.Unlock()
	entry := p.proxyCatalog().ByID[id]
	if entry == nil || entry.Template == nil {
		return "", proxyError("proxy_source_not_found", 404, "template not found")
	}
	var changeID string
	err := p.stateStore().Update(func(state *State) error {
		if runtime := state.Proxy.SourceRuntime[id]; runtime.ChangeID != "" {
			return proxyError("proxy_source_busy", 409, "previous template application is unfinished")
		}
		ids := proxyBindingAuthIDsFromState(*state, id, proxy.SourceTemplate)
		changeID = beginProxyChange(state, id, "template_apply", entry.Revision, ids, "syncing")
		return nil
	})
	return changeID, err
}
func (p *Plugin) cancelProxyChange(id string) error {
	return p.stateStore().Update(func(state *State) error {
		for i := range state.Proxy.Changes {
			change := &state.Proxy.Changes[i]
			if change.ID == id {
				if change.Status == "succeeded" {
					return proxyError("proxy_change_completed", 409, "completed changes cannot be canceled")
				}
				change.Status = "canceled"
				if !hasPendingSourceOperation(*state, change.SourceID) {
					runtime := state.Proxy.SourceRuntime[change.SourceID]
					if runtime.ChangeID == id {
						runtime.Lifecycle = "active"
						runtime.ChangeID = ""
						state.Proxy.SourceRuntime[change.SourceID] = runtime
					}
				}
				return nil
			}
		}
		return proxyError("proxy_change_not_found", 404, fmt.Sprintf("proxy change %q not found", id))
	})
}
