    function openSettings(section) {
      setTab('settings');
      if (section !== 'tasks') return;
      window.setTimeout(() => {
        const group = document.querySelector('.policy-group-task-history');
        group?.scrollIntoView({ behavior: 'smooth', block: 'start' });
        document.getElementById('p_task_history_enabled')?.focus({ preventScroll: true });
      }, 0);
    }
    function listParams(kind, all) {
      if (kind === 'accounts') return { page: state.accountPage, page_size: state.accountPageSize, summary: true, state: !all && state.accountState !== 'all' ? state.accountState : '', query: all ? '' : state.accountQuery };
      if (kind === 'leases') return { page: state.leasePage, page_size: state.leasePageSize, status: !all && !['all', 'paused'].includes(state.leaseFilter) ? state.leaseFilter : '', excluded: !all && state.leaseFilter === 'paused' ? true : undefined, source_id: all ? '' : state.leaseSourceFilter, query: all ? '' : state.leaseQuery };
      return { page: state.proxySourcePage, page_size: state.proxySourcePageSize, kind: !all && state.proxySourceFilter !== 'all' ? state.proxySourceFilter : '', enabled: !all && state.proxySourceEnabledFilter !== 'all' ? state.proxySourceEnabledFilter : '', health: !all && state.proxySourceHealthFilter !== 'all' ? state.proxySourceHealthFilter : '', query: all ? '' : state.proxySourceQuery };
    }
    function listRequestSignature(kind) {
      return JSON.stringify(listParams(kind));
    }
    const BINDING_STATUSES = new Set(['bound', 'unbound', 'external', 'drifted', 'reconcile_required', 'checking', 'read_failed', 'unknown']);
    const PROXY_REVIEW_STATUSES = ['external', 'drifted', 'read_failed', 'unknown', 'reconcile_required'];
    function normalizeBindingStatus(value) {
      const status = String(value || '').trim();
      return BINDING_STATUSES.has(status) ? status : 'checking';
    }
    function proxySummaryCounts(summary) {
      const counts = (summary || {}).binding_counts || {};
      return Object.assign({ bound: 0, unbound: 0, external: 0, drifted: 0, unknown: 0, checking: 0, read_failed: 0, reconcile_required: 0 }, counts);
    }
    function proxyNeedsReviewCount(counts) {
      return PROXY_REVIEW_STATUSES.reduce((sum, key) => sum + Number(counts?.[key] || 0), 0);
    }
    function normalizeBindingView(value) {
      const item = value || {};
      return Object.assign({}, item, {
        proxy_status: normalizeBindingStatus(item.proxy_status),
        proxy_source_id: item.proxy_source_id || '',
        proxy_source_kind: item.proxy_source_kind || '',
        proxy_source_name: item.proxy_source_name || '',
        proxy_generation: Number(item.proxy_generation || 0),
        proxy_source_revision: Number(item.proxy_source_revision || 0),
        proxy_lease_account: item.proxy_lease_account || '',
        proxy_managed: Boolean(item.proxy_managed || item.proxy_source_id),
        proxy_pending: Boolean(item.proxy_pending),
        proxy_inspected: Boolean(item.proxy_inspected),
        binding_version: Number(item.binding_version || 0),
        display: item.display || item.label || item.auth_id,
        proxy: item.proxy || ''
      });
    }

    const BINDING_ROW_FIELDS = ['proxy_status', 'proxy', 'proxy_source_id', 'proxy_source_kind', 'proxy_managed', 'proxy_generation', 'proxy_source_revision', 'proxy_lease_account', 'proxy_pending', 'proxy_inspected'];
    const BINDING_ROW_DEFAULTS = Object.freeze({
      proxy_status: 'checking', proxy: '', proxy_source_id: '', proxy_source_kind: '', proxy_managed: false,
      proxy_generation: 0, proxy_source_revision: 0, proxy_lease_account: '', proxy_pending: false, proxy_inspected: false
    });

    function mergeBindingItems(items, fallbackVersion, options) {
      const rows = Array.isArray(items) ? items : [];
      const complete = !options || options.complete !== false;
      for (const item of rows) {
        if (!item || !item.auth_id) continue;
        const incomingVersion = Number(item.binding_version ?? fallbackVersion ?? 0);
        const existing = state.bindingsByAuthID.get(item.auth_id);
        if (existing && Number(existing.binding_version || 0) > incomingVersion) continue;
        const previousSourceID = existing ? String(existing.proxy_source_id || '') : '';
        const binding = Object.assign({}, existing || {}, { auth_id: item.auth_id, binding_version: incomingVersion });
        for (const field of BINDING_ROW_FIELDS) {
          if (Object.hasOwn(item, field)) binding[field] = item[field];
          else if (complete) binding[field] = BINDING_ROW_DEFAULTS[field];
        }
        const incomingSourceID = Object.hasOwn(item, 'proxy_source_id') ? String(item.proxy_source_id || '') : previousSourceID;
        if (Object.hasOwn(item, 'proxy_source_name')) binding.proxy_source_name = item.proxy_source_name || '';
        else if (existing && incomingSourceID === previousSourceID) binding.proxy_source_name = existing.proxy_source_name || '';
        else binding.proxy_source_name = '';
        binding.proxy_status = normalizeBindingStatus(binding.proxy_status);
        binding.proxy = binding.proxy || '';
        binding.proxy_source_id = binding.proxy_source_id || '';
        binding.proxy_source_kind = binding.proxy_source_kind || '';
        binding.proxy_generation = Number(binding.proxy_generation || 0);
        binding.proxy_source_revision = Number(binding.proxy_source_revision || 0);
        binding.proxy_lease_account = binding.proxy_lease_account || '';
        binding.proxy_managed = Boolean(binding.proxy_managed || binding.proxy_source_id);
        binding.proxy_pending = Boolean(binding.proxy_pending);
        binding.proxy_inspected = Boolean(binding.proxy_inspected);
        state.bindingsByAuthID.set(item.auth_id, binding);
        state.bindingsVersion = Math.max(state.bindingsVersion, incomingVersion);
      }
      while (state.bindingsByAuthID.size > 10000) state.bindingsByAuthID.delete(state.bindingsByAuthID.keys().next().value);
      if (state.proxy && Number(state.proxy.binding_version || 0) < state.bindingsVersion) {
        state.proxySummaryLoadedAt = 0;
        state.proxySummaryRetries = 0;
      }
      applyBindingCacheToLists();
    }

    function mergeBindingForAccount(account) {
      if (!account || !account.auth_id) return account;
      const binding = state.bindingsByAuthID.get(account.auth_id);
      if (!binding) return normalizeBindingView(account);
      if (Number(account.binding_version || 0) > Number(binding.binding_version || 0)) return normalizeBindingView(account);
      return normalizeBindingView(Object.assign({}, account, binding));
    }

    function applyBindingCacheToLists() {
      state.accounts = (state.accounts || []).map(mergeBindingForAccount);
      state.leases = (state.leases || []).map(mergeBindingForAccount);
      if (state.activeTab === 'accounts') renderAccounts();
      if (state.activeTab === 'resin') renderResin();
      if (state.selectedAuthID) {
        const selected = findCachedAccount(state.selectedAuthID);
        if (selected) renderDrawer(selected);
      }
    }

    function findCachedAccount(authID) {
      return state.accounts.find((item) => item.auth_id === authID) || state.leases.find((item) => item.auth_id === authID) || state.accountDetails.get(authID) || null;
    }

    function proxySourceIdentity(account) {
      const source = (state.proxySources || []).find((item) => item.id === account.proxy_source_id);
      const kind = source?.kind || account.proxy_source_kind || '';
      return {
        kind: kind,
        kindLabel: kind ? proxySourceLabel(kind) : '',
        name: source?.name || account.proxy_source_name || ''
      };
    }
    async function loadList(kind, signal) {
      let signature = listRequestSignature(kind);
      const request = ++state.listRequests[kind];
      const action = kind === 'accounts' ? 'accounts.list' : (kind === 'leases' ? 'proxy.bindings.list' : 'proxy.sources.list');
      for (;;) {
        const params = listParams(kind);
        const value = await apiWithRetry(action, params, { signal: signal }, 2);
        if (request !== state.listRequests[kind] || signature !== listRequestSignature(kind)) return null;
        const data = Object.assign({ items: [], total: 0, page: params.page, page_size: params.page_size }, value || {});
        const pages = Math.max(1, Math.ceil(Number(data.total || 0) / params.page_size));
        if (params.page > pages) {
          state[PAGE_CONFIG[kind].pageKey] = pages;
          signature = listRequestSignature(kind);
          continue;
        }
        if (kind === 'accounts') {
          state.accountList = data;
          mergeBindingItems(data.items, data.binding_version);
          applyAccountListData(data);
        } else if (kind === 'leases') {
          state.leaseList = data;
          mergeBindingItems(data.items, data.binding_version);
          state.leases = data.items.map(mergeBindingForAccount);
          state.leaseProxyDetails.clear();
          if (data.binding_counts) {
            const incomingVersion = Number(data.binding_version || 0);
            const currentVersion = Number(state.proxy?.binding_version || 0);
            if (!state.proxy || incomingVersion >= currentVersion) {
              state.proxy = Object.assign({}, state.proxy || {}, { binding_counts: data.binding_counts, binding_total: data.binding_total, binding_version: incomingVersion });
            }
          }
        } else {
          state.proxySourceList = data;
          state.proxySources = data.items;
          state.sourceProxyDetails.clear();
          renderProxySourceChoices();
        }
        state.listLoadedAt[kind] = Date.now();
        state.listSignatures[kind] = signature;
        if (kind !== 'sources') scheduleProxyMetadataEnrichment(kind);
        return data;
      }
    }
    const DATA_CACHE_TTL_MS = 30000;
    function cacheFresh(loadedAt) {
      const timestamp = Number(loadedAt || 0);
      return timestamp > 0 && Date.now() - timestamp < DATA_CACHE_TTL_MS;
    }
    function listCache(kind) {
      return kind === 'accounts' ? state.accountList : (kind === 'leases' ? state.leaseList : state.proxySourceList);
    }
    function renderListKind(kind) {
      if (kind === 'accounts') {
        renderAccounts();
        renderOverview();
      } else if (kind === 'leases') {
        renderResin();
        renderOverview();
      } else {
        renderProxySources();
      }
    }
    function renderTabSurface(tab) {
      if (tab === 'overview') renderOverview();
      else if (tab === 'accounts') {
        renderAccounts();
        scheduleProxyMetadataEnrichment('accounts');
      } else if (tab === 'resin') {
        renderResin();
        scheduleProxyMetadataEnrichment('leases');
      } else if (tab === 'events') renderEvents();
      else if (tab === 'settings') populatePolicyForm();
      if (state.selectedAuthID) {
        const selected = findCachedAccount(state.selectedAuthID);
        if (selected) renderDrawer(selected);
      }
    }
    function startListLoad(kind) {
      const signature = listRequestSignature(kind);
      if (state.listPromises[kind]) {
        if (state.listSignatures[kind] === signature) return state.listPromises[kind];
        state.listControllers[kind]?.abort();
      }
      if (state.listControllers[kind]) {
        state.listControllers[kind].abort();
      }
      const controller = new AbortController();
      state.listControllers[kind] = controller;
      state.listSignatures[kind] = signature;
      const promise = loadList(kind, controller.signal).then((data) => {
        if (data && controller.signal === state.listControllers[kind]?.signal) renderListKind(kind);
        return data;
      }).finally(() => {
        if (state.listPromises[kind] === promise) state.listPromises[kind] = null;
        if (state.listControllers[kind] === controller) state.listControllers[kind] = null;
      });
      state.listPromises[kind] = promise;
      return promise;
    }
    function ensureList(kind, force) {
      const cached = listCache(kind);
      if (!force && cached) {
        if (!cacheFresh(state.listLoadedAt[kind])) startListLoad(kind).catch(() => {});
        return Promise.resolve(cached);
      }
      return startListLoad(kind);
    }
    async function reloadList(kind) {
      try { await startListLoad(kind); }
      catch (error) {
        if (error?.code !== 'aborted') toast(error.message || i18n.t("ui.list_load_failed"), 'error');
      }
    }
    const listReloadTimers = new Map();
    function scheduleListReload(kind) {
      state.listRequests[kind] += 1;
      window.clearTimeout(listReloadTimers.get(kind));
      listReloadTimers.set(kind, window.setTimeout(() => reloadList(kind), 200));
    }
    async function loadAllAccounts(kind, all) {
      const ids = new Set();
      const params = listParams(kind, all);
      const action = kind === 'leases' ? 'proxy.bindings.list' : 'accounts.list';
      const value = await apiWithRetry(action, Object.assign({}, params, { page: 1, page_size: 10000, ids_only: true, all_ids: true }), null, 2);
      const items = (value && value.items) || [];
      items.forEach((item) => { const id = typeof item === 'string' ? item : item.auth_id; if (id) ids.add(id); });
      return ids;
    }
    async function selectListScope(kind, all, button) {
      const total = all ? Number((state.overview || {}).auth_count || state.accountsTotal) : pagedItems(kind).total;
      if (!await confirmAction({ title: all ? i18n.t("ui.select_all_accounts_confirm_title") : i18n.t("ui.select_filtered_confirm_title"), detail: i18n.t('ui.select_all_accounts_confirm_detail', { count: total }), confirmLabel: i18n.t("ui.load_and_select_all"), icon: 'users' })) return;
      setBusy(button, true);
      try {
        const ids = await loadAllAccounts(kind, all);
        const selection = kind === 'accounts' ? state.accountSelection : state.leaseSelection;
        ids.forEach((id) => selection.add(id));
        PAGE_CONFIG[kind].render();
        toast(i18n.t('ui.selected_accounts_toast', { count: selection.size }), 'success');
      } catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    }
    async function ensureAccountData() {
      const tab = state.activeTab === 'resin' ? 'resin' : 'accounts';
      await ensureTabData(tab, false);
    }
    function applyAccountListData(value) {
      const data = value || {};
      state.accounts = (data.items || []).map(mergeBindingForAccount);
      state.accountsTotal = Number((state.overview || {}).auth_count || Math.max(state.accountsTotal, Number(data.total || state.accounts.length)));
      state.stateCounts = data.state_counts || {};
      state.accountsLoaded = true;
      trimAccountDetailsCache();
    }
    function trimAccountDetailsCache() {
      while (state.accountDetails.size > 100) {
        let removed = false;
        for (const key of state.accountDetails.keys()) {
          if (key === state.selectedAuthID) continue;
          state.accountDetails.delete(key);
          removed = true;
          break;
        }
        if (!removed) break;
      }
    }
    function scheduleAccountMetadataRefresh() {
      scheduleProxyMetadataEnrichment('accounts');
    }
    function scheduleProxyMetadataEnrichment(kind, delay) {
      if (kind !== 'accounts' && kind !== 'leases') return;
      if ((kind === 'accounts' && state.activeTab !== 'accounts') || (kind === 'leases' && state.activeTab !== 'resin')) return;
      if (state.metadataTimers[kind]) window.clearTimeout(state.metadataTimers[kind]);
      state.metadataTimers[kind] = window.setTimeout(() => {
        state.metadataTimers[kind] = 0;
        enrichVisibleProxyMetadata(kind).catch(() => {});
      }, Number(delay || 0));
    }
    function visibleCheckingAuthIDs(kind) {
      const rows = kind === 'leases' ? state.leases : state.accounts;
      return (rows || []).filter((row) => row && (row.proxy_status === 'checking' || (row.proxy_status === 'unknown' && !row.proxy_inspected))).map((row) => row.auth_id).filter(Boolean);
    }
    function pruneMetadataAttempts() {
      for (const authID of state.metadataAttempts.keys()) {
        const binding = state.bindingsByAuthID.get(authID);
        if (!binding || (binding.proxy_status !== 'checking' && binding.proxy_status !== 'unknown')) state.metadataAttempts.delete(authID);
      }
      while (state.metadataAttempts.size > 10000) state.metadataAttempts.delete(state.metadataAttempts.keys().next().value);
    }
    async function enrichVisibleProxyMetadata(kind) {
      if ((kind === 'accounts' && state.activeTab !== 'accounts') || (kind === 'leases' && state.activeTab !== 'resin')) return;
      if (state.metadataPromises[kind]) return state.metadataPromises[kind];
      const ids = visibleCheckingAuthIDs(kind).filter((authID) => Number(state.metadataAttempts.get(authID) || 0) < 3).slice(0, 100);
      if (!ids.length) return;
      const promise = (async () => {
        try {
          const data = await api('accounts.proxy_metadata', { auth_ids: ids });
          mergeBindingItems(data.items, data.binding_version);
          for (const authID of ids) state.metadataAttempts.set(authID, Number(state.metadataAttempts.get(authID) || 0) + 1);
          pruneMetadataAttempts();
          const remaining = visibleCheckingAuthIDs(kind).filter((authID) => ids.includes(authID));
          if (remaining.length) {
            const attempts = Math.max(...remaining.map((authID) => Number(state.metadataAttempts.get(authID) || 0)));
            scheduleProxyMetadataEnrichment(kind, Math.min(3000, 500 * Math.pow(2, Math.max(0, attempts - 1))));
          }
        } catch (_) {
          for (const authID of ids) state.metadataAttempts.set(authID, Number(state.metadataAttempts.get(authID) || 0) + 1);
          if (visibleCheckingAuthIDs(kind).length) scheduleProxyMetadataEnrichment(kind, 1000);
        }
      })().finally(() => {
        if (state.metadataPromises[kind] === promise) state.metadataPromises[kind] = null;
      });
      state.metadataPromises[kind] = promise;
      return promise;
    }

    async function loadOverviewData() {
      const value = await api('overview');
      state.overview = value || {};
      if (state.overview.policy) state.policy = state.overview.policy;
      state.overviewLoadedAt = Date.now();
      renderOverview();
      return state.overview;
    }
    function ensureOverview(force) {
      const hasCache = Boolean(state.overview);
      if (!force && hasCache && cacheFresh(state.overviewLoadedAt)) return Promise.resolve(state.overview);
      if (state.overviewPromise) return state.overviewPromise;
      const promise = loadOverviewData().finally(() => {
        if (state.overviewPromise === promise) state.overviewPromise = null;
      });
      state.overviewPromise = promise;
      if (!force && hasCache) {
        promise.catch(() => {});
        return Promise.resolve(state.overview);
      }
      return promise;
    }

    async function loadEventsData() {
      const value = await api('events.list', { limit: 1000 });
      state.events = (value && value.items) || [];
      state.eventsLoadedAt = Date.now();
      renderEvents();
      renderOverview();
      return state.events;
    }
    function ensureEvents(force) {
      const hasCache = state.eventsLoadedAt > 0;
      if (!force && hasCache && cacheFresh(state.eventsLoadedAt)) return Promise.resolve(state.events);
      if (state.eventsPromise) return state.eventsPromise;
      const promise = loadEventsData().finally(() => {
        if (state.eventsPromise === promise) state.eventsPromise = null;
      });
      state.eventsPromise = promise;
      if (!force && hasCache) {
        promise.catch(() => {});
        return Promise.resolve(state.events);
      }
      return promise;
    }

    async function loadPolicyData() {
      await loadPolicySchema();
      const value = await api('policy.get');
      state.policy = value || {};
      state.policyLoadedAt = Date.now();
      populatePolicyForm();
      renderOverview();
      return state.policy;
    }
    function ensurePolicy(force) {
      const hasCache = Boolean(state.policy && Object.keys(state.policy).length);
      if (!force && hasCache && cacheFresh(state.policyLoadedAt)) return Promise.resolve(state.policy);
      if (state.policyPromise) return state.policyPromise;
      const promise = loadPolicyData().finally(() => {
        if (state.policyPromise === promise) state.policyPromise = null;
      });
      state.policyPromise = promise;
      if (!force && hasCache) {
        promise.catch(() => {});
        return Promise.resolve(state.policy);
      }
      return promise;
    }

    async function loadProxySummaryData() {
      const incoming = (await api('proxy.summary')) || {};
      const previous = state.proxy || {};
      const incomingVersion = Number(incoming.binding_version || 0);
      const previousVersion = Number(previous.binding_version || 0);
      const summary = Object.assign({}, incoming);
      if (!summary.binding_counts && previous.binding_counts) {
        summary.binding_counts = previous.binding_counts;
        summary.binding_total = previous.binding_total;
        summary.binding_version = previousVersion;
      } else if (previous.binding_counts && previousVersion > incomingVersion) {
        summary.binding_counts = previous.binding_counts;
        summary.binding_total = previous.binding_total;
        summary.binding_version = previousVersion;
      }
      state.proxy = summary;
      const countsVersion = Number(summary.binding_version || incomingVersion || 0);
      const latestBindingVersion = Number(state.bindingsVersion || 0);
      const fresh = !incomingVersion || !latestBindingVersion || countsVersion >= latestBindingVersion;
      state.proxySummaryLoadedAt = fresh ? Date.now() : 0;
      if (fresh) state.proxySummaryRetries = 0;
      renderOverview();
      renderAccounts();
      renderResin();
      populateProxyAutoBindForm();
      if (!fresh && state.proxySummaryRetries < 2) {
        state.proxySummaryRetries += 1;
        window.setTimeout(() => ensureProxySummary(false).catch(() => {}), 0);
      }
      return state.proxy;
    }
    function ensureProxySummary(force) {
      const hasCache = Boolean(state.proxy);
      if (!force && hasCache && cacheFresh(state.proxySummaryLoadedAt)) return Promise.resolve(state.proxy);
      if (state.proxySummaryPromise) return state.proxySummaryPromise;
      const promise = loadProxySummaryData().finally(() => {
        if (state.proxySummaryPromise === promise) state.proxySummaryPromise = null;
      });
      state.proxySummaryPromise = promise;
      if (!force && hasCache) {
        promise.catch(() => {});
        return Promise.resolve(state.proxy);
      }
      return promise;
    }
    async function loadProxyData() {
      const results = await Promise.allSettled([ensureProxySummary(true), ensureList('sources', true)]);
      const errors = results.filter((result) => result.status === 'rejected').map((result) => result.reason);
      if (errors.length) throw errors[0];
      renderAll();
    }
    function ensureProxyData(force) {
      return Promise.all([ensureProxySummary(force), ensureList('sources', force)]).then((values) => {
        renderOverview();
        renderAccounts();
        renderResin();
        renderProxySources();
        populateProxyAutoBindForm();
        return values;
      });
    }

    async function loadTabData(tab, force) {
      const errors = [];
      if (tab === 'overview') {
        ensureProxySummary(force).catch(() => {});
        const results = await Promise.allSettled([ensureOverview(force), ensureEvents(force)]);
        errors.push(...results.filter((result) => result.status === 'rejected').map((result) => result.reason));
      } else if (tab === 'accounts') {
        try {
          await ensureList('accounts', force);
        } catch (error) {
          errors.push(error);
        }
        ensureOverview(false).catch(() => {});
        ensureProxySummary(false).catch(() => {});
      } else if (tab === 'resin') {
        try {
          await ensureList('leases', force);
        } catch (error) {
          errors.push(error);
        }
        // Load the runtime summary only after the bindings list has populated
        // binding_counts. The backend can then reuse that aggregation instead
        // of walking the entire account catalog a second time.
        ensureProxySummary(force).catch(() => {});
        ensureList('sources', force).catch(() => {});
      } else if (tab === 'events') {
        const results = await Promise.allSettled([ensureEvents(force), ensureOverview(false)]);
        errors.push(...results.filter((result) => result.status === 'rejected').map((result) => result.reason));
      } else if (tab === 'settings') {
        const results = await Promise.allSettled([ensurePolicy(force), ensureOverview(false)]);
        errors.push(...results.filter((result) => result.status === 'rejected').map((result) => result.reason));
      }
      renderTabSurface(tab);
      if (errors.length) throw errors[0];
    }
    function ensureTabData(tab, force) {
      if (!['overview', 'accounts', 'resin', 'events', 'settings'].includes(tab)) return Promise.resolve();
      if (state.tabPromises[tab]) return state.tabPromises[tab];
      const promise = loadTabData(tab, force).finally(() => {
        if (state.tabPromises[tab] === promise) state.tabPromises[tab] = null;
      });
      state.tabPromises[tab] = promise;
      return promise;
    }
    async function refreshSection(button, label, refresh) {
      state.activeRefreshes += 1;
      state.refreshing = true;
      setBusy(button, true);
      document.getElementById('syncLabel').textContent = i18n.t("ui.refreshing") + label;
      try {
        await refresh();
        document.getElementById('syncLabel').textContent = i18n.t('ui.updated_at_value', { time: formatTime(new Date().toISOString()) });
        toast(i18n.t('ui.refreshed', { label: label }), 'success');
      } catch (error) {
        toast(error.message || i18n.t('ui.refresh_failed', { label: label }), 'error');
      } finally {
        state.activeRefreshes = Math.max(0, state.activeRefreshes - 1);
        state.refreshing = state.activeRefreshes > 0;
        setBusy(button, false);
      }
    }
    function refreshAccounts(button) {
      return refreshSection(button, i18n.t("ui.account_quality"), async () => {
        const tasks = await Promise.allSettled([
          ensureOverview(true),
          ensureList('accounts', true)
        ]);
        const errors = tasks.filter((task) => task.status === 'rejected').map((task) => task.reason);
        renderAccounts();
        renderOverview();
        ensureProxySummary(false).catch(() => {});
        if (errors.length) throw errors[0];
      });
    }
    function refreshResin(button) {
      return refreshSection(button, i18n.t("ui.proxy_management"), async () => {
        const errors = [];
        try {
          await ensureList('leases', true);
        } catch (error) {
          errors.push(error);
        }
        const tasks = await Promise.allSettled([ensureProxySummary(true), ensureList('sources', true)]);
        errors.push(...tasks.filter((task) => task.status === 'rejected').map((task) => task.reason));
        renderResin();
        renderOverview();
        if (errors.length) throw errors[0];
      });
    }
    function refreshEvents(button) {
      return refreshSection(button, i18n.t("ui.event_log"), async () => {
        const tasks = await Promise.allSettled([ensureEvents(true), ensureOverview(false)]);
        const errors = tasks.filter((task) => task.status === 'rejected').map((task) => task.reason);
        renderEvents();
        renderOverview();
        if (errors.length) throw errors[0];
      });
    }
    async function refreshAll(silent) {
      const tab = state.activeTab;
      if (tab === 'tasks') {
        await Promise.all([refreshTaskCenterQueues(Boolean(state.taskCenter)), refreshTaskCenter(Boolean(state.taskCenter))]);
        return;
      }
      document.getElementById('syncLabel').textContent = i18n.t("ui.syncing");
      await ensureTabData(tab, true);
      document.getElementById('syncLabel').textContent = i18n.t('ui.updated_at_value', { time: formatTime(new Date().toISOString()) });
      if (!silent) toast(i18n.t("ui.data_refreshed"), 'success');
    }
    function metricCard(iconName, value, label, detail, tone) {
      return '<div class="metric"><span class="metric-icon ' + esc(tone || '') + '">' + icon(iconName) + '</span><div style="min-width:0"><p class="metric-label">' + esc(label) + '</p><p class="metric-value">' + esc(value) + '</p><p class="metric-detail">' + esc(detail || '') + '</p></div></div>';
    }
    function statCard(iconName, value, label, detail) {
      return '<div class="stat-cell"><span class="stat-icon">' + icon(iconName) + '</span><div><p class="stat-label">' + esc(label) + '</p><p class="stat-value">' + esc(value) + '</p><p class="stat-detail">' + esc(detail || '') + '</p></div></div>';
    }
    function policyChip(label, value) {
      return '<span class="policy-chip"><span class="policy-chip-label">' + esc(label) + '</span><span class="policy-chip-value">' + esc(value) + '</span></span>';
    }
    function summaryItem(label, value, options) {
      const settings = options || {};
      const valueClass = settings.truncate ? 'summary-value truncate' : 'summary-value';
      const title = settings.title ? ' title="' + esc(settings.title) + '"' : '';
      return '<div class="summary-item"><div class="summary-label">' + esc(label) + '</div><div class="' + valueClass + '"' + title + '>' + value + '</div></div>';
    }
    const PAGE_CONFIG = {
      accounts: { pageKey: 'accountPage', sizeKey: 'accountPageSize', pagerId: 'accountsPager', itemUnit: i18n.t('ui.unit_items'), unit: i18n.t("ui.unit_accounts"), items: () => state.accounts, metadata: () => state.accountList, render: () => renderAccounts() },
      leases: { pageKey: 'leasePage', sizeKey: 'leasePageSize', pagerId: 'leasesPager', itemUnit: i18n.t('ui.unit_items'), unit: i18n.t("ui.unit_accounts"), items: () => state.leases, metadata: () => state.leaseList, render: () => renderResin() },
      sources: { pageKey: 'proxySourcePage', sizeKey: 'proxySourcePageSize', pagerId: 'proxySourcesPager', itemUnit: i18n.t('ui.unit_items'), unit: i18n.t("ui.unit_proxies"), items: () => state.proxySources, metadata: () => state.proxySourceList, render: () => renderProxySources() },
      events: { pageKey: 'eventPage', sizeKey: 'eventPageSize', pagerId: 'eventsPager', itemUnit: i18n.t('ui.unit_items'), unit: i18n.t("ui.unit_events"), items: () => state.events.filter(eventMatches), render: () => renderEvents() }
    };
    function pagedItems(kind) {
      const config = PAGE_CONFIG[kind];
      const all = config.items();
      const size = [20, 50, 100].includes(Number(state[config.sizeKey])) ? Number(state[config.sizeKey]) : 50;
      if (config.metadata) {
        const meta = config.metadata() || {};
        const total = Number(meta.total || 0);
        const pages = Math.max(1, Math.ceil(total / size));
        const page = Math.min(Math.max(1, Number(state[config.pageKey]) || 1), pages);
        const start = (page - 1) * size;
        return { all: all, items: all, page: page, size: size, pages: pages, total: total, start: start, end: Math.min(start + all.length, total) };
      }
      const pages = Math.max(1, Math.ceil(all.length / size));
      const page = Math.min(Math.max(1, Number(state[config.pageKey]) || 1), pages);
      state[config.pageKey] = page;
      const start = (page - 1) * size;
      const end = Math.min(start + size, all.length);
      return { all: all, items: all.slice(start, end), page: page, size: size, pages: pages, total: all.length, start: start, end: end };
    }
