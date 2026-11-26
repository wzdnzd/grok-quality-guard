    const state = {
      overview: null,
      accounts: [],
      accountDetails: new Map(),
      accountsTotal: 0,
      accountsLoaded: false,
      stateCounts: {},
      events: [],
      policy: null,
      resin: null,
      proxy: null,
      proxySources: [],
      proxySourceSelection: new Set(),
      proxySubtab: 'bindings',
      proxySourceFilter: 'all',
      proxySourceQuery: '',
      proxySourceEnabledFilter: 'all',
      proxySourceHealthFilter: 'all',
      proxySourcePage: 1,
      proxySourcePageSize: 50,
      proxySourceChoices: [],
      proxyOperationSourceChoices: [],
      proxySourceList: null,
      proxyTasks: new Map(),
      taskCenter: null,
      taskCenterRefreshing: false,
      taskCenterQueuesPromise: null,
      taskCenterListRequest: 0,
      taskCenterPage: 1,
      taskCenterPageSize: 20,
      taskCenterQuery: '',
      taskCenterCategory: 'all',
      taskCenterStatus: 'all',
      taskCenterSelectedID: '',
      taskCenterDetail: null,
      taskCenterDetailPage: 1,
      taskCenterDetailStatus: 'all',
      taskCenterDetailLoading: false,
      taskCenterDetailRequest: 0,
      taskCenterSelectedQueue: null,
      taskCenterQueueDetail: null,
      taskQueuePage: 1,
      taskQueueDetailRequest: 0,
      proxyEditing: null,
      proxyEditorSequence: 0,
      proxyPreviewSequence: 0,
      leases: [],
      leaseList: null,
      accountList: null,
      bindingsByAuthID: new Map(),
      bindingsVersion: 0,
      listRequests: { accounts: 0, leases: 0, sources: 0 },
      listLoadedAt: { accounts: 0, leases: 0, sources: 0 },
      listPromises: { accounts: null, leases: null, sources: null },
      listControllers: { accounts: null, leases: null, sources: null },
      listSignatures: { accounts: '', leases: '', sources: '' },
      activeTab: 'overview',
      accountQuery: '',
      accountState: 'all',
      accountPage: 1,
      accountPageSize: 50,
      leaseQuery: '',
      leaseFilter: 'all',
      leaseSourceFilter: '',
      leasePage: 1,
      leasePageSize: 50,
      eventQuery: '',
      eventType: 'all',
      eventPage: 1,
      eventPageSize: 50,
      selectedAuthID: '',
      accountSelection: new Set(),
      leaseSelection: new Set(),
      leaseProxyDetails: new Map(),
      sourceProxyDetails: new Map(),
      refreshing: false,
      activeRefreshes: 0,
      proxySummaryLoadedAt: 0,
      proxySummaryPromise: null,
      proxySummaryRetries: 0,
      overviewPromise: null,
      eventsPromise: null,
      policyPromise: null,
      tabPromises: { overview: null, accounts: null, resin: null, events: null, settings: null },
      metadataPromises: { accounts: null, leases: null },
      metadataTimers: { accounts: 0, leases: 0 },
      overviewLoadedAt: 0,
      eventsLoadedAt: 0,
      policyLoadedAt: 0,
      metadataAttempts: new Map()
    };
    const formDrafts = new Map();
    let toastTimer = 0;
    let taskSearchTimer = 0;
    const STATE_TONES = Object.freeze({
      unknown: '', healthy: 'good', suspect: 'warn', degraded: 'bad', cooling: 'warn',
      quota_exhausted: 'warn', credential_invalid: 'bad',
      disabled_by_guard: 'bad', externally_disabled: ''
    });
    const CJK_TEXT = /[\u3400-\u9fff]/;
    const catalogKey = (prefix, value) => {
      const slug = String(value || '').trim().toLowerCase().replace(/[.\-\s]+/g, '_').replace(/[^a-z0-9_]/g, '');
      return slug ? prefix + slug : '';
    };
    const hasCatalogKey = (key) => Boolean(key) && Object.prototype.hasOwnProperty.call(i18n.resources[i18n.locale] || {}, key);
    const localizedCode = (prefix, value, fallbackKey) => {
      const key = catalogKey(prefix, value);
      if (hasCatalogKey(key)) return i18n.t(key);
      return i18n.t(fallbackKey);
    };
    const esc = (value) => i18n.translateSource(String(value == null ? '' : value)).replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));
    const icon = (name) => '<svg class="icon" aria-hidden="true"><use href="#i-' + name + '"></use></svg>';
    function cleanHelpCopy(value) {
      return String(value == null ? '' : value).trim().replace(/[\s,.\uFF0C\u3002\uFF1B;\u3001:\uFF1A!?\uFF01\uFF1F\u2026]+$/u, '');
    }
    const helpTip = (text, className) => {
      const copy = cleanHelpCopy(text);
      return copy ? '<span class="help-tip ' + esc(className || '') + '" tabindex="0" role="note" aria-label="' + esc(copy) + '">' + icon('info') + '<span class="help-tooltip" role="tooltip">' + esc(copy) + '</span></span>' : '';
    };
    let activeHelpTip = null;
    function hideHelpTooltip(tip) {
      const target = tip || activeHelpTip;
      const tooltip = target?.querySelector?.('.help-tooltip');
      if (tooltip) {
        tooltip.classList.remove('is-open');
        ['display', 'visibility', 'left', 'top'].forEach((name) => tooltip.style.removeProperty(name));
      }
      if (!tip || activeHelpTip === tip) activeHelpTip = null;
    }
    function showHelpTooltip(tip) {
      const tooltip = tip?.querySelector?.('.help-tooltip');
      if (!tooltip) return;
      if (activeHelpTip && activeHelpTip !== tip) hideHelpTooltip(activeHelpTip);
      activeHelpTip = tip;
      tooltip.classList.add('is-open');
      tooltip.style.visibility = 'hidden';
      const anchor = tip.getBoundingClientRect();
      const drawer = tip.closest?.('#proxySourceDrawer .drawer');
      const drawerRect = drawer?.getBoundingClientRect?.();
      const margin = 12;
      const gap = 8;
      const viewportWidth = Math.max(0, window.innerWidth || document.documentElement?.clientWidth || 0);
      const viewportHeight = Math.max(0, window.innerHeight || document.documentElement?.clientHeight || 0);
      const minLeft = Math.max(margin, (drawerRect?.left || 0) + margin);
      const maxRight = Math.min(viewportWidth - margin, (drawerRect?.right || viewportWidth) - margin);
      const availableWidth = Math.max(0, maxRight - minLeft);
      tooltip.style.maxWidth = Math.min(320, availableWidth) + 'px';
      const width = tooltip.offsetWidth || Math.min(320, availableWidth);
      const height = tooltip.offsetHeight || 0;
      let left = anchor.left + anchor.width / 2 - width / 2;
      left = Math.max(minLeft, Math.min(left, Math.max(minLeft, maxRight - width)));
      let top = anchor.bottom + gap;
      if (viewportHeight && top + height > viewportHeight - margin) top = Math.max(margin, anchor.top - height - gap);
      tooltip.style.left = Math.round(left) + 'px';
      tooltip.style.top = Math.round(top) + 'px';
      tooltip.style.visibility = 'visible';
    }
    function proxyFieldLabel(label, id, hint) {
      const text = id ? '<label for="' + esc(id) + '">' + esc(label) + '</label>' : '<span class="field-label-text">' + esc(label) + '</span>';
      return '<span class="field-label">' + text + helpTip(hint) + '</span>';
    }
    function setProxyEditorMeta(source, note) {
      const meta = document.getElementById('proxySourceEditorMeta');
      if (!meta) return;
      if (!source) {
        meta.hidden = true;
        meta.innerHTML = '';
        return;
      }
      meta.hidden = false;
      const metaNote = cleanHelpCopy(note);
      meta.innerHTML = '<span class="meta-chip">' + icon('link') + esc(i18n.t('ui.bound_count', { count: Number(source.binding_count || 0) })) + '</span>' + (metaNote ? '<span>' + esc(metaNote) + '</span>' : '');
    }

        function proxyPreviewSamplesHTML(samples, intro) {
      const items = Array.isArray(samples) ? samples : [];
      if (!items.length) return '<p class="proxy-preview-empty">' + i18n.t('ui.no_preview_samples') + '</p>';
      return '<div class="proxy-preview-summary">' + esc(intro || i18n.t('ui.validated')) + '<span class="proxy-preview-badge">' + i18n.t('ui.masked_password') + '</span></div><div class="proxy-preview-list">' + items.map((sample, index) => {
        const values = Object.entries(sample.values || {});
        return '<article class="proxy-preview-card"><div class="proxy-preview-card-head"><span class="proxy-preview-index">' + esc(i18n.t('ui.sample_number', { number: String(index + 1).padStart(2, '0') })) + '</span><span class="proxy-preview-badge">' + i18n.t('ui.standard_proxy_url') + '</span></div><div class="proxy-preview-url mono">' + esc(sample.display_url || sample.masked_url || sample.endpoint || '-') + '</div>' + (values.length ? '<div class="proxy-preview-values">' + values.map(([name, value]) => '<span class="proxy-preview-value"><b>' + esc(name) + ':</b>' + esc(value) + '</span>').join('') + '</div>' : '') + '</article>';
      }).join('') + '</div>';
    }

        const num = (value, digits) => new Intl.NumberFormat(i18n.intlLocale, { maximumFractionDigits: digits == null ? 0 : digits }).format(Number(value || 0));
    const stateLabel = (value) => {
      const raw = String(value == null ? '' : value).trim();
      const key = catalogKey('status.state.', raw);
      if (hasCatalogKey(key)) return i18n.t(key);
      return CJK_TEXT.test(raw) ? i18n.translateSource(raw) : i18n.t('status.state.other');
    };
    const stateTone = (value) => STATE_TONES[value] || '';
    const reasonLabel = (value) => {
      const raw = String(value == null ? '' : value).trim();
      if (!raw) return i18n.t('reason.none');
      if (hasCatalogKey(raw)) return i18n.t(raw);
      for (const prefix of ['reason.', 'reason.detail.']) {
        const key = catalogKey(prefix, raw);
        if (hasCatalogKey(key)) return i18n.t(key);
      }
      if (CJK_TEXT.test(raw)) return i18n.translateSource(raw);
      if (/^[a-z][a-z0-9_.-]*$/i.test(raw)) return i18n.t('reason.other');
      return raw;
    };
    const VERDICT_TONES = { quality: 'bad', transport: 'warn', account: 'warn', inconclusive: 'info', ignored: '' };
    const verdictTone = (verdict) => {
      if (!verdict) return '';
      if (verdict.decision === 'deliver' && !verdict.code) return 'good';
      return VERDICT_TONES[verdict.category] || '';
    };
    const verdictCategoryLabel = (value) => i18n.t('verdict.category.' + (value || 'ignored'));
    const verdictDecisionLabel = (value) => i18n.t('verdict.decision.' + (value || 'observe'));
    const verdictCodeLabel = (value) => value ? reasonLabel(value) : i18n.t('reason.none');
    const verdictEvidenceChips = (verdict) => {
      if (!verdict) return '';
      const evidence = verdict.evidence || {};
      const chips = [];
      if (!evidence.encrypted_bytes && !evidence.has_thinking && !evidence.reasoning_tokens && verdict.category === 'quality') chips.push(i18n.t('verdict.evidence.no_ciphertext'));
      if (evidence.hold_expired) chips.push(i18n.t('verdict.evidence.hold_expired'));
      if (evidence.strikes > 0) chips.push(i18n.t('verdict.evidence.strikes', { strikes: num(evidence.strikes) }));
      return chips.map((chip) => '<span class="verdict-chip">' + esc(chip) + '</span>').join('');
    };
    const verdictSummaryLine = (verdict) => {
      if (!verdict) return '';
      const decision = verdictDecisionLabel(verdict.decision);
      return verdict.code ? decision + i18n.t('ui.meta_separator') + verdictCodeLabel(verdict.code) : decision;
    };
    const verdictSummaryHTML = (verdict, fallback) => {
      const text = verdict ? verdictSummaryLine(verdict) : fallback;
      const dot = verdict ? '<span class="verdict-dot ' + esc(verdictTone(verdict)) + '"></span>' : '';
      return { html: dot + esc(text), text };
    };
    // Row evidence keeps only the values that explain the decision. TPS is a
    // dedicated column and the source/time live in the timeline cell, so they
    // are deliberately absent here.
    const verdictRowEvidence = (verdict) => {
      const evidence = verdict && verdict.evidence;
      if (!evidence) return '';
      const parts = [];
      if (evidence.encrypted_bytes > 0) {
        parts.push(i18n.t('verdict.evidence.encrypted', { bytes: formatBytes(evidence.encrypted_bytes), floor: formatBytes(evidence.encrypted_floor) }));
      } else if (verdict.category === 'quality' && !evidence.has_thinking && !evidence.reasoning_tokens) {
        parts.push(i18n.t('verdict.evidence.no_ciphertext'));
      }
      if (evidence.reasoning_tokens > 0) parts.push(i18n.t('verdict.evidence.reasoning', { tokens: num(evidence.reasoning_tokens) }));
      if (evidence.visible_tokens > 0) parts.push(i18n.t('verdict.evidence.visible', { tokens: num(evidence.visible_tokens) }));
      if (evidence.hold_expired) parts.push(i18n.t('verdict.evidence.hold_expired'));
      if (evidence.strikes > 0) parts.push(i18n.t('verdict.evidence.strikes', { strikes: num(evidence.strikes) }));
      return parts.slice(0, 4).join(i18n.t('ui.meta_separator'));
    };
    const verdictCard = (verdict) => {
      if (!verdict) return '';
      const tone = verdictTone(verdict);
      const detail = verdict.detail ? reasonText(verdict.detail) : '';
      const code = verdict.code ? verdictCodeLabel(verdict.code) : '';
      const showDetail = detail && detail !== code;
      return '<div class="drawer-section verdict-card ' + esc(tone) + '">' +
        '<div class="verdict-head">' + badge(verdictDecisionLabel(verdict.decision), tone) +
        (code ? '<span class="verdict-code">' + esc(code) + '</span>' : '') +
        '<span class="verdict-time">' + esc(relativeTime(verdict.at)) + ' · ' + esc(formatFullTime(verdict.at)) + '</span></div>' +
        (showDetail ? '<div class="verdict-detail">' + esc(detail) + '</div>' : '') +
        '<div class="verdict-evidence">' + verdictEvidenceChips(verdict) + '</div>' +
        '</div>';
    };
    const sourceLabel = (value) => {
      const raw = String(value == null ? '' : value).trim();
      const key = catalogKey('source.', raw);
      if (hasCatalogKey(key)) return i18n.t(key);
      return CJK_TEXT.test(raw) ? i18n.translateSource(raw) : i18n.t('source.other');
    };
    const qualityActionLabel = (value) => localizedCode('quality.action.', value, 'quality.action.unknown');
    const protocolLabel = (value) => {
      if (value === 'chat') return 'Chat Completions';
      if (value === 'responses') return 'Responses';
      if (value === 'anthropic') return 'Anthropic Messages';
      return value || i18n.t('quality.protocol.unknown');
    };
    const reasonText = (value, unknown) => {
      const text = String(value == null ? '' : value).trim();
      if (!text) return '';
      const direct = reasonLabel(text);
      if (direct !== i18n.t('reason.other')) return direct;
      let match = text.match(/^upstream status (\d+) \((.+)\)$/i);
      if (match) return i18n.t('ui.upstream_status_2', { status: match[1], reason: reasonText(match[2]) });
      match = text.match(/^upstream status (\d+)$/i);
      if (match) return i18n.t('ui.upstream_status', { status: match[1] });
      match = text.match(/^probe upstream status (\d+)$/i);
      if (match) return i18n.t('ui.probe_upstream_status', { status: match[1] });
      match = text.match(/^probe quality check failed:\s*(.+)$/i);
      if (match) return i18n.t('ui.probe_quality_check_failed', { reason: reasonText(match[1]) });
      match = text.match(/^host_call_failed:\s*(.+)$/i);
      if (match) return i18n.t('ui.host_call_failed', { reason: reasonText(match[1]) });
      if (CJK_TEXT.test(text)) return i18n.translateSource(text);
      return unknown === undefined ? i18n.t('ui.other_upstream_error') : unknown;
    };
    const localizeDetail = (value) => String(value == null ? '' : value).split(i18n.t('ui.meta_separator')).map((part) => {
      const text = part.trim();
      if (!text) return '';
      const sourceKey = catalogKey('source.', text);
      if (hasCatalogKey(sourceKey)) return i18n.t(sourceKey);
      if (text === 'permanent_eligible=true') return i18n.t('ui.permanent_disable_candidate_yes');
      if (text === 'permanent_eligible=false') return i18n.t('ui.permanent_disable_candidate_no');
      const fields = [];
      for (const field of text.matchAll(/(?:^|\s)(\w+)=([^=]*?)(?=\s+\w+=|$)/g)) {
        const labelKey = catalogKey('proxy.detail.', field[1]);
        if (!hasCatalogKey(labelKey)) continue;
        const raw = field[2].trim();
        const remoteKey = catalogKey('lease.remote.', raw);
        const fieldValue = field[1] === 'remote_lease' && hasCatalogKey(remoteKey) ? i18n.t(remoteKey) : raw;
        fields.push(i18n.t(labelKey) + i18n.t('ui.detail_separator') + fieldValue);
      }
      if (fields.length) return fields.join(i18n.t('ui.meta_separator'));
      return reasonText(text, '') || text;
    }).filter(Boolean).join(i18n.t('ui.meta_separator'));
    const attemptLabel = (attempt, options) => {
      if (!attempt) return i18n.t('ui.no_attempts_yet');
      const settings = options || {};
      const prefix = attempt.source ? sourceLabel(attempt.source) + i18n.t('ui.meta_separator') : '';
      const verdict = attempt.class ? reasonLabel(attempt.class) : i18n.t('reason.passed');
      const reason = reasonText(attempt.reason);
      const detail = attempt.class && reason && attempt.reason !== attempt.class && reason !== verdict && !reason.includes(verdict) ? i18n.t('ui.meta_separator') + reason : '';
      const at = settings.includeTime !== false && attempt.at ? i18n.t('ui.meta_separator') + formatFullTime(attempt.at) : '';
      return prefix + verdict + detail + at;
    };
    const actionLabel = (value) => {
      const raw = String(value == null ? '' : value).trim();
      const key = catalogKey('action.', raw);
      if (hasCatalogKey(key)) return i18n.t(key);
      if (/^(proxy|resin)_/.test(raw)) return i18n.t('action.proxy_operation');
      return CJK_TEXT.test(raw) ? i18n.translateSource(raw) : i18n.t('action.system');
    };
    const resultLabel = (value) => {
      const raw = String(value == null ? '' : value).trim();
      const key = catalogKey('result.', raw);
      if (hasCatalogKey(key)) return i18n.t(key);
      return CJK_TEXT.test(raw) ? i18n.translateSource(raw) : '-';
    };
    const badge = (text, tone) => '<span class="badge ' + esc(tone || '') + '">' + esc(text) + '</span>';
    const modeLabel = (policy) => {
      if (!policy) return i18n.t('ui.unknown');
      if (policy.guard === 'transparent') return i18n.t('ui.transparent_protection');
      if (policy.guard === 'passive') return i18n.t('ui.passive_observation');
      return i18n.t('ui.management_only');
    };
    const autoActionLabel = (value) => value === 'cooldown_then_disable' ? i18n.t('ui.cool_down_then_permanently_disable') : (value === 'cooldown_only' ? i18n.t('ui.cooldown') : i18n.t('ui.observe_only'));
    const formatTime = (value) => {
      if (!value) return '-';
      const date = new Date(value);
      if (Number.isNaN(date.getTime()) || date.getFullYear() < 2000) return '-';
      return new Intl.DateTimeFormat(i18n.intlLocale, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(date);
    };
    const formatFullTime = (value) => {
      if (!value) return '-';
      const date = new Date(value);
      if (Number.isNaN(date.getTime()) || date.getFullYear() < 2000) return '-';
      return new Intl.DateTimeFormat(i18n.intlLocale, { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' }).format(date);
    };
    const relativeTime = (value) => {
      if (!value) return '-';
      const date = new Date(value);
      if (Number.isNaN(date.getTime()) || date.getFullYear() < 2000) return '-';
      try {
        const formatter = new Intl.RelativeTimeFormat(i18n.intlLocale, { numeric: 'auto', style: 'short' });
        const seconds = Math.round((date.getTime() - Date.now()) / 1000);
        const abs = Math.abs(seconds);
        if (abs < 60) return formatter.format(seconds, 'second');
        if (abs < 3600) return formatter.format(Math.round(seconds / 60), 'minute');
        if (abs < 86400) return formatter.format(Math.round(seconds / 3600), 'hour');
        return formatter.format(Math.round(seconds / 86400), 'day');
      } catch (_) {
        return formatTime(value);
      }
    };
    const formatBytes = (value) => {
      const bytes = Number(value || 0);
      if (bytes < 1024) return num(bytes) + ' B';
      if (bytes < 1024 * 1024) return num(bytes / 1024, 1) + ' KiB';
      return num(bytes / 1024 / 1024, 1) + ' MiB';
    };
    const yesNo = (value) => i18n.t(value ? 'ui.yes' : 'ui.no');
    const evidenceCell = (label, value) => '<div class="detail-cell"><div class="detail-label">' + esc(label) + '</div><div class="detail-value">' + esc(value) + '</div></div>';
    // evidenceSection renders only values that are not already obvious from the
    // verdict card above it: one metric per cell so every row has a stable,
    // scannable height.
    const evidenceCellWideHTML = (label, html) => '<div class="detail-cell detail-cell-wide"><div class="detail-label">' + esc(label) + '</div><div class="detail-value detail-value-wrap">' + html + '</div></div>';
    const evidenceSection = (evidence) => {
      if (!evidence) return '';
      const classText = evidence.class ? reasonLabel(evidence.class) : i18n.t('reason.passed');
      const actionText = qualityActionLabel(evidence.action) || i18n.t('quality.action.unknown');
      const protocolText = protocolLabel(evidence.protocol);
      const ms = (value) => i18n.t('ui.duration_milliseconds', { value: num(value) });
      const flags = [
        ['ui.genuine_reasoning', evidence.has_thinking],
        ['ui.plaintext_reasoning', evidence.has_reasoning_delta],
        ['ui.reasoning_required', evidence.reasoning_required],
        ['ui.reasoning_started', evidence.reasoning_started],
        ['ui.semantic_output', evidence.semantic_output],
        ['ui.usage_reported', evidence.usage_reported]
      ].map(([key, value]) => '<span class="flag-chip ' + (value ? 'on' : '') + '">' + esc(i18n.t(key, { value: yesNo(value) })) + '</span>').join('');
      return [
        '<div class="drawer-section"><h3>' + i18n.t('ui.quality_verdict_evidence') + '</h3><div class="detail-grid evidence-grid">',
        evidenceCell(i18n.t('ui.verdict_source'), sourceLabel(evidence.source)),
        evidenceCell(i18n.t('ui.verdict_result'), classText + i18n.t('ui.meta_separator') + actionText),
        evidenceCell(i18n.t('ui.protocol_mode'), protocolText + i18n.t('ui.meta_separator') + i18n.t(evidence.streaming ? 'ui.streaming' : 'ui.non_streaming')),
        evidenceCell(i18n.t('ui.visible_tokens'), num(evidence.visible_tokens)),
        evidenceCell(i18n.t('ui.output_tokens'), num(evidence.output_tokens)),
        evidenceCell(i18n.t('ui.reasoning_tokens'), num(evidence.reasoning_tokens)),
        evidenceCell(i18n.t('ui.ciphertext_minimum'), formatBytes(evidence.encrypted_bytes) + i18n.t('ui.meta_separator') + formatBytes(evidence.encrypted_floor)),
        evidenceCell(i18n.t('ui.first_token'), ms(evidence.first_token_ms)),
        evidenceCell(i18n.t('ui.total_duration'), ms(evidence.duration_ms)),
        evidenceCell(i18n.t('ui.generation_window'), ms(evidence.generation_ms)),
        evidenceCell(i18n.t('ui.output_speed'), num(evidence.tps, 1) + ' TPS'),
        evidenceCell(i18n.t('ui.terminal_timeout'), i18n.t('ui.terminal_timed_out', { terminal: yesNo(evidence.terminal), timeout: yesNo(evidence.hold_expired) })),
        evidenceCellWideHTML(i18n.t('ui.reasoning_evidence_state'), flags),
        evidenceCellWideHTML(i18n.t('ui.parse_statistics'), esc(i18n.t('ui.chunks_bytes_events_unknown_parse_failures', { chunks: num(evidence.chunk_count), bytes: formatBytes(evidence.byte_count), events: num(evidence.event_count), unknown: num(evidence.unknown_events), failed: num(evidence.parse_failures) }))),
        '</div><p class="hint" style="margin-top:9px">' + i18n.t('ui.preview_privacy_note') + '</p></div>'
      ].join('');
    };
