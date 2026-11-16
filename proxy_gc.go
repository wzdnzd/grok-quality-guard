package main

import (
	"fmt"
	"time"

	proxy "grok-quality-guard/internal/proxy"
)

func proxyVersionKey(id string, revision uint64) string { return fmt.Sprintf("%s:%d", id, revision) }

// Run with the state lock held. Keep recovery references, not every historical
// generated password or completed million-account task indefinitely.
func gcProxyHistory(state *State) {
	versions := make(map[string]struct{})
	snapshots := make(map[string]struct{})
	useBinding := func(binding proxy.Binding) {
		if binding.SourceID != "" {
			versions[proxyVersionKey(binding.SourceID, binding.SourceRevision)] = struct{}{}
		}
		if binding.Lease != nil && binding.Lease.SnapshotID != "" {
			snapshots[binding.Lease.SnapshotID] = struct{}{}
		}
	}
	for _, account := range state.Accounts {
		if account != nil {
			useBinding(account.Proxy)
		}
	}
	for _, operation := range state.Proxy.Operations {
		useBinding(operation.Binding)
		if operation.OldLease != nil && operation.OldLease.SnapshotID != "" {
			snapshots[operation.OldLease.SnapshotID] = struct{}{}
		}
	}
	for _, job := range state.ResinLeaseDeletes {
		if job.SnapshotID != "" {
			snapshots[job.SnapshotID] = struct{}{}
		}
	}
	if resinCleanupConfigured(state.Resin) {
		snapshots[resinSnapshotID(state.Resin)] = struct{}{}
	}
	for _, change := range state.Proxy.Changes {
		if change.Status != "succeeded" {
			versions[proxyVersionKey(change.SourceID, change.TargetRevision)] = struct{}{}
		}
	}
	keepTemplates := state.Proxy.TemplateVersions[:0]
	for _, template := range state.Proxy.TemplateVersions {
		if _, used := versions[proxyVersionKey(template.ID, template.Revision)]; used {
			keepTemplates = append(keepTemplates, template)
		}
	}
	clear(state.Proxy.TemplateVersions[len(keepTemplates):])
	state.Proxy.TemplateVersions = keepTemplates
	keepNodes := state.Proxy.NodeVersions[:0]
	for _, node := range state.Proxy.NodeVersions {
		if _, used := versions[proxyVersionKey(node.ID, node.Revision)]; used {
			keepNodes = append(keepNodes, node)
		}
	}
	clear(state.Proxy.NodeVersions[len(keepNodes):])
	state.Proxy.NodeVersions = keepNodes
	for id := range state.ResinSnapshots {
		if _, used := snapshots[id]; !used {
			delete(state.ResinSnapshots, id)
		}
	}
	completed := 0
	for _, change := range state.Proxy.Changes {
		if change.Status == "succeeded" {
			completed++
		}
	}
	drop := max(0, completed-100)
	keepChanges := make([]proxy.SourceChange, 0, len(state.Proxy.Changes)-drop)
	for _, change := range state.Proxy.Changes {
		if change.Status == "succeeded" {
			if drop > 0 {
				drop--
				continue
			}
			change.AuthIDs = nil
			if len(change.Items) > 1000 {
				change.Items = append([]proxy.ChangeItem(nil), change.Items[:1000]...)
				change.ItemsTruncated = true
			}
		}
		keepChanges = append(keepChanges, change)
	}
	state.Proxy.Changes = keepChanges
	gcResinAdminTokens(state)
}

func (s *StateStore) maybeGCProxyHistoryLocked() {
	if time.Since(s.proxyLastGC) < time.Minute {
		return
	}
	// Most installations have no historical proxy configurations at all.
	if len(s.state.Proxy.TemplateVersions)+len(s.state.Proxy.NodeVersions)+len(s.state.ResinSnapshots) == 0 && len(s.state.Proxy.Changes) <= 100 {
		return
	}
	gcProxyHistory(&s.state)
	s.proxyLastGC = time.Now()
}
