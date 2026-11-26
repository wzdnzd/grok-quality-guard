import assert from 'node:assert/strict';
import { Element, deferred, harness, html, localeResources, plain, test } from '../support.mjs';

test('Lease actions reserve a stable slot for single and paired buttons', () => {
  const { dashboard: ui } = harness();
  ui.state.proxySources = [];
  const bound = ui.leaseRow({
    auth_id: 'auth-1', auth_id_display: 'auth-1', display: 'a***@example.com', display_full: '[EMAIL]',
    proxy: 'socks5h://us***r:******@resin:2260', proxy_status: 'bound', proxy_source_id: 'resin-1'
  });
  const external = ui.leaseRow({
    auth_id: 'auth-2', auth_id_display: 'auth-2', display: 'b***@example.com', display_full: '[EMAIL]',
    proxy: 'socks5h://us***r:******@resin:2260', proxy_status: 'external'
  });
  assert.match(bound, /<span class="proxy-row-primary"><button[^>]+data-proxy-action="rotate"/);
  assert.match(bound, /data-proxy-action="clear"/);
  assert.match(external, /<span class="proxy-row-primary is-single"><button[^>]+data-proxy-action="clear-external"/);
  assert.match(bound, /<\/span><button class="btn btn-xs btn-ghost"[^>]+data-proxy-action="pause-auto"/);
  assert.match(external, /<\/span><button class="btn btn-xs btn-ghost"[^>]+data-proxy-action="pause-auto"/);
  assert.match(html, /\.proxy-lease-table \.actions\s*\{\s*justify-content:\s*flex-start;/s);
  assert.match(html, /\.proxy-lease-table \.proxy-row-primary\s*\{[^}]*flex:\s*0 0 136px;[^}]*width:\s*136px;/s);
  assert.match(html, /\.proxy-lease-table \.proxy-row-primary\.is-single\s*\{\s*display:\s*inline-flex;/s);
});
test('Live resident queue detail renders active and waiting object IDs', async () => {
  const { dashboard: ui, get, mock } = harness();
  const requests = [];
  mock('api', async (action, params) => {
    requests.push({ action, params });
    const queueID = String(params.queue_id);
    return {
      queue_id: queueID,
      active: [{ id: queueID + '-active', label: queueID + '-active', status: 'running', started_at: '2026-11-18T08:00:00Z' }],
      waiting: {
        items: [{ id: queueID + '-waiting', label: queueID + '-waiting', status: 'waiting', position: 1, queued_at: '2026-11-18T07:59:00Z' }],
        total: 1, page: 1, page_size: 20
      }
    };
  });
  const cases = [
    { id: 'quality_probe', label: '质量检测队列' },
    { id: 'auto_bind', label: '自动绑定队列' },
    { id: 'proxy_health', label: '代理健康检查' }
  ];
  for (const testCase of cases) {
    const queue = { id: testCase.id, label: testCase.label, status: 'running', total: 2, completed: 0, running: 1, waiting: 1, succeeded: 0, failed: 0 };
    ui.state.taskCenterSelectedQueue = queue;
    await ui.loadTaskQueueDetail(queue, 1);
    const request = requests.at(-1);
    assert.equal(request.action, 'tasks.center.queue');
    assert.deepEqual(plain(request.params), { queue_id: testCase.id, page: 1, page_size: 20 });
    const body = get('residentQueueDetailBody').innerHTML;
    assert.match(body, /执行中的任务/);
    assert.match(body, /等待执行的任务/);
    assert.match(body, new RegExp(testCase.id + '-active'));
    assert.match(body, new RegExp(testCase.id + '-waiting'));
  }
  assert.equal(requests.length, cases.length);
});
test('English proxy editor surfaces do not leak Simplified Chinese copy', async () => {
  const { dashboard: ui, get } = harness({ languages: ['en-US'], language: 'en-US' });
  const failures = [];
  const check = async (name, open) => {
    await open();
    const rendered = get('proxySourceEditor').innerHTML + '\n' + get('proxySourceEditorTitle').textContent + '\n' + get('proxySourceEditorSubtitle').textContent;
    const leftovers = [...rendered.matchAll(/[^\n]*[\u3400-\u9fff][^\n]*/g)].map((match) => match[0].trim());
    if (leftovers.length) failures.push({ name, leftovers: [...new Set(leftovers)].slice(0, 20) });
  };
  await check('node', () => ui.openProxyNodeEditor(null));
  await check('template', () => ui.openProxyTemplateEditor(null));
  await check('resin', () => ui.openProxyResinEditor(null));
  await check('import', () => ui.openProxyImportEditor());
  assert.deepEqual(failures, []);
});
test('Internal error events show the sanitized diagnostic', () => {
  const { dashboard: ui, get } = harness();
  const diagnostic = 'read tcp 10.0.0.5:443: i/o timeout';
  ui.state.events = [{
    time: '2026-11-23T08:00:00Z',
    auth_id: 'auth-1',
    action: 'internal_error',
    result: 'error',
    detail: '发生内部错误，请查看日志',
    detail_raw: diagnostic
  }];
  ui.state.eventList = { total: 1, page: 1, page_size: 50 };
  ui.renderEvents();
  const rendered = get('eventRows').innerHTML;
  assert.match(rendered, /内部错误/);
  assert.match(rendered, /event-log-details/);
  assert.match(rendered, /event-log-summary/);
  assert.match(rendered, /event-log-link/);
  assert.match(rendered, /event-log-diagnostic/);
  assert.match(rendered, /原始错误日志/);
  assert.match(rendered, /title="/);
  assert.match(rendered, /i\/o timeout/);
  assert.match(rendered, /read tcp/);
  ui.i18n.applyLocale('en');
  ui.state.events[0].detail = 'An internal error occurred. Check the logs';
  ui.renderEvents();
  assert.match(get('eventRows').innerHTML, />Check the logs</);
  ui.i18n.applyLocale('zh-TW');
  ui.state.events[0].detail = '發生內部錯誤，請查看記錄';
  ui.renderEvents();
  assert.match(get('eventRows').innerHTML, />查看記錄</);
  ui.i18n.applyLocale('zh-CN');
  ui.state.events[0].detail = '发生内部错误，请查看日志';
  ui.state.eventQuery = 'i/o timeout';
  assert.equal(ui.eventMatches(ui.state.events[0]), true);
});
test('Account transport errors link to filtered transport events', async () => {
  const { dashboard: ui, get, context, mock } = harness();
  mock('api', async () => ({}));
  const account = {
    auth_id: 'auth-1',
    state: 'degraded',
    last_reason: 'probe_error',
    last_attempt: { class: 'probe_error', reason: 'reason.detail.probe_transport_error', source: 'probe' },
    last_observed_at: '2026-11-23T08:00:00Z'
  };
  const row = ui.accountRow(account);
  assert.match(row, /href="#events\/auth-1\/transport"/);
  assert.match(row, />探测传输错误<\/a>/);
  const localizedRow = ui.accountRow({ ...account, last_attempt: { ...account.last_attempt, reason: '探测传输错误' } });
  assert.match(localizedRow, /href="#events\/auth-1\/transport"/);

  context.window.location.hash = '#events/auth-1/transport';
  ui.applyLocationHash();
  assert.equal(ui.state.activeTab, 'events');
  assert.equal(ui.state.eventQuery, 'auth-1');
  assert.equal(ui.state.eventType, 'transport');
  assert.equal(get('eventSearch').value, 'auth-1');
  assert.equal(get('eventTypeFilter').value, 'transport');
  assert.equal(ui.eventMatches({ auth_id: 'auth-1', action: 'probe', class: 'probe_error', category: 'transport' }), true);
  assert.equal(ui.eventMatches({ auth_id: 'auth-1', action: 'probe', class: 'missing_thinking', category: 'quality' }), false);
  assert.equal(ui.eventMatches({ auth_id: 'auth-1', action: 'probe', class: '' }), false);
  assert.equal(ui.eventMatches({ auth_id: 'auth-2', action: 'probe', class: 'probe_error', category: 'transport' }), false);
  await Promise.resolve();
});
