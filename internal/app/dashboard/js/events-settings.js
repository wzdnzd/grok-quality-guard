    function eventMatches(event) {
      const query = state.eventQuery.toLowerCase().trim();
      const text = [event.auth_id, event.class, event.action, event.result, event.detail, event.detail_raw].join(' ').toLowerCase();
      if (query && !text.includes(query)) return false;
      if (['quality', 'transport', 'account', 'inconclusive', 'ignored'].includes(state.eventType)) return (event.category || 'ignored') === state.eventType;
      if (state.eventType === 'proxy') return /^(proxy|resin)_/.test(String(event.action || ''));
      if (state.eventType === 'task') return /^task\./.test(String(event.action || '')) || String(event.action || '') === 'cross_verify_schedule';
      return true;
    }
    function eventDetailHTML(event, text) {
      const detail = String(text == null ? '' : text);
      const diagnostic = String(event.detail_raw || '').trim();
      if (!detail || !diagnostic || diagnostic === detail) return esc(detail);
      const linkLabel = i18n.t('ui.view_logs');
      const detailLower = detail.toLocaleLowerCase();
      const linkLower = linkLabel.toLocaleLowerCase();
      const linkIndex = detailLower.indexOf(linkLower);
      const linkText = linkIndex < 0 ? linkLabel : detail.slice(linkIndex, linkIndex + linkLabel.length);
      const before = linkIndex < 0 ? detail + ' ' : detail.slice(0, linkIndex);
      const after = linkIndex < 0 ? '' : detail.slice(linkIndex + linkLabel.length);
      return '<details class="event-log-details"><summary class="event-log-summary" title="' + esc(diagnostic) + '">' + esc(before) + '<span class="event-log-link">' + esc(linkText) + '</span>' + esc(after) + '</summary><div class="event-log-diagnostic"><span class="event-log-diagnostic-label">' + esc(i18n.t('ui.event_diagnostic')) + '</span><code>' + esc(diagnostic) + '</code></div></details>';
    }
    function renderEvents() {
      const result = pagedItems('events');
      document.getElementById('eventRows').innerHTML = result.items.map((event) => {
        const category = event.category || 'ignored';
        const tone = VERDICT_TONES[category] || '';
        const code = event.code || event.class || '';
        const codeLabel = code ? verdictCodeLabel(code) : '';
        const action = actionLabel(event.action);
        const title = codeLabel || action;
        const detailText = localizeDetail(event.detail);
        const detail = detailText && detailText !== codeLabel ? eventDetailHTML(event, detailText) : '';
        const meta = [codeLabel ? esc(action) : '', esc(event.auth_id || i18n.t('ui.system_event')), detail].filter(Boolean).join(i18n.t('ui.meta_separator'));
        const decision = event.decision ? badge(verdictDecisionLabel(event.decision), tone) : (event.result ? badge(resultLabel(event.result), '') : '');
        return '<div class="event-item"><span class="event-dot ' + esc(tone) + '"></span><div style="min-width:0"><div class="event-title"><span>' + esc(title) + '</span>' + decision + '</div><div class="event-meta">' + meta + '</div></div><time class="recent-time" title="' + esc(formatFullTime(event.time)) + '">' + esc(relativeTime(event.time)) + '</time></div>';
      }).join('');
      document.getElementById('eventsEmpty').hidden = result.total > 0;
      document.getElementById('eventsCount').textContent = i18n.t('ui.filter_events_summary', { filtered: result.total, total: state.events.length });
      renderPager('events', result);
    }
    function setValue(id, value) { const el = document.getElementById(id); if (el) el.value = value == null ? '' : String(value); }
    function setChecked(id, value) { const el = document.getElementById(id); if (el) el.checked = Boolean(value); }
    function valueOf(id) { const el = document.getElementById(id); return el ? el.value.trim() : ''; }
    function numberValue(id, fallback) { const parsed = Number(valueOf(id)); return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback; }
    function nonNegativeIntegerValue(id, fallback) { const text = valueOf(id); if (!text) return fallback; const parsed = Number(text); return Number.isInteger(parsed) && parsed >= 0 ? parsed : NaN; }
    function signedNumberValue(id, fallback) { const raw = valueOf(id); if (raw === '') return fallback; const parsed = Number(raw); return Number.isFinite(parsed) ? parsed : fallback; }
    const POLICY_DURATION_FIELDS = new Set([
      'p_session_affinity_ttl',
      'p_score_min_interval',
      'p_score_max_age',
      'p_missing_cooldown',
      'p_empty_cooldown',
      'p_hold_timeout',
      'p_stream_idle_timeout',
      'p_attempt_timeout',
      'p_request_timeout',
      'p_hold_keepalive',
      'p_probe_timeout',
      'p_probe_scan_interval',
      'p_probe_unknown_delay',
      'p_probe_unknown_jitter',
      'p_probe_suspect_backoff',
      'p_probe_max_backoff'
    ]);
    // Field specs come from GET /settings/schema, the same table the backend
    // validates against, so the ranges are not duplicated in this file.
    const POLICY_SCHEMA_ENDPOINT = '/v0/management/plugins/grok-quality-guard/settings/schema';
    const POLICY_SCHEMA_BY_ID = new Map();
    const FIELD_ID_TO_PATH = {
      p_guard: 'guard',
      p_auto_action: 'auto_action',
      p_events_limit: 'events_limit',
      p_selection_strategy: 'routing.strategy.selection',
      p_session_affinity_ttl: 'routing.affinity.ttl',
      p_session_affinity_max_entries: 'routing.affinity.max_entries',
      p_max_accounts_per_bucket: 'routing.scoring.max_accounts_per_bucket',
      p_score_half_life: 'routing.scoring.half_life_hours',
      p_score_success_weight: 'routing.scoring.weights.success',
      p_score_ttft_weight: 'routing.scoring.weights.ttft',
      p_score_tps_weight: 'routing.scoring.weights.tps',
      p_score_probe_weight: 'routing.scoring.evidence.probe',
      p_score_transparent_weight: 'routing.scoring.evidence.transparent',
      p_score_passive_weight: 'routing.scoring.evidence.passive',
      p_score_min_interval: 'routing.scoring.refresh.min_interval',
      p_score_max_age: 'routing.scoring.refresh.max_age',
      p_score_drift_threshold: 'routing.scoring.refresh.drift_threshold',
      p_soft_tps: 'quality.soft_tps',
      p_hard_tps: 'quality.hard_tps',
      p_consecutive_soft: 'quality.consecutive_soft',
      p_consecutive_errors: 'quality.consecutive_errors',
      p_consecutive_missing_thinking: 'quality.consecutive_missing_thinking',
      p_strike_decay_after: 'quality.strike_decay_after',
      p_strike_decay_min_healthy: 'quality.strike_decay_min_healthy',
      p_min_generation_ms: 'quality.min_generation_ms',
      p_cross_verify: 'quality.cross_verify',
      p_min_passive_output: 'quality.min_passive_output_tokens',
      p_min_encrypted_bytes: 'quality.min_encrypted_bytes',
      p_encrypted_per_token: 'quality.encrypted_bytes_per_reasoning_token',
      p_missing_cooldown: 'quality.missing_thinking_cooldown',
      p_empty_cooldown: 'quality.empty_stream_cooldown',
      p_min_hold_output: 'quality.min_hold_output_tokens',
      p_hold_timeout: 'quality.hold_timeout',
      p_stream_idle_timeout: 'quality.stream_idle_timeout',
      p_attempt_timeout: 'quality.attempt_timeout',
      p_request_timeout: 'quality.request_timeout',
      p_hold_keepalive: 'quality.hold_keepalive',
      p_hold_max_bytes: 'quality.hold_max_buffer_bytes',
      p_max_attempts: 'quality.max_attempts',
      p_on_exhausted: 'quality.on_exhausted',
      p_probe_healthy_interval_hours: 'probe.healthy_interval_hours',
      p_probe_model: 'probe.model',
      p_probe_timeout: 'probe.timeout',
      p_probe_max_output: 'probe.max_output_tokens',
      p_probe_concurrency: 'probe.concurrency',
      p_probe_queue_size: 'probe.queue_size',
      p_probe_scan_interval: 'probe.scan_interval',
      p_probe_unknown_delay: 'probe.unknown_delay',
      p_probe_unknown_jitter: 'probe.unknown_jitter',
      p_probe_suspect_backoff: 'probe.suspect_backoff',
      p_probe_max_backoff: 'probe.max_backoff',
      p_task_history_retention_days: 'task_history.retention_days',
      p_task_history_max_records: 'task_history.max_records',
      p_task_history_max_bytes_mib: 'task_history.max_bytes',
      p_task_history_max_items: 'task_history.max_items_per_task'
    };
    // Fields whose schema unit differs from the input unit.
    const POLICY_SCHEMA_SCALE = { p_task_history_max_bytes_mib: 1 / 1048576 };
    async function loadPolicySchema() {
      try {
        const response = await fetch(POLICY_SCHEMA_ENDPOINT, { headers: { 'Accept-Language': i18n.locale } });
        if (!response || !response.ok) return;
        const payload = await response.json();
        const fields = payload && payload.data && Array.isArray(payload.data.fields) ? payload.data.fields : [];
        POLICY_SCHEMA_BY_ID.clear();
        const byPath = new Map(fields.map((field) => [field.path, field]));
        for (const [id, path] of Object.entries(FIELD_ID_TO_PATH)) {
          const spec = byPath.get(path);
          if (spec) POLICY_SCHEMA_BY_ID.set(id, spec);
        }
        applyPolicySchemaBounds();
      } catch (_) { }
    }
    // applyPolicySchemaBounds copies the backend ranges onto the controls so the
    // browser rejects out-of-range values with the same limits.
    function applyPolicySchemaBounds() {
      POLICY_SCHEMA_BY_ID.forEach((spec, id) => {
        const field = document.getElementById(id);
        if (!field) return;
        const scale = POLICY_SCHEMA_SCALE[id] || 1;
        if (spec.type === 'duration') {
          field.dataset.minSeconds = Number.isFinite(Number(spec.min)) ? String(Number(spec.min)) : '';
          field.dataset.maxSeconds = Number.isFinite(Number(spec.max)) ? String(Number(spec.max)) : '';
          return;
        }
        if (spec.type === 'enum' && Array.isArray(spec.enum)) field.dataset.schemaEnum = spec.enum.join(',');
        if (Number.isFinite(Number(spec.min)) && Number(spec.min) !== 0) field.min = String(Number(spec.min) * scale);
        if (Number.isFinite(Number(spec.max)) && Number(spec.max) !== 0) field.max = String(Number(spec.max) * scale);
        if (spec.type === 'integer') field.step = '1';
      });
    }
    // POLICY_NUMBER_MESSAGES keeps the field-specific copy only; the bounds
    // themselves come from the schema fetched above.
    const POLICY_NUMBER_MESSAGES = {
      p_max_accounts_per_bucket: 'ui.score_bucket_limit_invalid',
      p_score_success_weight: 'ui.score_weights_invalid',
      p_score_ttft_weight: 'ui.score_weights_invalid',
      p_score_tps_weight: 'ui.score_weights_invalid',
      p_score_probe_weight: 'ui.score_evidence_invalid',
      p_score_transparent_weight: 'ui.score_evidence_invalid',
      p_score_passive_weight: 'ui.score_evidence_invalid',
      p_score_drift_threshold: 'ui.score_drift_invalid',
      p_task_history_retention_days: 'ui.retention_days_range',
      p_task_history_max_records: 'ui.history_records_range',
      p_task_history_max_bytes_mib: 'ui.history_bytes_range',
      p_task_history_max_items: 'ui.task_items_range',
      p_probe_queue_size: 'ui.queue_capacity_range',
      p_probe_healthy_interval_hours: 'ui.healthy_probe_interval_range'
    };
    const POLICY_DURATION_MESSAGES = {
      p_session_affinity_ttl: 'ui.session_ttl_invalid',
      p_score_min_interval: 'ui.score_min_interval_invalid',
      p_score_max_age: 'ui.score_max_age_invalid'
    };
    const SCORE_METRIC_WEIGHTS = ['p_score_success_weight', 'p_score_ttft_weight', 'p_score_tps_weight'];
    const SCORE_EVIDENCE_WEIGHTS = ['p_score_probe_weight', 'p_score_transparent_weight', 'p_score_passive_weight'];
    const SCORE_REFRESH_FIELDS = ['p_score_min_interval', 'p_score_max_age'];
    const SOFT_HARD_FIELDS = ['p_soft_tps', 'p_hard_tps'];

    function parsePolicyDurationSeconds(text) {
      const input = String(text || '').trim();
      if (!input) return NaN;
      const units = { ns: 1e-9, us: 1e-6, 'µs': 1e-6, 'μs': 1e-6, ms: 1e-3, s: 1, m: 60, h: 3600 };
      const pattern = /([+-]?(?:\d+(?:\.\d*)?|\.\d+))(ns|us|µs|μs|ms|s|m|h)/g;
      let total = 0;
      let consumed = 0;
      let match;
      while ((match = pattern.exec(input)) !== null) {
        if (match.index !== consumed) return NaN;
        total += Number(match[1]) * units[match[2]];
        consumed = pattern.lastIndex;
      }
      return consumed === input.length ? total : NaN;
    }

    function policyFieldOwnError(id) {
      const field = document.getElementById(id);
      if (!field) return '';
      const raw = String(field.value == null ? '' : field.value).trim();
      if (id === 'p_state_file') return raw ? '' : i18n.t('ui.state_file_required');
      const type = String(field.getAttribute('type') || field.type || '').toLowerCase();
      if (type === 'number') {
        const value = Number(raw);
        const minRaw = field.getAttribute('min');
        const maxRaw = field.getAttribute('max');
        const stepRaw = field.getAttribute('step');
        const min = minRaw == null || minRaw === '' ? null : Number(minRaw);
        const max = maxRaw == null || maxRaw === '' ? null : Number(maxRaw);
        const integer = stepRaw == null || stepRaw === '' || Number(stepRaw) === 1;
        const specific = POLICY_NUMBER_MESSAGES[id];
        if (!raw || !Number.isFinite(value) || (integer && !Number.isInteger(value)) ||
          (min != null && value < min) || (max != null && value > max)) {
          if (specific) return i18n.t(specific);
          if (!raw) return i18n.t('ui.policy_number_required');
          if (!Number.isFinite(value)) return i18n.t('ui.policy_number_required');
          if (integer && !Number.isInteger(value)) return i18n.t('ui.policy_integer_required');
          if (min != null && max != null) return i18n.t('ui.policy_number_range', { min, max });
          if (min != null) return i18n.t('ui.policy_number_min', { min });
          return i18n.t('ui.policy_number_max', { max });
        }
      }
      if (POLICY_DURATION_FIELDS.has(id)) {
        const message = POLICY_DURATION_MESSAGES[id] || 'ui.policy_duration_invalid';
        const seconds = parsePolicyDurationSeconds(raw);
        if (!raw || !Number.isFinite(seconds)) return i18n.t(message);
        const rule = POLICY_SCHEMA_BY_ID.get(id);
        const minSeconds = rule && rule.min != null ? Number(rule.min) : 0;
        const maxSeconds = rule && rule.max != null && Number(rule.max) !== 0 ? Number(rule.max) : null;
        if ((minSeconds > 0 && seconds < minSeconds) || (maxSeconds != null && seconds > maxSeconds)) return i18n.t(message);
      }
      return '';
    }

    function setPolicyFieldError(id, message) {
      const field = document.getElementById(id);
      const container = field?.closest?.('.field');
      if (!field || !container) return;
      let error = container.querySelector('.field-error');
      if (!message && !error) return;
      if (!error) {
        error = document.createElement('span');
        error.className = 'field-error';
        error.id = id + '-error';
        error.setAttribute('role', 'alert');
        error.setAttribute('aria-live', 'polite');
        container.appendChild(error);
      }
      error.textContent = message || '';
      error.hidden = !message;
      if (message) {
        field.setAttribute('aria-invalid', 'true');
        field.setAttribute('aria-describedby', error.id);
      } else {
        field.removeAttribute('aria-invalid');
        if (field.getAttribute('aria-describedby') === error.id) field.removeAttribute('aria-describedby');
      }
    }

    function validateWeightGroup(ids, messageKey) {
      const ownErrors = ids.map((id) => policyFieldOwnError(id));
      ids.forEach((id, index) => setPolicyFieldError(id, ownErrors[index]));
      if (ownErrors.some(Boolean)) return false;
      const values = ids.map((id) => Number(String(document.getElementById(id)?.value || '').trim()));
      if (values.reduce((sum, value) => sum + value, 0) > 0) return true;
      ids.forEach((id) => setPolicyFieldError(id, i18n.t(messageKey)));
      return false;
    }

    function validateSoftHardPair() {
      const ownErrors = SOFT_HARD_FIELDS.map((id) => policyFieldOwnError(id));
      SOFT_HARD_FIELDS.forEach((id, index) => setPolicyFieldError(id, ownErrors[index]));
      if (ownErrors.some(Boolean)) return false;
      const soft = Number(valueOf('p_soft_tps'));
      const hard = Number(valueOf('p_hard_tps'));
      if (soft < hard) return true;
      setPolicyFieldError('p_hard_tps', i18n.t('ui.soft_tps_below_hard'));
      return false;
    }

    function validateScoreRefreshPair() {
      const ownErrors = SCORE_REFRESH_FIELDS.map((id) => policyFieldOwnError(id));
      SCORE_REFRESH_FIELDS.forEach((id, index) => setPolicyFieldError(id, ownErrors[index]));
      if (ownErrors.some(Boolean)) return false;
      const minSeconds = parsePolicyDurationSeconds(valueOf('p_score_min_interval'));
      const maxSeconds = parsePolicyDurationSeconds(valueOf('p_score_max_age'));
      if (maxSeconds >= minSeconds) return true;
      setPolicyFieldError('p_score_max_age', i18n.t('ui.score_refresh_order_invalid'));
      return false;
    }

    function validatePolicyField(id) {
      if (SCORE_METRIC_WEIGHTS.includes(id)) return validateWeightGroup(SCORE_METRIC_WEIGHTS, 'ui.score_weights_invalid');
      if (SCORE_EVIDENCE_WEIGHTS.includes(id)) return validateWeightGroup(SCORE_EVIDENCE_WEIGHTS, 'ui.score_evidence_invalid');
      if (SOFT_HARD_FIELDS.includes(id)) return validateSoftHardPair();
      if (SCORE_REFRESH_FIELDS.includes(id)) return validateScoreRefreshPair();
      const message = policyFieldOwnError(id);
      setPolicyFieldError(id, message);
      return !message;
    }

    function validatePolicyForm() {
      let valid = true;
      document.querySelectorAll('#policyForm input[type="number"]').forEach((field) => {
        if (!validatePolicyField(field.id)) valid = false;
      });
      POLICY_DURATION_FIELDS.forEach((id) => {
        if (!validatePolicyField(id)) valid = false;
      });
      if (!validatePolicyField('p_state_file')) valid = false;
      const firstError = document.querySelector('#policyForm [aria-invalid="true"]');
      if (firstError) {
        valid = false;
        firstError.focus?.();
      }
      return valid;
    }

    function clearPolicyValidation() {
      const form = document.getElementById('policyForm');
      if (!form) return;
      form.querySelectorAll('.field-error').forEach((error) => {
        error.textContent = '';
        error.hidden = true;
      });
      form.querySelectorAll('[aria-invalid="true"]').forEach((field) => {
        field.removeAttribute('aria-invalid');
        field.removeAttribute('aria-describedby');
      });
    }

    function populatePolicyForm() {
      if (isFormDirty('policyForm')) return;
      clearPolicyValidation();
      const policy = state.policy || {};
      const quality = policy.quality || {};
      const probe = policy.probe || {};
      const routing = policy.routing || {};
      const strategy = routing.strategy || {};
      const affinity = routing.affinity || {};
      const scoring = routing.scoring || {};
      const scoreWeights = scoring.weights || {};
      const evidenceWeights = scoring.evidence || {};
      const scoreRefresh = scoring.refresh || {};
      const taskHistory = policy.task_history || {};
      setValue('p_guard', policy.guard || 'passive');
      setValue('p_auto_action', policy.auto_action || 'observe');
      setValue('p_model_prefixes', (policy.model_prefixes || ['grok-']).join(', '));
      setValue('p_events_limit', policy.events_limit || 1000);
      setChecked('p_task_history_enabled', taskHistory.enabled !== false);
      setValue('p_task_history_retention_days', taskHistory.retention_days || 3);
      setValue('p_task_history_max_records', taskHistory.max_records || 500);
      setValue('p_task_history_max_bytes_mib', Math.max(1, Math.round(Number(taskHistory.max_bytes || 134217728) / 1048576)));
      setValue('p_task_history_max_items', taskHistory.max_items_per_task || 10000);
      setValue('p_state_file', (state.overview && state.overview.state_file) || '');
      setValue('p_selection_strategy', strategy.selection || 'round_robin');
      setChecked('p_prefer_last_success', strategy.prefer_last_success !== false);
      setChecked('p_session_affinity', affinity.enabled !== false);
      setValue('p_session_affinity_ttl', affinity.ttl || '1h');
      setValue('p_session_affinity_max_entries', affinity.max_entries || 10000);
      setChecked('p_scoring_enabled', scoring.enabled !== false);
      setValue('p_max_accounts_per_bucket', scoring.max_accounts_per_bucket || 512);
      setValue('p_score_success_weight', scoreWeights.success ?? 0.5);
      setValue('p_score_ttft_weight', scoreWeights.ttft ?? 0.3);
      setValue('p_score_tps_weight', scoreWeights.tps ?? 0.2);
      setValue('p_score_probe_weight', evidenceWeights.probe ?? 0.65);
      setValue('p_score_transparent_weight', evidenceWeights.transparent ?? 0.35);
      setValue('p_score_passive_weight', evidenceWeights.passive ?? 0);
      setValue('p_score_min_interval', scoreRefresh.min_interval || '5m');
      setValue('p_score_max_age', scoreRefresh.max_age || '30m');
      setValue('p_score_drift_threshold', scoreRefresh.drift_threshold ?? 0.03);
      setValue('p_score_half_life', scoring.half_life_hours || 168);
      setValue('p_soft_tps', quality.soft_tps || 500);
      setValue('p_non_reasoning_models', (quality.non_reasoning_models || []).join(', '));
      setValue('p_hard_tps', quality.hard_tps || 1000);
      setValue('p_consecutive_soft', quality.consecutive_soft || 2);
      setValue('p_consecutive_errors', quality.consecutive_errors || 2);
      setValue('p_consecutive_missing_thinking', quality.consecutive_missing_thinking || 1);
      setValue('p_strike_decay_after', quality.strike_decay_after ?? 48);
      setValue('p_strike_decay_min_healthy', quality.strike_decay_min_healthy ?? 10);
      setChecked('p_thinking_guard', quality.thinking_guard !== false);
      setValue('p_cross_verify', quality.cross_verify || 'thinking');
      setValue('p_min_generation_ms', quality.min_generation_ms || 1000);
      setChecked('p_burst_guard', quality.burst_guard === true);
      setValue('p_min_passive_output', quality.min_passive_output_tokens || 32);
      setValue('p_min_hold_output', quality.min_hold_output_tokens || 8);
      setValue('p_hold_timeout', quality.hold_timeout || '30s');
      setValue('p_stream_idle_timeout', quality.stream_idle_timeout || '90s');
      setValue('p_attempt_timeout', quality.attempt_timeout || '600s');
      setValue('p_request_timeout', quality.request_timeout || '900s');
      setValue('p_hold_keepalive', quality.hold_keepalive || '5s');
      setChecked('p_shadow_mode', quality.shadow_mode === true);
      setChecked('p_debug_logging', quality.debug_logging === true);
      setValue('p_hold_max_bytes', quality.hold_max_buffer_bytes || 4194304);
      setValue('p_min_encrypted_bytes', quality.min_encrypted_bytes || 256);
      setValue('p_encrypted_per_token', quality.encrypted_bytes_per_reasoning_token || 1);
      setValue('p_missing_cooldown', quality.missing_thinking_cooldown || '12h');
      setValue('p_empty_cooldown', quality.empty_stream_cooldown || '15m');
      setValue('p_max_attempts', quality.max_attempts || 4);
      setValue('p_on_exhausted', quality.on_exhausted || 'fail_unverified');
      setValue('p_probe_healthy_interval_hours', probe.healthy_interval_hours ?? 0);
      setValue('p_probe_model', probe.model || 'grok-4.5');
      setValue('p_probe_timeout', probe.timeout || '90s');
      setValue('p_probe_max_output', probe.max_output_tokens || 768);
      setValue('p_probe_concurrency', probe.concurrency || 1);
      setValue('p_probe_queue_size', probe.queue_size || 10000);
      setValue('p_probe_scan_interval', probe.scan_interval || '15s');
      setValue('p_probe_unknown_delay', probe.unknown_delay || '30s');
      setValue('p_probe_unknown_jitter', probe.unknown_jitter || '30s');
      setValue('p_probe_suspect_backoff', probe.suspect_backoff || '15m');
      setValue('p_probe_max_backoff', probe.max_backoff || '1h');
      document.getElementById('policyModeBadge').textContent = modeLabel(policy);
    }
    function collectPolicy() {
      const policy = JSON.parse(JSON.stringify(state.policy || {}));
      policy.guard = valueOf('p_guard');
      policy.auto_action = valueOf('p_auto_action');
      policy.model_prefixes = (valueOf('p_model_prefixes') || '').split(',').map((item) => item.trim()).filter(Boolean);
      policy.events_limit = numberValue('p_events_limit', 1000);
      policy.task_history = policy.task_history || {};
      policy.task_history.enabled = document.getElementById('p_task_history_enabled').checked;
      policy.task_history.retention_days = numberValue('p_task_history_retention_days', 3);
      policy.task_history.max_records = numberValue('p_task_history_max_records', 500);
      policy.task_history.max_bytes = Math.round(numberValue('p_task_history_max_bytes_mib', 128) * 1048576);
      policy.task_history.max_items_per_task = numberValue('p_task_history_max_items', 10000);
      policy.routing = policy.routing || {};
      policy.routing.strategy = policy.routing.strategy || {};
      policy.routing.strategy.selection = valueOf('p_selection_strategy') || 'round_robin';
      policy.routing.strategy.prefer_last_success = document.getElementById('p_prefer_last_success').checked;
      policy.routing.affinity = policy.routing.affinity || {};
      policy.routing.affinity.enabled = document.getElementById('p_session_affinity').checked;
      policy.routing.affinity.ttl = valueOf('p_session_affinity_ttl');
      policy.routing.affinity.max_entries = numberValue('p_session_affinity_max_entries', 10000);
      policy.routing.scoring = policy.routing.scoring || {};
      policy.routing.scoring.enabled = document.getElementById('p_scoring_enabled').checked;
      policy.routing.scoring.max_accounts_per_bucket = numberValue('p_max_accounts_per_bucket', 512);
      policy.routing.scoring.weights = policy.routing.scoring.weights || {};
      policy.routing.scoring.weights.success = signedNumberValue('p_score_success_weight', 0.5);
      policy.routing.scoring.weights.ttft = signedNumberValue('p_score_ttft_weight', 0.3);
      policy.routing.scoring.weights.tps = signedNumberValue('p_score_tps_weight', 0.2);
      policy.routing.scoring.evidence = policy.routing.scoring.evidence || {};
      policy.routing.scoring.evidence.probe = signedNumberValue('p_score_probe_weight', 0.65);
      policy.routing.scoring.evidence.transparent = signedNumberValue('p_score_transparent_weight', 0.35);
      policy.routing.scoring.evidence.passive = signedNumberValue('p_score_passive_weight', 0);
      policy.routing.scoring.refresh = policy.routing.scoring.refresh || {};
      policy.routing.scoring.refresh.min_interval = valueOf('p_score_min_interval');
      policy.routing.scoring.refresh.max_age = valueOf('p_score_max_age');
      policy.routing.scoring.refresh.drift_threshold = signedNumberValue('p_score_drift_threshold', 0.03);
      policy.routing.scoring.half_life_hours = numberValue('p_score_half_life', 168);
      policy.quality = policy.quality || {};
      policy.quality.soft_tps = numberValue('p_soft_tps', 500);
      policy.quality.non_reasoning_models = (valueOf('p_non_reasoning_models') || '').split(',').map((item) => item.trim()).filter(Boolean);
      policy.quality.hard_tps = numberValue('p_hard_tps', 1000);
      policy.quality.consecutive_soft = numberValue('p_consecutive_soft', 2);
      policy.quality.consecutive_errors = numberValue('p_consecutive_errors', 2);
      policy.quality.consecutive_missing_thinking = numberValue('p_consecutive_missing_thinking', 1);
      policy.quality.strike_decay_after = numberValue('p_strike_decay_after', 48);
      policy.quality.strike_decay_min_healthy = numberValue('p_strike_decay_min_healthy', 10);
      policy.quality.thinking_guard = document.getElementById('p_thinking_guard').checked;
      policy.quality.cross_verify = valueOf('p_cross_verify');
      policy.quality.min_generation_ms = numberValue('p_min_generation_ms', 1000);
      policy.quality.burst_guard = document.getElementById('p_burst_guard').checked;
      policy.quality.min_passive_output_tokens = numberValue('p_min_passive_output', 32);
      policy.quality.min_hold_output_tokens = numberValue('p_min_hold_output', 8);
      policy.quality.hold_timeout = valueOf('p_hold_timeout');
      policy.quality.stream_idle_timeout = valueOf('p_stream_idle_timeout');
      policy.quality.attempt_timeout = valueOf('p_attempt_timeout');
      policy.quality.request_timeout = valueOf('p_request_timeout');
      policy.quality.hold_keepalive = valueOf('p_hold_keepalive');
      policy.quality.shadow_mode = document.getElementById('p_shadow_mode').checked;
      policy.quality.debug_logging = document.getElementById('p_debug_logging').checked;
      policy.quality.hold_max_buffer_bytes = numberValue('p_hold_max_bytes', 4194304);
      policy.quality.min_encrypted_bytes = numberValue('p_min_encrypted_bytes', 256);
      policy.quality.encrypted_bytes_per_reasoning_token = numberValue('p_encrypted_per_token', 1);
      policy.quality.missing_thinking_cooldown = valueOf('p_missing_cooldown');
      policy.quality.empty_stream_cooldown = valueOf('p_empty_cooldown');
      policy.quality.max_attempts = numberValue('p_max_attempts', 4);
      policy.quality.on_exhausted = valueOf('p_on_exhausted');
      policy.probe = policy.probe || {};
      policy.probe.healthy_interval_hours = nonNegativeIntegerValue('p_probe_healthy_interval_hours', 0);
      policy.probe.model = valueOf('p_probe_model');
      policy.probe.timeout = valueOf('p_probe_timeout');
      policy.probe.max_output_tokens = numberValue('p_probe_max_output', 768);
      policy.probe.concurrency = numberValue('p_probe_concurrency', 1);
      policy.probe.queue_size = numberValue('p_probe_queue_size', 10000);
      policy.probe.scan_interval = valueOf('p_probe_scan_interval');
      policy.probe.unknown_delay = valueOf('p_probe_unknown_delay');
      policy.probe.unknown_jitter = valueOf('p_probe_unknown_jitter');
      policy.probe.suspect_backoff = valueOf('p_probe_suspect_backoff');
      policy.probe.max_backoff = valueOf('p_probe_max_backoff');
      return policy;
    }
    async function savePolicy(button) {
      const revision = formDraftRevision('policyForm');
      if (!validatePolicyForm()) {
        toast(i18n.t('ui.policy_fix_invalid_fields'), 'error');
        return;
      }
      const policy = collectPolicy();
      const nextStateFile = valueOf('p_state_file');
      if (!(policy.quality.soft_tps < policy.quality.hard_tps)) { toast(i18n.t("ui.soft_tps_below_hard"), 'error'); return; }
      if (!(policy.probe.queue_size >= 1 && policy.probe.queue_size <= 1000000)) { toast(i18n.t("ui.queue_capacity_range"), 'error'); return; }
      if (!Number.isInteger(policy.probe.healthy_interval_hours) || policy.probe.healthy_interval_hours < 0) { toast(i18n.t("ui.healthy_probe_interval_range"), 'error'); return; }
      if (!policy.routing.affinity.ttl) { toast(i18n.t("ui.session_ttl_required"), 'error'); return; }
      const scoring = policy.routing.scoring;
      if (!Number.isInteger(scoring.max_accounts_per_bucket) || !(scoring.max_accounts_per_bucket >= 16 && scoring.max_accounts_per_bucket <= 4096)) { toast(i18n.t("ui.score_bucket_limit_invalid"), 'error'); return; }
      const metricWeights = [scoring.weights.success, scoring.weights.ttft, scoring.weights.tps];
      if (!metricWeights.every(value => Number.isFinite(value) && value >= 0 && value <= 1) || metricWeights.reduce((sum, value) => sum + value, 0) <= 0) { toast(i18n.t("ui.score_weights_invalid"), 'error'); return; }
      const evidenceWeights = [scoring.evidence.probe, scoring.evidence.transparent, scoring.evidence.passive];
      if (!evidenceWeights.every(value => Number.isFinite(value) && value >= 0 && value <= 1) || evidenceWeights.reduce((sum, value) => sum + value, 0) <= 0) { toast(i18n.t("ui.score_evidence_invalid"), 'error'); return; }
      if (!scoring.refresh.min_interval || !scoring.refresh.max_age) { toast(i18n.t("ui.score_refresh_invalid"), 'error'); return; }
      if (!Number.isFinite(scoring.refresh.drift_threshold) || !(scoring.refresh.drift_threshold >= 0 && scoring.refresh.drift_threshold <= 1)) { toast(i18n.t("ui.score_drift_invalid"), 'error'); return; }
      if (!nextStateFile) { toast(i18n.t("ui.state_file_required"), 'error'); return; }
      const taskHistory = policy.task_history || {};
      if (!(taskHistory.retention_days >= 1 && taskHistory.retention_days <= 365)) { toast(i18n.t("ui.retention_days_range"), 'error'); return; }
      if (!(taskHistory.max_records >= 1 && taskHistory.max_records <= 10000)) { toast(i18n.t("ui.history_records_range"), 'error'); return; }
      if (!(taskHistory.max_bytes >= 1048576 && taskHistory.max_bytes <= 4294967296)) { toast(i18n.t("ui.history_bytes_range"), 'error'); return; }
      if (!(taskHistory.max_items_per_task >= 1 && taskHistory.max_items_per_task <= 100000)) { toast(i18n.t("ui.task_items_range"), 'error'); return; }
      setBusy(button, true);
      document.getElementById('policyStatus').textContent = i18n.t("ui.saving_policy");
      try {
        const currentStateFile = String((state.overview && state.overview.state_file) || '').trim();
        if (nextStateFile !== currentStateFile) {
          document.getElementById('policyStatus').textContent = i18n.t("ui.switching_state_file");
          const moved = await api('state_file.update', { state_file: nextStateFile });
          if (moved && moved.state_file) setValue('p_state_file', moved.state_file);
        }
        await api('policy.update', { policy: policy });
        clearPolicyValidation();
        clearFormDraft('policyForm', revision);
        document.getElementById('policyStatus').textContent = i18n.t("ui.settings_saved_hot_reload");
        toast(i18n.t("ui.settings_saved"), 'success');
        await refreshAll(true);
      } catch (error) {
        document.getElementById('policyStatus').textContent = i18n.t('ui.save_failed', { error: error.message });
        toast(error.message, 'error');
      } finally { setBusy(button, false); }
    }
