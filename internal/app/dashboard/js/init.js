    function trapDialogFocus(event, dialog) {
      if (event.key !== 'Tab') return;
      const focusable = Array.from(dialog.querySelectorAll('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href], [tabindex="0"]')).filter((element) => !element.closest('[hidden]') && element.getClientRects().length);
      if (!focusable.length) return;
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    }
    document.addEventListener('keydown', (event) => {
      const confirmDialog = document.getElementById('confirmDialog');
      if (!confirmDialog.hidden) {
        if (event.key === 'Escape') {
          event.preventDefault();
          settleConfirm(false);
          return;
        }
        trapDialogFocus(event, confirmDialog);
        return;
      }
      const proxyDrawer = document.getElementById('proxySourceDrawer');
      const accountDrawer = document.getElementById('accountDrawer');
      if (!proxyDrawer.hidden) { if (event.key === 'Escape') closeProxyDrawer(); else trapDialogFocus(event, proxyDrawer); }
      else if (!accountDrawer.hidden) { if (event.key === 'Escape') closeDrawer(); else trapDialogFocus(event, accountDrawer); }
      else if (state.activeTab === 'tasks' && state.taskCenterSelectedQueue && event.key === 'Escape') { event.preventDefault(); closeResidentQueueDetail(true); }
      else if (document.getElementById('taskDetailCard').classList.contains('mobile-open') && event.key === 'Escape') { event.preventDefault(); closeTaskCenterDetail(true); }
      else if (!document.getElementById('proxyAutoBindSettings').hidden && event.key === 'Escape') { event.preventDefault(); setProxyAutoBindSettingsOpen(false); }
    });
    function applyLocationHash() {
      const hash = (window.location.hash || '#overview').slice(1);
      if (hash === 'settings/resin') {
        setTab('resin', false);
        setProxySubtab('sources', false);
        const existing = (state.proxySources || []).find((source) => source.kind === 'resin');
        openProxyResinEditor(existing || null).catch((error) => toast(error.message, 'error'));
        return;
      }
      if (hash.startsWith('events/')) {
        const segments = hash.slice('events/'.length).split('/');
        let authID = '';
        let eventType = 'all';
        try { authID = decodeURIComponent(segments[0] || ''); } catch (_) {}
        try { eventType = decodeURIComponent(segments[1] || 'all'); } catch (_) {}
        if (!['all', 'quality', 'transport', 'account', 'inconclusive', 'ignored', 'proxy', 'task'].includes(eventType)) eventType = 'all';
        state.eventQuery = authID;
        state.eventType = eventType;
        state.eventPage = 1;
        document.getElementById('eventSearch').value = authID;
        document.getElementById('eventTypeFilter').value = eventType;
        setTab('events', false);
        return;
      }
      if (hash === 'tasks' || hash.startsWith('tasks/')) {
        const encodedTaskID = hash.startsWith('tasks/') ? hash.slice('tasks/'.length) : '';
        setTab('tasks', false);
        if (encodedTaskID) {
          let taskID = encodedTaskID;
          try { taskID = decodeURIComponent(encodedTaskID); } catch (_) {}
          selectTaskCenterTask(taskID, false);
        }
        return;
      }
      if (hash.startsWith('settings/')) { setTab('settings', false); openSettings(hash.slice('settings/'.length)); return; }
      setTab(hash, false);
    }
    window.addEventListener('hashchange', applyLocationHash);
    trackFormDraft('policyForm', populatePolicyForm);
    trackFormDraft('proxyAutoBindForm', populateProxyAutoBindForm);
    window.addEventListener('beforeunload', (event) => {
      if (state.proxyEditing?.dirty || Array.from(formDrafts.values()).some((draft) => draft.dirty)) { event.preventDefault(); event.returnValue = ''; }
    });

    window.setInterval(() => {
      if (state.activeTab !== 'tasks' || document.hidden) return;
      const counts = state.taskCenter?.counts || {};
      const queuesRunning = (state.taskCenter?.resident_queues || []).some((queue) => queue.status === 'running' || Number(queue.running || 0) > 0 || Number(queue.waiting || 0) > 0);
      refreshTaskCenterQueues(true);
      if (Number(counts.active || 0) > 0 || Number(counts.running_items || 0) > 0 || queuesRunning) refreshTaskCenter(true);
    }, 5000);
    function relocalizeDashboard() {
      renderAll();
      if (state.taskCenter) renderTaskCenter();
      i18n.translateTree(document.body);
    }
    document.getElementById('uiLanguage').value = i18n.locale;
    document.getElementById('uiLanguage').addEventListener('change', (event) => {
      i18n.applyLocale(event.target.value, {
        persist: true,
        afterApply: () => {
          relocalizeDashboard();
          refreshAll(true).catch((error) => toast(error.message, 'error'));
        }
      });
    });
    if (!i18n.hasResources()) {
      i18n.ensureResources().then(() => {
        i18n.applyLocale(i18n.locale);
        relocalizeDashboard();
      }).catch((error) => {
        console.error('i18n resources failed to load', error);
      });
    }
    i18n.observe();
    i18n.applyLocale(i18n.locale);
    applyLocationHash();
  