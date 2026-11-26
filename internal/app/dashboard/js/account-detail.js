    function renderAll() {
      renderOverview();
      renderAccounts();
      renderResin();
      renderProxySources();
      populatePolicyForm();
      populateProxyAutoBindForm();
      renderEvents();
      if (state.selectedAuthID) {
        const account = state.accounts.find((item) => item.auth_id === state.selectedAuthID) || state.leases.find((item) => item.auth_id === state.selectedAuthID) || state.accountDetails.get(state.selectedAuthID);
        if (account) renderDrawer(account);
        else closeDrawer();
      }
    }
    async function openAccountDrawer(authID) {
      const account = state.accounts.find((item) => item.auth_id === authID) || state.leases.find((item) => item.auth_id === authID) || state.accountDetails.get(authID);
      if (!account) return;
      state.accountDrawerFocus = document.activeElement;
      state.selectedAuthID = authID;
      renderDrawer(account);
      document.getElementById('accountDrawer').hidden = false;
      document.body.classList.add('drawer-open');
      window.setTimeout(() => document.getElementById('drawerClose').focus(), 20);
      try {
        const detail = await api('accounts.get', { auth_id: authID });
        if (state.selectedAuthID !== authID || !detail) return;
        state.accountDetails.set(authID, detail);
        trimAccountDetailsCache();
        const latest = state.accounts.find((item) => item.auth_id === authID) || state.leases.find((item) => item.auth_id === authID) || detail;
        if (latest) renderDrawer(latest);
      } catch (error) {
        toast(error.message || i18n.t("ui.account_details_load_failed"), 'error');
      }
    }
    function renderDrawer(accountSummary) {
      const summary = accountSummary || {};
      const detail = state.accountDetails.get(summary.auth_id) || {};
      const summaryVersion = Number(summary.binding_version || 0);
      const detailVersion = Number(detail.binding_version || 0);
      const merged = detailVersion > summaryVersion
        ? Object.assign({}, summary, detail)
        : Object.assign({}, detail, summary);
      const account = normalizeBindingView(merged);
      document.getElementById('drawerTitle').textContent = account.display_full || account.display || account.label || i18n.t("ui.account_details");
      document.getElementById('drawerSubtitle').textContent = account.auth_id_display || account.auth_id || '';
      const proxyStatus = account.proxy_status || 'unbound';
      const proxyStatusBadge = proxyStatus === 'bound' ? badge(i18n.t("ui.proxy_bound_short"), 'info') : (proxyStatus === 'drifted' ? badge(i18n.t('ui.drifted_from_record'), 'warn') : (proxyStatus === 'reconcile_required' ? badge(i18n.t('ui.needs_reconciliation'), 'bad') : (proxyStatus === 'external' ? badge(i18n.t('ui.external_configuration'), '') : (proxyStatus === 'checking' ? badge(i18n.t('proxy.binding.checking'), '') : (proxyStatus === 'read_failed' ? badge(i18n.t('proxy.binding.read_failed'), 'bad') : (proxyStatus === 'unknown' ? badge(i18n.t('proxy.binding.unknown'), 'bad') : badge(i18n.t("ui.proxy_unbound_short"), '')))))));
      const proxyIdentity = proxySourceIdentity(account);
      const proxySourceCell = (proxyIdentity.kindLabel || proxyIdentity.name) ? '<div class="detail-cell detail-cell-wide"><div class="detail-label">' + i18n.t('ui.proxy') + '</div><div class="detail-value proxy-source-value">' + (proxyIdentity.kindLabel ? '<span class="proxy-kind-chip">' + esc(proxyIdentity.kindLabel) + '</span>' : '') + (proxyIdentity.name ? '<span class="proxy-source-name" title="' + esc(proxyIdentity.name) + '">' + esc(proxyIdentity.name) + '</span>' : '') + '</div></div>' : '<div class="detail-cell detail-cell-wide"><div class="detail-label">' + i18n.t('ui.proxy') + '</div><div class="detail-value">-</div></div>';
      const actionButtons = [
        '<button class="btn" type="button" data-account-action="accounts.probe" data-auth="' + esc(account.auth_id) + '">' + icon('play') + i18n.t('ui.active_probe') + '</button>',
        '<button class="btn" type="button" data-account-action="accounts.cooldown" data-auth="' + esc(account.auth_id) + '">' + icon('clock') + i18n.t('ui.cooldown_account_hours', { hours: 12 }) + '</button>',
        '<button class="btn" type="button" data-account-action="accounts.restore" data-auth="' + esc(account.auth_id) + '">' + icon('check') + i18n.t('ui.restore') + '</button>',
        '<span class="action-tooltip-wrap"><button class="btn btn-danger" type="button" data-account-action="accounts.disable" data-auth="' + esc(account.auth_id) + '" aria-describedby="permanentDisableTooltip">' + icon('power') + i18n.t('ui.permanent_disable') + '</button><span class="action-tooltip" role="tooltip" id="permanentDisableTooltip">' + i18n.t('ui.permanent_disable_help') + '</span></span>'
      ];
      const evidence = account.evidence;
      const ignoredAttempt = account.last_attempt && account.last_attempt.class === 'ignored' ? badge(i18n.t("ui.no_attempts_counted"), 'warn') : '';
      const disableReason = account.disable_reason ? reasonLabel(account.disable_reason) : '';
      const disableReasonCell = disableReason ? '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.disable_reason_label') + '</div><div class="detail-value">' + esc(disableReason) + '</div></div>' : '';
      const canBind = proxyCanBind();
      if (proxyStatus === 'bound' || proxyStatus === 'drifted') {
        actionButtons.push('<button class="btn" type="button" data-proxy-action="rotate" data-auth="' + esc(account.auth_id) + '">' + icon('rotate') + i18n.t('action.proxy_rotate') + '</button>');
        actionButtons.push('<button class="btn btn-danger" type="button" data-proxy-action="clear" data-auth="' + esc(account.auth_id) + '">' + icon('x') + i18n.t('action.proxy_clear') + '</button>');
      } else if (proxyStatus === 'unbound' && canBind) {
        actionButtons.push('<button class="btn" type="button" data-proxy-action="bind" data-auth="' + esc(account.auth_id) + '">' + icon('link') + i18n.t('action.proxy_bind') + '</button>');
      } else if (proxyStatus === 'external') {
        actionButtons.push('<button class="btn btn-danger" type="button" data-proxy-action="clear-external" data-auth="' + esc(account.auth_id) + '">' + icon('x') + esc(i18n.t('ui.clear_external_proxy')) + '</button>');
      }
      actionButtons.push('<button class="btn btn-ghost" type="button" data-proxy-action="' + (account.auto_bind_excluded ? 'resume-auto' : 'pause-auto') + '" data-auth="' + esc(account.auth_id) + '">' + (account.auto_bind_excluded ? esc(i18n.t('ui.resume_auto_bind')) : esc(i18n.t('ui.pause_auto_bind'))) + '</button>');
      document.getElementById('drawerBody').innerHTML = [
        '<div class="drawer-badge-row">' + badge(stateLabel(account.state), stateTone(account.state)) + proxyStatusBadge + (account.strike_missing_thinking ? badge(i18n.t("ui.thinking_strike_marker"), 'warn') : '') + ignoredAttempt + '</div>',
        verdictCard(account.verdict),
        '<div class="drawer-section"><div class="detail-grid">',
        disableReasonCell,
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.consecutive_counts') + '</div><div class="detail-value">' + i18n.t('ui.soft_error_thinking_counts', { soft: num(account.soft_strikes || 0), errors: num(account.error_strikes || 0), thinking: num(account.thinking_strikes || 0) }) + '</div></div>',
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.healthy_streak') + '</div><div class="detail-value">' + esc(num(account.healthy_streak || 0)) + '</div></div>',
        evidence ? '' : '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.output_speed') + '</div><div class="detail-value">' + esc(num(account.tps, 1)) + ' TPS</div></div>',
        evidence ? '' : '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.output_reasoning') + '</div><div class="detail-value">' + esc(num(account.output_tokens)) + ' / ' + esc(num(account.reasoning_tokens)) + '</div></div>',
        '</div></div>',
        evidenceSection(evidence),
        '<div class="drawer-section"><h3>' + i18n.t('ui.account_information') + '</h3><div class="detail-grid">',
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.auth_id') + '</div><div class="detail-value mono">' + esc(account.auth_id_display || account.auth_id) + '</div></div>',
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.rotation_generation') + '</div><div class="detail-value">' + i18n.t('ui.generation', { generation: esc(account.proxy_generation || 0) }) + '</div></div>',
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.last_observed_label') + '</div><div class="detail-value">' + esc(formatFullTime(account.last_observed_at)) + '</div></div>',
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.last_probe') + '</div><div class="detail-value">' + esc(formatFullTime(account.last_probe_at)) + '</div></div>',
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.cooldown_deadline') + '</div><div class="detail-value">' + esc(formatFullTime(account.cooldown_until)) + '</div></div>',
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.probe_backoff') + '</div><div class="detail-value">' + esc(formatFullTime(account.probe_backoff_until)) + '</div></div>',
        '</div></div>',
        '<div class="drawer-section"><h3>' + i18n.t('ui.proxy') + '</h3><div class="detail-grid">',
        proxySourceCell,
        '<div class="detail-cell detail-cell-wide"><div class="detail-label">' + i18n.t('ui.proxy_address') + '</div><div class="detail-value mono detail-value-wrap" title="' + esc(account.proxy || '-') + '">' + esc(account.proxy || '-') + '</div></div>',
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.lease_account') + '</div><div class="detail-value mono detail-value-wrap">' + esc(account.proxy_lease_account || '-') + '</div></div>',
        '<div class="detail-cell"><div class="detail-label">' + i18n.t('ui.auto_bind_label') + '</div><div class="detail-value">' + badge(account.auto_bind_excluded ? i18n.t("task.status.paused") : i18n.t("ui.allow"), account.auto_bind_excluded ? 'warn' : 'good') + '</div></div>',
        '</div></div>',
        '<div class="drawer-section"><h3>' + i18n.t('ui.actions_section') + '</h3><div class="drawer-actions">' + actionButtons.join('') + '</div></div>'
      ].join('');
    }
    function closeDrawer() {
      if (state.selectedAuthID) state.accountDetails.delete(state.selectedAuthID);
      state.selectedAuthID = '';
      document.getElementById('accountDrawer').hidden = true;
      if (document.getElementById('proxySourceDrawer').hidden) document.body.classList.remove('drawer-open');
      if (state.accountDrawerFocus?.isConnected) state.accountDrawerFocus.focus();
    }
    async function runAccountAction(action, authID, button) {
      if (!authID) return;
      if (action === 'accounts.disable') {
        const confirmed = await confirmAction({
          title: i18n.t("ui.permanent_disable_account"),
          detail: i18n.t("ui.permanent_disable_detail"),
          confirmLabel: i18n.t('ui.permanent_disable'),
          icon: 'power',
          tone: 'danger'
        });
        if (!confirmed) return;
      }
      if (action === 'accounts.cooldown') {
        const confirmed = await confirmAction({
          title: i18n.t('ui.cooldown_account_hours', { hours: 12 }),
          detail: i18n.t("ui.cooldown_detail"),
          confirmLabel: i18n.t("ui.start_cooldown"),
          icon: 'clock'
        });
        if (!confirmed) return;
      }
      setBusy(button, true);
      try {
        const isProbe = action === 'accounts.probe';
        if (isProbe) toast(i18n.t("ui.active_probe_submitted"));
        const params = { auth_id: authID };
        if (isProbe) params.force = true;
        if (action === 'accounts.cooldown') params.duration = '12h';
        if (action === 'accounts.disable') params.reason = 'manual';
        const result = await api(action, params);
        if (isProbe) await waitForTask(result, i18n.t('ui.active_probe'));
        toast(isProbe ? i18n.t("ui.active_probe_complete") : i18n.t("ui.operation_complete"), 'success');
        closeDrawer();
        await refreshAll(true);
      } catch (error) { toast(error.message, 'error'); }
      finally { setBusy(button, false); }
    }

