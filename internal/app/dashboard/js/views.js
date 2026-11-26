    function renderPager(kind, result) {
      const config = PAGE_CONFIG[kind];
      const el = document.getElementById(config.pagerId);
      if (!result.total) {
        el.hidden = true;
        el.innerHTML = '';
        return;
      }
      el.hidden = false;
      const sizeOptions = [20, 50, 100].map((size) => '<option value="' + size + '"' + (size === result.size ? ' selected' : '') + '>' + size + '</option>').join('');
      el.innerHTML = '<span class="pager-summary">' + i18n.t('ui.of_page', { start: result.start + 1, end: result.end, itemUnit: config.itemUnit, total: result.total, unit: config.unit, page: result.page, pages: result.pages }) + '</span>' +
        '<div class="pager-controls"><select class="control pager-size" data-page-size-kind="' + kind + '" aria-label="' + i18n.t("ui.page_size") + '">' + sizeOptions + '</select>' +
        '<button class="btn btn-xs" type="button" data-page-kind="' + kind + '" data-page-action="first" ' + (result.page <= 1 ? 'disabled' : '') + '>' + i18n.t('ui.first_page') + '</button>' +
        '<button class="btn btn-xs" type="button" data-page-kind="' + kind + '" data-page-action="prev" ' + (result.page <= 1 ? 'disabled' : '') + '>' + i18n.t('ui.previous_page') + '</button>' +
        '<button class="btn btn-xs" type="button" data-page-kind="' + kind + '" data-page-action="next" ' + (result.page >= result.pages ? 'disabled' : '') + '>' + i18n.t('ui.next_page') + '</button>' +
        '<button class="btn btn-xs" type="button" data-page-kind="' + kind + '" data-page-action="last" ' + (result.page >= result.pages ? 'disabled' : '') + '>' + i18n.t('ui.last_page') + '</button></div>';
    }
    function applyPagerAction(kind, action) {
      const config = PAGE_CONFIG[kind];
      if (!config) return;
      const result = pagedItems(kind);
      let target = result.page;
      if (action === 'first') target = 1;
      else if (action === 'prev') target = result.page - 1;
      else if (action === 'next') target = result.page + 1;
      else if (action === 'last') target = result.pages;
      state[config.pageKey] = Math.min(Math.max(1, target), result.pages);
      if (config.metadata) reloadList(kind);
      else config.render();
    }
    function recentEventHTML(event) {
      const tone = event.class ? 'bad' : (event.action === 'restore' ? 'good' : '');
      return '<div class="recent-item"><span class="event-dot ' + esc(tone) + '"></span><div style="min-width:0"><div class="recent-title">' + esc(actionLabel(event.action)) + i18n.t('ui.meta_separator') + esc(event.class ? reasonLabel(event.class) : resultLabel(event.result)) + '</div><div class="recent-detail">' + esc(event.auth_id || i18n.t('ui.system_event')) + (event.detail ? i18n.t('ui.meta_separator') + esc(localizeDetail(event.detail)) : '') + '</div></div><time class="recent-time">' + esc(formatTime(event.time)) + '</time></div>';
    }
    function renderOverview() {
      const overview = state.overview || {};
      const policy = state.policy || overview.policy || {};
      const states = overview.states || state.stateCounts || {};
      const total = Number(overview.auth_count != null ? overview.auth_count : (state.accountsTotal || state.accounts.length));
      const healthy = Number(states.healthy || 0);
      const quarantined = Number(states.cooling || 0) + Number(states.disabled_by_guard || 0);
      const attention = Number(states.suspect || 0) + Number(states.degraded || 0) + quarantined;
      const enabled = overview.enabled !== false;
      document.getElementById('overviewMetrics').innerHTML = [
        metricCard(enabled ? 'shield' : 'power', i18n.t(enabled ? 'ui.running_service' : 'ui.stopped'), i18n.t(enabled ? 'ui.guard_service' : 'ui.plugin_disabled'), i18n.t(enabled ? 'ui.receiving_quality_events' : 'ui.enable_plugin_in_cpa_config'), enabled ? 'good' : 'bad'),
        metricCard('activity', modeLabel(policy), i18n.t('ui.detection_mode'), i18n.t(policy.guard === 'transparent' ? 'ui.switch_before_first_visible_chunk' : (policy.guard === 'passive' ? 'ui.observe_real_requests_only' : 'ui.manage_accounts_and_proxies_only'))),
        metricCard('users', num(total), i18n.t('ui.managed_accounts'), i18n.t('ui.healthy_accounts_count', { count: num(healthy) }), 'info'),
        metricCard('alert', num(attention), i18n.t('ui.accounts_needing_attention'), i18n.t('ui.quarantined_count', { count: num(quarantined) }), attention ? 'warn' : 'good')
      ].join('');
      const since = Date.now() - 24 * 60 * 60 * 1000;
      const recent = state.events.filter((event) => {
        const time = Date.parse(event.time);
        return !Number.isNaN(time) && time >= since;
      });
      const count = (predicate) => recent.filter(predicate).length;
      const probes = count((event) => event.action === 'probe');
      const passive = count((event) => event.action === 'observe' || event.action === 'transparent_attempt');
      const anomalies = count((event) => Boolean(event.class));
      const actions = count((event) => event.action === 'guard_disable');
      const rotations = count((event) => /^(proxy|resin)_rotate$/.test(String(event.action || '')));
      document.getElementById('overviewStats').innerHTML = [
        statCard('activity', num(recent.length), i18n.t('ui.events_24h'), i18n.t('ui.passive_audits_count', { count: passive })),
        statCard('play', num(probes), i18n.t('ui.active_probes'), i18n.t('ui.recovery_and_manual_probes')),
        statCard('terminal', num(passive), i18n.t('ui.passive_audits'), i18n.t('ui.from_real_user_requests')),
        statCard('alert', num(anomalies), i18n.t('ui.anomaly_hits'), i18n.t('ui.anomalies_and_probe_failures')),
        statCard('power', num(actions), i18n.t('ui.automatic_actions'), i18n.t('ui.cooldown_or_permanent_disable')),
        statCard('rotate', num(rotations), i18n.t('ui.proxy_rotation'), i18n.t('ui.proxy_rebinding'))
      ].join('');
      const order = ['healthy', 'suspect', 'degraded', 'cooling', 'quota_exhausted', 'credential_invalid', 'disabled_by_guard', 'externally_disabled', 'unknown'];
      const sum = order.reduce((totalCount, key) => totalCount + Number(states[key] || 0), 0);
      document.getElementById('qualityBars').innerHTML = order.map((key) => {
        const value = Number(states[key] || 0);
        const width = sum ? Math.round(value / sum * 100) : 0;
        return '<div class="bar-row"><span class="bar-label">' + esc(stateLabel(key)) + '</span><div class="bar-track"><div class="bar-fill ' + esc(stateTone(key)) + '" style="width:' + width + '%"></div></div><span class="bar-value">' + value + '</span></div>';
      }).join('');
      document.getElementById('recentEvents').innerHTML = state.events.slice(0, 6).map(recentEventHTML).join('') || '<div class="empty"><svg class="icon"><use href="#i-terminal"></use></svg><div>' + esc(i18n.t('ui.no_quality_events')) + '</div></div>';
      const quality = policy.quality || {};
      const policyHTML = [
        summaryItem(i18n.t('ui.tps_thresholds'), esc(i18n.t('ui.soft_hard_values', { soft: num(quality.soft_tps, 1), hard: num(quality.hard_tps, 1) }))),
        summaryItem(i18n.t('ui.consecutive_thresholds'), esc(i18n.t('ui.soft_error_missing_thinking_values', { soft: num(quality.consecutive_soft || 2), errors: num(quality.consecutive_errors || 2), thinking: num(quality.consecutive_missing_thinking || 1) }))),
        summaryItem(i18n.t('ui.automatic_actions'), esc(autoActionLabel(policy.auto_action))),
        summaryItem(i18n.t('ui.missing_thinking_cooldown'), esc(quality.missing_thinking_cooldown || '-')),
        summaryItem(i18n.t('ui.strike_decay'), esc(i18n.t('ui.strike_decay_value', { hours: String(quality.strike_decay_after ?? 48), healthy: String(quality.strike_decay_min_healthy ?? 10) }))),
        summaryItem(i18n.t('ui.detection_and_verification'), esc(i18n.t('ui.thinking_detection_cross_verification', { thinking: i18n.t(quality.thinking_guard !== false ? 'ui.on' : 'ui.off'), cross: i18n.t((quality.cross_verify && quality.cross_verify !== 'off') ? 'ui.partially_enabled' : 'ui.off') }))),
        summaryItem(i18n.t('ui.probe_model'), esc(policy.probe && policy.probe.model || '-')),
        summaryItem(i18n.t('ui.task_scan_and_concurrency'), esc(i18n.t('ui.scan_concurrency_queue_values', { scan: String(policy.probe && policy.probe.scan_interval || '15s'), concurrency: num(policy.probe && policy.probe.concurrency || 1), queue: num(policy.probe && policy.probe.queue_size || 10000) }))),
        summaryItem(i18n.t('ui.state_file'), esc(overview.state_file || '-'), { truncate: true, title: overview.state_file || '-' }),
        summaryItem(i18n.t('ui.event_retention'), esc(num(policy.events_limit || 0)))
      ].join('');
      document.getElementById('policySummary').innerHTML = policyHTML;
      const probe = policy.probe || {};
      document.getElementById('settingsPolicySummary').innerHTML = [
        policyChip(i18n.t('ui.detection_mode'), modeLabel(policy)),
        policyChip(i18n.t('ui.tps_thresholds'), i18n.t('ui.soft_hard_values', { soft: num(quality.soft_tps, 1), hard: num(quality.hard_tps, 1) })),
        policyChip(i18n.t('ui.consecutive_thresholds'), i18n.t('ui.soft_error_missing_thinking_values', { soft: num(quality.consecutive_soft || 2), errors: num(quality.consecutive_errors || 2), thinking: num(quality.consecutive_missing_thinking || 2) })),
        policyChip(i18n.t('ui.cross_verify'), i18n.t('ui.cross_verify_' + (quality.cross_verify || 'thinking'))),
        policyChip(i18n.t('ui.transparent_stream_protection'), i18n.t('ui.transparent_budget_values', { hold: quality.hold_timeout || '30s', idle: quality.stream_idle_timeout || '90s', attempt: quality.attempt_timeout || '600s', request: quality.request_timeout || '900s', attempts: num(quality.max_attempts || 4) })),
        policyChip(i18n.t('ui.active_probes'), i18n.t('ui.scan_concurrency_queue_values', { scan: String(probe.scan_interval || '15s'), concurrency: num(probe.concurrency || 1), queue: num(probe.queue_size || 10000) }))
      ].join('');
      document.getElementById('policyModeBadge').textContent = modeLabel(policy);
      const proxySummary = state.proxy || {};
      const proxyCounts = proxySummaryCounts(proxySummary);
      const proxySources = state.proxySources || [];
      const autoBind = proxySummary.auto_bind || {};
      document.getElementById('resinSummary').innerHTML = [
        summaryItem(i18n.t('ui.bindable_proxies'), esc(num(proxySummary.available_source_count ?? proxySummary.enabled_source_count ?? proxySources.filter((source) => source.enabled && source.valid).length))),
        summaryItem(i18n.t('ui.bound'), esc(num(proxyCounts.bound || 0))),
        summaryItem(i18n.t('ui.unbound'), esc(num(proxyCounts.unbound || 0))),
        summaryItem(i18n.t('ui.external_or_needs_review'), esc(num(Number(proxyCounts.external || 0) + Number(proxyCounts.read_failed || 0) + Number(proxyCounts.unknown || 0) + Number(proxyCounts.drifted || 0) + Number(proxyCounts.reconcile_required || 0)))),
        summaryItem(i18n.t('ui.auto_bind_label'), esc(i18n.t(autoBind.enabled ? 'ui.on' : 'ui.off'))),
        summaryItem(i18n.t('ui.auto_rotate'), esc(i18n.t(proxySummary.auto_rotate_after_quality_failure ? 'ui.enabled_2' : 'ui.disabled_2')))
      ].join('');
    }
    function accountFilteredList() {
      return state.accounts;
    }
    function selectedAccountAccounts() {
      return state.accounts.filter((account) => state.accountSelection.has(account.auth_id));
    }
    function selectedAccountIDs() {
      return Array.from(state.accountSelection);
    }
    function syncAccountSelectionUI() {
      const filtered = accountFilteredList();
      const current = pagedItems('accounts');
      const allCount = Number((state.overview || {}).auth_count || state.accountsTotal);
      const pageSelected = current.items.filter((account) => state.accountSelection.has(account.auth_id)).length;
      const selected = state.accountSelection.size;
      const selectedFiltered = filtered.filter((account) => state.accountSelection.has(account.auth_id)).length;
      const selectAll = document.getElementById('accountSelectAll');
      selectAll.disabled = current.items.length === 0;
      selectAll.checked = current.items.length > 0 && pageSelected === current.items.length;
      selectAll.indeterminate = pageSelected > 0 && pageSelected < current.items.length;
      selectAll.setAttribute('aria-label', i18n.t('ui.select_page_accounts'));
      selectAll.title = i18n.t('ui.select_page_accounts');
      const scope = current.items.length > 0 && pageSelected === current.items.length ? i18n.t('ui.page_all_included') : '';
      document.getElementById('accountSelectionCount').textContent = i18n.t('ui.selected_accounts', { count: selected, scope: scope });
      document.getElementById('accountClearSelectionBtn').hidden = selected === 0;
      const selectFiltered = document.getElementById('accountSelectFilteredBtn');
      const selectAllAccounts = document.getElementById('accountSelectAllBtn');
      selectFiltered.hidden = current.total === 0 || current.total === allCount;
      selectFiltered.textContent = i18n.t('ui.select_filtered_count', { count: current.total });
      selectAllAccounts.hidden = allCount === 0;
      selectAllAccounts.textContent = i18n.t('ui.select_all_accounts_count', { count: allCount });
      const probeButton = document.getElementById('accountProbeAllBtn');
      probeButton.disabled = selected === 0;
      probeButton.title = selected > 0 ? i18n.t('ui.probe_selected_accounts_title', { count: selected }) : i18n.t('ui.select_accounts_first');
      const restoreButton = document.getElementById('accountRestoreAllBtn');
      restoreButton.disabled = selected === 0;
      restoreButton.title = selected > 0 ? i18n.t('ui.restore_selected_accounts_action', { count: selected }) : i18n.t('ui.select_accounts_first');
      const disableButton = document.getElementById('accountDisableAllBtn');
      disableButton.disabled = selected === 0;
      disableButton.title = selected > 0 ? i18n.t('ui.disable_selected_accounts_action', { count: selected }) : i18n.t('ui.select_accounts_first');
    }
    function renderAccounts() {
      const overview = state.overview || {};
      const states = overview.states || state.stateCounts || {};
      const bound = proxySummaryCounts(state.proxy).bound;
      document.getElementById('accountMetrics').innerHTML = [
        metricCard('users', num(state.accountsTotal || state.accounts.length), i18n.t('ui.total_accounts'), i18n.t('ui.from_xai_subscription')),
        metricCard('shield', num(states.healthy || 0), i18n.t('ui.healthy_accounts'), i18n.t('ui.latest_quality_check_passed'), 'good'),
        metricCard('alert', num(Number(states.suspect || 0) + Number(states.degraded || 0)), i18n.t('ui.suspect_degraded'), i18n.t('ui.needs_attention_or_confirmation'), (Number(states.suspect || 0) + Number(states.degraded || 0)) ? 'warn' : 'good'),
        metricCard('database', num(bound), i18n.t('ui.managed_proxies'), i18n.t('ui.recorded_by_generic_proxy_module'), 'info')
      ].join('');
      const result = pagedItems('accounts');
      document.getElementById('accountRows').innerHTML = result.items.map(accountRow).join('');
      document.getElementById('accountsEmpty').hidden = result.total > 0;
      document.getElementById('accountsCount').textContent = i18n.t('ui.accounts_count_summary', { total: result.total, current: result.items.length });
      document.getElementById('accountsNote').textContent = i18n.t('ui.accounts_page_note', { count: state.accountPageSize });
      renderPager('accounts', result);
      syncAccountSelectionUI();
    }
    function attemptDetailHTML(account, text) {
      const detail = String(text == null ? '' : text);
      const attempt = account && account.last_attempt;
      if (!attempt) return esc(detail);
      const transportLabel = i18n.t('reason.detail.probe_transport_error');
      if (reasonText(attempt.reason, '') !== transportLabel) return esc(detail);
      const linkIndex = detail.indexOf(transportLabel);
      if (linkIndex < 0) return esc(detail);
      const href = eventFilterHash(account.auth_id, 'transport');
      const link = '<a class="event-log-link" href="' + esc(href) + '">' + esc(transportLabel) + '</a>';
      return esc(detail.slice(0, linkIndex)) + link + esc(detail.slice(linkIndex + transportLabel.length));
    }
    function accountRow(account) {
      const authID = esc(account.auth_id);
      const selected = state.accountSelection.has(account.auth_id);
      const proxyStatus = account.proxy_status || 'unbound';
      let proxyBadge = badge(i18n.t('proxy.binding.unbound'), '');
      if (proxyStatus === 'bound') proxyBadge = badge(i18n.t('proxy.binding.bound'), 'info');
      else if (proxyStatus === 'drifted') proxyBadge = badge(i18n.t('proxy.binding.drifted'), 'warn');
      else if (proxyStatus === 'reconcile_required') proxyBadge = badge(i18n.t('proxy.binding.reconcile_required'), 'bad');
      else if (proxyStatus === 'external') proxyBadge = badge(i18n.t('proxy.binding.external'), '');
      else if (proxyStatus === 'checking') proxyBadge = badge(i18n.t('proxy.binding.checking'), '');
      else if (proxyStatus === 'read_failed') proxyBadge = badge(i18n.t('proxy.binding.read_failed'), 'bad');
      else if (proxyStatus === 'unknown') proxyBadge = badge(i18n.t('proxy.binding.unknown'), 'bad');
      const proxyGeneration = Number(account.proxy_generation || 0) > 0 ? '<span class="proxy-generation">' + esc(i18n.t('ui.generation', { generation: account.proxy_generation })) + '</span>' : '';
      const proxyCell = '<div class="proxy-summary"><div class="proxy-summary-head">' + proxyBadge + proxyGeneration + '</div></div>';
      const cooldown = account.cooldown_until ? i18n.t('ui.cooldown_until', { time: formatFullTime(account.cooldown_until) }) : i18n.t('ui.no_cooldown');
      const metricDetail = i18n.t('ui.output_reasoning_ciphertext', { output: num(account.output_tokens), reasoning: num(account.reasoning_tokens), cipher: formatBytes(account.encrypted_bytes) });
      const isolatedByGuard = ['cooling', 'quota_exhausted', 'credential_invalid', 'disabled_by_guard'].includes(account.state);
      const disableReason = account.disable_reason ? reasonLabel(account.disable_reason) : '';
      const verdict = account.verdict || null;
      const reasonSummary = verdict
        ? verdictSummaryHTML(verdict, '')
        : verdictSummaryHTML(null, isolatedByGuard ? i18n.t('ui.account_disabled') : reasonLabel(account.last_reason));
      const evidenceSummary = verdictRowEvidence(verdict);
      const attemptDetail = evidenceSummary || (account.last_attempt ? attemptLabel(account.last_attempt, { includeTime: false }) : i18n.t('ui.last_observed', { time: formatTime(account.last_observed_at) }));
      const attemptDetailContent = evidenceSummary ? esc(evidenceSummary) : attemptDetailHTML(account, attemptDetail);
      const observedAt = account.last_observed_at ? i18n.t('ui.observed_ago', { time: relativeTime(account.last_observed_at) }) : '';
      const observedLine = observedAt ? '<div class="subline truncate-line" title="' + esc(formatFullTime(account.last_observed_at)) + '">' + esc(observedAt) + '</div>' : '';
      const reasonTextValue = disableReason ? i18n.t('ui.disable_reason', { reason: disableReason }) : '';
      const disableReasonDetail = reasonTextValue ? '<div class="subline truncate-line" title="' + esc(reasonTextValue) + '">' + esc(reasonTextValue) + '</div>' : '';
      const displayName = account.display_full || account.display || account.label || i18n.t('ui.unnamed_account');
      const displayID = account.auth_id_display || account.auth_id;
      return '<tr><td class="select-cell"><input type="checkbox" data-account-select="' + authID + '" aria-label="' + i18n.t('ui.select_account') + '" ' + (selected ? 'checked' : '') + '></td>' +
        '<td><div class="account-name truncate-line" title="' + esc(displayName) + '">' + esc(account.display || account.label || i18n.t('ui.unnamed_account')) + '</div><div class="subline truncate-line" title="' + esc(account.auth_id) + '">' + esc(displayID) + '</div></td><td class="status-cell nowrap">' + badge(stateLabel(account.state), stateTone(account.state)) + '</td><td><div class="truncate-line verdict-line" title="' + esc(reasonSummary.text) + '">' + reasonSummary.html + '</div><div class="subline truncate-line" title="' + esc(attemptDetail) + '">' + attemptDetailContent + '</div>' + disableReasonDetail + '</td><td><div class="metric-inline"><strong>' + esc(num(account.tps, 1)) + '</strong><span class="muted">TPS</span></div><div class="subline truncate-line" title="' + esc(metricDetail) + '">' + esc(metricDetail) + '</div></td><td class="proxy-cell">' + proxyCell + '</td><td class="time-cell"><div class="cell-primary truncate-line" title="' + esc(cooldown) + '">' + esc(cooldown) + '</div>' + observedLine + '</td>' + '<td class="actions-cell"><div class="actions"><button class="btn btn-xs" type="button" data-account-action="accounts.probe" data-auth="' + authID + '">' + icon('play') + esc(i18n.t('ui.probe')) + '</button><button class="btn btn-xs" type="button" data-open-account="' + authID + '">' + icon('eye') + esc(i18n.t('ui.details')) + '</button></div></td></tr>';
    }

    function leaseStatus(account) {
      if (account.proxy_status === 'checking') return 'checking';
      if (account.proxy_status === 'read_failed') return 'read_failed';
      if (account.proxy_status === 'unknown') return 'unknown';
      if (account.proxy_status === 'external') return 'external';
      if (account.proxy_status === 'drifted') return 'drifted';
      if (account.proxy_status === 'reconcile_required') return 'reconcile_required';
      if (account.proxy_managed || account.proxy_status === 'bound') return 'bound';
      return 'unbound';
    } function leaseFilteredList() {
      return state.leases;
    }
    function selectedLeaseAccounts() {
      return state.leases.filter((account) => state.leaseSelection.has(account.auth_id));
    }
    function syncLeaseSelectionUI() {
      const filtered = leaseFilteredList();
      const current = pagedItems('leases');
      const counts = proxySummaryCounts(state.proxy);
      const allCount = Number(state.overview?.auth_count || ['bound', 'unbound', 'external', 'drifted', 'checking', 'read_failed', 'unknown', 'reconcile_required'].reduce((sum, key) => sum + Number(counts[key] || 0), 0) || state.accountsTotal);
      const pageSelected = current.items.filter((account) => state.leaseSelection.has(account.auth_id)).length;
      const selected = state.leaseSelection.size;
      const selectedFiltered = filtered.filter((account) => state.leaseSelection.has(account.auth_id)).length;
      const selectAll = document.getElementById('leaseSelectAll');
      selectAll.disabled = current.items.length === 0;
      selectAll.checked = current.items.length > 0 && pageSelected === current.items.length;
      selectAll.indeterminate = pageSelected > 0 && pageSelected < current.items.length;
      selectAll.setAttribute('aria-label', i18n.t('ui.select_page_accounts'));
      selectAll.title = i18n.t('ui.select_page_accounts');
      const scope = current.items.length > 0 && pageSelected === current.items.length ? i18n.t('ui.page_all_included') : '';
      document.getElementById('leaseSelectionCount').textContent = i18n.t('ui.selected_accounts', { count: selected, scope: scope });
      document.getElementById('clearLeaseSelectionBtn').hidden = selected === 0;
      const selectFiltered = document.getElementById('selectFilteredBtn');
      const selectAllAccounts = document.getElementById('selectAllAccountsBtn');
      selectFiltered.hidden = current.total === 0 || current.total === allCount;
      selectFiltered.textContent = i18n.t('ui.select_filtered_count', { count: current.total });
      selectAllAccounts.hidden = allCount === 0;
      selectAllAccounts.textContent = i18n.t('ui.select_all_accounts_count', { count: allCount });
      const canBind = proxyCanBind();
      document.getElementById('bindSelectedBtn').disabled = selected === 0 || !canBind;
      document.getElementById('clearSelectedBtn').disabled = selected === 0;
      document.getElementById('pauseSelectedAutoBindBtn').disabled = selected === 0;
      document.getElementById('resumeSelectedAutoBindBtn').disabled = selected === 0;
    }
    function renderResin() {
      const proxy = state.proxy || {};
      const counts = proxySummaryCounts(proxy);
      const sources = state.proxySources || [];
      const canBind = proxyCanBind();
      const sourceTotal = Number(proxy.source_count ?? state.proxySourceList?.total ?? sources.length);
      const configured = sourceTotal > 0;
      const available = Number(proxy.available_source_count ?? sources.filter((source) => source.enabled && source.valid).length);
      const statusTotal = ['bound', 'unbound', 'external', 'drifted', 'checking', 'read_failed', 'unknown', 'reconcile_required'].reduce((sum, key) => sum + Number(counts[key] || 0), 0);
      const reportedBindingTotal = Number(proxy.binding_total);
      const bindingTotal = Number.isFinite(reportedBindingTotal) && reportedBindingTotal > 0 ? reportedBindingTotal : statusTotal;
      const needsReview = proxyNeedsReviewCount(counts);
      const bindingCoverage = bindingTotal ? Math.min(100, Math.floor(Number(counts.bound || 0) / bindingTotal * 1000) / 10) : 0;
      const bindingCoverageDetail = bindingTotal ? i18n.t('ui.binding_coverage', { value: new Intl.NumberFormat(i18n.intlLocale, { minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(bindingCoverage) }) : i18n.t('ui.no_accounts');
      document.getElementById('resinMetrics').innerHTML = [
        metricCard('database', num(counts.bound || 0), i18n.t('ui.bound_accounts'), bindingCoverageDetail, 'info'),
        metricCard('users', num(counts.unbound || 0), i18n.t('ui.unbound_accounts'), i18n.t('ui.actual_proxy_empty'), ''),
        metricCard('alert', num(needsReview), i18n.t('ui.needs_reconciliation'), i18n.t('ui.external_config_drift_or_read_failure'), needsReview ? 'warn' : 'good'),
        metricCard('check', num(sourceTotal), i18n.t('ui.proxy'), i18n.t('ui.available_ratio', { available: num(available), total: num(sourceTotal) }), available ? 'good' : 'warn')
      ].join('');
      document.getElementById('resinConfigState').textContent = !configured ? i18n.t('ui.no_proxy_configured') : (available ? i18n.t('ui.bindable_proxy_available') : i18n.t('ui.no_bindable_proxy'));
      const notice = document.getElementById('resinNotice');
      if (!configured) {
        notice.hidden = false;
        notice.className = 'notice warn';
        document.getElementById('resinNoticeText').textContent = i18n.t('ui.configure_proxy_notice');
      } else if (!available) {
        notice.hidden = false;
        notice.className = 'notice warn';
        document.getElementById('resinNoticeText').textContent = i18n.t('ui.no_new_binding_proxy_notice');
      } else {
        notice.hidden = true;
      }
      document.getElementById('bindUnboundBtn').disabled = !canBind;
      const result = pagedItems('leases');
      document.getElementById('leaseRows').innerHTML = result.items.map(leaseRow).join('');
      document.getElementById('leaseEmpty').hidden = result.total > 0;
      document.getElementById('leaseCount').textContent = i18n.t('ui.filter_accounts_count', { count: result.total });
      renderPager('leases', result);
      syncLeaseSelectionUI();
      renderProxyAutoBind();
    }
    function leaseRow(account) {
      const authID = esc(account.auth_id);
      const status = leaseStatus(account);
      const selected = state.leaseSelection.has(account.auth_id);
      const canBind = proxyCanBind();
      const sourceIdentity = proxySourceIdentity(account);
      const sourceLabel = sourceIdentity.name;
      const sourceKind = sourceIdentity.kindLabel;
      const bindingTone = status === 'bound' ? 'info' : (status === 'drifted' ? 'warn' : (status === 'reconcile_required' || status === 'read_failed' || status === 'unknown' ? 'bad' : ''));
      const bindingKey = catalogKey('proxy.binding.', status || 'unbound');
      const statusBadge = badge(hasCatalogKey(bindingKey) ? i18n.t(bindingKey) : i18n.t('proxy.binding.unbound'), bindingTone);
      let primaryActions = '';
      let primaryClass = 'proxy-row-primary is-single';
      if (status === 'bound' || status === 'drifted') {
        primaryClass = 'proxy-row-primary';
        primaryActions = '<button class="btn btn-xs" type="button" data-proxy-action="rotate" data-auth="' + authID + '">' + icon('rotate') + esc(i18n.t('ui.rotate')) + '</button>' +
          '<button class="btn btn-xs btn-danger" type="button" data-proxy-action="clear" data-auth="' + authID + '">' + icon('x') + esc(i18n.t('ui.clear')) + '</button>';
      } else if (status === 'unbound') {
        primaryActions = canBind ? '<button class="btn btn-xs" type="button" data-proxy-action="bind" data-auth="' + authID + '">' + icon('link') + esc(i18n.t('ui.bind')) + '</button>' : '<span class="subtle">' + esc(i18n.t('ui.no_bindable_proxy')) + '</span>';
      } else if (status === 'external') {
        primaryActions = '<button class="btn btn-xs btn-danger" type="button" data-proxy-action="clear-external" data-auth="' + authID + '">' + icon('x') + esc(i18n.t('ui.clear_external_proxy')) + '</button>';
      } else {
        primaryActions = '<button class="btn btn-xs btn-ghost" type="button" data-open-account="' + authID + '">' + icon('eye') + esc(i18n.t('ui.details')) + '</button>';
      }
      const actions = '<span class="' + primaryClass + '">' + primaryActions + '</span>' +
        '<button class="btn btn-xs btn-ghost" type="button" data-proxy-action="' + (account.auto_bind_excluded ? 'resume-auto' : 'pause-auto') + '" data-auth="' + authID + '">' + (account.auto_bind_excluded ? esc(i18n.t('ui.resume_auto_bind')) : esc(i18n.t('ui.pause_auto_bind'))) + '</button>';
      const proxy = endpointCell(account.proxy, state.leaseProxyDetails.get(account.auth_id), 'data-lease-endpoint', authID);
      const sourceParts = [sourceKind, sourceLabel].filter(Boolean);
      const sourceDetail = sourceParts.length ? '<div class="subline">' + esc(sourceParts.join(i18n.t('ui.meta_separator'))) + '</div>' : '';
      return '<tr><td class="select-cell"><input type="checkbox" data-lease-select="' + authID + '" aria-label="' + i18n.t('ui.select_account') + '" ' + (selected ? 'checked' : '') + '></td>' +
        '<td><div class="account-name truncate-line" title="' + esc(account.display_full || account.auth_id || account.display || account.label || i18n.t('ui.unnamed_account')) + '">' + esc(account.display || account.label || i18n.t('ui.unnamed_account')) + '</div><div class="subline truncate-line" title="' + esc(account.auth_id) + '">' + esc(account.auth_id_display || account.auth_id) + '</div>' + (account.auto_bind_excluded ? '<div class="subline">' + esc(i18n.t('ui.auto_bind_paused')) + '</div>' : '') + '</td>' +
        '<td>' + (sourceDetail || '<span class="subtle">-</span>') + '</td><td>' + proxy + '</td><td class="mono">' + (account.proxy_source_revision ? 'r' + esc(account.proxy_source_revision) : '-') + '</td><td>' + statusBadge + '</td><td class="actions-cell"><div class="actions">' + actions + '</div></td></tr>';
    }
    function endpointCell(value, revealed, attribute, id) {
      if (!value) return '<span class="subtle">-</span>';
      const text = revealed || value;
      const label = i18n.t(revealed ? 'ui.hide_full_endpoint' : 'ui.show_full_endpoint');
      return '<div class="endpoint-cell"><span class="mono endpoint-text truncate-line" title="' + esc(text) + '">' + esc(text) + '</span><button class="btn btn-xs btn-ghost icon-btn" type="button" ' + attribute + '="' + id + '" title="' + label + '" aria-label="' + label + '">' + icon('eye') + '</button></div>';
    }
    async function toggleLeaseEndpoint(authID, button) {
      if (state.leaseProxyDetails.has(authID)) {
        state.leaseProxyDetails.delete(authID);
        renderResin();
        return;
      }
      setBusy(button, true);
      try {
        const value = await api('proxy.bindings.endpoint', { auth_id: authID });
        const full = String(value?.proxy_full || '').trim();
        if (!full) throw new Error(i18n.t('ui.full_endpoint_empty'));
        state.leaseProxyDetails.set(authID, full);
        renderResin();
      } catch (error) {
        toast(i18n.t('ui.read_full_endpoint_failed', { error: error.message }), 'error');
      } finally {
        if (button.isConnected) setBusy(button, false);
      }
    }
    async function toggleSourceEndpoint(sourceID, button) {
      if (state.sourceProxyDetails.has(sourceID)) {
        state.sourceProxyDetails.delete(sourceID);
        renderProxySources();
        return;
      }
      setBusy(button, true);
      try {
        const value = await api('proxy.sources.endpoint', { source_id: sourceID });
        const full = String(value?.proxy_full || '').trim();
        if (!full) throw new Error(i18n.t('ui.full_endpoint_empty'));
        state.sourceProxyDetails.set(sourceID, full);
        renderProxySources();
      } catch (error) {
        toast(i18n.t('ui.read_full_endpoint_failed', { error: error.message }), 'error');
      } finally {
        if (button.isConnected) setBusy(button, false);
      }
    }
    function proxySourceLabel(kind) {
      if (kind === 'node') return i18n.t('ui.node');
      if (kind === 'template') return i18n.t('ui.template');
      if (kind === 'resin') return 'Resin';
      return kind || i18n.t('ui.proxy_source');
    }
    function proxySourceFilteredList() {
      return state.proxySources;
    }
    function proxySourceEnabledState(source) {
      const parts = [badge(i18n.t(source.enabled ? 'ui.enabled_state' : 'ui.disabled_state'), source.enabled ? 'good' : '')];
      if (!source.valid) parts.push('<div class="subline">' + esc(i18n.t('ui.invalid_config')) + '</div>');
      if (source.lifecycle === 'deleting') parts.push('<div class="subline">' + esc(i18n.t('ui.cleanup_incomplete')) + '</div>');
      else if (source.lifecycle === 'syncing') parts.push('<div class="subline">' + esc(i18n.t('ui.sync_incomplete')) + '</div>');
      return parts.join('');
    }
    function proxySourceConnectivity(source) {
      const tip = i18n.t('ui.proxy_connectivity_tip');
      const health = source.health || 'unknown';
      let label = i18n.t('ui.not_checked');
      let tone = '';
      if (health === 'healthy') { label = i18n.t('ui.reachable'); tone = 'good'; }
      else if (health === 'unhealthy') { label = i18n.t('ui.unreachable'); tone = 'bad'; }
      return '<div title="' + esc(tip) + '">' + badge(label, tone) + '</div>';
    }
    function proxySourceCheckedAt(source) {
      const checkedAt = formatFullTime(source.health_checked);
      return checkedAt === '-' ? '<span class="muted">-</span>' : '<span class="nowrap">' + esc(checkedAt) + '</span>';
    }
    function proxySourceAllCount() {
      const summary = state.proxy || {};
      if (summary.source_count != null) return Number(summary.source_count || 0);
      const counts = summary.source_counts || {};
      return Number(counts.node || 0) + Number(counts.template || 0) + Number(counts.resin || 0);
    }
    function syncProxySourceSelectionUI() {
      const current = pagedItems('sources');
      const allCount = proxySourceAllCount();
      const pageSelected = current.items.filter((source) => state.proxySourceSelection.has(source.id)).length;
      const selected = state.proxySourceSelection.size;
      const selectAll = document.getElementById('proxySourceSelectAll');
      selectAll.disabled = current.items.length === 0;
      selectAll.checked = current.items.length > 0 && pageSelected === current.items.length;
      selectAll.indeterminate = pageSelected > 0 && pageSelected < current.items.length;
      selectAll.setAttribute('aria-label', i18n.t('ui.select_page_accounts'));
      selectAll.title = i18n.t('ui.select_page_accounts');
      const scope = current.items.length > 0 && pageSelected === current.items.length ? i18n.t('ui.page_all_included') : '';
      document.getElementById('proxySourceSelectionCount').textContent = i18n.t('ui.selected_proxies', { count: selected, scope: scope });
      const selectFiltered = document.getElementById('proxySourceSelectFilteredBtn');
      const selectAllSources = document.getElementById('proxySourceSelectAllBtn');
      selectFiltered.hidden = current.total === 0 || current.total === allCount;
      selectFiltered.textContent = i18n.t('ui.select_filtered_count', { count: current.total });
      selectAllSources.hidden = allCount === 0;
      selectAllSources.textContent = i18n.t('ui.select_all_proxies_count', { count: allCount });
      document.getElementById('proxySourceClearSelectionBtn').hidden = selected === 0;
      const test = document.getElementById('proxySourceTestSelectedBtn');
      test.disabled = selected === 0;
      test.title = selected > 0 ? i18n.t('ui.test_selected_proxies', { count: selected }) : i18n.t('ui.select_proxies_first');
      const remove = document.getElementById('proxySourceDeleteSelectedBtn');
      remove.disabled = selected === 0;
      remove.title = selected > 0 ? i18n.t('ui.cleanup_delete_selected_proxies', { count: selected }) : i18n.t('ui.select_proxies_first');
    }
    function proxySourceRow(source) {
      const sourceID = esc(source.id || '');
      const selected = state.proxySourceSelection.has(source.id);
      const actions = [];
      if (source.change_id) actions.push('<button class="btn btn-xs" type="button" data-proxy-source-change="' + esc(source.change_id) + '">' + esc(i18n.t('ui.sync_task')) + '</button>');
      actions.push('<button class="btn btn-xs" type="button" data-proxy-source-edit="' + sourceID + '" data-kind="' + esc(source.kind) + '">' + icon('sliders') + '' + esc(i18n.t('ui.edit')) + '</button>');
      if (source.kind === 'template' && Number(source.binding_count || 0) > 0) {
        actions.push('<button class="btn btn-xs btn-ghost" type="button" data-proxy-source-apply="' + sourceID + '">' + icon('refresh') + '' + esc(i18n.t('ui.apply_to_bindings')) + '</button>');
      }
      if (Number(source.sync_failed || source.failed_count || 0) > 0 || source.sync_status === 'partial' || source.sync_status === 'failed') {
        actions.push('<button class="btn btn-xs" type="button" data-proxy-source-retry="' + sourceID + '">' + esc(i18n.t('ui.retry_failed_sync_items')) + '</button>');
      }
      if (source.kind === 'resin' && source.local_only) {
        actions.push('<button class="btn btn-xs btn-ghost" type="button" disabled title="' + esc(i18n.t('ui.local_rotation_no_endpoint')) + '">' + icon('activity') + esc(i18n.t('ui.probe')) + '</button>');
      } else {
        actions.push('<button class="btn btn-xs btn-ghost" type="button" data-proxy-source-test="' + sourceID + '">' + icon('activity') + esc(i18n.t('ui.probe')) + '</button>');
      }
      actions.push('<button class="btn btn-xs btn-danger" type="button" data-proxy-source-delete="' + sourceID + '" data-kind="' + esc(source.kind) + '">' + icon('x') + esc(i18n.t('ui.delete')) + '</button>');
      const endpoint = endpointCell(source.endpoint, state.sourceProxyDetails.get(source.id), 'data-source-endpoint', sourceID);
      return '<tr><td class="select-cell"><input type="checkbox" data-source-select="' + sourceID + '" aria-label="' + esc(i18n.t('ui.select_proxy')) + '" ' + (selected ? 'checked' : '') + '></td><td><div class="account-name">' + esc(source.name || i18n.t('ui.unnamed_proxy')) + '</div></td>' +
        '<td>' + badge(proxySourceLabel(source.kind), source.kind === 'resin' ? 'info' : '') + '</td>' +
        '<td>' + endpoint + '</td>' +
        '<td>' + proxySourceEnabledState(source) + '</td><td>' + proxySourceConnectivity(source) + '</td><td class="nowrap">' + proxySourceCheckedAt(source) + '</td><td class="mono">' + esc(source.binding_count || 0) + '</td>' +
        '<td class="mono" title="' + esc(i18n.t('ui.proxy_version_help')) + '">v' + esc(source.config_version || 1) + ' / r' + esc(source.revision || 1) + '</td>' +
        '<td class="actions-cell"><div class="actions">' + actions.join('') + '</div></td></tr>';
    }
