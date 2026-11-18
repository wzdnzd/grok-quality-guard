import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';
import { performance } from 'node:perf_hooks';

const html = fs.readFileSync(new URL('./dashboard.html', import.meta.url), 'utf8');
const scripts = [...html.matchAll(/<script(?:[^>]*)>([\s\S]*?)<\/script>/g)].map((match) => match[1]);
for (const script of scripts) new vm.Script(script);
const source = scripts.find((script) => script.includes('const API ='))
  .replace(/\s+applyLocationHash\(\);\s+refreshAll\(true\);\s*$/, '');

// A small DOM boundary for the existing inline controller. These tests exercise
// asynchronous UI state and parsing; browser layout remains a separate check.
class Element {
  constructor(id = '') {
    this.id = id;
    this.dataset = {};
    this.value = '';
    this.checked = false;
    this.disabled = false;
    this.hidden = false;
    this.innerHTML = '';
    this.textContent = '';
    this.attributes = new Map();
    this.selectors = new Map();
    this.children = [];
    this.listeners = new Map();
    this.isConnected = true;
    this.classes = new Set();
    this.classList = {
      add: (...values) => values.forEach((value) => this.classes.add(value)),
      remove: (...values) => values.forEach((value) => this.classes.delete(value)),
      toggle: (value, enabled) => enabled ? this.classes.add(value) : this.classes.delete(value),
      contains: (value) => this.classes.has(value)
    };
  }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name); }
  removeAttribute(name) { this.attributes.delete(name); }
  hasAttribute(name) {
    if (name.startsWith('data-')) {
      const key = name.slice(5).replace(/-([a-z])/g, (_, char) => char.toUpperCase());
      return Object.hasOwn(this.dataset, key);
    }
    return this.attributes.has(name);
  }
  querySelector(selector) {
    if (this.selectors.has(selector)) return this.selectors.get(selector);
    if (selector === '.form-actions') {
      const actions = new Element(this.id + '-actions');
      this.selectors.set(selector, actions);
      return actions;
    }
    return null;
  }
  querySelectorAll(selector) { return this.selectors.get(selector) || []; }
  append(...children) { this.children.push(...children); }
  appendChild(child) { this.children.push(child); child.parent = this; return child; }
  addEventListener(kind, listener) {
    const listeners = this.listeners.get(kind) || [];
    listeners.push(listener);
    this.listeners.set(kind, listeners);
  }
  emit(kind, event = {}) {
    return Promise.all((this.listeners.get(kind) || []).map((listener) => listener({ target: this, currentTarget: this, preventDefault() {}, ...event })));
  }
  closest(selector) { return this.ancestors?.get(selector) || null; }
  focus() { this.focused = true; }
  getClientRects() { return [{}]; }
}

