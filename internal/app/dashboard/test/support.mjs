import fs from 'node:fs';
import vm from 'node:vm';

const localeResources = Object.fromEntries(['zh-CN', 'zh-TW', 'en'].map((locale) => [
  locale,
  JSON.parse(fs.readFileSync(new URL(`../../i18n/locales/${locale}.json`, import.meta.url), 'utf8'))
]));
let html = fs.readFileSync(new URL('../dashboard.html', import.meta.url), 'utf8');
const manifest = JSON.parse(fs.readFileSync(new URL('../parts.json', import.meta.url), 'utf8'));
const i18nResourceMarker = 'const I18N_RESOURCE_DATA = null; // i18n-resource-data';

function readDashboardParts(files) {
  return files
    .map((name) => fs.readFileSync(new URL(`../${name}`, import.meta.url), 'utf8'))
    .join('\n');
}

html = html
  .replace('/* __DASHBOARD_CSS__ */', () => readDashboardParts(manifest.css))
  .replace('// __DASHBOARD_JS__', () => readDashboardParts(manifest.js));
const documentHtml = html.replace(i18nResourceMarker, () => `const I18N_RESOURCE_DATA = ${JSON.stringify(localeResources)}; // i18n-resource-data`);
const scripts = [...documentHtml.matchAll(/<script(?![^>]*type="application\/json")(?:[^>]*)>([\s\S]*?)<\/script>/g)].map((match) => match[1]);
for (const script of scripts) new vm.Script(script);
const source = scripts.find((script) => script.includes('const API ='))
  .replace(/\s+applyLocationHash\(\);\s*(?:refreshAll\(true\);\s*)?$/, '');

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

function harness(options = {}) {
  const elements = new Map();
  const cards = new Map();
  const groups = new Map();
  const storage = new Map(Object.entries(options.storage || {}));
  const storageWrites = [];
  const localStorage = {
    getItem(key) { return storage.has(key) ? storage.get(key) : null; },
    setItem(key, value) { storage.set(key, String(value)); storageWrites.push({ key, value: String(value) }); },
    removeItem(key) { storage.delete(key); }
  };
  const get = (id) => {
    if (!elements.has(id)) elements.set(id, new Element(id));
    return elements.get(id);
  };
  for (const id of ['proxySourceDrawer', 'accountDrawer', 'confirmDialog']) get(id).hidden = true;
  const document = {
    body: new Element('body'),
    documentElement: new Element('html'),
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
  const capturedTimers = new Map();
  let nextTimerID = 0;
  const scheduleTimer = (callback, duration) => {
    if (options.captureTimers) {
      const timerID = ++nextTimerID;
      capturedTimers.set(timerID, callback);
      return timerID;
    }
    if (duration === 0 || duration === 1000) queueMicrotask(callback);
    return 1;
  };
  const clearTimer = (timerID) => { capturedTimers.delete(timerID); };
  const runTimers = () => {
    const callbacks = [...capturedTimers.values()];
    capturedTimers.clear();
    callbacks.forEach((callback) => callback());
  };
  const context = vm.createContext({
    document,
    navigator: {
      languages: options.languages || [],
      language: options.language || ''
    },
    window: {
      location: { hash: '#overview' },
      history: { replaceState(_state, _title, hash) { historyCalls.push(hash); } },
      localStorage,
      addEventListener() {},
      clearTimeout: clearTimer,
      setInterval() {},
      setTimeout: scheduleTimer
    },
    HTMLElement: Element,
    CSS: { escape: (value) => String(value) },
    Blob, URL, TextEncoder, AbortController, Date, Intl, JSON, Map, Set,
    fetch: options.fetch || (async () => { throw new Error('Unexpected network request'); })
  });
  const runtimeSource = options.i18nResources === undefined
    ? source
    : source.replace(/const I18N_RESOURCE_DATA = [^\n]*; \/\/ i18n-resource-data/, `const I18N_RESOURCE_DATA = ${JSON.stringify(options.i18nResources)}; // i18n-resource-data`);
  vm.runInContext(runtimeSource + '\n globalThis.dashboard = { i18n, state, formDrafts, proxyGroupDraft, appendProxyGroupValues, proxyRowsMatchPrefix, splitProxyEnum, parseProxyDelimitedTable, formatProxyDelimitedTable, alignProxyImportedRows, populatePolicyForm, populateProxyAutoBindForm, clearFormDraft, formDraftRevision, loadList, loadAllAccounts, applyAccountListData, ensureList, ensureTabData, startListLoad, ensureProxySummary, renderTabSurface, mergeBindingItems, mergeBindingForAccount, renderProxySourceChoices, leaseStatus, selectedAccountIDs, pagedItems, renderOverview, renderAccounts, renderProxySources, renderResin, syncProxySourceSelectionUI, proxySourceAllCount, loadAllProxySourceIDs, selectProxySourceScope, proxySourceEnabledState, proxySourceConnectivity, listParams, testSelectedProxySources, deleteSelectedProxySources, renderProxyGroup, proxyTemplateConditionValues, previewProxyTemplate, proxyResultMessage, waitForTask, closeProxyDrawer, changeProxyEnumSeparator, detachProxyPlaceholderGroup, confirmProxyBindingOptions, renderProxyOperationSourceChoices, searchProxyOperationSourceChoices, chooseProxyOperationSource, proxyOperationSourceValue, importProxyGroupRows, collectProxyTemplateGroups, collectProxyTemplateEditor, runProxyAccountBatches, runSelectedProxyAction, renderProxyAutoBind, normalizeBindingStatus, proxySummaryCounts, proxyNeedsReviewCount, accountRow, renderDrawer, openProxyNodeEditor, openProxyTemplateEditor, openProxyResinEditor, openProxyImportEditor, collectProxyResinEditor, previewProxyResin, endpointCell, leaseRow, toggleLeaseEndpoint, proxySourceRow, toggleSourceEndpoint, eventMatches, renderEvents, api, friendlyApiMessage, saveProxyAutoBind, setProxyAutoBindSettingsOpen, proxyImportLine, parseProxyImportText, cleanHelpCopy, proxyNodeRowHTML, renderTaskCenter, refreshTaskCenter, refreshTaskCenterQueues, selectTaskCenterTask, selectTaskCenterQueue, closeTaskCenterDetail, closeResidentQueueDetail, renderResidentQueues, loadTaskCenterDetail, loadTaskQueueDetail, openTaskCenterChange, renderTaskDetail, renderTaskQueueDetail, renderTaskPager, taskHistoryText, taskCenterSummaryText, renderTaskCenterSummary, taskKindLabel, taskStatusLabel, taskOutcomeCounts, taskResultSummary, collectPolicy, populatePolicyForm, applyLocationHash, setTab, refreshAll, refreshAccounts, ensureAccountData, ensureProxyData, loadProxyData, renderAll, scheduleAccountMetadataRefresh, refreshResin, refreshEvents };', context);
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
  return { dashboard: context.dashboard, get, context, mock, placeholder, groupCard, document, historyCalls, storage, storageWrites, runTimers, capturedTimers };
}

const plain = (value) => JSON.parse(JSON.stringify(value));
const deferred = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const cases = [];
const test = (name, run) => cases.push({ name, run });

export { Element, harness, plain, deferred, html, localeResources, scripts, cases, test };
