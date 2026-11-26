import assert from 'node:assert/strict';
import { Element, deferred, harness, html, localeResources, plain, test } from '../support.mjs';

test('A late task-center detail response cannot replace a newer selection', async () => {
  const { dashboard: ui, mock } = harness();
  const old = deferred(), fresh = deferred();
  let requests = 0;
  mock('api', () => (++requests === 1 ? old.promise : fresh.promise));
  ui.state.taskCenterSelectedID = 'task-1';
  const first = ui.loadTaskCenterDetail('task-1', 1, 'all', false);
  ui.state.taskCenterSelectedID = 'task-2';
  const second = ui.loadTaskCenterDetail('task-2', 1, 'all', false);
  fresh.resolve({ task: { task_id: 'task-2', status: 'succeeded' }, items: [], total: 0, item_total: 0, page: 1, page_size: 20, source: 'history' });
  await second;
  assert.equal(ui.state.taskCenterDetail.task.task_id, 'task-2');
  old.resolve({ task: { task_id: 'task-1', status: 'succeeded' }, items: [], total: 0, item_total: 0, page: 1, page_size: 20, source: 'history' });
  await first;
  assert.equal(ui.state.taskCenterDetail.task.task_id, 'task-2');
});
test('Settings toggles share one row style with info icons', () => {
  for (const [id, title, hint] of [
    ['p_scoring_enabled', 'ui.enable_health_scoring', 'ui.score_enabled_hint'],
    ['p_task_history_enabled', 'ui.save_task_history', 'ui.stop_saving_new_task_records_when_disabled'],
    ['p_prefer_last_success', 'ui.prefer_the_last_successful_account', 'ui.use_it_only_when_healthy_and_idle_spread_concurrent'],
    ['p_session_affinity', 'ui.enable_session_affinity', 'ui.keep_a_session_on_the_same_auth_while_the'],
    ['p_thinking_guard', 'ui.enable_missing_reasoning_detection', 'ui.when_disabled_only_tps_empty_stream_and_similar_quality'],
    ['p_burst_guard', 'ui.strict_short_window_detection', 'ui.when_enabled_a_very_short_generation_window_with_tps'],
    ['p_shadow_mode', 'ui.shadow_mode', 'ui.shadow_mode_quality_observations_only']
  ]) {
    assert.match(html, new RegExp('<label class="toggle-row"><input id="' + id + '" type="checkbox"><span class="toggle-title">' + title.replace(/\./g, '\.') + '<span class="hint-icon" tabindex="0" role="img" aria-label="' + hint.replace(/\./g, '\.') + '" title="' + hint.replace(/\./g, '\.') + '">'));
  }
  const settings = html.slice(html.indexOf('id="panel-settings"'), html.indexOf('</main>'));
  assert.doesNotMatch(settings, /class="check-card|class="check-row|class="hint"/);
  assert.doesNotMatch(html, /function upgradeHints/);
  assert.match(html, /\.field>\.field-error\s*\{\s*color: var\(--bad\);/);
  assert.match(html, /\.settings-group \.retry-grid\s*\{\s*grid-template-columns: repeat\(3, minmax\(0, 1fr\)\);/);
  assert.match(html, /class="field-grid retry-grid"/);
  assert.match(html, /ui\.wait_time_after_the_first_failure/);
  assert.match(html, /ui\.longest_backoff_wait/);
});

test('Every settings field puts its explanation behind an info icon', () => {
  const settings = html.slice(html.indexOf('id="policyForm"'), html.indexOf('class="form-actions policy-actions"'));
  const fields = settings.match(/<div class="field[^"]*"><span class="field-head"><label for="p_[a-z_0-9]+">[^<]+<\/label><span class="hint-icon"[^>]*aria-label="[^"]+" title="[^"]+">/g) || [];
  const controls = settings.match(/<(?:input|select) id="p_[a-z_0-9]+"/g) || [];
  const toggles = settings.match(/<label class="toggle-row"><input id="p_/g) || [];
  assert.equal(fields.length + toggles.length, controls.length);
});

test('Stream idle timeout round-trips as its own field', () => {
  // The idle window is separate from attempt_timeout: one bounds upstream
  // silence, the other bounds the whole attempt.
  assert.match(html, /<input id="p_stream_idle_timeout" type="text" placeholder="90s">/);
  assert.match(html, /'p_stream_idle_timeout',/);
  assert.match(html, /p_stream_idle_timeout: 'quality\.stream_idle_timeout',/);
  assert.match(html, /setValue\('p_stream_idle_timeout', quality\.stream_idle_timeout \|\| '90s'\);/);
  assert.match(html, /policy\.quality\.stream_idle_timeout = valueOf\('p_stream_idle_timeout'\);/);
});

test('Settings keep the requested field layout', () => {
  const settings = html.slice(html.indexOf('id="policyForm"'), html.indexOf('class="form-actions policy-actions"'));
  // model_prefixes and state_file share one full-width row split 1:2.
  assert.match(settings, /<div class="field-pair">\s*<div class="field"><span class="field-head"><label for="p_model_prefixes">/);
  assert.match(settings, /<div class="field-pair">[\s\S]*?<div class="field"><span class="field-head"><label for="p_state_file">/);
  assert.match(html, /\.policy-group \.field-pair\s*\{[\s\S]*?grid-column: 1 \/ -1;[\s\S]*?grid-template-columns: minmax\(0, 1fr\) minmax\(0, 2fr\);/);
  // non_reasoning_models sits directly after empty_stream_cooldown on the same row.
  const emptyIndex = settings.indexOf('<label for="p_empty_cooldown">');
  const nonReasoningIndex = settings.indexOf('<label for="p_non_reasoning_models">');
  assert.ok(emptyIndex > 0 && nonReasoningIndex > emptyIndex);
  const between = settings.slice(emptyIndex, nonReasoningIndex);
  assert.equal((between.match(/<div class="field/g) || []).length, 1);
  assert.match(settings, /<div class="field"><span class="field-head"><label for="p_non_reasoning_models">/);
});

test('Overview cards pair into equal-height rows', () => {
  assert.doesNotMatch(html, /<div class="overview-grid">\s*<div class="stack">/);
  assert.match(html, /<div class="overview-grid">\s*<section class="card overview-card" aria-labelledby="qualityTitle">[\s\S]*?aria-labelledby="policySummaryTitle"[\s\S]*?aria-labelledby="recentTitle"[\s\S]*?aria-labelledby="resinSummaryTitle"/);
  assert.match(html, /\.overview-grid\s*\{[\s\S]*?align-items:\s*stretch/);
  assert.match(html, /\.overview-card\s*\{[\s\S]*?display: flex;[\s\S]*?flex-direction: column/);
  assert.match(html, /\.overview-card > \.card-body,\s*\.overview-card > \.recent-list\s*\{\s*flex: 1 1 auto;/);
});

test('Policy settings group parameters by type in a single column', () => {
  assert.match(html, /<form id="policyForm" class="settings-layout policy-settings-form" novalidate>/);
  assert.match(html, /<section class="card policy-summary-card">[\s\S]*?<div class="policy-chips" id="settingsPolicySummary"><\/div>/);
  assert.match(html, /<div class="policy-settings-grid">/);
  const order = [
    'ui.operating_mode_and_actions',
    'ui.transparent_auth_selection_and_session_affinity',
    'ui.ewma_health_ranking',
    'ui.quality_thresholds_and_reasoning_evidence',
    'ui.transparent_stream_protection',
    'ui.active_probes',
    'ui.task_center_and_history'
  ].map((key) => html.indexOf('</svg>' + key + '</h3>'));
  assert.ok(order.every((index, position) => index > 0 && (position === 0 || order[position - 1] < index)));
  assert.match(html, /\.policy-settings-grid\s*\{[\s\S]*?grid-template-columns: minmax\(0, 1fr\);/);
  assert.match(html, /\.policy-group \.field-grid\s*\{\s*grid-template-columns: repeat\(4, minmax\(0, 1fr\)\);[\s\S]*?align-items: start;/);
  assert.match(html, /\.policy-group\.columns-3 \.field-grid\s*\{\s*grid-template-columns: repeat\(3, minmax\(0, 1fr\)\);/);
  assert.match(html, /\.field-head\s*\{[\s\S]*?min-height: 16px;/);
  assert.match(html, /@media \(max-width: 920px\)\s*\{[\s\S]*?\.policy-group \.field-grid,\s*\.policy-group\.columns-3 \.field-grid\s*\{\s*grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);/);
  assert.match(html, /@media \(max-width: 680px\)\s*\{[\s\S]*?\.policy-group \.field-grid,\s*\.policy-group\.columns-3 \.field-grid\s*\{\s*grid-template-columns: 1fr;/);
  assert.match(html, /class="form-actions policy-actions"/);
});

test('Template variable generation controls share one roomy row', () => {
  assert.match(html, /\.proxy-generation-grid\s*\{\s*grid-template-columns: repeat\(3, minmax\(0, 1fr\)\);/);
  assert.match(html, /\.proxy-variable-body \.field\s*\{\s*gap: 7px;/);
  assert.match(html, /\.proxy-variable-body \.field>label:not\(\.check-row\)\s*\{[\s\S]*?flex-direction: column;[\s\S]*?gap: 7px;/);
  assert.match(html, /<div class="field-grid proxy-generation-grid">/);
});

test('Routing affinity cards share a row and parameter hints explain their effects', () => {
  const ttl = html.indexOf('id="p_session_affinity_ttl"');
  const prefer = html.indexOf('id="p_prefer_last_success"');
  const affinity = html.indexOf('id="p_session_affinity"');
  assert(ttl > 0 && ttl < prefer && prefer < affinity);
  assert.match(html, /ui.health_records_expire_after_this_duration/);
  assert.match(html, /ui.choose_only_healthy_unexpired_proxies/);
  assert.match(html, /:password@proxy\.example:1080/);
  assert.doesNotMatch(html, /:密码@proxy\.example:1080/);
  assert.match(html, /id="p_probe_healthy_interval_hours" type="number" min="0" step="1"/);
  assert.match(html, /<label for="p_probe_healthy_interval_hours">[^<]+<\/label><span class="hint-icon"[^>]*aria-label="ui\.hours_zero_disables_active_healthy_probes"/);
  assert.match(html, /id="p_probe_scan_interval"/);
  assert.match(html, /ui.bind_nodes_templates_and_proxy_services_in_one_place/);
  assert.match(html, /<th>ui.endpoint<\/th>/);
  assert.match(html, /<option value="resin">Resin<\/option>/);
  assert.match(html, /proxyAddSourceKind/);
  assert.match(html, /ui.new_type/);
  assert.doesNotMatch(html, /resinFormHost|resinSourceBar|data-open-resin title=/);
});

test('Routing scoring controls expose dynamic buckets, weights, and request-driven refresh', () => {
  for (const id of [
    'p_scoring_enabled',
    'p_max_accounts_per_bucket',
    'p_score_success_weight',
    'p_score_ttft_weight',
    'p_score_tps_weight',
    'p_score_probe_weight',
    'p_score_transparent_weight',
    'p_score_passive_weight',
    'p_score_min_interval',
    'p_score_max_age',
    'p_score_drift_threshold'
  ]) {
    assert.match(html, new RegExp('id="' + id + '"'));
  }
  assert.match(html, /ui\.score_bucket_range_hint/);
  assert.doesNotMatch(html, /score-guidance|ui\.score_(?:bucket|weights|half_life)_hint/);
  assert.match(html, /ui\.score_enabled_hint/);
  assert.match(html, /ui\.score_success_hint/);
  assert.match(html, /ui\.score_ttft_hint/);
  assert.match(html, /ui\.score_tps_hint/);
  assert.match(html, /ui\.score_probe_hint/);
  assert.match(html, /ui\.score_transparent_hint/);
  assert.match(html, /ui\.score_passive_hint/);
  assert.match(html, /ui\.score_min_interval_hint/);
  assert.match(html, /ui\.score_refresh_hint/);
  assert.match(html, /ui\.score_drift_hint/);
  assert.doesNotMatch(html, /ui\.score_toggle_label|ui\.task_history_toggle_label/);
  assert.match(html, /ui\.enable_health_scoring/);
  assert.match(html, /ui\.score_min_interval_invalid/);
  assert.match(html, /ui\.score_max_age_invalid/);
  assert.match(html, /ui\.score_refresh_order_invalid/);
  assert.match(html, /ui\.policy_fix_invalid_fields/);
  for (const id of ['p_score_success_weight', 'p_score_ttft_weight', 'p_score_tps_weight', 'p_score_probe_weight', 'p_score_transparent_weight', 'p_score_passive_weight']) {
    assert.match(html, new RegExp('id="' + id + '"[^>]*max="1"'));
  }
  assert.match(html, /metricWeights\.every\(value => Number\.isFinite\(value\) && value >= 0 && value <= 1\)/);
  assert.match(html, /evidenceWeights\.every\(value => Number\.isFinite\(value\) && value >= 0 && value <= 1\)/);
  assert.match(html, /policy\.routing\.scoring\.max_accounts_per_bucket = numberValue/);
  assert.match(html, /policy\.routing\.scoring\.weights\.success = signedNumberValue/);
  assert.match(html, /policy\.routing\.scoring\.refresh\.drift_threshold = signedNumberValue/);
  assert.match(html, /addEventListener\('input', \(event\) => \{\s*if \(event\.target\?\.id\) validatePolicyField\(event\.target\.id\);/);
  assert.match(html, /if \(!validatePolicyForm\(\)\) \{\s*toast\(i18n\.t\('ui\.policy_fix_invalid_fields'\), 'error'\);/);
});

test('Policy fields validate inline and clear when corrected', async () => {
  const { dashboard: ui, get } = harness();
  const form = get('policyForm');
  const field = new Element('score-weight-field');
  const error = new Element('score-weight-error');
  field.selectors.set('.field-error', error);
  for (const [id, value] of [
    ['p_score_success_weight', '0.5'],
    ['p_score_ttft_weight', '0.3'],
    ['p_score_tps_weight', '0.2']
  ]) {
    const input = get(id);
    input.value = value;
    input.setAttribute('type', 'number');
    input.setAttribute('min', '0');
    input.setAttribute('max', '1');
    input.setAttribute('step', '0.01');
    input.ancestors = new Map();
  }
  const success = get('p_score_success_weight');
  success.ancestors.set('.field', field);

  success.value = '1.2';
  await form.emit('input', { target: success });
  assert.equal(success.getAttribute('aria-invalid'), 'true');
  assert.equal(error.hidden, false);
  assert.match(error.textContent, /0–1/);

  success.value = '0.5';
  await form.emit('input', { target: success });
  assert.equal(success.hasAttribute('aria-invalid'), false);
  assert.equal(error.hidden, true);
});

test('Network and management API failures use actionable Chinese copy', async () => {
  const { dashboard: ui, context, mock, get } = harness();
  context.fetch = async () => { throw new TypeError('Failed to fetch'); };
  await assert.rejects(ui.api('overview'), /无法连接管理接口，请检查 CPA 服务和插件状态/);
  context.fetch = async () => ({
    ok: false,
    status: 409,
    headers: { get: () => null },
    json: async () => ({ ok: false, error: { code: 'auto_bind_source_unavailable', message: 'automatic source availability changed' } })
  });
  await assert.rejects(ui.api('proxy.auto_bind.run'), (error) => {
    assert.equal(error.code, 'auto_bind_source_unavailable');
    assert.match(error.message, /自动绑定使用的代理已变化，将重新选择代理/);
    assert.doesNotMatch(error.message, /auto_bind_source_unavailable|automatic source availability changed/);
    return true;
  });
  await ui.openProxyResinEditor(null);
  assert.equal(get('proxySourceDrawer').hidden, false);
  assert.match(get('proxySourceEditor').innerHTML, /Resin 连接/);
});

test('Deep relations show two ancestors and a child with breadcrumb navigation', () => {
  const { dashboard: ui, groupCard } = harness();
  const group = ui.proxyGroupDraft({ id: 'deep', columns: ['a', 'b', 'c', 'd', 'e'], rows: [['A', 'B', 'C', 'D', 'E']] }, 0);
  assert.equal(group.childIndex, 0);
  group.childIndex = 4; group.path = ['A', 'B', 'C', 'D', ''];
  ui.state.proxyEditing = { groups: new Map([[0, group]]) };
  const card = groupCard(group);
  ui.renderProxyGroup(0);
  const rendered = card.querySelector('[data-group-body]').innerHTML;
  assert.equal((rendered.match(/data-group-path=/g) || []).length, 2);
  assert.equal((rendered.match(/data-group-breadcrumb=/g) || []).length, 4);
});

test('Bulk proxy import editor explains automatic URL shape parsing', async () => {
  const { dashboard: ui, get } = harness();
  await ui.openProxyImportEditor();
  assert.equal(get('proxySourceDrawer').hidden, false);
  assert.match(get('proxySourceEditor').innerHTML, /host:port@user:pass/);
  assert.match(get('proxySourceEditor').innerHTML, /socks5h:\/\/proxy\.example:1080:user:pass/);
  assert.match(get('proxySourceEditor').innerHTML, /http:\/\/proxy\.example:8080@user:pass/);
});

test('Bulk proxy import supports automatic address-first fallback', () => {
  const { dashboard: ui } = harness();
  const text = [
    '# node list',
    'socks5h://proxy.example:1080:alice:secret',
    'http://proxy.example:8080:u%40x:p%253A%40',
    'SOCKS5H://[2001:db8::1]:1080:alice:secret',
    'socks5h://proxy.example:1080:alice:secret',
    'http://proxy.example:8080:alice',
    'http://[2001:db8::1:1080:alice:secret',
    'http://proxy.example:8080:u:p:with:colons'
  ].join('\n');
  const parsed = ui.parseProxyImportText(text);
  assert.equal(parsed.lineCount, 8);
  assert.equal(parsed.validCount, 4);
  assert.equal(parsed.invalid, 2);
  assert.equal(parsed.duplicate, 1);
  assert.equal(parsed.comments, 1);
  assert.deepEqual(plain(parsed.valid), [
    'socks5h://proxy.example:1080:alice:secret',
    'http://proxy.example:8080:u%40x:p%253A%40',
    'SOCKS5H://[2001:db8::1]:1080:alice:secret',
    'http://proxy.example:8080:u:p:with:colons'
  ]);
  assert.equal(ui.proxyImportLine('socks5h://proxy.example:1080:alice:secret').valid, true);
  assert.equal(ui.proxyImportLine('http://alice:secret@proxy.example:8080').valid, true);
});

test('Bulk proxy import supports host-port-at-credentials fallback', () => {
  const { dashboard: ui } = harness();
  const text = [
    '# proxy list',
    'http://proxy.example:8080@alice:secret',
    'socks5h://[2001:db8::1]:1080@u%40x:p%253A%40',
    'http://proxy.example:8080@alice',
    'http://proxy.example:8080@:secret'
  ].join('\n');
  const parsed = ui.parseProxyImportText(text);
  assert.equal(parsed.lineCount, 5);
  assert.equal(parsed.validCount, 2);
  assert.equal(parsed.invalid, 2);
  assert.equal(parsed.comments, 1);
  assert.deepEqual(plain(parsed.valid), [
    'http://proxy.example:8080@alice:secret',
    'socks5h://[2001:db8::1]:1080@u%40x:p%253A%40'
  ]);
  assert.equal(ui.proxyImportLine('http://proxy.example:8080@user:pass').valid, true);
  assert.equal(ui.proxyImportLine('http://[2001:db8::1]:1080@user:pass').valid, true);
  assert.equal(ui.proxyImportLine('http://2001:db8::1:1080@user:pass').valid, false);
});

test('Proxy bind event shows binding details instead of an upstream error', () => {
  const { dashboard: ui, get } = harness();
  ui.state.events = [{ time: '2026-11-18T08:00:00Z', auth_id: 'xai-auth-1', action: 'proxy_bind', result: 'bound', detail: 'source=node-a account=lease-1 remote_lease=delete_queued' }];
  ui.renderEvents();
  const rendered = get('eventRows').innerHTML;
  assert.match(rendered, /绑定代理/);
  assert.match(rendered, /已绑定/);
  assert.match(rendered, /来源代理：node-a/);
  assert.match(rendered, /租约账户：lease-1/);
  assert.match(rendered, /远端租约：已排队删除/);
  assert.doesNotMatch(rendered, /其他上游错误/);
});

test('Resident queue detail is paged and proxy sync actions use the task center', async () => {
  const { dashboard: ui, get } = harness();
  assert.doesNotMatch(html, /function openProxyTasks/);
  assert.doesNotMatch(html, /代理任务与同步结果/);
  assert.match(html, /async function openProxyChangeDetails[\s\S]*openTaskCenter\(\)/);
  ui.state.taskCenterSelectedQueue = { id: 'resin_cleanup', label: 'Resin 租约清理', status: 'failed', total: 45, completed: 0, running: 0, waiting: 30, failed: 15, succeeded: 0, skipped: 0 };
  ui.state.taskCenterQueueDetail = {
    loading: false,
    mode: 'list',
    data: { items: [{ id: 'cleanup-1', auth_id: 'auth-1', status: 'retry_wait', last_error: 'upstream timeout' }], total: 45, page: 2, page_size: 20 }
  };
  await ui.renderTaskQueueDetail();
  const body = get('residentQueueDetailBody').innerHTML;
  assert.match(body, /第 2 \/ 3 页 · 共：45 项/);
  assert.match(body, /data-task-queue-page="-1"/);
  assert.match(body, /auth-1/);
});
