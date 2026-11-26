import assert from 'node:assert/strict';
import { Element, deferred, harness, html, localeResources, plain, test } from '../support.mjs';

test('Interface language follows browser locale, persisted choice, and supported fallbacks', () => {
  let ui = harness({ languages: ['zh-Hant-TW', 'en-US'], language: 'zh-Hant-TW' }).dashboard;
  assert.equal(ui.i18n.locale, 'zh-TW');
  ui = harness({ languages: ['zh-HK'], language: 'zh-HK' }).dashboard;
  assert.equal(ui.i18n.locale, 'zh-TW');
  ui = harness({ languages: ['zh-Hans-CN'], language: 'zh-Hans-CN' }).dashboard;
  assert.equal(ui.i18n.locale, 'zh-CN');
  ui = harness({ languages: ['en-GB'], language: 'en-GB' }).dashboard;
  assert.equal(ui.i18n.locale, 'en');
  ui = harness({ languages: ['fr-FR'], language: 'fr-FR' }).dashboard;
  assert.equal(ui.i18n.locale, 'zh-CN');
});

test('Language switching updates document language, persistence, interpolation, and source fallback', () => {
  const { dashboard: ui, document, storageWrites } = harness({ languages: ['zh-CN'], language: 'zh-CN' });
  assert.equal(ui.i18n.applyLocale('en', { persist: true }), 'en');
  assert.equal(document.documentElement.lang, 'en');
  assert.equal(storageWrites.at(-1).value, 'en');
  assert.equal(ui.i18n.detectLocale(), 'en');
  assert.equal(ui.i18n.translateSource('账号详情'), 'Account details');
  assert.equal(ui.i18n.t('count.accounts', { count: 2 }), '2 accounts');
  assert.equal(ui.i18n.applyLocale('zh-TW'), 'zh-TW');
  assert.equal(document.documentElement.lang, 'zh-TW');
  assert.equal(ui.i18n.translateSource('账号详情'), '帳號詳情');
  assert.equal(ui.i18n.t('ui.account_details'), '帳號詳情');
  assert.equal(ui.i18n.applyLocale('zh-CN'), 'zh-CN');
  assert.equal(ui.i18n.translateSource('账号详情'), '账号详情');
});

test('Language switching reinterprets already-localized source text', () => {
  const { dashboard: ui } = harness({ languages: ['zh-TW'], language: 'zh-TW' });
  assert.equal(ui.i18n.translateLocalizedSource('ui.overview'), '概覽');
  assert.equal(ui.i18n.applyLocale('en'), 'en');
  assert.equal(ui.i18n.translateLocalizedSource('概覽'), 'Overview');
  assert.equal(ui.i18n.translateLocalizedSource('介面語言'), 'Interface language');
  assert.equal(ui.i18n.applyLocale('zh-CN'), 'zh-CN');
  assert.equal(ui.i18n.translateLocalizedSource('Interface language'), '界面语言');
  assert.equal(ui.i18n.translateLocalizedSource('Overview'), '概览');
});

test('Embedded locale data localizes the page without a fallback request', () => {
  const { dashboard: ui, get } = harness({
    languages: ['en-US'],
    language: 'en-US',
    fetch: async () => { throw new Error('locale fallback should not be requested'); }
  });
  assert.equal(ui.i18n.hasResources(), true);
  assert.equal(ui.i18n.t('ui.overview'), 'Overview');
  assert.equal(ui.i18n.t('language.zh_cn'), 'Simplified Chinese');
  assert.equal(get('uiLanguage').value, 'en');
});

test('Missing inline locale resources hydrate from the management endpoint', async () => {
  const requests = [];
  const { dashboard: ui, get } = harness({
    languages: ['en-US'],
    language: 'en-US',
    i18nResources: {},
    fetch: async (url, init) => {
      requests.push({ url, init });
      return { ok: true, status: 200, json: async () => localeResources };
    }
  });
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(requests.length, 1);
  assert.equal(ui.i18n.hasResources(), true);
  assert.equal(requests[0].url, '/v0/management/plugins/grok-quality-guard/locales');
  assert.equal(requests[0].init.headers['Accept-Language'], 'en');
  assert.equal(ui.i18n.t('ui.overview'), 'Overview');
  assert.equal(ui.i18n.t('language.zh_cn'), 'Simplified Chinese');
  assert.equal(get('uiLanguage').value, 'en');
});

test('Scoring weight hints stay on one line without parentheses or trailing punctuation', () => {
  for (const locale of ['zh-CN', 'zh-TW', 'en']) {
    for (const key of ['ui.score_probe_hint', 'ui.score_transparent_hint', 'ui.score_passive_hint']) {
      const copy = localeResources[locale][key];
      assert.doesNotMatch(copy, /[\r\n]/, `${locale} ${key} must stay on one line`);
      assert.doesNotMatch(copy, /[（）()]/, `${locale} ${key} must not use parentheses`);
      assert.doesNotMatch(copy, /[。；，、：！？,;:!?]$/, `${locale} ${key} must not end with punctuation`);
    }
  }
});

