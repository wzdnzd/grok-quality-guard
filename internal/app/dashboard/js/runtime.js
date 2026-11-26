    function toast(message, type) {
      const el = document.getElementById('toast');
      el.textContent = message;
      el.className = 'toast ' + (type || '');
      el.hidden = false;
      window.clearTimeout(toastTimer);
      toastTimer = window.setTimeout(() => { el.hidden = true; }, 4200);
    }
    let confirmResolver = null;
    let confirmPreviousFocus = null;
    function confirmAction(options) {
      const config = options || {};
      const dialog = document.getElementById('confirmDialog');
      const accept = document.getElementById('confirmAccept');
      const cancel = document.getElementById('confirmCancel');
      const detail = document.getElementById('confirmDetail');
      if (!dialog.hidden) settleConfirm(false);
      document.getElementById('confirmTitle').textContent = config.title || i18n.t("ui.confirm_operation");
      const detailText = String(config.detail || '').trim();
      detail.textContent = detailText;
      document.getElementById('confirmOptions').innerHTML = config.optionsHTML || '';
      detail.hidden = detailText === '';
      accept.textContent = config.confirmLabel || i18n.t("ui.confirm");
      cancel.textContent = config.cancelLabel || i18n.t("ui.cancel");
      const danger = config.tone === 'danger';
      document.getElementById('confirmCard').className = 'confirm-card' + (config.wide ? ' wide' : '');
      accept.className = 'btn ' + (danger ? 'btn-danger' : 'btn-primary');
      document.getElementById('confirmIcon').className = 'confirm-icon' + (danger ? ' danger' : '');
      document.getElementById('confirmIconUse').setAttribute('href', '#i-' + (config.icon || 'alert'));
      confirmPreviousFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      dialog.hidden = false;
      document.body.classList.add('confirm-open');
      return new Promise((resolve) => {
        confirmResolver = resolve;
        window.setTimeout(() => (danger ? cancel : accept).focus(), 20);
      });
    }
    function settleConfirm(accepted) {
      const dialog = document.getElementById('confirmDialog');
      if (!dialog || dialog.hidden) return;
      dialog.hidden = true;
      document.body.classList.remove('confirm-open');
      const resolve = confirmResolver;
      confirmResolver = null;
      const previousFocus = confirmPreviousFocus;
      confirmPreviousFocus = null;
      if (previousFocus && typeof previousFocus.focus === 'function') previousFocus.focus();
      if (resolve) resolve(Boolean(accepted));
    }
    function setBusy(button, busy) {
      if (!button) return;
      button.disabled = busy;
      const svg = button.querySelector('svg');
      if (svg) svg.classList.toggle('spin', busy);
    }
    function trackFormDraft(id, populate) {
      const form = document.getElementById(id);
      const draft = { dirty: false, revision: 0, populate: populate };
      formDrafts.set(id, draft);
      const notice = document.createElement('span');
      notice.className = 'draft-note';
      notice.dataset.draftNote = id;
      notice.setAttribute('role', 'status');
      notice.setAttribute('aria-live', 'polite');
      const discard = document.createElement('button');
      discard.type = 'button';
      discard.className = 'btn btn-xs btn-ghost';
      discard.textContent = i18n.t("ui.discard_unsaved_changes");
      discard.hidden = true;
      discard.dataset.draftDiscard = id;
      form.querySelector('.form-actions').append(notice, discard);
      const mark = () => {
        draft.dirty = true;
        draft.revision += 1;
        notice.textContent = i18n.t("ui.unsaved_changes");
        discard.hidden = false;
      };
      form.addEventListener('input', mark);
      form.addEventListener('change', mark);
      discard.addEventListener('click', async () => {
        if (!await confirmAction({ title: i18n.t("ui.discard_unsaved_changes"), detail: i18n.t("ui.loading_saved_configuration"), confirmLabel: i18n.t("ui.discard_changes"), tone: 'danger' })) return;
        clearFormDraft(id);
        populate();
      });
    }
    function isFormDirty(id) { return Boolean(formDrafts.get(id)?.dirty); }
    function formDraftRevision(id) { return formDrafts.get(id)?.revision || 0; }
    function clearFormDraft(id, revision) {
      const draft = formDrafts.get(id);
      if (!draft || (revision != null && draft.revision !== revision)) return false;
      draft.dirty = false;
      const note = document.querySelector('[data-draft-note="' + id + '"]');
      if (note) note.textContent = '';
      const discard = document.querySelector('[data-draft-discard="' + id + '"]');
      if (discard) discard.hidden = true;
      return true;
    }
    function markProxyEditorDirty() {
      const editor = state.proxyEditing;
      if (!editor || editor.kind === 'tasks') return;
      editor.dirty = true;
      editor.revision += 1;
      state.proxyPreviewSequence += 1;
      const output = document.getElementById('proxyTemplatePreviewResults');
      if (output && output.innerHTML) output.innerHTML = '<p class="hint">' + i18n.t('ui.preview_stale') + '</p>';
    }
    function replaceProxyHTML(container, html) {
      if (container.proxyRenderHTML === html) return;
      const active = document.activeElement;
      const inside = container.contains?.(active);
      const selector = inside ? (active.id ? '#' + CSS.escape(active.id) : Object.entries(active.dataset || {}).map(([key, value]) => '[data-' + key.replace(/[A-Z]/g, (letter) => '-' + letter.toLowerCase()) + '="' + CSS.escape(String(value)) + '"]').join('')) : '';
      const scroll = container.scrollTop;
      const selection = inside ? [active.selectionStart, active.selectionEnd] : [];
      container.innerHTML = html;
      container.proxyRenderHTML = html;
      container.scrollTop = scroll;
      if (!inside) return;
      const next = (selector && container.querySelector(selector)) || container.querySelector('button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled])');
      if (next && !next.disabled) {
        next.focus({ preventScroll: true });
        if (typeof next.setSelectionRange === 'function' && typeof selection[0] === 'number') { try { next.setSelectionRange(...selection); } catch (_) { } }
      }
    }
    function beginProxyEditor(kind, source) {
      document.getElementById('proxySourceEditor').proxyRenderHTML = undefined;
      state.proxyEditing = { kind: kind, source: source, dirty: false, revision: 0, sequence: ++state.proxyEditorSequence, groups: new Map(), nextGroupIndex: 0, assignments: {}, previousFocus: document.activeElement };
      setProxyEditorMeta(null);
      return state.proxyEditing;
    }
    function friendlyApiMessage(message, code) {
      const text = String(message || '').trim();
      for (const candidate of [code, text]) {
        if (hasCatalogKey(candidate)) return i18n.t(candidate);
        for (const prefix of ['api_error.', 'api.error.']) {
          const key = catalogKey(prefix, candidate);
          if (hasCatalogKey(key)) return i18n.t(key);
        }
      }
      if (/failed to fetch|networkerror|load failed|network request failed/i.test(text)) return i18n.t('error.network');
      if (!text) return i18n.t('error.generic');
      if (!CJK_TEXT.test(text)) return i18n.t('error.generic');
      return i18n.translateSource(text);
    }
    async function api(action, params, options) {
      let response;
      try {
        response = await fetch(API, {
          method: 'POST',
          credentials: 'same-origin',
          signal: options && options.signal,
          headers: { 'Content-Type': 'application/json', 'Accept-Language': i18n.locale },
          body: JSON.stringify({ action: action, params: params || {} })
        });
      } catch (cause) {
        if (cause && cause.name === 'AbortError') {
          const error = new Error('request aborted');
          error.code = 'aborted';
          error.cause = cause;
          throw error;
        }
        const error = new Error(i18n.t('error.network'));
        error.code = 'network_error';
        error.cause = cause;
        throw error;
      }
      let payload = null;
      try { payload = await response.json(); } catch (_) {
        const error = new Error(response.ok ? i18n.t('error.invalid_response') : i18n.t('error.unavailable', { status: response.status }));
        error.status = response.status;
        throw error;
      }
      if (!payload || !payload.ok) {
        const rawCode = payload && payload.error && payload.error.code;
        const rawMessage = payload && payload.error && payload.error.message ? payload.error.message : i18n.t('error.request_failed', { status: response.status });
        const error = new Error(friendlyApiMessage(rawMessage, rawCode));
        error.code = rawCode;
        error.status = response.status;
        const retryAfter = Number(response.headers.get('Retry-After') || 0);
        error.retryAfter = Number.isFinite(retryAfter) && retryAfter > 0 ? retryAfter * 1000 : 0;
        error.details = payload && payload.error && payload.error.details;
        error.path = payload && payload.error && payload.error.path;
        throw error;
      }
      return payload.data;
    }
    function abortError() {
      const error = new Error('request aborted');
      error.code = 'aborted';
      return error;
    }
    function retryableReadError(error) {
      return error && (error.code === 'catalog_loading' || error.code === 'busy');
    }
    function waitForRetry(delay, signal) {
      if (!delay) return Promise.resolve();
      if (signal && signal.aborted) return Promise.reject(abortError());
      return new Promise((resolve, reject) => {
        const timer = window.setTimeout(() => {
          if (signal) signal.removeEventListener('abort', onAbort);
          resolve();
        }, delay);
        const onAbort = () => {
          window.clearTimeout(timer);
          if (signal) signal.removeEventListener('abort', onAbort);
          reject(abortError());
        };
        if (signal) signal.addEventListener('abort', onAbort, { once: true });
      });
    }
    async function apiWithRetry(action, params, options, maxRetries) {
      const retries = Number.isFinite(maxRetries) ? Math.max(0, maxRetries) : 2;
      for (let attempt = 0; ; attempt++) {
        try {
          return await api(action, params, options);
        } catch (error) {
          if (!retryableReadError(error) || attempt >= retries) throw error;
          const delay = Math.min(30000, Math.max(500, Number(error.retryAfter || 0) || 1000 * (attempt + 1)));
          await waitForRetry(delay, options && options.signal);
          if (options && options.signal && options.signal.aborted) throw abortError();
        }
      }
    }
    function rememberProxyTask(task, label) {
      if (!task || !task.task_id) return;
      state.proxyTasks.set(task.task_id, Object.assign({ label: label }, task));
      while (state.proxyTasks.size > 50) state.proxyTasks.delete(state.proxyTasks.keys().next().value);
    }

    async function waitForTask(value, label) {
      let task = value;
      if (!task || !task.task_id) return value;
      toast(i18n.t('ui.submitted_view_task_center', { label: label || i18n.t('ui.task') }));
      rememberProxyTask(task, label);
      if (state.activeTab === 'tasks') refreshTaskCenter(true);
      const deadline = Date.now() + 30 * 60 * 1000;
      while (task.status === 'queued' || task.status === 'running') {
        if (Date.now() > deadline) throw new Error(i18n.t('ui.wait_timeout_task_center', { label: label || i18n.t('ui.task') }));
        await new Promise((resolve) => window.setTimeout(resolve, 1000));
        task = await api('tasks.get', { task_id: task.task_id });
        rememberProxyTask(task, label);
        if (state.activeTab === 'tasks') renderTaskCenter();
      }
      if (task.status === 'failed' || task.status === 'cancelled' || task.status === 'canceled' || task.status === 'interrupted') {
        const error = new Error(task.error || i18n.t(task.status === 'cancelled' ? 'ui.task_cancelled_label' : 'ui.task_center_failed_suffix', { label: label || i18n.t('ui.task') }));
        error.result = task.result;
        throw error;
      }
      if (state.activeTab === 'tasks') refreshTaskCenter(true);
      return task.result;
    }

    function setTab(name, updateHash) {
      const allowed = ['overview', 'accounts', 'resin', 'tasks', 'events', 'settings'];
      if (!allowed.includes(name)) name = 'overview';
      state.activeTab = name;
      document.querySelectorAll('.tabs [data-tab]').forEach((button) => {
        const active = button.dataset.tab === name;
        button.classList.toggle('active', active);
        button.setAttribute('aria-selected', active ? 'true' : 'false');
        button.tabIndex = active ? 0 : -1;
      });
      document.querySelectorAll('main > .tab-panel').forEach((panel) => {
        panel.classList.toggle('active', panel.id === 'panel-' + name);
      });
      if (name !== 'tasks') document.getElementById('taskDetailCard').classList.remove('mobile-open');
      if (updateHash !== false && window.history && window.history.replaceState) {
        const hash = name === 'tasks' && state.taskCenterSelectedID ? 'tasks/' + encodeURIComponent(state.taskCenterSelectedID) : name;
        window.history.replaceState(null, '', '#' + hash);
      }
      renderTabSurface(name);
      if (name === 'tasks') window.setTimeout(() => {
        refreshTaskCenterQueues(Boolean(state.taskCenter));
        refreshTaskCenter(Boolean(state.taskCenter));
      }, 0);
      else window.setTimeout(() => ensureTabData(name, false).catch((error) => toast(error.message || i18n.t('ui.partial_data_load_failed'), 'error')), 0);
    }
