import assert from 'node:assert/strict';
import { Element, deferred, harness, html, localeResources, plain, test } from '../support.mjs';

test('Account rows show binding status and truncate full cooldown labels', () => {
  const { dashboard: ui } = harness();
  assert.match(html, /<th title="ui.proxy_binding_state_and_rotation_generation">ui.binding_status<\/th>/);
  assert.match(html, /<th title="ui.cooldown_end_time">ui.cooldown<\/th>/);
  assert.match(html, /\.accounts-table td\.select-cell,\s*\.accounts-table td\.status-cell,\s*\.accounts-table td\.proxy-cell,\s*\.accounts-table td\.time-cell,\s*\.accounts-table td\.actions-cell\s*\{\s*vertical-align:\s*middle;/s);
  assert.doesNotMatch(html, /\.time-cell \.cell-primary,\s*\.time-cell \.subline/);
  const cooling = ui.accountRow({
    auth_id: 'auth-cooling', display: 'cooling@example.com', state: 'cooling',
    cooldown_until: '2026-11-18T10:00:00Z', probe_backoff_until: '2026-11-18T11:00:00Z',
    proxy_status: 'bound', proxy_generation: 2, proxy_source_kind: 'template', proxy_source_name: 'Hidden source'
  });
  assert.match(cooling, /<td class="time-cell"><div class="cell-primary truncate-line" title="冷却至：/);
  assert.match(cooling, /title="冷却至：2026\/11\/18[^"]*"/);
  assert.doesNotMatch(cooling, /可立即探测|探测退避至|Hidden source|模板/);
  const ready = ui.accountRow({ auth_id: 'auth-ready', display: 'ready@example.com', state: 'healthy', cooldown_until: '', probe_backoff_until: '2026-11-18T11:00:00Z' });
  assert.match(ready, /<td class="time-cell"><div class="cell-primary truncate-line" title="无冷却">无冷却<\/div><\/td>/);
  assert.doesNotMatch(ready, /可立即探测|探测退避至/);
});test('A stale condition preview cannot overwrite newer inputs or validation state', async () => {
  const { dashboard: ui, get, mock } = harness();
  const response = deferred();
  const editor = { kind: 'template', revision: 1, assignments: { vendor: 'A' } };
  ui.state.proxyEditing = editor;
  mock('collectProxyTemplateEditor', () => ({ name: 'draft' }));
  mock('api', () => response.promise);
  get('proxyTemplatePreviewResults').innerHTML = 'new condition state';
  const preview = ui.previewProxyTemplate(editor, null, {}, new Element());
  editor.assignments.vendor = 'B';
  ui.state.proxyPreviewSequence += 1;
  response.resolve({ valid: true, samples: [{ masked_url: 'socks5h://proxy.example:1080', values: { vendor: 'A' } }] });
  await preview;
  assert.equal(get('proxyTemplatePreviewResults').innerHTML, 'new condition state');
  mock('api', async () => ({ valid: true, samples: [{ display_url: 'socks5h://preview-b:******@proxy.example:1080', masked_url: 'socks5h://proxy.example:1080', values: { vendor: 'B', profile: '<unsafe>' } }] }));
  await ui.previewProxyTemplate(editor, null, {}, new Element());
  assert.match(get('proxyTemplatePreviewResults').innerHTML, /socks5h:\/\/preview-b:\*\*\*\*\*\*@proxy\.example:1080/);
  assert.match(get('proxyTemplatePreviewResults').innerHTML, /<b>vendor:<\/b>B<\/span>/);
  assert.match(get('proxyTemplatePreviewResults').innerHTML, /<b>profile:<\/b>&lt;unsafe&gt;<\/span>/);
  assert.match(get('proxyTemplatePreviewResults').innerHTML, /&lt;unsafe&gt;/);
});

test('Proxy URL is the sole password source for node and template editors', () => {
  const { dashboard: ui, get, placeholder } = harness();
  get('ptpl_name').value = 'region proxy';
  get('ptpl_url').value = 'socks5h://proxy-{country}-sid-{session}:test-pass@proxy.example:3010';
  get('ptpl_enabled').checked = true;
  get('ptpl_update_policy').value = 'keep_bound';
  ui.state.proxyEditing = { groups: new Map() };
  placeholder('country', { separator: ',', input: 'US,JP,SG' });
  placeholder('session', { kind: 'random' });

  const source = ui.collectProxyTemplateEditor(null, {});
  assert.equal(Object.hasOwn(source, 'password_update'), false);
  assert.match(html, /value\.edit_url \|\| value\.url/);
  assert.match(html, /template\.edit_url \|\| ''/);
  assert.doesNotMatch(html, /proxyPasswordFields|readProxyPasswordUpdate|_password_action/);
});

test('Partial sync results distinguish committed failures and skipped accounts', async () => {
  const { dashboard: ui, mock } = harness();
  const result = { saved: true, counts: { total: 3, succeeded: 1, failed: 1, pending: 0 }, items: [{ auth_id: 'a', status: 'succeeded' }, { auth_id: 'b', status: 'needs_reconcile', committed: true, needs_reconcile: true }, { auth_id: 'c', status: 'skipped' }] };
  const summary = ui.proxyResultMessage(result, 'Node');
  assert.equal(summary.failed, 1);
  assert.match(summary.message, /配置已保存.*成功：1.*跳过：1.*失败：1/);
  mock('api', async () => ({ task_id: 'partial', status: 'succeeded', result }));
  assert.equal((await ui.waitForTask({ task_id: 'partial', status: 'queued' }, 'Sync')).saved, true);
  assert.equal(ui.state.proxyTasks.get('partial').result.counts.failed, 1);
});

test('Proxy event filter includes proxy and provider-specific actions', () => {
  const { dashboard: ui } = harness();
  ui.state.eventType = 'proxy';
  ui.state.eventQuery = '';
  assert.equal(ui.eventMatches({ action: 'proxy_bind' }), true);
  assert.equal(ui.eventMatches({ action: 'resin_lease_delete' }), true);
  assert.equal(ui.eventMatches({ action: 'probe' }), false);
});
test('Closing a dirty editor preserves input until discard is confirmed', async () => {
  const { dashboard: ui, get, mock } = harness();
  const editor = { kind: 'template', dirty: true, previousFocus: new Element() };
  ui.state.proxyEditing = editor; get('proxySourceDrawer').hidden = false;
  get('proxySourceEditor').innerHTML = 'unsaved draft';
  mock('confirmAction', async () => false);
  assert.equal(await ui.closeProxyDrawer(), false);
  assert.equal(ui.state.proxyEditing, editor);
  assert.equal(get('proxySourceEditor').innerHTML, 'unsaved draft');
  mock('confirmAction', async () => true);
  assert.equal(await ui.closeProxyDrawer(), true);
  assert.equal(get('proxySourceEditor').innerHTML, '');
  assert.equal(editor.previousFocus.focused, true);
});

test('Clear options pass null, rebind, and pause as distinct overrides', async () => {
  const { dashboard: ui, get, mock } = harness();
  mock('confirmAction', async () => true);
  for (const [choice, expected] of [['global', null], ['rebind', false], ['pause', true]]) {
    get('proxyClearPolicy').value = choice;
    assert.equal((await ui.confirmProxyBindingOptions('clear', 2, 'Clear')).exclude_auto_bind, expected);
  }
});

test('Selected proxy clear is direct and explicitly allows external configurations', async () => {
  const { dashboard: ui, mock } = harness();
  ui.state.leaseSelection.add('auth-external');
  const requests = [];
  let confirmations = 0;
  let optionPrompts = 0;
  mock('confirmAction', async () => { confirmations++; return true; });
  mock('confirmProxyBindingOptions', async () => { optionPrompts++; return { exclude_auto_bind: null }; });
  mock('api', async (action, params) => {
    requests.push({ action, params });
    return {
      count: params.auth_ids.length,
      counts: { total: params.auth_ids.length, succeeded: params.auth_ids.length, failed: 0 },
      items: params.auth_ids.map((authID) => ({ auth_id: authID, status: 'cleared' }))
    };
  });
  mock('reportProxyResult', () => {});
  mock('refreshAll', async () => {});
  await ui.runSelectedProxyAction('clear', new Element());
  assert.equal(requests.length, 1);
  assert.equal(requests[0].action, 'proxy.bindings.clear');
  assert.equal(requests[0].params.allow_external, true);
  assert.deepEqual(plain(requests[0].params.auth_ids), ['auth-external']);
  assert.equal(confirmations, 0);
  assert.equal(optionPrompts, 0);
});

test('Auto-bind scan runtime is shown in the status bar instead of settings', () => {
  const { dashboard: ui, get } = harness();
  ui.state.proxy = {
    auto_bind: { enabled: false },
    auto_bind_runtime: {
      last_scan_at: '2026-11-17T05:17:13Z', bound_total: 2, failed_total: 1,
      skipped_total: 3, excluded_count: 4, last_error: 'catalog unavailable'
    }
  };
  ui.renderProxyAutoBind();
  assert.match(get('proxyAutoBindText').textContent, /最近扫描：2026\/11\/17/);
  assert.match(get('proxyAutoBindText').title, /累计成功：2 · 失败：1 · 跳过：3 · 排除：4 个账号/);
  assert.match(get('proxyAutoBindText').title, /最近错误：账号目录暂时不可用/);
  assert.doesNotMatch(get('proxyAutoBindText').title, /catalog unavailable/);
  assert.doesNotMatch(html, /id="proxyAutoBindDetails"/);
});

test('Recoverable auto-bind source errors use Chinese copy and disappear when cleared', () => {
  const { dashboard: ui, get } = harness();
  const expected = '自动绑定使用的代理已变化，将重新选择代理';
  assert.equal(ui.friendlyApiMessage('automatic source availability changed', 'auto_bind_source_unavailable'), expected);
  assert.equal(ui.friendlyApiMessage('', 'auto_bind_source_unavailable'), expected);
  assert.equal(ui.friendlyApiMessage('auto_bind_source_unavailable', ''), expected);

  ui.state.proxy = {
    auto_bind: { enabled: true },
    auto_bind_runtime: { last_error: 'automatic source availability changed' }
  };
  ui.renderProxyAutoBind();
  assert.match(get('proxyAutoBindText').title, new RegExp(expected));
  assert.doesNotMatch(get('proxyAutoBindText').title, /automatic source availability changed|auto_bind_source_unavailable/);

  ui.state.proxy.auto_bind_runtime.last_error = '';
  ui.renderProxyAutoBind();
  assert.doesNotMatch(get('proxyAutoBindText').title, /最近错误/);
});

test('Resident queue cards localize technical source errors', () => {
  const { dashboard: ui, get } = harness();
  ui.renderResidentQueues([{
    id: 'auto_bind', label: '自动绑定队列', status: 'idle', total: 0, completed: 0,
    running: 0, waiting: 0, succeeded: 0, failed: 0,
    last_error: 'automatic source availability changed'
  }], {});
  const rendered = get('residentQueues').innerHTML;
  assert.match(rendered, /class="progress-track"/);
  assert.match(rendered, /style="width:0%"/);
  assert.match(rendered, /自动绑定使用的代理已变化，将重新选择代理/);
  assert.doesNotMatch(rendered, /automatic source availability changed|auto_bind_source_unavailable/);
});
test('Auto-bind scheduler waiting reasons are localized', () => {
  const { dashboard: ui, get } = harness();
  const cases = [
    ['waiting_sources', '等待可用代理'],
    ['waiting_health', '等待健康代理'],
    ['manual_task', '等待手动任务完成'],
    ['catalog_unknown', '等待账号目录同步']
  ];
  for (const [reason, expected] of cases) {
    ui.state.proxy = { auto_bind: { enabled: true }, auto_bind_runtime: { waiting_reason: reason } };
    ui.renderProxyAutoBind();
    const text = get('proxyAutoBindText').textContent;
    assert.ok(text.includes(expected), reason + ' should render Chinese copy');
    assert.ok(!text.includes(reason), reason + ' should not be exposed in the status bar');
  }
  ui.state.proxy.auto_bind_runtime.waiting_reason = 'future_reason';
  ui.renderProxyAutoBind();
  assert.match(get('proxyAutoBindText').textContent, /等待自动绑定继续/);
  assert.doesNotMatch(get('proxyAutoBindText').textContent, /future_reason/);
});

test('Saving auto-bind settings collapses the panel and returns focus', async () => {
  const { dashboard: ui, get, mock } = harness();
  const panel = get('proxyAutoBindSettings');
  const toggle = get('proxyAutoBindToggleSettingsBtn');
  panel.hidden = false;
  toggle.textContent = '收起设置';
  ui.state.proxy = { auto_bind: { enabled: false }, auto_bind_runtime: {} };
  mock('api', async (action, params) => {
    assert.equal(action, 'proxy.auto_bind.update');
    assert.equal(params.auto_rotate_after_quality_failure, false);
    return { config: { enabled: false }, runtime: {}, auto_rotate_after_quality_failure: false };
  });
  mock('renderProxyAutoBind', () => {});
  mock('toast', () => {});

  await ui.saveProxyAutoBind(get('proxyAutoBindSaveBtn'));

  assert.equal(panel.hidden, true);
  assert.equal(toggle.textContent, '设置');
  assert.equal(toggle.getAttribute('aria-expanded'), 'false');
  assert.equal(toggle.focused, true);
});

test('Binding source candidates use a bounded labeled picker', async () => {
  const { dashboard: ui, get, mock } = harness();
  ui.state.proxySources = [
    { id: 'node-1', name: 'Node one', kind: 'node' },
    { id: 'template-1', name: 'Template one', kind: 'template' },
    { id: 'resin-1', name: 'Resin one', kind: 'resin' }
  ];
  ui.state.proxyOperationSourceChoices = [];
  ui.renderProxyOperationSourceChoices('templates');
  let candidateHTML = get('proxyOperationSourceChoices').innerHTML;
  assert.match(candidateHTML, /data-proxy-source-option="template-1"/);
  assert.doesNotMatch(candidateHTML, /data-proxy-source-option="node-1"|data-proxy-source-option="resin-1"/);
  assert.match(candidateHTML, />模板 · Template one</);

  const requests = [];
  mock('api', async (action, params) => {
    requests.push({ action, params });
    return { items: [
      { id: 'template-2', name: 'Template two', kind: 'template' },
      { id: 'node-2', name: 'Node two', kind: 'node' }
    ] };
  });
  await ui.searchProxyOperationSourceChoices('tpl', 'templates');
  candidateHTML = get('proxyOperationSourceChoices').innerHTML;
  assert.equal(requests[0].action, 'proxy.sources.list');
  assert.equal(requests[0].params.kind, 'template');
  assert.match(candidateHTML, /data-proxy-source-option="template-2"/);
  assert.doesNotMatch(candidateHTML, /data-proxy-source-option="node-2"/);

  await ui.searchProxyOperationSourceChoices('', 'all');
  candidateHTML = get('proxyOperationSourceChoices').innerHTML;
  assert.equal(Object.hasOwn(requests[1].params, 'kind'), false);
  assert.match(candidateHTML, /data-proxy-source-option="template-2"/);
  assert.match(candidateHTML, /data-proxy-source-option="node-2"/);

  assert.equal(ui.chooseProxyOperationSource('template-2'), true);
  assert.equal(get('proxyOperationSource').value, '模板 · Template two');
  assert.equal(get('proxyOperationSourceID').value, 'template-2');
  assert.equal(ui.proxyOperationSourceValue(), 'template-2');

  assert.match(html, /\.confirm-card\.wide\s*\{/);
  assert.match(html, /role="combobox"/);
  assert.match(html, /\.proxy-source-picker-menu\s*\{[\s\S]*?max-height:\s*240px/);
  assert.doesNotMatch(html, /<datalist id="proxyOperationSourceChoices"/);
});

test('Lease proxy filter shows source kind and name without source IDs', async () => {
  const { dashboard: ui, get } = harness();
  ui.state.proxySources = [
    { id: 'resin-1', name: 'CPA', kind: 'resin' },
    { id: 'node-1', name: 'Node one', kind: 'node' }
  ];
  ui.state.proxySourceChoices = [];
  ui.renderProxySourceChoices();
  const choices = get('proxySourceChoices').innerHTML;
  assert.match(choices, /<option value="Resin · CPA"><\/option>/);
  assert.match(choices, /<option value="节点 · Node one"><\/option>/);
  assert.doesNotMatch(choices, /resin-1|node-1/);

  const input = get('leaseSourceFilter');
  input.value = 'Resin · CPA';
  await input.emit('input');
  assert.equal(ui.state.leaseSourceFilter, 'resin-1');
});

test('Searchable proxy source filter does not reserve an invisible picker', () => {
  assert.match(html, /#leaseSourceFilter\s*\{[\s\S]*?padding-right:\s*9px/);
  assert.match(html, /#leaseSourceFilter::-webkit-calendar-picker-indicator/);
  assert.match(html, /#leaseSourceFilter::-webkit-search-cancel-button/);
});

test('Bulk proxy import filters invalid and duplicate lines and stays bounded', () => {
  const { dashboard: ui } = harness();
  const text = [
    '# proxy list',
    '',
    'http://user:pass@proxy.example:8080',
    'SOCKS5H://user:pass@[2001:db8::1]:1080',
    'http://user:pass@proxy.example:8080',
    'proxy.example:8080',
    'ftp://proxy.example:21',
    'http://'
  ].join('\r\n');
  assert.match(html, /\.field \.proxy-import-text\s*\{\s*height: 400px;\s*min-height: 400px;/);
  assert.match(localeResources['zh-CN']['ui.import_hint'], /上限：100000 条 · 16 MiB/);
  const parsed = ui.parseProxyImportText(text);
  assert.equal(parsed.lineCount, 7);
  assert.equal(parsed.validCount, 2);
  assert.equal(parsed.invalid, 3);
  assert.equal(parsed.duplicate, 1);
  assert.equal(parsed.comments, 1);
  assert.deepEqual(plain(parsed.valid), ['http://user:pass@proxy.example:8080', 'SOCKS5H://user:pass@[2001:db8::1]:1080']);
  assert.equal(parsed.invalidSamples.length, 3);
  assert.match(localeResources['zh-CN']['ui.import_hint'], /标准 URL 优先，失败后自动尝试地址在前或 host:port@user:pass/);
  assert.match(localeResources['zh-CN']['ui.import_standard_url_placeholder'], /socks5h:\/\/proxy\.example:1080:user:pass/);
  assert.match(localeResources['zh-CN']['ui.import_standard_url_placeholder'], /http:\/\/proxy\.example:8080@user:pass/);
  const many = Array.from({ length: 100_000 }, (_, index) => 'http://user:pass@proxy-' + index + '.example:' + (8000 + index % 1000)).join('\n');
  const batch = ui.parseProxyImportText(many);
  assert.equal(batch.validCount, 100_000);
  assert.equal(batch.overLimit, false);
  assert.equal(batch.overBytes, false);
  assert.equal(ui.parseProxyImportText(many + '\nhttp://user:pass@overflow.example:8080').overLimit, true);
});

test('Invalid CSV import keeps every existing relation and the import draft', async () => {
  const { dashboard: ui, groupCard } = harness();
  const rows = [['A', 'basic'], ['B', 'pro']];
  const group = ui.proxyGroupDraft({ id: 'plans', columns: ['vendor', 'plan'], rows }, 0);
  group.importText = 'vendor,plan\nC,"unfinished';
  ui.state.proxyEditing = { kind: 'template', groups: new Map([[0, group]]), assignments: {} };
  groupCard(group);
  await ui.importProxyGroupRows(group);
  assert.deepEqual(plain(group.rows), rows);
  assert.equal(group.importText, 'vendor,plan\nC,"unfinished');
  assert.match(group.error, /导入未应用/);
});

test('Proxy sync details request their next page instead of expanding a million items', async () => {
  const { dashboard: ui, get, mock } = harness();
  const requests = [];
  mock('api', async (action, params) => {
    requests.push({ action, params });
    return { change_id: 'change', page: params.page, page_size: 20, total: 1_000_000, counts: { total: 1_000_000, pending: 1_000_000 }, items: Array.from({ length: 20 }, (_, index) => ({ auth_id: 'auth-' + (20 + index), status: 'pending' })) };
  });
  ui.state.taskCenterSelectedQueue = { id: 'proxy_changes', label: '代理同步队列', status: 'running', total: 1_000_000, completed: 0, running: 0, waiting: 1_000_000, succeeded: 0, failed: 0, skipped: 0 };
  await ui.openTaskCenterChange('change', 2);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].action, 'proxy.changes.get');
  assert.equal(requests[0].params.page, 2);
  assert.equal(requests[0].params.page_size, 20);
  assert.equal((get('residentQueueDetailBody').innerHTML.match(/<td class="mono">auth-/g) || []).length, 20);
  assert.match(get('residentQueueDetailBody').innerHTML, /第 2 \/ 50000 页/);
});

test('Cleanup queue pages retain bounded twenty-item responses', async () => {
  const { dashboard: ui, get, mock } = harness();
  const requests = [];
  mock('api', async (action, params) => {
    requests.push({ action, params });
    return { total: 1_000_000, page: params.page, page_size: 20, items: Array.from({ length: 20 }, (_, index) => ({ id: 'cleanup-' + index, auth_id: 'auth-' + index, account: 'preview-' + index, status: 'pending', attempts: 0 })) };
  });
  const queue = { id: 'resin_cleanup', label: 'Resin 租约清理', status: 'running', total: 1_000_000, completed: 0, running: 0, waiting: 1_000_000, succeeded: 0, failed: 0, skipped: 0 };
  ui.state.taskCenterSelectedQueue = queue;
  await ui.loadTaskQueueDetail(queue, 2);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].action, 'resin.cleanup.list');
  assert.equal(requests[0].params.page, 2);
  assert.equal(requests[0].params.page_size, 20);
  assert.equal((get('residentQueueDetailBody').innerHTML.match(/data-task-cleanup-retry=/g) || []).length, 20);
  assert.match(get('residentQueueDetailBody').innerHTML, /第 2 \/ 50000 页/);
});
test('ID-only full selection stops after one complete response', async () => {
  const { dashboard: ui, mock } = harness();
  let requests = 0;
  mock('api', async () => { requests += 1; return { total: 10001, items: Array.from({ length: 10001 }, (_, index) => 'auth-' + index) }; });
  assert.equal((await ui.loadAllAccounts('leases', true)).size, 10001);
  assert.equal(requests, 1);
});

test('Selected operations use bounded batches and retain only failed selections', async () => {
  const { dashboard: ui, mock } = harness();
  const ids = Array.from({ length: 10001 }, (_, index) => 'auth-' + index);
  ids.forEach((id) => ui.state.leaseSelection.add(id));
  const requests = [];
  mock('api', async (action, params) => {
    requests.push(params.auth_ids.length);
    return { count: params.auth_ids.length, items: params.auth_ids.map((id) => ({ auth_id: id, status: id === 'auth-5' ? 'failed' : 'succeeded', error: id === 'auth-5' ? 'failed locally' : '' })) };
  });
  const result = await ui.runProxyAccountBatches('proxy.bindings.bind', ids, { source_ids: ['node'] }, 'Bind');
  assert.deepEqual(requests, [10000, 1]);
  assert.equal(result.succeeded, 10000);
  assert.equal(result.failed, 1);
  assert.deepEqual([...ui.state.leaseSelection], ['auth-5']);
});

test('Saving cannot silently discard pending bulk or CSV relation input', async () => {
  const { dashboard: ui, get, groupCard } = harness();
  const group = ui.proxyGroupDraft({ id: 'plans', columns: ['vendor', 'plan'], rows: [['A', 'basic']] }, 0);
  group.path = ['A', ''];
  ui.state.proxyEditing = { kind: 'template', revision: 0, groups: new Map([[0, group]]), assignments: {} };
  const card = groupCard(group);
  const field = card.querySelector('[data-group-bulk-values]');
  field.dataset.groupBulkValues = '';
  field.ancestors = new Map([['[data-group-card]', card]]);
  field.value = 'basic|pro';
  await get('proxySourceEditor').emit('input', { target: field });
  assert.throws(() => ui.collectProxyTemplateGroups(), /未应用的批量值/);
  assert.deepEqual(plain(group.rows), [['A', 'basic']]);
  field.value = '';
  await get('proxySourceEditor').emit('input', { target: field });
  group.importText = 'vendor,plan\nC,pro'; group.importRevision += 1;
  assert.throws(() => ui.collectProxyTemplateGroups(), /未应用的 CSV\/TSV/);
  await ui.importProxyGroupRows(group);
  assert.deepEqual(plain(ui.collectProxyTemplateGroups()[0].rows), [['A', 'basic'], ['C', 'pro']]);
});

test('Binding status metrics use canonical names only', () => {
  const { dashboard: ui } = harness();
  assert.equal(ui.normalizeBindingStatus('bound'), 'bound');
  assert.equal(ui.normalizeBindingStatus('checking'), 'checking');
  assert.equal(ui.normalizeBindingStatus('read_failed'), 'read_failed');
  assert.equal(ui.normalizeBindingStatus('reconcile_required'), 'reconcile_required');
  assert.equal(ui.normalizeBindingStatus('unknown'), 'unknown');
  assert.equal(ui.normalizeBindingStatus(''), 'checking');
  const counts = ui.proxySummaryCounts({ binding_counts: { bound: 12, checking: 5, read_failed: 2, reconcile_required: 3, external: 2, unknown: 4 } });
  assert.equal(counts.checking, 5);
  assert.equal(counts.read_failed, 2);
  assert.equal(counts.bound, 12);
  assert.equal(counts.reconcile_required, 3);
  assert.equal(counts.external, 2);
  assert.equal(counts.unknown, 4);
});

test('Binding summary separates account coverage from proxy availability', () => {
  const { dashboard: ui, get } = harness();
  ui.state.proxy = {
    binding_counts: { bound: 3092, unbound: 0, external: 0, drifted: 0, unknown: 0, reconcile_required: 0 },
    binding_total: 3092,
    source_count: 2,
    available_source_count: 2,
    auto_bind: {},
    auto_bind_runtime: {}
  };
  ui.state.proxySources = [];
  ui.state.leases = [];
  ui.state.leaseList = { total: 3092, page: 1, page_size: 50 };
  ui.renderResin();
  const rendered = get('resinMetrics').innerHTML;
  assert.match(rendered, /绑定覆盖率：100\.0%/);
  assert.match(rendered, /可用：2 \/ 2/);
  assert.doesNotMatch(rendered, /可绑定代理：2/);

  ui.state.proxy.binding_counts = { bound: 3078, unbound: 14, external: 0, drifted: 0, unknown: 0, reconcile_required: 0 };
  ui.renderResin();
  const partial = get('resinMetrics').innerHTML;
  assert.match(partial, /绑定覆盖率：99\.5%/);
  assert.doesNotMatch(partial, /绑定覆盖率：100(?:\.0)?%/);
});

test('Binding review metric and filter share one aggregate status', () => {
  const { dashboard: ui, get } = harness();
  const counts = { bound: 3078, unbound: 14, external: 2, drifted: 3, read_failed: 4, unknown: 5, checking: 0, reconcile_required: 0 };
  assert.equal(ui.proxyNeedsReviewCount(counts), 14);
  ui.state.proxy = { binding_counts: counts, binding_total: 3092, source_count: 1, available_source_count: 1, auto_bind: {}, auto_bind_runtime: {} };
  ui.state.proxySources = [];
  ui.state.leases = [];
  ui.state.leaseList = { total: 3092, page: 1, page_size: 50 };
  ui.renderResin();
  assert.match(html, /<option value="needs_review">ui.needs_reconciliation<\/option>/);
  assert.match(get('resinMetrics').innerHTML, /待核对/);
});

test('A flat source detail response preserves existing template relations', async () => {
  const { dashboard: ui, get, mock } = harness();
  let editFields;
  mock('api', async (_action, params) => { editFields = params.edit_fields; return { id: 'flat', kind: 'template', name: 'Current template', config_version: 5, enabled: true, username_pattern: 'user-{vendor}-{plan}', placeholders: [{ name: 'vendor', kind: 'enum', group_id: 'plans', enum: { separator: '|' } }, { name: 'plan', kind: 'enum', group_id: 'plans', enum: { separator: '|' } }], value_groups: [{ id: 'plans', columns: ['vendor', 'plan'], rows: [['A', 'basic']] }] }; });
  await ui.openProxyTemplateEditor({ id: 'flat', kind: 'template', config_version: 1 });
  assert.equal(ui.state.proxyEditing.source.config_version, 5);
  assert.deepEqual(plain(ui.state.proxyEditing.groups.get(0).rows), [['A', 'basic']]);
  assert.match(get('proxySourceEditor').innerHTML, /Current template/);
  assert.equal(editFields, true);
});

test('Resin source editor uses the shared template lifecycle and reserved account placeholder', async () => {
  const { dashboard: ui, get, placeholder } = harness();
  await ui.openProxyResinEditor(null);
  const editorHTML = get('proxySourceEditor').innerHTML;
  assert.match(editorHTML, /账号格式/);
  assert.match(editorHTML, /\{account\}/);
  assert.match(editorHTML, /data-placeholder-card="account"[^>]*>.*?保留变量/s);
  assert.doesNotMatch(editorHTML, /data-placeholder-kind="account"|data-enum-input="account"/);
  assert.match(editorHTML, /grok-\{account\}-\{suffix\}/);
  assert.match(editorHTML, /仅本地轮换/);
  get('presin_name').value = 'Primary Resin';
  get('presin_account_format').value = 'xai-{account}-{region}';
  placeholder('region', { input: 'us,jp', separator: ',' });
  const input = ui.collectProxyResinEditor(null, {});
  assert.equal(input.name, 'Primary Resin');
  assert.equal(input.account_format, 'xai-{account}-{region}');
  assert.equal(input.placeholders.length, 1);
  assert.equal(input.placeholders[0].name, 'region');
  assert.equal(input.placeholders.some((item) => item.name === 'account'), false);
});

test('Lease rows use binding source names before the source list loads', () => {
  const { dashboard: ui } = harness();
  ui.state.proxySources = [];
  const row = ui.leaseRow({
    auth_id: 'auth-1', auth_id_display: 'auth-1', display: 'a***@example.com', display_full: '[EMAIL]',
    proxy: 'socks5h://us***r:******@resin:2260', proxy_status: 'bound',
    proxy_source_id: 'resin-5a1f576f27fb4f601a90ee44', proxy_source_kind: 'resin', proxy_source_name: 'CPA'
  });
  assert.match(row, /Resin · CPA/);
  assert.doesNotMatch(row, /resin-5a1f576f27fb4f601a90ee44/);
});

test('Lease endpoints stay masked until the eye reveals the full endpoint', async () => {
  const { dashboard: ui, mock } = harness();
  ui.state.proxy = {};
  ui.state.proxySources = [];
  ui.state.resin = {};
  const account = { auth_id: 'auth-1', auth_id_display: 'auth-1', display: 'a***@example.com', display_full: 'alice@example.com', proxy: 'socks5h://us***r:******@resin:2260', proxy_status: 'bound', proxy_managed: true };
  let row = ui.leaseRow(account);
  assert.ok(row.includes('title="' + account.display_full + '"'));
  assert.ok(row.includes('>' + account.display + '</div>'));
  assert.ok(row.includes(account.proxy));
  assert.ok(row.includes('title="' + account.proxy + '"'));
  assert.match(row, /data-lease-endpoint="auth-1"/);
  assert.match(row, /title="显示完整端点"/);
  assert.doesNotMatch(row, /user:pass/);
  ui.state.leaseProxyDetails.set('auth-1', 'socks5h://user:pass@resin:2260');
  row = ui.leaseRow(account);
  assert.match(row, /socks5h:\/\/user:pass@resin:2260/);
  assert.match(row, /title="socks5h:\/\/user:pass@resin:2260"/);
  assert.match(row, /隐藏完整端点/);

  ui.state.leaseProxyDetails.clear();
  mock('renderResin', () => {});
  mock('api', async (action) => {
    assert.equal(action, 'proxy.bindings.endpoint');
    return { proxy_full: 'socks5h://user:pass@resin:2260' };
  });
  await ui.toggleLeaseEndpoint('auth-1', new Element());
  assert.equal(ui.state.leaseProxyDetails.get('auth-1'), 'socks5h://user:pass@resin:2260');
});

test('Source endpoints keep the proxy URL shape and reveal full credentials on demand', async () => {
  const { dashboard: ui, mock } = harness();
  const masked = 'socks5h://us***r:******@proxy.example:8080';
  const full = 'socks5h://user:secret@proxy.example:8080';
  const source = { id: 'node-1', kind: 'node', name: 'Primary', endpoint: masked, password_set: true };
  let row = ui.proxySourceRow(source);
  assert.match(row, /data-source-endpoint="node-1"/);
  assert.ok(row.includes(masked));
  assert.ok(row.includes('title="' + masked + '"'));
  assert.doesNotMatch(row, /user:secret/);

  ui.state.sourceProxyDetails.set('node-1', full);
  row = ui.proxySourceRow(source);
  assert.ok(row.includes(full));
  assert.ok(row.includes('title="' + full + '"'));
  assert.match(row, /隐藏完整端点/);

  ui.state.sourceProxyDetails.clear();
  mock('renderProxySources', () => {});
  mock('api', async (action) => {
    assert.equal(action, 'proxy.sources.endpoint');
    return { proxy_full: full };
  });
  await ui.toggleSourceEndpoint('node-1', new Element());
  assert.equal(ui.state.sourceProxyDetails.get('node-1'), full);
});

test('Proxy source connectivity and check time use separate compact columns', () => {
  const { dashboard: ui } = harness();
  const row = ui.proxySourceRow({
    id: 'node-1', kind: 'node', name: 'Node one', enabled: true, valid: true,
    health: 'unhealthy', health_checked: '2026-11-17T10:00:00Z', binding_count: 0
  });
  assert.match(row, /已启用/);
  assert.match(row, /不可达/);
  assert.match(row, /2026\/11\/17/);
  assert.match(row, /<div class="account-name">Node one<\/div><\/td>/);
  assert.doesNotMatch(row, /绑定上限：不限/);
  assert.doesNotMatch(row, /· 上限：/);
  assert.doesNotMatch(row, /检测：|尚未检测|检测时生成临时端点/);
  assert.doesNotMatch(row, />可用</);
  assert.match(row, /仅表示最近一次通过网络代理建立测试目标隧道的检测结果/);
  assert.match(html, /ui.network_connectivity<\/th>\s*<th title="ui.time_of_the_latest_network_connectivity_check">ui.checked_at<\/th>/);

  const templateRow = ui.proxySourceRow({
    id: 'template-1', kind: 'template', name: 'Region template', enabled: true, valid: true,
    health: 'healthy', health_checked: '2026-11-17T10:00:00Z', binding_count: 0
  });
  assert.match(templateRow, /可连通/);
  assert.match(templateRow, /已启用/);
  assert.doesNotMatch(templateRow, /样本可连通/);
  assert.doesNotMatch(templateRow, /个变量|个关联组|保留旧绑定/);

  const unknown = ui.proxySourceConnectivity({ id: 'template-2', kind: 'template', enabled: true, valid: true, health_checked: '0001-01-01T00:00:00Z' });
  assert.match(unknown, /未检测/);
  assert.doesNotMatch(unknown, /检测时生成临时端点|检测：-|不适用|无法检测|样本/);

  const localResin = ui.proxySourceConnectivity({ id: 'resin', kind: 'resin', enabled: true, valid: true, local_only: true });
  assert.match(localResin, /未检测/);
  assert.doesNotMatch(localResin, /本地轮换无独立端点|不适用|无法检测/);
});

test('Proxy table fits the page with wider name and endpoint columns', () => {
  assert.match(html, /\.proxy-source-table\s*\{[^}]*min-width:\s*980px;/s);
  assert.match(html, /\.proxy-source-table th:nth-child\(2\)\s*\{\s*width:\s*190px;/s);
  assert.match(html, /\.proxy-source-table th:nth-child\(4\)\s*\{\s*width:\s*auto;/s);
  assert.match(html, /\.proxy-source-table \.actions-cell\s*\{\s*width:\s*220px;/s);
  assert.match(html, /\.proxy-source-table \.actions\s*\{[^}]*flex-wrap:\s*wrap;/s);
  assert.match(html, /\.proxy-source-table th,\s*\.proxy-source-table td\s*\{[^}]*padding-left:\s*5px;[^}]*padding-right:\s*5px;/s);
});

test('Binding picker refreshes scoped proxy candidates before showing them', async () => {
  const { dashboard: ui, get, mock } = harness();
  ui.state.proxySources = [{ id: 'node-1', name: 'Node one', kind: 'node' }];
  ui.state.proxyOperationSourceChoices = [];
  get('proxyOperationSourceChoices').hidden = true;
  get('proxyBindScope').value = 'all';
  const requests = [];
  mock('api', async (action, params) => {
    requests.push({ action, params });
    return { items: [{ id: 'template-1', name: 'Template one', kind: 'template' }], total: 1 };
  });
  const pending = deferred();
  mock('confirmAction', () => pending.promise);
  const optionsPromise = ui.confirmProxyBindingOptions('bind', 1, 'Bind');
  get('proxyBindScope').value = 'templates';
  await get('proxyBindScope').emit('change');
  await get('proxyOperationSource').emit('focus');
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(requests.length, 1);
  assert.equal(requests[0].action, 'proxy.sources.list');
  assert.equal(requests[0].params.kind, 'template');
  assert.match(get('proxyOperationSourceChoices').innerHTML, /data-proxy-source-option="template-1"/);
  assert.equal(ui.chooseProxyOperationSource('template-1'), true);
  pending.resolve(true);
  const options = await optionsPromise;
  assert.deepEqual(plain(options.source_ids), ['template-1']);
});

test('Proxy source filters include enabled and connectivity dimensions', () => {
  const { dashboard: ui } = harness();
  ui.state.proxySourceEnabledFilter = 'disabled';
  ui.state.proxySourceHealthFilter = 'unhealthy';
  ui.state.proxySourceQuery = 'proxy';
  assert.equal(JSON.stringify(ui.listParams('sources')), JSON.stringify({
    page: 1,
    page_size: 50,
    kind: '',
    enabled: 'disabled',
    health: 'unhealthy',
    query: 'proxy'
  }));
});

test('Proxy source selection enables batch test and delete actions', () => {
  const { dashboard: ui, get } = harness();
  ui.state.proxySources = [
    { id: 'node-1', kind: 'node', name: 'Node one', enabled: true, valid: true },
    { id: 'template-1', kind: 'template', name: 'Template one', enabled: false, valid: true }
  ];
  ui.state.proxySourceList = { total: 2, page: 1, page_size: 50 };
  ui.state.proxySourceSelection.add('node-1');
  ui.renderProxySources();
  assert.equal(get('proxySourceSelectionCount').textContent, '已选：1 个代理');
  assert.equal(get('proxySourceTestSelectedBtn').disabled, false);
  assert.equal(get('proxySourceDeleteSelectedBtn').disabled, false);
  assert.match(get('proxySourceRows').innerHTML, /data-source-select="node-1"/);
  assert.match(get('proxySourceRows').innerHTML, /data-source-select="node-1" aria-label="选择代理" checked/);
});

test('Resin rows use the unified edit, delete, and single-line type presentation', () => {
  const { dashboard: ui, get } = harness();
  ui.state.proxySources = [];
  const row = ui.proxySourceRow({ id: 'resin-1', kind: 'resin', name: 'Resin A', enabled: true, valid: true, local_only: true, config_version: 2, revision: 3 });
  assert.match(row, /data-proxy-source-edit="resin-1" data-kind="resin"/);
  assert.match(row, /data-proxy-source-delete="resin-1" data-kind="resin"/);
  assert.match(row, /本地轮换/);
  assert.match(row, /本地轮换没有独立端点/);
  const apiRow = ui.proxySourceRow({ id: 'resin-2', kind: 'resin', name: 'Resin B', enabled: true, valid: true, local_only: false, config_version: 2, revision: 3 });
  assert.doesNotMatch(row, /<div class=.subline.>(?:API|本地)轮换<\/div>/);
  assert.doesNotMatch(apiRow, /<div class=.subline.>(?:API|本地)轮换<\/div>/);
  assert.doesNotMatch(row, /data-open-resin|配置 Resin/);
});

test('Proxy delete confirms stay concise and avoid trailing full stops', () => {
  assert.match(html, /i18n\.t\('ui\.delete_proxy_detail'\)/);
  assert.match(html, /i18n\.t\('ui\.delete_selected_proxies_detail', \{ count: ids\.length \}\)/);
  assert.equal(localeResources['zh-CN']['ui.delete_proxy_detail'], '将删除此代理并清理受管理代理，失败项保留并报告');
  assert.doesNotMatch(html, /成功项不会回滚。|成功项不会回滚'/);
});

test('Proxy management uses a compact workspace toolbar with underlined sub-navigation', () => {
  assert.match(html, /<div class="proxy-workspace-bar">/);
  assert.doesNotMatch(html, /代理工作区|统一管理账号绑定关系/);
  assert.doesNotMatch(html, /proxyTasksBtn|查看任务中心|renderProxyTaskBadge/);
  assert.match(html, /data-proxy-subtab="bindings">ui.account_bindings/);
  assert.match(html, /data-proxy-subtab="sources">ui.proxy_sources/);
  assert.match(html, /<div class="proxy-workspace-status" id="proxyAutoBindBar">/);
  assert.match(html, /\.proxy-workspace-status\s*\{[\s\S]*?border-left: 1px solid var\(--line\)/);
  assert.match(html, /\.proxy-subnav \.tab-button\.active::after\s*\{\s*background: var\(--text\);/);
  assert.match(html, /document\.querySelectorAll\('\.tabs, \.settings-tabs, \.proxy-subnav'\)/);
});

test('Proxy UI uses unified naming and avoids Chinese full stops', () => {
  assert.doesNotMatch(html, /代理来源|个来源|来源名称|全部来源|添加来源|指定来源|来源范围/);
  assert.doesNotMatch(html, /。/);
  assert.doesNotMatch(html, /…/);
  assert.doesNotMatch(html, /；/);
});

test('Proxy help copy is concise, consistent, and stays inside the drawer', () => {
  const { dashboard: ui } = harness();
  assert.equal(ui.cleanHelpCopy('说明；'), '说明');
  assert.equal(ui.cleanHelpCopy('说明，'), '说明');
  assert.equal(ui.cleanHelpCopy('说明？'), '说明');
  assert.equal(ui.cleanHelpCopy('说明：'), '说明');
  assert.equal(ui.cleanHelpCopy('说明！'), '说明');
  assert.equal(ui.cleanHelpCopy('说明.'), '说明');
  assert.equal(ui.cleanHelpCopy('说明…'), '说明');
  const row = ui.proxyNodeRowHTML(0, { name: '非常长的节点名称', edit_url: 'http://user:pass@long-proxy-host.example:8080/very/long/path' }, true);
  assert.match(row, /data-proxy-node-name/);
  assert.match(row, /data-proxy-node-url/);
  assert.match(row, /data-proxy-node-remove/);
  assert.match(row, /class="field proxy-node-name-field"/);
  assert.match(row, /class="field proxy-node-url-field"/);
  assert.match(row, /inputmode="url"/);
  assert.match(row, /title="非常长的节点名称"/);
  assert.match(row, /title="http:\/\/user:pass@long-proxy-host\.example:8080\/very\/long\/path"/);
  assert.doesNotMatch(row, /field full/);
  assert.match(html, /data-proxy-node-add/);
  assert.match(html, /i18n\.t\(editing \? 'ui\.proxy_node_edit_title' : 'ui\.proxy_node_add_title'\)/);
  assert.equal(localeResources['zh-CN']['ui.proxy_node_add_title'], '添加代理节点');
  assert.match(html, /i18n\.t\('ui\.add_row'\)/);
  assert.equal(localeResources['en']['ui.add_row'], 'Add row');
  assert.doesNotMatch(html, /\+ '添加节点<\/button>'/);
  assert.match(html, /data-proxy-node-submit-label>' \+ esc\(i18n\.t\('ui\.save_node'\)\)/);
  assert.match(html, /submitLabel\.textContent = rows\.length > 1/);
  assert.match(html, /proxy\.sources\.import_nodes', \{ nodes: nodes \}/);
  assert.match(html, /#proxySourceDrawer \.drawer-body \{[\s\S]*?overflow-x: hidden/);
  assert.match(html, /tip\.closest\?\.\('#proxySourceDrawer \.drawer'\)/);
  assert.match(html, /\.help-tooltip \{[\s\S]*?position: fixed/);
  assert.match(html, /\.help-tooltip \{[\s\S]*?overflow-wrap: anywhere/);
  assert.match(html, /grid-template-columns: minmax\(150px, 220px\) minmax\(0, 1fr\)/);
  assert.match(html, /\.proxy-node-row-fields input \{[\s\S]*?text-overflow: ellipsis/);
  assert.match(html, /event\.target\.title = event\.target\.value/);
  assert.match(html, /const availableWidth = Math\.max\(0, maxRight - minLeft\)/);
  assert.match(html, /#proxySourceDrawer \.hint,[\s\S]*?font-weight: 500/);
});

test('Proxy source select-all loads every filtered source across pages', async () => {
  const { dashboard: ui, get, mock } = harness();
  ui.state.proxy = { source_count: 600, source_counts: { node: 600, template: 0, resin: 0 } };
  ui.state.proxySourceFilter = 'node';
  ui.state.proxySourceEnabledFilter = 'enabled';
  ui.state.proxySourceHealthFilter = 'healthy';
  ui.state.proxySourceQuery = 'edge';
  ui.state.proxySources = [{ id: 'node-0', kind: 'node', name: 'Node 0', enabled: true, valid: true }];
  ui.state.proxySourceList = { total: 501, page: 1, page_size: 50 };
  ui.renderProxySources();
  assert.equal(get('proxySourceSelectFilteredBtn').hidden, false);
  assert.equal(get('proxySourceSelectFilteredBtn').textContent, '全选当前筛选：501 个');
  assert.equal(get('proxySourceSelectAllBtn').hidden, false);
  assert.equal(get('proxySourceSelectAllBtn').textContent, '全选所有代理：600 个');

  const sources = Array.from({ length: 501 }, (_, index) => ({ id: 'node-' + index, kind: 'node', name: 'Node ' + index }));
  const requests = [];
  mock('api', async (action, params) => {
    requests.push({ action, params });
    const start = (params.page - 1) * params.page_size;
    return { items: sources.slice(start, start + params.page_size), total: sources.length, page: params.page, page_size: params.page_size };
  });
  mock('confirmAction', async () => true);
  await ui.selectProxySourceScope(false, get('proxySourceSelectFilteredBtn'));
  assert.equal(requests.length, 2);
  assert.equal(requests[0].action, 'proxy.sources.list');
  assert.equal(requests[0].params.page_size, 500);
  assert.equal(requests[0].params.kind, 'node');
  assert.equal(requests[0].params.enabled, 'enabled');
  assert.equal(requests[0].params.health, 'healthy');
  assert.equal(requests[0].params.query, 'edge');
  assert.equal(ui.state.proxySourceSelection.size, 501);
  assert.match(get('proxySourceSelectionCount').textContent, /^已选：501 个代理/);

  const allParams = plain(ui.listParams('sources', true));
  assert.equal(allParams.kind, '');
  assert.equal(allParams.enabled, '');
  assert.equal(allParams.health, '');
  assert.equal(allParams.query, '');
});

test('Proxy source versions explain endpoint and generation semantics', () => {
  const { dashboard: ui } = harness();
  const row = ui.proxySourceRow({
    id: 'template-1', kind: 'template', name: 'Region template', enabled: true, valid: true,
    config_version: 4, revision: 7, binding_count: 0
  });
  assert.match(row, /v4 \/ r7/);
  assert.match(row, /title="v：标准端点内容版本/);
  assert.match(row, /r：生成配置版本/);
});

