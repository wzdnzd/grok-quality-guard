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
    Blob, URL, Date, Intl, JSON, Map, Set,
    fetch: async () => { throw new Error('Unexpected network request'); }
  });
  vm.runInContext(source + '\n globalThis.dashboard = { state, formDrafts, proxyGroupDraft, appendProxyGroupValues, proxyRowsMatchPrefix, splitProxyEnum, parseProxyDelimitedTable, formatProxyDelimitedTable, alignProxyImportedRows, populatePolicyForm, populateResinForm, populateProxyAutoBindForm, clearFormDraft, formDraftRevision, loadList, loadAllAccounts, applyAccountListData, selectedAccountIDs, pagedItems, renderProxySources, renderProxyGroup, proxyTemplateConditionValues, previewProxyTemplate, proxyResultMessage, waitForTask, closeProxyDrawer, changeProxyEnumSeparator, detachProxyPlaceholderGroup, confirmProxyBindingOptions, renderProxyTaskDrawer, refreshProxyTasks, importProxyGroupRows, collectProxyTemplateGroups, runProxyAccountBatches, normalizeBindingStatus, proxySummaryCounts, openProxyTemplateEditor, resinInput, loadProxyTaskDetail };', context);
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

test('Partial sync results distinguish committed failures and skipped accounts', async () => {
  const { dashboard: ui, mock } = harness();
  const result = { saved: true, counts: { total: 3, succeeded: 1, failed: 1, pending: 0 }, items: [{ auth_id: 'a', status: 'succeeded' }, { auth_id: 'b', status: 'needs_reconcile', committed: true, needs_reconcile: true }, { auth_id: 'c', status: 'skipped' }] };
  const summary = ui.proxyResultMessage(result, 'Node');
  assert.equal(summary.failed, 1);
  assert.match(summary.message, /配置已保存.*成功 1.*跳过 1.*失败 1/);
  mock('api', async () => ({ task_id: 'partial', status: 'succeeded', result }));
  assert.equal((await ui.waitForTask({ task_id: 'partial', status: 'queued' }, 'Sync')).saved, true);
  assert.equal(ui.state.proxyTasks.get('partial').result.counts.failed, 1);
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
