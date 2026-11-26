import assert from 'node:assert/strict';
import { Element, deferred, harness, html, localeResources, plain, test } from '../support.mjs';

test('CSV and TSV round-trip quoted delimiters, Unicode, and column alignment', () => {
  const { dashboard: ui } = harness();
  const columns = ['供应商', '套餐', '身份'];
  const rows = [['甲', '含,逗号', 'x|1'], ['乙', 'basic', '引号"和\n换行']];
  for (const delimiter of [',', '\t']) {
    const text = ui.formatProxyDelimitedTable(columns, rows, delimiter);
    const parsed = ui.parseProxyDelimitedTable('\uFEFF' + text, delimiter);
    assert.deepEqual(plain(ui.alignProxyImportedRows(columns, parsed, true)), rows);
  }
  const reordered = [['身份', '供应商', '套餐'], ['x1', '甲', 'basic']];
  assert.deepEqual(plain(ui.alignProxyImportedRows(columns, reordered, true)), [['甲', 'basic', 'x1']]);
  assert.throws(() => ui.alignProxyImportedRows(columns, [['供应商', '套餐', '身份'], ['甲', '', 'x1']], true), /套餐.*值为空/);
  assert.throws(() => ui.parseProxyDelimitedTable('"unfinished', ','), /引号未闭合/);
  assert.deepEqual(plain(ui.parseProxyDelimitedTable('""\n', ',')), [['']]);
});

test('Batch editing uses all parent values and preserves unfinished branches', () => {
  const { dashboard: ui } = harness();
  const group = ui.proxyGroupDraft({ columns: ['vendor', 'plan', 'profile'], rows: [['A', 'basic', 'x1'], ['B', 'basic', 'x2']] }, 0);
  group.childIndex = 2;
  ui.appendProxyGroupValues(group, ['B', 'basic'], ['x3', 'x4']);
  assert.deepEqual(plain(group.rows), [['A', 'basic', 'x1'], ['B', 'basic', 'x2'], ['B', 'basic', 'x3'], ['B', 'basic', 'x4']]);
  assert.throws(() => ui.appendProxyGroupValues(group, ['', 'basic'], ['x5']), /完整父级路径/);
  group.childIndex = 0;
  ui.appendProxyGroupValues(group, [], ['C']);
  assert(group.rows.some((row) => row[0] === 'C' && row[1] === '' && row[2] === ''));
  group.childIndex = 1;
  ui.appendProxyGroupValues(group, ['C'], ['pro']);
  group.childIndex = 2;
  ui.appendProxyGroupValues(group, ['C', 'pro'], ['x9']);
  assert(group.rows.some((row) => row.join('/') === 'C/pro/x9'));
  assert(!group.rows.some((row) => row[0] === 'C' && row.some((value) => !value)));
  assert.deepEqual(plain(ui.splitProxyEnum('basic||pro||basic', '||')), ['basic', 'pro']);
  assert.throws(() => ui.splitProxyEnum('basic||', '||'), /空值/);
});

test('Condition candidates respect multiple fixed parents and arbitrary variable names', () => {
  const { dashboard: ui, placeholder } = harness();
  const group = ui.proxyGroupDraft({ id: 'service', columns: ['供应商', 'some-plan', 'profile'], rows: [['甲', 'basic', 'x1'], ['乙', 'basic', 'x2'], ['乙', 'pro', 'x3']] }, 0);
  ui.state.proxyEditing = { kind: 'template', groups: new Map([[0, group]]), assignments: { '供应商': '乙', 'some-plan': 'basic' } };
  placeholder('profile', { group: 'service' });
  assert.deepEqual(plain(ui.proxyTemplateConditionValues('profile')), ['x2']);
  ui.state.proxyEditing.assignments['some-plan'] = 'pro';
  assert.deepEqual(plain(ui.proxyTemplateConditionValues('profile')), ['x3']);
});

