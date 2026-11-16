package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"grok-quality-guard/internal/app/redact"
	proxy "grok-quality-guard/internal/proxy"
)

const (
	maxProxySourceCount = 100000
	maxProxyImportNodes = maxProxySourceCount
	maxProxyImportBytes = 16 << 20
)

func validateSourceMetadata(name string, priority, limit int) error {
	if strings.TrimSpace(name) == "" || len(name) > 256 {
		return proxyError("proxy_name_invalid", 400, "proxy name must contain 1 to 256 bytes")
	}
	if priority < -1000000 || priority > 1000000 || limit < 0 || limit > 1000000 {
		return proxyError("proxy_limits_invalid", 400, "priority or binding limit is outside the supported range")
	}
	return nil
}
func passwordUpdateKeepsExisting(update *passwordUpdate) bool {
	return update != nil && update.Action == "keep" && update.Value == nil
}
func proxyInputEndpoint(input proxyNodeInput) (proxy.Endpoint, error) {
	if input.URL != "" {
		if input.Protocol != "" || input.Host != "" || input.Port != 0 || input.Username != "" || input.Password != "" || (input.PasswordUpdate != nil && !passwordUpdateKeepsExisting(input.PasswordUpdate)) {
			return proxy.Endpoint{}, proxyError("proxy_endpoint_ambiguous", 400, "provide a URL or individual endpoint fields")
		}
		return proxy.ParseEndpoint(input.URL, proxy.ParseOptions{AllowColonFormat: input.ImportFormat == "address_first"})
	}
	endpoint := proxy.Endpoint{Protocol: proxy.Protocol(strings.ToLower(strings.TrimSpace(input.Protocol))), Host: strings.TrimSpace(input.Host), Port: input.Port, Username: input.Username, Password: input.Password, HasUser: input.Username != "", HasPassword: input.HasPassword || input.Password != ""}
	if input.PasswordUpdate != nil {
		if err := applyPasswordUpdate(&endpoint, input.PasswordUpdate); err != nil {
			return proxy.Endpoint{}, err
		}
	}
	if err := endpoint.Validate(); err != nil {
		return proxy.Endpoint{}, err
	}
	return proxy.ParseEndpoint(endpoint.CanonicalURL(), proxy.ParseOptions{})
}
func applyPasswordUpdate(endpoint *proxy.Endpoint, update *passwordUpdate) error {
	if update == nil {
		return nil
	}
	switch update.Action {
	case "keep":
		if update.Value != nil {
			return proxyError("password_update_invalid", 400, "keep does not accept a password value")
		}
	case "clear":
		if update.Value != nil {
			return proxyError("password_update_invalid", 400, "clear does not accept a password value")
		}
		endpoint.Password = ""
		endpoint.HasPassword = false
	case "set":
		if update.Value == nil {
			return proxyError("password_update_invalid", 400, "set requires a password value")
		}
		endpoint.Password = *update.Value
		endpoint.HasPassword = true
		endpoint.HasUser = endpoint.Username != ""
	default:
		return proxyError("password_update_invalid", 400, "password action must be keep, set, or clear")
	}
	return nil
}
func proxyNodeName(input proxyNodeInput, endpoint proxy.Endpoint) string {
	if strings.TrimSpace(input.Name) != "" {
		return strings.TrimSpace(input.Name)
	}
	return string(endpoint.Protocol) + "://" + net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
}
func (s *Service) createProxyNode(input proxyNodeInput) (proxy.Node, error) {
	endpoint, err := proxyInputEndpoint(input)
	if err != nil {
		return proxy.Node{}, err
	}
	now := time.Now().UTC()
	node := proxy.Node{ID: newProxyID("node"), Name: proxyNodeName(input, endpoint), Endpoint: endpoint, Enabled: input.Enabled == nil || *input.Enabled, Priority: input.Priority, MaxBindings: input.MaxBindings, Health: proxy.HealthUnknown, ConfigVersion: 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := validateSourceMetadata(node.Name, node.Priority, node.MaxBindings); err != nil {
		return proxy.Node{}, err
	}
	err = s.env.ProxyStore().Update(func(state *State) error {
		for _, existing := range state.Proxy.Nodes {
			if existing.Endpoint.Fingerprint() == node.Endpoint.Fingerprint() {
				return proxyError("proxy_duplicate", 409, "proxy node already exists")
			}
		}
		if len(state.Proxy.Nodes)+len(state.Proxy.Templates) >= maxProxySourceCount {
			return proxyError("proxy_source_limit", 400, "maximum proxy source count reached")
		}
		state.Proxy.Nodes = append(state.Proxy.Nodes, node)
		s.env.ProxyRecordEvent(state, "", "", "proxy_node_create", "created", "source="+node.ID)
		return nil
	})
	if err == nil {
		s.wakeProxyAutoBind()
	}
	return node, err
}
func (s *Service) importProxyNodes(inputs []proxyNodeInput, text string) (map[string]any, error) {
	inputBytes := len(text)
	for _, input := range inputs {
		inputBytes += len(input.URL) + len(input.Name) + len(input.Host) + len(input.Username) + len(input.Password)
		if input.PasswordUpdate != nil && input.PasswordUpdate.Value != nil {
			inputBytes += len(*input.PasswordUpdate.Value)
		}
		if inputBytes > maxProxyImportBytes {
			return nil, proxyError("proxy_import_limit", 400, "proxy import exceeds 16 MiB")
		}
	}
	if len(text) > maxProxyImportBytes || len(inputs) > maxProxyImportNodes {
		return nil, proxyError("proxy_import_limit", 400, "proxy import exceeds 16 MiB or 100000 nodes")
	}
	type importItem struct {
		Input proxyNodeInput
		Line  int
	}
	items := make([]importItem, 0, len(inputs))
	for i, input := range inputs {
		items = append(items, importItem{input, i + 1})
	}
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			items = append(items, importItem{proxyNodeInput{URL: strings.TrimSpace(line)}, i + 1})
		}
	}
	if len(items) > maxProxyImportNodes {
		return nil, proxyError("proxy_import_limit", 400, "proxy import exceeds 100000 nodes")
	}
	created := make([]proxy.Node, 0, len(items))
	details := make([]map[string]any, 0)
	skipped, invalid, duplicate := 0, 0, 0
	seen := make(map[string]struct{})
	for _, node := range s.proxyCatalog().Sources {
		if node.Node != nil {
			seen[node.Node.Endpoint.Fingerprint()] = struct{}{}
		}
	}
	for _, item := range items {
		endpoint, err := proxyInputEndpoint(item.Input)
		if err != nil {
			code := "invalid"
			if errors.Is(err, proxy.ErrMissingScheme) {
				skipped++
				code = "missing_scheme"
			} else {
				invalid++
			}
			details = append(details, map[string]any{"line": item.Line, "code": code, "error": redact.SanitizeError(err)})
			continue
		}
		if _, ok := seen[endpoint.Fingerprint()]; ok {
			duplicate++
			details = append(details, map[string]any{"line": item.Line, "code": "duplicate"})
			continue
		}
		name := proxyNodeName(item.Input, endpoint)
		if err := validateSourceMetadata(name, item.Input.Priority, item.Input.MaxBindings); err != nil {
			invalid++
			details = append(details, map[string]any{"line": item.Line, "code": "invalid", "error": err.Error()})
			continue
		}
		now := time.Now().UTC()
		node := proxy.Node{ID: newProxyID("node"), Name: name, Endpoint: endpoint, Enabled: item.Input.Enabled == nil || *item.Input.Enabled, Priority: item.Input.Priority, MaxBindings: item.Input.MaxBindings, Health: proxy.HealthUnknown, ConfigVersion: 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
		created = append(created, node)
		seen[endpoint.Fingerprint()] = struct{}{}
	}
	if len(created) > 0 {
		if err := s.env.ProxyStore().Update(func(state *State) error {
			if len(state.Proxy.Nodes)+len(state.Proxy.Templates)+len(created) > maxProxySourceCount {
				return proxyError("proxy_source_limit", 400, "maximum proxy source count reached")
			}
			current := make(map[string]struct{}, len(state.Proxy.Nodes))
			for _, node := range state.Proxy.Nodes {
				current[node.Endpoint.Fingerprint()] = struct{}{}
			}
			for _, node := range created {
				if _, ok := current[node.Endpoint.Fingerprint()]; ok {
					return proxyError("proxy_duplicate", 409, "source list changed during import; retry the import")
				}
			}
			state.Proxy.Nodes = append(state.Proxy.Nodes, created...)
			s.env.ProxyRecordEvent(state, "", "", "proxy_nodes_import", "created", fmt.Sprintf("count=%d", len(created)))
			return nil
		}); err != nil {
			return nil, err
		}
		s.wakeProxyAutoBind()
	}
	return map[string]any{"items": proxyNodesView(created), "created": len(created), "imported": len(created), "skipped": skipped, "invalid": invalid, "duplicate": duplicate, "skipped_items": details}, nil
}

