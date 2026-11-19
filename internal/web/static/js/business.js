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
  const peerLabels = {
    user: "私聊",
    chat: "群组",
    channel: "频道",
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
    row.href = account.status === "online"
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
      element("span", `badge ${account.status === "online" ? "badge-blue" : "badge-neutral"}`,
        statusLabels[account.status] || account.status || "未知"),
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
                element("small", "", `${formatPhone(account.phone_number)} · ${statusLabels[account.status] || account.status}`),
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
    setText("[data-monitor-targets]", collection.target_count ?? "0");
    setText("[data-monitor-backlog]", collection.backlog_target_count ?? "0");
    setText("[data-monitor-delay]", formatDuration(collection.max_checkpoint_delay_seconds));
    setText("[data-monitor-failures]", collection.consecutive_failures ?? "0");
    setText("[data-monitor-flood]", collection.flood_wait_target_count ?? "0");
    setText("[data-monitor-success]", formatDate(collection.last_successful_at));
    setText("[data-monitor-error]", status?.last_error || "—");
    if (status?.last_error) {
      showNotice(notice, status.last_error, true);
    } else if (Number(collection.backlog_target_count) > 0) {
      showNotice(notice, "部分监控目标仍有历史消息积压，checkpoint 暂未推进。", true);
    } else if (Number(collection.flood_wait_target_count) > 0) {
      showNotice(notice, "部分目标受到 Telegram FloodWait 限制，将按周期重试。", true);
    }
  };

  const initMonitor = () => {
    const body = document.querySelector("[data-monitor-rules-body]");
    if (!body) {
      return;
    }
    const wrap = document.querySelector("[data-monitor-rules-wrap]");
    const empty = document.querySelector("[data-monitor-rules-empty]");
    const alertList = document.querySelector("[data-monitor-alerts]");
    const notice = document.querySelector("[data-monitor-notice]");
    let accounts = [];

    const renderRules = (rules) => {
      const accountMap = accountMapFrom(accounts);
      body.replaceChildren();
      rules.forEach((rule) => {
        const account = accountMap.get(Number(rule.account_id));
        const row = document.createElement("tr");
        appendCell(row, rule.name);
        appendCell(row, account ? accountLabel(account) : `账号 ${rule.account_id}`);
        appendCell(row, `${rule.peer_title || peerLabels[rule.peer_type] || "会话"} · ${peerLabels[rule.peer_type] || rule.peer_type}`);
        appendCell(row, Array.isArray(rule.keywords) && rule.keywords.length ? rule.keywords.join("、") : "全部消息");
        appendCell(row, formatDate(rule.last_run_at));
        appendCell(row, rule.last_error
          ? element("span", "badge badge-red", "运行异常")
          : element("span", "badge badge-blue", "运行中"));
        body.append(row);
      });
      wrap?.toggleAttribute("hidden", rules.length === 0);
      empty?.toggleAttribute("hidden", rules.length > 0);
    };

    const load = async () => {
      showNotice(notice);
      try {
        const [rulesResult, alertsResult, statusResult, accountsResult] = await Promise.allSettled([
          api("/api/v1/monitor/rules?enabled=true"),
          api("/api/v1/monitor/alerts?state=pending&limit=5"),
          api("/api/v1/monitor/status"),
          loadAccounts(),
        ]);
        accounts = accountsResult.status === "fulfilled" ? accountsResult.value : [];
        const rules = rulesResult.status === "fulfilled" &&
          Array.isArray(rulesResult.value?.items) ? rulesResult.value.items : [];
        const alerts = alertsResult.status === "fulfilled" &&
          Array.isArray(alertsResult.value?.items) ? alertsResult.value.items : [];
        renderRules(rules);
        setText("[data-monitor-rule-count]", rules.length);
        setText("[data-monitor-pending-count]", alerts.length);
        if (statusResult.status === "fulfilled") {
          createMonitorStatus(statusResult.value, notice);
        } else {
          createMonitorStatus(null, notice);
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
      } catch (requestError) {
        showNotice(notice, requestError.message, true);
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
  };

  const initMonitorTasks = () => {
    const body = document.querySelector("[data-tasks-body]");
    if (!body) {
      return;
    }
    const wrap = document.querySelector("[data-tasks-wrap]");
    const empty = document.querySelector("[data-tasks-empty]");
    const error = document.querySelector("[data-tasks-error]");
    const dialog = document.getElementById("monitor-rule-dialog");
    const form = document.querySelector("[data-monitor-rule-form]");
    const formNotice = document.querySelector("[data-monitor-rule-notice]");
    const accountSelect = document.querySelector("[data-monitor-account]");
    const chatSelect = document.querySelector("[data-monitor-chat]");
    const channelSelect = document.querySelector("[data-monitor-channel]");
    let accounts = [];
    let channels = [];
    let chatRequest = 0;

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
        appendCell(row, rule.name);
        const peer = element("span", "business-cell-stack");
        peer.append(
          element("strong", "", rule.peer_title || peerLabels[rule.peer_type] || "会话"),
          element("small", "", `${account ? accountLabel(account) : `账号 ${rule.account_id}`} · ${peerLabels[rule.peer_type] || rule.peer_type}`),
        );
        appendCell(row, peer);
        appendCell(row, Array.isArray(rule.keywords) && rule.keywords.length ? rule.keywords.join("、") : "全部消息");
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
        appendCell(row, remove);
        body.append(row);
      });
      wrap?.toggleAttribute("hidden", rules.length === 0);
      empty?.toggleAttribute("hidden", rules.length > 0);
      setText("[data-task-count]", rules.length);
      setText("[data-task-enabled-count]", rules.filter((rule) => rule.enabled).length);
    };

    const load = async () => {
      showNotice(error);
      try {
        const [rulesPayload, channelPayload, accountItems] = await Promise.all([
          api("/api/v1/monitor/rules"),
          api("/api/v1/monitor/channels"),
          loadAccounts(),
        ]);
        accounts = accountItems;
        channels = Array.isArray(channelPayload?.items) ? channelPayload.items : [];
        renderRules(Array.isArray(rulesPayload?.items) ? rulesPayload.items : []);
      } catch (requestError) {
        showNotice(error, requestError.message, true);
      }
    };

    document.querySelectorAll("[data-task-create]").forEach((button) => {
      button.addEventListener("click", () => {
        form?.reset();
        showNotice(formNotice);
        const online = accounts.filter((account) => account.status === "online");
        fillSelect(accountSelect, online.map((account) => ({
          value: String(account.id),
          label: `${accountLabel(account)} · ${formatPhone(account.phone_number)}`,
        })), online.length ? "请选择在线账号" : "没有在线账号");
        fillSelect(chatSelect, [], "请选择在线账号");
        fillSelect(channelSelect, channels
          .filter((channel) => channel.enabled)
          .map((channel) => ({
            value: String(channel.id),
            label: channel.name,
          })), "请选择通知渠道");
        dialog?.showModal();
      });
    });
    document.querySelectorAll("[data-monitor-rule-close]").forEach((button) => {
      button.addEventListener("click", () => dialog?.close());
    });
    accountSelect?.addEventListener("change", loadChats);
    form?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const submit = form.querySelector("button[type='submit']");
      const data = new FormData(form);
      const [peerType, peerID, ...titleParts] = String(data.get("chat") || "").split(":");
      if (!peerType || !Number(peerID)) {
        showNotice(formNotice, "请选择监听取话。", true);
        return;
      }
      const keywords = String(data.get("keywords") || "")
        .split(/[\n,，]+/)
        .map((item) => item.trim())
        .filter(Boolean);
      submit?.setAttribute("disabled", "");
      showNotice(formNotice);
      try {
        await api("/api/v1/monitor/rules", {
          method: "POST",
          body: JSON.stringify({
            name: String(data.get("name") || "").trim(),
            account_id: Number(data.get("account_id")),
            peer_type: peerType,
            peer_id: Number(peerID),
            peer_title: titleParts.join(":"),
            keywords,
            notification_channel_id: Number(data.get("notification_channel_id")),
          }),
        });
        dialog?.close();
        showToast("监控规则已创建");
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
          heading.append(
            element("strong", "", alert.rule_name || "监控规则"),
            element("span", `badge ${alert.state === "pending" ? "badge-red" : "badge-neutral"}`,
              alert.state === "pending" ? "待处理" : "已解决"),
          );
          copy.append(
            heading,
            element("p", "", alert.message_preview || "监控命中"),
            element("small", "", `${alert.peer_title || "会话"} · ${alert.notification_channel_name || "站内通知"} · ${formatDate(alert.created_at)}${alert.matched_keyword ? ` · 命中 ${alert.matched_keyword}` : ""}`),
          );
          card.append(icon, copy);
          if (alert.state === "pending") {
            const resolve = element("button", "button button-secondary button-compact", "标记已解决");
            resolve.type = "button";
            resolve.addEventListener("click", async () => {
              resolve.disabled = true;
              try {
                await api(`/api/v1/monitor/alerts/${encodeURIComponent(alert.id)}/resolve`, {
                  method: "POST",
                  body: "{}",
                });
                showToast("告警已解决");
                await load();
              } catch (requestError) {
                showNotice(error, requestError.message, true);
              } finally {
                resolve.disabled = false;
              }
            });
            card.append(resolve);
          }
          return card;
        }));
        list.toggleAttribute("hidden", items.length === 0);
        empty?.toggleAttribute("hidden", items.length > 0);
        setText("[data-alert-total]", items.length);
      } catch (requestError) {
        showNotice(error, requestError.message, true);
      }
    };
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
    let offset = 0;
    let total = 0;
    let accounts = [];
    const limit = 50;

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
    days?.addEventListener("change", load);
    document.querySelector("[data-report-refresh]")?.addEventListener("click", load);
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
  initReports();
})();