test('Changing an enum separator preserves values and refuses collisions', () => {
  const { dashboard: ui, placeholder } = harness();
  ui.state.proxyEditing = { groups: new Map() };
  const card = placeholder('mode', { separator: '|', input: 'basic|pro' });
  const separator = card.querySelector('[data-enum-separator]');
  separator.value = ';';
  ui.changeProxyEnumSeparator(separator);
  assert.equal(card.querySelector('[data-enum-input]').value, 'basic;pro');
  separator.value = 'pro';
  ui.changeProxyEnumSeparator(separator);
  assert.equal(separator.value, ';');
  assert.equal(card.querySelector('[data-enum-input]').value, 'basic;pro');
});

test('Periodic form population leaves dirty policy and auto-bind values intact', async () => {
  const { dashboard: ui, get } = harness();
  ui.state.policy = { quality: { hard_tps: 800 } };
  ui.state.proxy = { auto_bind: { source_scope: 'nodes' } };
  ui.populatePolicyForm(); ui.populateProxyAutoBindForm();
  get('p_hard_tps').value = '777';
  get('pab_source_scope').value = 'templates';
  await get('policyForm').emit('input');
  await get('proxyAutoBindForm').emit('input');
  ui.state.policy = { quality: { hard_tps: 900 } };
  ui.state.proxy = { auto_bind: { source_scope: 'all' } };
  ui.populatePolicyForm(); ui.populateProxyAutoBindForm();
  assert.equal(get('p_hard_tps').value, '777');
  assert.equal(get('pab_source_scope').value, 'templates');
});

test('Out-of-order list responses cannot replace a newer page', async () => {
  const { dashboard: ui, mock } = harness();
  const old = deferred(), fresh = deferred();
  let count = 0;
  mock('api', () => (++count === 1 ? old.promise : fresh.promise));
  const first = ui.loadList('sources');
  ui.state.proxySourceQuery = 'new';
  const second = ui.loadList('sources');
  fresh.resolve({ items: [{ id: 'new', kind: 'node' }], total: 1 });
  await second;
  old.resolve({ items: [{ id: 'old', kind: 'node' }], total: 1 });
  await first;
  assert.equal(ui.state.proxySources[0].id, 'new');
});

test('Changing account filters during an in-flight list load queues the current request', async () => {
  const { dashboard: ui, get, mock } = harness();
  const first = deferred();
  const second = deferred();
  const queries = [];
  mock('api', (action, params) => {
    assert.equal(action, 'accounts.list');
    queries.push(params.query);
    return queries.length === 1 ? first.promise : second.promise;
  });
  ui.state.activeTab = 'accounts';
  const initial = ui.ensureList('accounts', false);
  await Promise.resolve();
  ui.state.accountQuery = 'new';
  const updated = ui.ensureList('accounts', false);
  first.resolve({ items: [{ auth_id: 'auth-1', display: 'old***@example.com', state: 'healthy', proxy_status: 'unbound', binding_version: 1 }], total: 1, page: 1, page_size: 50, state_counts: { healthy: 1 }, binding_version: 1 });
  await initial;
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(queries.length, 2, 'the newer filter must trigger a follow-up request');
  assert.equal(queries[1], 'new');
  second.resolve({ items: [{ auth_id: 'auth-1', display: 'new***@example.com', state: 'healthy', proxy_status: 'bound', binding_version: 2 }], total: 1, page: 1, page_size: 50, state_counts: { healthy: 1 }, binding_version: 2 });
  await updated;
  assert.match(get('accountRows').innerHTML, /new\*\*\*@example\.com/);
});

