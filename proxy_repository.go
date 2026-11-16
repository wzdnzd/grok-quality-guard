package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	proxy "grok-quality-guard/internal/proxy"
)

// Proxy commits use a small fsynced journal so a batch does not clone and encode
// every quality account twice per auth. The normal state checkpoint includes
// the sequence number, making replay idempotent if a crash precedes truncation.
const maxProxyJournalRecord = 4 << 20

type proxyAccountCommit struct {
	AuthID   string                     `json:"auth_id"`
	Binding  proxy.Binding              `json:"binding"`
	AutoBind proxy.AutoBindAccountState `json:"auto_bind"`
	Resin    ResinState                 `json:"resin"`
}

type proxyCommit struct {
	Sequence         uint64                         `json:"sequence"`
	Account          *proxyAccountCommit            `json:"account,omitempty"`
	Operation        *proxy.Operation               `json:"operation,omitempty"`
	DeleteOperations []string                       `json:"delete_operations,omitempty"`
	Cleanup          []ResinLeaseDelete             `json:"cleanup,omitempty"`
	Tokens           map[string]string              `json:"tokens,omitempty"`
	Snapshots        map[string]ResinSettings       `json:"snapshots,omitempty"`
	Runtimes         map[string]proxy.SourceRuntime `json:"runtimes,omitempty"`
	ChangeID         string                         `json:"change_id,omitempty"`
	ChangeItem       *proxy.ChangeItem              `json:"change_item,omitempty"`
	Event            *Event                         `json:"event,omitempty"`
}

