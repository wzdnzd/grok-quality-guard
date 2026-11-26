    document.querySelectorAll('.tabs [data-tab]').forEach((button) => button.addEventListener('click', () => setTab(button.dataset.tab)));
    document.querySelectorAll('[data-proxy-subtab]').forEach((button) => button.addEventListener('click', () => setProxySubtab(button.dataset.proxySubtab)));
    document.querySelectorAll('[data-goto-tab]').forEach((button) => button.addEventListener('click', () => setTab(button.dataset.gotoTab)));
    document.querySelectorAll('[data-open-settings]').forEach((button) => button.addEventListener('click', () => openSettings(button.dataset.openSettings)));
    document.querySelectorAll('.tabs, .settings-tabs, .proxy-subnav').forEach((tablist) => tablist.addEventListener('keydown', (event) => {
      if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return;
      const buttons = Array.from(tablist.querySelectorAll('[role="tab"]'));
      const current = buttons.indexOf(document.activeElement);
      if (current < 0) return;
      event.preventDefault();
      const next = (current + (event.key === 'ArrowRight' ? 1 : -1) + buttons.length) % buttons.length;
      buttons[next].focus();
      buttons[next].click();
    }));
    document.getElementById('accountRefreshBtn').addEventListener('click', (event) => refreshAccounts(event.currentTarget));
    document.getElementById('leaseRefreshBtn').addEventListener('click', (event) => refreshResin(event.currentTarget));
    document.getElementById('eventRefreshBtn').addEventListener('click', (event) => refreshEvents(event.currentTarget));
    document.getElementById('taskSearch').addEventListener('input', (event) => {
      state.taskCenterQuery = event.target.value;
      state.taskCenterPage = 1;
      window.clearTimeout(taskSearchTimer);
      taskSearchTimer = window.setTimeout(() => refreshTaskCenter(false), 250);
    });
    document.getElementById('taskCategoryFilter').addEventListener('change', (event) => {
      state.taskCenterCategory = event.target.value;
      state.taskCenterPage = 1;
      refreshTaskCenter(false);
    });
    document.getElementById('taskStatusFilter').addEventListener('change', (event) => {
      state.taskCenterStatus = event.target.value;
      state.taskCenterPage = 1;
      refreshTaskCenter(false);
    });
    document.getElementById('taskRefreshBtn').addEventListener('click', (event) => {
      refreshTaskCenter(false).then(() => toast(i18n.t("ui.task_list_refreshed"), 'success'));
    });
    document.getElementById('taskRows').addEventListener('click', async (event) => {
      const cancel = event.target.closest('[data-task-center-cancel]');
      if (cancel) {
        const taskID = cancel.dataset.taskCenterCancel;
        const confirmed = await confirmAction({
          title: i18n.t('ui.cancel_task_title'),
          detail: i18n.t("ui.cancel_task_detail"),
          confirmLabel: i18n.t("ui.request_cancel"),
          icon: 'power',
          tone: 'danger'
        });
        if (!confirmed) return;
        try {
          await api('tasks.center.cancel', { task_id: taskID });
          toast(i18n.t('ui.task_cancellation_requested'), 'success');
          await refreshTaskCenter(true);
        } catch (error) {
          toast(error.message, 'error');
          await refreshTaskCenter(true);
        }
        return;
      }
      const remove = event.target.closest('[data-task-center-delete]');
      if (remove) {
        const taskID = remove.dataset.taskCenterDelete;
        const task = (state.taskCenter?.items || []).find((item) => item.task_id === taskID);
        const label = task?.label || taskKindLabel(task?.kind) || i18n.t("ui.this_task");
        const confirmed = await confirmAction({
          title: i18n.t('ui.delete_task'),
          detail: i18n.t('ui.delete_task_detail', { label: label }),
          confirmLabel: i18n.t('ui.permanently_delete'),
          icon: 'x',
          tone: 'danger'
        });
        if (!confirmed) return;
        try {
          await api('tasks.center.delete', { task_id: taskID });
          if (state.taskCenterSelectedID === taskID) {
            state.taskCenterDetailRequest++;
            state.taskCenterSelectedID = '';
            state.taskCenterDetail = null;
            document.getElementById('taskDetailCard').classList.remove('mobile-open');
            if (window.history && window.history.replaceState) window.history.replaceState(null, '', '#tasks');
          }
          toast(i18n.t('ui.task_permanently_deleted'), 'success');
          await refreshTaskCenter(true);
        } catch (error) {
          toast(error.message, 'error');
          await refreshTaskCenter(true);
        }
        return;
      }
      const row = event.target.closest('[data-task-select]');
      if (row) selectTaskCenterTask(row.dataset.taskSelect, true);
    });
    document.getElementById('taskPager').addEventListener('click', (event) => {
      const button = event.target.closest('[data-task-page]');
      if (!button || button.disabled) return;
      const total = Number(state.taskCenter?.total || 0);
      const size = Number(state.taskCenterPageSize || state.taskCenter?.page_size || 20);
      const pages = Math.max(1, Math.ceil(total / size));
      const action = button.dataset.taskPage;
      if (action === 'first') state.taskCenterPage = 1;
      else if (action === 'prev') state.taskCenterPage = Math.max(1, state.taskCenterPage - 1);
      else if (action === 'next') state.taskCenterPage = Math.min(pages, state.taskCenterPage + 1);
      else if (action === 'last') state.taskCenterPage = pages;
      refreshTaskCenter(false);
    });
    document.getElementById('taskPager').addEventListener('change', (event) => {
      if (event.target.id !== 'taskPageSize') return;
      state.taskCenterPageSize = Number(event.target.value) || 20;
      state.taskCenterPage = 1;
      refreshTaskCenter(false);
    });
    document.getElementById('residentQueues').addEventListener('click', async (event) => {
      const open = event.target.closest('[data-task-queue]');
      if (open) { selectTaskCenterQueue(open.dataset.taskQueue, true); return; }
      const refresh = event.target.closest('[data-task-queue-refresh]');
      if (refresh) { await refreshTaskCenterQueue(refresh.dataset.taskQueueRefresh, true); }
    });
    document.getElementById('taskDetailClose').addEventListener('click', () => closeTaskCenterDetail(true));
    document.getElementById('residentQueueDetailClose').addEventListener('click', () => closeResidentQueueDetail(true));
    document.getElementById('taskDetailBody').addEventListener('change', (event) => {
      if (event.target.id !== 'taskItemStatusFilter') return;
      state.taskCenterDetailStatus = event.target.value;
      loadTaskCenterDetail(state.taskCenterSelectedID, 1, state.taskCenterDetailStatus, false);
    });
    async function handleTaskCenterClick(event) {
      const itemPage = event.target.closest('[data-task-item-page]');
      if (itemPage) {
        const page = Math.max(1, Number(state.taskCenterDetailPage || 1) + Number(itemPage.dataset.taskItemPage || 0));
        loadTaskCenterDetail(state.taskCenterSelectedID, page, state.taskCenterDetailStatus, false);
        return;
      }

      const queuePage = event.target.closest('[data-task-queue-page]');
      if (queuePage) {
        const page = Math.max(1, Number(state.taskQueuePage || 1) + Number(queuePage.dataset.taskQueuePage || 0));
        try { await loadTaskQueueDetail(state.taskCenterSelectedQueue, page); }
        catch (error) { toast(error.message, 'error'); }
        return;
      }
      const changePage = event.target.closest('[data-task-change-page]');
      if (changePage) {
        const change = state.taskCenterQueueDetail?.data;
        const page = Math.max(1, Number(change?.page || 1) + Number(changePage.dataset.taskChangePage || 0));
        try { await openTaskCenterChange(change?.change_id, page); }
        catch (error) { toast(error.message, 'error'); }
        return;
      }
      const back = event.target.closest('[data-task-queue-back]');
      if (back) {
        if (state.taskCenterSelectedQueue?.synthetic) closeResidentQueueDetail(true);
        else await loadTaskQueueDetail(state.taskCenterSelectedQueue, state.taskQueuePage || 1);
        return;
      }
      const changeDetail = event.target.closest('[data-task-change-detail]');
      if (changeDetail) {
        try { await openTaskCenterChange(changeDetail.dataset.taskChangeDetail, 1); }
        catch (error) { toast(error.message, 'error'); }
        return;
      }
      const changeRetry = event.target.closest('[data-task-change-retry]');
      if (changeRetry) {
        try {
          const task = await api('proxy.changes.retry', { change_id: changeRetry.dataset.taskChangeRetry });
          await waitForTask(task, i18n.t('ui.retry_proxy_sync'));
          await loadTaskQueueDetail(state.taskCenterSelectedQueue, state.taskQueuePage || 1);
          toast(i18n.t("ui.failed_items_retried"), 'success');
          refreshTaskCenter(true);
        } catch (error) { toast(error.message, 'error'); }
        return;
      }
      const cleanupRetry = event.target.closest('[data-task-cleanup-retry]');
      if (cleanupRetry) {
        try {
          await api('resin.cleanup.retry', { id: cleanupRetry.dataset.taskCleanupRetry });
          await loadTaskQueueDetail(state.taskCenterSelectedQueue, state.taskQueuePage || 1);
          toast(i18n.t("ui.cleanup_requeued"), 'success');
          refreshTaskCenter(true);
        } catch (error) { toast(error.message, 'error'); }
        return;
      }
      const cleanupPause = event.target.closest('[data-task-cleanup-pause]');
      if (cleanupPause) {
        try {
          await api('resin.cleanup.pause', { id: cleanupPause.dataset.taskCleanupPause, paused: cleanupPause.dataset.paused === 'true' });
          await loadTaskQueueDetail(state.taskCenterSelectedQueue, state.taskQueuePage || 1);
          toast(cleanupPause.dataset.paused === 'true' ? i18n.t("ui.cleanup_paused") : i18n.t("ui.cleanup_resumed"), 'success');
          refreshTaskCenter(true);
        } catch (error) { toast(error.message, 'error'); }
        return;
      }
      const queueAction = event.target.closest('[data-task-queue-action]');
      if (queueAction) {
        try {
          if (queueAction.dataset.taskQueueAction === 'auto_bind') {
            setTab('resin');
            setProxySubtab('bindings');
            window.setTimeout(() => setProxyAutoBindSettingsOpen(true), 0);
          } else if (queueAction.dataset.taskQueueAction === 'scan') {
            await api('proxy.auto_bind.run');
            toast(i18n.t('ui.auto_bind_scan_triggered_toast'), 'success');
            window.setTimeout(() => refreshTaskCenter(true), 1000);
          }
        } catch (error) { toast(error.message, 'error'); }
      }
    }
    document.getElementById('taskDetailBody').addEventListener('click', handleTaskCenterClick);
    document.getElementById('residentQueueDetail').addEventListener('click', handleTaskCenterClick);
    document.getElementById('accountProbeAllBtn').addEventListener('click', async (event) => {
      const button = event.currentTarget;
      const authIDs = selectedAccountIDs();
      const count = authIDs.length;
      if (!count) {
        syncAccountSelectionUI();
        toast(i18n.t("ui.select_accounts_to_probe"), 'success');
        return;
      }
      const probeConfirmed = await confirmAction({
        title: i18n.t("ui.probe_selected_accounts_confirm_title"),
        detail: i18n.t('ui.probe_selected_accounts_detail', { count: count }),
        confirmLabel: i18n.t("ui.start_probe"),
        icon: 'play',
        wide: true
      });
      if (!probeConfirmed) return;
      setBusy(button, true);
      try {
        const task = await api('accounts.probe_selected', { auth_ids: authIDs });
        toast(i18n.t("ui.probe_task_submitted"));
        const result = await waitForTask(task, i18n.t("ui.probe_selected_accounts_label"));
        const probed = Number(result && result.probed || 0);
        const restored = Number(result && result.restored || 0);
        const skipped = Number(result && result.skipped || 0);
        const failed = Number(result && result.failed || 0);
        let message = i18n.t('ui.probe_result_summary', { probed: probed, restored: restored ? i18n.t('ui.restored_then_probed', { count: restored }) : '', skipped: skipped ? i18n.t('ui.skipped_unprobeable', { count: skipped }) : '', failed: failed ? i18n.t('ui.failed_count_suffix', { count: failed }) : '' });
        if (failed) {
          const firstError = Array.isArray(result && result.items) ? result.items.find((item) => item && item.error) : null;
          if (firstError && firstError.error) message += i18n.t('ui.detail_separator') + friendlyApiMessage(firstError.error, firstError.error_code || firstError.code);
        }
        toast(message, failed > 0 ? 'error' : 'success');
        state.accountSelection.clear();
        await refreshAll(true);
      } catch (error) {
        toast(error.message, 'error');
      } finally {
        setBusy(button, false);
        syncAccountSelectionUI();
      }
    });
    document.getElementById('accountRestoreAllBtn').addEventListener('click', async (event) => {
      const button = event.currentTarget;
      const authIDs = selectedAccountIDs();
      const count = authIDs.length;
      if (!count) {
        syncAccountSelectionUI();
        toast(i18n.t("ui.select_accounts_to_restore"), 'success');
        return;
      }
      setBusy(button, true);
      try {
        const preview = await api('accounts.restore_preview', { auth_ids: authIDs });
        const externalCount = Number(preview && preview.external_disabled || 0);
        const optionsHTML = externalCount > 0
          ? '<div class="field"><label class="check-row"><input id="restoreExternalAccounts" type="checkbox"><span>' + i18n.t('ui.restore_external_count', { count: externalCount }) + '</span></label></div>'
          : '';
        const restoreConfirmed = await confirmAction({
          title: i18n.t("ui.restore_selected_accounts_confirm_title"),
          detail: i18n.t('ui.restore_selected_accounts_detail', { count: count }),
          confirmLabel: i18n.t('ui.restore_accounts'),
          icon: 'check',
          wide: true,
          optionsHTML: optionsHTML
        });
        if (!restoreConfirmed) return;
        const includeExternal = externalCount > 0 && Boolean(document.getElementById('restoreExternalAccounts')?.checked);
        const task = await api('accounts.restore_selected', { auth_ids: authIDs, include_external: includeExternal });
        toast(i18n.t("ui.restore_task_submitted"));
        const result = await waitForTask(task, i18n.t("ui.restore_selected_accounts_label"));
        const restored = Number(result && result.restored || 0);
        const externalRestored = Number(result && result.external_restored || 0);
        const skipped = Number(result && result.skipped || 0);
        const external = Number(result && result.external_left_disabled || 0);
        const failed = Number(result && result.failed || 0);
        const recovered = restored + externalRestored;
        let message = recovered > 0 ? i18n.t('ui.restore_result_summary', { count: recovered }) : i18n.t('ui.no_accounts_restored');
        if (externalRestored) message += i18n.t('ui.external_restored_suffix', { count: externalRestored });
        if (skipped) message += i18n.t('ui.healthy_skipped_suffix', { count: skipped });
        if (external) message += i18n.t('ui.external_cleanup_suffix', { count: external });
        if (failed) {
          message += i18n.t('ui.failed_count_suffix', { count: failed });
          const firstError = Array.isArray(result && result.items) ? result.items.find((item) => item && item.error) : null;
          if (firstError && firstError.error) message += i18n.t('ui.detail_separator') + friendlyApiMessage(firstError.error, firstError.error_code || firstError.code);
        }
        toast(message, failed > 0 ? 'error' : 'success');
        state.accountSelection.clear();
        await refreshAll(true);
      } catch (error) {
        toast(error.message, 'error');
      } finally {
        setBusy(button, false);
        syncAccountSelectionUI();
      }
    });
    document.getElementById('accountDisableAllBtn').addEventListener('click', async (event) => {
      const button = event.currentTarget;
      const authIDs = selectedAccountIDs();
      const count = authIDs.length;
      if (!count) {
        syncAccountSelectionUI();
        toast(i18n.t("ui.select_accounts_to_disable"), 'success');
        return;
      }
      setBusy(button, true);
      try {
        const disableConfirmed = await confirmAction({
          title: i18n.t("ui.disable_selected_accounts_confirm_title"),
          detail: i18n.t('ui.disable_selected_accounts_detail', { count: count }),
          confirmLabel: i18n.t("ui.disable_accounts"),
          icon: 'power',
          tone: 'danger',
          wide: true,
          optionsHTML: '<div class="field"><label class="check-row"><input id="disableModifyAuth" type="checkbox"><span>' + i18n.t('ui.modify_oauth_file') + '</span></label></div>'
        });
        if (!disableConfirmed) return;
        const modifyAuth = Boolean(document.getElementById('disableModifyAuth')?.checked);
        const task = await api('accounts.disable_selected', { auth_ids: authIDs, modify_auth: modifyAuth });
        toast(i18n.t("ui.disable_task_submitted"));
        const result = await waitForTask(task, i18n.t("ui.disable_selected_accounts_label"));
        const disabled = Number(result && result.disabled || 0);
        const skipped = Number(result && result.skipped || 0);
        const failed = Number(result && result.failed || 0);
        let message = disabled > 0 ? i18n.t('ui.disable_result_summary', { count: disabled }) : i18n.t('ui.no_accounts_disabled');
        message += modifyAuth ? i18n.t("ui.oauth_modified_suffix") : i18n.t("ui.plugin_only_suffix");
        if (skipped) message += i18n.t('ui.already_disabled_skipped_suffix', { count: skipped });
        if (failed) {
          message += i18n.t('ui.failed_count_suffix', { count: failed });
          const firstError = Array.isArray(result && result.items) ? result.items.find((item) => item && item.error) : null;
          if (firstError && firstError.error) message += i18n.t('ui.detail_separator') + friendlyApiMessage(firstError.error, firstError.error_code || firstError.code);
        }
        toast(message, failed > 0 ? 'error' : 'success');
        state.accountSelection.clear();
        await refreshAll(true);
      } catch (error) {
        toast(error.message, 'error');
      } finally {
        setBusy(button, false);
        syncAccountSelectionUI();
      }
    });
    document.getElementById('accountSearch').addEventListener('input', (event) => { state.accountQuery = event.target.value; state.accountPage = 1; scheduleListReload('accounts'); });
    document.getElementById('accountStateFilter').addEventListener('change', (event) => { state.accountState = event.target.value; state.accountPage = 1; scheduleListReload('accounts'); });
    document.getElementById('leaseSearch').addEventListener('input', (event) => { state.leaseQuery = event.target.value; state.leasePage = 1; scheduleListReload('leases'); });
    document.getElementById('leaseFilter').addEventListener('change', (event) => { state.leaseFilter = event.target.value; state.leasePage = 1; scheduleListReload('leases'); });
        document.getElementById('leaseSourceFilter').addEventListener('input', (event) => {
      const value = event.target.value.trim();
      const selected = applyLeaseSourceFilterInput(value);
      if (!selected) searchProxySourceChoices(value).catch((error) => toast(error.message, 'error'));
    });
    document.getElementById('eventSearch').addEventListener('input', (event) => { state.eventQuery = event.target.value; state.eventPage = 1; renderEvents(); });
    document.getElementById('eventTypeFilter').addEventListener('change', (event) => { state.eventType = event.target.value; state.eventPage = 1; renderEvents(); });
    document.getElementById('accountRows').addEventListener('click', (event) => {
      const open = event.target.closest('[data-open-account]');
      if (open) { openAccountDrawer(open.dataset.openAccount); return; }
      const action = event.target.closest('[data-account-action]');
      if (action) runAccountAction(action.dataset.accountAction, action.dataset.auth, action);
    });
    document.getElementById('accountRows').addEventListener('change', (event) => {
      const checkbox = event.target.closest('[data-account-select]');
      if (!checkbox) return;
      if (checkbox.checked) state.accountSelection.add(checkbox.dataset.accountSelect);
      else state.accountSelection.delete(checkbox.dataset.accountSelect);
      syncAccountSelectionUI();
    });
    document.getElementById('accountSelectAll').addEventListener('change', (event) => {
      const checked = event.currentTarget.checked;
      pagedItems('accounts').items.forEach((account) => {
        if (checked) state.accountSelection.add(account.auth_id);
        else state.accountSelection.delete(account.auth_id);
      });
      document.querySelectorAll('#accountRows [data-account-select]').forEach((checkbox) => {
        checkbox.checked = state.accountSelection.has(checkbox.dataset.accountSelect);
      });
      syncAccountSelectionUI();
    });
    document.getElementById('accountSelectFilteredBtn').addEventListener('click', (event) => selectListScope('accounts', false, event.currentTarget));
    document.getElementById('accountSelectAllBtn').addEventListener('click', (event) => selectListScope('accounts', true, event.currentTarget));
    document.getElementById('accountClearSelectionBtn').addEventListener('click', () => {
      state.accountSelection.clear();
      renderAccounts();
    });
    document.getElementById('drawerBody').addEventListener('click', (event) => {
      const accountAction = event.target.closest('[data-account-action]');
      if (accountAction) { runAccountAction(accountAction.dataset.accountAction, accountAction.dataset.auth, accountAction); return; }
      const proxyAction = event.target.closest('[data-proxy-action]');
      if (proxyAction) runProxyAction(proxyAction.dataset.proxyAction, proxyAction.dataset.auth, proxyAction);
    });
    document.getElementById('leaseRows').addEventListener('click', (event) => {
      const endpoint = event.target.closest('[data-lease-endpoint]');
      if (endpoint) { toggleLeaseEndpoint(endpoint.dataset.leaseEndpoint, endpoint); return; }
      const open = event.target.closest('[data-open-account]');
      if (open) { openAccountDrawer(open.dataset.openAccount); return; }
      const action = event.target.closest('[data-proxy-action]');
      if (action) runProxyAction(action.dataset.proxyAction, action.dataset.auth, action);
    });
    document.getElementById('proxySourceRows').addEventListener('change', (event) => {
      const checkbox = event.target.closest('[data-source-select]');
      if (!checkbox) return;
      if (checkbox.checked) state.proxySourceSelection.add(checkbox.dataset.sourceSelect);
      else state.proxySourceSelection.delete(checkbox.dataset.sourceSelect);
      syncProxySourceSelectionUI();
    });
    document.getElementById('proxySourceSelectAll').addEventListener('change', (event) => {
      const checked = event.currentTarget.checked;
      pagedItems('sources').items.forEach((source) => {
        if (checked) state.proxySourceSelection.add(source.id);
        else state.proxySourceSelection.delete(source.id);
      });
      renderProxySources();
    });
    document.getElementById('proxySourceSelectFilteredBtn').addEventListener('click', (event) => selectProxySourceScope(false, event.currentTarget));
    document.getElementById('proxySourceSelectAllBtn').addEventListener('click', (event) => selectProxySourceScope(true, event.currentTarget));
    document.getElementById('proxySourceClearSelectionBtn').addEventListener('click', () => {
      state.proxySourceSelection.clear();
      renderProxySources();
    });
    document.getElementById('proxySourceTestSelectedBtn').addEventListener('click', (event) => testSelectedProxySources(event.currentTarget));
    document.getElementById('proxySourceDeleteSelectedBtn').addEventListener('click', (event) => deleteSelectedProxySources(event.currentTarget));
    document.getElementById('proxySourceRows').addEventListener('click', (event) => {
      const endpoint = event.target.closest('[data-source-endpoint]');
      if (endpoint) { toggleSourceEndpoint(endpoint.dataset.sourceEndpoint, endpoint); return; }
      const edit = event.target.closest('[data-proxy-source-edit]');
      if (edit) { openProxySourceByID(edit.dataset.proxySourceEdit).catch((error) => toast(error.message, 'error')); return; }
      const apply = event.target.closest('[data-proxy-source-apply]');
      if (apply) { applyProxyTemplateSource(apply.dataset.proxySourceApply, apply); return; }
      const test = event.target.closest('[data-proxy-source-test]');
      if (test) { testProxySource(test.dataset.proxySourceTest, test); return; }
      const retry = event.target.closest('[data-proxy-source-retry]');
      if (retry) { retryProxySourceSync(retry.dataset.proxySourceRetry, retry); return; }
      const change = event.target.closest('[data-proxy-source-change]');
      if (change) { openProxyChangeDetails(change.dataset.proxySourceChange).catch((error) => toast(error.message, 'error')); return; }
      const remove = event.target.closest('[data-proxy-source-delete]');
      if (remove) { deleteProxySource(remove.dataset.proxySourceDelete, remove.dataset.kind); }
    });
    document.addEventListener('click', (event) => {
      const resinButton = event.target.closest('[data-open-resin]');
      if (!resinButton) return;
      setProxySubtab('sources');
      const existing = (state.proxySources || []).find((source) => source.kind === 'resin');
      openProxyResinEditor(existing || null).catch((error) => toast(error.message, 'error'));
    });
    document.getElementById('proxySourceEditor').addEventListener('click', async (event) => {
      const target = event.target.closest('button');
      if (!target) return;
      try {
        if (target.hasAttribute('data-proxy-node-add')) {
          const list = document.getElementById('proxyNodeRows');
          const editor = state.proxyEditing;
          if (!list || editor?.kind !== 'node') return;
          const index = editor.nextNodeRow++;
          list.insertAdjacentHTML('beforeend', proxyNodeRowHTML(index, {}, true));
          syncProxyNodeRows();
          markProxyEditorDirty();
          list.querySelector('[data-proxy-node-row]:last-child [data-proxy-node-url]')?.focus();
          return;
        }
        if (target.hasAttribute('data-proxy-node-remove')) {
          const row = target.closest('[data-proxy-node-row]');
          const list = document.getElementById('proxyNodeRows');
          if (!row || !list || list.querySelectorAll('[data-proxy-node-row]').length <= 1) return;
          row.remove();
          syncProxyNodeRows();
          markProxyEditorDirty();
          list.querySelector('[data-proxy-node-row]:last-child [data-proxy-node-url]')?.focus();
          return;
        }
        const card = target.closest('[data-group-card]');
        const group = card && state.proxyEditing?.groups.get(Number(card.dataset.groupCard));
        if (!group) return;
        if (target.hasAttribute('data-group-view')) { group.view = target.dataset.groupView; renderProxyGroup(group.index); return; }
        if (target.hasAttribute('data-bulk-fill-group')) { bulkFillProxyGroup(group.index); return; }
        if (target.hasAttribute('data-remove-group')) {
          if (group.rows.length && !await confirmAction({ title: i18n.t('ui.remove_group'), detail: i18n.t('ui.remove_group_detail', { count: group.rows.length }), confirmLabel: i18n.t('ui.remove_group'), tone: 'danger' })) return;
          document.querySelectorAll('[data-placeholder-group]').forEach((field) => { if (field.value === group.id) detachProxyPlaceholderGroup(field, group); });
          state.proxyEditing.groups.delete(group.index);
          card.remove(); markProxyEditorDirty(); syncProxyPlaceholderCards(); renderProxyTemplateConditions(); return;
        }
        if (target.hasAttribute('data-group-table-page')) { group.page += Number(target.dataset.groupTablePage); renderProxyGroup(group.index); return; }
        if (target.hasAttribute('data-group-child-page')) { group.childPage += Number(target.dataset.groupChildPage); renderProxyGroup(group.index); return; }
        if (target.hasAttribute('data-group-add-row')) { group.rows.push(group.columns.map(() => '')); group.page = Math.ceil(group.rows.length / 20); group.view = 'table'; markProxyEditorDirty(); renderProxyGroup(group.index); return; }
        if (target.hasAttribute('data-group-delete-row')) { group.rows.splice(Number(target.dataset.groupDeleteRow), 1); markProxyEditorDirty(); renderProxyGroup(group.index); syncProxyPlaceholderCards(); renderProxyTemplateConditions(); return; }
        if (target.hasAttribute('data-group-import-apply')) { await importProxyGroupRows(group); return; }
        if (target.hasAttribute('data-group-delete-path') || target.hasAttribute('data-group-delete-value')) { await deleteProxyGroupPath(group, target.hasAttribute('data-group-delete-value') ? target.dataset.groupDeleteValue : null); return; }
        if (target.hasAttribute('data-group-rename-path')) { await renameProxyGroupPath(group, card); return; }
        if (target.hasAttribute('data-group-copy-path')) { await copyProxyGroupPath(group, card); return; }
        if (target.hasAttribute('data-group-rename-child')) { await renameProxyGroupLeaf(group, card); return; }
        if (target.hasAttribute('data-group-cancel-child')) { group.leafDrafts.delete(JSON.stringify(group.editPrefix.concat(group.editValue))); group.editValue = null; renderProxyGroup(group.index); return; }
        if (target.hasAttribute('data-group-breadcrumb')) { group.childIndex = Number(target.dataset.groupBreadcrumb); group.childPage = 1; group.search = ''; renderProxyGroup(group.index); return; }
        if (target.hasAttribute('data-group-drill-value')) {
          if (group.childIndex < group.columns.length - 1) { group.path[group.childIndex] = target.dataset.groupDrillValue; group.path = group.path.map((value, index) => index > group.childIndex ? '' : value); group.childIndex += 1; group.childPage = 1; group.search = ''; renderProxyGroup(group.index); }
          else { group.editValue = target.dataset.groupDrillValue; group.editPrefix = group.path.slice(0, group.childIndex); group.editDraft = group.leafDrafts.get(JSON.stringify(group.editPrefix.concat(group.editValue))) ?? group.editValue; renderProxyGroup(group.index); }
          return;
        }
        if (target.hasAttribute('data-group-export')) {
          const blob = new Blob(['\uFEFF' + formatProxyDelimitedTable(group.columns, group.rows, ',')], { type: 'text/csv;charset=utf-8' });
          const url = URL.createObjectURL(blob);
          const link = document.createElement('a'); link.href = url; link.download = (group.id || 'proxy-values') + '.csv'; link.click(); URL.revokeObjectURL(url); return;
        }
      } catch (error) { toast(error.message, 'error'); }
    });
    document.getElementById('proxySourceEditor').addEventListener('input', (event) => {
      const field = event.target;
      if (field.hasAttribute('data-proxy-assignment')) {
        state.proxyEditing.assignments[field.dataset.proxyAssignment] = field.value.trim();
        state.proxyPreviewSequence += 1;
        updateProxyTemplateConditionLists();
        const output = document.getElementById('proxyTemplatePreviewResults');
        if (output?.innerHTML) output.innerHTML = '<p class="hint">' + i18n.t('ui.preview_condition_stale') + '</p>';
        return;
      }
      const card = field.closest('[data-group-card]');
      const group = card && state.proxyEditing?.groups.get(Number(card.dataset.groupCard));
      if (group) {
        if (field.hasAttribute('data-group-bulk-values')) group.bulkDrafts.set(proxyGroupBulkKey(group), field.value);
        if (field.hasAttribute('data-group-import-text')) { group.importText = field.value; group.importRevision += 1; }
        if (field.hasAttribute('data-group-leaf-rename')) { group.editDraft = field.value; group.leafDrafts.set(JSON.stringify(group.editPrefix.concat(group.editValue)), field.value); }
        if (field.hasAttribute('data-group-cell-row')) { group.rows[Number(field.dataset.groupCellRow)][Number(field.dataset.groupCellColumn)] = field.value; if (field.value.trim()) field.removeAttribute('aria-invalid'); }
        if (field.hasAttribute('data-group-path') || field.hasAttribute('data-group-child-search') || field.hasAttribute('data-group-rename-value') || field.hasAttribute('data-group-copy-target')) return;
      }
      markProxyEditorDirty();
    });
    document.getElementById('proxySourceEditor').addEventListener('change', async (event) => {
      const field = event.target;
      if (field.hasAttribute('data-proxy-assignment')) { updateProxyTemplateConditionLists(); return; }
      if (field.hasAttribute('data-enum-separator')) { changeProxyEnumSeparator(field); markProxyEditorDirty(); renderProxyTemplateConditions(); return; }
      if (field.hasAttribute('data-placeholder-kind') || field.hasAttribute('data-placeholder-group') || field.hasAttribute('data-enum-input')) {
        if (field.hasAttribute('data-placeholder-group')) {
          const previous = Array.from(state.proxyEditing.groups.values()).find((group) => group.id === field.dataset.previousGroup);
          if (!field.value.trim() && previous) detachProxyPlaceholderGroup(field, previous);
          field.dataset.previousGroup = field.value.trim();
        }
        syncProxyPlaceholderCards(); renderProxyTemplateConditions(); markProxyEditorDirty(); return;
      }
      const card = field.closest('[data-group-card]');
      const group = card && state.proxyEditing?.groups.get(Number(card.dataset.groupCard));
      if (!group) { markProxyEditorDirty(); return; }
      try {
        if (field.hasAttribute('data-group-columns')) { await changeProxyGroupColumns(card, group, field); return; }
        if (field.hasAttribute('data-group-id')) { const old = group.id; group.id = field.value.trim(); if (old) document.querySelectorAll('[data-placeholder-group]').forEach((input) => { if (input.value === old) { input.value = group.id; input.dataset.previousGroup = group.id; } }); syncProxyPlaceholderCards(); renderProxyTemplateConditions(); }
        if (field.hasAttribute('data-group-name')) group.name = field.value.trim();
        if (field.hasAttribute('data-group-path')) { const column = Number(field.dataset.groupPath); group.path[column] = field.value.trim(); group.path = group.path.map((value, index) => index > column ? '' : value); group.childPage = 1; renderProxyGroup(group.index); return; }
        if (field.hasAttribute('data-group-child-column')) { group.childIndex = Number(field.value); group.search = ''; group.childPage = 1; renderProxyGroup(group.index); return; }
        if (field.hasAttribute('data-group-child-search')) { group.search = field.value; group.childPage = 1; renderProxyGroup(group.index); return; }
        if (field.hasAttribute('data-group-import-format')) { group.importFormat = field.value; group.importRevision += 1; }
        if (field.hasAttribute('data-group-import-mode')) { group.importMode = field.value; group.importRevision += 1; }
        if (field.hasAttribute('data-group-import-header')) { group.importHeader = field.checked; group.importRevision += 1; }
        if (field.hasAttribute('data-group-import-file')) {
          const file = field.files?.[0];
          if (file) { if (file.size > PROXY_IMPORT_MAX_BYTES) throw new Error(i18n.t("ui.file_too_large")); const editor = state.proxyEditing; const text = await file.text(); if (state.proxyEditing !== editor) return; group.importText = text; group.importFormat = /\.tsv$/i.test(file.name) ? 'tsv' : 'csv'; group.importRevision += 1; renderProxyGroup(group.index); }
        }
        if (field.hasAttribute('data-group-cell-row')) { syncProxyPlaceholderCards(); updateProxyTemplateConditionLists(); }
        markProxyEditorDirty();
      } catch (error) { group.error = error.message; renderProxyGroup(group.index); }
    });
    document.getElementById('leaseRows').addEventListener('change', (event) => {
      const checkbox = event.target.closest('[data-lease-select]');
      if (!checkbox) return;
      if (checkbox.checked) state.leaseSelection.add(checkbox.dataset.leaseSelect);
      else state.leaseSelection.delete(checkbox.dataset.leaseSelect);
      syncLeaseSelectionUI();
    });
    document.getElementById('leaseSelectAll').addEventListener('change', (event) => {
      const checked = event.currentTarget.checked;
      pagedItems('leases').items.forEach((account) => {
        if (checked) state.leaseSelection.add(account.auth_id);
        else state.leaseSelection.delete(account.auth_id);
      });
      document.querySelectorAll('#leaseRows [data-lease-select]').forEach((checkbox) => {
        checkbox.checked = state.leaseSelection.has(checkbox.dataset.leaseSelect);
      });
      syncLeaseSelectionUI();
    });
    document.getElementById('selectFilteredBtn').addEventListener('click', (event) => selectListScope('leases', false, event.currentTarget));
    document.getElementById('selectAllAccountsBtn').addEventListener('click', (event) => selectListScope('leases', true, event.currentTarget));
    document.getElementById('bindSelectedBtn').addEventListener('click', (event) => runSelectedProxyAction('bind', event.currentTarget));
    document.getElementById('clearSelectedBtn').addEventListener('click', (event) => runSelectedProxyAction('clear', event.currentTarget));
    document.getElementById('pauseSelectedAutoBindBtn').addEventListener('click', (event) => setProxyAutoBindSelection(Array.from(state.leaseSelection), true, event.currentTarget));
    document.getElementById('resumeSelectedAutoBindBtn').addEventListener('click', (event) => setProxyAutoBindSelection(Array.from(state.leaseSelection), false, event.currentTarget));
    document.getElementById('clearLeaseSelectionBtn').addEventListener('click', () => {
      state.leaseSelection.clear();
      renderResin();
    });
    document.getElementById('bindUnboundBtn').addEventListener('click', (event) => bindAllUnbound(event.currentTarget));
    document.getElementById('proxyAutoBindRunBtn').addEventListener('click', (event) => runProxyAutoBindNow(event.currentTarget));
    function setProxyAutoBindSettingsOpen(open) {
      const panel = document.getElementById('proxyAutoBindSettings');
      const toggle = document.getElementById('proxyAutoBindToggleSettingsBtn');
      if (!panel || !toggle) return;
      panel.hidden = !open;
      toggle.textContent = i18n.t(open ? 'ui.collapse_settings' : 'ui.settings');
      toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (open) populateProxyAutoBindForm();
      else toggle.focus();
    }
    document.getElementById('proxyAutoBindToggleSettingsBtn').addEventListener('click', () => {
      setProxyAutoBindSettingsOpen(document.getElementById('proxyAutoBindSettings').hidden);
    });
    document.getElementById('proxyAutoBindCloseBtn').addEventListener('click', () => setProxyAutoBindSettingsOpen(false));
    document.getElementById('proxyAutoBindForm').addEventListener('submit', (event) => {
      event.preventDefault();
      saveProxyAutoBind(event.submitter || document.querySelector('#proxyAutoBindForm button[type="submit"]'));
    });
    document.getElementById('proxyAddSourceBtn').addEventListener('click', () => {
      const kind = valueOf('proxyAddSourceKind');
      const open = kind === 'template' ? openProxyTemplateEditor(null) : (kind === 'resin' ? openProxyResinEditor(null) : openProxyNodeEditor(null));
      open.catch((error) => toast(error.message, 'error'));
    });
    document.getElementById('proxyImportNodesBtn').addEventListener('click', () => openProxyImportEditor().catch((error) => toast(error.message, 'error')));
    document.getElementById('proxySourceRefreshBtn').addEventListener('click', (event) => refreshResin(event.currentTarget));
    document.getElementById('proxySourceSearch').addEventListener('input', (event) => { state.proxySourceQuery = event.target.value; state.proxySourcePage = 1; scheduleListReload('sources'); });
    document.getElementById('proxySourceFilter').addEventListener('change', (event) => { state.proxySourceFilter = event.target.value; state.proxySourcePage = 1; scheduleListReload('sources'); });
    document.getElementById('proxySourceEnabledFilter').addEventListener('change', (event) => { state.proxySourceEnabledFilter = event.target.value; state.proxySourcePage = 1; scheduleListReload('sources'); });
    document.getElementById('proxySourceHealthFilter').addEventListener('change', (event) => { state.proxySourceHealthFilter = event.target.value; state.proxySourcePage = 1; scheduleListReload('sources'); });
    document.getElementById('policyForm').addEventListener('input', (event) => {
      if (event.target?.id) validatePolicyField(event.target.id);
    });
    document.getElementById('policyForm').addEventListener('change', (event) => {
      if (event.target?.id) validatePolicyField(event.target.id);
    });
    document.getElementById('policyForm').addEventListener('submit', (event) => {
      event.preventDefault();
      savePolicy(event.submitter || document.querySelector('#policyForm button[type="submit"]'));
    });
    document.getElementById('policyResetBtn').addEventListener('click', async (event) => {
      const confirmed = await confirmAction({
        title: i18n.t("ui.reset_policy"),
        detail: i18n.t("ui.reset_policy_detail"),
        confirmLabel: i18n.t("ui.reset_policy"),
        icon: 'refresh',
        tone: 'danger'
      });
      if (!confirmed) return;
      const button = event.currentTarget;
      setBusy(button, true);
      try {
        await api('policy.reset');
        clearFormDraft('policyForm');
        toast(i18n.t("ui.policy_reset_toast"), 'success');
        await refreshAll(true);
      } catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    });
    document.addEventListener('click', (event) => {
      const button = event.target.closest('[data-page-kind][data-page-action]');
      if (!button) return;
      applyPagerAction(button.dataset.pageKind, button.dataset.pageAction);
    });
    document.addEventListener('change', (event) => {
      const select = event.target.closest('[data-page-size-kind]');
      if (!select) return;
      const kind = select.dataset.pageSizeKind;
      const config = PAGE_CONFIG[kind];
      if (!config) return;
      state[config.sizeKey] = Number(select.value);
      state[config.pageKey] = 1;
      if (config.metadata) reloadList(kind);
      else config.render();
    });
    document.addEventListener('pointerover', (event) => {
      const tip = event.target.closest?.('.help-tip');
      if (!tip || tip.contains(event.relatedTarget)) return;
      showHelpTooltip(tip);
    });
    document.addEventListener('pointerout', (event) => {
      const tip = event.target.closest?.('.help-tip');
      if (!tip || tip.contains(event.relatedTarget)) return;
      hideHelpTooltip(tip);
    });
    document.addEventListener('focusin', (event) => {
      const tip = event.target.closest?.('.help-tip');
      if (tip) showHelpTooltip(tip);
    });
    document.addEventListener('focusout', (event) => {
      const tip = event.target.closest?.('.help-tip');
      if (tip) hideHelpTooltip(tip);
    });
    document.addEventListener('scroll', () => hideHelpTooltip(), true);
    window.addEventListener('resize', () => hideHelpTooltip());
    document.getElementById('drawerClose').addEventListener('click', closeDrawer);
    document.getElementById('accountDrawer').addEventListener('click', (event) => { if (event.target === event.currentTarget) closeDrawer(); });
    document.getElementById('proxySourceEditorClose').addEventListener('click', closeProxyDrawer);
    document.getElementById('proxySourceDrawer').addEventListener('click', (event) => { if (event.target === event.currentTarget) closeProxyDrawer(); });
    document.getElementById('confirmCancel').addEventListener('click', () => settleConfirm(false));
    document.getElementById('confirmAccept').addEventListener('click', () => settleConfirm(true));
    document.getElementById('confirmDialog').addEventListener('click', (event) => { if (event.target === event.currentTarget) settleConfirm(false); });