test('Stale proxy summaries preserve newer binding counts and retry within a bound', async () => {
  const { dashboard: ui, mock } = harness({ captureTimers: true });
  let calls = 0;
  mock('api', async (action) => {
    assert.equal(action, 'proxy.summary');
    calls += 1;
    return { binding_counts: { checking: 4 }, binding_total: 4, binding_version: 9, source_count: 2, enabled_source_count: 1 };
  });
  ui.state.bindingsVersion = 11;
  ui.state.proxy = { binding_counts: { unbound: 4 }, binding_total: 4, binding_version: 10, source_count: 1 };
  ui.state.proxySummaryLoadedAt = Date.now() - 60000;
  await ui.ensureProxySummary(true);
  assert.deepEqual(ui.state.proxy.binding_counts, { unbound: 4 });
  assert.equal(ui.state.proxy.binding_total, 4);
  assert.equal(ui.state.proxy.binding_version, 10);
  assert.equal(ui.state.proxy.source_count, 2);
  assert.equal(ui.state.proxySummaryLoadedAt, 0);
  assert.equal(ui.state.proxySummaryRetries, 1);
  assert.equal(calls, 1);
});
test('Account tab first paint loads one page list and reuses shared bindings', async () => {
  const { dashboard: ui, get, mock, runTimers } = harness({ captureTimers: true });
  const requests = [];
  let metadataIDs = null;
  mock('api', async (action, params) => {
    requests.push(action);
    if (action === 'overview') return { auth_count: 1, states: { unknown: 1 } };
    if (action === 'accounts.list') return {
      items: [{ auth_id: 'auth-1', display: 'a***@example.com', state: 'unknown', proxy_status: 'checking', proxy_inspected: false, binding_version: 9 }],
      total: 1, page: 1, page_size: 50, state_counts: { unknown: 1 }, binding_version: 9
    };
    if (action === 'accounts.proxy_metadata') {
      metadataIDs = params.auth_ids;
      return { items: [{ auth_id: 'auth-1', proxy_status: 'unbound', proxy_inspected: true, proxy_managed: false, binding_version: 10 }], binding_version: 10 };
    }
    if (action === 'proxy.summary') return { binding_counts: { checking: 1 }, binding_version: 9 };
    if (action === 'proxy.sources.list') return { items: [], total: 0, page: 1, page_size: 50 };
    throw new Error('unexpected request ' + action);
  });
  ui.state.activeTab = 'accounts';
  await ui.ensureTabData('accounts', false);
  runTimers();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.ok(requests.includes('accounts.list'));
  assert.ok(!requests.includes('proxy.sources.list'), 'account tab must not block on the proxy source list');
  assert.deepEqual(metadataIDs, ['auth-1']);
  assert.equal(ui.state.bindingsByAuthID.get('auth-1').proxy_status, 'unbound');
  assert.match(get('accountRows').innerHTML, new RegExp(ui.i18n.t('proxy.binding.unbound')));
});

test('Switching back to an already loaded tab does not request its list again', async () => {
  const { dashboard: ui, mock } = harness({ captureTimers: true });
  let accountRequests = 0;
  mock('api', async (action) => {
    if (action === 'accounts.list') {
      accountRequests += 1;
      return { items: [{ auth_id: 'auth-1', display: 'a***@example.com', state: 'healthy', proxy_status: 'unbound', binding_version: 1 }], total: 1, page: 1, page_size: 50, state_counts: { healthy: 1 }, binding_version: 1 };
    }
    if (action === 'overview') return { auth_count: 1, states: { healthy: 1 } };
    if (action === 'proxy.summary') return { binding_counts: { unbound: 1 }, binding_version: 1 };
    return { items: [], total: 0, page: 1, page_size: 50 };
  });
  ui.state.activeTab = 'accounts';
  await ui.ensureTabData('accounts', false);
  await new Promise((resolve) => setTimeout(resolve, 0));
  ui.setTab('overview', false);
  ui.setTab('accounts', false);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(accountRequests, 1, 'cached account list should be reused within the TTL');
});

test('Stale list revalidation keeps the old rows visible until fresh data arrives', async () => {
  const { dashboard: ui, get, mock } = harness({ captureTimers: true });
  const fresh = deferred();
  let accountRequests = 0;
  mock('api', (action) => {
    if (action === 'accounts.list') {
      accountRequests += 1;
      if (accountRequests === 1) return Promise.resolve({ items: [{ auth_id: 'auth-1', display: 'old***@example.com', state: 'healthy', proxy_status: 'unbound', binding_version: 1 }], total: 1, page: 1, page_size: 50, state_counts: { healthy: 1 }, binding_version: 1 });
      return fresh.promise;
    }
    if (action === 'overview') return Promise.resolve({ auth_count: 1, states: { healthy: 1 } });
    if (action === 'proxy.summary') return Promise.resolve({ binding_counts: { unbound: 1 }, binding_version: 1 });
    return Promise.resolve({ items: [], total: 0, page: 1, page_size: 50 });
  });
  ui.state.activeTab = 'accounts';
  await ui.ensureTabData('accounts', false);
  ui.state.listLoadedAt.accounts = Date.now() - 60000;
  await ui.ensureTabData('accounts', false);
  assert.equal(accountRequests, 2);
  assert.match(get('accountRows').innerHTML, /old\*\*\*@example\.com/);
  fresh.resolve({ items: [{ auth_id: 'auth-1', display: 'new***@example.com', state: 'healthy', proxy_status: 'bound', binding_version: 2 }], total: 1, page: 1, page_size: 50, state_counts: { healthy: 1 }, binding_version: 2 });
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.match(get('accountRows').innerHTML, /new\*\*\*@example\.com/);
});