function harness() {
  const elements = new Map();
  const cards = new Map();
  const groups = new Map();
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new Element(id));
    return elements.get(id);
  };
  for (const id of ['proxySourceDrawer', 'accountDrawer', 'confirmDialog']) get(id).hidden = true;
  const document = {
    body: new Element('body'),
    activeElement: new Element('opener'),
    hidden: false,
    getElementById: get,
    createElement: () => new Element(),
    addEventListener() {},
    querySelectorAll(selector) {
      if (selector === '[data-placeholder-card]') return [...cards.values()];
      if (selector === '[data-placeholder-group]') return [...cards.values()].map((card) => card.querySelector('[data-placeholder-group]'));
      return [];
    },
    querySelector(selector) {
      const placeholder = selector.match(/^\[data-placeholder-card="(.*)"\]$/);
      if (placeholder) return cards.get(placeholder[1]) || null;
      const group = selector.match(/^\[data-group-card="(\d+)"\]$/);
      if (group) return groups.get(Number(group[1])) || null;
      const draft = selector.match(/^\[data-draft-(note|discard)="(.*)"\]$/);
      if (draft) return get(draft[2]).querySelector('.form-actions').children.find((child) => child.dataset[draft[1] === 'note' ? 'draftNote' : 'draftDiscard'] === draft[2]) || null;
      return null;
    }
  };
  const historyCalls = [];
  const context = vm.createContext({
    document,
    window: {
      location: { hash: '#overview' },
      history: { replaceState(_state, _title, hash) { historyCalls.push(hash); } },
      addEventListener() {},
      clearTimeout() {},
      setInterval() {},
      setTimeout(callback, duration) { if (duration === 1000) queueMicrotask(callback); return 1; }
    },
    HTMLElement: Element,
    CSS: { escape: (value) => String(value) },
    Blob, URL, TextEncoder, Date, Intl, JSON, Map, Set,
    fetch: async () => { throw new Error('Unexpected network request'); }
  });
  vm.runInContext(source + '\n globalThis.dashboard = { state, formDrafts, proxyGroupDraft, appendProxyGroupValues, proxyRowsMatchPrefix, splitProxyEnum, parseProxyDelimitedTable, formatProxyDelimitedTable, alignProxyImportedRows, populatePolicyForm, populateProxyAutoBindForm, clearFormDraft, formDraftRevision, loadList, loadAllAccounts, applyAccountListData, selectedAccountIDs, pagedItems, renderProxySources, renderResin, syncProxySourceSelectionUI, proxySourceAllCount, loadAllProxySourceIDs, selectProxySourceScope, proxySourceEnabledState, proxySourceConnectivity, listParams, testSelectedProxySources, deleteSelectedProxySources, renderProxyGroup, proxyTemplateConditionValues, previewProxyTemplate, proxyResultMessage, waitForTask, closeProxyDrawer, changeProxyEnumSeparator, detachProxyPlaceholderGroup, confirmProxyBindingOptions, renderProxyOperationSourceChoices, searchProxyOperationSourceChoices, chooseProxyOperationSource, proxyOperationSourceValue, importProxyGroupRows, collectProxyTemplateGroups, collectProxyTemplateEditor, runProxyAccountBatches, runSelectedProxyAction, renderProxyAutoBind, normalizeBindingStatus, proxySummaryCounts, accountRow, openProxyTemplateEditor, openProxyResinEditor, openProxyImportEditor, collectProxyResinEditor, previewProxyResin, endpointCell, leaseRow, toggleLeaseEndpoint, proxySourceRow, toggleSourceEndpoint, eventMatches, renderEvents, api, friendlyApiMessage, saveProxyAutoBind, setProxyAutoBindSettingsOpen, proxyImportLine, parseProxyImportText, cleanHelpCopy, proxyNodeRowHTML, renderTaskCenter, refreshTaskCenter, selectTaskCenterTask, selectTaskCenterQueue, closeTaskCenterDetail, renderResidentQueues, loadTaskCenterDetail, loadTaskQueueDetail, openTaskCenterChange, renderTaskDetail, renderTaskQueueDetail, renderTaskPager, taskHistoryText, collectPolicy, populatePolicyForm, applyLocationHash };', context);
  function mock(name, callback) {
    context.testCallback = callback;
    vm.runInContext(name + ' = globalThis.testCallback;', context);
  }
  function placeholder(name, { group = '', separator = '|', input = '', kind = 'enum' } = {}) {
    const card = new Element();
    card.dataset.placeholderCard = name;
    const fields = {
      '[data-placeholder-kind]': kind,
      '[data-placeholder-group]': group,
      '[data-enum-separator]': separator,
      '[data-enum-input]': input,
      '[data-random-length]': '12'
    };
    for (const [selector, value] of Object.entries(fields)) {
      const field = new Element();
      field.value = value;
      field.ancestors = new Map([['[data-placeholder-card]', card]]);
      card.selectors.set(selector, field);
    }
    card.querySelector('[data-enum-separator]').dataset.previousSeparator = separator;
    card.querySelector('[data-placeholder-group]').dataset.previousGroup = group;
    for (const selector of ['[data-enum-error]', '[data-enum-summary]', '[data-random-case]']) card.selectors.set(selector, new Element());
    cards.set(name, card);
    return card;
  }
  function groupCard(group) {
    const card = new Element();
    card.dataset.groupCard = group.index;
    card.selectors.set('[data-group-error]', new Element());
    card.selectors.set('[data-group-body]', new Element());
    for (const [selector, value] of [['[data-group-id]', group.id], ['[data-group-name]', group.name], ['[data-group-columns]', group.columns.join(',')], ['[data-group-bulk-values]', ''], ['[data-group-import-text]', group.importText]]) { const field = new Element(); field.value = value; card.selectors.set(selector, field); }
    groups.set(group.index, card);
    return card;
  }
  return { dashboard: context.dashboard, get, context, mock, placeholder, groupCard, document, historyCalls };
}