func (s *Service) updateProxyNode(input proxyNodeInput) (proxy.Node, map[string]any, error) {
	input.RetryExistingChange = true
	return s.updateProxyNodeContext(context.Background(), input)
}
func (s *Service) updateProxyNodeContext(ctx context.Context, input proxyNodeInput) (proxy.Node, map[string]any, error) {
	s.mgr.changeMu.Lock()
	current, ok := s.proxyExistingNode(input.ID)
	if !ok {
		s.mgr.changeMu.Unlock()
		return proxy.Node{}, nil, proxyError("proxy_source_not_found", 404, "proxy node not found")
	}
	if input.ExpectedVersion > 0 && input.ExpectedVersion != current.ConfigVersion {
		s.mgr.changeMu.Unlock()
		return proxy.Node{}, nil, proxyError("proxy_source_version_conflict", 409, "node configuration changed; reload before saving")
	}
	next := current
	changedEndpoint := false
	syncRequested := input.URL != "" || input.Protocol != "" || input.Host != "" || input.Port != 0 || input.UsernameSet || input.PasswordUpdate != nil
	if syncRequested {
		var endpoint proxy.Endpoint
		var err error
		if input.URL != "" {
			endpoint, err = proxyInputEndpoint(input)
		} else {
			endpoint = current.Endpoint
			if input.Protocol != "" {
				endpoint.Protocol = proxy.Protocol(strings.ToLower(input.Protocol))
			}
			if input.Host != "" {
				endpoint.Host = input.Host
			}
			if input.Port != 0 {
				endpoint.Port = input.Port
			}
			if input.UsernameSet || input.Username != "" {
				endpoint.Username = input.Username
				endpoint.HasUser = input.Username != ""
			}
			err = applyPasswordUpdate(&endpoint, input.PasswordUpdate)
			if err == nil {
				endpoint, err = endpoint.Normalize()
			}
		}
		if err != nil {
			s.mgr.changeMu.Unlock()
			return proxy.Node{}, nil, err
		}
		changedEndpoint = endpoint.Fingerprint() != current.Endpoint.Fingerprint()
		next.Endpoint = endpoint
	}
	if input.Name != "" {
		next.Name = strings.TrimSpace(input.Name)
	}
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if input.PrioritySet || input.Priority != 0 {
		next.Priority = input.Priority
	}
	if input.MaxBindingsSet || input.MaxBindings != 0 {
		next.MaxBindings = input.MaxBindings
	}
	if err := validateSourceMetadata(next.Name, next.Priority, next.MaxBindings); err != nil {
		s.mgr.changeMu.Unlock()
		return proxy.Node{}, nil, err
	}
	next.ConfigVersion++
	next.UpdatedAt = time.Now().UTC()
	if changedEndpoint {
		next.Revision++
		next.Health = proxy.HealthUnknown
		next.HealthChecked = time.Time{}
	}
	var changeID string
	err := s.env.ProxyStore().Update(func(state *State) error {
		for i, node := range state.Proxy.Nodes {
			if node.ID != input.ID {
				continue
			}
			if node.ConfigVersion != current.ConfigVersion {
				return proxyError("proxy_source_version_conflict", 409, "node configuration changed before save")
			}
			for _, other := range state.Proxy.Nodes {
				if other.ID != next.ID && other.Endpoint.Fingerprint() == next.Endpoint.Fingerprint() {
					return proxyError("proxy_duplicate", 409, "another node uses this endpoint")
				}
			}
			runtime := state.Proxy.SourceRuntime[next.ID]
			if input.RetryExistingChange && syncRequested && !changedEndpoint && runtime.ChangeID != "" {
				for _, change := range state.Proxy.Changes {
					if change.ID == runtime.ChangeID {
						if change.Kind != "node_sync" {
							return proxyError("proxy_source_busy", 409, "another source change is in progress")
						}
						changeID = runtime.ChangeID
						break
					}
				}
			}
			if changedEndpoint && runtime.ChangeID != "" {
				return proxyError("proxy_source_busy", 409, "complete the previous node synchronization first")
			}
			if changedEndpoint && hasPendingSourceOperation(*state, next.ID) {
				return proxyError("proxy_source_busy", 409, "an auth operation is using this node")
			}
			if changedEndpoint {
				state.Proxy.NodeVersions = append(state.Proxy.NodeVersions, current)
			}
			state.Proxy.Nodes[i] = next
			if changedEndpoint {
				ids := proxyBindingAuthIDsFromState(*state, next.ID, proxy.SourceNode)
				if len(ids) > 0 {
					changeID = beginProxyChange(state, next.ID, "node_sync", next.Revision, ids, "syncing")
				}
			}
			s.env.ProxyRecordEvent(state, "", "", "proxy_node_update", "saved", "source="+next.ID)
			return nil
		}
		return proxyError("proxy_source_not_found", 404, "proxy node not found")
	})
	s.mgr.changeMu.Unlock()
	if err != nil {
		return proxy.Node{}, nil, err
	}
	result := map[string]any{"source": proxyNodeView(next, s.env.ProxyStore().ProxyCounts()[next.ID]), "saved": true, "endpoint_changed": changedEndpoint, "sync_requested": syncRequested, "change_id": changeID}
	if changeID != "" {
		changeResult, err := s.runProxyChange(ctx, changeID)
		for key, value := range changeResult {
			result[key] = value
		}
		if err != nil {
			return next, result, err
		}
	}
	s.wakeProxyAutoBind()
	return next, result, nil
}