test('Binding cache rejects late versions and preserves omitted source names', () => {
  const { dashboard: ui } = harness();
  ui.state.accounts = [{ auth_id: 'auth-1', display: 'a***@example.com', state: 'healthy' }];
  ui.state.leases = [{ auth_id: 'auth-1', display: 'a***@example.com', state: 'healthy' }];
  ui.mergeBindingItems([{ auth_id: 'auth-1', proxy_status: 'bound', proxy: 'socks5h://u***@proxy:1080', proxy_source_id: 'node-1', proxy_source_kind: 'node', proxy_source_name: 'Node one', proxy_managed: true, proxy_generation: 1, proxy_source_revision: 2, proxy_inspected: true, binding_version: 5 }], 5);
  ui.mergeBindingItems([{ auth_id: 'auth-1', proxy_status: 'bound', proxy: 'socks5h://u***@proxy:1080', proxy_source_id: 'node-1', proxy_source_kind: 'node', proxy_managed: true, proxy_generation: 1, proxy_source_revision: 2, proxy_inspected: true, binding_version: 6 }], 6);
  assert.equal(ui.state.bindingsByAuthID.get('auth-1').proxy_source_name, 'Node one');
  ui.mergeBindingItems([{ auth_id: 'auth-1', proxy_status: 'unbound', proxy_managed: false, proxy_inspected: true, binding_version: 4 }], 4);
  assert.equal(ui.state.bindingsByAuthID.get('auth-1').proxy_status, 'bound', 'a lower binding version must not overwrite newer state');
  ui.mergeBindingItems([{ auth_id: 'auth-1', proxy_status: 'bound', proxy_source_id: 'node-2', proxy_source_kind: 'node', proxy_managed: true, proxy_inspected: true, binding_version: 7 }], 7);
  assert.equal(ui.state.bindingsByAuthID.get('auth-1').proxy_source_name, '');
  assert.equal(ui.state.accounts[0].proxy_status, 'bound');
  assert.equal(ui.state.leases[0].proxy_status, 'bound');
});

test('Account drawer keeps the newer binding projection from list or detail', () => {
  const { dashboard: ui, get } = harness({ languages: ['en'], language: 'en' });
  ui.state.accountDetails.set('auth-1', {
    auth_id: 'auth-1', display: 'a***@example.com', state: 'unknown',
    proxy_status: 'checking', proxy_inspected: false, binding_version: 8
  });
  ui.renderDrawer({
    auth_id: 'auth-1', display: 'a***@example.com', state: 'unknown',
    proxy_status: 'bound', proxy_source_id: 'node-1', proxy_managed: true, proxy_inspected: true, binding_version: 9
  });
  assert.match(get('drawerBody').innerHTML, /Proxy bound/);
  assert.doesNotMatch(get('drawerBody').innerHTML, /Checking/);

  ui.state.accountDetails.set('auth-1', {
    auth_id: 'auth-1', display: 'a***@example.com', state: 'unknown',
    proxy_status: 'unbound', proxy_inspected: true, binding_version: 10
  });
  ui.renderDrawer({
    auth_id: 'auth-1', display: 'a***@example.com', state: 'unknown',
    proxy_status: 'checking', proxy_inspected: false, binding_version: 9
  });
  assert.doesNotMatch(get('drawerBody').innerHTML, /Proxy bound/);
  assert.match(get('drawerBody').innerHTML, /No proxy bound/);
});
test('Checking and read failures are distinct in account and proxy rows', () => {
  const { dashboard: ui } = harness();
  const checking = ui.accountRow({ auth_id: 'auth-1', display: 'a***@example.com', state: 'unknown', proxy_status: 'checking' });
  const failed = ui.accountRow({ auth_id: 'auth-1', display: 'a***@example.com', state: 'unknown', proxy_status: 'read_failed', proxy_inspected: true });
  assert.match(checking, new RegExp(ui.i18n.t('proxy.binding.checking')));
  assert.doesNotMatch(checking, new RegExp(ui.i18n.t('proxy.binding.read_failed')));
  assert.match(failed, new RegExp(ui.i18n.t('proxy.binding.read_failed')));
  assert.equal(ui.leaseStatus({ proxy_status: 'checking' }), 'checking');
  assert.equal(ui.leaseStatus({ proxy_status: 'read_failed' }), 'read_failed');
  assert.equal(ui.leaseStatus({ proxy_status: 'unbound' }), 'unbound');
});

