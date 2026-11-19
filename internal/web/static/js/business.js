(() => {
  const shell = document.querySelector(".app-shell");
  if (!shell) {
    return;
  }

  const statusLabels = {
    online: "在线",
    pending: "待验证",
    paused: "已暂停",
    error: "已掉线",
  };
  const verificationTTL = 10 * 60 * 1000;
  const peerLabels = {
    user: "私聊",
    chat: "群组",
    channel: "频道",
  };
  const correlationTypeDetails = {
    crypto_address: {label: "链上地址", icon: "activity"},
    link: {label: "相同链接", icon: "paperclip"},
    content: {label: "相同文本", icon: "message-square-text"},
    domain: {label: "相同域名", icon: "activity"},
    sender: {label: "相同发送者", icon: "user-round"},
  };
  const countryCodes = [
    "+886", "+852", "+853", "+971", "+966", "+972", "+351",
    "+358", "+353", "+420", "+421", "+385", "+386", "+381", "+380",
    "+61", "+62", "+63", "+64", "+65", "+66", "+60", "+49", "+44",
    "+43", "+41", "+39", "+34", "+33", "+32", "+31", "+30", "+27",
    "+20", "+1", "+7", "+86", "+81", "+82",
  ];

  const csrfToken = () => {
    const prefix = "telenexus_csrf=";
    return document.cookie
      .split(";")
      .map((part) => part.trim())
      .find((part) => part.startsWith(prefix))
      ?.slice(prefix.length);
  };

  const api = async (path, options = {}) => {
    const headers = new Headers(options.headers || {});
    headers.set("Accept", "application/json");
    if (options.body !== undefined) {
      headers.set("Content-Type", "application/json");
    }
    if (options.method && !["GET", "HEAD", "OPTIONS"].includes(options.method.toUpperCase())) {
      const token = csrfToken();
      if (token) {
        headers.set("X-CSRF-Token", decodeURIComponent(token));
      }
    }
    const response = await fetch(path, {...options, headers});
    const text = response.status === 204 ? "" : await response.text();
    let payload = null;
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch {
        payload = null;
      }
    }
    if (!response.ok) {
      if (response.status === 401) {
        window.location.replace("/login");
      }
      const error = new Error(payload?.error?.message || `请求失败（${response.status}）`);
      error.code = payload?.error?.code || "request_failed";
      error.status = response.status;
      throw error;
    }
    return payload;
  };

  const showNotice = (element, message = "", isError = false) => {
    if (!element) {
      return;
    }
    element.textContent = message;
    element.hidden = !message;
    element.classList.toggle("form-notice-error", isError);
  };

  const showToast = (message) => {
    const toast = document.querySelector("[data-business-toast]");
    if (!toast) {
      return;
    }
    toast.textContent = message;
    toast.hidden = false;
    window.clearTimeout(Number(toast.dataset.timer) || 0);
    toast.dataset.timer = String(window.setTimeout(() => {
      toast.hidden = true;
    }, 2600));
  };

  const formatDate = (value) => {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
      return "—";
    }
    return new Intl.DateTimeFormat("zh-CN", {
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false,
    }).format(date);
  };

  const formatDay = (value) => {
    const date = new Date(`${value}T00:00:00`);
    if (Number.isNaN(date.getTime())) {
      return value || "—";
    }
    return new Intl.DateTimeFormat("zh-CN", {
      month: "2-digit",
      day: "2-digit",
    }).format(date);
  };

  const formatPhone = (phone) => {
    const digits = String(phone || "").replace(/\D/g, "");
    if (!digits) {
      return "未设置";
    }
    const normalized = `+${digits}`;
    const countryCode = countryCodes.find((code) => normalized.startsWith(code)) || "";
    return countryCode && normalized.length > countryCode.length
      ? `${countryCode} ${normalized.slice(countryCode.length)}`
      : normalized;
  };

  const element = (tag, className = "", text = "") => {
    const node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text) {
      node.textContent = text;
    }
    return node;
  };

  const appendCell = (row, content) => {
    const cell = document.createElement("td");
    if (content instanceof Node) {
      cell.append(content);
    } else {
      cell.textContent = String(content ?? "");
    }
    row.append(cell);
  };

  const setText = (selector, value, root = document) => {
    const target = root.querySelector(selector);
    if (target) {
      target.textContent = String(value);
    }
  };

  const accountLabel = (account) => (
    account?.display_name || account?.username || formatPhone(account?.phone_number)
  );

  const accountMapFrom = (items) => new Map(
    (Array.isArray(items) ? items : []).map((account) => [Number(account.id), account]),
  );

  const accountDisplayStatus = (account) => {
    const status = String(account?.status || "pending");
    const verifiedAt = account?.last_verified_at
      ? new Date(account.last_verified_at).getTime()
      : Number.NaN;
    if (
      status === "online" &&
      (!Number.isFinite(verifiedAt) || Date.now() - verifiedAt > verificationTTL)
    ) {
      return {key: "stale", label: "状态待确认"};
    }
    return {key: status, label: statusLabels[status] || status || "未知"};
  };

  const loadAccounts = async () => {
    const payload = await api("/api/v1/accounts?limit=100");
    return Array.isArray(payload?.items) ? payload.items : [];
  };

  const fillSelect = (select, items, placeholder) => {
    if (!select) {
      return;
    }
    const options = [];
    if (placeholder) {
      options.push(new Option(placeholder, ""));
    }
    items.forEach((item) => options.push(new Option(item.label, item.value)));
    select.replaceChildren(...options);
    select.disabled = items.length === 0;
  };

  const createEmptyCopy = (message) => element("p", "business-empty-copy", message);

  const renderChart = (container, items, emptyMessage) => {
    if (!container) {
      return;
    }
    const values = Array.isArray(items) ? items : [];
    container.replaceChildren();
    if (values.length === 0) {
      container.append(createEmptyCopy(emptyMessage));
      return;
    }
    const maxCount = Math.max(1, ...values.map((item) => Number(item.count) || 0));
    const chart = element("div", "business-chart-bars");
    values.forEach((item) => {
      const column = element("div", "business-chart-column");
      column.title = `${formatDay(item.date)}：${Number(item.count) || 0} 条`;
      const track = element("div", "business-chart-track");
      const bar = element("span", "business-chart-bar");
      const count = Number(item.count) || 0;
      bar.style.height = `${Math.max(count > 0 ? 8 : 2, Math.round(count / maxCount * 100))}%`;
      track.append(bar);
      column.append(element("strong", "business-chart-value", String(count)), track);
      column.append(element("small", "", formatDay(item.date)));
      chart.append(column);
    });
    container.append(chart);
  };

  const renderCompactAccount = (account) => {
    const row = document.createElement("a");
    row.className = "business-compact-row";
    const status = accountDisplayStatus(account);
    row.href = status.key === "online"
      ? `/accounts/${encodeURIComponent(account.id)}/web`
      : "/accounts";
    const icon = element("span", "business-row-icon");
    icon.append(element("span", "icon icon-user-round"));
    const copy = element("span", "business-row-copy");
    copy.append(
      element("strong", "", accountLabel(account)),
      element("small", "", `${formatPhone(account.phone_number)} · ${account.username ? `@${account.username}` : "未设置用户名"}`),
    );
    row.append(
      icon,
      copy,
      element("span", `badge ${status.key === "online" ? "badge-blue" : "badge-neutral"}`,
        status.label),
    );
    return row;
  };

  const renderCompactAlert = (alert) => {
    const row = document.createElement("a");
    row.className = "business-compact-row";
    row.href = "/monitor/alerts";
    const icon = element("span", "business-row-icon business-row-icon-alert");
    icon.append(element("span", "icon icon-bell-ring"));
    const copy = element("span", "business-row-copy");
    copy.append(
      element("strong", "", alert.rule_name || "监控规则"),
      element("small", "", alert.rule_version ? `规则 v${alert.rule_version}` : "规则版本未知"),
      element("small", "", alert.message_preview || alert.peer_title || "监控命中"),
    );
    row.append(icon, copy, element("time", "business-row-time", formatDate(alert.created_at)));
    return row;
  };

  const renderNotificationPanel = (alerts) => {
    const panel = document.querySelector("#notification-panel");
    const body = panel?.querySelector(".empty-state");
    if (!body || !Array.isArray(alerts) || alerts.length === 0) {
      return;
    }
    body.className = "business-notification-list";
    body.replaceChildren();
    alerts.slice(0, 5).forEach((alert) => body.append(renderCompactAlert(alert)));
    const dot = document.querySelector('[data-open-notifications] .notification-dot');
    dot?.removeAttribute("hidden");
  };

  const initDashboard = () => {
    const root = document.querySelector("[data-dashboard]");
    if (!root) {
      return;
    }
    const updateDashboard = async () => {
      try {
        const [dashboard, accounts, report, alerts, health] = await Promise.all([
          api("/api/v1/dashboard"),
          loadAccounts(),
          api("/api/v1/reports?days=7"),
          api("/api/v1/monitor/alerts?state=pending&limit=5"),
          api("/api/v1/health"),
        ]);
        const accountCount = Number(dashboard.account_count) || 0;
        const onlineCount = Number(dashboard.online_count) || 0;
        const monitorCount = Number(dashboard.monitor_count) || 0;
        const todayCount = Number(dashboard.today_messages) || 0;
        const alertCount = Number(dashboard.pending_alerts) || 0;

        setText("[data-dashboard-account-count]", accountCount, root);
        setText("[data-dashboard-online-count]", onlineCount, root);
        setText("[data-dashboard-monitor-count]", monitorCount, root);
        setText("[data-dashboard-rule-count]", monitorCount, root);
        setText("[data-dashboard-message-count]", todayCount, root);
        setText("[data-dashboard-today-count]", todayCount, root);
        setText("[data-dashboard-alert-count]", alertCount, root);
        setText("[data-dashboard-summary]", `${accountCount} 个账号 · ${onlineCount} 个在线 · ${monitorCount} 条监控规则`, root);
        setText("[data-dashboard-address]", window.location.host, root);
        setText("[data-dashboard-updated]", `更新于 ${new Intl.DateTimeFormat("zh-CN", {
          hour: "2-digit",
          minute: "2-digit",
          hour12: false,
        }).format(new Date())}`, root);

        const serviceStatus = String(health?.status || "error");
        const healthLabel = serviceStatus === "ok" ? "正常" : serviceStatus === "degraded" ? "部分可用" : "异常";
        setText("[data-dashboard-service]", serviceStatus === "ok" ? "服务运行正常" : `服务${healthLabel}`, root);
        setText("[data-dashboard-health]", healthLabel, root);
        setText("[data-dashboard-telegram]", dashboard.telegram_ready ? "已配置" : "未配置", root);

        const accountTask = root.querySelector("[data-dashboard-account-task]");
        if (accountTask) {
          accountTask.textContent = accountCount > 0 ? `${onlineCount} 个在线` : "待接入";
          accountTask.className = `badge ${onlineCount > 0 ? "badge-blue" : "badge-neutral"}`;
        }
        const monitorTask = root.querySelector("[data-dashboard-monitor-task]");
        if (monitorTask) {
          monitorTask.textContent = monitorCount > 0 ? `${monitorCount} 条启用` : "待创建";
          monitorTask.className = `badge ${monitorCount > 0 ? "badge-blue" : "badge-neutral"}`;
        }
        const messageTask = root.querySelector("[data-dashboard-message-task]");
        if (messageTask) {
          messageTask.textContent = todayCount > 0 ? `${todayCount} 条今日消息` : "暂无数据";
          messageTask.className = `badge ${todayCount > 0 ? "badge-blue" : "badge-neutral"}`;
        }

        renderChart(
          root.querySelector("[data-dashboard-chart]"),
          report?.by_day,
          "最近 7 天还没有索引消息。",
        );
        const accountList = root.querySelector("[data-dashboard-accounts]");
        if (accountList) {
          const recent = accounts
            .slice()
            .sort((left, right) => new Date(right.updated_at) - new Date(left.updated_at))
            .slice(0, 4);
          accountList.replaceChildren(
            ...(recent.length
              ? recent.map(renderCompactAccount)
              : [createEmptyCopy("还没有 Telegram 账号。")]),
          );
        }
        const alertList = root.querySelector("[data-dashboard-alerts]");
        const alertItems = Array.isArray(alerts?.items) ? alerts.items : [];
        if (alertList) {
          alertList.replaceChildren(
            ...(alertItems.length
              ? alertItems.slice(0, 4).map(renderCompactAlert)
              : [createEmptyCopy("当前没有待处理告警。")]),
          );
        }
        renderNotificationPanel(alertItems);
      } catch (error) {
        setText("[data-dashboard-summary]", error.message, root);
        setText("[data-dashboard-service]", "数据读取失败", root);
      }
    };
    updateDashboard();
  };

  const initGroups = () => {
    const grid = document.querySelector("[data-groups-grid]");
    if (!grid) {
      return;
    }
    const empty = document.querySelector("[data-groups-empty]");
    const error = document.querySelector("[data-groups-error]");
    const createDialog = document.getElementById("group-create-dialog");
    const createForm = document.querySelector("[data-group-form]");
    const createNotice = document.querySelector("[data-group-form-notice]");
    const membersDialog = document.getElementById("group-members-dialog");
    const membersForm = document.querySelector("[data-group-members-form]");
    const membersTitle = document.querySelector("[data-group-members-title]");
    const membersNotice = document.querySelector("[data-group-members-notice]");
    let contactGroup = null;
    let groupAccounts = [];

    const closeDialog = (dialog) => {
      if (dialog?.open) {
        dialog.close();
      }
    };
    const load = async () => {
      showNotice(error);
      try {
        const [groupsPayload, accounts] = await Promise.all([
          api("/api/v1/groups"),
          loadAccounts(),
        ]);
        const groups = Array.isArray(groupsPayload?.items) ? groupsPayload.items : [];
        const accountMap = accountMapFrom(accounts);
        groupAccounts = accounts;
        grid.replaceChildren();
        const assigned = new Set();
        groups.forEach((group) => {
          (group.account_ids || []).forEach((id) => assigned.add(Number(id)));
          const card = element("article", "business-entity-card");
          card.dataset.color = String(group.color || "blue");
          const heading = element("div", "business-entity-heading");
          const icon = element("span", "business-entity-icon");
          icon.append(element("span", "icon icon-folder-users"));
          const copy = element("span", "business-row-copy");
          copy.append(
            element("strong", "", group.name),
            element("small", "", `${group.account_count || 0} 个账号`),
          );
          heading.append(icon, copy);
          const names = (group.account_ids || [])
            .map((id) => accountMap.get(Number(id)))
            .filter(Boolean)
            .slice(0, 4)
            .map(accountLabel);
          const chips = element("div", "business-chip-list");
          if (names.length === 0) {
            chips.append(element("span", "business-chip is-muted", "尚未分配账号"));
          } else {
            names.forEach((name) => chips.append(element("span", "business-chip", name)));
            if ((group.account_ids || []).length > names.length) {
              chips.append(element("span", "business-chip", `+${group.account_ids.length - names.length}`));
            }
          }
          const actions = element("div", "business-entity-actions");
          const assign = element("button", "button button-secondary button-compact", "分配账号");
          assign.type = "button";
          assign.addEventListener("click", () => {
            contactGroup = group;
            if (membersTitle) {
              membersTitle.textContent = `分配账号到 ${group.name}`;
            }
            const selected = new Set((group.account_ids || []).map(Number));
            const list = document.querySelector("[data-group-account-options]");
            list?.replaceChildren(...groupAccounts.map((account) => {
              const label = document.createElement("label");
              label.className = "business-check-row";
              const input = document.createElement("input");
              input.type = "checkbox";
              input.value = String(account.id);
              input.checked = selected.has(Number(account.id));
              const text = element("span", "business-check-copy");
              text.append(
                element("strong", "", accountLabel(account)),
                element("small", "", `${formatPhone(account.phone_number)} · ${accountDisplayStatus(account).label}`),
              );
              label.append(input, text);
              return label;
            }));
            showNotice(membersNotice);
            membersDialog?.showModal();
          });
          const remove = element("button", "button button-danger-ghost button-compact", "删除");
          remove.type = "button";
          remove.addEventListener("click", async () => {
            if (!window.confirm(`删除分组“${group.name}”？账号本身不会被删除。`)) {
              return;
            }
            remove.disabled = true;
            try {
              await api(`/api/v1/groups/${encodeURIComponent(group.id)}`, {method: "DELETE"});
              showToast("分组已删除");
              await load();
            } catch (requestError) {
              showNotice(error, requestError.message, true);
            } finally {
              remove.disabled = false;
            }
          });
          actions.append(assign, remove);
          card.append(heading, chips, actions);
          grid.append(card);
        });
        grid.toggleAttribute("hidden", groups.length === 0);
        empty?.toggleAttribute("hidden", groups.length > 0);
        setText("[data-group-count]", groups.length);
        setText("[data-group-account-count]", assigned.size);
      } catch (requestError) {
        showNotice(error, requestError.message, true);
      }
    };

    document.querySelectorAll("[data-group-create]").forEach((button) => {
      button.addEventListener("click", () => {
        createForm?.reset();
        showNotice(createNotice);
        createDialog?.showModal();
      });
    });
    document.querySelectorAll("[data-group-close]").forEach((button) => {
      button.addEventListener("click", () => closeDialog(createDialog));
    });
    document.querySelectorAll("[data-group-members-close]").forEach((button) => {
      button.addEventListener("click", () => closeDialog(membersDialog));
    });
    createForm?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const submit = createForm.querySelector("button[type='submit']");
      const data = new FormData(createForm);
      submit?.setAttribute("disabled", "");
      showNotice(createNotice);
      try {
        await api("/api/v1/groups", {
          method: "POST",
          body: JSON.stringify({
            name: String(data.get("name") || "").trim(),
            color: String(data.get("color") || "blue"),
          }),
        });
        closeDialog(createDialog);
        showToast("分组已创建");
        await load();
      } catch (requestError) {
        showNotice(createNotice, requestError.message, true);
      } finally {
        submit?.removeAttribute("disabled");
      }
    });
    membersForm?.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (!contactGroup) {
        return;
      }
      const submit = membersForm.querySelector("button[type='submit']");
      const accountIDs = [...membersForm.querySelectorAll("input[type='checkbox']:checked")]
        .map((input) => Number(input.value))
        .filter((id) => id > 0);
      submit?.setAttribute("disabled", "");
      showNotice(membersNotice);
      try {
        await api(`/api/v1/groups/${encodeURIComponent(contactGroup.id)}/accounts`, {
          method: "PUT",
          body: JSON.stringify({account_ids: accountIDs}),
        });
        closeDialog(membersDialog);
        contactGroup = null;
        showToast("分组账号已更新");
        await load();
      } catch (requestError) {
        showNotice(membersNotice, requestError.message, true);
      } finally {
        submit?.removeAttribute("disabled");
      }
    });
    load();
  };

  const formatDuration = (seconds) => {
    const value = Math.max(0, Number(seconds) || 0);
    if (value === 0) {
      return "0 秒";
    }
    if (value < 60) {
      return `${Math.round(value)} 秒`;
    }
    if (value < 3600) {
      return `${Math.round(value / 60)} 分钟`;
    }
    if (value < 86400) {
      return `${Math.round(value / 3600)} 小时`;
    }
    return `${Math.round(value / 86400)} 天`;
  };

  const createMonitorStatus = (status, notice) => {
    const collection = status?.collection || {};
    setText("[data-monitor-state]", status?.running ? "运行中" : "空闲");
    setText("[data-monitor-started]", formatDate(status?.last_started_at));
    setText("[data-monitor-finished]", formatDate(status?.last_finished_at));
    setText("[data-monitor-next]", formatDate(status?.next_run_at));
    const mode = status?.mode === "event_driven_with_poll_fallback"
      ? "实时监听 + 轮询补漏"
      : "周期轮询";
    setText("[data-monitor-mode]", mode);
    setText(
      "[data-monitor-trigger-counts]",
      `${Number(status?.update_driven_runs) || 0} / ${Number(status?.poll_runs) || 0}`,
    );
    setText(
      "[data-monitor-listeners]",
      `${Number(status?.connected_listeners) || 0} / ${Number(status?.listener_count) || 0}`,
    );
    setText("[data-monitor-update-queue]", status?.queued_update_events ?? "0");
    setText("[data-monitor-targets]", collection.target_count ?? "0");
    setText("[data-monitor-queued]", status?.queued_targets ?? "0");
    setText("[data-monitor-backlog]", collection.backlog_target_count ?? "0");
    setText("[data-monitor-delay]", formatDuration(collection.max_checkpoint_delay_seconds));
    setText("[data-monitor-failures]", collection.consecutive_failures ?? "0");
    setText("[data-monitor-flood]", collection.flood_wait_target_count ?? "0");
    setText("[data-monitor-success]", formatDate(collection.last_successful_at));
    setText("[data-monitor-delivery-queued]", collection.queued_deliveries ?? "0");
    setText("[data-monitor-delivery-failed]", collection.failed_deliveries ?? "0");
    setText("[data-monitor-delivery-oldest]", formatDate(collection.oldest_queued_at));
    setText("[data-monitor-error]", status?.last_error || "—");
    if (status?.last_error) {
      showNotice(notice, status.last_error, true);
    } else if (Number(collection.backlog_target_count) > 0) {
      showNotice(notice, "部分监控目标仍有历史消息积压，checkpoint 暂未推进。", true);
    } else if (Number(collection.flood_wait_target_count) > 0) {
      showNotice(notice, "部分目标受到 Telegram FloodWait 限制，将按周期重试。", true);
    } else if (Number(collection.failed_deliveries) > 0) {
      showNotice(notice, "存在最终失败的通知投递，请检查通知渠道和告警详情。", true);
    }
  };

  const initMonitor = () => {
    const body = document.querySelector("[data-monitor-rules-body]");
    if (!body) {
      return;
    }
    const wrap = document.querySelector("[data-monitor-rules-wrap]");
    const empty = document.querySelector("[data-monitor-rules-empty]");
    const healthBody = document.querySelector("[data-monitor-health-body]");
    const healthWrap = document.querySelector("[data-monitor-health-wrap]");
    const healthEmpty = document.querySelector("[data-monitor-health-empty]");
    const healthState = document.querySelector("[data-monitor-health-state]");
    const accountBody = document.querySelector("[data-monitor-account-body]");
    const accountWrap = document.querySelector("[data-monitor-account-wrap]");
    const accountEmpty = document.querySelector("[data-monitor-account-empty]");
    const accountState = document.querySelector("[data-monitor-account-state]");
    const listenerBody = document.querySelector("[data-monitor-listener-body]");
    const listenerWrap = document.querySelector("[data-monitor-listener-wrap]");
    const listenerEmpty = document.querySelector("[data-monitor-listener-empty]");
    const listenerState = document.querySelector("[data-monitor-listener-state]");
    const alertList = document.querySelector("[data-monitor-alerts]");
    const notice = document.querySelector("[data-monitor-notice]");
    let accounts = [];
    let loading = false;

    const renderRules = (rules) => {
      const accountMap = accountMapFrom(accounts);
      body.replaceChildren();
      rules.forEach((rule) => {
        const account = accountMap.get(Number(rule.account_id));
        const row = document.createElement("tr");
        appendCell(row, rule.name);
        appendCell(row, account ? accountLabel(account) : `账号 ${rule.account_id}`);
        appendCell(row, `${rule.peer_title || peerLabels[rule.peer_type] || "会话"} · ${peerLabels[rule.peer_type] || rule.peer_type}`);
        const keywordText = Array.isArray(rule.keywords) && rule.keywords.length
          ? rule.keywords.join("、")
          : "全部消息";
        appendCell(
          row,
          Number(rule.cooldown_seconds) > 0
            ? `${keywordText} · 冷却 ${Number(rule.cooldown_seconds)} 秒`
            : keywordText,
        );
        appendCell(row, formatDate(rule.last_run_at));
        appendCell(row, rule.last_error
          ? element("span", "badge badge-red", "运行异常")
          : element("span", "badge badge-blue", "运行中"));
        body.append(row);
      });
      wrap?.toggleAttribute("hidden", rules.length === 0);
      empty?.toggleAttribute("hidden", rules.length > 0);
    };

    const renderHealth = (targets) => {
      const accountMap = accountMapFrom(accounts);
      healthBody.replaceChildren();
      targets.forEach((target) => {
        const account = accountMap.get(Number(target.account_id));
        const row = document.createElement("tr");
        appendCell(row, `${account ? accountLabel(account) : `账号 ${target.account_id}`} · ${target.peer_title || peerLabels[target.peer_type] || target.peer_type}`);
        appendCell(row, target.last_successful_at
          ? formatDate(target.last_successful_at)
          : element("span", "badge badge-red", "从未成功"));
        appendCell(row, target.last_successful_at
          ? formatDuration(target.checkpoint_delay_seconds)
          : "—");
        appendCell(row, target.consecutive_failures
          ? element("span", "badge badge-red", String(target.consecutive_failures))
          : element("span", "badge badge-blue", "0"));
        const batch = element("span", "", target.backlog_detected ? "积压" : "已追平");
        if (target.flood_wait) {
          batch.append(" · FloodWait");
        }
        if (target.next_collection_at) {
          batch.append(` · 冷却至 ${formatDate(target.next_collection_at)}`);
        }
        batch.append(` · 上批 ${Number(target.last_batch_fetched) || 0} 条`);
        if (target.last_batch_at) {
          batch.append(` · ${formatDate(target.last_batch_at)}`);
        }
        appendCell(row, batch);
        appendCell(row, target.last_error
          ? element("span", "badge badge-red", target.last_error)
          : "—");
        healthBody.append(row);
      });
      healthWrap?.toggleAttribute("hidden", targets.length === 0);
      healthEmpty?.toggleAttribute("hidden", targets.length > 0);
      const attention = targets.some((target) =>
        target.consecutive_failures > 0 || target.backlog_detected || target.flood_wait);
      if (healthState) {
        healthState.className = attention ? "badge badge-red" : "badge badge-blue";
        healthState.textContent = attention ? "需关注" : targets.length ? "正常" : "无目标";
      }
    };

    const renderAccounts = (accountStates) => {
      if (!accountBody) {
        return;
      }
      const accountMap = accountMapFrom(accounts);
      accountBody.replaceChildren();
      accountStates.forEach((item) => {
        const account = accountMap.get(Number(item.account_id));
        const row = document.createElement("tr");
        appendCell(row, account ? accountLabel(account) : `账号 ${item.account_id}`);
        const nextRun = item.next_collection_at
          ? new Date(item.next_collection_at).getTime()
          : 0;
        let state = element("span", "badge badge-neutral", "等待调度");
        if (item.running) {
          state = element("span", "badge badge-blue", "采集处理中");
        } else if (item.flood_wait || nextRun > Date.now()) {
          state = element("span", "badge badge-red", "退避中");
        } else if (Number(item.consecutive_failures) > 0) {
          state = element("span", "badge badge-red", "需关注");
        }
        appendCell(row, state);
        appendCell(
          row,
          `${Number(item.processed_targets) || 0} / ${Number(item.target_count) || 0}`,
        );
        appendCell(row, Number(item.queued_targets) || 0);
        appendCell(row, item.next_collection_at ? formatDate(item.next_collection_at) : "立即");
        appendCell(
          row,
          Number(item.consecutive_failures)
            ? element("span", "badge badge-red", String(item.consecutive_failures))
            : element("span", "badge badge-blue", "0"),
        );
        appendCell(row, item.last_error || "—");
        accountBody.append(row);
      });
      accountWrap?.toggleAttribute("hidden", accountStates.length === 0);
      accountEmpty?.toggleAttribute("hidden", accountStates.length > 0);
      if (accountState) {
        const running = accountStates.some((item) => item.running);
        const attention = accountStates.some((item) =>
          item.flood_wait || item.consecutive_failures > 0);
        accountState.className = attention
          ? "badge badge-red"
          : running
            ? "badge badge-blue"
            : "badge badge-neutral";
        accountState.textContent = attention
          ? "需关注"
          : running
            ? "运行中"
            : accountStates.length
              ? "等待调度"
              : "无账号";
      }
    };

    const renderListeners = (listeners) => {
      if (!listenerBody) {
        return;
      }
      const accountMap = accountMapFrom(accounts);
      listenerBody.replaceChildren();
      listeners.forEach((listener) => {
        const account = accountMap.get(Number(listener.account_id));
        const row = document.createElement("tr");
        appendCell(row, account ? accountLabel(account) : `账号 ${listener.account_id}`);
        appendCell(row, listener.connected
          ? element("span", "badge badge-blue", "已连接")
          : element("span", "badge badge-red", "未连接"));
        appendCell(row, `${Number(listener.events_observed) || 0} 次`);
        appendCell(
          row,
          `${Number(listener.disconnections) || 0} 次断开 · ${Number(listener.gap_recovery_count) || 0} 次补漏`,
        );
        appendCell(row, listener.last_event_at ? formatDate(listener.last_event_at) : "—");
        appendCell(row, listener.last_error || "—");
        listenerBody.append(row);
      });
      listenerWrap?.toggleAttribute("hidden", listeners.length === 0);
      listenerEmpty?.toggleAttribute("hidden", listeners.length > 0);
      if (listenerState) {
        const disconnected = listeners.some((listener) => !listener.connected);
        listenerState.className = disconnected ? "badge badge-red" : "badge badge-blue";
        listenerState.textContent = disconnected
          ? "需关注"
          : listeners.length
            ? "正常"
            : "等待连接";
      }
    };

    const load = async () => {
      if (loading) {
        return;
      }
      loading = true;
      showNotice(notice);
      try {
        const [rulesResult, alertsResult, statusResult, targetsResult, accountsResult] = await Promise.allSettled([
          api("/api/v1/monitor/rules?enabled=true"),
          api("/api/v1/monitor/alerts?state=pending&limit=5"),
          api("/api/v1/monitor/status"),
          api("/api/v1/monitor/targets"),
          loadAccounts(),
        ]);
        accounts = accountsResult.status === "fulfilled" ? accountsResult.value : [];
        const rules = rulesResult.status === "fulfilled" &&
          Array.isArray(rulesResult.value?.items) ? rulesResult.value.items : [];
        const alerts = alertsResult.status === "fulfilled" &&
          Array.isArray(alertsResult.value?.items) ? alertsResult.value.items : [];
        const targets = targetsResult.status === "fulfilled" &&
          Array.isArray(targetsResult.value?.items) ? targetsResult.value.items : [];
        renderRules(rules);
        renderHealth(targets);
        setText("[data-monitor-rule-count]", rules.length);
        setText("[data-monitor-pending-count]", alerts.length);
        if (statusResult.status === "fulfilled") {
          createMonitorStatus(statusResult.value, notice);
          renderAccounts(Array.isArray(statusResult.value?.accounts)
            ? statusResult.value.accounts
            : []);
          renderListeners(Array.isArray(statusResult.value?.listeners)
            ? statusResult.value.listeners
            : []);
        } else {
          createMonitorStatus(null, notice);
          renderAccounts([]);
          renderListeners([]);
          showNotice(notice, statusResult.reason.message, true);
        }
        if (alertList) {
          alertList.replaceChildren(
            ...(alerts.length
              ? alerts.slice(0, 4).map(renderCompactAlert)
              : [createEmptyCopy("当前没有待处理告警。")]),
          );
        }
        renderNotificationPanel(alerts);
        if (rulesResult.status === "rejected") {
          showNotice(notice, rulesResult.reason.message, true);
        }
        if (alertsResult.status === "rejected") {
          showNotice(notice, alertsResult.reason.message, true);
        }
        if (targetsResult.status === "rejected") {
          showNotice(notice, targetsResult.reason.message, true);
        }
      } catch (requestError) {
        showNotice(notice, requestError.message, true);
      } finally {
        loading = false;
      }
    };

    document.querySelector("[data-monitor-run]")?.addEventListener("click", async (event) => {
      const button = event.currentTarget;
      button.disabled = true;
      showNotice(notice);
      try {
        const status = await api("/api/v1/monitor/run", {method: "POST", body: "{}"});
        createMonitorStatus(status, notice);
        showToast("监控任务已启动");
        window.setTimeout(load, 1500);
      } catch (requestError) {
        showNotice(notice, requestError.message, true);
      } finally {
        button.disabled = false;
      }
    });
    load();
    window.setInterval(() => {
      if (!document.hidden) {
        load();
      }
    }, 15000);
  };

  const initMonitorTasks = () => {
    const body = document.querySelector("[data-tasks-body]");
    if (!body) {
      return;
    }
    const wrap = document.querySelector("[data-tasks-wrap]");
    const empty = document.querySelector("[data-tasks-empty]");
    const error = document.querySelector("[data-tasks-error]");
    const channelBody = document.querySelector("[data-channel-list-body]");
    const channelError = document.querySelector("[data-channel-list-error]");
    const dialog = document.getElementById("monitor-rule-dialog");
    const form = document.querySelector("[data-monitor-rule-form]");
    const formNotice = document.querySelector("[data-monitor-rule-notice]");
    const dialogTitle = document.querySelector("[data-rule-dialog-title]");
    const accountSelect = document.querySelector("[data-monitor-account]");
    const chatSelect = document.querySelector("[data-monitor-chat]");
    const channelSelect = document.querySelector("[data-monitor-channel]");
    const previewButton = document.querySelector("[data-rule-preview]");
    const previewResults = document.querySelector("[data-rule-preview-results]");
    const previewSummary = document.querySelector("[data-rule-preview-summary]");
    const previewSamples = document.querySelector("[data-rule-preview-samples]");
    const historyDialog = document.getElementById("monitor-rule-history-dialog");
    const versionList = document.querySelector("[data-rule-version-list]");
    const historyError = document.querySelector("[data-rule-history-error]");
    const channelDialog = document.getElementById("notification-channel-dialog");
    const channelForm = document.querySelector("[data-notification-channel-form]");
    const channelNotice = document.querySelector("[data-notification-channel-notice]");
    let accounts = [];
    let channels = [];
    let chatRequest = 0;
    let editingRule = null;

    const loadChats = async () => {
      if (!chatSelect) {
        return;
      }
      const requestID = ++chatRequest;
      const accountID = Number(accountSelect?.value);
      fillSelect(chatSelect, [], "正在读取会话...");
      chatSelect.disabled = true;
      if (!Number.isFinite(accountID) || accountID <= 0) {
        fillSelect(chatSelect, [], "请选择在线账号");
        return;
      }
      try {
        const payload = await api(`/api/v1/accounts/${encodeURIComponent(accountID)}/chats?limit=100`);
        if (requestID !== chatRequest) {
          return;
        }
        const chats = Array.isArray(payload?.items) ? payload.items : [];
        fillSelect(chatSelect, chats.map((chat) => ({
          value: `${chat.type}:${chat.peer_id}:${chat.title || ""}`,
          label: `${chat.title || "未命名会话"} · ${peerLabels[chat.type] || chat.type}`,
        })), "请选择监听取话");
      } catch (requestError) {
        if (requestID === chatRequest) {
          fillSelect(chatSelect, [], "会话读取失败");
          showNotice(formNotice, requestError.message, true);
        }
      }
    };

    const renderRules = (rules) => {
      const accountMap = accountMapFrom(accounts);
      body.replaceChildren();
      rules.forEach((rule) => {
        const account = accountMap.get(Number(rule.account_id));
        const row = document.createElement("tr");
        const ruleIdentity = element("span", "business-cell-stack");
        ruleIdentity.append(
          element("strong", "", rule.name),
          element("small", "", `v${rule.version || 1} · 责任人 ${rule.owner || "未设置"}`),
        );
        appendCell(row, ruleIdentity);
        const peer = element("span", "business-cell-stack");
        peer.append(
          element("strong", "", rule.peer_title || peerLabels[rule.peer_type] || "会话"),
          element("small", "", `${account ? accountLabel(account) : `账号 ${rule.account_id}`} · ${peerLabels[rule.peer_type] || rule.peer_type}`),
        );
        appendCell(row, peer);
        const terms = element("span", "business-cell-stack");
        terms.append(element(
          "strong",
          "",
          Array.isArray(rule.keywords) && rule.keywords.length
            ? rule.keywords.join("、")
            : "全部消息",
        ));
        if (Array.isArray(rule.exclude_keywords) && rule.exclude_keywords.length) {
          terms.append(element("small", "", `排除：${rule.exclude_keywords.join("、")}`));
        }
        appendCell(row, terms);
        appendCell(row, rule.notification_channel_name || "站内通知");
        appendCell(row, formatDate(rule.updated_at));
        const toggle = document.createElement("input");
        toggle.type = "checkbox";
        toggle.checked = Boolean(rule.enabled);
        toggle.setAttribute("aria-label", `${rule.enabled ? "停用" : "启用"}规则 ${rule.name}`);
        toggle.addEventListener("change", async () => {
          toggle.disabled = true;
          try {
            await api(`/api/v1/monitor/rules/${encodeURIComponent(rule.id)}`, {
              method: "PUT",
              body: JSON.stringify({enabled: toggle.checked}),
            });
            showToast(toggle.checked ? "规则已启用" : "规则已停用");
            await load();
          } catch (requestError) {
            toggle.checked = !toggle.checked;
            showNotice(error, requestError.message, true);
          } finally {
            toggle.disabled = false;
          }
        });
        appendCell(row, toggle);
        const actions = element("span", "business-actions");
        const edit = element("button", "button button-secondary button-compact", "编辑");
        edit.type = "button";
        edit.addEventListener("click", () => openRuleDialog(rule));
        const history = element("button", "button button-secondary button-compact", "版本");
        history.type = "button";
        history.addEventListener("click", () => openRuleHistory(rule));
        const remove = element("button", "button button-danger-ghost button-compact", "删除");
        remove.type = "button";
        remove.addEventListener("click", async () => {
          if (!window.confirm(`删除监控规则“${rule.name}”？`)) {
            return;
          }
          remove.disabled = true;
          try {
            await api(`/api/v1/monitor/rules/${encodeURIComponent(rule.id)}`, {method: "DELETE"});
            showToast("监控规则已删除");
            await load();
          } catch (requestError) {
            showNotice(error, requestError.message, true);
          } finally {
            remove.disabled = false;
          }
        });
        actions.append(edit, history, remove);
        appendCell(row, actions);
        body.append(row);
      });
      wrap?.toggleAttribute("hidden", rules.length === 0);
      empty?.toggleAttribute("hidden", rules.length > 0);
      setText("[data-task-count]", rules.length);
      setText("[data-task-enabled-count]", rules.filter((rule) => rule.enabled).length);
    };

    const renderChannels = (items, rules) => {
      const usage = new Map();
      rules.forEach((rule) => {
        const id = Number(rule.notification_channel_id);
        usage.set(id, (usage.get(id) || 0) + 1);
      });
      channelBody.replaceChildren();
      items.forEach((channel) => {
        const row = document.createElement("tr");
        const identity = element("span", "business-cell-stack");
        identity.append(
          element("strong", "", channel.name || "未命名渠道"),
          element("small", "", channel.kind === "in_app" ? "系统内置" : "Webhook"),
        );
        appendCell(row, identity);
        appendCell(row, channel.kind === "in_app" ? "站内通知" : "Webhook");
        const status = element(
          "span",
          `badge ${
            channel.configured && channel.enabled ? "badge-green" : "badge-red"
          }`,
          channel.configured
            ? channel.enabled ? "可用" : "已停用"
            : "配置缺失",
        );
        appendCell(row, status);
        appendCell(row, formatDate(channel.created_at));
        if (channel.kind === "in_app") {
          appendCell(row, "不可删除");
        } else {
          const references = usage.get(Number(channel.id)) || 0;
          const remove = element(
            "button",
            "button button-danger-ghost button-compact",
            references > 0 ? `使用中 · ${references}` : "删除",
          );
          remove.type = "button";
          remove.disabled = references > 0;
          remove.title = references > 0 ? "请先删除引用此渠道的监控规则" : "";
          remove.addEventListener("click", async () => {
            if (references > 0 ||
                !window.confirm(`删除通知渠道“${channel.name}”？`)) {
              return;
            }
            remove.disabled = true;
            try {
              await api(`/api/v1/monitor/channels/${encodeURIComponent(channel.id)}`, {
                method: "DELETE",
              });
              showToast("通知渠道已删除");
              await load();
            } catch (requestError) {
              showNotice(channelError, requestError.message, true);
            } finally {
              remove.disabled = false;
            }
          });
          appendCell(row, remove);
        }
        channelBody.append(row);
      });
      setText("[data-channel-count]", items.length);
    };

    const load = async () => {
      showNotice(error);
      showNotice(channelError);
      try {
        const [rulesPayload, channelPayload, accountItems] = await Promise.all([
          api("/api/v1/monitor/rules"),
          api("/api/v1/monitor/channels"),
          loadAccounts(),
        ]);
        accounts = accountItems;
        channels = Array.isArray(channelPayload?.items) ? channelPayload.items : [];
        const rules = Array.isArray(rulesPayload?.items) ? rulesPayload.items : [];
        renderRules(rules);
        renderChannels(channels, rules);
      } catch (requestError) {
        showNotice(error, requestError.message, true);
      }
    };

    const parseRuleTerms = (value) => String(value || "")
      .split(/[\n,，]+/)
      .map((item) => item.trim())
      .filter(Boolean);

    const resetRulePreview = () => {
      previewResults?.setAttribute("hidden", "");
      previewSamples?.replaceChildren();
      if (previewSummary) {
        previewSummary.textContent = "尚未预览";
      }
    };

    const renderRulePreview = (preview) => {
      if (!previewResults || !previewSamples) {
        return;
      }
      previewResults.removeAttribute("hidden");
      if (previewSummary) {
        previewSummary.textContent =
          `扫描 ${preview.scanned || 0} 条，命中 ${preview.matched || 0} 条，` +
          `排除 ${preview.excluded || 0} 条；仅展示最近样本。`;
      }
      const sections = [];
      const createSection = (title, items) => {
        if (!Array.isArray(items) || items.length === 0) {
          return;
        }
        const section = element("section", "rule-preview-section");
        section.append(element("h4", "", title));
        items.forEach((item) => {
          const sample = element("article", "rule-preview-sample");
          const meta = element(
            "small",
            "",
            [
              `#${item.message_id}`,
              item.sender || "未知发送者",
              item.matched_keyword ? `命中“${item.matched_keyword}”` : "",
              item.excluded_by ? `排除词“${item.excluded_by}”` : "",
            ].filter(Boolean).join(" · "),
          );
          sample.append(meta, element("p", "", item.text || "无文本内容"));
          section.append(sample);
        });
        sections.push(section);
      };
      createSection("命中样本", preview.samples);
      createSection("被排除样本", preview.excluded_items);
      if (sections.length === 0) {
        sections.push(element("p", "muted", "最近消息中没有符合当前规则的样本。"));
      }
      previewSamples.replaceChildren(...sections);
    };

    const openRuleDialog = (rule = null) => {
      if (!form || !dialog) {
        return;
      }
      editingRule = rule;
      form.reset();
      resetRulePreview();
      showNotice(formNotice);
      if (dialogTitle) {
        dialogTitle.textContent = rule ? "编辑规则" : "新建规则";
      }
      const onlineAccounts = accounts.filter(
        (account) => accountDisplayStatus(account).key === "online",
      );
      const accountChoices = rule ? accounts : onlineAccounts;
      fillSelect(accountSelect, accountChoices.map((account) => ({
        value: String(account.id),
        label: `${accountLabel(account)} · ${formatPhone(account.phone_number)}`,
      })), accountChoices.length ? "请选择账号" : "没有可用账号");
      fillSelect(channelSelect, channels
        .filter((channel) => channel.enabled)
        .map((channel) => ({
          value: String(channel.id),
          label: channel.name,
        })), "请选择通知渠道");
      if (rule) {
        form.elements.namedItem("name").value = rule.name || "";
        form.elements.namedItem("keywords").value =
          Array.isArray(rule.keywords) ? rule.keywords.join("\n") : "";
        form.elements.namedItem("exclude_keywords").value =
          Array.isArray(rule.exclude_keywords) ? rule.exclude_keywords.join("\n") : "";
        form.elements.namedItem("owner").value = rule.owner || "";
        form.elements.namedItem("cooldown_seconds").value = String(rule.cooldown_seconds || 0);
        form.elements.namedItem("notification_channel_id").value =
          String(rule.notification_channel_id || 1);
        form.elements.namedItem("account_id").value = String(rule.account_id || "");
        const peerValue = [
          rule.peer_type,
          String(rule.peer_id),
          rule.peer_title || "",
        ].join(":");
        fillSelect(chatSelect, [{
          value: peerValue,
          label: `${rule.peer_title || peerLabels[rule.peer_type] || "会话"} · ${
            peerLabels[rule.peer_type] || rule.peer_type
          }`,
        }], "当前监听取话");
        chatSelect.value = peerValue;
      } else {
        fillSelect(chatSelect, [], "请选择账号");
        if (accountSelect?.options.length > 1) {
          accountSelect.selectedIndex = 1;
          loadChats();
        }
      }
      dialog.showModal();
    };

    const openRuleHistory = async (rule) => {
      if (!historyDialog || !versionList) {
        return;
      }
      versionList.replaceChildren(element("p", "muted", "正在读取版本历史..."));
      showNotice(historyError);
      historyDialog.showModal();
      try {
        const payload = await api(
          `/api/v1/monitor/rules/${encodeURIComponent(rule.id)}/versions`,
        );
        const versions = Array.isArray(payload?.items) ? payload.items : [];
        if (versions.length === 0) {
          versionList.replaceChildren(element("p", "muted", "暂无版本记录。"));
          return;
        }
        versionList.replaceChildren(...versions.map((version) => {
          const item = element("article", "rule-version-item");
          const header = element("div", "rule-version-header");
          header.append(
            element("strong", "", `版本 ${version.version}`),
            element(
              "span",
              `badge ${version.enabled ? "badge-green" : "badge-neutral"}`,
              version.enabled ? "启用" : "停用",
            ),
          );
          const terms = Array.isArray(version.keywords) && version.keywords.length
            ? `关键词：${version.keywords.join("、")}`
            : "关键词：全部消息";
          const excludes = Array.isArray(version.exclude_keywords) &&
            version.exclude_keywords.length
            ? `排除：${version.exclude_keywords.join("、")}`
            : "排除：无";
          item.append(
            header,
            element("small", "", `操作者 ${version.actor || "system"} · ${formatDate(version.created_at)}`),
            element("p", "", terms),
            element("p", "", excludes),
            element("p", "", `责任人：${version.owner || "未设置"}`),
          );
          return item;
        }));
      } catch (requestError) {
        versionList.replaceChildren();
        showNotice(historyError, requestError.message, true);
      }
    };

    document.querySelectorAll("[data-task-create]").forEach((button) => {
      button.addEventListener("click", () => openRuleDialog());
    });
    document.querySelectorAll("[data-monitor-rule-close]").forEach((button) => {
      button.addEventListener("click", () => {
        dialog?.close();
        editingRule = null;
      });
    });
    document.querySelectorAll("[data-rule-history-close]").forEach((button) => {
      button.addEventListener("click", () => historyDialog?.close());
    });
    document.querySelectorAll("[data-channel-create]").forEach((button) => {
      button.addEventListener("click", () => {
        channelForm?.reset();
        showNotice(channelNotice);
        channelDialog?.showModal();
      });
    });
    document.querySelectorAll("[data-notification-channel-close]").forEach((button) => {
      button.addEventListener("click", () => channelDialog?.close());
    });
    channelForm?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const submit = channelForm.querySelector("button[type='submit']");
      const data = new FormData(channelForm);
      submit?.setAttribute("disabled", "");
      showNotice(channelNotice);
      try {
        await api("/api/v1/monitor/channels", {
          method: "POST",
          body: JSON.stringify({
            name: String(data.get("name") || "").trim(),
            kind: "webhook",
            url: String(data.get("url") || "").trim(),
            secret: String(data.get("secret") || "").trim(),
          }),
        });
        channelDialog?.close();
        showToast("Webhook 通知渠道已添加");
        await load();
      } catch (requestError) {
        showNotice(channelNotice, requestError.message, true);
      } finally {
        submit?.removeAttribute("disabled");
      }
    });
    accountSelect?.addEventListener("change", loadChats);
    previewButton?.addEventListener("click", async () => {
      if (!form) {
        return;
      }
      const data = new FormData(form);
      const [peerType, peerID] = String(data.get("chat") || "").split(":");
      if (!peerType || !Number(peerID)) {
        showNotice(formNotice, "请先选择监听取话。", true);
        return;
      }
      previewButton.disabled = true;
      showNotice(formNotice);
      try {
        const preview = await api("/api/v1/monitor/rules/preview", {
          method: "POST",
          body: JSON.stringify({
            account_id: Number(data.get("account_id")),
            peer_type: peerType,
            peer_id: Number(peerID),
            keywords: parseRuleTerms(data.get("keywords")),
            exclude_keywords: parseRuleTerms(data.get("exclude_keywords")),
            limit: 10,
          }),
        });
        renderRulePreview(preview);
      } catch (requestError) {
        showNotice(formNotice, requestError.message, true);
      } finally {
        previewButton.disabled = false;
      }
    });
    form?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const submit = form.querySelector("button[type='submit']");
      const data = new FormData(form);
      const [peerType, peerID, ...titleParts] = String(data.get("chat") || "").split(":");
      if (!peerType || !Number(peerID)) {
        showNotice(formNotice, "请选择监听取话。", true);
        return;
      }
      const keywords = parseRuleTerms(data.get("keywords"));
      const excludeKeywords = parseRuleTerms(data.get("exclude_keywords"));
      submit?.setAttribute("disabled", "");
      showNotice(formNotice);
      const payload = {
        name: String(data.get("name") || "").trim(),
        account_id: Number(data.get("account_id")),
        peer_type: peerType,
        peer_id: Number(peerID),
        peer_title: titleParts.join(":"),
        keywords,
        exclude_keywords: excludeKeywords,
        owner: String(data.get("owner") || "").trim(),
        cooldown_seconds: Number(data.get("cooldown_seconds")) || 0,
        notification_channel_id: Number(data.get("notification_channel_id")),
      };
      try {
        if (editingRule) {
          await api(`/api/v1/monitor/rules/${encodeURIComponent(editingRule.id)}`, {
            method: "PUT",
            body: JSON.stringify(payload),
          });
        } else {
          await api("/api/v1/monitor/rules", {
            method: "POST",
            body: JSON.stringify(payload),
          });
        }
        dialog?.close();
        showToast(editingRule ? "监控规则已更新" : "监控规则已创建");
        editingRule = null;
        await load();
      } catch (requestError) {
        showNotice(formNotice, requestError.message, true);
      } finally {
        submit?.removeAttribute("disabled");
      }
    });
    load();
  };

  const initAlerts = () => {
    const list = document.querySelector("[data-alerts-list]");
    if (!list) {
      return;
    }
    const empty = document.querySelector("[data-alerts-empty]");
    const error = document.querySelector("[data-alerts-error]");
    const state = document.querySelector("[data-alert-state]");
    const dialog = document.querySelector("#alert-action-dialog");
    const actionForm = document.querySelector("[data-alert-action-form]");
    const actionSelect = document.querySelector("[data-alert-action]");
    const actionSummary = document.querySelector("[data-alert-action-summary]");
    const actionNotice = document.querySelector("[data-alert-action-notice]");
    const severityField = document.querySelector("[data-alert-severity-field]");
    const assigneeField = document.querySelector("[data-alert-assignee-field]");

    const stateMeta = (value) => ({
      pending: { label: "待处理", className: "badge-red", action: "acknowledge" },
      acknowledged: { label: "处理中", className: "badge-blue", action: "resolve" },
      resolved: { label: "已解决", className: "badge-neutral", action: "reopen" },
    }[value] || { label: value || "未知", className: "badge-neutral", action: "note" });
    const severityMeta = (value) => ({
      low: { label: "低", className: "badge-neutral" },
      medium: { label: "中", className: "badge-blue" },
      high: { label: "高", className: "badge-red" },
      critical: { label: "严重", className: "badge-red" },
    }[value] || { label: value || "未分级", className: "badge-neutral" });
    const syncActionFields = () => {
      const action = actionSelect?.value;
      severityField?.toggleAttribute("hidden", action !== "set_severity");
      assigneeField?.toggleAttribute("hidden", action !== "assign");
    };

    const load = async () => {
      showNotice(error);
      try {
        const query = new URLSearchParams({limit: "100"});
        if (state?.value) {
          query.set("state", state.value);
        }
        const payload = await api(`/api/v1/monitor/alerts?${query}`);
        const items = Array.isArray(payload?.items) ? payload.items : [];
        list.replaceChildren(...items.map((alert) => {
          const card = element("article", "business-list-item");
          const icon = element("span", "business-row-icon business-row-icon-alert");
          icon.append(element("span", "icon icon-bell-ring"));
          const copy = element("span", "business-list-copy");
          const heading = element("span", "business-list-heading");
          const currentState = stateMeta(alert.state);
          const severity = severityMeta(alert.severity);
          heading.append(
            element(
              "strong",
              "",
              `${alert.rule_name || "监控规则"}${alert.rule_version ? ` · v${alert.rule_version}` : ""}`,
            ),
            element("span", `badge ${severity.className}`, severity.label),
            element("span", `badge ${currentState.className}`, currentState.label),
          );
          copy.append(
            heading,
            element("p", "", alert.message_preview || "监控命中"),
            element("small", "", `${alert.peer_title || "会话"} · ${alert.notification_channel_name || "站内通知"} · ${formatDate(alert.created_at)}${alert.matched_keyword ? ` · 命中 ${alert.matched_keyword}` : ""}`),
            element("small", "", `负责人：${alert.assigned_to || "未分派"}${alert.resolved_by ? ` · 解决人：${alert.resolved_by} · ${formatDate(alert.resolved_at)}` : alert.acknowledged_by ? ` · 确认人：${alert.acknowledged_by}` : ""}`),
          element("small", "", alert.delivery_state === "delivered"
            ? `投递成功 · ${formatDate(alert.delivery_at)}`
            : alert.delivery_state === "queued"
              ? `等待投递 · 第 ${Number(alert.delivery_attempts) || 0} 次尝试${
                alert.delivery_next_attempt_at
                  ? ` · 下次 ${formatDate(alert.delivery_next_attempt_at)}`
                  : ""
              }`
              : alert.delivery_state === "failed"
                ? `投递失败${alert.delivery_error ? ` · ${alert.delivery_error}` : ""}`
                : "无投递记录"),
          );
          card.append(icon, copy);
          const processButton = element("button", "button button-secondary button-compact", "处理告警");
          processButton.type = "button";
          processButton.addEventListener("click", () => {
            actionForm.reset();
            actionForm.elements.alert_id.value = String(alert.id);
            actionSelect.value = currentState.action;
            actionForm.elements.severity.value = alert.severity || "medium";
            actionForm.elements.assignee.value = alert.assigned_to || "";
            actionSummary.textContent = `${alert.rule_name || "监控规则"} · ${alert.message_preview || "监控命中"}`;
            showNotice(actionNotice);
            syncActionFields();
            dialog.showModal();
          });
          card.append(processButton);
          return card;
        }));
        list.toggleAttribute("hidden", items.length === 0);
        empty?.toggleAttribute("hidden", items.length > 0);
        setText("[data-alert-total]", items.length);
      } catch (requestError) {
        showNotice(error, requestError.message, true);
      }
    };
    actionSelect?.addEventListener("change", syncActionFields);
    document.querySelectorAll("[data-alert-action-close]").forEach((button) => {
      button.addEventListener("click", () => dialog.close());
    });
    actionForm?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const data = new FormData(actionForm);
      const alertID = Number(data.get("alert_id"));
      const payload = {
        action: String(data.get("action") || ""),
        severity: String(data.get("severity") || ""),
        assignee: String(data.get("assignee") || "").trim(),
        note: String(data.get("note") || "").trim(),
      };
      const submit = actionForm.querySelector('button[type="submit"]');
      submit.disabled = true;
      showNotice(actionNotice);
      try {
        await api(`/api/v1/monitor/alerts/${encodeURIComponent(alertID)}`, {
          method: "PATCH",
          body: JSON.stringify(payload),
        });
        dialog.close();
        showToast("告警处理状态已更新");
        await load();
      } catch (requestError) {
        showNotice(actionNotice, requestError.message, true);
      } finally {
        submit.disabled = false;
      }
    });
    state?.addEventListener("change", load);
    document.querySelector("[data-alerts-refresh]")?.addEventListener("click", load);
    load();
  };

  const initMessages = () => {
    const form = document.querySelector("[data-message-filter]");
    if (!form) {
      return;
    }
    const body = document.querySelector("[data-messages-body]");
    const wrap = document.querySelector("[data-messages-wrap]");
    const empty = document.querySelector("[data-messages-empty]");
    const pagination = document.querySelector("[data-messages-pagination]");
    const error = document.querySelector("[data-messages-index-error]");
    const accountSelect = document.querySelector("[data-message-account]");
    const indexState = document.querySelector("[data-search-index-state]");
    const indexError = document.querySelector("[data-search-index-error]");
    const rebuildButton = document.querySelector("[data-search-index-rebuild]");
    let offset = 0;
    let total = 0;
    let accounts = [];
    const limit = 50;

    const renderIndexHealth = (health) => {
      const healthy = health?.status === "healthy" && health?.integrity_ok;
      if (indexState) {
        indexState.className = healthy ? "badge badge-blue" : "badge badge-red";
        indexState.textContent = healthy ? "索引一致" : "需要重建";
      }
      setText("[data-search-index-searchable]", Number(health?.searchable_count) || 0);
      setText("[data-search-index-documents]", Number(health?.document_count) || 0);
      setText("[data-search-index-missing]", Number(health?.missing_count) || 0);
      setText("[data-search-index-orphans]", Number(health?.orphan_count) || 0);
      setText("[data-search-index-deleted]", Number(health?.deleted_indexed_count) || 0);
      setText("[data-search-index-checked]", formatDate(health?.checked_at));
      setText(
        "[data-search-index-rebuilt]",
        health?.last_rebuilt_at
          ? `${formatDate(health.last_rebuilt_at)} · 第 ${Number(health.rebuild_count) || 0} 次`
          : "尚未重建",
      );
      const detail = health?.integrity_error || health?.last_rebuild_error || "";
      showNotice(
        indexError,
        detail || (healthy ? "" : "全文索引与消息投影不一致，请执行重建。"),
        !healthy,
      );
    };

    const loadIndexHealth = async () => {
      try {
        renderIndexHealth(await api("/api/v1/search/health"));
      } catch (requestError) {
        if (indexState) {
          indexState.className = "badge badge-red";
          indexState.textContent = "状态不可用";
        }
        showNotice(indexError, requestError.message, true);
      }
    };

    const queryFromForm = () => {
      const data = new FormData(form);
      const query = new URLSearchParams({limit: String(limit), offset: String(offset)});
      const q = String(data.get("q") || "").trim();
      const accountID = String(data.get("account_id") || "");
      const peerType = String(data.get("peer_type") || "");
      if (q) {
        query.set("q", q);
      }
      if (accountID) {
        query.set("account_id", accountID);
      }
      if (peerType) {
        query.set("peer_type", peerType);
      }
      for (const key of ["from", "to"]) {
        const value = String(data.get(key) || "");
        if (!value) {
          continue;
        }
        const date = new Date(value);
        if (!Number.isNaN(date.getTime())) {
          query.set(key, date.toISOString());
        }
      }
      return query;
    };

    const load = async () => {
      showNotice(error);
      try {
        const payload = await api(`/api/v1/messages?${queryFromForm()}`);
        const items = Array.isArray(payload?.items) ? payload.items : [];
        total = Number(payload?.total) || 0;
        const accountMap = accountMapFrom(accounts);
        body?.replaceChildren(...items.map((message) => {
          const row = document.createElement("tr");
          appendCell(row, formatDate(message.sent_at));
          appendCell(row, accountLabel(accountMap.get(Number(message.account_id))) || `账号 ${message.account_id}`);
          appendCell(row, `${message.peer_title || peerLabels[message.peer_type] || "会话"} · ${peerLabels[message.peer_type] || message.peer_type}`);
          appendCell(row, message.sender || "未知发送者");
          const text = element("span", "business-message-preview", message.text || "空消息");
          appendCell(row, text);
          return row;
        }));
        wrap?.toggleAttribute("hidden", items.length === 0);
        empty?.toggleAttribute("hidden", items.length > 0);
        setText("[data-message-total]", `共 ${total} 条`);
        pagination?.toggleAttribute("hidden", total <= limit);
        setText("[data-messages-page]", `第 ${Math.floor(offset / limit) + 1} 页`);
        const prev = document.querySelector("[data-messages-prev]");
        const next = document.querySelector("[data-messages-next]");
        if (prev) {
          prev.disabled = offset <= 0;
        }
        if (next) {
          next.disabled = offset + limit >= total;
        }
      } catch (requestError) {
        showNotice(error, requestError.message, true);
      }
    };

    form.addEventListener("submit", (event) => {
      event.preventDefault();
      offset = 0;
      load();
    });
    document.querySelector("[data-messages-prev]")?.addEventListener("click", () => {
      offset = Math.max(0, offset - limit);
      load();
    });
    document.querySelector("[data-messages-next]")?.addEventListener("click", () => {
      if (offset + limit < total) {
        offset += limit;
        load();
      }
    });
    document.querySelector("[data-search-index-refresh]")
      ?.addEventListener("click", loadIndexHealth);
    rebuildButton?.addEventListener("click", async () => {
      rebuildButton.disabled = true;
      showNotice(indexError);
      try {
        renderIndexHealth(await api("/api/v1/search/rebuild", {
          method: "POST",
          body: "{}",
        }));
        showToast("搜索索引已重建");
      } catch (requestError) {
        showNotice(indexError, requestError.message, true);
      } finally {
        rebuildButton.disabled = false;
      }
    });
    loadIndexHealth();
    loadAccounts()
      .then((items) => {
        accounts = items;
        fillSelect(accountSelect, items.map((account) => ({
          value: String(account.id),
          label: `${accountLabel(account)} · ${formatPhone(account.phone_number)}`,
        })), "全部账号");
        load();
      })
      .catch((requestError) => showNotice(error, requestError.message, true));
  };

  const initCorrelations = () => {
    const list = document.querySelector("[data-correlation-list]");
    if (!list) {
      return;
    }
    const empty = document.querySelector("[data-correlation-empty]");
    const error = document.querySelector("[data-correlation-error]");
    const warning = document.querySelector("[data-correlation-warning]");
    const days = document.querySelector("[data-correlation-days]");
    const type = document.querySelector("[data-correlation-type]");
    let accounts = [];
    let requestID = 0;

    const renderSource = (source) => {
      const account = accounts.find((item) => Number(item.id) === Number(source.account_id));
      const row = element("div", "business-compact-row");
      const icon = element("span", "business-row-icon");
      icon.append(element("span", "icon icon-message-square-text"));
      const copy = element("span", "business-row-copy");
      copy.append(
        element(
          "strong",
          "",
          account ? accountLabel(account) : `账号 ${source.account_id}`,
        ),
        element(
          "small",
          "",
          `${source.peer_title || peerLabels[source.peer_type] || "会话"} · ${source.sender || "未知发送者"} · ${formatDate(source.sent_at)}`,
        ),
        element(
          "small",
          "",
          `消息 #${source.message_id} · 证据 ${String(source.evidence_event_id || "未记录").slice(0, 16)}`,
        ),
      );
      if (source.text_preview) {
        copy.append(element("p", "business-message-preview", source.text_preview));
      }
      row.append(icon, copy);
      return row;
    };

    const renderGroup = (group) => {
      const details = correlationTypeDetails[group.type] || {
        label: "关联线索",
        icon: "folder-users",
      };
      const card = element("article", "business-list-item");
      const heading = element("div", "business-list-heading");
      const icon = element("span", "business-row-icon");
      icon.append(element("span", `icon icon-${details.icon}`));
      const copy = element("span", "business-list-copy");
      copy.append(
        element("strong", "", group.match || details.label),
        element(
          "small",
          "",
          `${details.label}${group.network_label ? ` · ${group.network_label}` : ""} · ${group.account_count} 个账号 · ${group.source_count} 条来源`,
        ),
        element("p", "", group.confidence_reason || "请结合原始消息人工核验。"),
      );
      if (group.type === "crypto_address" && /^https:\/\/(etherscan\.io|tronscan\.org|mempool\.space)\//.test(group.explorer_url || "")) {
        const explorer = element("a", "button button-secondary button-compact", "在链上浏览器查看");
        explorer.href = group.explorer_url;
        explorer.target = "_blank";
        explorer.rel = "noopener";
        copy.append(explorer);
      }
      heading.append(
        icon,
        copy,
        element(
          "span",
          Number(group.confidence) >= 0.7 ? "badge badge-blue" : "badge badge-neutral",
          `${group.confidence_label || "低"}置信度`,
        ),
      );
      const sources = element("div", "business-compact-list");
      (Array.isArray(group.sources) ? group.sources : []).forEach((source) => {
        sources.append(renderSource(source));
      });
      if (group.sources_truncated) {
        sources.append(createEmptyCopy("来源较多，仅显示最近记录。"));
      }
      card.append(heading, sources);
      return card;
    };

    const load = async () => {
      const currentRequest = ++requestID;
      showNotice(error);
      showNotice(warning);
      try {
        const query = new URLSearchParams({
          days: String(days?.value || "30"),
          limit: "50",
        });
        if (type?.value) {
          query.set("types", type.value);
        }
        const payload = await api(`/api/v1/correlations?${query}`);
        if (currentRequest !== requestID) {
          return;
        }
        const groups = Array.isArray(payload?.groups) ? payload.groups : [];
        list.replaceChildren(...groups.map(renderGroup));
        list.toggleAttribute("hidden", groups.length === 0);
        empty?.toggleAttribute("hidden", groups.length > 0);
        setText("[data-correlation-total]", Number(payload?.total_groups) || 0);
        setText(
          "[data-correlation-scanned]",
          `分析 ${Number(payload?.message_count) || 0} 条消息 · ${formatDate(payload?.analyzed_at)}`,
        );
        setText(
          "[data-correlation-note]",
          payload?.governing_note || "关联结果仅用于发现线索，需结合原始消息人工核验。",
        );
        if (payload?.truncated) {
          showNotice(
            warning,
            `消息超过单次分析上限 ${Number(payload.message_limit) || 0} 条，结果仅覆盖最近消息。`,
            true,
          );
        }
      } catch (requestError) {
        if (currentRequest === requestID) {
          showNotice(error, requestError.message, true);
        }
      }
    };

    document.querySelector("[data-correlation-refresh]")
      ?.addEventListener("click", load);
    days?.addEventListener("change", load);
    type?.addEventListener("change", load);
    loadAccounts()
      .then((items) => {
        accounts = items;
        load();
      })
      .catch(() => load());
  };

  const initReports = () => {
    const chart = document.querySelector("[data-report-chart]");
    if (!chart) {
      return;
    }
    const days = document.querySelector("[data-report-days]");
    const tableBody = document.querySelector("[data-report-table-body]");
    const tableWrap = document.querySelector("[data-report-table-wrap]");
    const empty = document.querySelector("[data-report-empty]");
    const error = document.querySelector("[data-report-error]");
    const manifestError = document.querySelector("[data-evidence-manifest-error]");
    const manifestState = document.querySelector("[data-evidence-manifest-state]");
    const manifestIssues = document.querySelector("[data-evidence-manifest-issues]");
    const manifestIssuesWrap = document.querySelector("[data-evidence-manifest-issues-wrap]");
    const manifestIssuesEmpty = document.querySelector("[data-evidence-manifest-empty]");
    let accounts = [];

    const load = async () => {
      showNotice(error);
      try {
        const report = await api(`/api/v1/reports?days=${encodeURIComponent(days?.value || "7")}`);
        const byDay = Array.isArray(report?.by_day) ? report.by_day : [];
        const byAccount = Array.isArray(report?.by_account) ? report.by_account : [];
        setText("[data-report-total]", Number(report?.total) || 0);
        setText("[data-report-generated]", `生成于 ${formatDate(report?.generated_at)}`);
        renderChart(chart, byDay, "所选时间范围内还没有索引消息。");
        const accountMap = accountMapFrom(accounts);
        const accountList = document.querySelector("[data-report-accounts]");
        if (accountList) {
          accountList.replaceChildren(...(byAccount.length
            ? byAccount.map((item) => {
              const account = accountMap.get(Number(item.account_id));
              const row = element("div", "business-compact-row");
              const icon = element("span", "business-row-icon");
              icon.append(element("span", "icon icon-user-round"));
              const copy = element("span", "business-row-copy");
              copy.append(
                element("strong", "", account ? accountLabel(account) : `账号 ${item.account_id}`),
                element("small", "", account ? formatPhone(account.phone_number) : "本地账号已移除"),
              );
              row.append(icon, copy, element("strong", "business-row-value", String(item.count)));
              return row;
            })
            : [createEmptyCopy("暂无账号消息统计。")]));
        }
        tableBody?.replaceChildren(...byDay.map((item) => {
          const row = document.createElement("tr");
          appendCell(row, formatDay(item.date));
          appendCell(row, item.count);
          return row;
        }));
        tableWrap?.toggleAttribute("hidden", byDay.length === 0);
        empty?.toggleAttribute("hidden", byDay.length > 0);
      } catch (requestError) {
        showNotice(error, requestError.message, true);
      }
    };

    const generateManifest = async (event) => {
      const button = event?.currentTarget || document.querySelector("[data-evidence-manifest-generate]");
      if (button) {
        button.disabled = true;
      }
      showNotice(manifestError);
      try {
        const daysValue = encodeURIComponent(days?.value || "7");
        const manifest = await api(`/api/v1/evidence/manifest?days=${daysValue}`);
        const state = String(manifest?.data_state || "empty");
        const stateLabels = {
          complete: "完整",
          incomplete: "数据不完整",
          empty: "无证据",
        };
        if (manifestState) {
          manifestState.className = state === "complete"
            ? "badge badge-blue"
            : state === "incomplete"
              ? "badge badge-red"
              : "badge badge-neutral";
          manifestState.textContent = stateLabels[state] || "未知";
        }
        setText("[data-evidence-manifest-actor]", manifest?.generated_by || "—");
        setText("[data-evidence-manifest-generated]", formatDate(manifest?.generated_at));
        setText("[data-evidence-manifest-events]", Number(manifest?.event_count) || 0);
        setText("[data-evidence-manifest-messages]", Number(manifest?.message_count) || 0);
        setText(
          "[data-evidence-manifest-batches]",
          `${Number(manifest?.batch_count) || 0} 批`,
        );
        setText(
          "[data-evidence-manifest-completeness]",
          `完成 ${Number(manifest?.completed_batches) || 0} · 部分 ${Number(manifest?.partial_batches) || 0} · 失败 ${Number(manifest?.failed_batches) || 0}`,
        );
        setText(
          "[data-evidence-manifest-sequence]",
          manifest?.first_sequence || manifest?.last_sequence
            ? `${manifest.first_sequence} — ${manifest.last_sequence}`
            : "—",
        );
        setText(
          "[data-evidence-manifest-observed]",
          manifest?.first_observed_at
            ? `${formatDate(manifest.first_observed_at)} — ${formatDate(manifest.last_observed_at)}`
            : "—",
        );
        setText("[data-evidence-manifest-sha]", manifest?.evidence_sha256 || "—");
        const issues = Array.isArray(manifest?.incomplete_batches)
          ? manifest.incomplete_batches
          : [];
        const accountMap = accountMapFrom(accounts);
        manifestIssues?.replaceChildren(...issues.map((item) => {
          const row = document.createElement("tr");
          const account = accountMap.get(Number(item.account_id));
          appendCell(row, item.batch_id);
          appendCell(row, account ? accountLabel(account) : `账号 ${item.account_id}`);
          appendCell(row, `${peerLabels[item.peer_type] || item.peer_type} · ${item.peer_id}`);
          appendCell(
            row,
            item.status === "failed"
              ? element("span", "badge badge-red", "失败")
              : element("span", "badge badge-red", "部分完成"),
          );
          return row;
        }));
        manifestIssuesWrap?.toggleAttribute("hidden", issues.length === 0);
        manifestIssuesEmpty?.toggleAttribute("hidden", issues.length > 0);
      } catch (requestError) {
        showNotice(manifestError, requestError.message, true);
      } finally {
        if (button) {
          button.disabled = false;
        }
      }
    };
    days?.addEventListener("change", load);
    document.querySelector("[data-report-refresh]")?.addEventListener("click", load);
    document.querySelector("[data-evidence-manifest-generate]")
      ?.addEventListener("click", generateManifest);
    loadAccounts()
      .then((items) => {
        accounts = items;
        load();
      })
      .catch((requestError) => showNotice(error, requestError.message, true));
  };

  initDashboard();
  initGroups();
  initMonitor();
  initMonitorTasks();
  initAlerts();
  initMessages();
  initCorrelations();
  initReports();
})();