func buildProxyTemplate(input proxyTemplateInput, current *proxy.Template) (proxy.Template, error) {
	var template proxy.Template
	passwordUpdate := input.PasswordUpdate
	if current != nil {
		template = proxy.CloneTemplate(*current)
	}
	if input.URL != "" {
		parsed, err := proxy.ParseTemplateURL(input.URL)
		if err != nil {
			return proxy.Template{}, err
		}
		if parsed.HasPassword {
			// A complete template URL is authoritative for its credentials.
			passwordUpdate = nil
		}
		if current != nil {
			parsed.ID = current.ID
			parsed.Name = current.Name
			parsed.Enabled = current.Enabled
			parsed.Priority = current.Priority
			parsed.MaxBindings = current.MaxBindings
			parsed.UpdatePolicy = current.UpdatePolicy
			parsed.CreatedAt = current.CreatedAt
		}
		template = parsed
	} else if current == nil && input.Host == "" {
		return proxy.Template{}, proxyError("template_url_required", 400, "template URL or endpoint fields are required")
	}
	if input.Protocol != "" {
		template.Protocol = proxy.Protocol(strings.ToLower(input.Protocol))
	}
	if input.Host != "" {
		template.Host = input.Host
	}
	if input.Port != 0 {
		template.Port = input.Port
	}
	if input.UsernamePattern != nil {
		template.UsernamePattern = *input.UsernamePattern
		template.UsernamePatternEncoding = "literal"
		if input.UsernamePatternEncoding != "" {
			template.UsernamePatternEncoding = input.UsernamePatternEncoding
		} else if current != nil && *input.UsernamePattern == current.UsernamePattern {
			template.UsernamePatternEncoding = current.UsernamePatternEncoding
		}
		template.HasUser = template.UsernamePattern != ""
	}
	if passwordUpdate != nil {
		endpoint := proxy.Endpoint{Username: "configured", HasUser: true, Password: template.Password, HasPassword: template.HasPassword}
		if err := applyPasswordUpdate(&endpoint, passwordUpdate); err != nil {
			return proxy.Template{}, err
		}
		template.Password = endpoint.Password
		template.HasPassword = endpoint.HasPassword
	}
	if input.Name != "" {
		template.Name = strings.TrimSpace(input.Name)
	}
	if template.Name == "" {
		template.Name = string(template.Protocol) + "://" + net.JoinHostPort(template.Host, strconv.Itoa(template.Port))
	}
	if input.Placeholders != nil {
		template.Placeholders = input.Placeholders
	}
	if input.ValueGroups != nil {
		template.ValueGroups = input.ValueGroups
	}
	if input.Enabled != nil {
		template.Enabled = *input.Enabled
	}
	if current == nil && input.Enabled == nil {
		template.Enabled = true
	}
	if input.PrioritySet || input.Priority != 0 {
		template.Priority = input.Priority
	}
	if input.MaxBindingsSet || input.MaxBindings != 0 {
		template.MaxBindings = input.MaxBindings
	}
	if input.UpdatePolicy != "" {
		template.UpdatePolicy = input.UpdatePolicy
	}
	if template.UpdatePolicy == "" {
		template.UpdatePolicy = proxy.TemplateUpdateKeepBound
	}
	if err := validateSourceMetadata(template.Name, template.Priority, template.MaxBindings); err != nil {
		return proxy.Template{}, err
	}
	if template.UpdatePolicy != proxy.TemplateUpdateKeepBound && template.UpdatePolicy != proxy.TemplateUpdateRerenderBound {
		return proxy.Template{}, proxyError("template_update_policy_invalid", 400, "template update policy must be keep_bound or rerender_bound")
	}
	compiled, err := proxy.CompileTemplate(template)
	if err != nil {
		return proxy.Template{}, err
	}
	template = compiled.NormalizedTemplate()
	now := time.Now().UTC()
	if current != nil {
		template.ID = current.ID
		template.CreatedAt = current.CreatedAt
		template.ConfigVersion = current.ConfigVersion + 1
		template.Revision = current.Revision
		if !templateGeneratedConfigEqual(template, *current) {
			template.Revision++
		}
	} else {
		template.ConfigVersion = 1
		template.Revision = 1
		template.CreatedAt = now
	}
	template.ValidatedAt = now
	template.UpdatedAt = now
	return template, nil
}
func templateGeneratedConfigEqual(left, right proxy.Template) bool {
	left.CreatedAt, right.CreatedAt = time.Time{}, time.Time{}
	left.UpdatedAt, right.UpdatedAt = time.Time{}, time.Time{}
	left.ValidatedAt, right.ValidatedAt = time.Time{}, time.Time{}
	left.Name, right.Name = "", ""
	left.Enabled, right.Enabled = false, false
	left.Priority, right.Priority = 0, 0
	left.MaxBindings, right.MaxBindings = 0, 0
	left.UpdatePolicy, right.UpdatePolicy = "", ""
	left.ConfigVersion, right.ConfigVersion = 0, 0
	left.Revision, right.Revision = 0, 0
	return reflect.DeepEqual(left, right)
}
func (s *Service) createProxyTemplate(input proxyTemplateInput) (proxy.Template, error) {
	template, err := buildProxyTemplate(input, nil)
	if err != nil {
		return proxy.Template{}, err
	}
	template.ID = newProxyID("template")
	err = s.env.ProxyStore().Update(func(state *State) error {
		if len(state.Proxy.Nodes)+len(state.Proxy.Templates) >= maxProxySourceCount {
			return proxyError("proxy_source_limit", 400, "maximum proxy source count reached")
		}
		state.Proxy.Templates = append(state.Proxy.Templates, template)
		s.env.ProxyRecordEvent(state, "", "", "proxy_template_create", "created", "source="+template.ID)
		return nil
	})
	if err == nil {
		s.wakeProxyAutoBind()
	}
	return template, err
}
func (s *Service) updateProxyTemplate(input proxyTemplateInput) (proxy.Template, map[string]any, error) {
	return s.updateProxyTemplateContext(context.Background(), input)
}
func (s *Service) updateProxyTemplateContext(ctx context.Context, input proxyTemplateInput) (proxy.Template, map[string]any, error) {
	s.mgr.changeMu.Lock()
	current, ok := s.proxyExistingTemplate(input.ID)
	if !ok {
		s.mgr.changeMu.Unlock()
		return proxy.Template{}, nil, proxyError("proxy_source_not_found", 404, "proxy template not found")
	}
	if input.ExpectedVersion > 0 && input.ExpectedVersion != current.ConfigVersion {
		s.mgr.changeMu.Unlock()
		return proxy.Template{}, nil, proxyError("proxy_source_version_conflict", 409, "template configuration changed; reload before saving")
	}
	next, err := buildProxyTemplate(input, current)
	if err != nil {
		s.mgr.changeMu.Unlock()
		return proxy.Template{}, nil, err
	}
	rerender := next.UpdatePolicy == proxy.TemplateUpdateRerenderBound && next.Revision != current.Revision
	var changeID string
	boundCount := 0
	err = s.env.ProxyStore().Update(func(state *State) error {
		for i, template := range state.Proxy.Templates {
			if template.ID != next.ID {
				continue
			}
			if template.ConfigVersion != current.ConfigVersion {
				return proxyError("proxy_source_version_conflict", 409, "template configuration changed before save")
			}
			if next.Revision != current.Revision && state.Proxy.SourceRuntime[next.ID].ChangeID != "" {
				return proxyError("proxy_source_busy", 409, "complete the previous template application first")
			}
			if next.Revision != current.Revision && hasPendingSourceOperation(*state, next.ID) {
				return proxyError("proxy_source_busy", 409, "an auth operation is using this template")
			}
			if next.Revision != current.Revision {
				state.Proxy.TemplateVersions = append(state.Proxy.TemplateVersions, *current)
			}
			state.Proxy.Templates[i] = next
			ids := proxyBindingAuthIDsFromState(*state, next.ID, proxy.SourceTemplate)
			boundCount = len(ids)
			if rerender && len(ids) > 0 {
				changeID = beginProxyChange(state, next.ID, "template_rerender", next.Revision, ids, "syncing")
			}
			s.env.ProxyRecordEvent(state, "", "", "proxy_template_update", "saved", "source="+next.ID)
			return nil
		}
		return proxyError("proxy_source_not_found", 404, "proxy template not found")
	})
	s.mgr.changeMu.Unlock()
	if err != nil {
		return proxy.Template{}, nil, err
	}
	result := map[string]any{"source": proxyTemplateView(next, boundCount), "saved": true, "bound_count": boundCount, "rerender": rerender, "change_id": changeID, "previous_revision": current.Revision, "revision": next.Revision}
	if changeID != "" {
		changeResult, err := s.runProxyChange(ctx, changeID)
		for k, v := range changeResult {
			result[k] = v
		}
		if err != nil {
			return next, result, err
		}
	}
	s.wakeProxyAutoBind()
	return next, result, nil
}