type proxyJournalRecord struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"checksum"`
}

func (s *StateStore) CommitProxy(commit proxyCommit) error {
	if s == nil {
		return errors.New("state store is not initialized")
	}
	s.mu.Lock()
	commit.Sequence = s.state.Proxy.JournalSequence + 1
	if err := s.appendProxyJournalLocked(commit); err != nil {
		s.mu.Unlock()
		return err
	}
	s.applyProxyCommitLocked(commit)
	s.dirty = true
	s.mu.Unlock()
	s.scheduleFlush()
	return nil
}

func (s *StateStore) appendProxyJournalLocked(commit proxyCommit) error {
	if s.path == "" {
		return errors.New("state path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create proxy journal directory: %w", err)
	}
	payload, err := json.Marshal(commit)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	data, err := json.Marshal(proxyJournalRecord{Payload: payload, Checksum: hex.EncodeToString(sum[:])})
	if err != nil {
		return err
	}
	if len(data) > maxProxyJournalRecord {
		return errors.New("proxy operation exceeds journal record limit")
	}
	data = append(data, '\n')
	file, err := os.OpenFile(s.path+".proxy-journal", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open proxy journal: %w", err)
	}
	defer file.Close()
	if err = file.Chmod(0o600); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	n, err := file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = file.Sync()
	}
	if err != nil {
		// A failed prepare never permits an auth write; remove its incomplete tail.
		rollback := file.Truncate(info.Size())
		if rollback == nil {
			rollback = file.Sync()
		}
		return errors.Join(fmt.Errorf("persist proxy operation: %w", err), rollback)
	}
	return nil
}

func (s *StateStore) replayProxyJournalLocked() error {
	file, err := os.OpenFile(s.path+".proxy-journal", os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open proxy journal for recovery: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 64<<10)
	var offset int64
	for {
		var line []byte
		var readErr error
		for {
			fragment, errRead := reader.ReadSlice('\n')
			if len(line)+len(fragment) > maxProxyJournalRecord {
				return errors.New("proxy journal record is too large")
			}
			line = append(line, fragment...)
			if errRead != bufio.ErrBufferFull {
				readErr = errRead
				break
			}
		}
		if readErr == io.EOF {
			if len(line) > 0 {
				if err := file.Truncate(offset); err != nil {
					return err
				}
				if err := file.Sync(); err != nil {
					return err
				}
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
		offset += int64(len(line))
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record proxyJournalRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return fmt.Errorf("invalid proxy journal: %w", err)
		}
		sum := sha256.Sum256(record.Payload)
		if record.Checksum != hex.EncodeToString(sum[:]) {
			return errors.New("proxy journal checksum mismatch")
		}
		var commit proxyCommit
		if err := json.Unmarshal(record.Payload, &commit); err != nil {
			return err
		}
		if commit.Sequence <= s.state.Proxy.JournalSequence {
			continue
		}
		if commit.Sequence != s.state.Proxy.JournalSequence+1 {
			return errors.New("proxy journal sequence gap")
		}
		s.applyProxyCommitLocked(commit)
		s.dirty = true
	}
}

func (s *StateStore) checkpointProxyJournalLocked() {
	path := s.path + ".proxy-journal"
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err == nil {
		_ = file.Sync()
		_ = file.Close()
	}
}

func (s *StateStore) applyProxyCommitLocked(commit proxyCommit) {
	state := &s.state
	if state.Accounts == nil {
		state.Accounts = make(map[string]*AccountState)
	}
	if commit.Account != nil {
		delta := commit.Account
		account := state.Accounts[delta.AuthID]
		if account == nil {
			account = &AccountState{AuthID: delta.AuthID, State: StateUnknown, FirstSeenAt: time.Now().UTC()}
			state.Accounts[delta.AuthID] = account
		}
		before := *account
		account.Proxy = proxy.CloneBinding(delta.Binding)
		account.AutoBind = delta.AutoBind
		account.Resin = delta.Resin
		s.indexProxyAccountLocked(delta.AuthID, &before, account)
	}
	if commit.Operation != nil {
		if state.Proxy.Operations == nil {
			state.Proxy.Operations = make(map[string]proxy.Operation)
		}
		state.Proxy.Operations[commit.Operation.ID] = proxy.CloneOperation(*commit.Operation)
		s.indexProxyOperationLocked(*commit.Operation)
	}
	for _, id := range commit.DeleteOperations {
		if operation, ok := state.Proxy.Operations[id]; ok {
			operation.Status = "removed"
			s.indexProxyOperationLocked(operation)
			if s.proxyPending[operation.AuthID] == id {
				delete(s.proxyPending, operation.AuthID)
			}
			delete(state.Proxy.Operations, id)
		}
	}
	for id, token := range commit.Tokens {
		if state.ResinAdminTokens == nil {
			state.ResinAdminTokens = make(map[string]string)
		}
		state.ResinAdminTokens[id] = token
	}
	for id, settings := range commit.Snapshots {
		if state.ResinSnapshots == nil {
			state.ResinSnapshots = make(map[string]ResinSettings)
		}
		state.ResinSnapshots[id] = settings
	}
	for id, runtime := range commit.Runtimes {
		if state.Proxy.SourceRuntime == nil {
			state.Proxy.SourceRuntime = make(map[string]proxy.SourceRuntime)
		}
		state.Proxy.SourceRuntime[id] = runtime
		s.proxySourcesEpoch++
	}
	for _, job := range commit.Cleanup {
		key := resinLeaseDeleteKey(job.AdminURL, job.PlatformID, job.Account)
		if s.proxyCleanupIndex == nil {
			s.proxyCleanupIndex = make(map[string]int)
			for i, previous := range state.ResinLeaseDeletes {
				s.proxyCleanupIndex[resinLeaseDeleteKey(previous.AdminURL, previous.PlatformID, previous.Account)] = i
			}
		}
		if _, exists := s.proxyCleanupIndex[key]; !exists {
			s.proxyCleanupIndex[key] = len(state.ResinLeaseDeletes)
			s.proxyLeaseAccounts[job.Account]++
			state.ResinLeaseDeletes = append(state.ResinLeaseDeletes, job)
		}
	}
	if commit.ChangeItem != nil {
		for i := range state.Proxy.Changes {
			change := &state.Proxy.Changes[i]
			if change.ID != commit.ChangeID {
				continue
			}
			if j, ok := s.proxyChangeIndex[change.ID][commit.ChangeItem.AuthID]; ok {
				before, after := proxyChangeBucket(change.Items[j].Status), proxyChangeBucket(commit.ChangeItem.Status)
				if before != after {
					if before == "succeeded" {
						change.Counts.Succeeded = max(0, change.Counts.Succeeded-1)
					} else if before == "pending" {
						change.Counts.Pending = max(0, change.Counts.Pending-1)
					} else {
						change.Counts.Failed = max(0, change.Counts.Failed-1)
					}
					if after == "succeeded" {
						change.Counts.Succeeded++
					} else if after == "pending" {
						change.Counts.Pending++
					} else {
						change.Counts.Failed++
					}
				}
				change.Items[j] = *commit.ChangeItem
			}
			break
		}
	}
	if commit.Event != nil {
		appendEvent(state, *commit.Event)
	}
	state.Proxy.JournalSequence = commit.Sequence
	state.UpdatedAt = time.Now().UTC()
}

func proxyChangeBucket(status string) string {
	switch status {
	case "succeeded", "superseded", "skipped":
		return "succeeded"
	case "pending", "":
		return "pending"
	default:
		return "failed"
	}
}

func (s *StateStore) indexProxyAccountLocked(id string, previous, current *AccountState) {
	if s.proxyCounts == nil {
		s.proxyCounts = make(map[string]int)
		s.proxyBySource = make(map[string]map[string]struct{})
	}
	if s.proxyLeaseAccounts == nil {
		s.proxyLeaseAccounts = make(map[string]int)
	}
	oldLease, newLease := "", ""
	if previous != nil {
		oldLease = proxyLeaseIdentity(*previous)
	}
	if current != nil {
		newLease = proxyLeaseIdentity(*current)
	}
	if oldLease != newLease {
		if oldLease != "" {
			s.proxyLeaseAccounts[oldLease]--
			if s.proxyLeaseAccounts[oldLease] <= 0 {
				delete(s.proxyLeaseAccounts, oldLease)
			}
		}
		if newLease != "" {
			s.proxyLeaseAccounts[newLease]++
		}
	}
	oldID, newID := "", ""
	if previous != nil {
		oldID, _ = proxyBindingSource(*previous)
	}
	if current != nil {
		newID, _ = proxyBindingSource(*current)
	}
	if oldID == newID {
		return
	}
	if oldID != "" {
		delete(s.proxyBySource[oldID], id)
		if s.proxyCounts[oldID] > 0 {
			s.proxyCounts[oldID]--
		}
	}
	if newID != "" {
		if s.proxyBySource[newID] == nil {
			s.proxyBySource[newID] = make(map[string]struct{})
		}
		s.proxyBySource[newID][id] = struct{}{}
		s.proxyCounts[newID]++
	}
}

func (s *StateStore) indexProxyOperationLocked(operation proxy.Operation) {
	if s.proxyTerminalOps == nil {
		s.proxyTerminalOps = make(map[string]struct{})
	}
	if operation.ChangeID == "" && (operation.Status == "finalized" || operation.Status == "failed") {
		s.proxyTerminalOps[operation.ID] = struct{}{}
	} else {
		delete(s.proxyTerminalOps, operation.ID)
	}
	if s.proxyPending == nil {
		s.proxyPending = make(map[string]string)
	}
	if s.proxyPendingCounts == nil {
		s.proxyPendingCounts = make(map[string]int)
		s.proxyPendingReserved = make(map[string]string)
		s.proxyOperationLeases = make(map[string]string)
	}
	if sourceID := s.proxyPendingReserved[operation.AuthID]; sourceID != "" {
		s.proxyPendingCounts[sourceID]--
		if s.proxyPendingCounts[sourceID] <= 0 {
			delete(s.proxyPendingCounts, sourceID)
		}
		delete(s.proxyPendingReserved, operation.AuthID)
	}
	if identity := s.proxyOperationLeases[operation.ID]; identity != "" {
		s.proxyLeaseAccounts[identity]--
		if s.proxyLeaseAccounts[identity] <= 0 {
			delete(s.proxyLeaseAccounts, identity)
		}
		delete(s.proxyOperationLeases, operation.ID)
	}
	if operation.Status == "prepared" || operation.Status == "needs_reconcile" {
		currentID := ""
		if account := s.state.Accounts[operation.AuthID]; account != nil {
			currentID, _ = proxyBindingSource(*account)
		}
		if !operation.Clear && operation.Binding.SourceID != "" && operation.Binding.SourceID != currentID {
			s.proxyPendingReserved[operation.AuthID] = operation.Binding.SourceID
			s.proxyPendingCounts[operation.Binding.SourceID]++
		}
		if operation.Binding.Lease != nil && operation.Binding.Lease.Account != "" {
			identity := operation.Binding.Lease.Account
			s.proxyOperationLeases[operation.ID] = identity
			s.proxyLeaseAccounts[identity]++
		}
	}
	if operation.Status == "prepared" || operation.Status == "needs_reconcile" {
		s.proxyPending[operation.AuthID] = operation.ID
	} else if s.proxyPending[operation.AuthID] == operation.ID {
		delete(s.proxyPending, operation.AuthID)
	}
}

func (s *StateStore) rebuildProxyIndexesLocked() {
	s.proxyCleanupIndex = make(map[string]int, len(s.state.ResinLeaseDeletes))
	s.proxyTerminalOps = make(map[string]struct{})
	s.proxySourcesEpoch++
	s.proxyCounts = make(map[string]int)
	s.proxyBySource = make(map[string]map[string]struct{})
	s.proxyPending = make(map[string]string)
	s.proxyLeaseAccounts = make(map[string]int)
	s.proxyPendingCounts = make(map[string]int)
	s.proxyPendingReserved = make(map[string]string)
	s.proxyOperationLeases = make(map[string]string)
	s.proxyChangeIndex = make(map[string]map[string]int, len(s.state.Proxy.Changes))
	for _, change := range s.state.Proxy.Changes {
		index := make(map[string]int, len(change.Items))
		for j, item := range change.Items {
			index[item.AuthID] = j
		}
		s.proxyChangeIndex[change.ID] = index
	}
	for id, account := range s.state.Accounts {
		s.indexProxyAccountLocked(id, nil, account)
	}
	for _, operation := range s.state.Proxy.Operations {
		s.indexProxyOperationLocked(operation)
	}
	for i, job := range s.state.ResinLeaseDeletes {
		s.proxyCleanupIndex[resinLeaseDeleteKey(job.AdminURL, job.PlatformID, job.Account)] = i
		s.proxyLeaseAccounts[job.Account]++
	}
}

func proxyLeaseIdentity(account AccountState) string {
	if account.Proxy.Lease != nil {
		return account.Proxy.Lease.Account
	}
	return account.Resin.Account
}

func (s *StateStore) ProxyChange(id string) (proxy.SourceChange, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, change := range s.state.Proxy.Changes {
		if change.ID == id {
			change.AuthIDs = append([]string(nil), change.AuthIDs...)
			change.Items = append([]proxy.ChangeItem(nil), change.Items...)
			return change, true
		}
	}
	return proxy.SourceChange{}, false
}

func (s *StateStore) ProxyChangeStatus(id string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, change := range s.state.Proxy.Changes {
		if change.ID == id {
			return change.Status
		}
	}
	return ""
}

func (s *StateStore) ProxySourcesEpoch() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxySourcesEpoch
}

func (s *StateStore) ProxyChanges() []proxy.SourceChange {
	s.mu.RLock()
	defer s.mu.RUnlock()
	changes := make([]proxy.SourceChange, len(s.state.Proxy.Changes))
	for i, change := range s.state.Proxy.Changes {
		changes[i] = change
		changes[i].AuthIDs = append([]string(nil), change.AuthIDs...)
		changes[i].Items = append([]proxy.ChangeItem(nil), change.Items...)
	}
	return changes
}

func (s *StateStore) ProxyTemplateVersion(id string, revision uint64) (proxy.Template, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.state.Proxy.Templates {
		if t.ID == id && (revision == 0 || t.Revision == revision) {
			return proxy.CloneTemplate(t), true
		}
	}
	for _, t := range s.state.Proxy.TemplateVersions {
		if t.ID == id && t.Revision == revision {
			return proxy.CloneTemplate(t), true
		}
	}
	return proxy.Template{}, false
}

func (s *StateStore) ProxyNodeVersion(id string, revision uint64) (proxy.Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, n := range s.state.Proxy.Nodes {
		if n.ID == id && (revision == 0 || n.Revision == revision) {
			return n, true
		}
	}
	for _, n := range s.state.Proxy.NodeVersions {
		if n.ID == id && n.Revision == revision {
			return n, true
		}
	}
	return proxy.Node{}, false
}

func (s *StateStore) PrepareProxy(operation proxy.Operation, automatic bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing := s.proxyPending[operation.AuthID]; existing != "" && existing != operation.ID {
		return proxyError("proxy_reconcile_required", 409, "auth has an unresolved proxy operation")
	}
	if previous, ok := s.state.Proxy.Operations[operation.ID]; ok && (previous.AuthID != operation.AuthID || previous.Action != operation.Action || previous.ExpectedHash != operation.ExpectedHash || previous.Binding.SourceID != operation.Binding.SourceID) {
		return proxyError("proxy_operation_conflict", 409, "operation_id belongs to a different operation")
	}
	account := s.state.Accounts[operation.AuthID]
	if account != nil {
		binding, _ := bindingForAccount(*account)
		if binding.Generation != operation.ExpectedGeneration || binding.SourceID != operation.PreviousSourceID {
			return proxyError("proxy_binding_conflict", 409, "binding changed before proxy prepare")
		}
		if automatic && account.AutoBind.Excluded {
			return proxyError("auto_bind_excluded", 409, "automatic binding is paused for this auth")
		}
	}
	if automatic && !s.state.Proxy.AutoBind.Enabled {
		return proxyError("auto_bind_disabled", 409, "automatic binding is disabled")
	}
	if operation.ChangeID != "" {
		active := false
		for _, change := range s.state.Proxy.Changes {
			if change.ID == operation.ChangeID && change.Status == "running" {
				active = true
				break
			}
		}
		if !active {
			return proxyError("proxy_change_canceled", 409, "source change is no longer active")
		}
	}
	if !operation.Clear && operation.Binding.SourceID != "" {
		sourceID := operation.Binding.SourceID
		if operation.ChangeID != "" {
			active := false
			for _, change := range s.state.Proxy.Changes {
				if change.ID == operation.ChangeID && change.Status == "running" {
					active = true
					break
				}
			}
			if !active || s.state.Proxy.SourceRuntime[sourceID].ChangeID != operation.ChangeID {
				return proxyError("proxy_change_canceled", 409, "source change is no longer active")
			}
		}
		lifecycle := s.state.Proxy.SourceRuntime[sourceID]
		if lifecycle.Lifecycle != "" && lifecycle.Lifecycle != "active" && lifecycle.ChangeID != operation.ChangeID {
			return proxyError("proxy_source_busy", 409, "source change is still in progress")
		}
		found := sourceID == resinSourceID
		enabled, limit := false, 0
		if found {
			enabled = s.state.Proxy.ResinSource.Enabled
			if s.state.Resin.Configured {
				enabled = s.state.Resin.Enabled
			}
			if operation.Binding.SourceRevision != max(1, s.state.Proxy.ResinSource.Revision) {
				return proxyError("proxy_source_version_conflict", 409, "Resin revision changed before prepare")
			}
		}
		for _, node := range s.state.Proxy.Nodes {
			if node.ID == sourceID {
				found = true
				enabled = node.Enabled
				limit = node.MaxBindings
				if node.Revision != operation.Binding.SourceRevision {
					return proxyError("proxy_source_version_conflict", 409, "node revision changed before proxy prepare")
				}
			}
		}
		for _, template := range s.state.Proxy.Templates {
			if template.ID == sourceID {
				found = true
				enabled = template.Enabled
				limit = template.MaxBindings
				if template.Revision != operation.Binding.SourceRevision && operation.ChangeID == "" {
					return proxyError("proxy_source_version_conflict", 409, "template revision changed before proxy prepare")
				}
			}
		}
		if !found {
			return proxyError("proxy_source_not_found", 404, "proxy source no longer exists")
		}
		if !enabled && operation.ChangeID == "" {
			return proxyError("proxy_source_disabled", 409, "source was disabled before proxy prepare")
		}
		if sourceID != operation.PreviousSourceID && limit > 0 {
			load := s.proxyCounts[sourceID] + s.proxyPendingCounts[sourceID]
			if s.proxyPendingReserved[operation.AuthID] == sourceID {
				load--
			}
			if load >= limit {
				return proxyError("proxy_source_capacity", 409, "source binding limit was reached before prepare")
			}
		}
		if automatic {
			config := s.state.Proxy.AutoBind
			if !proxyScopeContains(config.SourceScope, operation.Binding.SourceKind) {
				return proxyError("auto_bind_scope_changed", 409, "automatic source scope changed")
			}
			if len(config.SourceIDs) > 0 {
				selected := false
				for _, id := range config.SourceIDs {
					if id == sourceID {
						selected = true
						break
					}
				}
				if !selected {
					return proxyError("auto_bind_scope_changed", 409, "automatic source selection changed")
				}
			}
		}
	}
	commit := proxyCommit{Sequence: s.state.Proxy.JournalSequence + 1, Operation: &operation}
	if err := s.appendProxyJournalLocked(commit); err != nil {
		return err
	}
	s.applyProxyCommitLocked(commit)
	s.dirty = true
	s.scheduleFlush()
	return nil
}

func (s *StateStore) ProxyOperationsForSource(sourceID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for _, id := range s.proxyPending {
		operation := s.state.Proxy.Operations[id]
		if operation.Binding.SourceID == sourceID || operation.PreviousSourceID == sourceID {
			count++
		}
	}
	return count
}

func (s *StateStore) PrunableProxyOperations() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.proxyTerminalOps) <= 256 {
		return nil
	}
	var oldest proxy.Operation
	for id := range s.proxyTerminalOps {
		operation := s.state.Proxy.Operations[id]
		if operation.ID != "" && (oldest.ID == "" || operation.UpdatedAt.Before(oldest.UpdatedAt)) {
			oldest = operation
		}
	}
	if oldest.ID != "" {
		return []string{oldest.ID}
	}
	return nil
}

// Source projection excludes quality accounts, events, and generated operations.
func (s *StateStore) ProxySourcesSnapshot() proxy.ProxyState {
	if s == nil {
		return proxy.ProxyState{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	projection := s.state.Proxy
	projection.Operations, projection.Changes, projection.TemplateVersions, projection.NodeVersions = nil, nil, nil, nil
	return proxy.CloneProxyState(projection)
}

func (s *StateStore) ProxyCounts() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := make(map[string]int, len(s.proxyCounts))
	for id, count := range s.proxyCounts {
		counts[id] = count
	}
	return counts
}

func (s *StateStore) ProxyAuthIDs(sourceID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.proxyBySource[sourceID]))
	for id := range s.proxyBySource[sourceID] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (s *StateStore) ProxyAccount(authID string) AccountState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	account := s.state.Accounts[authID]
	if account == nil {
		return AccountState{AuthID: authID}
	}
	return AccountState{AuthID: authID, State: account.State, DisableKind: account.DisableKind, Resin: account.Resin, Proxy: proxy.CloneBinding(account.Proxy), AutoBind: account.AutoBind}
}

func (s *StateStore) ProxyAutoBindConfig() proxy.AutoBindConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	config := s.state.Proxy.AutoBind
	config.SourceIDs = append([]string(nil), config.SourceIDs...)
	return config
}

func (s *StateStore) ProxyOperation(id string) (proxy.Operation, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	operation, ok := s.state.Proxy.Operations[id]
	return proxy.CloneOperation(operation), ok
}

func (s *StateStore) ProxyPending(authID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proxyPending[authID]
}

func (s *StateStore) ProxyPendingOperations(limit int) []proxy.Operation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	operations := make([]proxy.Operation, 0, min(limit, len(s.proxyPending)))
	for _, id := range s.proxyPending {
		operation := s.state.Proxy.Operations[id]
		if operation.NextAttemptAt.After(time.Now()) {
			continue
		}
		operations = append(operations, proxy.CloneOperation(operation))
		if len(operations) == limit {
			break
		}
	}
	return operations
}

func (s *StateStore) TakeRecoveredProxyChanges() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.proxyRecoveredChanges
	s.proxyRecoveredChanges = nil
	return ids
}

func (s *StateStore) ProxyResinSource() proxy.ResinSourceState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Proxy.ResinSource
}

func resinSnapshotID(settings ResinSettings) string {
	key := settings.AdminURL + "\x00" + settings.PlatformID + "\x00" + settings.Platform + "\x00" + settings.AdminToken + "\x00" + settings.ProxyTemplate + "\x00" + settings.ProxyToken + "\x00" + fmt.Sprint(settings.LocalOnly)
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func (s *StateStore) ResinSnapshot(id string) (ResinSettings, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	settings, ok := s.state.ResinSnapshots[strings.TrimSpace(id)]
	return settings, ok
}

func (s *StateStore) RecordProxyHealth(id string, revision uint64, health proxy.HealthStatus, checked time.Time, cause error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	currentRevision := uint64(0)
	for _, node := range s.state.Proxy.Nodes {
		if node.ID == id {
			currentRevision = node.Revision
		}
	}
	for _, template := range s.state.Proxy.Templates {
		if template.ID == id {
			currentRevision = template.Revision
		}
	}
	if id == resinSourceID {
		currentRevision = max(1, s.state.Proxy.ResinSource.Revision)
	}
	if currentRevision != revision {
		return nil
	}
	runtime := s.state.Proxy.SourceRuntime[id]
	runtime.Health = health
	runtime.HealthRevision = revision
	runtime.HealthChecked = checked
	if cause != nil {
		runtime.LastError = sanitizeError(cause)
	} else {
		runtime.LastError = ""
		runtime.RetryAt = time.Time{}
	}
	commit := proxyCommit{Sequence: s.state.Proxy.JournalSequence + 1, Runtimes: map[string]proxy.SourceRuntime{id: runtime}}
	if err := s.appendProxyJournalLocked(commit); err != nil {
		return err
	}
	s.applyProxyCommitLocked(commit)
	s.dirty = true
	s.scheduleFlush()
	return nil
}

func (s *StateStore) ProxyChangeSummaries() []proxy.SourceChange {
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]proxy.SourceChange, len(s.state.Proxy.Changes))
	for i, change := range s.state.Proxy.Changes {
		change.AuthIDs = nil
		change.Items = nil
		items[i] = change
	}
	return items
}
