    function taskHistoryText(history) {
      const value = history || {};
      if (value.enabled === false) return i18n.t('ui.history_disabled_only_current');
      return i18n.t('ui.task_history_summary', { days: Number(value.retention_days || 3), records: Number(value.record_count || 0), used: formatBytes(value.archive_bytes || 0), max: formatBytes(value.max_bytes || 0) });
    }
    function taskCenterSummaryText(history) {
      const value = history || {};
      if (value.enabled === false) return i18n.t('ui.task_center_summary_disabled');
      return i18n.t('ui.task_center_summary', { days: Number(value.retention_days || 3), records: Number(value.record_count || 0), used: formatBytes(value.archive_bytes || 0), max: formatBytes(value.max_bytes || 0) });
    }
    function renderTaskCenterSummary(history) {
      const element = document.getElementById('taskHistorySummary');
      if (!element) return;
      const text = taskCenterSummaryText(history);
      element.textContent = text;
      element.title = text;
    }
    function taskCount(value) {
      const count = Number(value ?? 0);
      return Number.isFinite(count) && count > 0 ? count : 0;
    }
    function taskOutcomeCounts(task) {
      const value = task && typeof task === 'object' ? task : {};
      const result = value.result && typeof value.result === 'object' ? value.result : {};
      const counts = result.counts && typeof result.counts === 'object' ? result.counts : {};
      let succeeded = taskCount(value.succeeded);
      let failed = taskCount(value.failed);
      let skipped = taskCount(value.skipped);
      if (succeeded + failed + skipped === 0) {
        // Older history summaries can have zero generic counters even though
        // their domain result recorded the real outcome. Reconstruct the counts
        // without treating subset fields (for example probe.result.restored) as
        // additional successful items.
        switch (String(value.kind || '')) {
          case 'accounts.restore_selected':
            succeeded = taskCount(result.restored) + taskCount(result.external_restored);
            failed = taskCount(result.failed);
            skipped = taskCount(result.skipped) + taskCount(result.external_left_disabled);
            break;
          case 'accounts.probe_selected':
            succeeded = taskCount(result.probed);
            failed = taskCount(result.failed);
            skipped = taskCount(result.skipped);
            break;
          case 'accounts.disable_selected':
            succeeded = taskCount(result.disabled);
            failed = taskCount(result.failed);
            skipped = taskCount(result.skipped);
            break;
          case 'proxy.sources.test_selected':
            succeeded = taskCount(result.reachable ?? counts.reachable);
            failed = taskCount(result.unreachable ?? counts.unreachable) + taskCount(result.failed ?? counts.failed);
            skipped = taskCount(counts.pending);
            break;
          case 'proxy.sources.delete_selected':
            succeeded = taskCount(result.deleted ?? counts.deleted);
            failed = taskCount(result.failed ?? counts.failed);
            skipped = taskCount(counts.pending);
            break;
          default:
            succeeded = taskCount(result.succeeded ?? counts.succeeded);
            failed = taskCount(result.failed ?? counts.failed);
            skipped = taskCount(result.skipped ?? counts.pending);
            break;
        }
        if (succeeded + failed + skipped === 0) {
          if (result.probed != null) {
            succeeded = taskCount(result.probed);
          } else if (result.disabled != null) {
            succeeded = taskCount(result.disabled);
          } else if (result.deleted != null) {
            succeeded = taskCount(result.deleted);
          } else if (result.reachable != null) {
            succeeded = taskCount(result.reachable);
            failed = taskCount(result.unreachable);
          } else if (result.restored != null || result.external_restored != null) {
            succeeded = taskCount(result.restored) + taskCount(result.external_restored);
            skipped = taskCount(result.external_left_disabled);
          }
        }
      }
      const total = Math.max(taskCount(value.total), taskCount(value.completed), taskCount(result.count), taskCount(result.total), taskCount(counts.total), succeeded + failed + skipped);
      return { total, succeeded, failed, skipped };
    }
    function taskResultSummary(task) {
      if (!task) return '';
      const outcome = taskOutcomeCounts(task);
      const result = task.result;
      if (outcome.total > 0) {
        return i18n.t('ui.task_result_totals', { total: num(outcome.total), succeeded: num(outcome.succeeded), failed: num(outcome.failed), skipped: num(outcome.skipped) });
      }
      if (result && typeof result === 'object') {
        if (result.message) return friendlyApiMessage(String(result.message), result.code);
        if (result.change_id) return i18n.t('ui.proxy_sync_change', { id: result.change_id });
      }
      return task.error ? friendlyApiMessage(task.error, task.error_code) : '';
    }
    function renderTaskPager(result) {
      const el = document.getElementById('taskPager');
      const total = Number(result.total || 0);
      const size = Number(result.page_size || state.taskCenterPageSize || 20);
      const pages = Math.max(1, Math.ceil(total / size));
      const page = Math.min(Math.max(1, Number(result.page || state.taskCenterPage || 1)), pages);
      state.taskCenterPage = page;
      if (!total) { el.hidden = true; el.innerHTML = ''; return; }
      el.hidden = false;
      el.innerHTML = '<span class="pager-summary">' + esc(i18n.t('ui.task_pager_summary', { start: (page - 1) * size + 1, end: Math.min(page * size, total), total: total, page: page, pages: pages })) + '</span>' +
        '<div class="pager-controls"><select class="control pager-size" id="taskPageSize" aria-label="' + esc(i18n.t('ui.task_page_size')) + '">' +
        [20, 50, 100].map((value) => '<option value="' + value + '"' + (value === size ? ' selected' : '') + '>' + value + '</option>').join('') +
        '</select><button class="btn btn-xs" type="button" data-task-page="first" ' + (page <= 1 ? 'disabled' : '') + '>' + esc(i18n.t('ui.first_page')) + '</button>' +
        '<button class="btn btn-xs" type="button" data-task-page="prev" ' + (page <= 1 ? 'disabled' : '') + '>' + esc(i18n.t('ui.previous_page')) + '</button>' +
        '<button class="btn btn-xs" type="button" data-task-page="next" ' + (page >= pages ? 'disabled' : '') + '>' + esc(i18n.t('ui.next_page')) + '</button>' +
        '<button class="btn btn-xs" type="button" data-task-page="last" ' + (page >= pages ? 'disabled' : '') + '>' + esc(i18n.t('ui.last_page')) + '</button></div>';
    }
    function taskRow(task) {
      const id = esc(task.task_id);
      const label = task.label || taskKindLabel(task.kind) || i18n.t('ui.task');
      const translatedLabel = i18n.translateSource(String(label));
      const status = String(task.status || '');
      const active = status === 'queued' || status === 'running';
      const percent = taskPercent(task);
      const tone = status === 'failed' || status === 'interrupted' ? 'bad' : (status === 'succeeded' ? 'good' : '');
      const meta = [taskCategoryLabel(task.category), task.auth_count ? i18n.t('ui.account_count_short', { count: task.auth_count }) : '', formatFullTime(task.created_at)].filter(Boolean).join(i18n.t('ui.meta_separator'));
      const action = active
        ? '<button class="task-row-action" type="button" data-task-center-cancel="' + id + '">' + esc(i18n.t('ui.cancel_task')) + '</button>'
        : '<button class="task-row-action" type="button" data-task-center-delete="' + id + '">' + esc(i18n.t('ui.delete_task')) + '</button>';
      return '<div class="task-row ' + (state.taskCenterSelectedID === task.task_id ? 'selected' : '') + '">' +
        '<button class="task-row-hit" type="button" data-task-select="' + id + '" aria-label="' + esc(i18n.t('ui.view_task', { label: translatedLabel })) + '"></button>' +
        '<span class="task-row-main"><span class="task-row-title"><span class="task-row-label">' + esc(label) + '</span><span class="task-row-status-group"><span class="task-status ' + taskStatusClass(status) + '">' + esc(taskStatusLabel(status)) + '</span>' + action + '</span></span>' +
        '<span class="task-row-meta">' + esc(meta) + i18n.t('ui.meta_separator') + esc(taskProgressText(task)) + '</span></span>' +
        '<span class="task-row-count">' + Number(task.completed || 0) + ' / ' + Number(task.total || 0) + '</span>' +
        '<span class="task-row-progress"><span class="progress-track"><span class="progress-fill ' + tone + '" style="width:' + percent + '%"></span></span><span class="task-row-count">' + percent + '%</span></span></div>';
    }
    function queueMetric(label, value) {
      return '<span class="task-toolbar-metric"><span>' + esc(label) + '</span><strong>' + esc(value) + '</strong></span>';
    }
    function renderResidentQueues(queues) {
      const container = document.getElementById('residentQueues');
      if (!container) return;
      const selectedQueueID = String(state.taskCenterSelectedQueue?.id || '');
      if (!queues || !queues.length) {
        container.innerHTML = '<div class="empty" style="grid-column:1/-1"><svg class="icon"><use href="#i-activity"></use></svg><div>' + esc(i18n.t('ui.no_active_resident_queues')) + '</div></div>';
        return;
      }
      container.innerHTML = queues.map((queue) => {
        const total = Number(queue.total || 0);
        const completed = Number(queue.completed || 0);
        const percent = total > 0 ? Math.min(100, Math.round(completed / total * 100)) : 0;
        const stats = [queueMetric(i18n.t('ui.queue_running_label'), num(queue.running || 0)), queueMetric(i18n.t('ui.queue_waiting_label'), num(queue.waiting || 0)), queueMetric(i18n.t('ui.queue_succeeded_label'), num(queue.succeeded || 0)), queueMetric(i18n.t('ui.queue_failed_label'), num(queue.failed || 0))].join('');
        const progress = '<div class="progress-track"><div class="progress-fill ' + (Number(queue.failed || 0) ? 'bad' : 'good') + '" style="width:' + percent + '%"></div></div>';
        const queueID = String(queue.id || '');
        const selected = queueID === selectedQueueID;
        const buttonID = 'residentQueueView-' + queueID.replace(/[^a-zA-Z0-9_-]/g, '');
        const detailButton = ['auto_bind', 'proxy_health', 'proxy_changes', 'resin_cleanup', 'quality_probe'].includes(queue.id) ? '<button class="btn btn-xs" type="button" id="' + esc(buttonID) + '" data-task-queue="' + esc(queueID) + '" aria-controls="residentQueueDetail" aria-expanded="' + (selected ? 'true' : 'false') + '">' + esc(i18n.t(selected ? 'ui.hide_queue' : 'ui.view_queue')) + '</button>' : '';
        const queueError = queue.last_error ? friendlyApiMessage(queue.last_error, queue.last_error_code) : '';
        return '<article class="queue-card' + (selected ? ' selected' : '') + '"><div class="queue-card-head"><div><strong>' + esc(queue.label) + '</strong><p>' + esc(i18n.translateSource(queue.description || queue.detail || '')) + '</p></div><span class="task-status ' + taskStatusClass(queue.status) + '">' + esc(taskStatusLabel(queue.status)) + '</span></div>' +
          '<div class="queue-card-stats">' + stats + '</div>' + progress + '<div class="queue-card-actions">' + detailButton + '<button class="btn btn-xs btn-ghost" type="button" data-task-queue-refresh="' + esc(queueID) + '">' + esc(i18n.t('ui.refresh_status')) + '</button></div>' +
          (queueError ? '<div class="queue-card-error">' + esc(queueError) + '</div>' : '') + '</article>';
      }).join('');
    }
    function renderTaskDetailEmpty(title, detail) {
      document.getElementById('taskDetailBody').innerHTML = '<div class="task-detail-empty"><span class="task-empty-icon"><svg class="icon"><use href="#i-terminal"></use></svg></span><h3>' + esc(title) + '</h3><p>' + esc(detail) + '</p></div>';
      document.getElementById('taskDetailBadge').hidden = true;
    }
    function renderTaskDetail() {
      if (!state.taskCenterSelectedID) {
        document.getElementById('taskDetailTitle').innerHTML = icon('eye') + i18n.t("ui.task_details");
        document.getElementById('taskDetailSubtitle').textContent = i18n.t('ui.select_a_task_on_the_left_to_view_its');
        renderTaskDetailEmpty(i18n.t('ui.select_a_task_to_view_details'), i18n.t('ui.task_detail_hint'));
        return;
      }
      if (state.taskCenterDetailLoading && !state.taskCenterDetail) {
        document.getElementById('taskDetailTitle').innerHTML = icon('eye') + i18n.t("ui.task_details");
        document.getElementById('taskDetailSubtitle').textContent = i18n.t("ui.loading_task_details");
        document.getElementById('taskDetailBody').innerHTML = '<div class="task-detail-empty"><span class="task-empty-icon"><svg class="icon spin"><use href="#i-refresh"></use></svg></span><h3>' + i18n.t('ui.loading') + '</h3><p>' + i18n.t('ui.history_details_loaded_on_demand') + '</p></div>';
        document.getElementById('taskDetailBadge').hidden = true;
        return;
      }
      const detail = state.taskCenterDetail;
      if (!detail) {
        renderTaskDetailEmpty(i18n.t('ui.task_details_unavailable'), i18n.t('ui.task_history_cleaned'));
        return;
      }
      const task = detail.task || {};
      const outcome = taskOutcomeCounts(task);
      const total = outcome.total;
      const completed = Math.max(0, Number(task.completed || 0));
      const running = Math.max(0, Number(task.running || 0));
      const waiting = Math.max(0, Number(task.waiting || 0));
      const percent = total > 0 ? Math.max(0, Math.min(100, Math.round(completed / total * 100))) : 0;
      const status = String(task.status || '');
      document.getElementById('taskDetailTitle').innerHTML = icon('eye') + esc(task.label || taskKindLabel(task.kind) || i18n.t("ui.task_details"));
      document.getElementById('taskDetailSubtitle').textContent = taskCategoryLabel(task.category) + i18n.t('ui.meta_separator') + (task.kind ? taskKindLabel(task.kind) : task.task_id);
      const detailBadge = document.getElementById('taskDetailBadge');
      detailBadge.hidden = false;
      detailBadge.className = 'badge ' + (status === 'succeeded' ? 'good' : (status === 'failed' || status === 'interrupted' ? 'bad' : (status === 'running' || status === 'queued' ? 'info' : '')));
      detailBadge.textContent = taskStatusLabel(status);
      const itemFilter = '<select class="control" id="taskItemStatusFilter" aria-label="' + i18n.t('ui.filter_item_results') + '"><option value="all"' + (state.taskCenterDetailStatus === 'all' ? ' selected' : '') + '>' + i18n.t('ui.all_results') + '</option><option value="succeeded"' + (state.taskCenterDetailStatus === 'succeeded' ? ' selected' : '') + '>' + i18n.t('ui.succeeded') + '</option><option value="failed"' + (state.taskCenterDetailStatus === 'failed' ? ' selected' : '') + '>' + i18n.t('ui.failed') + '</option><option value="skipped"' + (state.taskCenterDetailStatus === 'skipped' ? ' selected' : '') + '>' + i18n.t('ui.skipped') + '</option></select>';
      const counts = '<div class="task-count-grid">' +
        '<div class="task-count"><span>' + i18n.t('ui.task_total') + '</span><strong>' + num(total) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.completed') + '</span><strong>' + num(completed) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.worker_processing') + '</span><strong>' + num(running) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.waiting_2') + '</span><strong>' + num(waiting) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.queue_succeeded') + '</span><strong>' + num(outcome.succeeded) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.failed') + '</span><strong>' + num(outcome.failed) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.skipped') + '</span><strong>' + num(outcome.skipped) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.duration') + '</span><strong>' + esc(taskDurationLabel(task.duration_ms)) + '</strong></div></div>';
      const meta = '<div class="task-meta-grid">' +
        '<div class="task-meta-item">' + i18n.t('ui.task_id') + '<strong title="' + esc(task.task_id) + '">' + esc(task.task_id) + '</strong></div>' +
        '<div class="task-meta-item">' + i18n.t('ui.task_type') + '<strong>' + esc(taskKindLabel(task.kind)) + '</strong></div>' +
        '<div class="task-meta-item">' + i18n.t('ui.created_at') + '<strong>' + esc(formatFullTime(task.created_at)) + '</strong></div>' +
        '<div class="task-meta-item">' + i18n.t('ui.started_at') + '<strong>' + esc(formatFullTime(task.started_at)) + '</strong></div>' +
        '<div class="task-meta-item">' + i18n.t('ui.ended_at') + '<strong>' + esc(formatFullTime(task.ended_at)) + '</strong></div>' +
        '<div class="task-meta-item">' + i18n.t('ui.updated_at') + '<strong>' + esc(formatFullTime(task.updated_at)) + '</strong></div>' +
        '<div class="task-meta-item">' + i18n.t('ui.source') + '<strong>' + esc(detail.source === 'history' ? i18n.t('ui.history_archive') : i18n.t('ui.current_task')) + '</strong></div>' +
        '<div class="task-meta-item">' + i18n.t('ui.item_retention') + '<strong>' + num(task.items_stored || 0) + ' / ' + num(task.item_count || 0) + (task.items_truncated ? i18n.t('ui.truncated') : '') + '</strong></div>' +
        (Array.isArray(task.auth_ids) && task.auth_ids.length ? '<div class="task-meta-item task-meta-wide">' + i18n.t('ui.involved_accounts') + '<strong>' + esc(task.auth_ids.join(i18n.t('ui.list_separator'))) + (Number(task.auth_count || 0) > task.auth_ids.length ? i18n.t('ui.account_count_more_suffix', { count: num(task.auth_count) }) : '') + '</strong></div>' : '') +
        '</div>';
      const resultSummary = taskResultSummary(task);
      const resultText = resultSummary || (task.error ? friendlyApiMessage(task.error, task.error_code) : '');
      const resultHTML = resultText ? '<div class="task-result-box ' + (task.error ? 'error' : '') + '">' + esc(resultText) + '</div>' : '';
      const items = Array.isArray(detail.items) ? detail.items : [];
      const itemTotal = Number(detail.item_total ?? detail.total ?? items.length);
      const itemRows = items.map((item) => {
        const value = item && typeof item === 'object' ? item : { result: item };
        const identity = value.auth_id || value.source_id || value.account || value.id || value.name || '-';
        const statusValue = value.status || value.result || '-';
        const rawReason = value.error || value.message || value.reason || value.detail || (value.endpoint ? i18n.t('ui.endpoint_label', { endpoint: value.endpoint }) : '') || (value.next_attempt_at ? i18n.t('ui.next_attempt_label', { time: formatFullTime(value.next_attempt_at) }) : '-');
        const reason = rawReason && rawReason !== '-' ? friendlyApiMessage(rawReason, value.code) : '-';
        return '<tr><td class="mono">' + esc(identity) + '</td><td><span class="task-status ' + taskStatusClass(value.status) + '">' + esc(taskStatusLabel(statusValue)) + '</span></td><td class="task-item-json">' + esc(reason) + '</td></tr>';
      }).join('');
      const itemPagination = '<div class="task-item-toolbar"><span class="muted">' + i18n.t('ui.matched_retained_items', { matched: num(detail.total || 0), retained: num(itemTotal), truncated: detail.items_truncated ? i18n.t('ui.overflow_truncated') : '' }) + '</span><span class="muted">' + i18n.t('ui.page', { page: Number(detail.page || 1), pages: Math.max(1, Math.ceil(Number(detail.total || 0) / Number(detail.page_size || 20))) }) + '</span></div>';
      const itemTable = '<div class="table-wrap task-item-table"><table><thead><tr><th>' + i18n.t('ui.account_or_object') + '</th><th>' + i18n.t('ui.result') + '</th><th>' + i18n.t('ui.reason_or_details') + '</th></tr></thead><tbody>' + (itemRows || '<tr><td colspan="3" class="muted">' + i18n.t('ui.no_item_details') + '</td></tr>') + '</tbody></table></div>';
      const itemActions = '<div class="form-actions task-detail-actions"><button class="btn btn-xs" type="button" data-task-item-page="-1" ' + (Number(detail.page || 1) <= 1 ? 'disabled' : '') + '>' + i18n.t('ui.previous_page') + '</button><button class="btn btn-xs" type="button" data-task-item-page="1" ' + ((Number(detail.page || 1) * Number(detail.page_size || 20)) >= Number(detail.total || 0) ? 'disabled' : '') + '>' + i18n.t('ui.next_page') + '</button></div>';
      document.getElementById('taskDetailBody').innerHTML =
        '<div class="task-detail-hero"><div class="task-detail-hero-top"><div class="task-detail-hero-title"><strong>' + esc(taskProgressText(task)) + '</strong><p>' + esc((task.current_stage || '') + (task.auth_count ? i18n.t('ui.account_count_suffix', { count: task.auth_count }) : '')) + '</p></div><span class="muted">' + percent + '%</span></div><div class="progress-track"><div class="progress-fill ' + (status === 'failed' || status === 'interrupted' ? 'bad' : (status === 'succeeded' ? 'good' : '')) + '" style="width:' + percent + '%"></div></div><div class="task-detail-percent">' + i18n.t('ui.items_completed_progress', { completed: num(completed), total: num(total), processing: running || waiting ? i18n.t('ui.processing_waiting_progress', { running: num(running), waiting: num(waiting) }) : '' }) + '</div></div>' +
        '<div class="task-detail-section">' + counts + '</div>' +
        '<div class="task-detail-section"><h3>' + i18n.t('ui.task_info') + '</h3>' + meta + '</div>' +
        (resultHTML ? '<div class="task-detail-section"><h3>' + i18n.t('ui.result_summary') + '</h3>' + resultHTML + '</div>' : '') +
        '<div class="task-detail-section"><div class="task-item-toolbar"><h3>' + i18n.t('ui.item_results') + '</h3>' + itemFilter + '</div>' + itemPagination + itemTable + itemActions + '</div>';
    }
    function taskQueuePager(data, pageAttribute) {
      const total = Number(data?.total || 0);
      const size = Math.max(1, Number(data?.page_size || 20));
      const pages = Math.max(1, Math.ceil(total / size));
      const page = Math.min(Math.max(1, Number(data?.page || 1)), pages);
      if (!total) return '';
      return '<div class="form-actions task-queue-pager"><span class="muted">' + i18n.t('ui.task_queue_pager', { page: page, pages: pages, total: total }) + '</span><span class="task-queue-pager-actions"><button class="btn btn-xs" type="button" ' + pageAttribute + '="-1" ' + (page <= 1 ? 'disabled' : '') + '>' + i18n.t('ui.previous_page') + '</button><button class="btn btn-xs" type="button" ' + pageAttribute + '="1" ' + (page >= pages ? 'disabled' : '') + '>' + i18n.t('ui.next_page') + '</button></span></div>';
    }
    function revealResidentQueueDetail() {
      const panel = document.getElementById('residentQueueDetail');
      if (panel && typeof panel.scrollIntoView === 'function') panel.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
    }
    async function renderTaskQueueDetail() {
      const panel = document.getElementById('residentQueueDetail');
      const bodyEl = document.getElementById('residentQueueDetailBody');
      const queue = state.taskCenterSelectedQueue;
      if (!panel || !bodyEl || !queue) {
        if (panel) panel.hidden = true;
        if (bodyEl) bodyEl.innerHTML = '';
        return;
      }
      panel.hidden = false;
      const detailBadge = document.getElementById('residentQueueDetailBadge');
      detailBadge.hidden = false;
      detailBadge.className = 'badge ' + (queue.status === 'failed' ? 'bad' : (queue.status === 'running' ? 'info' : (queue.status === 'succeeded' ? 'good' : '')));
      detailBadge.textContent = taskStatusLabel(queue.status);
      document.getElementById('residentQueueDetailTitle').innerHTML = icon('activity') + esc(queue.label || i18n.t('ui.resident_queue'));
      document.getElementById('residentQueueDetailSubtitle').textContent = queue.description || queue.detail || '';
      const queueDetail = state.taskCenterQueueDetail;
      if (queueDetail?.loading) {
        bodyEl.innerHTML = '<div class="task-detail-empty"><span class="task-empty-icon"><svg class="icon spin"><use href="#i-refresh"></use></svg></span><h3>' + i18n.t('ui.loading_queue') + '</h3><p>' + i18n.t('ui.loading_current_queue_items') + '</p></div>';
        return;
      }
      const stats = '<div class="task-count-grid">' +
        '<div class="task-count"><span>' + i18n.t('ui.queue_total') + '</span><strong>' + num(queue.total || 0) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.completed') + '</span><strong>' + num(queue.completed || 0) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.processing') + '</span><strong>' + num(queue.running || 0) + '</strong></div>' +
        '<div class="task-count"><span>' + i18n.t('ui.waiting_2') + '</span><strong>' + num(queue.waiting || 0) + '</strong></div></div>';
      let body = '<div class="task-detail-section">' + stats + '</div>';
      if (queue.last_error) body += '<div class="task-detail-section"><h3>' + i18n.t('ui.latest_error') + '</h3><div class="task-result-box error">' + esc(friendlyApiMessage(queue.last_error, queue.last_error_code)) + '</div></div>';
      if (queue.id === 'proxy_changes' && queueDetail) {
        if (queueDetail.mode === 'change') {
          const change = queueDetail.data || {};
          const items = Array.isArray(change.items) ? change.items : [];
          body += '<div class="task-detail-section"><div class="task-item-toolbar"><h3>' + i18n.t('ui.proxy_sync_details', { id: change.change_id || '' }) + '</h3><button class="btn btn-xs" type="button" data-task-queue-back="1">' + (queue?.synthetic ? i18n.t("ui.back_to_task_center") : i18n.t("ui.back_to_queue")) + '</button></div><div class="table-wrap task-item-table"><table><thead><tr><th>' + i18n.t('ui.account') + '</th><th>' + i18n.t('ui.result') + '</th><th>' + i18n.t('ui.reason') + '</th></tr></thead><tbody>' + (items.map((item) => '<tr><td class="mono">' + esc(item.auth_id || '-') + '</td><td>' + esc(taskStatusLabel(item.status)) + '</td><td class="task-item-json">' + esc(item.error ? friendlyApiMessage(item.error, item.code) : '-') + '</td></tr>').join('') || '<tr><td colspan="3" class="muted">' + i18n.t('ui.no_details') + '</td></tr>') + '</tbody></table></div>' + taskQueuePager(change, 'data-task-change-page') + '</div>';
        } else {
          const items = Array.isArray(queueDetail.data?.items) ? queueDetail.data.items : [];
          body += '<div class="task-detail-section"><h3>' + i18n.t('ui.proxy_sync_records') + '</h3><div class="table-wrap task-item-table"><table><thead><tr><th>' + i18n.t('ui.proxy') + '</th><th>' + i18n.t('ui.status') + '</th><th>' + i18n.t('ui.progress') + '</th><th>' + i18n.t('ui.actions') + '</th></tr></thead><tbody>' + (items.map((item) => '<tr><td class="mono">' + esc(item.source_id || item.id || '-') + '</td><td>' + esc(taskStatusLabel(item.status)) + '</td><td>' + esc(i18n.t('ui.pending_sync_count', { succeeded: Number(item.counts?.succeeded || 0), failed: Number(item.counts?.failed || 0), pending: Number(item.counts?.pending || 0) })) + '</td><td><button class="btn btn-xs" type="button" data-task-change-detail="' + esc(item.id || item.change_id) + '">' + i18n.t('ui.detail') + '</button>' + (Number(item.counts?.failed || 0) ? ' <button class="btn btn-xs" type="button" data-task-change-retry="' + esc(item.id || item.change_id) + '">' + i18n.t('ui.retry_failed_items') + '</button>' : '') + '</td></tr>').join('') || '<tr><td colspan="4" class="muted">' + i18n.t('ui.no_sync_records') + '</td></tr>') + '</tbody></table></div>' + taskQueuePager(queueDetail.data, 'data-task-queue-page') + '</div>';
        }
      } else if (queue.id === 'resin_cleanup' && queueDetail) {
        const items = Array.isArray(queueDetail.data?.items) ? queueDetail.data.items : [];
        body += '<div class="task-detail-section"><h3>' + i18n.t('ui.leases_pending_cleanup') + '</h3><div class="table-wrap task-item-table"><table><thead><tr><th>' + i18n.t('ui.account') + '</th><th>' + i18n.t('ui.status') + '</th><th>' + i18n.t('ui.next_attempt_or_error') + '</th><th>' + i18n.t('ui.actions') + '</th></tr></thead><tbody>' + (items.map((item) => '<tr><td class="mono">' + esc(item.auth_id || '-') + '</td><td>' + esc(item.paused ? i18n.t("task.status.paused") : taskStatusLabel(item.status)) + '</td><td class="task-item-json">' + esc(item.last_error ? friendlyApiMessage(item.last_error, item.last_error_code || item.code) : (item.next_attempt_at ? formatFullTime(item.next_attempt_at) : i18n.t("ui.waiting_for_cleanup"))) + '</td><td><button class="btn btn-xs" type="button" data-task-cleanup-retry="' + esc(item.id) + '" ' + (item.paused ? 'disabled' : '') + '>' + i18n.t('ui.retry') + '</button> <button class="btn btn-xs btn-ghost" type="button" data-task-cleanup-pause="' + esc(item.id) + '" data-paused="' + (item.paused ? 'false' : 'true') + '">' + i18n.t(item.paused ? 'ui.resume' : 'ui.pause') + '</button></td></tr>').join('') || '<tr><td colspan="4" class="muted">' + i18n.t('ui.no_leases_are_waiting_for_cleanup') + '</td></tr>') + '</tbody></table></div>' + taskQueuePager(queueDetail.data, 'data-task-queue-page') + '</div>';
      } else if (['quality_probe', 'auto_bind', 'proxy_health'].includes(queue.id) && queueDetail) {
        const data = queueDetail.data || {};
        const active = Array.isArray(data.active) ? data.active : [];
        const waiting = data.waiting || {};
        const waitingItems = Array.isArray(waiting.items) ? waiting.items : [];
        const objectLabel = i18n.t(queue.id === 'proxy_health' ? 'ui.proxy' : 'ui.account');
        const itemIdentity = (item) => item.label || item.auth_id || item.source_id || item.id || '-';
        const activeRows = active.map((item) => '<tr><td class="mono">' + esc(itemIdentity(item)) + '</td><td><span class="task-status ' + taskStatusClass(item.status) + '">' + esc(taskStatusLabel(item.status)) + '</span></td><td>' + esc(formatFullTime(item.started_at)) + '</td></tr>').join('') || '<tr><td colspan="3" class="muted">' + i18n.t('ui.no_running_tasks') + '</td></tr>';
        const waitingRows = waitingItems.map((item) => '<tr><td class="mono">' + esc(item.position || '-') + '</td><td class="mono">' + esc(itemIdentity(item)) + '</td><td><span class="task-status ' + taskStatusClass(item.status) + '">' + esc(taskStatusLabel(item.status)) + '</span></td><td>' + esc(formatFullTime(item.queued_at)) + '</td></tr>').join('') || '<tr><td colspan="4" class="muted">' + i18n.t('ui.no_tasks_are_waiting_to_run') + '</td></tr>';
        const actions = queue.id === 'auto_bind' || queue.id === 'proxy_health' ? '<div class="task-detail-actions"><button class="btn btn-xs" type="button" data-task-queue-action="auto_bind">' + i18n.t('ui.open_auto_bind_settings') + '</button><button class="btn btn-xs btn-ghost" type="button" data-task-queue-action="scan">' + i18n.t('ui.scan_now') + '</button></div>' : '';
        body += '<div class="task-detail-section"><h3>' + i18n.t('ui.running_tasks') + '</h3><div class="table-wrap task-item-table"><table><thead><tr><th>' + esc(objectLabel) + '</th><th>' + i18n.t('ui.status') + '</th><th>' + i18n.t('ui.started_at') + '</th></tr></thead><tbody>' + activeRows + '</tbody></table></div></div>';
        body += '<div class="task-detail-section"><div class="task-item-toolbar"><h3>' + i18n.t('ui.tasks_waiting_to_run') + '</h3><span class="muted">' + i18n.t('ui.records_count', { count: num(waiting.total || 0) }) + '</span></div><div class="table-wrap task-item-table"><table><thead><tr><th>' + i18n.t('ui.position') + '</th><th>' + esc(objectLabel) + '</th><th>' + i18n.t('ui.status') + '</th><th>' + i18n.t('ui.enqueued_at') + '</th></tr></thead><tbody>' + waitingRows + '</tbody></table></div>' + taskQueuePager(waiting, 'data-task-queue-page') + '</div>' + actions;
      } else {
        const actions = queue.id === 'auto_bind' || queue.id === 'proxy_health' ? '<button class="btn btn-xs" type="button" data-task-queue-action="auto_bind">' + i18n.t('ui.open_auto_bind_settings') + '</button><button class="btn btn-xs btn-ghost" type="button" data-task-queue-action="scan">' + i18n.t('ui.scan_now') + '</button>' : '';
        body += '<div class="task-detail-section"><h3>' + i18n.t('ui.execution_notes') + '</h3><div class="task-result-box">' + esc(queue.detail || queue.description || i18n.t('ui.no_further_description')) + '</div>' + (actions ? '<div class="task-detail-actions">' + actions + '</div>' : '') + '</div>';
      }
      bodyEl.innerHTML = body;
    }
    async function loadTaskCenterDetail(taskID, page, status, silent) {
      const id = String(taskID || '').trim();
      if (!id) return;
      const request = ++state.taskCenterDetailRequest;
      state.taskCenterDetailLoading = true;
      if (!silent) state.taskCenterDetail = null;
      renderTaskDetail();
      try {
        const detail = await api('tasks.center.get', { task_id: id, page: page || 1, page_size: 20, item_status: status && status !== 'all' ? status : '' });
        if (request !== state.taskCenterDetailRequest || state.taskCenterSelectedID !== id) return;
        state.taskCenterDetail = detail;
        state.taskCenterDetailPage = Number(detail.page || page || 1);
        state.taskCenterDetailStatus = status || 'all';
      } catch (error) {
        if (request !== state.taskCenterDetailRequest) return;
        state.taskCenterDetail = null;
        if (!silent) toast(error.message, 'error');
      } finally {
        if (request === state.taskCenterDetailRequest) {
          state.taskCenterDetailLoading = false;
          renderTaskDetail();
        }
      }
    }
    function selectTaskCenterTask(taskID, updateHash) {
      state.taskCenterSelectedID = String(taskID || '');
      state.taskCenterDetail = null;
      state.taskCenterDetailPage = 1;
      state.taskCenterDetailStatus = 'all';
      document.getElementById('taskDetailCard').classList.add('mobile-open');
      renderTaskCenter();
      if (state.taskCenterSelectedID) {
        loadTaskCenterDetail(state.taskCenterSelectedID, 1, 'all', false);
        if (updateHash !== false && window.history && window.history.replaceState) window.history.replaceState(null, '', '#tasks/' + encodeURIComponent(state.taskCenterSelectedID));
      }
    }
    async function selectTaskCenterQueue(queueID) {
      const queues = state.taskCenter?.resident_queues || [];
      const queue = queues.find((item) => item.id === queueID);
      if (!queue) return;
      if (state.taskCenterSelectedQueue?.id === queue.id) {
        closeResidentQueueDetail(true);
        return;
      }
      state.taskCenterSelectedQueue = queue;
      state.taskCenterQueueDetail = { loading: true };
      state.taskQueuePage = 1;
      renderTaskCenter();
      revealResidentQueueDetail();
      try {
        await loadTaskQueueDetail(queue, 1);
      } catch (error) {
        state.taskCenterQueueDetail = null;
        renderTaskQueueDetail();
        toast(error.message, 'error');
      }
    }
    function closeResidentQueueDetail(returnFocus) {
      const queueID = String(state.taskCenterSelectedQueue?.id || '');
      state.taskQueueDetailRequest++;
      state.taskCenterSelectedQueue = null;
      state.taskCenterQueueDetail = null;
      state.taskQueuePage = 1;
      renderTaskCenter();
      if (returnFocus && queueID) {
        const buttonID = 'residentQueueView-' + queueID.replace(/[^a-zA-Z0-9_-]/g, '');
        document.getElementById(buttonID)?.focus();
      }
    }
    async function loadTaskQueueDetail(queue, page) {
      if (!queue) return;
      const queueID = String(queue.id || '');
      const pageNo = Math.max(1, Number(page || 1));
      const request = ++state.taskQueueDetailRequest;
      if (!['proxy_changes', 'resin_cleanup', 'quality_probe', 'auto_bind', 'proxy_health'].includes(queue.id)) {
        if (state.taskCenterSelectedQueue?.id !== queueID) return;
        state.taskCenterQueueDetail = { loading: false, mode: 'summary', data: queue };
        state.taskQueuePage = 1;
        renderTaskQueueDetail();
        return;
      }
      const current = state.taskCenterQueueDetail;
      const preserve = current && !current.loading && ['list', 'live'].includes(current.mode) && state.taskCenterSelectedQueue?.id === queueID;
      if (!preserve) {
        state.taskCenterQueueDetail = { loading: true, mode: 'list' };
        renderTaskQueueDetail();
      }
      try {
        const live = ['quality_probe', 'auto_bind', 'proxy_health'].includes(queue.id);
        const action = live ? 'tasks.center.queue' : (queue.id === 'proxy_changes' ? 'proxy.changes.list' : 'resin.cleanup.list');
        const params = live ? { queue_id: queue.id, page: pageNo, page_size: 20 } : { page: pageNo, page_size: 20 };
        const data = await api(action, params);
        if (request !== state.taskQueueDetailRequest || state.taskCenterSelectedQueue?.id !== queueID) return;
        state.taskCenterQueueDetail = { loading: false, mode: live ? 'live' : 'list', data: data };
        state.taskQueuePage = Number(data?.page || pageNo);
        renderTaskQueueDetail();
      } catch (error) {
        if (request !== state.taskQueueDetailRequest) return;
        if (!preserve) state.taskCenterQueueDetail = null;
        renderTaskQueueDetail();
        throw error;
      }
    }
    function renderTaskCenter() {
      const result = state.taskCenter || {};
      const items = Array.isArray(result.items) ? result.items : [];
      const counts = result.counts || {};
      const history = result.history || {};
      document.getElementById('taskMetrics').innerHTML = [
        metricCard('activity', num(counts.active || 0), i18n.t("ui.task_in_progress"), i18n.t("ui.waiting_or_running"), Number(counts.active || 0) ? 'info' : 'good'),
        metricCard('play', num(counts.running_items || 0), i18n.t('ui.worker_processing'), i18n.t('ui.current_parallel_items'), Number(counts.running_items || 0) ? 'info' : ''),
        metricCard('alert', num(counts.failed_items || 0), i18n.t('ui.failed_work_items'), i18n.t('ui.view_reasons_in_task_details'), Number(counts.failed_items || 0) ? 'warn' : 'good'),
        metricCard('database', history.enabled === false ? i18n.t('ui.off') : i18n.t('ui.records_count', { count: num(history.record_count || 0) }), i18n.t('ui.task_history'), taskHistoryText(history))
      ].join('');
      document.getElementById('tasksCount').textContent = i18n.t('ui.current_filtered_tasks', { count: num(result.total || 0) });
      document.getElementById('taskRows').innerHTML = items.map(taskRow).join('');
      document.getElementById('tasksEmpty').hidden = items.length > 0;
      renderTaskPager(result);
      renderTaskCenterSummary(history);
      renderResidentQueues(result.resident_queues || []);
      renderTaskQueueDetail();
      renderTaskDetail();
    }
    function refreshTaskCenterQueues(silent) {
      if (state.taskCenterQueuesPromise) return state.taskCenterQueuesPromise;
      state.taskCenterQueuesPromise = (async () => {
        try {
          const result = await apiWithRetry('tasks.center.queues', null, null, 2);
          const queues = Array.isArray(result?.resident_queues) ? result.resident_queues : [];
          state.taskCenter = Object.assign({}, state.taskCenter || {}, { resident_queues: queues });
          renderResidentQueues(queues);
        } catch (error) {
          if (!silent) toast(error.message || i18n.t('ui.partial_data_load_failed'), 'error');
        } finally {
          state.taskCenterQueuesPromise = null;
        }
      })();
      return state.taskCenterQueuesPromise;
    }
    async function refreshTaskCenter(silent) {
      if (state.taskCenterRefreshing && silent) return;
      const request = ++state.taskCenterListRequest;
      state.taskCenterRefreshing = true;
      try {
        let result = null;
        for (let attempt = 0; attempt < 2; attempt++) {
          result = await apiWithRetry('tasks.center.list', {
            page: state.taskCenterPage,
            page_size: state.taskCenterPageSize,
            category: state.taskCenterCategory === 'all' ? '' : state.taskCenterCategory,
            status: state.taskCenterStatus === 'all' ? '' : state.taskCenterStatus,
            query: state.taskCenterQuery
          }, null, 2);
          if (request !== state.taskCenterListRequest) return;
          const page = Number(result?.page || 1);
          const pages = Math.max(1, Math.ceil(Number(result?.total || 0) / Number(result?.page_size || state.taskCenterPageSize || 20)));
          if (page <= pages) break;
          state.taskCenterPage = pages;
        }
        if (!result) return;
        const previousQueues = Array.isArray(state.taskCenter?.resident_queues) ? state.taskCenter.resident_queues : [];
        state.taskCenter = Object.assign({}, result, {
          resident_queues: Array.isArray(result.resident_queues) ? result.resident_queues : previousQueues
        });
        state.taskCenterPage = Number(result.page || 1);
        renderTaskCenter();
        if (request !== state.taskCenterListRequest) return;
        if (state.taskCenterSelectedID) {
          const detailStatus = state.taskCenterDetail?.task?.status;
          if (!state.taskCenterDetail || detailStatus === 'queued' || detailStatus === 'running') {
            await loadTaskCenterDetail(state.taskCenterSelectedID, state.taskCenterDetailPage || 1, state.taskCenterDetailStatus || 'all', Boolean(state.taskCenterDetail));
            if (request !== state.taskCenterListRequest) return;
          }
        }
        if (state.taskCenterSelectedQueue) {
          const selectedQueue = state.taskCenterSelectedQueue;
          const latestQueue = (state.taskCenter.resident_queues || []).find((item) => item.id === selectedQueue.id);
          if (latestQueue) {
            state.taskCenterSelectedQueue = Object.assign({}, latestQueue, selectedQueue.synthetic ? { synthetic: true } : {});
            const currentDetail = state.taskCenterQueueDetail;
            if (!currentDetail?.loading) {
              if (currentDetail?.mode === 'change' && currentDetail.data?.change_id) {
                await openTaskCenterChange(currentDetail.data.change_id, currentDetail.data.page || 1, true);
              } else {
                await loadTaskQueueDetail(latestQueue, state.taskQueuePage || 1);
              }
            }
          } else if (selectedQueue.synthetic && selectedQueue.id === 'proxy_changes') {
            const currentDetail = state.taskCenterQueueDetail;
            if (!currentDetail?.loading && currentDetail?.mode === 'change' && currentDetail.data?.change_id) {
              await openTaskCenterChange(currentDetail.data.change_id, currentDetail.data.page || 1, true);
            }
          } else {
            closeResidentQueueDetail(false);
          }
        }
      } catch (error) {
        if (request !== state.taskCenterListRequest) return;
        if (!silent) toast(error.message, 'error');
      } finally {
        if (request === state.taskCenterListRequest) state.taskCenterRefreshing = false;
      }
    }
    function openTaskCenter(taskID) {
      setTab('tasks');
      if (taskID) selectTaskCenterTask(taskID, true);
    }
    function closeTaskCenterDetail(updateHash) {
      state.taskCenterDetailRequest++;
      state.taskCenterSelectedID = '';
      state.taskCenterDetail = null;
      document.getElementById('taskDetailCard').classList.remove('mobile-open');
      renderTaskCenter();
      if (updateHash !== false && window.history && window.history.replaceState) window.history.replaceState(null, '', '#tasks');
    }
    async function openTaskCenterChange(changeID, page, silent) {
      const id = String(changeID || '').trim();
      if (!id) return;
      const pageNo = Math.max(1, Number(page || 1));
      const request = ++state.taskQueueDetailRequest;
      const current = state.taskCenterQueueDetail;
      const preserve = current?.mode === 'change' && current?.data?.change_id === id && Number(current?.data?.page || 1) === pageNo;
      if (!preserve) {
        state.taskCenterQueueDetail = { loading: true, mode: 'change', data: { change_id: id, page: pageNo, page_size: 20 } };
        renderTaskQueueDetail();
      }
      try {
        const data = await api('proxy.changes.get', { change_id: id, page: pageNo, page_size: 20 });
        if (request !== state.taskQueueDetailRequest || state.taskCenterSelectedQueue?.id !== 'proxy_changes') return;
        const counts = data?.counts || {};
        const queue = state.taskCenterSelectedQueue;
        if (queue) {
          queue.status = data?.status || queue.status;
          queue.total = Number(data?.total ?? counts.total ?? queue.total ?? 0);
          queue.succeeded = Number(counts.succeeded || 0);
          queue.failed = Number(counts.failed || 0);
          queue.completed = queue.succeeded + queue.failed;
          queue.waiting = Number(counts.pending || 0);
          queue.running = data?.status === 'running' && queue.waiting > 0 ? Math.max(1, Number(queue.running || 0)) : 0;
        }
        state.taskCenterQueueDetail = { loading: false, mode: 'change', data: data };
        state.taskQueuePage = Number(data?.page || pageNo);
        renderTaskQueueDetail();
      } catch (error) {
        if (request !== state.taskQueueDetailRequest) return;
        if (!preserve) {
          state.taskCenterQueueDetail = null;
          renderTaskQueueDetail();
        }
        if (!silent) throw error;
      }
    }
    async function refreshTaskCenterQueue(queueID, announce) {
      await refreshTaskCenterQueues(true);
      const latest = (state.taskCenter?.resident_queues || []).find((item) => item.id === queueID);
      if (!latest) closeResidentQueueDetail(true);
      if (announce) toast(i18n.t("ui.queue_status_refreshed"), 'success');
      refreshTaskCenter(true);
    }
    function watchTaskInBackground(value, label) {
      if (!value || !value.task_id) return;
      waitForTask(value, label).then((result) => {
        if (state.activeTab === 'tasks') refreshTaskCenter(true);
        if (result && label && state.activeTab !== 'tasks') toast(i18n.t('ui.task_completed_view_center', { label: label }), 'success');
      }).catch((error) => toast(error.message || i18n.t('ui.task_center_failed_suffix', { label: label || i18n.t('ui.task') }), 'error'));
    }
    function eventFilterHash(authID, eventType) {
      let hash = '#events';
      if (authID) hash += '/' + encodeURIComponent(authID);
      if (eventType && eventType !== 'all') hash += '/' + encodeURIComponent(eventType);
      return hash;
    }