test('Resin tab loads bindings first so summary can reuse binding counts', async () => {
  const { dashboard: ui, mock } = harness({ captureTimers: true });
  const requests = [];
  let resolveLeases;
  const leases = new Promise((resolve) => { resolveLeases = resolve; });
  mock('api', (action) => {
    requests.push(action);
    if (action === 'proxy.bindings.list') return leases;
    if (action === 'proxy.summary') return Promise.resolve({ binding_counts: { bound: 1 }, binding_total: 1, binding_version: 2 });
    if (action === 'proxy.sources.list') return Promise.resolve({ items: [], total: 0, page: 1, page_size: 50 });
    if (action === 'overview') return Promise.resolve({ auth_count: 1, states: { healthy: 1 } });
    throw new Error('unexpected request ' + action);
  });
  ui.state.activeTab = 'resin';
  const loading = ui.ensureTabData('resin', false);
  await Promise.resolve();
  assert.ok(!requests.includes('proxy.summary'), 'summary must wait for the binding aggregation');
  resolveLeases({ items: [{ auth_id: 'auth-1', display: 'a***@example.com', state: 'healthy', proxy_status: 'bound', proxy_managed: true, binding_version: 2 }], total: 1, page: 1, page_size: 50, binding_counts: { bound: 1 }, binding_total: 1, binding_version: 2 });
  await loading;
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.ok(requests.indexOf('proxy.bindings.list') < requests.indexOf('proxy.summary'));
});

