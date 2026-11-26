import assert from 'node:assert/strict';
import { Element, deferred, harness, html, localeResources, plain, test } from '../support.mjs';

test('Task center is the unified six-tab entry and supports task deep links', async () => {
  const { dashboard: ui, context, get, historyCalls, mock } = harness();
  assert.match(html, /data-tab="overview">ui.overview<\/button>[\s\S]*data-tab="accounts">ui.account<\/button>[\s\S]*data-tab="resin">ui.proxy_management<\/button>[\s\S]*data-tab="tasks">ui.task_center<\/button>[\s\S]*data-tab="events">ui.event_log<\/button>[\s\S]*data-tab="settings">ui.settings<\/button>/);
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

test('Resident queues render while the full task history request is still loading', async () => {
  const { dashboard: ui, get, mock } = harness();
  const history = deferred();
  const requests = [];
  mock('api', (action) => {
    requests.push(action);
    if (action === 'tasks.center.queues') {
      return Promise.resolve({
        resident_queues: [{ id: 'auto_bind', label: 'Auto bind queue', description: 'Proxy workers', status: 'running', total: 2, completed: 1, running: 1, waiting: 1, succeeded: 1, failed: 0 }]
      });
    }
    if (action === 'tasks.center.list') return history.promise;
    throw new Error('unexpected request ' + action);
  });

  ui.setTab('tasks', false);
  await new Promise((resolve) => setTimeout(resolve, 0));
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.ok(requests.includes('tasks.center.queues'));
  assert.ok(requests.includes('tasks.center.list'));
  assert.match(get('residentQueues').innerHTML, /Auto bind queue/);

  history.resolve({ items: [], total: 0, page: 1, page_size: 20, counts: {}, history: { enabled: true }, resident_queues: [] });
  await new Promise((resolve) => setTimeout(resolve, 0));
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
  assert.match(body, /操作失败，请稍后重试/);
  assert.doesNotMatch(body, /upstream failed/);
  assert.match(body, /已截断/);
  assert.equal(localeResources['zh-CN']['task.status.skipped'], '已跳过');
  assert.equal(localeResources['zh-CN']['task.status.probed'], '已检测');
});

test('Task center localizes every batch task kind and item status', () => {
  const taskKinds = {
    'accounts.probe': 'task.kind.accounts_probe',
    'accounts.probe_selected': 'task.kind.accounts_probe_selected',
    'accounts.restore_selected': 'task.kind.accounts_restore_selected',
    'accounts.disable_selected': 'task.kind.accounts_disable_selected',
    'quality.cross_verify': 'task.kind.quality_cross_verify',
    'proxy.auto_rotate': 'task.kind.proxy_auto_rotate',
    'proxy.bind': 'task.kind.proxy_bind',
    'proxy.clear': 'task.kind.proxy_clear',
    'proxy.rotate': 'task.kind.proxy_rotate',
    'proxy.adopt': 'task.kind.proxy_adopt',
    'proxy.bind_unbound': 'task.kind.proxy_bind_unbound',
    'proxy.template.apply': 'task.kind.proxy_template_apply',
    'proxy.source.create': 'task.kind.proxy_source_create',
    'proxy.source.update': 'task.kind.proxy_source_update',
    'proxy.source.delete': 'task.kind.proxy_source_delete',
    'proxy.sources.test_selected': 'task.kind.proxy_sources_test_selected',
    'proxy.sources.delete_selected': 'task.kind.proxy_sources_delete_selected',
    'proxy.change.retry': 'task.kind.proxy_change_retry'
  };
  const itemStatuses = [
    'succeeded', 'failed', 'skipped', 'probed', 'restored', 'external_restored',
    'external_left_disabled', 'disabled', 'reachable', 'unreachable', 'deleted',
    'bound', 'cleared', 'rotated', 'adopted', 'already_bound', 'dry_run',
    'external', 'drifted', 'unbound', 'unavailable', 'conflict', 'rotation_unavailable',
    'reconcile_required', 'commit_unknown', 'needs_reconcile', 'partial', 'superseded',
    'canceled', 'prepared', 'finalized', 'pending', 'unknown'
  ];
  const locales = ['zh-CN', 'zh-TW', 'en'];
  for (const locale of locales) {
    for (const key of Object.values(taskKinds)) {
      assert.equal(typeof localeResources[locale][key], 'string', `${locale} is missing ${key}`);
      assert.notEqual(localeResources[locale][key].trim(), '', `${locale} has an empty ${key}`);
    }
    for (const status of itemStatuses) {
      const key = 'task.status.' + status;
      assert.equal(typeof localeResources[locale][key], 'string', `${locale} is missing ${key}`);
      assert.notEqual(localeResources[locale][key].trim(), '', `${locale} has an empty ${key}`);
    }
  }
  assert.equal(localeResources['zh-CN']['task.kind.accounts_restore_selected'], '账号恢复');
  assert.equal(localeResources['zh-CN']['task.status.external_restored'], '已恢复外部禁用');
  assert.notEqual(localeResources['zh-CN']['task.status.unknown'], '待更新');
  const zh = harness({ languages: ['zh-CN'], language: 'zh-CN' }).dashboard;
  assert.equal(zh.taskKindLabel('accounts.restore_selected'), '账号恢复');
  assert.equal(zh.taskStatusLabel('external_restored'), '已恢复外部禁用');
  const traditional = harness({ languages: ['zh-TW'], language: 'zh-TW' }).dashboard;
  assert.equal(traditional.taskKindLabel('accounts.restore_selected'), '帳號恢復');
  assert.equal(traditional.taskStatusLabel('external_restored'), '已恢復外部停用');
  const english = harness({ languages: ['en-US'], language: 'en-US' }).dashboard;
  assert.equal(english.taskKindLabel('accounts.restore_selected'), 'Restore accounts');
  assert.equal(english.taskStatusLabel('external_restored'), 'Externally restored');
});

test('Task outcome counters cover every batch result shape without double counting', () => {
  const { dashboard: ui } = harness();
  assert.deepEqual(plain(ui.taskOutcomeCounts({
    kind: 'accounts.restore_selected', total: 3,
    result: { count: 3, restored: 1, external_restored: 1, external_left_disabled: 1, skipped: 0, failed: 0 }
  })), { total: 3, succeeded: 2, failed: 0, skipped: 1 });
  assert.deepEqual(plain(ui.taskOutcomeCounts({
    kind: 'accounts.probe_selected', total: 3,
    result: { count: 3, probed: 2, restored: 2, skipped: 1, failed: 0 }
  })), { total: 3, succeeded: 2, failed: 0, skipped: 1 });
  assert.deepEqual(plain(ui.taskOutcomeCounts({
    kind: 'accounts.disable_selected', total: 3,
    result: { count: 3, disabled: 2, skipped: 1, failed: 0 }
  })), { total: 3, succeeded: 2, failed: 0, skipped: 1 });
  assert.deepEqual(plain(ui.taskOutcomeCounts({
    kind: 'proxy.sources.test_selected', total: 3,
    result: { count: 3, counts: { total: 3, reachable: 2, unreachable: 1, failed: 0, pending: 0 }, reachable: 2, unreachable: 1, failed: 0 }
  })), { total: 3, succeeded: 2, failed: 1, skipped: 0 });
  assert.deepEqual(plain(ui.taskOutcomeCounts({
    kind: 'proxy.sources.delete_selected', total: 3,
    result: { count: 3, counts: { total: 3, deleted: 2, failed: 1, pending: 0 }, deleted: 2, failed: 1 }
  })), { total: 3, succeeded: 2, failed: 1, skipped: 0 });
  assert.deepEqual(plain(ui.taskOutcomeCounts({
    kind: 'proxy.bind', total: 4,
    result: { count: 4, counts: { total: 4, succeeded: 3, failed: 1, pending: 0 }, failed: 1 }
  })), { total: 4, succeeded: 3, failed: 1, skipped: 0 });
  assert.deepEqual(plain(ui.taskOutcomeCounts({
    kind: 'proxy.template.apply', total: 3,
    result: { count: 3, counts: { total: 3, succeeded: 2, failed: 0, pending: 1 } }
  })), { total: 3, succeeded: 2, failed: 0, skipped: 1 });
});

test('Completed restore tasks render localized type, item outcomes and derived counts', async () => {
  const { dashboard: ui, get, mock } = harness({ languages: ['zh-CN'], language: 'zh-CN' });
  const task = {
    task_id: 'restore-1', kind: 'accounts.restore_selected', category: 'account',
    status: 'succeeded', total: 2, completed: 2, succeeded: 0, failed: 0, skipped: 0,
    created_at: '2026-11-18T08:00:00Z', ended_at: '2026-11-18T08:00:01Z', duration_ms: 1000,
    result: { count: 2, restored: 0, external_restored: 2, external_left_disabled: 0, failed: 0, skipped: 0 }
  };
  ui.state.taskCenter = {
    items: [task], total: 1, page: 1, page_size: 20, counts: {}, resident_queues: [],
    history: { enabled: true, retention_days: 3, record_count: 1, archive_bytes: 10, max_bytes: 100 }
  };
  ui.renderTaskCenter();
  const rows = get('taskRows').innerHTML;
  assert.match(rows, /账号恢复/);
  assert.doesNotMatch(rows, /accounts\.restore_selected/);

  mock('api', async (action) => {
    assert.equal(action, 'tasks.center.get');
    return {
      task,
      items: [
        { auth_id: 'auth-1', status: 'external_restored' },
        { auth_id: 'auth-2', status: 'external_restored' }
      ],
      total: 2, item_total: 2, page: 1, page_size: 20, source: 'history'
    };
  });
  ui.state.taskCenterSelectedID = task.task_id;
  await ui.loadTaskCenterDetail(task.task_id, 1, 'all', false);
  const body = get('taskDetailBody').innerHTML;
  assert.match(body, /账号恢复/);
  assert.doesNotMatch(body, /accounts\.restore_selected/);
  assert.equal((body.match(/已恢复外部禁用/g) || []).length, 2);
  assert.doesNotMatch(body, /待更新/);
  assert.doesNotMatch(body, /external_restored/);
  assert.match(body, /共 2 项 · 成功 2 · 失败 0 · 跳过 0/);
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

  ui.state.taskCenter = {
    items: [{ task_id: 'finished-1', kind: 'accounts.restore_selected', status: 'succeeded' }],
    total: 1, page: 1, page_size: 20, counts: {}, resident_queues: [],
    history: { enabled: true, retention_days: 3, record_count: 1, archive_bytes: 10, max_bytes: 100 }
  };
  ui.state.taskCenterSelectedID = 'finished-1';
  const remove = get('taskRows').emit('click', {
    target: actionTarget('[data-task-center-delete]', { taskCenterDelete: 'finished-1' })
  });
  await Promise.resolve();
  assert.match(get('confirmDetail').textContent, /永久删除/);
  assert.match(get('confirmDetail').textContent, /账号恢复/);
  assert.doesNotMatch(get('confirmDetail').textContent, /accounts\.restore_selected/);
  await get('confirmAccept').emit('click');
  await remove;
  assert.ok(calls.some((call) => call.action === 'tasks.center.delete' && call.params.task_id === 'finished-1'));
  assert.equal(ui.state.taskCenterSelectedID, '');
  assert.equal(historyCalls.at(-1), '#tasks');
});

test('Task history settings explain defaults and convert MiB to bytes', () => {
  const { dashboard: ui, get } = harness();
  assert.match(html, /id="p_task_history_enabled"/);
  assert.match(html, /<label for="p_task_history_retention_days">[^<]+<\/label><span class="hint-icon"[^>]*aria-label="ui\.counted_from_task_completion_default_3_days"/);
  assert.match(html, /<label for="p_task_history_max_records">[^<]+<\/label><span class="hint-icon"[^>]*aria-label="ui\.evict_the_oldest_tasks_first_when_exceeded_default_500"/);
  assert.match(html, /<label for="p_task_history_max_bytes_mib">[^<]+<\/label><span class="hint-icon"[^>]*aria-label="ui\.compressed_disk_usage_of_completed_task_archives_in_mib"/);
  assert.match(html, /<label for="p_task_history_max_items">[^<]+<\/label><span class="hint-icon"[^>]*aria-label="ui\.keep_the_summary_and_mark_item_details_as_truncated"/);
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

test('Healthy account probe interval defaults to zero and round-trips configured hours', () => {
  const { dashboard: ui, get } = harness();
  assert.match(html, /id="p_probe_healthy_interval_hours"/);
  ui.state.policy = { probe: { healthy_interval_hours: 6 } };
  ui.populatePolicyForm();
  assert.equal(get('p_probe_healthy_interval_hours').value, '6');
  ui.state.policy.probe.healthy_interval_hours = 0;
  ui.populatePolicyForm();
  assert.equal(get('p_probe_healthy_interval_hours').value, '0');
  get('p_probe_healthy_interval_hours').value = '0';
  assert.equal(ui.collectPolicy().probe.healthy_interval_hours, 0);
  get('p_probe_healthy_interval_hours').value = '12';
  assert.equal(ui.collectPolicy().probe.healthy_interval_hours, 12);
  get('p_probe_healthy_interval_hours').value = '1.5';
  assert.ok(Number.isNaN(ui.collectPolicy().probe.healthy_interval_hours));
});
test('Task history summary is merged into the task center header on one line', () => {
  const { dashboard: ui, get } = harness();
  const queueMarkup = html.slice(html.indexOf('class="card task-queue-card"'), html.indexOf('<div class="task-center-grid">'));
  const taskHeaderMarkup = html.slice(html.indexOf('id="taskListTitle"'), html.indexOf('<div class="toolbar task-toolbar">'));
  assert.doesNotMatch(queueMarkup, /taskHistorySummary|configure_task_history/);
  assert.match(taskHeaderMarkup, /id="taskHistorySummary"/);
  assert.match(taskHeaderMarkup, /configure_task_history/);
  assert.match(html, /\.task-center-header \.task-center-summary\s*\{[\s\S]*?white-space:\s*nowrap/);
  ui.renderTaskCenterSummary({ enabled: true, retention_days: 3, record_count: 17, archive_bytes: 1024, max_bytes: 2048 });
  assert.equal(get('taskHistorySummary').textContent, '批量任务进度与历史 · 保留 3 天 · 17 条 · 1 KiB / 2 KiB');
  assert.equal(get('taskHistorySummary').title, get('taskHistorySummary').textContent);
  ui.renderTaskCenterSummary({ enabled: false, retention_days: 3, record_count: 0, archive_bytes: 0, max_bytes: 134217728 });
  assert.equal(get('taskHistorySummary').textContent, '批量任务进度与历史 · 历史已关闭');
});
test('Resident queue details expand inline and close without opening task details', async () => {
  const { dashboard: ui, get, mock } = harness();
  mock('api', async () => ({ queue_id: 'auto_bind', active: [], waiting: { items: [], total: 0, page: 1, page_size: 20 } }));
  ui.renderResidentQueues([]);
  assert.match(get('residentQueues').innerHTML, /当前没有启用中的常驻队列/);
  assert.match(html, /id="residentQueueDetail"[\s\S]*id="residentQueueDetailBody"[\s\S]*<div class="task-center-grid">/);
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
  assert.match(get('residentQueues').innerHTML, /id="residentQueueView-auto_bind"[^>]+aria-expanded="false"/);
  await ui.selectTaskCenterQueue('auto_bind');
  assert.match(get('residentQueueDetailBody').innerHTML, /打开自动绑定设置/);
  assert.match(get('residentQueues').innerHTML, /queue-card selected/);
  assert.match(get('residentQueues').innerHTML, /id="residentQueueView-auto_bind"[^>]+aria-expanded="true"/);
  assert.equal(get('residentQueueDetail').hidden, false);
  assert.equal(get('taskDetailCard').classList.contains('mobile-open'), false);
  ui.closeResidentQueueDetail(false);
  assert.equal(get('residentQueueDetail').hidden, true);
  assert.doesNotMatch(get('residentQueues').innerHTML, /queue-card selected/);
  await Promise.resolve();
});
test('Opening a resident queue leaves the selected task detail intact', async () => {
  const { dashboard: ui, get, mock } = harness();
  mock('api', async (action) => {
    if (action === 'tasks.center.queue') return { queue_id: 'auto_bind', active: [], waiting: { items: [], total: 0, page: 1, page_size: 20 } };
    throw new Error('unexpected API action: ' + action);
  });
  ui.state.taskCenter = {
    items: [{ task_id: 'task-1', kind: 'accounts.probe_selected', category: 'account', label: '批量检测账号', status: 'succeeded', total: 1, completed: 1, succeeded: 1 }],
    total: 1, page: 1, page_size: 20, counts: { active: 0, running_items: 0, failed_items: 0 },
    history: { enabled: true, retention_days: 3, record_count: 1, archive_bytes: 0, max_bytes: 134217728 },
    resident_queues: [{ id: 'auto_bind', label: '自动绑定队列', category: 'proxy', status: 'idle', description: '扫描未绑定账号', total: 0, completed: 0, running: 0, waiting: 0, succeeded: 0, failed: 0 }]
  };
  ui.state.taskCenterSelectedID = 'task-1';
  ui.state.taskCenterDetail = {
    task: { task_id: 'task-1', kind: 'accounts.probe_selected', category: 'account', label: '批量检测账号', status: 'succeeded', total: 1, completed: 1, succeeded: 1 },
    items: [{ auth_id: 'auth-1', status: 'succeeded' }], total: 1, item_total: 1, page: 1, page_size: 20, source: 'history'
  };
  ui.renderTaskCenter();
  assert.match(get('taskDetailBody').innerHTML, /task-1/);
  await ui.selectTaskCenterQueue('auto_bind');
  assert.equal(ui.state.taskCenterSelectedID, 'task-1');
  assert.match(get('taskDetailBody').innerHTML, /task-1/);
  assert.match(get('residentQueueDetailBody').innerHTML, /执行中的任务/);
});

