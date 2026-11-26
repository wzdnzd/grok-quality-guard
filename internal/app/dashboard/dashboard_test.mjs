import assert from 'node:assert/strict';
import { performance } from 'node:perf_hooks';
import { cases, harness, scripts } from './test/support.mjs';
import './test/cases/i18n.mjs';
import './test/cases/loading-proxy.mjs';
import './test/cases/proxy-management.mjs';
import './test/cases/settings-forms.mjs';
import './test/cases/task-center.mjs';
import './test/cases/events-account.mjs';

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