test('Proxy source rows render before the runtime summary finishes', async () => {
  const { dashboard: ui, get, mock } = harness();
  const summary = deferred();
  mock('api', (action) => {
    if (action === 'proxy.summary') return summary.promise;
    if (action === 'proxy.sources.list') return Promise.resolve({ items: [{ id: 'node-1', kind: 'node', name: 'Node one', enabled: true, valid: true, endpoint: 'socks5h://u***@host:1080', binding_count: 0 }], total: 1, page: 1, page_size: 50 });
    throw new Error('unexpected request ' + action);
  });
  const request = ui.ensureProxyData(true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.match(get('proxySourceRows').innerHTML, /Node one/);
  summary.resolve({ binding_counts: {} });
  await request;
});

test('Proxy data requests remain single-flight when requested concurrently', async () => {
  const { dashboard: ui, mock } = harness();
  const summary = deferred();
  const sources = deferred();
  const requests = [];
  mock('api', (action) => {
    requests.push(action);
    if (action === 'proxy.summary') return summary.promise;
    if (action === 'proxy.sources.list') return sources.promise;
    throw new Error('unexpected request ' + action);
  });
  const first = ui.ensureProxyData(true);
  const second = ui.ensureProxyData(true);
  assert.deepEqual(requests.sort(), ['proxy.sources.list', 'proxy.summary']);
  summary.resolve({});
  sources.resolve({ items: [], total: 0, page: 1, page_size: 50 });
  await Promise.all([first, second]);
  assert.equal(requests.length, 2);
});

test('Visible metadata enrichment only requests current checking rows', async () => {
  const { dashboard: ui, mock, runTimers } = harness({ captureTimers: true });
  const metadataRequests = [];
  ui.state.activeTab = 'accounts';
  ui.state.accounts = [
    { auth_id: 'auth-1', proxy_status: 'checking', proxy_inspected: false },
    { auth_id: 'auth-2', proxy_status: 'unbound', proxy_inspected: true },
    { auth_id: 'auth-3', proxy_status: 'read_failed', proxy_inspected: true }
  ];
  mock('api', async (action, params) => {
    assert.equal(action, 'accounts.proxy_metadata');
    metadataRequests.push(params.auth_ids);
    return { items: params.auth_ids.map((authID) => ({ auth_id: authID, proxy_status: 'checking', proxy_inspected: false, binding_version: 8 })), binding_version: 8 };
  });
  ui.scheduleAccountMetadataRefresh();
  runTimers();
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.deepEqual(metadataRequests, [['auth-1']]);
});

test('refreshAll refreshes only the active tab and does not hold a global lock', async () => {
  const { dashboard: ui, mock } = harness();
  const requests = [];
  mock('api', async (action) => {
    requests.push(action);
    if (action === 'accounts.list') return { items: [], total: 0, page: 1, page_size: 50, state_counts: {} };
    if (action === 'overview') return { auth_count: 0, states: {} };
    if (action === 'proxy.summary') return { binding_counts: {} };
    return { items: [], total: 0, page: 1, page_size: 50 };
  });
  ui.state.activeTab = 'accounts';
  await ui.refreshAll(true);
  assert.ok(requests.includes('accounts.list'));
  assert.ok(!requests.includes('proxy.bindings.list'));
  assert.equal(ui.state.refreshing, false);
});

test('Account detail cache is bounded while the selected detail stays available', () => {
  const { dashboard: ui } = harness();
  for (let index = 0; index < 150; index += 1) {
    ui.state.accountDetails.set('auth-' + index, { auth_id: 'auth-' + index });
  }
  ui.state.selectedAuthID = 'auth-0';
  ui.applyAccountListData({ items: [], total: 150, state_counts: { unknown: 150 } });
  assert.equal(ui.state.accountDetails.has('auth-0'), true);
  assert.ok(ui.state.accountDetails.size <= 100, 'detail cache size = ' + ui.state.accountDetails.size);
});
test('A million-source list renders only one page and preserves cross-page account selection', async () => {
  const { dashboard: ui, get, mock } = harness();
  const requests = [];
  mock('api', async (action, params) => {
    requests.push({ action, params });
    return { total: 1_000_000, page: 1, page_size: 50, items: Array.from({ length: 50 }, (_, index) => ({ id: 'node-' + index, name: 'Node ' + index, kind: 'node', enabled: true, valid: true })) };
  });
  await ui.loadList('sources');
  ui.renderProxySources();
  assert.equal(requests.length, 1);
  assert.equal(requests[0].params.page_size, 50);
  assert.equal((get('proxySourceRows').innerHTML.match(/<tr>/g) || []).length, 50);
  ui.state.accountSelection.add('old-page-auth');
  ui.applyAccountListData({ items: [{ auth_id: 'new-page-auth' }], total: 1_000_000 });
  assert.deepEqual(plain(ui.selectedAccountIDs()), ['old-page-auth']);
});

test('A large relation table displays twenty rows without creating a whole tree', () => {
  const { dashboard: ui, groupCard } = harness();
  const rows = Array.from({ length: 10_000 }, (_, index) => ['vendor-' + index, 'profile-' + index]);
  const group = ui.proxyGroupDraft({ id: 'many', columns: ['vendor', 'profile'], rows }, 0);
  ui.state.proxyEditing = { groups: new Map([[0, group]]) };
  const card = groupCard(group);
  group.view = 'table'; ui.renderProxyGroup(0);
  assert.equal((card.querySelector('[data-group-body]').innerHTML.match(/<tr>/g) || []).length, 21);
  group.view = 'hierarchy'; group.childIndex = 0; ui.renderProxyGroup(0);
  assert.equal((card.querySelector('[data-group-body]').innerHTML.match(/data-group-drill-value=/g) || []).length, 50);
});