test('Locale resources keep matching keys and interpolation placeholders', () => {
  const baseKeys = Object.keys(localeResources.en).sort();
  for (const locale of ['zh-CN', 'zh-TW']) {
    assert.deepEqual(Object.keys(localeResources[locale]).sort(), baseKeys, `${locale} key set differs from en`);
  }
  const placeholders = (value) => [...String(value).matchAll(/\{([a-zA-Z0-9_]+)(?=[:}])/g)].map((match) => match[1]).sort();
  for (const key of baseKeys) {
    const expected = placeholders(localeResources.en[key]);
    for (const locale of ['zh-CN', 'zh-TW']) {
      assert.deepEqual(placeholders(localeResources[locale][key]), expected, `${locale} placeholders differ for ${key}`);
    }
  }
});
test('English rendering paths do not leak Simplified Chinese copy', () => {
  const { dashboard: ui, get } = harness({ languages: ['en-US'], language: 'en-US' });
  ui.state.overview = {
    enabled: true,
    auth_count: 3,
    states: { healthy: 2, suspect: 1, degraded: 0, cooling: 0, disabled_by_guard: 0 },
    policy: { transparent_guard: false, passive_guard: true }
  };
  ui.state.policy = ui.state.overview.policy;
  ui.state.accountsTotal = 3;
  ui.state.accountPageSize = 50;
  ui.state.accountPage = 1;
  ui.state.accountList = { total: 3, page: 1, page_size: 50 };
  ui.state.accounts = [
    { auth_id: 'auth-1', display: 'a@example.com', state: 'healthy', proxy_status: 'bound', proxy_generation: 2, output_tokens: 10, reasoning_tokens: 5, encrypted_bytes: 12 },
    { auth_id: 'auth-2', display: 'b@example.com', state: 'cooling', cooldown_until: '2026-11-18T10:00:00Z', proxy_status: 'unbound' }
  ];
  ui.state.proxy = {
    binding_total: 2,
    counts: { bound: 1, unbound: 1, external: 0, unknown: 0, drifted: 0, reconcile_required: 0 },
    source_count: 1,
    available_source_count: 1,
    source_counts: { node: 1, template: 0, resin: 0 },
    auto_bind: { enabled: true },
    auto_bind_runtime: { last_scan_at: '2026-11-17T05:17:13Z', bound_total: 1, failed_total: 0, skipped_total: 0, excluded_count: 0 }
  };
  ui.state.proxySources = [{ id: 'node-1', kind: 'node', name: 'Node one', enabled: true, valid: true, endpoint: 'socks5h://u***@host:1080', binding_count: 1 }];
  ui.state.proxySourceList = { total: 1, page: 1, page_size: 50 };
  ui.state.leases = [{ auth_id: 'auth-1', display: 'a@example.com', proxy_status: 'bound', proxy: 'socks5h://u***@host:1080' }];
  ui.state.leaseList = { total: 1, page: 1, page_size: 50 };
  ui.state.events = [{ time: '2026-11-18T08:00:00Z', auth_id: 'auth-1', action: 'proxy_bind', result: 'bound', detail: 'source=node-1 account=lease-1 remote_lease=delete_queued' }];
  ui.state.eventList = { total: 1, page: 1, page_size: 50 };
  ui.state.taskCenter = {
    items: [{ task_id: 'task-1', kind: 'accounts.probe_selected', category: 'account', label: '批量检测账号', status: 'running', total: 2, completed: 1, running: 1, waiting: 0 }],
    total: 1, page: 1, page_size: 20,
    counts: { active: 1, running_items: 1, failed_items: 0 },
    history: { enabled: true, retention_days: 3, record_count: 1, archive_bytes: 1024, max_bytes: 2048 },
    resident_queues: [{ id: 'auto_bind', label: '自动绑定队列', description: '自动绑定账号', status: 'idle', total: 0, completed: 0, running: 0, waiting: 0, succeeded: 0, failed: 0 }]
  };

  ui.renderOverview();
  ui.renderAccounts();
  ui.renderResin();
  ui.renderProxySources();
  ui.renderEvents();
  ui.renderTaskCenter();
  ui.renderProxyAutoBind();
  const rendered = ['overviewMetrics', 'accountMetrics', 'accountRows', 'accountsCount', 'resinMetrics', 'resinNotice', 'proxySourceRows', 'proxySourceSummary', 'eventRows', 'eventsCount', 'taskMetrics', 'taskRows', 'residentQueues', 'proxyAutoBindText']
    .map((id) => `${id}: ${get(id).innerHTML || ''}\n${get(id).textContent || ''}\n${get(id).title || ''}`)
    .join('\n');
  const leftovers = [...rendered.matchAll(/[^\n]*[\u3400-\u9fff][^\n]*/g)].map((match) => match[0].trim());
  assert.deepEqual(leftovers, []);
});