func beginProxyChange(state *State, sourceID, kind string, revision uint64, ids []string, lifecycle string) string {
	if state.Proxy.SourceRuntime == nil {
		state.Proxy.SourceRuntime = make(map[string]proxy.SourceRuntime)
	}
	id := newProxyID("change")
	items := make([]proxy.ChangeItem, 0, len(ids))
	for _, authID := range ids {
		if account := state.Accounts[authID]; account != nil {
			binding, _ := bindingForAccount(*account)
			items = append(items, proxy.ChangeItem{AuthID: authID, Generation: binding.Generation, EndpointHash: binding.EndpointHash, Status: "pending"})
		}
	}
	change := proxy.SourceChange{ID: id, SourceID: sourceID, Kind: kind, TargetRevision: revision, Status: "pending", AuthIDs: ids, Items: items, Counts: proxy.ChangeCounts{Total: len(items), Pending: len(items)}, CreatedAt: time.Now().UTC()}
	state.Proxy.Changes = append(state.Proxy.Changes, change)
	runtime := state.Proxy.SourceRuntime[sourceID]
	runtime.Lifecycle = lifecycle
	runtime.ChangeID = id
	state.Proxy.SourceRuntime[sourceID] = runtime
	return id
}
func hasPendingSourceOperation(state State, sourceID string) bool {
	for _, operation := range state.Proxy.Operations {
		if (operation.Status == "prepared" || operation.Status == "needs_reconcile") && (operation.Binding.SourceID == sourceID || operation.PreviousSourceID == sourceID) {
			return true
		}
	}
	return false
}
func proxyBindingAuthIDsFromState(state State, sourceID string, kind proxy.SourceKind) []string {
	ids := make([]string, 0)
	for id, account := range state.Accounts {
		if account == nil {
			continue
		}
		binding, ok := bindingForAccount(*account)
		if ok && binding.SourceID == sourceID && binding.SourceKind == kind {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (s *Service) deleteProxyNode(id string, force bool) (map[string]any, error) {
	return s.deleteProxySource(context.Background(), id, force)
}
func (s *Service) deleteProxyTemplate(id string, force bool) (map[string]any, error) {
	return s.deleteProxySource(context.Background(), id, force)
}
func (s *Service) deleteProxySource(ctx context.Context, id string, force bool) (map[string]any, error) {
	s.mgr.changeMu.Lock()
	if s.proxyCatalog().ByID[id] == nil {
		s.mgr.changeMu.Unlock()
		return nil, proxyError("proxy_source_not_found", 404, "proxy source not found")
	}
	ids := s.env.ProxyStore().ProxyAuthIDs(id)
	if len(ids) > 0 && !force {
		s.mgr.changeMu.Unlock()
		return nil, proxyError("proxy_source_in_use", 409, fmt.Sprintf("source has %d bindings; confirmation is required", len(ids)))
	}
	var changeID string
	err := s.env.ProxyStore().Update(func(state *State) error {
		runtime := state.Proxy.SourceRuntime[id]
		if runtime.ChangeID != "" {
			for _, change := range state.Proxy.Changes {
				if change.ID == runtime.ChangeID && change.Kind == "source_delete" {
					changeID = change.ID
					return nil
				}
			}
			return proxyError("proxy_source_busy", 409, "another source change is in progress")
		}
		if hasPendingSourceOperation(*state, id) {
			return proxyError("proxy_source_busy", 409, "an auth operation is using this source")
		}
		changeID = beginProxyChange(state, id, "source_delete", 0, ids, "deleting")
		return nil
	})
	s.mgr.changeMu.Unlock()
	if err != nil {
		return nil, err
	}
	result, err := s.runProxyChange(ctx, changeID)
	if err != nil {
		return result, err
	}
	if result["status"] != "succeeded" {
		return result, proxyError("proxy_source_cleanup_failed", 409, "source retained because one or more auth proxy changes failed")
	}
	result["deleted"] = true
	result["source_id"] = id
	return result, nil
}