const plain = (value) => JSON.parse(JSON.stringify(value));
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const cases = [];
const test = (name, run) => cases.push({ name, run });

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

test('Account rows show binding status and truncate full cooldown labels', () => {
  const { dashboard: ui } = harness();
  assert.match(html, /<th title="代理绑定状态与轮换代数">绑定状态<\/th>/);
  assert.match(html, /<th title="冷却截止时间">冷却<\/th>/);
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

test('Selected proxy clear explicitly allows external configurations', async () => {
  const { dashboard: ui, mock } = harness();
  ui.state.leaseSelection.add('auth-external');
  const requests = [];
  mock('confirmProxyBindingOptions', async () => ({ exclude_auto_bind: null }));
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
  assert.match(get('proxyAutoBindText').title, /最近错误：catalog unavailable/);
  assert.doesNotMatch(html, /id="proxyAutoBindDetails"/);
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
  assert.match(candidateHTML, />Template one · 模板</);

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
  assert.equal(get('proxyOperationSource').value, 'Template two');
  assert.equal(get('proxyOperationSourceID').value, 'template-2');
  assert.equal(ui.proxyOperationSourceValue(), 'template-2');

  assert.match(html, /\.confirm-card\.wide\s*\{/);
  assert.match(html, /role="combobox"/);
  assert.match(html, /\.proxy-source-picker-menu\s*\{[\s\S]*?max-height:\s*240px/);
  assert.doesNotMatch(html, /<datalist id="proxyOperationSourceChoices"/);
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
  assert.match(html, /上限：100000 条 · 16 MiB/);
  const parsed = ui.parseProxyImportText(text);
  assert.equal(parsed.lineCount, 7);
  assert.equal(parsed.validCount, 2);
  assert.equal(parsed.invalid, 3);
  assert.equal(parsed.duplicate, 1);
  assert.equal(parsed.comments, 1);
  assert.deepEqual(plain(parsed.valid), ['http://user:pass@proxy.example:8080', 'SOCKS5H://user:pass@[2001:db8::1]:1080']);
  assert.equal(parsed.invalidSamples.length, 3);
  assert.match(html, /标准 URL 优先，失败后自动尝试地址在前或 host:port@user:pass/);
  assert.match(html, /socks5h:\/\/proxy\.example:1080:user:pass/);
  assert.match(html, /http:\/\/proxy\.example:8080@user:pass/);
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
  assert.equal((get('taskDetailBody').innerHTML.match(/<td class="mono">auth-/g) || []).length, 20);
  assert.match(get('taskDetailBody').innerHTML, /第 2 \/ 50000 页/);
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
  assert.equal((get('taskDetailBody').innerHTML.match(/data-task-cleanup-retry=/g) || []).length, 20);
  assert.match(get('taskDetailBody').innerHTML, /第 2 \/ 50000 页/);
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
  assert.equal(ui.normalizeBindingStatus('reconcile_required'), 'reconcile_required');
  assert.equal(ui.normalizeBindingStatus('unknown'), 'unknown');
  const counts = ui.proxySummaryCounts({ binding_counts: { bound: 12, reconcile_required: 3, external: 2, unknown: 4 } });
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
  assert.match(rendered, /绑定覆盖率：100%/);
  assert.match(rendered, /可用：2 \/ 2/);
  assert.doesNotMatch(rendered, /可绑定代理：2/);
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
  assert.match(html, /网络连通性<\/th>\s*<th title="最近一次网络连通性检测时间">检测时间<\/th>/);

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
  assert.match(html, /将删除此代理并清理受管理代理，失败项保留并报告/);
  assert.match(html, /将删除 ' \+ ids\.length \+ ' 个代理并清理受管理代理，失败项保留并报告/);
  assert.doesNotMatch(html, /成功项不会回滚。|成功项不会回滚'/);
});

test('Proxy management uses a compact workspace toolbar with underlined sub-navigation', () => {
  assert.match(html, /<div class="proxy-workspace-bar">/);
  assert.doesNotMatch(html, /代理工作区|统一管理账号绑定关系/);
  assert.doesNotMatch(html, /proxyTasksBtn|查看任务中心|renderProxyTaskBadge/);
  assert.match(html, /data-proxy-subtab="bindings">账号绑定/);
  assert.match(html, /data-proxy-subtab="sources">代理资源/);
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
  assert.match(html, /editing \? '编辑代理节点' : '添加代理节点'/);
  assert.match(html, /增加一行/);
  assert.doesNotMatch(html, /\+ '添加节点<\/button>'/);
  assert.match(html, /data-proxy-node-submit-label>保存节点<\/span>/);
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
test('Checkbox controls reserve a label line and retry fields share one row', () => {
  assert.match(html, /\.field>\.check-row:first-child\s*\{\s*margin-top: 20px;/);
  assert.match(html, /\.settings-group \.retry-grid\s*\{\s*grid-template-columns: repeat\(3, minmax\(0, 1fr\)\);/);
  assert.match(html, /class="field-grid retry-grid"/);
  assert.match(html, /首次失败后的等待时长/);
  assert.match(html, /退避等待的最长时间/);
});

test('Task history checkbox keeps its hint below the control and matches field height', () => {
  assert.match(html, /<div class="field task-history-field">\s*<label class="check-row"><input id="p_task_history_enabled" type="checkbox"><span>保存历史任务<\/span><\/label>\s*<span class="hint">关闭后不再保存新任务记录<\/span>/);
  assert.match(html, /\.policy-group-task-history \.field > \.check-row,\s*\.policy-group-task-history \.field > input\s*\{\s*height: 33px;\s*min-height: 33px;/);
  assert.doesNotMatch(html, /task-history-switch/);
});

test('Overview cards pair into equal-height rows', () => {
  assert.doesNotMatch(html, /<div class="overview-grid">\s*<div class="stack">/);
  assert.match(html, /<div class="overview-grid">\s*<section class="card overview-card" aria-labelledby="qualityTitle">[\s\S]*?aria-labelledby="policySummaryTitle"[\s\S]*?aria-labelledby="recentTitle"[\s\S]*?aria-labelledby="resinSummaryTitle"/);
  assert.match(html, /\.overview-grid\s*\{[\s\S]*?align-items:\s*stretch/);
  assert.match(html, /\.overview-card\s*\{[\s\S]*?display: flex;[\s\S]*?flex-direction: column/);
  assert.match(html, /\.overview-card > \.card-body,\s*\.overview-card > \.recent-list\s*\{\s*flex: 1 1 auto;/);
});

test('Policy settings use a compact grouped grid with responsive density', () => {
  assert.match(html, /<form id="policyForm" class="settings-layout policy-settings-form">/);
  assert.match(html, /<div class="policy-settings-grid">/);
  for (const group of ['mode', 'task-history', 'routing', 'quality', 'probe']) {
    assert.match(html, new RegExp('class="policy-group policy-group-' + group + '"'));
  }
  const modeStart = html.indexOf('class="policy-group policy-group-mode"');
  const taskHistoryStart = html.indexOf('class="policy-group policy-group-task-history"');
  const routingStart = html.indexOf('class="policy-group policy-group-routing"');
  const qualityStart = html.indexOf('class="policy-group policy-group-quality"');
  const probeStart = html.indexOf('class="policy-group policy-group-probe"');
  assert.ok(modeStart < routingStart && routingStart < taskHistoryStart && taskHistoryStart < qualityStart && qualityStart < probeStart);
  assert.match(html, /\.policy-settings-grid\s*\{[\s\S]*?grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);/);
  assert.match(html, /\.policy-group-quality \.field-grid,\s*\.policy-group-probe \.field-grid\s*\{\s*grid-template-columns: repeat\(4, minmax\(0, 1fr\)\);/);
  assert.match(html, /\.policy-group-quality \.check-card\s*\{\s*grid-column: span 2;/);
  assert.match(html, /@media \(max-width: 1180px\)\s*\{[\s\S]*?\.settings-summary-card \.summary-grid\s*\{\s*grid-template-columns: repeat\(4, minmax\(0, 1fr\)\);/);
  assert.match(html, /@media \(max-width: 920px\)\s*\{[\s\S]*?\.policy-settings-grid\s*\{\s*grid-template-columns: 1fr;/);
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
  assert.match(html, /健康记录超过此时长后失效/);
  assert.match(html, /仅选择健康且未过期的代理/);
  assert.match(html, /:password@proxy\.example:1080/);
  assert.doesNotMatch(html, /:密码@proxy\.example:1080/);
  assert.match(html, /\.check-card\.compact\s*\{[^}]*min-height: 33px;[^}]*margin-top: 20px;/s);
  assert.match(html, /class="check-card compact" title="健康账号不做周期探测"><input id="p_probe_recovery_only"/);
  assert.match(html, /id="p_probe_scan_interval"/);
  assert.doesNotMatch(html, /class="check-desc">健康账号不做周期探测/);
  assert.match(html, /默认显示脱敏端点，点击眼睛查看完整端点/);
  assert.match(html, /<th>端点<\/th>/);
  assert.match(html, /<option value="resin">Resin<\/option>/);
  assert.match(html, /proxyAddSourceKind/);
  assert.match(html, /新增类型/);
  assert.doesNotMatch(html, /resinFormHost|resinSourceBar|data-open-resin title=/);
});

test('Network and management API failures use actionable Chinese copy', async () => {
  const { dashboard: ui, context, mock, get } = harness();
  context.fetch = async () => { throw new TypeError('Failed to fetch'); };
  await assert.rejects(ui.api('overview'), /无法连接管理接口，请检查 CPA 服务和插件状态/);
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
  const body = get('taskDetailBody').innerHTML;
  assert.match(body, /第 2 \/ 3 页 · 共：45 项/);
  assert.match(body, /data-task-queue-page="-1"/);
  assert.match(body, /auth-1/);
});
test('Task center is the unified six-tab entry and supports task deep links', async () => {
  const { dashboard: ui, context, get, historyCalls, mock } = harness();
  assert.match(html, /data-tab="overview">概览<\/button>[\s\S]*data-tab="accounts">账号<\/button>[\s\S]*data-tab="resin">代理管理<\/button>[\s\S]*data-tab="tasks">任务中心<\/button>[\s\S]*data-tab="events">事件日志<\/button>[\s\S]*data-tab="settings">设置<\/button>/);
  assert.doesNotMatch(html, /data-tab="accounts">账号质量<\/button>/);
  assert.match(html, /\.tabs\s*\{\s*width: 100%;\s*margin-bottom: 18px;/);
  mock('api', async (action) => {
    if (action === 'tasks.center.get') return { task: { task_id: 'task 1', kind: 'accounts.probe_selected', category: 'account', label: '批量检测账号', status: 'succeeded', total: 1, completed: 1, succeeded: 1 }, items: [], total: 0, item_total: 0, page: 1, page_size: 20, source: 'history' };
    return {};
  });
  context.window.location.hash = '#tasks';
  ui.applyLocationHash();
  assert.equal(ui.state.activeTab, 'tasks');
  context.window.location.hash = '#tasks/task%201';
  ui.applyLocationHash();
  assert.equal(ui.state.taskCenterSelectedID, 'task 1');
  assert.equal(get('taskDetailCard').classList.contains('mobile-open'), true);
  ui.selectTaskCenterTask('task-2', true);
  assert.equal(historyCalls.at(-1), '#tasks/task-2');
  await Promise.resolve();
  await Promise.resolve();
});

test('Task center keeps filters and refresh on one row with equal-height cards', () => {
  assert.match(html, /<div class="toolbar task-toolbar">/);
  assert.match(html, /id="taskStatusFilter"[\s\S]*id="taskRefreshBtn"/);
  assert.equal((html.match(/\.task-toolbar\s*\{/g) || []).length, 1);
  assert.match(html, /\.task-toolbar\s*\{[\s\S]*?flex-wrap:\s*nowrap/);
  assert.match(html, /\.task-toolbar \.toolbar-right\s*\{[\s\S]*?margin-left:\s*auto/);
  assert.match(html, /\.task-center-grid\s*\{[\s\S]*?align-items:\s*stretch/);
  assert.match(html, /\.task-list-card,\s*\.task-detail-card\s*\{[\s\S]*?align-self:\s*stretch/);
});

test('Task center refresh keeps filters and page size in the list request', async () => {
  const { dashboard: ui, get, mock } = harness();
  let request = null;
  mock('api', async (action, params) => {
    assert.equal(action, 'tasks.center.list');
    request = params;
    return {
      items: [{ task_id: 'task-1', kind: 'accounts.probe_selected', category: 'account', label: '批量检测账号', status: 'running', total: 12, completed: 7, running: 2, waiting: 3, succeeded: 5, failed: 2, auth_count: 4, created_at: '2026-11-18T08:00:00Z' }],
      total: 51, page: 2, page_size: 50,
      counts: { active: 1, running_items: 2, failed_items: 2 },
      history: { enabled: true, retention_days: 3, max_records: 500, record_count: 7, archive_bytes: 2048, max_bytes: 134217728 },
      resident_queues: []
    };
  });
  ui.state.taskCenterPage = 2;
  ui.state.taskCenterPageSize = 50;
  ui.state.taskCenterCategory = 'account';
  ui.state.taskCenterStatus = 'active';
  ui.state.taskCenterQuery = 'probe';
  await ui.refreshTaskCenter(true);
  assert.deepEqual(plain(request), { page: 2, page_size: 50, category: 'account', status: 'active', query: 'probe' });
  assert.equal(ui.state.taskCenter.total, 51);
  assert.match(get('taskRows').innerHTML, /批量检测账号/);
  assert.match(get('taskPager').innerHTML, /50<\/option>/);
  assert.match(get('taskPager').innerHTML, /第 2 \/ 2 页/);
});

test('Task detail exposes progress, queue counts, metadata and lazy item paging', async () => {
  const { dashboard: ui, get, mock } = harness();
  let request = null;
  mock('api', async (action, params) => {
    assert.equal(action, 'tasks.center.get');
    request = params;
    return {
      task: {
        task_id: 'task-1', kind: 'accounts.probe_selected', category: 'account', label: '批量检测账号', status: 'running',
        total: 12, completed: 7, running: 2, waiting: 3, succeeded: 5, failed: 2, skipped: 0,
        item_count: 12, items_stored: 10, items_truncated: true, auth_ids: ['auth-1', 'auth-2'], auth_count: 4,
        current_stage: '正在检测账号', created_at: '2026-11-18T08:00:00Z', started_at: '2026-11-18T08:00:01Z', updated_at: '2026-11-18T08:00:20Z', duration_ms: 19000
      },
      items: [{ auth_id: 'auth-2', status: 'failed', error: 'upstream failed' }],
      total: 2, item_total: 10, page: 2, page_size: 1, items_truncated: true, source: 'history'
    };
  });
  ui.state.taskCenterSelectedID = 'task-1';
  await ui.loadTaskCenterDetail('task-1', 2, 'failed', false);
  assert.deepEqual(plain(request), { task_id: 'task-1', page: 2, page_size: 20, item_status: 'failed' });
  const body = get('taskDetailBody').innerHTML;
  assert.match(body, /任务总数/);
  assert.match(body, /已完成/);
  assert.match(body, /正在处理/);
  assert.match(body, /等待中/);
  assert.match(body, /历史归档/);
  assert.match(body, /upstream failed/);
  assert.match(body, /已截断/);
  assert.match(html, /skipped: '已跳过'/);
  assert.match(html, /probed: '已检测'/);
});

test('Task center places cancel beside active status and delete beside finished status', () => {
  const { dashboard: ui, get } = harness();
  ui.state.taskCenter = {
    items: [
      { task_id: 'running-1', kind: 'accounts.probe_selected', category: 'account', label: '批量检测账号', status: 'running', total: 3, completed: 1, running: 1, waiting: 1 },
      { task_id: 'finished-1', kind: 'proxy.bind', category: 'proxy', label: '批量绑定代理', status: 'interrupted', total: 3, completed: 2 }
    ],
    total: 2, page: 1, page_size: 20, counts: {}, resident_queues: [],
    history: { enabled: true, retention_days: 3, record_count: 2, archive_bytes: 10, max_bytes: 100 }
  };
  ui.renderTaskCenter();
  const rows = get('taskRows').innerHTML;
  assert.match(rows, /<span class="task-status running">执行中<\/span><button class="task-row-action" type="button" data-task-center-cancel="running-1">取消任务<\/button>/);
  assert.match(rows, /<span class="task-status failed">已中断<\/span><button class="task-row-action" type="button" data-task-center-delete="finished-1">删除任务<\/button>/);
  ui.state.taskCenterDetail = {
    task: { task_id: 'running-1', label: '批量检测账号', status: 'running', total: 1, completed: 0, running: 1 },
    items: [], total: 0, item_total: 0, page: 1, page_size: 20, source: 'active'
  };
  ui.state.taskCenterSelectedID = 'running-1';
  ui.renderTaskDetail();
  assert.doesNotMatch(get('taskDetailBody').innerHTML, /data-task-center-cancel/);
  assert.match(html, /data-task-center-delete/);
  assert.match(html, /tasks\.center\.delete/);
});

test('Task center row actions confirm cancel and permanent deletion without selecting the row', async () => {
  const { dashboard: ui, get, mock, historyCalls } = harness();
  const calls = [];
  mock('api', async (action, params) => {
    calls.push({ action, params });
    if (action === 'tasks.center.list') {
      return {
        items: [], total: 0, page: 1, page_size: 20, counts: {}, resident_queues: [],
        history: { enabled: true, retention_days: 3, record_count: 0, archive_bytes: 0, max_bytes: 100 }
      };
    }
    return { ok: true };
  });
  const actionTarget = (selector, dataset) => {
    const control = new Element();
    Object.assign(control.dataset, dataset);
    const target = new Element();
    target.ancestors = new Map([[selector, control]]);
    return target;
  };

  const cancel = get('taskRows').emit('click', {
    target: actionTarget('[data-task-center-cancel]', { taskCenterCancel: 'running-1' })
  });
  await Promise.resolve();
  assert.equal(get('confirmDialog').hidden, false);
  await get('confirmAccept').emit('click');
  await cancel;
  assert.ok(calls.some((call) => call.action === 'tasks.center.cancel' && call.params.task_id === 'running-1'));
  assert.equal(ui.state.taskCenterSelectedID, '');

  ui.state.taskCenterSelectedID = 'finished-1';
  const remove = get('taskRows').emit('click', {
    target: actionTarget('[data-task-center-delete]', { taskCenterDelete: 'finished-1' })
  });
  await Promise.resolve();
  assert.match(get('confirmDetail').textContent, /永久删除/);
  await get('confirmAccept').emit('click');
  await remove;
  assert.ok(calls.some((call) => call.action === 'tasks.center.delete' && call.params.task_id === 'finished-1'));
  assert.equal(ui.state.taskCenterSelectedID, '');
  assert.equal(historyCalls.at(-1), '#tasks');
});

test('Task history settings explain defaults and convert MiB to bytes', () => {
  const { dashboard: ui, get } = harness();
  assert.match(html, /id="p_task_history_enabled"/);
  assert.match(html, /id="p_task_history_retention_days"[\s\S]*默认：3 天/);
  assert.match(html, /id="p_task_history_max_records"[\s\S]*默认：500 条/);
  assert.match(html, /id="p_task_history_max_bytes_mib"[\s\S]*默认：128 MiB/);
  assert.match(html, /id="p_task_history_max_items"[\s\S]*默认：10000 条/);
  ui.state.policy = { task_history: { enabled: true, retention_days: 3, max_records: 500, max_bytes: 64 * 1048576, max_items_per_task: 10000 } };
  ui.populatePolicyForm();
  assert.equal(Number(get('p_task_history_max_bytes_mib').value), 64);
  assert.equal(Number(get('p_task_history_retention_days').value), 3);
  get('p_task_history_max_bytes_mib').value = '96';
  get('p_task_history_retention_days').value = '7';
  get('p_task_history_max_records').value = '750';
  get('p_task_history_max_items').value = '20000';
  const policy = ui.collectPolicy();
  assert.equal(policy.task_history.max_bytes, 96 * 1048576);
  assert.equal(policy.task_history.retention_days, 7);
  assert.equal(policy.task_history.max_records, 750);
  assert.equal(policy.task_history.max_items_per_task, 20000);
});

test('Resident queues render only the returned enabled queues and detail can close', async () => {
  const { dashboard: ui, get, historyCalls } = harness();
  ui.renderResidentQueues([], { enabled: false, retention_days: 3, record_count: 0, archive_bytes: 0, max_bytes: 134217728 });
  assert.match(get('taskHistorySummary').textContent, /历史记录已关闭/);
  assert.match(get('residentQueues').innerHTML, /当前没有启用中的常驻队列/);
  ui.state.taskCenter = {
    items: [], total: 0, page: 1, page_size: 20, counts: {},
    history: { enabled: true, retention_days: 3, record_count: 0, archive_bytes: 0, max_bytes: 134217728 },
    resident_queues: [
      { id: 'auto_bind', label: '自动绑定队列', category: 'proxy', status: 'running', description: '扫描未绑定账号', total: 8, completed: 5, running: 1, waiting: 2, succeeded: 5, failed: 0 },
      { id: 'quality_probe', label: '质量检测队列', category: 'quality', status: 'running', description: '扫描到期账号', total: 8, completed: 5, running: 1, waiting: 2, succeeded: 5, failed: 0 }
    ]
  };
  ui.renderTaskCenter();
  assert.match(get('residentQueues').innerHTML, /自动绑定队列/);
  assert.match(get('residentQueues').innerHTML, /质量检测队列/);
  assert.match(get('residentQueues').innerHTML, /查看队列/);
  ui.selectTaskCenterQueue('auto_bind', true);
  assert.match(get('taskDetailBody').innerHTML, /打开自动绑定设置/);
  assert.equal(get('taskDetailCard').classList.contains('mobile-open'), true);
  ui.closeTaskCenterDetail(true);
  assert.equal(get('taskDetailCard').classList.contains('mobile-open'), false);
  assert.equal(historyCalls.at(-1), '#tasks');
  await Promise.resolve();
});
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
for (const { name, run } of cases) {
  await run();
  console.log('PASS ' + name);
}
console.log('Passed ' + cases.length + ' dashboard regressions; ' + scripts.length + ' script blocks parsed.');

if (process.argv.includes('--benchmark')) {
  const { dashboard: ui, groupCard } = harness();
  const rows = Array.from({ length: 100_000 }, (_, index) => ['vendor-' + index, 'base']);
  const group = ui.proxyGroupDraft({ id: 'large', columns: ['vendor', 'profile'], rows }, 0);
  group.childIndex = 1;
  const start = performance.now();
  ui.appendProxyGroupValues(group, ['vendor-new'], Array.from({ length: 10_000 }, (_, index) => 'profile-' + index));
  const elapsed = performance.now() - start;
  assert.equal(group.rows.length, 110_000);
  ui.state.proxyEditing = { groups: new Map([[0, group]]) };
  const card = groupCard(group); group.view = 'table'; ui.renderProxyGroup(0);
  const visible = (card.querySelector('[data-group-body]').innerHTML.match(/data-group-cell-row=/g) || []).length / 2;
  console.log('BENCH append 10,000 children to 100,000 rows: ' + elapsed.toFixed(1) + ' ms; visible rows: ' + visible + '.');
}
