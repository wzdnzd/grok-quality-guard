    function renderProxySources() {
      const result = pagedItems('sources');
      const sources = result.items;
      const all = state.proxySources || [];
      document.getElementById('proxySourceRows').innerHTML = sources.map(proxySourceRow).join('');
      document.getElementById('proxySourcesEmpty').hidden = sources.length > 0;
      const counts = (state.proxy || {}).source_counts || {};
      document.getElementById('proxySourceSummary').textContent = i18n.t('ui.proxy_source_summary', { total: result.total, nodes: Number(counts.node || 0), templates: Number(counts.template || 0), resin: Number(counts.resin || 0) });
      renderPager('sources', result);
      syncProxySourceSelectionUI();
    }
    function setProxySubtab(name) {
      const allowed = ['bindings', 'sources'];
      if (!allowed.includes(name)) name = 'bindings';
      state.proxySubtab = name;
      document.querySelectorAll('[data-proxy-subtab]').forEach((button) => {
        const active = button.dataset.proxySubtab === name;
        button.classList.toggle('active', active);
        button.setAttribute('aria-selected', active ? 'true' : 'false');
        button.tabIndex = active ? 0 : -1;
      });
      document.querySelectorAll('.proxy-subpanel').forEach((panel) => {
        panel.classList.toggle('active', panel.id === 'proxy-subpanel-' + name);
      });
    }
    function proxyCanBind() {
      return Number(state.proxy?.available_source_count ?? state.proxySources.filter((source) => source.enabled && source.valid).length) > 0;
    }
    function proxySourceChoiceLabel(source) {
      const kind = proxySourceLabel(source?.kind);
      const name = String(source?.name || '').trim();
      return name ? kind + i18n.t('ui.meta_separator') + name : kind;
    }
    function proxySourceChoicePool() {
      return new Map(state.proxySources.concat(state.proxySourceChoices).map((source) => [source.id, source]));
    }
    function proxySourceChoiceForInput(value) {
      const text = String(value || '').trim();
      if (!text) return null;
      const folded = text.toLowerCase();
      const choices = Array.from(proxySourceChoicePool().values());
      return choices.find((source) => proxySourceChoiceLabel(source) === text)
        || choices.find((source) => String(source.name || '').trim().toLowerCase() === folded)
        || choices.find((source) => source.id === text)
        || null;
    }
    function applyLeaseSourceFilterInput(value) {
      const selected = proxySourceChoiceForInput(value);
      const sourceID = selected?.id || '';
      if (state.leaseSourceFilter !== sourceID) {
        state.leaseSourceFilter = sourceID;
        state.leasePage = 1;
        scheduleListReload('leases');
      }
      return selected;
    }
    function renderProxySourceChoices() {
      const seen = new Set();
      const options = [];
      for (const source of proxySourceChoicePool().values()) {
        const label = proxySourceChoiceLabel(source);
        if (!label || seen.has(label.toLowerCase())) continue;
        seen.add(label.toLowerCase());
        options.push('<option value="' + esc(label) + '"></option>');
        if (options.length >= 100) break;
      }
      document.getElementById('proxySourceChoices').innerHTML = options.join('');
    }
    function proxySourceKindForScope(scope) {
      if (scope === 'nodes') return 'node';
      if (scope === 'templates') return 'template';
      if (scope === 'resin') return 'resin';
      return '';
    }
    let proxyOperationSourceActiveIndex = -1;
    let proxyOperationSourceVisibleChoices = [];
    function proxyOperationSourcePool() {
      return new Map(state.proxySources.concat(state.proxyOperationSourceChoices).map((source) => [source.id, source]));
    }
    function proxyOperationSourceQuery() {
      const fieldValue = valueOf('proxyOperationSource');
      const selectedID = valueOf('proxyOperationSourceID');
      const selected = proxyOperationSourcePool().get(selectedID);
      if (selected && (proxySourceChoiceLabel(selected) === fieldValue || (selected.name || selected.id) === fieldValue)) return '';
      return fieldValue.toLowerCase();
    }
    function renderProxyOperationSourceChoices(scope) {
      const kind = proxySourceKindForScope(scope);
      const query = proxyOperationSourceQuery();
      const selectedID = valueOf('proxyOperationSourceID');
      const visible = Array.from(proxyOperationSourcePool().values())
        .filter((source) => !kind || source.kind === kind)
        .filter((source) => !query || String(source.name || source.id).toLowerCase().includes(query) || String(source.id || '').toLowerCase().includes(query))
        .slice(0, 100);
      proxyOperationSourceVisibleChoices = visible;
      proxyOperationSourceActiveIndex = Math.max(-1, Math.min(proxyOperationSourceActiveIndex, visible.length - 1));
      const menu = document.getElementById('proxyOperationSourceChoices');
      if (!menu) return visible;
      menu.innerHTML = visible.length
        ? visible.map((source, index) => '<button type="button" class="proxy-source-picker-option' + (index === proxyOperationSourceActiveIndex ? ' active' : '') + '" id="proxyOperationSourceOption-' + index + '" role="option" aria-selected="' + String(source.id === selectedID) + '" data-proxy-source-option="' + esc(source.id) + '">' + esc(proxySourceChoiceLabel(source)) + '</button>').join('')
        : '<div class="proxy-source-picker-empty">' + esc(i18n.t('ui.no_matching_proxy')) + '</div>';
      return visible;
    }
    function setProxyOperationSourceMenuOpen(open) {
      const menu = document.getElementById('proxyOperationSourceChoices');
      const field = document.getElementById('proxyOperationSource');
      if (menu) menu.hidden = !open;
      if (field) field.setAttribute('aria-expanded', String(Boolean(open)));
      document.getElementById('confirmCard')?.classList.toggle('source-picker-open', Boolean(open));
      if (!open) proxyOperationSourceActiveIndex = -1;
    }
    function chooseProxyOperationSource(id) {
      const source = proxyOperationSourcePool().get(id);
      if (!source) return false;
      const field = document.getElementById('proxyOperationSource');
      const hidden = document.getElementById('proxyOperationSourceID');
      if (field) field.value = proxySourceChoiceLabel(source);
      if (hidden) hidden.value = source.id;
      setProxyOperationSourceMenuOpen(false);
      field?.focus();
      return true;
    }
    function proxyOperationSourceValue() {
      const selectedID = valueOf('proxyOperationSourceID');
      if (selectedID) return selectedID;
      const value = valueOf('proxyOperationSource');
      if (!value) return '';
      const match = Array.from(proxyOperationSourcePool().values()).find((source) => source.id === value || source.name === value || proxySourceChoiceLabel(source) === value);
      return match ? match.id : value;
    }
    function moveProxyOperationSourceSelection(direction, scope) {
      const count = proxyOperationSourceVisibleChoices.length;
      if (!count) return;
      const start = proxyOperationSourceActiveIndex < 0 ? (direction > 0 ? -1 : 0) : proxyOperationSourceActiveIndex;
      proxyOperationSourceActiveIndex = (start + direction + count) % count;
      renderProxyOperationSourceChoices(scope);
      const active = document.getElementById('proxyOperationSourceOption-' + proxyOperationSourceActiveIndex);
      if (active && typeof active.scrollIntoView === 'function') active.scrollIntoView({ block: 'nearest' });
    }
    let proxySourceChoiceRequest = 0;
    async function searchProxySourceChoices(query) {
      const request = ++proxySourceChoiceRequest;
      const value = await api('proxy.sources.list', { page: 1, page_size: 50, query: query || '' });
      if (request !== proxySourceChoiceRequest) return;
      state.proxySourceChoices = value?.items || [];
      renderProxySourceChoices();
    }
    let proxyOperationSourceChoiceRequest = 0;
    async function searchProxyOperationSourceChoices(query, scope) {
      const request = ++proxyOperationSourceChoiceRequest;
      const kind = proxySourceKindForScope(scope);
      const params = { page: 1, page_size: 50, query: query || '' };
      if (kind) params.kind = kind;
      const value = await api('proxy.sources.list', params);
      if (request !== proxyOperationSourceChoiceRequest) return;
      state.proxyOperationSourceChoices = value?.items || [];
      renderProxyOperationSourceChoices(scope);
    }
    function renderProxyAutoBind() {
      const summary = state.proxy || {};
      const config = summary.auto_bind || {};
      const runtime = summary.auto_bind_runtime || {};
      const enabled = Boolean(config.enabled);
      const waitingKey = catalogKey('proxy.auto_bind.waiting.', runtime.waiting_reason);
      const waiting = runtime.waiting_reason ? (i18n.t('ui.meta_separator') + (hasCatalogKey(waitingKey) ? i18n.t(waitingKey) : i18n.t('ui.waiting_for_auto_bind_to_continue'))) : '';
      const queue = Number(runtime.queue_depth || 0);
      const inFlight = Number(runtime.in_flight || 0);
      const lastScanAt = runtime.last_scan_at ? formatFullTime(runtime.last_scan_at) : '';
      const stateText = enabled ? (runtime.running ? i18n.t('ui.auto_bind_queue_running') : i18n.t('ui.auto_bind_queue_on')) : i18n.t('ui.auto_bind_queue_off');
      const statusText = i18n.t('ui.auto_bind_status', { state: stateText }) +
        (enabled ? i18n.t('ui.queue_in_flight_status', { queue: queue, running: inFlight, waiting: waiting }) : '') +
        (lastScanAt ? i18n.t('ui.last_scan_status', { time: lastScanAt }) : '');
      const lastError = runtime.last_error ? friendlyApiMessage(runtime.last_error, runtime.last_error_code) : '';
      const errorSuffix = lastError ? i18n.t('ui.latest_error_suffix', { error: lastError }) : '';
      const scanDetails = i18n.t('ui.scan_summary', { time: lastScanAt || i18n.t('ui.not_scanned_yet'), succeeded: Number(runtime.bound_total || 0), failed: Number(runtime.failed_total || 0), skipped: Number(runtime.skipped_total || 0), excluded: Number(runtime.excluded_count || summary.excluded_count || 0), error: errorSuffix });
      const statusEl = document.getElementById('proxyAutoBindText');
      statusEl.textContent = statusText;
      statusEl.title = scanDetails;
      statusEl.setAttribute('aria-label', i18n.t('ui.auto_bind_status_aria', { status: statusText, details: scanDetails }));
      const badgeEl = document.getElementById('proxyAutoBindStateBadge');
      badgeEl.textContent = i18n.t(enabled ? 'ui.auto_bind_queue_on' : 'ui.auto_bind_queue_off');
      badgeEl.className = 'badge ' + (enabled ? 'good' : '');
      document.getElementById('proxyAutoBindRunBtn').disabled = !enabled;
    }
    function populateProxyAutoBindForm() {
      if (isFormDirty('proxyAutoBindForm')) return;
      const config = (state.proxy && state.proxy.auto_bind) || {};
      setChecked('pab_enabled', Boolean(config.enabled));
      setChecked('pab_auto_rotate', Boolean(state.proxy && state.proxy.auto_rotate_after_quality_failure));
      setValue('pab_source_scope', config.source_scope || 'all');
      setValue('pab_source_ids', (config.source_ids || []).join(', '));
      setValue('pab_concurrency', config.concurrency || 2);
      setValue('pab_queue_size', config.queue_size || 1000);
      setValue('pab_max_per_cycle', config.max_per_cycle || 100);
      setValue('pab_scan_interval', config.scan_interval || '30s');
      setValue('pab_health_max_age', config.health_max_age || '10m');
      setChecked('pab_require_healthy', Boolean(config.require_healthy));
      setChecked('pab_include_guard_disabled', Boolean(config.include_guard_disabled));
      setChecked('pab_include_externally_disabled', Boolean(config.include_externally_disabled));
      setValue('pab_manual_clear_policy', config.manual_clear_policy || 'rebind');
      setValue('pab_retry_initial', config.retry_initial || '1m');
      setValue('pab_retry_max', config.retry_max || '1h');
      renderProxyAutoBind();
    }
    function collectProxyAutoBindConfig() {
      return {
        enabled: document.getElementById('pab_enabled').checked,
        source_scope: valueOf('pab_source_scope') || 'all',
        source_ids: (valueOf('pab_source_ids') || '').split(',').map((item) => item.trim()).filter(Boolean),
        concurrency: numberValue('pab_concurrency', 2),
        queue_size: numberValue('pab_queue_size', 1000),
        max_per_cycle: numberValue('pab_max_per_cycle', 100),
        scan_interval: valueOf('pab_scan_interval') || '30s',
        health_max_age: valueOf('pab_health_max_age') || '10m',
        require_healthy: document.getElementById('pab_require_healthy').checked,
        include_guard_disabled: document.getElementById('pab_include_guard_disabled').checked,
        include_externally_disabled: document.getElementById('pab_include_externally_disabled').checked,
        manual_clear_policy: valueOf('pab_manual_clear_policy') || 'rebind',
        retry_initial: valueOf('pab_retry_initial') || '1m',
        retry_max: valueOf('pab_retry_max') || '1h',
        auto_rotate_after_quality_failure: document.getElementById('pab_auto_rotate').checked
      };
    }
    async function saveProxyAutoBind(button) {
      const revision = formDraftRevision('proxyAutoBindForm');
      setBusy(button, true);
      document.getElementById('proxyAutoBindStatus').textContent = i18n.t("ui.saving_auto_bind_settings");
      try {
        const config = collectProxyAutoBindConfig();
        const autoRotate = config.auto_rotate_after_quality_failure;
        delete config.auto_rotate_after_quality_failure;
        const result = await api('proxy.auto_bind.update', { config: config, auto_rotate_after_quality_failure: autoRotate });
        state.proxy = state.proxy || {};
        state.proxy.auto_bind = result.config;
        state.proxy.auto_bind_runtime = result.runtime;
        state.proxy.auto_rotate_after_quality_failure = Boolean(result.auto_rotate_after_quality_failure);
        clearFormDraft('proxyAutoBindForm', revision);
        renderProxyAutoBind();
        document.getElementById('proxyAutoBindStatus').textContent = i18n.t("ui.saved");
        toast(i18n.t("ui.auto_bind_settings_saved"), 'success');
        setProxyAutoBindSettingsOpen(false);
      } catch (error) {
        document.getElementById('proxyAutoBindStatus').textContent = i18n.t('ui.save_failed', { error: error.message });
        toast(error.message, 'error');
      } finally { setBusy(button, false); }
    }
    function proxySyncFailures(result) {
      const success = new Set(['succeeded', 'bound', 'rotated', 'cleared', 'updated', 'created', 'deleted', 'reachable', 'unchanged', 'skipped', 'superseded', 'already_bound', 'queued', 'running', 'pending']);
      const items = result && Array.isArray(result.items) ? result.items : (result && result.auth_id ? [result] : []);
      return items.filter((item) => item && (item.needs_reconcile || (!success.has(item.status) && (item.error || item.status))));
    }
    function proxyResultCounts(result) {
      const value = result || {};
      const counts = value.counts || {};
      const items = Array.isArray(value.items) ? value.items : (value.auth_id ? [value] : []);
      const count = Number(value.count ?? counts.total ?? items.length);
      const complete = items.length >= count && count > 0;
      const failed = Math.max(Number(value.failed || value.failed_count || counts.failed || 0), proxySyncFailures(value).length);
      const skipped = Number(value.skipped ?? (complete ? items.filter((item) => ['skipped', 'superseded', 'unchanged', 'already_bound'].includes(item.status)).length : 0));
      const pending = Number(value.pending ?? counts.pending ?? 0);
      const succeeded = Number(value.succeeded ?? value.success_count ?? (complete ? Math.max(0, count - skipped - failed - pending) : counts.succeeded) ?? Math.max(0, count - skipped - failed - pending));
      return { count: count, succeeded: succeeded, skipped: skipped, failed: failed, pending: pending, completedMode: !complete && counts.succeeded != null && value.skipped == null };
    }
    function proxyResultMessage(result, label) {
      const value = result || {};
      const { count, succeeded, skipped, failed, pending, completedMode } = proxyResultCounts(value);
      let message = label || i18n.t('action.proxy_operation');
      if (value.saved) message += i18n.t("ui.proxy_operation_config_saved_suffix");
      if (count || failed || skipped) message += i18n.t(completedMode ? 'ui.proxy_operation_completed_prefix' : 'ui.proxy_operation_success_prefix', { succeeded: succeeded, skipped: skipped, failed: failed });
      else message += value.saved ? '' : i18n.t('ui.completed');
      if (pending) message += i18n.t('ui.proxy_operation_pending_suffix', { pending: pending });
      if (value.change_id) message += i18n.t("ui.proxy_operation_view_sync_suffix");
      return { message: message, failed: failed };
    }
    function reportProxyResult(result, label) {
      const summary = proxyResultMessage(result, label);
      toast(summary.message, summary.failed ? 'error' : 'success');
      return summary;
    }
    async function openProxyChangeDetails(changeID) {
      const id = String(changeID || '').trim();
      if (!id) return;
      openTaskCenter();
      await refreshTaskCenter(true);
      const queue = (state.taskCenter?.resident_queues || []).find((item) => item.id === 'proxy_changes');
      if (queue) {
        await selectTaskCenterQueue('proxy_changes', true);
        await openTaskCenterChange(id, 1);
        return;
      }
      state.taskCenterSelectedID = '';
      state.taskCenterDetail = null;
      state.taskCenterSelectedQueue = {
        id: 'proxy_changes',
        label: i18n.t("ui.proxy_sync_records"),
        category: 'proxy',
        status: 'idle',
        description: i18n.t('ui.proxy_change_item_results'),
        total: 0,
        completed: 0,
        running: 0,
        waiting: 0,
        succeeded: 0,
        failed: 0,
        skipped: 0,
        synthetic: true
      };
      state.taskCenterQueueDetail = { loading: true, mode: 'change', data: { change_id: id, page: 1, page_size: 20 } };
      state.taskQueuePage = 1;
      renderTaskCenter();
      revealResidentQueueDetail();
      await openTaskCenterChange(id, 1);
    }
    async function showProxyEditorError(error, source, editor) {
      if (state.proxyEditing !== editor) return;
      const status = document.getElementById('proxySourceEditorStatus');

      if (status) status.textContent = i18n.t('ui.save_failed', { error: error.message });
      const path = error.path || error.details?.path || error.details?.errors?.[0]?.path;
      if (path) { focusProxyValidationPath(path); if (status) status.textContent += i18n.t('ui.meta_separator') + path; }
      if (source && (error.status === 409 || error.code === 'proxy_source_version_conflict')) {
        try {
          const latest = await api('proxy.sources.get', { source_id: source.id });
          if (state.proxyEditing !== editor) return;
          const view = latest.view || latest.source || latest || {};
          status.textContent += i18n.t('ui.proxy_source_version_reloaded', { server: view.config_version || '-', draft: source.config_version || '-' });
        } catch (_) { }
      }
      toast(error.message, 'error');
    }
    function focusProxyValidationPath(path) {
      let field;
      const placeholder = String(path).match(/^placeholders\[(\d+)\](.*)$/);
      const relation = String(path).match(/^value_groups\[(\d+)\](?:\.rows\[(\d+)\](?:\[(\d+)\])?)?/);
      if (placeholder) {
        const card = Array.from(document.querySelectorAll('[data-placeholder-card]'))[Number(placeholder[1])];
        if (card && 'open' in card) card.open = true;
        const suffix = placeholder[2];
        field = card?.querySelector(suffix.includes('separator') ? '[data-enum-separator]' : suffix.includes('random.length') ? '[data-random-length]' : suffix.includes('group_id') ? '[data-placeholder-group]' : suffix.includes('enum') ? '[data-enum-input]' : '[data-placeholder-kind]');
      } else if (relation) {
        const group = Array.from(state.proxyEditing?.groups.values() || [])[Number(relation[1])];
        if (group) {
          const row = relation[2] == null ? null : Number(relation[2]);
          if (row != null) { group.view = 'table'; group.page = Math.floor(row / 20) + 1; renderProxyGroup(group.index); }
          const card = document.querySelector('[data-group-card="' + group.index + '"]');
          field = row == null ? card?.querySelector('[data-group-columns]') : card?.querySelector('[data-group-cell-row="' + row + '"]' + (relation[3] != null ? '[data-group-cell-column="' + Number(relation[3]) + '"]' : ''));
        }
      } else if (String(path).startsWith('assignments.')) field = document.querySelector('[data-proxy-assignment="' + CSS.escape(String(path).slice('assignments.'.length)) + '"]');
      else field = document.getElementById('ptpl_url') || document.getElementById('pnode_url');
      if (field) { field.setAttribute('aria-invalid', 'true'); field.focus(); }
    }
    function openProxyDrawer() {
      document.getElementById('proxySourceDrawer').hidden = false;
      document.body.classList.add('drawer-open');
      window.setTimeout(() => document.getElementById('proxySourceEditorClose').focus(), 20);
    }
    async function closeProxyDrawer(force) {
      const editor = state.proxyEditing;
      if (force !== true && editor && editor.dirty && !await confirmAction({ title: i18n.t('ui.close_unsaved_editor_title'), detail: i18n.t('ui.unsaved_editor_detail'), confirmLabel: i18n.t('ui.discard_and_close'), tone: 'danger' })) return false;
      if (editor?.importTimer) window.clearTimeout(editor.importTimer);
      hideHelpTooltip();
      document.getElementById('proxySourceDrawer').hidden = true;
      document.getElementById('proxySourceEditor').innerHTML = '';
      setProxyEditorMeta(null);
      if (document.getElementById('accountDrawer').hidden) document.body.classList.remove('drawer-open');
      state.proxyEditing = null;
      state.proxyPreviewSequence += 1;
      if (editor?.previousFocus?.isConnected) editor.previousFocus.focus();
      return true;
    }
    function proxyEditorField(label, id, value, options) {
      const settings = options || {};
      const attrs = ' id="' + esc(id) + '" type="' + esc(settings.type || 'text') + '"' +
        (settings.placeholder ? ' placeholder="' + esc(settings.placeholder) + '"' : '') +
        (settings.min != null ? ' min="' + esc(settings.min) + '"' : '') +
        (settings.max != null ? ' max="' + esc(settings.max) + '"' : '');
      return '<div class="field' + (settings.full ? ' full' : '') + '">' + proxyFieldLabel(label, id, settings.hint) + '<input' + attrs + ' value="' + esc(value == null ? '' : value) + '"></div>';
    }
    function proxyEditorCheck(label, id, checked, hint) {
      return '<div class="field"><div class="check-row"><label class="check-choice" for="' + esc(id) + '"><input id="' + esc(id) + '" type="checkbox" ' + (checked ? 'checked' : '') + '><span>' + esc(label) + '</span></label>' + helpTip(hint) + '</div></div>';
    }
    function proxyNodeRowHTML(index, node, removable) {
      const value = node || {};
      const suffix = index ? '_' + index : '';
      const nameID = 'pnode_name' + suffix;
      const urlID = 'pnode_url' + suffix;
      return '<div class="proxy-node-row" data-proxy-node-row>' +
        '<div class="proxy-node-row-head"><span class="proxy-node-row-index">' + esc(i18n.t('ui.node')) + '<b data-proxy-node-index>' + String(index + 1).padStart(2, '0') + '</b></span>' +
        (removable ? '<button class="btn btn-ghost icon-btn proxy-node-row-remove" type="button" data-proxy-node-remove aria-label="' + esc(i18n.t('ui.remove_node')) + '">' + icon('x') + '</button>' : '') +
        '</div><div class="proxy-node-row-fields">' +
        '<div class="field proxy-node-name-field">' + proxyFieldLabel(i18n.t('ui.node_name'), nameID) + '<input id="' + nameID + '" data-proxy-node-name type="text" value="' + esc(value.name || '') + '" title="' + esc(value.name || '') + '" placeholder="' + esc(i18n.t('ui.optional_auto_name')) + '"></div>' +
        '<div class="field proxy-node-url-field">' + proxyFieldLabel(i18n.t('ui.proxy_url'), urlID) + '<input id="' + urlID + '" data-proxy-node-url type="text" inputmode="url" value="' + esc(value.edit_url || value.url || '') + '" title="' + esc(value.edit_url || value.url || '') + '" placeholder="http://user:pass@proxy.example:8080" autocomplete="off" spellcheck="false"></div>' +
        '</div></div>';
    }
    function syncProxyNodeRows() {
      const list = document.getElementById('proxyNodeRows');
      if (!list) return;
      const rows = Array.from(list.querySelectorAll('[data-proxy-node-row]'));
      rows.forEach((row, index) => {
        const number = row.querySelector('[data-proxy-node-index]');
        if (number) number.textContent = String(index + 1).padStart(2, '0');
        const remove = row.querySelector('[data-proxy-node-remove]');
        if (remove) remove.hidden = rows.length === 1;
      });
      const count = document.getElementById('proxyNodeRowCount');
      if (count) count.textContent = i18n.t('ui.node_count', { count: rows.length });
      const submitLabel = list.closest('form')?.querySelector('[data-proxy-node-submit-label]');
      if (submitLabel) submitLabel.textContent = rows.length > 1 ? i18n.t('ui.save_nodes_count', { count: rows.length }) : i18n.t('ui.save_node');
    }
    async function openProxyNodeEditor(source) {
      if (!document.getElementById('proxySourceDrawer').hidden && !await closeProxyDrawer()) return;
      const editor = beginProxyEditor('node', source);
      let detail = null;
      if (source) {
        detail = await api('proxy.sources.get', { source_id: source.id, edit_fields: true });
      }
      if (state.proxyEditing !== editor) return;
      if (detail) source = Object.assign({}, source, detail.view || detail.source || detail);
      const node = detail?.node || (detail?.kind === 'node' ? detail : {});
      const editing = Boolean(source);
      editor.nextNodeRow = 1;
      document.getElementById('proxySourceEditorTitle').textContent = i18n.t(editing ? 'ui.proxy_node_edit_title' : 'ui.proxy_node_add_title');
      document.getElementById('proxySourceEditorSubtitle').textContent = i18n.t(editing ? 'ui.proxy_node_edit_subtitle' : 'ui.proxy_node_add_subtitle');
      setProxyEditorMeta(source, i18n.t('ui.sync_bound_accounts_after_save'));
      const addButton = editing ? '' : '<button class="btn btn-xs" type="button" data-proxy-node-add>' + icon('plus') + esc(i18n.t('ui.add_row')) + '</button>';
      const listHelp = i18n.t(editing ? 'ui.proxy_node_protocols_help' : 'ui.proxy_node_add_url_help');
      const settingsHelp = i18n.t(editing ? 'ui.proxy_node_edit_settings_help' : 'ui.proxy_node_add_settings_help');
      document.getElementById('proxySourceEditor').innerHTML = '<form id="proxyNodeEditorForm" class="drawer-form"><section class="card"><div class="form-section">' +
        '<div class="section-heading"><h3 class="section-title">' + esc(i18n.t(editing ? 'ui.connection_info' : 'ui.node_list')) + helpTip(listHelp) + '<span class="section-count" id="proxyNodeRowCount">' + esc(i18n.t('ui.node_count', { count: 1 })) + '</span></h3>' + addButton + '</div>' +
        '<div class="proxy-node-list" id="proxyNodeRows">' + proxyNodeRowHTML(0, node, !editing) + '</div>' +
        '</div></section><section class="card"><div class="form-section"><h3 class="section-title">' + esc(i18n.t('ui.proxy_node_settings')) + helpTip(settingsHelp) + '</h3><div class="field-grid">' +
        proxyEditorCheck(i18n.t('ui.node_enabled'), 'pnode_enabled', editing ? Boolean(source.enabled) : true, i18n.t('ui.node_disabled_help')) +
        proxyEditorField(i18n.t('ui.priority'), 'pnode_priority', node.priority || 0, { type: 'number', min: -1000000, max: 1000000, hint: i18n.t('ui.priority_help') }) +
        proxyEditorField(i18n.t('ui.binding_limit'), 'pnode_max_bindings', node.max_bindings || 0, { type: 'number', min: 0, hint: i18n.t('ui.binding_limit_zero_help') }) +
        '</div></div></section><div class="form-actions"><button class="btn btn-primary" type="submit">' + icon('check') + (editing ? esc(i18n.t('ui.validate_and_save')) : '<span data-proxy-node-submit-label>' + esc(i18n.t('ui.save_node')) + '</span>') + '</button><span class="status-line" id="proxySourceEditorStatus" role="status" aria-live="polite"></span></div></form>';
      const form = document.getElementById('proxyNodeEditorForm');
      syncProxyNodeRows();
      form.addEventListener('input', (event) => {
        if (!event.target.matches?.('[data-proxy-node-name], [data-proxy-node-url]')) return;
        event.target.removeAttribute('aria-invalid');
        event.target.title = event.target.value;
      });
      form.addEventListener('submit', async (event) => {
        event.preventDefault();
        const button = event.submitter || form.querySelector('button[type="submit"]');
        const revision = editor.revision;
        if (editing && !editor.dirty) {
          const message = i18n.t('ui.unchanged_cannot_save');
          document.getElementById('proxySourceEditorStatus').textContent = message;
          toast(message, 'error');
          return;
        }
        const rows = Array.from(form.querySelectorAll('[data-proxy-node-row]'));
        rows.forEach((row) => row.querySelectorAll('input').forEach((input) => input.removeAttribute('aria-invalid')));
        const entries = rows.map((row, index) => {
          const nameField = row.querySelector('[data-proxy-node-name]');
          const urlField = row.querySelector('[data-proxy-node-url]');
          return { row, index, nameField, urlField, name: String(nameField?.value || '').trim(), url: String(urlField?.value || '').trim() };
        }).filter((entry) => entry.name || entry.url);
        const status = document.getElementById('proxySourceEditorStatus');
        if (!entries.length) {
          const firstURL = form.querySelector('[data-proxy-node-url]');
          firstURL?.setAttribute('aria-invalid', 'true');
          firstURL?.focus();
          status.textContent = i18n.t('ui.proxy_url_required');
          return;
        }
        const seenURLs = new Map();
        const seenNames = new Map();
        let invalidEntry = null;
        let invalidMessage = '';
        for (const entry of entries) {
          if (!entry.url) { invalidEntry = entry; invalidMessage = i18n.t('ui.proxy_node_missing_url', { index: entry.index + 1 }); break; }
          const parsed = proxyImportLine(entry.url);
          if (!parsed.valid) { invalidEntry = entry; invalidMessage = i18n.t('ui.proxy_node_invalid_url', { index: entry.index + 1, reason: parsed.reason || i18n.t('ui.proxy_url_invalid_format') }); break; }
          if (seenURLs.has(parsed.key)) { invalidEntry = entry; invalidMessage = i18n.t('ui.proxy_node_duplicate', { index: entry.index + 1, duplicate: seenURLs.get(parsed.key) + 1 }); break; }
          seenURLs.set(parsed.key, entry.index);
          const nameKey = entry.name.toLowerCase();
          if (nameKey && seenNames.has(nameKey)) { invalidEntry = entry; invalidMessage = i18n.t('ui.proxy_node_duplicate_name', { index: entry.index + 1 }); break; }
          if (nameKey) seenNames.set(nameKey, entry.index);
        }
        if (invalidEntry) {
          invalidEntry.urlField?.setAttribute('aria-invalid', 'true');
          invalidEntry.urlField?.focus();
          status.textContent = invalidMessage;
          toast(invalidMessage, 'error');
          return;
        }
        const input = {
          id: source ? source.id : '',
          name: entries[0].name,
          url: entries[0].url,
          enabled: document.getElementById('pnode_enabled').checked,
          priority: signedNumberValue('pnode_priority', 0),
          max_bindings: numberOrZero('pnode_max_bindings'),
          expected_version: source ? Number(source.config_version || 0) : 0
        };
        setBusy(button, true);
        status.textContent = i18n.t(entries.length > 1 ? 'ui.validating_and_adding_nodes' : 'ui.validating_and_saving');
        try {
          if (!editing && entries.length > 1) {
            const nodes = entries.map((entry) => ({ name: entry.name, url: entry.url, enabled: input.enabled, priority: input.priority, max_bindings: input.max_bindings }));
            const result = await waitForTask(await api('proxy.sources.import_nodes', { nodes: nodes }), i18n.t('ui.proxy_node_add_title'));
            if (state.proxyEditing !== editor) return;
            const created = Number(result?.created || 0);
            const duplicate = Number(result?.duplicate || 0);
            const invalid = Number(result?.invalid || 0);
            const skipped = Number(result?.skipped || 0);
            const message = i18n.t('ui.nodes_added_message', { created: created, duplicate: duplicate, invalid: invalid, skipped: skipped ? i18n.t('ui.nodes_skipped_suffix', { skipped: skipped }) : '' });
            status.textContent = message;
            toast(message, created && !duplicate && !invalid && !skipped ? 'success' : 'error');
            if (created && !duplicate && !invalid && !skipped && editor.revision === revision) {
              editor.dirty = false;
              await closeProxyDrawer(true);
            }
          } else {
            const queued = await api(editing ? 'proxy.sources.update' : 'proxy.sources.create', { kind: 'node', source: input });
            const result = await waitForTask(queued, i18n.t('ui.save_node_and_sync'));
            if (state.proxyEditing !== editor) return;
            if (result?.ignored) { status.textContent = i18n.t('ui.ignored_protocol_input'); toast(i18n.t('ui.node_not_created_missing_protocol'), 'error'); return; }
            const savedSource = result?.source || (result?.id ? result : null);
            if (savedSource) source = Object.assign({}, source, savedSource);
            editor.source = source;
            if (editor.revision === revision) editor.dirty = false;
            const summary = reportProxyResult(result, i18n.t('ui.save_node_and_sync'));
            if (summary.failed) {
              const message = i18n.t('ui.node_saved_sync_failures', { count: summary.failed });
              status.textContent = message;
              toast(message, 'error');
            } else {
              status.textContent = summary.message + (editor.dirty ? i18n.t('ui.draft_has_unsaved_changes_suffix') : '');
              if (!editor.dirty) await closeProxyDrawer(true);
            }
          }
          await loadProxyData();
          renderProxySources();
          renderResin();
        } catch (error) {
          await showProxyEditorError(error, source, editor);
        } finally { setBusy(button, false); }
      });
      openProxyDrawer();
    }
    function templatePlaceholderNames(url) {
      const names = [];
      const seen = new Set();
      String(url || '').replace(/\{([^{}]+)\}/g, (_, name) => {
        name = String(name).trim();
        if (name && !seen.has(name)) { seen.add(name); names.push(name); }
        return '';
      });
      return names;
    }
    function proxyPlaceholderCard(name, definition, expanded) {
      const placeholder = definition || {};
      const kind = placeholder.kind || 'enum';
      if (kind === 'reserved') {
        return '<details class="proxy-template-variable" data-placeholder-card="' + esc(name) + '" open><summary><span class="proxy-variable-name">{' + esc(name) + '}</span><span class="proxy-variable-kind">' + i18n.t('ui.reserved_variable') + '</span>' + helpTip(i18n.t('ui.reserved_variable_help'), 'proxy-variable-help') + '</summary></details>';
      }
      const enumSpec = placeholder.enum || {};
      const random = placeholder.random || {};
      const enumInput = enumSpec.input || (enumSpec.values || []).join(enumSpec.separator || ',');
      const open = expanded !== false ? ' open' : '';
      const kindLabel = kind === 'enum' ? i18n.t('ui.enum_variable') : i18n.t('ui.random_variable');
      const kindHelp = kind === 'enum' ? i18n.t('ui.enum_variable_help') : i18n.t('ui.random_variable_help');
      return '<details class="proxy-template-variable" data-placeholder-card="' + esc(name) + '"' + open + '><summary><span class="proxy-variable-name">{' + esc(name) + '}</span><span class="proxy-variable-kind" data-placeholder-summary-kind>' + esc(kindLabel) + '</span>' + helpTip(kindHelp, 'proxy-variable-help') + '</summary><div class="proxy-variable-body"><div class="field-grid proxy-generation-grid">' +
        '<div class="field">' + proxyFieldLabel(i18n.t('ui.generation_method'), '', i18n.t('ui.generation_method_help')) + '<select aria-label="' + esc(i18n.t('ui.generation_method')) + '" data-placeholder-kind="' + esc(name) + '"><option value="enum"' + (kind === 'enum' ? ' selected' : '') + '>' + i18n.t('ui.enum_values') + '</option><option value="random"' + (kind === 'random' ? ' selected' : '') + '>' + i18n.t('ui.random_string') + '</option></select></div>' +
        '<div class="field" data-enum-field>' + proxyFieldLabel(i18n.t('ui.related_group'), '', i18n.t('ui.related_group_help')) + '<input aria-label="' + esc(i18n.t('ui.related_group')) + '" data-placeholder-group="' + esc(name) + '" data-previous-group="' + esc(placeholder.group_id || '') + '" value="' + esc(placeholder.group_id || '') + '" placeholder="' + esc(i18n.t('ui.optional_example_plans')) + '"></div>' +
        '<div class="field" data-enum-field>' + proxyFieldLabel(i18n.t('ui.separator'), '', i18n.t('ui.separator_help')) + '<input aria-label="' + esc(i18n.t('ui.separator')) + '" data-enum-separator="' + esc(name) + '" data-previous-separator="' + esc(enumSpec.separator || ',') + '" value="' + esc(enumSpec.separator || ',') + '"><span class="proxy-field-error" data-enum-error></span></div>' +
        '<div class="field full" data-enum-field>' + proxyFieldLabel(i18n.t('ui.candidate_values'), '', i18n.t('ui.candidate_values_help')) + '<textarea aria-label="' + esc(i18n.t('ui.candidate_values')) + '" data-enum-input="' + esc(name) + '" placeholder="' + esc(i18n.t('ui.candidate_values_placeholder')) + '">' + esc(enumInput) + '</textarea><span class="hint" data-enum-summary></span></div>' +
        '<div class="field" data-random-field>' + proxyFieldLabel(i18n.t('ui.random_length'), '', i18n.t('ui.random_length_help')) + '<input aria-label="' + esc(i18n.t('ui.random_length')) + '" type="number" min="1" max="128" data-random-length="' + esc(name) + '" value="' + esc(random.length || 16) + '"></div>' +
        '<div class="field" data-random-field>' + proxyFieldLabel(i18n.t('ui.character_set'), '', i18n.t('ui.character_set_help')) + '<label class="check-row"><input type="checkbox" data-random-case="' + esc(name) + '" ' + (random.case_sensitive ? 'checked' : '') + '><span>' + i18n.t('ui.case_sensitive') + '</span></label></div>' +
        '</div></div></details>';
    }

        function splitProxyEnum(input, separator) {
      if (!separator || Array.from(separator).length > 8) throw new Error(i18n.t("ui.separator_length_error"));
      const values = String(input).split(separator).map((value) => value.trim());
      if (values.some((value) => !value)) throw new Error(i18n.t("ui.enum_empty_error"));
      return Array.from(new Set(values));
    }
    function syncProxyPlaceholderCards() {
      document.querySelectorAll('[data-placeholder-card]').forEach((card) => {
        const kindField = card.querySelector('[data-placeholder-kind]');
        if (!kindField) return;
        const enumKind = kindField.value === 'enum';
        const summaryKind = card.querySelector('[data-placeholder-summary-kind]');
        if (summaryKind) summaryKind.textContent = enumKind ? i18n.t('ui.enum_variable') : i18n.t('ui.random_variable');
        card.querySelectorAll('[data-enum-field]').forEach((field) => { field.hidden = !enumKind; });
        card.querySelectorAll('[data-random-field]').forEach((field) => { field.hidden = enumKind; });
        if (!enumKind) return;
        const groupID = card.querySelector('[data-placeholder-group]').value.trim();
        const input = card.querySelector('[data-enum-input]');
        const separator = card.querySelector('[data-enum-separator]').value;
        const summary = card.querySelector('[data-enum-summary]');
        input.readOnly = Boolean(groupID);
        if (groupID) {
          const group = Array.from(state.proxyEditing?.groups.values() || []).find((item) => item.id === groupID);
          const index = group?.columns.indexOf(card.dataset.placeholderCard) ?? -1;
          const values = index >= 0 ? Array.from(new Set(group.rows.map((row) => row[index]).filter(Boolean))) : [];
          input.value = values.slice(0, 50).join(separator || ',');
          summary.textContent = group ? i18n.t('ui.related_group_summary', { count: num(values.length), truncated: values.length > 50 ? i18n.t('ui.only_first_50_values') : '' }) : i18n.t('ui.related_group_empty');
        } else {
          try { summary.textContent = i18n.t('ui.candidate_values_summary', { values: splitProxyEnum(input.value, separator).slice(0, 50).join(i18n.t('ui.meta_separator')) }); }
          catch (error) { summary.textContent = error.message; }
        }
      });
    }

        function changeProxyEnumSeparator(field) {
      const card = field.closest('[data-placeholder-card]');
      const input = card.querySelector('[data-enum-input]');
      const previous = field.dataset.previousSeparator;
      const error = card.querySelector('[data-enum-error]');
      try {
        const next = field.value;
        if (!next || Array.from(next).length > 8) throw new Error(i18n.t("ui.separator_length_error"));
        const groupID = card.querySelector('[data-placeholder-group]').value.trim();
        const group = Array.from(state.proxyEditing.groups.values()).find((item) => item.id === groupID);
        const index = group?.columns.indexOf(card.dataset.placeholderCard) ?? -1;
        const values = group && index >= 0 ? group.rows.map((row) => row[index]).filter(Boolean) : (input.value ? splitProxyEnum(input.value, previous) : []);
        if (values.some((value) => value.includes(next))) throw new Error(i18n.t("ui.separator_contains_existing_value"));
        if (!groupID) input.value = values.join(next);
        field.dataset.previousSeparator = next;
        field.removeAttribute('aria-invalid');
        error.textContent = '';
        syncProxyPlaceholderCards();
      } catch (failure) {
        field.value = previous;
        field.setAttribute('aria-invalid', 'true');
        error.textContent = failure.message;
      }
    }
    function proxyGroupSeparator(column) {
      const card = document.querySelector('[data-placeholder-card="' + CSS.escape(column) + '"]');
      return card ? card.querySelector('[data-enum-separator]').value : '|';
    }
    function detachProxyPlaceholderGroup(field, group) {
      const card = field.closest('[data-placeholder-card]');
      const index = group.columns.indexOf(card.dataset.placeholderCard);
      if (index >= 0) card.querySelector('[data-enum-input]').value = Array.from(new Set(group.rows.map((row) => row[index]).filter(Boolean))).join(card.querySelector('[data-enum-separator]').value);
      field.value = '';
      field.dataset.previousGroup = '';
    }
    function proxyRowsUnique(rows) {
      const seen = new Set();
      return rows.filter((row) => { const key = JSON.stringify(row); if (seen.has(key)) return false; seen.add(key); return true; });
    }
    function proxyRowsMatchPrefix(row, prefix) { return prefix.every((value, index) => row[index] === value); }
    function proxyGroupDraft(group, index) {
      return { index: index, id: group.id || '', name: group.name || '', columns: (group.columns || []).slice(), rows: (group.rows || []).map((row) => row.slice()), view: 'hierarchy', page: 1, childPage: 1, childIndex: (group.columns || []).length > 3 ? 0 : Math.max(0, (group.columns || []).length - 1), path: (group.columns || []).map(() => ''), search: '', bulkDrafts: new Map(), leafDrafts: new Map(), importText: '', importFormat: 'csv', importMode: 'append', importHeader: true, importRevision: 0, importAppliedRevision: 0, editValue: null, editPrefix: [], editDraft: '', error: '' };
    }
    function proxyGroupCard(group, index) {
      const g = proxyGroupDraft(group || {}, index);
      state.proxyEditing.groups.set(index, g);
      state.proxyEditing.nextGroupIndex = Math.max(state.proxyEditing.nextGroupIndex, index + 1);
      return '<div class="proxy-group-editor" data-group-card="' + index + '"><div class="field-grid">' +
        '<div class="field"><label>' + i18n.t('ui.group_id') + '<input data-group-id="' + index + '" value="' + esc(g.id) + '" placeholder="' + esc(i18n.t('ui.group_id_placeholder')) + '"></label></div>' +
        '<div class="field"><label>' + i18n.t('ui.display_name') + '<input data-group-name="' + index + '" value="' + esc(g.name) + '"></label></div>' +
        '<div class="field full"><label>' + i18n.t('ui.columns_and_order') + '<input data-group-columns="' + index + '" value="' + esc(g.columns.join(', ')) + '" placeholder="vendor, plan, profile"></label><span class="hint">' + i18n.t('ui.columns_help') + '</span></div>' +
        '</div><div class="form-actions"><button class="btn btn-xs" type="button" data-group-view="hierarchy">' + i18n.t('ui.parent_and_child') + '</button><button class="btn btn-xs" type="button" data-group-view="table">' + i18n.t('ui.advanced_table') + '</button><button class="btn btn-xs" type="button" data-group-view="import">' + i18n.t('ui.import_csv_tsv') + '</button><button class="btn btn-xs btn-ghost" type="button" data-group-export="csv">' + i18n.t('ui.export_csv') + '</button><button class="btn btn-xs btn-danger btn-ghost" type="button" data-remove-group="' + index + '">' + i18n.t('ui.remove_group_existing') + '</button></div><p class="proxy-field-error" data-group-error role="alert"></p><div class="proxy-group-view" data-group-body></div></div>';
    }

        function proxyGroupColumnChoices(group, column, prefix) {
      const values = new Set();
      group.rows.forEach((row) => { if (proxyRowsMatchPrefix(row, prefix) && row[column]) values.add(row[column]); });
      return Array.from(values);
    }
    function proxyGroupBulkKey(group) { return JSON.stringify([group.childIndex, group.path.slice(0, group.childIndex)]); }
    function renderProxyGroup(index) {
      const g = state.proxyEditing?.groups.get(Number(index));
      const card = document.querySelector('[data-group-card="' + index + '"]');
      if (!g || !card) return;
      card.querySelector('[data-group-error]').textContent = g.error;
      const body = card.querySelector('[data-group-body]');
      if (!g.columns.length) { body.innerHTML = '<p class="hint">' + i18n.t('ui.group_columns_empty') + '</p>'; return; }
      const incomplete = g.rows.filter((row) => row.length !== g.columns.length || row.some((value) => !String(value).trim())).length;
      const summary = '<p class="hint">' + i18n.t('ui.valid_combinations_summary', { valid: num(g.rows.length - incomplete), incomplete: incomplete ? i18n.t('ui.incomplete_combinations', { count: num(incomplete) }) : '' }) + '</p>';
      if (g.view === 'table') {
        const pages = Math.max(1, Math.ceil(g.rows.length / 20));
        g.page = Math.max(1, Math.min(g.page, pages));
        const start = (g.page - 1) * 20;
        replaceProxyHTML(body, summary + '<div class="table-wrap"><table><thead><tr>' + g.columns.map((column) => '<th>' + esc(column) + '</th>').join('') + '<th>' + i18n.t('ui.operations') + '</th></tr></thead><tbody>' + g.rows.slice(start, start + 20).map((row, offset) => '<tr>' + g.columns.map((column, columnIndex) => '<td><input data-group-cell-row="' + (start + offset) + '" data-group-cell-column="' + columnIndex + '" aria-label="' + esc(i18n.t('ui.row_action_label', { row: start + offset + 1, column: column })) + '" value="' + esc(row[columnIndex] || '') + '"' + (!row[columnIndex] ? ' aria-invalid="true"' : '') + '></td>').join('') + '<td><button class="btn btn-xs btn-danger btn-ghost" type="button" data-group-delete-row="' + (start + offset) + '">' + i18n.t('ui.delete_row') + '</button></td></tr>').join('') + '</tbody></table></div><div class="form-actions"><button class="btn btn-xs" type="button" data-group-add-row>' + i18n.t('ui.add_table_row') + '</button><button class="btn btn-xs" type="button" data-group-table-page="-1" ' + (g.page <= 1 ? 'disabled' : '') + '>' + i18n.t('ui.previous_page') + '</button><span>' + i18n.t('ui.page_of', { page: g.page, pages: pages }) + '</span><button class="btn btn-xs" type="button" data-group-table-page="1" ' + (g.page >= pages ? 'disabled' : '') + '>' + i18n.t('ui.next_page') + '</button></div>');
        return;
      }
      if (g.view === 'import') {
        replaceProxyHTML(body, summary + '<div class="field-grid"><div class="field"><label>' + i18n.t('ui.format') + '<select data-group-import-format><option value="csv"' + (g.importFormat === 'csv' ? ' selected' : '') + '>' + i18n.t('ui.csv_comma') + '</option><option value="tsv"' + (g.importFormat === 'tsv' ? ' selected' : '') + '>' + i18n.t('ui.tsv_tab') + '</option></select></label></div><div class="field"><label>' + i18n.t('ui.import_mode') + '<select data-group-import-mode><option value="append"' + (g.importMode === 'append' ? ' selected' : '') + '>' + i18n.t('ui.append_and_dedupe') + '</option><option value="replace"' + (g.importMode === 'replace' ? ' selected' : '') + '>' + i18n.t('ui.replace_all_rows') + '</option></select></label></div><div class="field"><label class="check-row"><input type="checkbox" data-group-import-header ' + (g.importHeader ? 'checked' : '') + '><span>' + i18n.t('ui.first_row_is_header') + '</span></label></div><div class="field"><label>' + i18n.t('ui.utf8_file') + '<input type="file" accept=".csv,.tsv,text/csv,text/tab-separated-values" data-group-import-file></label></div><div class="field full"><label>' + i18n.t('ui.combination_data') + '<textarea data-group-import-text placeholder="' + esc(g.columns.join(',')) + '">' + esc(g.importText) + '</textarea></label><span class="hint">' + i18n.t('ui.import_csv_help') + '</span></div></div><button class="btn btn-xs" type="button" data-group-import-apply>' + i18n.t('ui.parse_and_import') + '</button>');
        return;
      }
      g.childIndex = Math.max(0, Math.min(g.childIndex, g.columns.length - 1));
      const prefix = g.columns.slice(0, g.childIndex).map((_, columnIndex) => g.path[columnIndex] || '');
      const completePath = prefix.length === g.childIndex && prefix.every(Boolean);
      const fieldHTML = g.columns.slice(0, g.childIndex).map((column, columnIndex) => {
        if (columnIndex < g.childIndex - 2) return '';
        const values = proxyGroupColumnChoices(g, columnIndex, g.path.slice(0, columnIndex));
        const listID = 'proxy-path-' + index + '-' + columnIndex;
        return '<div class="field"><label>' + esc(column) + '<input data-group-path="' + columnIndex + '" list="' + listID + '" value="' + esc(g.path[columnIndex] || '') + '" autocomplete="off"></label><datalist id="' + listID + '">' + values.slice(0, 50).map((value) => '<option value="' + esc(value) + '"></option>').join('') + '</datalist><span class="hint">' + i18n.t('ui.existing_or_new_branch') + (values.length > 50 ? i18n.t('ui.candidates_first_50') : '') + '</span></div>';
      }).join('');
      const values = completePath ? proxyGroupColumnChoices(g, g.childIndex, prefix).filter((value) => !g.search || value.toLowerCase().includes(g.search.toLowerCase())) : [];
      const pages = Math.max(1, Math.ceil(values.length / 50));
      g.childPage = Math.max(1, Math.min(g.childPage, pages));
      const column = g.columns[g.childIndex];
      const separator = proxyGroupSeparator(column);
      const leafEditorHTML = g.editValue == null ? '' : '<div class="proxy-template-variable"><p class="proxy-path">' + esc(i18n.t('ui.edit_child_prefix', { path: g.editPrefix.concat(g.editValue).join(' / ') })) + '</p><div class="field"><label>' + esc(g.columns[g.columns.length - 1] + ' ' + i18n.t('ui.new_value')) + '<input data-group-leaf-rename value="' + esc(g.editDraft) + '"></label></div><div class="form-actions"><button class="btn btn-xs" type="button" data-group-rename-child>' + i18n.t('ui.apply_child_changes') + '</button><button class="btn btn-xs btn-ghost" type="button" data-group-cancel-child>' + i18n.t('ui.cancel_child_changes') + '</button></div></div>';
      const breadcrumbsHTML = '<nav class="form-actions" aria-label="' + esc(i18n.t('ui.parent_path')) + '">' + g.columns.slice(0, g.childIndex).map((name, columnIndex) => '<button class="btn btn-xs btn-ghost" type="button" data-group-breadcrumb="' + columnIndex + '">' + esc(name + '=' + (g.path[columnIndex] || i18n.t('ui.select_value'))) + '</button>').join('') + '</nav>';
      replaceProxyHTML(body, summary + breadcrumbsHTML + '<div class="field"><label>' + i18n.t('ui.current_edit_column') + '<select data-group-child-column>' + g.columns.map((name, columnIndex) => '<option value="' + columnIndex + '"' + (columnIndex === g.childIndex ? ' selected' : '') + '>' + esc(name) + '</option>').join('') + '</select></label></div><p class="proxy-path">' + esc(i18n.t('ui.path_label', { path: prefix.map((value, columnIndex) => g.columns[columnIndex] + '=' + (value || i18n.t('ui.select_value'))).concat(column).join(' / ') })) + '</p><div class="proxy-path-fields">' + fieldHTML + '</div><div class="field"><label>' + esc(column + ' ' + i18n.t('ui.bulk_values')) + '<textarea data-group-bulk-values placeholder="' + esc(i18n.t('ui.separator_placeholder', { separator: separator })) + '">' + esc(g.bulkDrafts.get(proxyGroupBulkKey(g)) || '') + '</textarea></label><span class="hint">' + i18n.t('ui.current_separator', { separator: esc(separator), next: g.childIndex < g.columns.length - 1 ? i18n.t('ui.continue_after_add') : '' }) + '</span></div><div class="form-actions"><button class="btn btn-xs" type="button" data-bulk-fill-group="' + index + '">' + i18n.t('ui.append_to_current_path') + '</button><button class="btn btn-xs btn-danger btn-ghost" type="button" data-group-delete-path ' + (!completePath ? 'disabled' : '') + '>' + i18n.t('ui.delete_current_path') + '</button></div><div class="field"><label>' + i18n.t('ui.search_current_children') + '<input type="search" data-group-child-search value="' + esc(g.search) + '"></label></div><div class="proxy-value-tags">' + values.slice((g.childPage - 1) * 50, g.childPage * 50).map((value) => '<button class="btn btn-xs btn-ghost" type="button" data-group-drill-value="' + esc(value) + '">' + esc(value) + (g.childIndex < g.columns.length - 1 ? ' →' : '') + '</button><button class="btn btn-xs btn-danger btn-ghost" type="button" aria-label="' + esc(i18n.t('ui.delete_value_branch', { value: value })) + '" data-group-delete-value="' + esc(value) + '">' + icon('x') + '</button>').join('') + '</div><div class="form-actions"><button class="btn btn-xs" type="button" data-group-child-page="-1" ' + (g.childPage <= 1 ? 'disabled' : '') + '>' + i18n.t('ui.previous_page') + '</button><span>' + i18n.t('ui.children_count_page', { count: values.length, page: g.childPage, pages: pages }) + '</span><button class="btn btn-xs" type="button" data-group-child-page="1" ' + (g.childPage >= pages ? 'disabled' : '') + '>' + i18n.t('ui.next_page') + '</button></div>' + (g.childIndex ? '<details><summary>' + i18n.t('ui.rename_or_copy_parent') + '</summary><div class="field"><label>' + i18n.t('ui.current_parent_new_name') + '<input data-group-rename-value></label></div><button class="btn btn-xs" type="button" data-group-rename-path>' + i18n.t('ui.rename_current_parent') + '</button><div class="proxy-path-fields">' + g.columns.slice(0, g.childIndex).map((name, columnIndex) => '<div class="field"><label>' + esc(i18n.t('ui.target', { name: name })) + '<input data-group-copy-target="' + columnIndex + '"></label></div>').join('') + '</div><div class="field"><label>' + i18n.t('ui.copy_mode') + '<select data-group-copy-mode><option value="merge">' + i18n.t('ui.merge_and_dedupe') + '</option><option value="replace">' + i18n.t('ui.replace_target_branch') + '</option></select></label></div><button class="btn btn-xs" type="button" data-group-copy-path>' + i18n.t('ui.copy_to_target_parent') + '</button></details>' : '') + leafEditorHTML);
    }

    function parseProxyDelimitedTable(text, delimiter) {
      const source = String(text || '').replace(/^\uFEFF/, '');
      const rows = [];
      let row = [], field = '', quoted = false, afterQuote = false, touched = false;
      for (let index = 0; index < source.length; index += 1) {
        const char = source[index];
        if (quoted) {
          if (char === '"' && source[index + 1] === '"') { field += '"'; index += 1; }
          else if (char === '"') { quoted = false; afterQuote = true; }
          else field += char;
          continue;
        }
        if (char === '"' && !field && !afterQuote) { quoted = true; touched = true; continue; }
        if (char === delimiter) { row.push(field); field = ''; afterQuote = false; touched = true; continue; }
        if (char === '\n' || char === '\r') {
          if (char === '\r' && source[index + 1] === '\n') index += 1;
          row.push(field); if (touched || field || row.length > 1) rows.push(row); row = []; field = ''; afterQuote = false; touched = false; continue;
        }
        if (afterQuote && !/\s/.test(char)) throw new Error(i18n.t('ui.csv_line_extra_chars', { line: rows.length + 1 }));
        if (char === '"') throw new Error(i18n.t('ui.csv_line_quote_wrap', { line: rows.length + 1 }));
        touched = true;
        if (!afterQuote) field += char;
      }
      if (quoted) throw new Error(i18n.t('ui.csv_line_unclosed_quote', { line: rows.length + 1 }));
      if (field || row.length || afterQuote || touched) { row.push(field); rows.push(row); }
      return rows;
    }

            function formatProxyDelimitedTable(columns, rows, delimiter) {
      const encode = (value) => { const text = String(value ?? ''); return /["\r\n]/.test(text) || text.includes(delimiter) ? '"' + text.replace(/"/g, '""') + '"' : text; };
      return [columns].concat(rows).map((row) => row.map(encode).join(delimiter)).join('\r\n');
    }
    function alignProxyImportedRows(columns, records, hasHeader) {
      if (!columns.length) throw new Error(i18n.t('ui.group_columns_required'));
      const rows = records.map((row) => row.slice());
      let indices = columns.map((_, index) => index);
      if (hasHeader) {
        const header = rows.shift() || [];
        if (header.length !== columns.length || new Set(header).size !== header.length || columns.some((column) => !header.includes(column))) throw new Error(i18n.t('ui.group_header_mismatch'));
        indices = columns.map((column) => header.indexOf(column));
      }
      if (!rows.length) throw new Error(i18n.t('ui.group_no_rows'));
      return rows.map((row, index) => {
        const line = index + (hasHeader ? 2 : 1);
        if (row.length !== columns.length) throw new Error(i18n.t('ui.group_row_column_count', { line: line, count: columns.length }));
        return indices.map((columnIndex, indexInGroup) => { const value = String(row[columnIndex]).trim(); if (!value) throw new Error(i18n.t('ui.group_row_empty_value', { line: line, column: columns[indexInGroup] })); return value; });
      });
    }

        function proxyTemplateConditionValues(name) {
      const editor = state.proxyEditing;
      const placeholder = readProxyPlaceholder(name);
      if (!placeholder || placeholder.kind !== 'enum') return [];
      if (!placeholder.group_id) {
        try { return splitProxyEnum(placeholder.enum.input || '', placeholder.enum.separator); } catch (_) { return []; }
      }
      const group = Array.from(editor.groups.values()).find((item) => item.id === placeholder.group_id);
      const column = group?.columns.indexOf(name) ?? -1;
      if (column < 0) return [];
      const values = new Set();
      group.rows.forEach((row) => {
        const matches = group.columns.every((other, index) => other === name || !editor.assignments[other] || row[index] === editor.assignments[other]);
        if (matches && row[column]) values.add(row[column]);
      });
      return Array.from(values);
    }
    function renderProxyTemplateConditions() {
      const container = document.getElementById('proxyTemplateConditions');
      const editor = state.proxyEditing;
      if (!container || !['template', 'resin'].includes(editor?.kind)) return;
      const names = Array.from(document.querySelectorAll('[data-placeholder-card]')).filter((card) => card.querySelector('[data-placeholder-kind]')?.value === 'enum').map((card) => card.dataset.placeholderCard);
      container.innerHTML = names.map((name, index) => '<div class="field">' + proxyFieldLabel(i18n.t('ui.fixed_value', { name: name }), '', i18n.t('ui.leave_blank_random')) + '<input aria-label="' + esc(i18n.t('ui.fixed_value', { name: name })) + '" data-proxy-assignment="' + esc(name) + '" list="proxy-condition-' + index + '" value="' + esc(editor.assignments[name] || '') + '" placeholder="' + esc(i18n.t('ui.leave_blank_random_short')) + '" autocomplete="off"><datalist id="proxy-condition-' + index + '">' + proxyTemplateConditionValues(name).slice(0, 50).map((value) => '<option value="' + esc(value) + '"></option>').join('') + '</datalist></div>').join('') || '<p class="proxy-empty-note">' + i18n.t('ui.no_enum_variables_random_preview') + '</p>';
    }

        function updateProxyTemplateConditionLists() {
      document.querySelectorAll('[data-proxy-assignment]').forEach((field) => {
        const values = proxyTemplateConditionValues(field.dataset.proxyAssignment);
        document.getElementById(field.getAttribute('list')).innerHTML = values.filter((value) => !field.value || value.toLowerCase().includes(field.value.toLowerCase())).slice(0, 50).map((value) => '<option value="' + esc(value) + '"></option>').join('');
        if (field.value && !values.includes(field.value)) field.setAttribute('aria-invalid', 'true');
        else field.removeAttribute('aria-invalid');
      });
    }
    async function previewProxyTemplate(editor, source, template, button) {
      const request = ++state.proxyPreviewSequence;
      const revision = editor.revision;
      setBusy(button, true);
      try {
        const input = collectProxyTemplateEditor(source, template);
        const assignments = Object.fromEntries(Object.entries(editor.assignments).filter(([, value]) => value));
        const result = await api('proxy.sources.preview', { source: input, assignments: assignments, count: 3 });
        if (state.proxyEditing !== editor || request !== state.proxyPreviewSequence || revision !== editor.revision) return;
        const output = document.getElementById('proxyTemplatePreviewResults');
        const errors = result?.errors || result?.validation?.errors || [];
        if (!result || !result.valid) {
          output.innerHTML = '<p class="proxy-field-error" role="alert">' + esc(result?.error || i18n.t('ui.template_validation_failed')) + '</p>' + errors.slice(0, 50).map((issue) => '<p class="proxy-field-error">' + esc(typeof issue === 'string' ? issue : [issue.path || issue.field, issue.group_id, issue.row != null ? i18n.t('ui.validation_row', { row: Number(issue.row) + 1 }) : '', issue.message || issue.error].filter(Boolean).join(i18n.t('ui.meta_separator'))) + '</p>').join('');
          if (errors[0]?.path) focusProxyValidationPath(errors[0].path);
          return;
        }
        const summary = result.summary || result.validation?.summary || {};
        const count = (result.samples || []).length;
        output.innerHTML = proxyPreviewSamplesHTML(result.samples, i18n.t('ui.validation_passed_samples', { count: num(count), max: summary.maximum_username_bytes ? i18n.t('ui.max_username_bytes', { bytes: num(summary.maximum_username_bytes) }) : '' }));
        document.getElementById('proxySourceEditorStatus').textContent = i18n.t('ui.preview_passed');
      } catch (error) {
        if (state.proxyEditing !== editor || request !== state.proxyPreviewSequence) return;
        const message = i18n.t('ui.preview_failed', { error: error.message });
        document.getElementById('proxySourceEditorStatus').textContent = message;
        toast(message, 'error');
      } finally { setBusy(button, false); }
    }

    async function openProxyTemplateEditor(source) {
      if (!document.getElementById('proxySourceDrawer').hidden && !await closeProxyDrawer()) return;
      const editor = beginProxyEditor('template', source);
      let detail = null;
      if (source) detail = await api('proxy.sources.get', { source_id: source.id, edit_fields: true });
      if (state.proxyEditing !== editor) return;
      if (detail) source = Object.assign({}, source, detail.view || detail.source || detail);
      editor.source = source;
      const template = detail?.template || (detail?.kind === 'template' ? detail : {});
      const existing = new Map((template.placeholders || []).map((item) => [item.name, item]));
      editor.placeholderDrafts = existing;
      const initialNames = templatePlaceholderNames(template.username_pattern || '');
      const groups = template.value_groups || [];
      document.getElementById('proxySourceEditorTitle').textContent = i18n.t(source ? 'ui.proxy_template_edit_title' : 'ui.proxy_template_add_title');
      document.getElementById('proxySourceEditorSubtitle').textContent = i18n.t(source ? 'ui.proxy_template_edit_subtitle' : 'ui.proxy_template_add_subtitle');
      setProxyEditorMeta(source, i18n.t('ui.proxy_template_meta'));
      const urlHint = (template.username_pattern ? i18n.t('ui.current_username_prefix', { username: template.username_pattern }) + ' ' : '') + i18n.t('ui.template_url_help');
      document.getElementById('proxySourceEditor').innerHTML = '<form id="proxyTemplateEditorForm" class="drawer-form"><div class="stack"><section class="card"><div class="form-section"><h3 class="section-title">' + i18n.t('ui.proxy_template_format_title') + helpTip(i18n.t('ui.proxy_template_format_help')) + '</h3><div class="field-grid">' +
        proxyEditorField(i18n.t('ui.template_name'), 'ptpl_name', template.name || '', { hint: i18n.t('ui.template_name_help') }) +
        proxyEditorCheck(i18n.t('ui.enable_template'), 'ptpl_enabled', source ? Boolean(source.enabled) : true, i18n.t('ui.node_disabled_help')) +
        proxyEditorField(i18n.t('ui.template_url'), 'ptpl_url', template.edit_url || '', { full: true, placeholder: 'socks5h://user-{vendor}-{plan}:password@proxy.example:1080', hint: urlHint }) +
        proxyEditorField(i18n.t('ui.priority'), 'ptpl_priority', template.priority || 0, { type: 'number', min: -1000000, max: 1000000, hint: i18n.t('ui.priority_help') }) +
        proxyEditorField(i18n.t('ui.binding_limit'), 'ptpl_max_bindings', template.max_bindings || 0, { type: 'number', min: 0, hint: i18n.t('ui.binding_limit_zero_help') }) +
        '<div class="field">' + proxyFieldLabel(i18n.t('ui.save_changes_policy'), 'ptpl_update_policy', i18n.t('ui.save_changes_policy_help')) + '<select id="ptpl_update_policy"><option value="keep_bound">' + i18n.t('ui.keep_template_only') + '</option><option value="rerender_bound">' + i18n.t('ui.sync_bound_accounts') + '</option></select></div>' +
        '</div></div></section><section class="card"><div class="form-section"><h3 class="section-title">' + i18n.t('ui.variables_and_relations') + helpTip(i18n.t('ui.variables_and_relations_help')) + '<span class="section-count" id="proxyTemplateVariableCount">' + esc(i18n.t('ui.variable_count', { count: initialNames.length })) + '</span></h3><div id="proxyTemplateVariables">' + (initialNames.map((name, index) => proxyPlaceholderCard(name, existing.get(name), index === 0)).join('') || '<p class="proxy-empty-note">' + i18n.t('ui.template_url_empty_variables') + '</p>') + '</div><div id="proxyTemplateGroups">' + groups.map((group, index) => proxyGroupCard(group, index)).join('') + '</div><div class="form-actions"><button class="btn btn-xs" type="button" id="proxyAddGroupBtn">' + icon('plus') + i18n.t('ui.add_related_group') + '</button></div></div></section><section class="card"><div class="form-section"><h3 class="section-title">' + i18n.t('ui.validation_and_preview') + helpTip(i18n.t('ui.template_preview_help')) + '</h3><div class="field-grid" id="proxyTemplateConditions"></div><div id="proxyTemplatePreviewResults" role="status" aria-live="polite"></div></div></section></div><div class="form-actions"><button class="btn" type="button" id="proxyTemplatePreviewBtn">' + icon('eye') + i18n.t('ui.generate_preview') + '</button><button class="btn btn-primary" type="submit">' + icon('check') + i18n.t('ui.validate_and_save') + '</button><span class="status-line" id="proxySourceEditorStatus" role="status" aria-live="polite"></span></div></form>';
      if (template.update_policy) setValue('ptpl_update_policy', template.update_policy);
      const rerenderVariables = () => {
        const rawURL = valueOf('ptpl_url');
        const names = templatePlaceholderNames(rawURL || template.username_pattern || '');
        const container = document.getElementById('proxyTemplateVariables');
        Array.from(container.querySelectorAll('[data-placeholder-card]')).forEach((card) => editor.placeholderDrafts.set(card.dataset.placeholderCard, readProxyPlaceholder(card.dataset.placeholderCard)));
        container.innerHTML = names.map((name, index) => proxyPlaceholderCard(name, editor.placeholderDrafts.get(name), index === 0)).join('') || '<p class="hint">' + i18n.t('ui.template_url_empty_variables') + '</p>';
        const count = document.getElementById('proxyTemplateVariableCount');
        if (count) count.textContent = i18n.t('ui.variable_count', { count: names.length });
        syncProxyPlaceholderCards();
        renderProxyTemplateConditions();
      };
      document.getElementById('ptpl_url').addEventListener('input', rerenderVariables);
      document.getElementById('proxyAddGroupBtn').addEventListener('click', () => {
        const container = document.getElementById('proxyTemplateGroups');
        const index = editor.nextGroupIndex++;
        container.insertAdjacentHTML('beforeend', proxyGroupCard({ id: '', columns: [], rows: [] }, index));
        renderProxyGroup(index);
        markProxyEditorDirty();
      });
      document.getElementById('proxyTemplatePreviewBtn').addEventListener('click', (event) => previewProxyTemplate(editor, source, template, event.currentTarget));
      document.getElementById('proxyTemplateEditorForm').addEventListener('submit', async (event) => {
        event.preventDefault();
        const button = event.submitter;
        const revision = editor.revision;
        if (source && !editor.dirty) {
          const message = i18n.t('ui.unchanged_cannot_save');
          document.getElementById('proxySourceEditorStatus').textContent = message;
          toast(message, 'error');
          return;
        }
        setBusy(button, true);
        document.getElementById('proxySourceEditorStatus').textContent = i18n.t('ui.validating_and_saving');
        try {
          const input = collectProxyTemplateEditor(source, template);
          const queued = await api(source ? 'proxy.sources.update' : 'proxy.sources.create', { kind: 'template', source: input });
          const result = await waitForTask(queued, i18n.t('ui.template_save_and_sync'));
          if (state.proxyEditing !== editor) return;
          const savedSource = result?.source || (result?.id ? result : null);
          if (savedSource) {
            source = Object.assign({}, source, savedSource);
            template.username_pattern = savedSource.username_pattern || '';
            template.placeholders = input.placeholders; template.value_groups = input.value_groups;
            if (!valueOf('ptpl_url')) rerenderVariables();
          }
          editor.source = source;
          if (editor.revision === revision) editor.dirty = false;
          const summary = reportProxyResult(result, i18n.t('ui.template_save_and_sync'));
          if (summary.failed) {
            const message = i18n.t('ui.template_saved_with_sync_failures', { count: summary.failed });
            document.getElementById('proxySourceEditorStatus').textContent = message;
            toast(message, 'error');
            if (source && result.source) {
              source.config_version = result.source.config_version;
              source.revision = result.source.revision;
            }
          } else {
            document.getElementById('proxySourceEditorStatus').textContent = summary.message + (editor.dirty ? i18n.t('ui.draft_has_unsaved_changes_suffix') : '');
            if (!editor.dirty) await closeProxyDrawer(true);
          }
          await loadProxyData();
          renderProxySources();
          renderResin();
        } catch (error) {
          await showProxyEditorError(error, source, editor);
        } finally { setBusy(button, false); }
      });
      editor.groups.forEach((_, index) => renderProxyGroup(index));
      syncProxyPlaceholderCards();
      renderProxyTemplateConditions();
      openProxyDrawer();
    }

        async function openProxyResinEditor(source) {
      if (!document.getElementById('proxySourceDrawer').hidden && !await closeProxyDrawer()) return;
      const editor = beginProxyEditor('resin', source);
      let detail = null;
      if (source) detail = await api('proxy.sources.get', { source_id: source.id, edit_fields: true });
      if (state.proxyEditing !== editor) return;
      if (detail) source = Object.assign({}, source, detail.view || detail.source || detail);
      editor.source = source;
      const resin = detail?.kind === 'resin' ? detail : (detail || {});
      const accountFormat = resin.account_format || 'grok-{account}-{suffix}';
      const existing = new Map((resin.placeholders || []).map((item) => [item.name, item]));
      existing.set('account', { name: 'account', kind: 'reserved' });
      if (!existing.has('suffix') && !source) existing.set('suffix', { name: 'suffix', kind: 'random', random: { length: 4, case_sensitive: false } });
      editor.placeholderDrafts = existing;
      const initialNames = templatePlaceholderNames(accountFormat);
      const groups = resin.value_groups || [];
      document.getElementById('proxySourceEditorTitle').textContent = i18n.t(source ? 'ui.resin_edit_title' : 'ui.resin_add_title');
      document.getElementById('proxySourceEditorSubtitle').textContent = i18n.t(source ? 'ui.resin_edit_subtitle' : 'ui.resin_add_subtitle');
      setProxyEditorMeta(source, i18n.t('ui.resin_meta'));
      document.getElementById('proxySourceEditor').innerHTML = '<form id="proxyResinEditorForm" class="drawer-form"><div class="stack"><section class="card"><div class="form-section"><h3 class="section-title">' + i18n.t('ui.resin_connection') + helpTip(i18n.t('ui.resin_connection_help')) + '<span class="section-count" id="proxyResinModeBadge">' + i18n.t('ui.api_mode') + '</span></h3><div class="field-grid">' +
        proxyEditorField(i18n.t('ui.proxy_name'), 'presin_name', resin.name || 'Resin', { placeholder: i18n.t('ui.proxy_name_placeholder'), hint: i18n.t('ui.proxy_name_help') }) +
        proxyEditorCheck(i18n.t('ui.enable_source'), 'presin_enabled', source ? Boolean(source.enabled) : true, i18n.t('ui.node_disabled_help')) +
        proxyEditorCheck(i18n.t('ui.local_rotation_only'), 'presin_local_only', Boolean(resin.local_only), i18n.t('ui.local_rotation_help')) +
        proxyEditorField(i18n.t('ui.platform_name'), 'presin_platform', resin.platform || '', { placeholder: i18n.t('ui.platform_placeholder'), hint: i18n.t('ui.platform_help') }) +
        proxyEditorField(i18n.t('ui.admin_api_url'), 'presin_admin_url', resin.admin_url || '', { placeholder: 'http://resin:2260', hint: i18n.t('ui.admin_api_help') }) +
        proxyEditorField(i18n.t('ui.admin_token'), 'presin_admin_token', '', { type: 'password', placeholder: i18n.t('ui.leave_blank_keep_saved'), hint: resin.admin_token_set ? i18n.t('ui.configured_leave_blank') : i18n.t('ui.api_mode_required') }) +
        proxyEditorField(i18n.t('ui.proxy_token'), 'presin_proxy_token', '', { type: 'password', placeholder: i18n.t('ui.leave_blank_keep_saved'), hint: resin.proxy_token_set ? i18n.t('ui.configured_leave_blank') : i18n.t('ui.api_mode_required_local_not_needed') }) +
        proxyEditorField(i18n.t('ui.priority'), 'presin_priority', resin.priority || 0, { type: 'number', min: -1000000, max: 1000000, hint: i18n.t('ui.priority_help') }) +
        proxyEditorField(i18n.t('ui.binding_limit'), 'presin_max_bindings', resin.max_bindings || 0, { type: 'number', min: 0, hint: i18n.t('ui.binding_limit_zero_help') }) +
        '</div></div></section>' +
        '<section class="card"><div class="form-section"><h3 class="section-title">' + i18n.t('ui.proxy_account_format') + helpTip(i18n.t('ui.account_format_address_help')) + '<span class="section-count" id="proxyResinVariableCount">' + esc(i18n.t('ui.variable_count', { count: initialNames.length })) + '</span></h3>' +
        proxyEditorField(i18n.t('ui.account_format'), 'presin_account_format', accountFormat, { full: true, placeholder: 'xai-{account}-{region}', hint: i18n.t('ui.account_format_help') }) +
        '<div id="proxyTemplateVariables">' + (initialNames.map((name, index) => proxyPlaceholderCard(name, existing.get(name), index === 0)).join('') || '<p class="proxy-empty-note">' + i18n.t('ui.use_account_or_other_variable') + '</p>') + '</div>' +
        '<div id="proxyTemplateGroups">' + groups.map((group, index) => proxyGroupCard(group, index)).join('') + '</div><div class="form-actions"><button class="btn btn-xs" type="button" id="proxyAddGroupBtn">' + icon('plus') + i18n.t('ui.add_related_group') + '</button></div></div></section>' +
        '<section class="card"><div class="form-section"><h3 class="section-title">' + i18n.t('ui.validation_and_preview') + helpTip(i18n.t('ui.resin_preview_help')) + '</h3><div id="proxyTemplatePreviewResults" role="status" aria-live="polite"></div></div></section></div>' +
        '<div class="form-actions"><button class="btn" type="button" id="proxyResinPreviewBtn">' + icon('eye') + i18n.t('ui.generate_preview') + '</button><button class="btn btn-primary" type="submit">' + icon('check') + i18n.t('ui.validate_and_save') + '</button><span class="status-line" id="proxySourceEditorStatus" role="status" aria-live="polite"></span></div></form>';
      const rerenderVariables = () => {
        const rawFormat = valueOf('presin_account_format');
        const names = templatePlaceholderNames(rawFormat || accountFormat);
        const container = document.getElementById('proxyTemplateVariables');
        Array.from(container.querySelectorAll('[data-placeholder-card]')).forEach((card) => {
          if (!card.querySelector('[data-placeholder-kind]')) return;
          editor.placeholderDrafts.set(card.dataset.placeholderCard, readProxyPlaceholder(card.dataset.placeholderCard));
        });
        container.innerHTML = names.map((name, index) => proxyPlaceholderCard(name, editor.placeholderDrafts.get(name), index === 0)).join('') || '<p class="hint">' + i18n.t('ui.use_account_or_other_variable') + '</p>';
        const count = document.getElementById('proxyResinVariableCount');
        if (count) count.textContent = i18n.t('ui.variable_count', { count: names.length });
        syncProxyPlaceholderCards();
        renderProxyTemplateConditions();
      };
      const updateMode = () => {
        const local = document.getElementById('presin_local_only').checked;
        ['presin_platform', 'presin_admin_url', 'presin_admin_token', 'presin_proxy_token'].forEach((id) => {
          const field = document.getElementById(id);
          if (field) {
            field.disabled = local;
            field.closest?.('.field')?.classList.toggle('is-disabled', local);
          }
        });
        const badge = document.getElementById('proxyResinModeBadge');
        if (badge) badge.textContent = local ? i18n.t('ui.local_rotation') : i18n.t('ui.api_mode');
      };
      document.getElementById('presin_account_format').addEventListener('input', rerenderVariables);
      document.getElementById('presin_local_only').addEventListener('change', updateMode);
      document.getElementById('proxyAddGroupBtn').addEventListener('click', () => {
        const container = document.getElementById('proxyTemplateGroups');
        const index = editor.nextGroupIndex++;
        container.insertAdjacentHTML('beforeend', proxyGroupCard({ id: '', columns: [], rows: [] }, index));
        renderProxyGroup(index);
        markProxyEditorDirty();
      });
      document.getElementById('proxyResinPreviewBtn').addEventListener('click', (event) => previewProxyResin(editor, source, resin, event.currentTarget));
      document.getElementById('proxyResinEditorForm').addEventListener('submit', async (event) => {
        event.preventDefault();
        const button = event.submitter;
        const revision = editor.revision;
        if (source && !editor.dirty) {
          const message = i18n.t('ui.unchanged_cannot_save');
          document.getElementById('proxySourceEditorStatus').textContent = message;
          toast(message, 'error');
          return;
        }
        setBusy(button, true);
        document.getElementById('proxySourceEditorStatus').textContent = i18n.t('ui.validating_and_saving');
        try {
          const input = collectProxyResinEditor(source, resin);
          const queued = await api(source ? 'proxy.sources.update' : 'proxy.sources.create', { kind: 'resin', source: input });
          const result = await waitForTask(queued, i18n.t('ui.resin_save'));
          if (state.proxyEditing !== editor) return;
          const savedSource = result?.source || result;
          if (savedSource?.id) source = Object.assign({}, source, savedSource);
          editor.source = source;
          if (editor.revision === revision) editor.dirty = false;
          document.getElementById('proxySourceEditorStatus').textContent = i18n.t('ui.resin_saved');
          toast(i18n.t('ui.resin_saved'), 'success');
          await loadProxyData();
          renderProxySources();
          renderResin();
          if (!editor.dirty) await closeProxyDrawer(true);
        } catch (error) {
          const message = i18n.t('ui.save_failed', { error: error.message });
          document.getElementById('proxySourceEditorStatus').textContent = message;
          toast(message, 'error');
        } finally {
          setBusy(button, false);
        }
      });
      editor.groups.forEach((_, index) => renderProxyGroup(index));
      syncProxyPlaceholderCards();
      renderProxyTemplateConditions();
      updateMode();
      openProxyDrawer();
    }

        function collectProxyResinEditor(source, current) {
      const format = valueOf('presin_account_format');
      const names = templatePlaceholderNames(format || current.account_format || '');
      const placeholders = names.filter((name) => name !== 'account').map(readProxyPlaceholder).filter(Boolean);
      return {
        id: source ? source.id : '',
        name: valueOf('presin_name'),
        enabled: document.getElementById('presin_enabled').checked,
        local_only: document.getElementById('presin_local_only').checked,
        platform: valueOf('presin_platform'),
        admin_url: valueOf('presin_admin_url'),
        admin_token: valueOf('presin_admin_token'),
        proxy_token: valueOf('presin_proxy_token'),
        account_format: format,
        placeholders: placeholders,
        value_groups: collectProxyTemplateGroups(),
        priority: signedNumberValue('presin_priority', 0),
        max_bindings: numberOrZero('presin_max_bindings'),
        update_policy: 'keep_bound',
        expected_version: source ? Number(source.config_version || 0) : 0
      };
    }
    async function previewProxyResin(editor, source, current, button) {
      const revision = editor.revision;
      setBusy(button, true);
      try {
        const input = collectProxyResinEditor(source, current);
        const result = await api('proxy.sources.preview', { kind: 'resin', source: input, count: 3 });
        if (state.proxyEditing !== editor || editor.revision !== revision) return;
        const output = document.getElementById('proxyTemplatePreviewResults');
        if (!result || !result.valid) {
          output.innerHTML = '<p class="proxy-field-error" role="alert">' + esc(result?.error || i18n.t('ui.resin_validation_failed')) + '</p>';
          return;
        }
        output.innerHTML = proxyPreviewSamplesHTML(result.samples, i18n.t('ui.resin_preview_samples'));
        document.getElementById('proxySourceEditorStatus').textContent = i18n.t('ui.preview_passed');
      } catch (error) {
        const message = i18n.t('ui.preview_failed', { error: error.message });
        if (state.proxyEditing === editor) document.getElementById('proxySourceEditorStatus').textContent = message;
        toast(message, 'error');
      } finally {
        setBusy(button, false);
      }
    }

        function readProxyPlaceholder(name) {
      const card = document.querySelector('[data-placeholder-card="' + CSS.escape(name) + '"]');
      if (!card) return null;
      const kind = card.querySelector('[data-placeholder-kind]').value;
      const groupID = card.querySelector('[data-placeholder-group]').value.trim();
      const definition = { name: name, kind: kind, group_id: kind === 'enum' && groupID ? groupID : undefined };
      if (kind === 'enum') {
        definition.enum = { separator: card.querySelector('[data-enum-separator]').value };
        if (!groupID) definition.enum.input = card.querySelector('[data-enum-input]').value;
      }
      else definition.random = { length: Number(card.querySelector('[data-random-length]').value || 16), case_sensitive: card.querySelector('[data-random-case]').checked };
      return definition;
    }
    function collectProxyTemplateGroups() {
      const ids = new Set();
      return Array.from(state.proxyEditing.groups.values()).map((group) => {
        const card = document.querySelector('[data-group-card="' + group.index + '"]');
        group.id = card.querySelector('[data-group-id]').value.trim();
        group.name = card.querySelector('[data-group-name]').value.trim();
        const pendingBulk = Array.from(group.bulkDrafts.entries()).find(([, value]) => value.trim());
        if (pendingBulk) {
          const [depth, prefix] = JSON.parse(pendingBulk[0]);
          group.childIndex = depth; group.path = group.columns.map((_, index) => prefix[index] || ''); group.view = 'hierarchy';
          renderProxyGroup(group.index); card.querySelector('[data-group-bulk-values]')?.focus();
          throw new Error(i18n.t('ui.group_unapplied_bulk', { group: group.id }));
        }
        if (group.importText.trim() && group.importRevision !== group.importAppliedRevision) { group.view = 'import'; renderProxyGroup(group.index); card.querySelector('[data-group-import-text]')?.focus(); throw new Error(i18n.t('ui.group_unapplied_import', { group: group.id })); }
        const pendingLeaf = Array.from(group.leafDrafts.entries()).find(([key, value]) => value.trim() !== JSON.parse(key).at(-1));
        if (pendingLeaf) {
          const path = JSON.parse(pendingLeaf[0]); group.editValue = path.at(-1); group.editPrefix = path.slice(0, -1); group.editDraft = pendingLeaf[1]; group.view = 'hierarchy'; group.childIndex = group.columns.length - 1; group.path = group.columns.map((_, index) => group.editPrefix[index] || '');
          renderProxyGroup(group.index); card.querySelector('[data-group-leaf-rename]')?.focus();
          throw new Error(i18n.t('ui.group_unapplied_leaf', { group: group.id }));
        }
        const columns = card.querySelector('[data-group-columns]').value.split(',').map((value) => value.trim()).filter(Boolean);
        if (!group.id || ids.has(group.id)) throw new Error(i18n.t('ui.group_id_invalid'));
        ids.add(group.id);
        if (columns.length < 2 || new Set(columns).size !== columns.length) throw new Error(i18n.t('ui.group_needs_two_columns', { group: group.id }));
        if (JSON.stringify(columns) !== JSON.stringify(group.columns)) throw new Error(i18n.t('ui.group_columns_unapplied', { group: group.id }));
        if (!group.rows.length) throw new Error(i18n.t('ui.group_no_valid_rows', { group: group.id }));
        const invalid = group.rows.findIndex((row) => row.length !== columns.length || row.some((value) => !String(value).trim()));
        if (invalid >= 0) {
          group.view = 'table'; group.page = Math.floor(invalid / 20) + 1;
          group.error = i18n.t('ui.group_incomplete_row', { row: invalid + 1 });
          renderProxyGroup(group.index);
          card.querySelector('[aria-invalid="true"]')?.focus();
          throw new Error(i18n.t('ui.group_prefix_error', { group: group.id, error: group.error }));
        }
        return { id: group.id, name: group.name, columns: columns, rows: proxyRowsUnique(group.rows.map((row) => row.map((value) => String(value).trim()))) };
      });
    }

        function collectProxyTemplateEditor(source, currentTemplate) {
      const names = templatePlaceholderNames(valueOf('ptpl_url') || currentTemplate.username_pattern || '');
      const placeholders = names.map(readProxyPlaceholder).filter(Boolean);
      return {
        id: source ? source.id : '',
        name: valueOf('ptpl_name'),
        url: valueOf('ptpl_url'),
        placeholders: placeholders,
        value_groups: collectProxyTemplateGroups(),
        enabled: document.getElementById('ptpl_enabled').checked,
        priority: signedNumberValue('ptpl_priority', 0),
        max_bindings: numberOrZero('ptpl_max_bindings'),
        update_policy: valueOf('ptpl_update_policy') || 'keep_bound',
        expected_version: source ? Number(source.config_version || 0) : 0
      };
    }
    function numberOrZero(id) {
      const parsed = Number(valueOf(id));
      return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
    }
    const PROXY_IMPORT_MAX_NODES = 100000;
    const PROXY_IMPORT_MAX_BYTES = 16 << 20;
    const PROXY_IMPORT_PROTOCOLS = new Set(['http:', 'https:', 'socks5:', 'socks5h:']);
    const PROXY_IMPORT_ENCODER = new TextEncoder();

    function decodeProxyImportCredential(value, errorKey) {
      try { return decodeURIComponent(value); } catch (_) { throw new Error(i18n.t(errorKey)); }
    }
    function proxyImportStandard(value, protocol) {
      let endpoint;
      try { endpoint = new URL(value); } catch (_) { return { valid: false, reason: i18n.t('ui.import_invalid_url') }; }
      if (!endpoint.hostname) return { valid: false, reason: i18n.t('ui.import_missing_hostname') };
      if (endpoint.pathname && endpoint.pathname !== '/') return { valid: false, reason: i18n.t('ui.import_path_not_allowed') };
      if (endpoint.search || endpoint.hash) return { valid: false, reason: i18n.t('ui.import_query_fragment_not_allowed') };
      const authority = value.slice(value.indexOf('://') + 3).split(/[\/?#]/, 1)[0];
      const hostPort = authority.slice(authority.lastIndexOf('@') + 1);
      const portMatch = hostPort.startsWith('[') ? /^\[[^\]]+\]:([0-9]+)$/.exec(hostPort) : /^[^:]+:([0-9]+)$/.exec(hostPort);
      if (!portMatch) return { valid: false, reason: i18n.t('ui.import_missing_explicit_port') };
      const port = Number(portMatch[1]);
      if (!Number.isInteger(port) || port < 1 || port > 65535) return { valid: false, reason: i18n.t('ui.import_invalid_port') };
      return { valid: true, key: endpoint.href, value: value, protocol: protocol };
    }
    function proxyImportAddressFirst(value, protocol) {
      const rest = value.slice(value.indexOf('://') + 3);
      if (!rest || /[\/?#]/.test(rest)) return { valid: false, reason: i18n.t('ui.import_address_first_no_path') };
      let addressEnd = rest.indexOf(':');
      let host = '';
      if (rest.startsWith('[')) {
        const bracket = rest.indexOf(']');
        if (bracket < 1 || rest[bracket + 1] !== ':') return { valid: false, reason: i18n.t('ui.import_invalid_ipv6_host') };
        host = rest.slice(1, bracket);
        addressEnd = bracket + 1;
      } else {
        if (addressEnd < 1) return { valid: false, reason: i18n.t('ui.import_missing_hostname') };
        host = rest.slice(0, addressEnd);
      }
      if (!host || /[\s\/?#@\[\]]/.test(host)) return { valid: false, reason: i18n.t('ui.import_invalid_hostname') };
      const remainder = rest.slice(addressEnd + 1);
      const portEnd = remainder.indexOf(':');
      if (portEnd < 1) return { valid: false, reason: i18n.t('ui.import_address_first_format') };
      const portText = remainder.slice(0, portEnd);
      if (!/^[0-9]+$/.test(portText)) return { valid: false, reason: i18n.t('ui.import_invalid_port') };
      const port = Number(portText);
      if (!Number.isInteger(port) || port < 1 || port > 65535) return { valid: false, reason: i18n.t('ui.import_invalid_port') };
      const credentials = remainder.slice(portEnd + 1);
      const credentialEnd = credentials.indexOf(':');
      if (credentialEnd <= 0) return { valid: false, reason: i18n.t('ui.import_username_password_required') };
      let username;
      let password;
      try {
        username = decodeProxyImportCredential(credentials.slice(0, credentialEnd), 'ui.import_invalid_username_encoding');
        password = decodeProxyImportCredential(credentials.slice(credentialEnd + 1), 'ui.import_invalid_password_encoding');
      } catch (error) {
        return { valid: false, reason: error.message };
      }
      if (!username) return { valid: false, reason: i18n.t('ui.import_username_empty') };
      if (/[\u0000-\u001f\u007f]/.test(username + password)) return { valid: false, reason: i18n.t('ui.import_credentials_control_chars') };
      const key = protocol + '//' + host.toLowerCase() + ':' + port + ':' + encodeURIComponent(username) + ':' + encodeURIComponent(password);
      return { valid: true, key: key, value: value, protocol: protocol };
    }
    function proxyImportHostPortAtCredentials(value, protocol) {
      const rest = value.slice(value.indexOf('://') + 3);
      if (!rest || /[\/?#]/.test(rest)) return { valid: false, reason: i18n.t('ui.import_host_port_at_no_path') };
      const at = rest.indexOf('@');
      if (at <= 0 || at >= rest.length - 1) return { valid: false, reason: i18n.t('ui.import_credentials_format_required') };
      const address = rest.slice(0, at);
      const credentials = rest.slice(at + 1);
      let host;
      let portText;
      if (address.startsWith('[')) {
        const bracket = address.indexOf(']');
        if (bracket < 1 || address[bracket + 1] !== ':') return { valid: false, reason: i18n.t('ui.import_invalid_ipv6_host') };
        host = address.slice(1, bracket);
        portText = address.slice(bracket + 2);
      } else {
        const colon = address.lastIndexOf(':');
        if (colon < 1) return { valid: false, reason: i18n.t('ui.import_at_prefix_host_port') };
        host = address.slice(0, colon);
        portText = address.slice(colon + 1);
        if (host.includes(':')) return { valid: false, reason: i18n.t('ui.import_ipv6_brackets') };
      }
      if (!host || /[\s\/?#@\[\]]/.test(host)) return { valid: false, reason: i18n.t('ui.import_invalid_hostname') };
      if (!/^[0-9]+$/.test(portText)) return { valid: false, reason: i18n.t('ui.import_invalid_port') };
      const port = Number(portText);
      if (!Number.isInteger(port) || port < 1 || port > 65535) return { valid: false, reason: i18n.t('ui.import_invalid_port') };
      const credentialEnd = credentials.indexOf(':');
      if (credentialEnd <= 0) return { valid: false, reason: i18n.t('ui.import_at_suffix_credentials') };
      let username;
      let password;
      try {
        username = decodeProxyImportCredential(credentials.slice(0, credentialEnd), 'ui.import_invalid_username_encoding');
        password = decodeProxyImportCredential(credentials.slice(credentialEnd + 1), 'ui.import_invalid_password_encoding');
      } catch (error) {
        return { valid: false, reason: error.message };
      }
      if (!username) return { valid: false, reason: i18n.t('ui.import_username_empty') };
      if (/[\u0000-\u001f\u007f]/.test(username + password)) return { valid: false, reason: i18n.t('ui.import_credentials_control_chars') };
      const key = protocol + '//' + host.toLowerCase() + ':' + port + '@' + encodeURIComponent(username) + ':' + encodeURIComponent(password);
      return { valid: true, key: key, value: value, protocol: protocol };
    }
    function proxyImportLine(raw) {
      const value = String(raw || '').trim();
      if (!value) return { valid: false, empty: true, reason: '' };
      if (/^[#;]/.test(value)) return { valid: false, comment: true, reason: '' };
      const match = /^([a-z][a-z0-9+.-]*):\/\//i.exec(value);
      if (!match) return { valid: false, reason: i18n.t('ui.import_missing_protocol') };
      const protocol = match[1].toLowerCase() + ':';
      if (!PROXY_IMPORT_PROTOCOLS.has(protocol)) return { valid: false, reason: i18n.t('ui.import_unsupported_protocol') };
      const standard = proxyImportStandard(value, protocol);
      if (standard.valid) return standard;
      const addressFirst = proxyImportAddressFirst(value, protocol);
      return addressFirst.valid ? addressFirst : proxyImportHostPortAtCredentials(value, protocol);
    }

        function parseProxyImportText(text) {
      const source = String(text || '');
      const lines = source.replace(/\r\n?/g, '\n').split('\n');
      const seen = new Set();
      const valid = [];
      const invalidSamples = [];
      let lineCount = 0;
      let invalid = 0;
      let duplicate = 0;
      let comments = 0;
      let validCount = 0;
      for (let index = 0; index < lines.length; index += 1) {
        const raw = lines[index].trim();
        if (!raw) continue;
        lineCount += 1;
        const parsed = proxyImportLine(raw);
        if (parsed.comment) { comments += 1; continue; }
        if (!parsed.valid) {
          invalid += 1;
          if (invalidSamples.length < 6) invalidSamples.push({ line: index + 1, reason: parsed.reason });
          continue;
        }
        if (seen.has(parsed.key)) { duplicate += 1; continue; }
        seen.add(parsed.key);
        validCount += 1;
        if (valid.length <= PROXY_IMPORT_MAX_NODES) valid.push(parsed.value);
      }
      const validBytes = PROXY_IMPORT_ENCODER.encode(valid.join('\n')).length;
      return {
        lineCount: lineCount,
        valid: valid,
        validCount: validCount,
        invalid: invalid,
        duplicate: duplicate,
        comments: comments,
        invalidSamples: invalidSamples,
        rawBytes: PROXY_IMPORT_ENCODER.encode(source).length,
        validBytes: validBytes,
        overLimit: validCount > PROXY_IMPORT_MAX_NODES,
        overBytes: validBytes > PROXY_IMPORT_MAX_BYTES
      };
    }

    async function openProxyImportEditor() {
      if (!document.getElementById('proxySourceDrawer').hidden && !await closeProxyDrawer()) return;
      const editor = beginProxyEditor('import', null);
      editor.importTimer = 0;
      document.getElementById('proxySourceEditorTitle').textContent = i18n.t('ui.bulk_import_proxy_nodes');
      document.getElementById('proxySourceEditorSubtitle').textContent = i18n.t('ui.bulk_import_subtitle');
      document.getElementById('proxySourceEditor').innerHTML = '<form id="proxyImportForm" class="drawer-form"><section class="card"><div class="form-section"><div class="import-toolbar"><div><h3>' + i18n.t('ui.proxy_list') + '</h3></div><div class="import-toolbar-actions"><input id="proxyImportFile" type="file" accept=".txt,.list,.csv,text/plain,text/csv" hidden><button class="btn btn-xs" type="button" id="proxyImportFileBtn">' + icon('upload') + i18n.t('ui.load_file') + '</button><button class="btn btn-xs btn-ghost" type="button" id="proxyImportFilterBtn">' + icon('check') + i18n.t('ui.filter_invalid_lines') + '</button></div></div><div class="field full"><label for="proxyImportText">' + i18n.t('ui.proxy_url') + '</label><textarea id="proxyImportText" class="proxy-import-text" spellcheck="false" wrap="off" aria-describedby="proxyImportHint" placeholder="' + esc(i18n.t('ui.import_standard_url_placeholder')) + '"></textarea><span class="hint" id="proxyImportHint">' + i18n.t('ui.import_hint') + '</span></div><div class="import-stats" id="proxyImportStats" role="status" aria-live="polite"></div><div class="import-samples" id="proxyImportSamples" hidden></div><div class="form-actions"><button class="btn btn-primary" type="submit" id="proxyImportSubmitBtn" disabled>' + icon('check') + i18n.t('ui.import_valid_nodes') + '</button><span class="status-line" id="proxySourceEditorStatus" role="status" aria-live="polite"></span></div></div></section></form>';

      const textarea = document.getElementById('proxyImportText');
      const fileInput = document.getElementById('proxyImportFile');
      const submit = document.getElementById('proxyImportSubmitBtn');
      const stats = document.getElementById('proxyImportStats');
      const samples = document.getElementById('proxyImportSamples');
      const status = document.getElementById('proxySourceEditorStatus');

      const stat = (label, value, tone) => '<span class="import-stat ' + (tone || '') + '"><span>' + esc(label) + ':</span><strong>' + esc(String(value)) + '</strong></span>';
      const renderPreview = (parsed, note) => {
        stats.innerHTML = stat(i18n.t('ui.total'), parsed.lineCount) + stat(i18n.t('ui.valid'), parsed.validCount, parsed.validCount ? 'good' : '') + stat(i18n.t('ui.invalid'), parsed.invalid, parsed.invalid ? 'bad' : '') + stat(i18n.t('ui.duplicate'), parsed.duplicate, parsed.duplicate ? 'warn' : '') + stat(i18n.t('ui.size'), formatBytes(parsed.validBytes));
        const problems = [];
        if (parsed.overLimit) problems.push(i18n.t('ui.import_over_100k'));
        if (parsed.overBytes) problems.push(i18n.t('ui.import_over_16mib'));
        parsed.invalidSamples.forEach((sample) => problems.push(esc(i18n.t('ui.import_line_error', { line: sample.line, reason: sample.reason }))));
        samples.hidden = problems.length === 0;
        samples.innerHTML = problems.length ? '<strong>' + i18n.t('ui.import_check') + '</strong><br>' + problems.join('<br>') : '';
        submit.disabled = parsed.validCount === 0 || parsed.overLimit || parsed.overBytes;
        if (note) status.textContent = note;
        return parsed;
      };
      const parseCurrent = (note) => renderPreview(parseProxyImportText(textarea.value), note);
      const schedulePreview = () => {
        window.clearTimeout(editor.importTimer);
        editor.importTimer = window.setTimeout(() => {
          if (state.proxyEditing === editor) parseCurrent();
        }, 180);
      };

      textarea.addEventListener('input', schedulePreview);

      textarea.addEventListener('paste', (event) => {
        const pasted = event.clipboardData?.getData('text') || '';
        if (PROXY_IMPORT_ENCODER.encode(pasted).length <= PROXY_IMPORT_MAX_BYTES) return;
        event.preventDefault();
        status.textContent = i18n.t('ui.paste_over_16mib');
        toast(i18n.t('ui.paste_over_16mib_short'), 'error');
      });
      document.getElementById('proxyImportFileBtn').addEventListener('click', () => fileInput.click());
      document.getElementById('proxyImportFilterBtn').addEventListener('click', () => {
        const parsed = parseCurrent();
        if (parsed.overLimit || parsed.overBytes) {
          toast(i18n.t('ui.import_limit_batch'), 'error');
          return;
        }
        textarea.value = parsed.valid.join('\n');
        markProxyEditorDirty();
        parseCurrent(i18n.t('ui.removed_lines', { count: parsed.invalid + parsed.duplicate }));
      });
      fileInput.addEventListener('change', async () => {
        const file = fileInput.files?.[0];
        if (!file) return;
        try {
          if (file.size > PROXY_IMPORT_MAX_BYTES) {
            status.textContent = i18n.t('ui.file_over_16mib');
            toast(i18n.t('ui.file_over_16mib_short'), 'error');
            return;
          }
          const parsed = renderPreview(parseProxyImportText(await file.text()));
          if (!parsed.validCount) {
            status.textContent = i18n.t('ui.file_no_valid_proxy');
            toast(i18n.t('ui.file_no_valid_proxy'), 'error');
            return;
          }
          if (parsed.overLimit || parsed.overBytes) {
            status.textContent = i18n.t('ui.import_limit_split_file');
            toast(i18n.t('ui.import_limit_short'), 'error');
            return;
          }
          textarea.value = parsed.valid.join('\n');
          markProxyEditorDirty();
          renderPreview(parsed, i18n.t('ui.file_loaded_summary', { name: file.name, valid: parsed.validCount, filtered: parsed.invalid + parsed.duplicate }));
          toast(i18n.t('ui.loaded_valid_nodes', { count: parsed.validCount }), 'success');
        } catch (error) {
          const message = i18n.t('ui.file_read_failed', { error: error.message });
          status.textContent = message;
          toast(message, 'error');
        } finally {
          fileInput.value = '';
        }
      });
      document.getElementById('proxyImportForm').addEventListener('submit', async (event) => {
        event.preventDefault();
        window.clearTimeout(editor.importTimer);
        const parsed = parseCurrent();
        if (parsed.overLimit || parsed.overBytes) { status.textContent = i18n.t('ui.import_limit_batch'); return; }
        if (!parsed.validCount) { status.textContent = i18n.t('ui.no_importable_proxy'); return; }
        const button = event.submitter || submit;
        const revision = editor.revision;
        setBusy(button, true);
        try {
          const result = await waitForTask(await api('proxy.sources.import_nodes', { urls: parsed.valid }), i18n.t('ui.import_proxy_nodes'));
          if (state.proxyEditing !== editor) return;
          const created = Number(result.created || 0);
          const skipped = Number(result.skipped || 0) + Number(result.invalid || 0);
          const duplicate = Number(result.duplicate || 0);
          const message = i18n.t('ui.import_created_summary', { created: created, skipped: skipped, duplicate: duplicate });
          status.textContent = message;
          toast(message, created ? 'success' : 'error');
          if (created && editor.revision === revision) { editor.dirty = false; await closeProxyDrawer(true); }
          await loadProxyData();
          renderProxySources();
          renderResin();
        } catch (error) {
          const message = i18n.t('ui.import_failed', { error: error.message });
          status.textContent = message;
          toast(message, 'error');
        } finally { setBusy(button, false); }
      });

      parseCurrent(i18n.t('ui.waiting_for_input'));
      openProxyDrawer();
    }

        async function openProxySourceByID(sourceID) {
      const source = state.proxySources.find((item) => item.id === sourceID);
      if (!source) return;
      if (source.kind === 'node') await openProxyNodeEditor(source);
      else if (source.kind === 'template') await openProxyTemplateEditor(source);
      else if (source.kind === 'resin') await openProxyResinEditor(source);
    }
    async function deleteProxySource(sourceID, kind) {
      const confirmed = await confirmAction({
        title: esc(i18n.t('ui.delete_proxy_title')),
        detail: esc(i18n.t('ui.delete_proxy_detail')),
        confirmLabel: esc(i18n.t('ui.delete_proxy_confirm')),
        icon: 'x',
        tone: 'danger'
      });
      if (!confirmed) return;
      try {
        const result = await waitForTask(await api('proxy.sources.delete', { source_id: sourceID, kind: kind, force: true }), i18n.t('ui.delete_proxy_task'));
        reportProxyResult(result, i18n.t('ui.delete_proxy_result'));
        state.proxySourceSelection.delete(sourceID);
        await loadProxyData();
        await refreshResin(null);
      } catch (error) { toast(error.message, 'error'); }
    }
    function selectedProxySourceIDs() {
      return Array.from(state.proxySourceSelection);
    }
    async function loadAllProxySourceIDs(all) {
      const ids = new Set();
      const params = listParams('sources', Boolean(all));
      for (let page = 1; ; page += 1) {
        const value = await api('proxy.sources.list', Object.assign({}, params, { page: page, page_size: 500 }));
        const items = (value && value.items) || [];
        items.forEach((item) => {
          const id = typeof item === 'string' ? item : item?.id;
          if (id) ids.add(id);
        });
        const total = Number(value?.total || items.length);
        if (!items.length || ids.size >= total || page * 500 >= total) break;
      }
      return ids;
    }
    async function selectProxySourceScope(all, button) {
      const count = all ? proxySourceAllCount() : pagedItems('sources').total;
      if (!count) return;
      if (!await confirmAction({ title: all ? i18n.t("ui.select_all_proxies") : i18n.t("ui.select_all_filtered"), detail: i18n.t('ui.select_all_proxies_confirm_detail', { count: count }), confirmLabel: i18n.t("ui.load_and_select_all"), icon: 'database' })) return;
      setBusy(button, true);
      try {
        const ids = await loadAllProxySourceIDs(all);
        ids.forEach((id) => state.proxySourceSelection.add(id));
        renderProxySources();
        toast(i18n.t('ui.selected_proxies_toast', { count: state.proxySourceSelection.size }), 'success');
      } catch (error) {
        toast(error.message, 'error');
      } finally {
        setBusy(button, false);
      }
    }
    async function testProxySource(sourceID, button) {
      const source = state.proxySources.find((item) => item.id === sourceID);
      setBusy(button, true);
      try {
        const result = await waitForTask(await api('proxy.sources.test', { source_id: sourceID }), i18n.t('ui.proxy_connectivity_test'));
        const generated = source?.kind === 'template' ? i18n.t('ui.template_generated_endpoint') : '';
        toast(result.reachable ? i18n.t('ui.endpoint_reachable', { generated: generated, latency: Number(result.latency_ms || 0) }) : i18n.t('ui.endpoint_unreachable', { generated: generated, error: result.error || i18n.t('ui.unreachable') }), result.reachable ? 'success' : 'error');
        await loadProxyData();
        renderProxySources();
      } catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    }
    async function testSelectedProxySources(button) {
      const ids = selectedProxySourceIDs();
      if (!ids.length) {
        syncProxySourceSelectionUI();
        toast(i18n.t('ui.select_proxies_to_test'), 'error');
        return;
      }
      setBusy(button, true);
      try {
        const task = await api('proxy.sources.test_selected', { source_ids: ids });
        toast(i18n.t('ui.proxy_connectivity_submitted', { count: ids.length }));
        const result = await waitForTask(task, i18n.t('ui.test_selected_proxies_label'));
        const counts = result?.counts || {};
        const reachable = Number(counts.reachable || 0);
        const unreachable = Number(counts.unreachable || 0);
        const failed = Number(counts.failed || 0);
        const message = i18n.t('ui.proxy_test_complete', { reachable: reachable, unreachable: unreachable, failed: failed });
        toast(message, unreachable || failed ? 'error' : 'success');
        await loadProxyData();
        renderProxySources();
        renderResin();
      } catch (error) {
        const counts = error.result?.counts || {};
        if (Object.keys(counts).length) {
          toast(i18n.t('ui.proxy_test_cancelled', { reachable: Number(counts.reachable || 0), unreachable: Number(counts.unreachable || 0), pending: Number(counts.pending || 0) }), 'error');
          await loadProxyData();
          renderProxySources();
          renderResin();
        } else {
          toast(error.message, 'error');
        }
      }
      finally { setBusy(button, false); }
    }
    async function deleteSelectedProxySources(button) {
      const ids = selectedProxySourceIDs();
      if (!ids.length) {
        syncProxySourceSelectionUI();
        toast(i18n.t('ui.select_proxies_to_delete'), 'error');
        return;
      }
      const confirmed = await confirmAction({
        title: esc(i18n.t('ui.delete_selected_proxies_title')),
        detail: i18n.t('ui.delete_selected_proxies_detail', { count: ids.length }),
        confirmLabel: esc(i18n.t('ui.delete_selected_proxies_confirm')),
        icon: 'x',
        tone: 'danger'
      });
      if (!confirmed) return;
      setBusy(button, true);
      try {
        const task = await api('proxy.sources.delete_selected', { source_ids: ids, force: true });
        toast(i18n.t('ui.proxy_delete_submitted', { count: ids.length }));
        const result = await waitForTask(task, i18n.t('ui.delete_selected_proxies_task'));
        const items = Array.isArray(result?.items) ? result.items : [];
        const deleted = new Set(items.filter((item) => item.status === 'deleted').map((item) => item.source_id));
        deleted.forEach((id) => state.proxySourceSelection.delete(id));
        const failed = Number(result?.counts?.failed || 0);
        if (failed) {
          const message = i18n.t('ui.delete_selected_proxies_partial', { deleted: deleted.size, failed: failed });
          toast(message, 'error');
        } else {
          state.proxySourceSelection.clear();
          toast(i18n.t('ui.delete_selected_proxies_success', { deleted: deleted.size }), 'success');
        }
        await loadProxyData();
        renderProxySources();
        renderResin();
      } catch (error) {
        const items = Array.isArray(error.result?.items) ? error.result.items : [];
        const deleted = new Set(items.filter((item) => item.status === 'deleted').map((item) => item.source_id));
        deleted.forEach((id) => state.proxySourceSelection.delete(id));
        if (deleted.size) {
          toast(i18n.t('ui.delete_selected_proxies_partial_error', { message: error.message, deleted: deleted.size }), 'error');
          await loadProxyData();
          renderProxySources();
          renderResin();
        } else {
          toast(error.message, 'error');
        }
      }
      finally { setBusy(button, false); }
    }
    async function applyProxyTemplateSource(sourceID, button) {
      let preview;
      try { preview = await api('proxy.template.apply', { source_id: sourceID, dry_run: true }); }
      catch (error) { toast(error.message, 'error'); return; }
      const confirmed = await confirmAction({
        title: i18n.t('ui.apply_template_to_bound_accounts'),
        detail: i18n.t('ui.apply_template_detail', { count: Number(preview.count || preview.bound_count || 0) }),
        confirmLabel: i18n.t('ui.start_apply'),
        icon: 'refresh'
      });
      if (!confirmed) return;
      setBusy(button, true);
      try {
        const task = await api('proxy.template.apply', { source_id: sourceID });
        toast(i18n.t('ui.template_apply_submitted'), 'success');
        const result = await waitForTask(task, i18n.t("task.label.proxy_template_apply"));
        reportProxyResult(result, i18n.t("task.label.proxy_template_apply"));
        await loadProxyData();
        renderProxySources();
        renderResin();
      } catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    }
    async function confirmProxyBindingOptions(action, count, label) {
      const clear = action === 'clear' || action === 'clear-external';
      const policy = state.proxy?.auto_bind?.manual_clear_policy || 'rebind';
      const options = clear
        ? '<div class="field"><label for="proxyClearPolicy">' + i18n.t('ui.clear_auto_bind_after') + '</label><select id="proxyClearPolicy"><option value="global">' + i18n.t('ui.follow_global_setting') + i18n.t('ui.meta_separator') + (policy === 'pause' ? i18n.t('ui.pause_these_accounts') : i18n.t('ui.allow_automatic_rebinding')) + '</option><option value="rebind">' + i18n.t('ui.this_time_allow_rebind') + '</option><option value="pause">' + i18n.t('ui.this_time_pause_accounts') + '</option></select><span class="hint">' + i18n.t('ui.paused_accounts_remain_paused_hint') + '</span></div>'
        : '<div class="field-grid"><div class="field"><label for="proxyBindScope">' + i18n.t('ui.proxy_scope') + '</label><select id="proxyBindScope"><option value="all">' + i18n.t('ui.all_proxies') + '</option><option value="nodes">' + i18n.t('ui.nodes_only') + '</option><option value="templates">' + i18n.t('ui.templates_only') + '</option><option value="resin">' + i18n.t('ui.resin_only') + '</option></select><span class="hint">' + i18n.t('ui.limit_auto_bind_proxy_types') + '</span></div><div class="field proxy-source-picker"><label for="proxyOperationSource">' + i18n.t('ui.specify_proxy_searchable') + '</label><div class="proxy-source-combobox"><input id="proxyOperationSource" type="text" autocomplete="off" role="combobox" aria-autocomplete="list" aria-expanded="false" aria-controls="proxyOperationSourceChoices" placeholder="' + i18n.t('ui.leave_blank_auto_assign') + '"><input id="proxyOperationSourceID" type="hidden" value=""><div class="proxy-source-picker-menu" id="proxyOperationSourceChoices" role="listbox" hidden></div></div><span class="hint">' + i18n.t('ui.candidates_filtered_by_scope') + '</span></div></div>';
      const detail = action === 'clear-external' ? i18n.t('ui.clear_local_proxy_only') : (clear ? i18n.t('ui.clear_proxy_binding_detail') : i18n.t('ui.bind_empty_proxy_detail'));
      const pending = confirmAction({ title: label, detail: i18n.t('ui.process_accounts_detail', { count: count }) + i18n.t('ui.meta_separator') + detail, confirmLabel: i18n.t(clear ? 'ui.start_clear' : 'ui.start_bind'), icon: clear ? 'x' : 'link', tone: clear ? 'danger' : '', wide: !clear, optionsHTML: options });
      const sourceField = document.getElementById('proxyOperationSource');
      if (sourceField) {
        const scopeField = document.getElementById('proxyBindScope');
        const menu = document.getElementById('proxyOperationSourceChoices');
        const hiddenField = document.getElementById('proxyOperationSourceID');
        let timer = 0;
        const scope = () => scopeField?.value || 'all';
        const renderChoices = () => renderProxyOperationSourceChoices(scope());
        const refreshChoices = () => searchProxyOperationSourceChoices(proxyOperationSourceQuery(), scope()).catch((error) => toast(error.message, 'error'));
        const openChoices = () => {
          const needsRefresh = menu.hidden;
          proxyOperationSourceActiveIndex = -1;
          renderChoices();
          setProxyOperationSourceMenuOpen(true);
          if (needsRefresh) refreshChoices();
        };
        renderChoices();
        scopeField?.addEventListener('change', () => {
          window.clearTimeout(timer);
          sourceField.value = '';
          hiddenField.value = '';
          state.proxyOperationSourceChoices = [];
          setProxyOperationSourceMenuOpen(false);
          renderChoices();
          sourceField.focus();
        });
        sourceField.addEventListener('focus', openChoices);
        sourceField.addEventListener('click', openChoices);
        sourceField.addEventListener('input', () => {
          window.clearTimeout(timer);
          hiddenField.value = '';
          proxyOperationSourceActiveIndex = -1;
          renderChoices();
          setProxyOperationSourceMenuOpen(true);
          timer = window.setTimeout(refreshChoices, 250);
        });
        menu.addEventListener('mousedown', (event) => event.preventDefault());
        menu.addEventListener('click', (event) => {
          const option = event.target.closest?.('[data-proxy-source-option]');
          if (!option) return;
          event.preventDefault();
          chooseProxyOperationSource(option.dataset.proxySourceOption);
        });
        sourceField.addEventListener('keydown', (event) => {
          if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault();
            if (menu.hidden) openChoices();
            else renderChoices();
            moveProxyOperationSourceSelection(event.key === 'ArrowDown' ? 1 : -1, scope());
            return;
          }
          if (event.key === 'Enter' && !menu.hidden && proxyOperationSourceActiveIndex >= 0) {
            const source = proxyOperationSourceVisibleChoices[proxyOperationSourceActiveIndex];
            if (source && chooseProxyOperationSource(source.id)) event.preventDefault();
            return;
          }
          if (event.key === 'Escape' && !menu.hidden) {
            event.preventDefault();
            event.stopPropagation?.();
            setProxyOperationSourceMenuOpen(false);
            return;
          }
          if (event.key === 'Tab') setProxyOperationSourceMenuOpen(false);
        });
        sourceField.addEventListener('blur', () => {
          window.setTimeout(() => {
            if (document.activeElement !== sourceField) setProxyOperationSourceMenuOpen(false);
          }, 100);
        });
      }
      if (!await pending) return null;
      if (clear) {
        const value = valueOf('proxyClearPolicy');
        return { exclude_auto_bind: value === 'global' ? null : value === 'pause' };
      }
      const sourceID = proxyOperationSourceValue();
      return { source_scope: valueOf('proxyBindScope') || 'all', source_ids: sourceID ? [sourceID] : [] };
    }
    async function setProxyAutoBindSelection(authIDs, excluded, button) {
      if (!authIDs.length) return;
      setBusy(button, true);
      let updated = 0;
      try {
        for (let start = 0; start < authIDs.length; start += 10000) {
          const batch = authIDs.slice(start, start + 10000);
          const result = await waitForTask(await api('proxy.bindings.set_auto_bind', { auth_ids: batch, excluded: excluded }), excluded ? i18n.t("ui.pause_auto_bind") : i18n.t("ui.resume_auto_bind"));
          updated += Number(result.count ?? result.updated?.length ?? batch.length);
          document.getElementById('proxyBindingStatus').textContent = i18n.t('ui.auto_bind_progress', { action: i18n.t(excluded ? 'ui.pause' : 'ui.resume'), processed: updated, total: authIDs.length });
        }
        toast(i18n.t(excluded ? 'ui.batch_auto_bind_paused' : 'ui.batch_auto_bind_resumed', { count: updated }), 'success');
        await reloadList('leases');
      } catch (error) { toast(i18n.t('ui.partial_account_operation_failed', { count: updated, error: error.message }), 'error'); }
      finally { setBusy(button, false); }
    }
    async function retryProxySourceSync(sourceID, button) {
      setBusy(button, true);
      try { const label = i18n.t('ui.retry_proxy_sync'); const result = await waitForTask(await api('proxy.sources.retry_sync', { source_id: sourceID }), label); reportProxyResult(result, label); await loadProxyData(); renderProxySources(); }
      catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    }
    async function runProxyAccountBatches(action, authIDs, options, label) {
      const totals = { count: 0, succeeded: 0, skipped: 0, failed: 0, pending: 0 };
      for (let start = 0; start < authIDs.length; start += 10000) {
        const batch = authIDs.slice(start, start + 10000);
        const result = await waitForTask(await api(action, Object.assign({ auth_ids: batch }, options)), label);
        const counts = proxyResultCounts(result);
        for (const key of Object.keys(totals)) totals[key] += counts[key];
        const failures = new Set(proxySyncFailures(result).map((item) => item.auth_id));
        if (!counts.failed || Array.isArray(result?.items) && result.items.length >= counts.count) batch.forEach((id) => { if (!failures.has(id)) state.leaseSelection.delete(id); });
        document.getElementById('proxyBindingStatus').textContent = i18n.t('ui.proxy_binding_progress', { label: label, processed: Math.min(start + batch.length, authIDs.length), total: authIDs.length, succeeded: totals.succeeded, failed: totals.failed });
      }
      return totals;
    }
    async function runProxyAction(action, authID, button) {
      if (!authID) return;
      if (action === 'pause-auto' || action === 'resume-auto') { await setProxyAutoBindSelection([authID], action === 'pause-auto', button); return; }
      const label = action === 'bind' ? i18n.t('action.proxy_bind')
        : action === 'rotate' ? i18n.t('action.proxy_rotate')
          : action === 'clear' ? i18n.t('action.proxy_clear')
            : action === 'clear-external' ? i18n.t('ui.clear_external_proxy')
              : i18n.t('ui.proxy_operations');
      let options = {};
      if (action === 'bind') {
        options = await confirmProxyBindingOptions(action, 1, label);
        if (options == null) return;
      }
      setBusy(button, true);
      try {
        const apiAction = action === 'bind' ? 'proxy.bindings.bind' : (action === 'rotate' ? 'proxy.bindings.rotate' : 'proxy.bindings.clear');
        const params = Object.assign({ auth_id: authID }, options);
        if (action === 'clear' || action === 'clear-external') params.allow_external = true;
        const task = await api(apiAction, params);
        toast(i18n.t('ui.task_submitted_label', { label: label }), 'success');
        const result = await waitForTask(task, label);
        reportProxyResult(result, label);
        await refreshAll(true);
      } catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    }
    async function runSelectedProxyAction(action, button) {
      const authIDs = Array.from(state.leaseSelection);
      if (!authIDs.length) { toast(i18n.t("ui.select_accounts_first"), 'success'); return; }
      const label = action === 'bind' ? i18n.t('ui.bind_selected_proxy') : i18n.t('ui.clear_selected');
      const options = action === 'bind' ? await confirmProxyBindingOptions(action, authIDs.length, label) : {};
      if (options == null) return;
      setBusy(button, true);
      try {
        const apiAction = action === 'bind' ? 'proxy.bindings.bind' : 'proxy.bindings.clear';
        const params = action === 'clear' ? Object.assign({}, options, { allow_external: true }) : options;
        const result = await runProxyAccountBatches(apiAction, authIDs, params, label);
        reportProxyResult(result, label);
        await refreshAll(true);
      } catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    }
    async function bindAllUnbound(button) {
      const options = await confirmProxyBindingOptions('bind', Number(state.proxy?.binding_counts?.unbound || 0), i18n.t("task.label.proxy_bind_unbound"));
      if (options == null) return;
      setBusy(button, true);
      try {
        const task = await api('proxy.bind_unbound', Object.assign({ dry_run: false }, options));
        toast(i18n.t('ui.proxy_bind_submitted'), 'success');
        const result = await waitForTask(task, i18n.t('task.label.proxy_bind_unbound'));
        state.leaseSelection.clear();
        reportProxyResult(result, i18n.t('task.label.proxy_bind_unbound'));
        await refreshAll(true);
      } catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    }
    async function runProxyAutoBindNow(button) {
      setBusy(button, true);
      try {
        const result = await api('proxy.auto_bind.run');
        state.proxy = state.proxy || {};
        state.proxy.auto_bind_runtime = result.runtime;
        renderProxyAutoBind();
        toast(i18n.t('ui.auto_bind_scan_triggered_toast'), 'success');
        window.setTimeout(() => refreshResin(null), 1500);
      } catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    }
    function appendProxyGroupValues(group, prefix, values) {
      if (prefix.length !== group.childIndex || prefix.some((value) => !value)) throw new Error(i18n.t('ui.parent_path_incomplete'));
      if (!values.length || values.some((value) => !value)) throw new Error(i18n.t('ui.child_value_required'));
      const appended = [];
      const existingPrefixes = new Set(group.rows.map((row) => JSON.stringify(row.slice(0, group.childIndex + 1))));
      const completedPrefixes = new Set();
      values.forEach((value) => {
        const fullPrefix = prefix.concat(value);
        const key = JSON.stringify(fullPrefix);
        if (existingPrefixes.has(key)) return;
        existingPrefixes.add(key);
        const row = group.columns.map((_, index) => fullPrefix[index] || '');
        appended.push(row);
        for (let depth = 1; depth < fullPrefix.length; depth += 1) completedPrefixes.add(JSON.stringify(fullPrefix.slice(0, depth)));
      });
      const retained = group.rows.filter((row) => {
        const missing = row.findIndex((value) => !value);
        if (missing <= 0 || row.slice(missing).some(Boolean)) return true;
        return !completedPrefixes.has(JSON.stringify(row.slice(0, missing)));
      });
      group.rows = proxyRowsUnique(retained.concat(appended));
      return appended.length;
    }
    function bulkFillProxyGroup(index) {
      const card = document.querySelector('[data-group-card="' + index + '"]');
      const group = state.proxyEditing?.groups.get(Number(index));
      if (!card || !group) return;
      try {
        const raw = card.querySelector('[data-group-bulk-values]').value;
        const prefix = group.columns.slice(0, group.childIndex).map((_, column) => group.path[column] || '');
        const values = splitProxyEnum(raw, proxyGroupSeparator(group.columns[group.childIndex]));
        const added = appendProxyGroupValues(group, prefix, values);
        group.bulkDrafts.delete(proxyGroupBulkKey(group));
        group.error = '';
        markProxyEditorDirty();
        renderProxyGroup(index);
        syncProxyPlaceholderCards();
        renderProxyTemplateConditions();
        toast(i18n.t('ui.group_values_appended', { count: added, continue: group.childIndex < group.columns.length - 1 ? i18n.t('ui.continue_remaining_columns') : '' }), 'success');
      } catch (error) { group.error = error.message; card.querySelector('[data-group-error]').textContent = error.message; }
    }
    async function changeProxyGroupColumns(card, group, input) {
      if (Array.from(group.bulkDrafts.values()).some((value) => value.trim()) || group.leafDrafts.size || group.importText.trim() && group.importRevision !== group.importAppliedRevision) { group.error = i18n.t('ui.apply_or_clear_drafts_before_columns'); input.value = group.columns.join(', '); renderProxyGroup(group.index); return; }
      const next = input.value.split(',').map((value) => value.trim()).filter(Boolean);
      if (new Set(next).size !== next.length) { group.error = i18n.t('ui.duplicate_column_names'); renderProxyGroup(group.index); return; }
      const removed = group.columns.filter((column) => !next.includes(column));
      if (removed.length && group.rows.length && !await confirmAction({ title: i18n.t('ui.delete_related_columns_title'), detail: i18n.t('ui.delete_related_columns_detail', { rows: group.rows.length, columns: removed.join(i18n.t('ui.list_separator')) }), confirmLabel: i18n.t('ui.delete_these_columns'), tone: 'danger' })) { input.value = group.columns.join(', '); return; }
      const previous = group.columns;
      group.rows = group.rows.map((row) => next.map((column) => previous.includes(column) ? row[previous.indexOf(column)] || '' : ''));
      group.columns = next;
      group.path = next.map(() => '');
      group.childIndex = next.length > 3 ? 0 : Math.max(0, next.length - 1);
      group.error = '';
      markProxyEditorDirty();
      renderProxyGroup(group.index);
      syncProxyPlaceholderCards();
      renderProxyTemplateConditions();
    }
    async function importProxyGroupRows(group) {
      try {
        const delimiter = group.importFormat === 'tsv' ? '\t' : ',';
        const parsed = alignProxyImportedRows(group.columns, parseProxyDelimitedTable(group.importText, delimiter), group.importHeader);
        const unique = proxyRowsUnique(parsed);
        const before = group.rows.length;
        const next = proxyRowsUnique((group.importMode === 'replace' ? [] : group.rows).concat(unique));
        const duplicate = parsed.length - (group.importMode === 'replace' ? next.length : next.length - before);
        if (group.importMode === 'replace' && before && !await confirmAction({ title: i18n.t('ui.replace_group_draft_title'), detail: i18n.t('ui.replace_group_draft_detail', { before: before, imported: unique.length, duplicates: parsed.length - unique.length }), confirmLabel: i18n.t('ui.replace_draft'), tone: 'danger' })) return;
        group.rows = next;
        group.importAppliedRevision = group.importRevision;
        group.error = '';
        markProxyEditorDirty();
        renderProxyGroup(group.index);
        syncProxyPlaceholderCards();
        renderProxyTemplateConditions();
        toast(i18n.t('ui.group_import_summary', { imported: unique.length, duplicates: Math.max(0, duplicate), replaced: group.importMode === 'replace' ? i18n.t('ui.group_import_replace_suffix', { count: before }) : '' }), 'success');
      } catch (error) {
        group.error = i18n.t('ui.group_import_not_applied', { error: error.message });
        renderProxyGroup(group.index);
      }
    }
    async function deleteProxyGroupPath(group, value) {
      const prefix = group.columns.slice(0, group.childIndex).map((_, index) => group.path[index] || '');
      if (prefix.some((item) => !item)) return;
      if (value != null) prefix.push(value);
      const count = group.rows.filter((row) => proxyRowsMatchPrefix(row, prefix)).length;
      if (!count || !await confirmAction({ title: i18n.t('ui.delete_parent_branch_title'), detail: i18n.t('ui.delete_parent_branch_detail', { count: count }), confirmLabel: i18n.t('ui.delete_branch'), tone: 'danger' })) return;
      group.rows = group.rows.filter((row) => !proxyRowsMatchPrefix(row, prefix));
      for (const key of group.leafDrafts.keys()) if (proxyRowsMatchPrefix(JSON.parse(key), prefix)) group.leafDrafts.delete(key);
      if (group.editValue != null && proxyRowsMatchPrefix(group.editPrefix.concat(group.editValue), prefix)) group.editValue = null;
      markProxyEditorDirty(); renderProxyGroup(group.index); syncProxyPlaceholderCards(); renderProxyTemplateConditions();
    }
    async function renameProxyGroupLeaf(group, card) {
      const next = card.querySelector('[data-group-leaf-rename]').value.trim();
      const oldPath = group.editPrefix.concat(group.editValue);
      const nextPath = group.editPrefix.concat(next);
      if (!next) { group.error = i18n.t('ui.child_value_required'); renderProxyGroup(group.index); return; }
      if (!group.rows.some((row) => proxyRowsMatchPrefix(row, oldPath))) { group.error = i18n.t('ui.group_no_longer_exists'); renderProxyGroup(group.index); return; }
      const exists = next !== group.editValue && group.rows.some((row) => proxyRowsMatchPrefix(row, nextPath));
      if (exists && !await confirmAction({ title: i18n.t('ui.merge_same_child_title'), detail: i18n.t('ui.merge_same_child_detail'), confirmLabel: i18n.t('ui.confirm_merge') })) return;
      group.rows = proxyRowsUnique(group.rows.map((row) => proxyRowsMatchPrefix(row, oldPath) ? nextPath : row));
      group.leafDrafts.delete(JSON.stringify(oldPath)); group.editValue = null; group.error = '';
      markProxyEditorDirty(); renderProxyGroup(group.index); syncProxyPlaceholderCards(); renderProxyTemplateConditions();
    }
    async function renameProxyGroupPath(group, card) {
      const prefix = group.columns.slice(0, group.childIndex).map((_, index) => group.path[index] || '');
      const next = card.querySelector('[data-group-rename-value]').value.trim();
      if (!prefix.length || prefix.some((value) => !value) || !next) { group.error = i18n.t('ui.select_full_path_and_new_name'); renderProxyGroup(group.index); return; }
      const target = prefix.slice(); target[target.length - 1] = next;
      if (JSON.stringify(prefix) === JSON.stringify(target)) return;
      const sourceRows = group.rows.filter((row) => proxyRowsMatchPrefix(row, prefix));
      const exists = group.rows.some((row) => proxyRowsMatchPrefix(row, target));
      if (!await confirmAction({ title: i18n.t(exists ? 'ui.merge_same_parent_title' : 'ui.rename_parent'), detail: i18n.t('ui.rename_parent_detail', { count: sourceRows.length, outcome: i18n.t(exists ? 'ui.merge_parent_outcome' : 'ui.keep_other_parent_paths') }), confirmLabel: i18n.t(exists ? 'ui.confirm_merge' : 'ui.rename') })) return;
      group.rows = proxyRowsUnique(group.rows.map((row) => proxyRowsMatchPrefix(row, prefix) ? target.concat(row.slice(prefix.length)) : row));
      group.path = target.concat(group.path.slice(prefix.length));
      markProxyEditorDirty(); renderProxyGroup(group.index); syncProxyPlaceholderCards(); renderProxyTemplateConditions();
    }
    async function copyProxyGroupPath(group, card) {
      const source = group.columns.slice(0, group.childIndex).map((_, index) => group.path[index] || '');
      const target = Array.from(card.querySelectorAll('[data-group-copy-target]')).map((field) => field.value.trim());
      if (!source.length || source.some((value) => !value) || target.length !== source.length || target.some((value) => !value)) { group.error = i18n.t('ui.source_target_paths_required'); renderProxyGroup(group.index); return; }
      const copied = group.rows.filter((row) => proxyRowsMatchPrefix(row, source)).map((row) => target.concat(row.slice(source.length)));
      const replace = card.querySelector('[data-group-copy-mode]').value === 'replace';
      const covered = replace ? group.rows.filter((row) => proxyRowsMatchPrefix(row, target)).length : 0;
      if (!copied.length || !await confirmAction({ title: i18n.t('ui.copy_parent_branch_title'), detail: i18n.t('ui.copy_parent_branch_detail', { count: copied.length, outcome: replace ? i18n.t('ui.copy_replace_outcome', { count: covered }) : i18n.t('ui.copy_dedupe_outcome') }), confirmLabel: i18n.t('ui.copy_branch') })) return;
      const retained = replace ? group.rows.filter((row) => !proxyRowsMatchPrefix(row, target)) : group.rows;
      group.rows = proxyRowsUnique(retained.concat(copied));
      markProxyEditorDirty(); renderProxyGroup(group.index); syncProxyPlaceholderCards(); renderProxyTemplateConditions();
    }
    function taskStatusLabel(value) {
      return localizedCode('task.status.', value, 'task.status.unknown');
    }
    function taskCategoryLabel(value) {
      return localizedCode('task.category.', value, 'task.category.generic');
    }
    function taskKindLabel(value) {
      return localizedCode('task.kind.', value, 'task.kind.default');
    }
    function taskStatusClass(value) {
      const status = String(value || '').toLowerCase();
      if (['running', 'starting', 'queued', 'pending', 'waiting', 'prepared'].includes(status)) return 'running';
      if (['succeeded', 'probed', 'restored', 'external_restored', 'adopted', 'disabled', 'deleted', 'bound', 'cleared', 'rotated', 'updated', 'created', 'reachable', 'finalized', 'finished'].includes(status)) return 'succeeded';
      if (['failed', 'error', 'interrupted', 'unreachable', 'unavailable', 'conflict', 'rotation_unavailable', 'partial', 'needs_reconcile', 'reconcile_required', 'commit_unknown', 'permanent_failure', 'credential_blocked'].includes(status)) return 'failed';
      if (['skipped', 'superseded', 'unchanged', 'already_bound', 'external_left_disabled', 'protected', 'paused', 'idle', 'cancelled', 'canceled', 'dry_run', 'external', 'drifted', 'unbound', 'removed', 'retry_wait'].includes(status)) return 'paused';
      return '';
    }
    function taskPercent(task) {
      const total = Math.max(0, Number(task && task.total || 0));
      const completed = Math.max(0, Number(task && task.completed || 0));
      return total > 0 ? Math.max(0, Math.min(100, Math.round(completed / total * 100))) : 0;
    }
    function taskProgressText(task) {
      if (!task) return '';
      if (task.current_stage || task.progress) return task.current_stage || task.progress;
      if (task.error) return friendlyApiMessage(task.error, task.error_code);
      if (['queued', 'running'].includes(task.status)) return i18n.t('ui.preparing');
      const total = Number(task.total || 0);
      return total > 0 ? i18n.t('ui.processed_count', { completed: Number(task.completed || 0), total: total }) : i18n.t('ui.no_item_details');
    }
    function taskDurationLabel(ms) {
      const value = Number(ms || 0);
      if (!value) return '-';
      if (value < 1000) return i18n.t('ui.duration_milliseconds', { value: value });
      if (value < 60000) return i18n.t('ui.duration_seconds', { value: (value / 1000).toFixed(value < 10000 ? 1 : 0) });
      if (value < 3600000) return i18n.t('ui.duration_minutes_seconds', { minutes: Math.floor(value / 60000), seconds: Math.round((value % 60000) / 1000) });
      return i18n.t('ui.duration_hours_minutes', { hours: Math.floor(value / 3600000), minutes: Math.round((value % 3600000) / 60000) });
    }
