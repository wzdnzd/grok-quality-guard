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
      toggle: (value, enabled) => enabled ? this.classes.add(value) : this.classes.delete(value)
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
  const context = vm.createContext({
    document,
    window: {
      location: { hash: '#overview' },
      history: { replaceState() {} },
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
  vm.runInContext(source + '\n globalThis.dashboard = { state, formDrafts, proxyGroupDraft, appendProxyGroupValues, proxyRowsMatchPrefix, splitProxyEnum, parseProxyDelimitedTable, formatProxyDelimitedTable, alignProxyImportedRows, populatePolicyForm, populateResinForm, populateProxyAutoBindForm, clearFormDraft, formDraftRevision, loadList, loadAllAccounts, applyAccountListData, selectedAccountIDs, pagedItems, renderProxySources, syncProxySourceSelectionUI, proxySourceEnabledState, proxySourceConnectivity, listParams, testSelectedProxySources, deleteSelectedProxySources, renderProxyGroup, proxyTemplateConditionValues, previewProxyTemplate, proxyResultMessage, waitForTask, closeProxyDrawer, changeProxyEnumSeparator, detachProxyPlaceholderGroup, confirmProxyBindingOptions, renderProxyOperationSourceChoices, searchProxyOperationSourceChoices, renderProxyTaskDrawer, refreshProxyTasks, importProxyGroupRows, collectProxyTemplateGroups, collectProxyTemplateEditor, runProxyAccountBatches, normalizeBindingStatus, proxySummaryCounts, openProxyTemplateEditor, openResinEditor, resinInput, resinAccountFormatFromTemplate, endpointCell, leaseRow, toggleLeaseEndpoint, proxySourceRow, toggleSourceEndpoint, loadProxyTaskDetail, eventMatches, api, friendlyApiMessage, proxyImportLine, parseProxyImportText };', context);
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
  return { dashboard: context.dashboard, get, context, mock, placeholder, groupCard, document };
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

test('Periodic form population leaves dirty policy, Resin, and auto-bind values intact', async () => {
  const { dashboard: ui, get } = harness();
  ui.state.policy = { quality: { hard_tps: 800 } };
  ui.state.resin = { platform: 'Old', admin_token_set: true };
  ui.state.proxy = { auto_bind: { source_scope: 'nodes' } };
  ui.populatePolicyForm(); ui.populateResinForm(); ui.populateProxyAutoBindForm();
  get('p_hard_tps').value = '777';
  get('r_platform').value = 'Draft'; get('r_admin_token').value = 'new-secret';
  get('pab_source_scope').value = 'templates';
  await get('policyForm').emit('input');
  await get('resinForm').emit('input');
  await get('proxyAutoBindForm').emit('input');
  const revision = ui.formDraftRevision('resinForm');
  await get('resinForm').emit('input');
  assert.equal(ui.clearFormDraft('resinForm', revision), false);
  ui.state.policy = { quality: { hard_tps: 900 } };
  ui.state.resin = { platform: 'Server' };
  ui.state.proxy = { auto_bind: { source_scope: 'all' } };
  ui.populatePolicyForm(); ui.populateResinForm(); ui.populateProxyAutoBindForm();
  assert.equal(get('p_hard_tps').value, '777');
  assert.equal(get('r_platform').value, 'Draft');
  assert.equal(get('r_admin_token').value, 'new-secret');
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

test('A stale condition preview cannot overwrite newer inputs or validation state', async () => {
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
  mock('api', async () => ({ valid: true, samples: [{ masked_url: 'socks5h://proxy.example:1080', values: { vendor: 'B', profile: '<unsafe>' } }] }));
  await ui.previewProxyTemplate(editor, null, {}, new Element());
  assert.match(get('proxyTemplatePreviewResults').innerHTML, /vendor/);
  assert.match(get('proxyTemplatePreviewResults').innerHTML, /&lt;unsafe&gt;/);
});

test('Default password keep is omitted from template URL payloads', () => {
  const { dashboard: ui, get, placeholder } = harness();
  get('ptpl_name').value = 'region proxy';
  get('ptpl_url').value = 'socks5h://proxy-{country}-sid-{session}:test-pass@proxy.example:3010';
  get('ptpl_enabled').checked = true;
  get('ptpl_update_policy').value = 'keep_bound';
  get('ptpl_password_action').value = 'keep';
  ui.state.proxyEditing = { groups: new Map() };
  placeholder('country', { separator: ',', input: 'US,JP,SG' });
  placeholder('session', { kind: 'random' });

  const source = ui.collectProxyTemplateEditor(null, {});
  assert.equal(source.password_update, undefined);
  assert.equal(Object.hasOwn(JSON.parse(JSON.stringify(source)), 'password_update'), false);

  get('ptpl_password_action').value = 'set';
  get('ptpl_password_value').value = 'new-pass';
  assert.deepEqual(plain(ui.collectProxyTemplateEditor(null, {}).password_update), { action: 'set', value: 'new-pass' });
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
  assert.equal(ui.eventMatches({ action: 'resin_rotate' }), true);
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

test('Binding source candidates follow the selected source scope', async () => {
  const { dashboard: ui, get, mock } = harness();
  ui.state.proxySources = [
    { id: 'node-1', name: 'Node one', kind: 'node' },
    { id: 'template-1', name: 'Template one', kind: 'template' },
    { id: 'resin-1', name: 'Resin one', kind: 'resin' }
  ];
  ui.state.proxyOperationSourceChoices = [];
  ui.renderProxyOperationSourceChoices('templates');
  let candidateHTML = get('proxyOperationSourceChoices').innerHTML;
  assert.match(candidateHTML, /value="template-1"/);
  assert.doesNotMatch(candidateHTML, /value="node-1"|value="resin-1"/);

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
  assert.match(candidateHTML, /value="template-2"/);
  assert.doesNotMatch(candidateHTML, /value="node-2"/);

  await ui.searchProxyOperationSourceChoices('', 'all');
  candidateHTML = get('proxyOperationSourceChoices').innerHTML;
  assert.equal(Object.hasOwn(requests[1].params, 'kind'), false);
  assert.match(candidateHTML, /value="template-2"/);
  assert.match(candidateHTML, /value="node-2"/);
  assert.match(html, /\.confirm-card\.wide\s*\{/);
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

test('Change details request their next page instead of expanding a million items', async () => {
  const { dashboard: ui, get, mock } = harness();
  const requests = [];
  ui.state.proxyEditing = { kind: 'tasks', taskDetailPage: 1, taskDetail: { change_id: 'change', page: 1, page_size: 20, total: 1_000_000, counts: { total: 1_000_000 }, items: [] } };
  mock('api', async (action, params) => {
    requests.push({ action, params });
    return { change_id: 'change', page: params.page, page_size: 20, total: 1_000_000, counts: { total: 1_000_000, pending: 1_000_000 }, items: Array.from({ length: 20 }, (_, index) => ({ auth_id: 'auth-' + (20 + index), status: 'pending' })) };
  });
  const next = new Element();
  next.dataset.taskDetailPage = '1';
  next.ancestors = new Map([['button', next]]);
  await get('proxySourceEditor').emit('click', { target: next });
  assert.equal(requests.length, 1);
  assert.equal(requests[0].action, 'proxy.changes.get');
  assert.equal(requests[0].params.page, 2);
  assert.equal(requests[0].params.page_size, 20);
  assert.equal((get('proxySourceEditor').innerHTML.match(/<td>auth-/g) || []).length, 20);
  assert.match(get('proxySourceEditor').innerHTML, /第 2 \/ 50000 页/);
});

test('Cleanup polling and paging retain bounded twenty-item responses', async () => {
  const { dashboard: ui, get, mock } = harness();
  const requests = [];
  ui.state.proxyEditing = { kind: 'tasks' };
  mock('api', async (action, params) => {
    requests.push({ action, params });
    if (action !== 'resin.cleanup.list') return { items: [] };
    return { total: 1_000_000, page: params.page, page_size: 20, items: Array.from({ length: 20 }, (_, index) => ({ id: 'cleanup-' + index, auth_id: 'cleanup-auth-' + index, account: 'preview-' + index, status: 'pending', attempts: 0 })) };
  });
  const next = new Element();
  next.dataset.cleanupPage = '1';
  next.ancestors = new Map([['button', next]]);
  await get('proxySourceEditor').emit('click', { target: next });
  const cleanup = requests.filter((request) => request.action === 'resin.cleanup.list');
  assert.equal(cleanup.length, 1);
  assert.equal(cleanup[0].params.page, 2);
  assert.equal(cleanup[0].params.page_size, 20);
  assert.equal((get('proxySourceEditor').innerHTML.match(/data-cleanup-retry=/g) || []).length, 20);
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

test('Current and legacy binding statuses keep the same visible metrics', () => {
  const { dashboard: ui } = harness();
  assert.equal(ui.normalizeBindingStatus('managed'), 'bound');
  assert.equal(ui.normalizeBindingStatus('needs_reconcile'), 'reconcile_required');
  assert.equal(ui.normalizeBindingStatus('unknown'), 'unknown');
  const counts = ui.proxySummaryCounts({ binding_counts: { managed: 12, needs_reconcile: 3, external: 2 } });
  assert.equal(counts.bound, 12);
  assert.equal(counts.reconcile_required, 3);
  assert.equal(counts.external, 2);
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

test('A dirty Resin form retains its original version when the server changes', async () => {
  const { dashboard: ui, get } = harness();
  ui.state.resin = { config_version: 5, platform: 'Original' };
  ui.populateResinForm();
  get('r_platform').value = 'Draft';
  await get('resinForm').emit('input');
  ui.state.resin = { config_version: 8, platform: 'Other administrator' };
  ui.populateResinForm();
  assert.equal(ui.resinInput().platform, 'Draft');
  assert.equal(ui.resinInput().expected_version, 5);
});

test('Resin form edits only the username account format', () => {
  const { dashboard: ui, get } = harness();
  assert.equal(ui.resinAccountFormatFromTemplate('socks5h://CPA.grok-{account}:secret@resin:2260', 'CPA'), 'grok-{account}');
  ui.state.resin = { config_version: 5, name: 'Primary Resin', account_format: 'grok-{account}' };
  ui.populateResinForm();
  assert.equal(get('r_name').value, 'Primary Resin');
  assert.equal(get('r_account_format').value, 'grok-{account}');
  get('r_account_format').value = 'svc-{account}';
  get('r_admin_url').value = 'https://resin.example:8443/base';
  const input = ui.resinInput();
  assert.equal(input.name, 'Primary Resin');
  assert.equal(input.account_format, 'svc-{account}');
  assert.equal(Object.hasOwn(input, 'proxy_template'), false);
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

test('Proxy source enabled and connectivity statuses stay independent', () => {
  const { dashboard: ui } = harness();
  const row = ui.proxySourceRow({
    id: 'node-1', kind: 'node', name: 'Node one', enabled: true, valid: true,
    health: 'unhealthy', health_checked: '2026-11-17T10:00:00Z', binding_count: 0
  });
  assert.match(row, /已启用/);
  assert.match(row, /不可达/);
  assert.doesNotMatch(row, />可用</);
  assert.match(row, /仅表示最近一次通过网络代理建立测试目标隧道的检测结果/);

  const templateRow = ui.proxySourceRow({
    id: 'template-1', kind: 'template', name: 'Region template', enabled: true, valid: true,
    health: 'healthy', health_checked: '2026-11-17T10:00:00Z', binding_count: 0
  });
  assert.match(templateRow, /可连通/);
  assert.match(templateRow, /已启用/);
  assert.doesNotMatch(templateRow, /样本可连通/);

  const unknown = ui.proxySourceConnectivity({ id: 'template-2', kind: 'template', enabled: true, valid: true, health_checked: '0001-01-01T00:00:00Z' });
  assert.match(unknown, /未检测/);
  assert.match(unknown, /检测时生成临时端点/);
  assert.doesNotMatch(unknown, /检测：-/);
  assert.doesNotMatch(unknown, /不适用|无法检测|样本/);

  const localResin = ui.proxySourceConnectivity({ id: 'resin', kind: 'resin', enabled: true, valid: true, local_only: true });
  assert.match(localResin, /未检测/);
  assert.match(localResin, /本地轮换无独立端点/);
  assert.doesNotMatch(localResin, /不适用|无法检测/);
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
  assert.equal(get('proxySourceSelectionCount').textContent, '已选：1 个来源');
  assert.equal(get('proxySourceTestSelectedBtn').disabled, false);
  assert.equal(get('proxySourceDeleteSelectedBtn').disabled, false);
  assert.match(get('proxySourceRows').innerHTML, /data-source-select="node-1"/);
  assert.match(get('proxySourceRows').innerHTML, /data-source-select="node-1" aria-label="选择来源" checked/);
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

test('A late task-detail refresh cannot restore an older selected page', async () => {
  const { dashboard: ui, mock } = harness();
  const old = deferred(), fresh = deferred();
  let requests = 0;
  mock('api', () => (++requests === 1 ? old.promise : fresh.promise));
  const editor = { kind: 'tasks' };
  ui.state.proxyEditing = editor;
  const first = ui.loadProxyTaskDetail(editor, 'change', 'change', 1);
  const second = ui.loadProxyTaskDetail(editor, 'change', 'change', 2);
  fresh.resolve({ change_id: 'change', page: 2, page_size: 20, total: 100, items: [] });
  assert.equal(await second, true);
  old.resolve({ change_id: 'change', page: 1, page_size: 20, total: 100, items: [] });
  assert.equal(await first, false);
  assert.equal(editor.taskDetailPage, 2);
});

test('Checkbox controls reserve a label line and retry fields share one row', () => {
  assert.match(html, /\.field>\.check-row:first-child\s*\{\s*margin-top: 20px;/);
  assert.match(html, /\.settings-group \.retry-grid\s*\{\s*grid-template-columns: repeat\(3, minmax\(0, 1fr\)\);/);
  assert.match(html, /class="field-grid retry-grid"/);
  assert.match(html, /首次失败后的等待时长/);
  assert.match(html, /退避等待的最长时间/);
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
  assert.match(html, /仅选择健康且未过期的来源/);
  assert.match(html, /:password@proxy\.example:1080/);
  assert.doesNotMatch(html, /:密码@proxy\.example:1080/);
  assert.match(html, /\.check-card\.compact\s*\{[^}]*min-height: 33px;[^}]*margin-top: 20px;/s);
  assert.match(html, /class="check-card compact" title="健康账号不做周期探测"><input id="p_probe_recovery_only"/);
  assert.doesNotMatch(html, /class="check-desc">健康账号不做周期探测/);
  assert.match(html, /默认显示脱敏端点，点击眼睛查看完整端点/);
  assert.match(html, /<th>端点<\/th>/);
  assert.match(html, /for="r_account_format">账号格式/);
  assert.match(html, /for="r_name">来源名称/);
  assert.match(html, /无需填写 \{platform\}；只填写 username 后缀，例如 \{account\}。系统会拼成 socks5h:\/\/\{platform\}\.&lt;填写内容&gt;:\{proxy_token\}@&lt;Admin API 主机&gt;:&lt;端口&gt;/);
  assert.doesNotMatch(html, /for="r_proxy_template">模板/);
});

test('Network and management API failures use actionable Chinese copy', async () => {
  const { dashboard: ui, context, mock, get } = harness();
  context.fetch = async () => { throw new TypeError('Failed to fetch'); };
  await assert.rejects(ui.api('overview'), /无法连接管理接口，请检查 CPA 服务和插件状态/);
  mock('api', async () => { throw new Error('配置读取失败'); });
  await ui.openResinEditor();
  assert.equal(get('proxySourceDrawer').hidden, false);
  assert.equal(get('resinReloadBtn').hidden, false);
  assert.equal(get('resinSaveBtn').disabled, true);
  assert.match(get('resinStatus').textContent, /配置读取失败/);
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


