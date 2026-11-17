(() => {
  const shell = document.querySelector(".app-shell");
  const sidebar = document.getElementById("sidebar");
  const sidebarToggle = document.getElementById("sidebar-toggle");
  const backdrop = document.getElementById("mobile-backdrop");
  const userTrigger = document.getElementById("user-trigger");
  const userDropdown = document.getElementById("user-dropdown");
  const menuToggles = [...document.querySelectorAll("[data-submenu-toggle]")];

  if (!shell) {
    return;
  }

  const isMobile = () => window.matchMedia("(max-width: 767px)").matches;

  const setSubmenuOpen = (group, open) => {
    const toggle = group.querySelector("[data-submenu-toggle]");
    const submenu = group.querySelector(".submenu");
    group.classList.toggle("is-open", open);
    toggle?.setAttribute("aria-expanded", String(open));
    submenu?.classList.toggle("is-open", open);
  };

  menuToggles.forEach((toggle) => {
    const group = toggle.closest("[data-menu-group]");
    if (!group) {
      return;
    }
    setSubmenuOpen(group, toggle.getAttribute("aria-expanded") === "true");
    toggle.addEventListener("click", () => {
      const shouldOpen = toggle.getAttribute("aria-expanded") !== "true";
      if (shell.dataset.sidebar === "collapsed" && !isMobile()) {
        setCollapsed(false);
      }
      menuToggles.forEach((otherToggle) => {
        const otherGroup = otherToggle.closest("[data-menu-group]");
        if (otherGroup && otherGroup !== group) {
          setSubmenuOpen(otherGroup, false);
        }
      });
      setSubmenuOpen(group, shouldOpen);
    });
  });

  const closeMobileSidebar = () => {
    sidebar?.classList.remove("is-open");
    if (backdrop) {
      backdrop.hidden = true;
    }
  };

  const setCollapsed = (collapsed) => {
    if (isMobile()) {
      sidebar?.classList.toggle("is-open", collapsed);
      if (backdrop) {
        backdrop.hidden = !collapsed;
      }
    } else {
      shell.dataset.sidebar = collapsed ? "collapsed" : "expanded";
    }
    if (sidebarToggle) {
      sidebarToggle.setAttribute("aria-expanded", String(!collapsed));
    }
  };

  sidebarToggle?.addEventListener("click", () => {
    if (isMobile()) {
      setCollapsed(!sidebar?.classList.contains("is-open"));
      return;
    }
    setCollapsed(shell.dataset.sidebar !== "collapsed");
  });

  backdrop?.addEventListener("click", closeMobileSidebar);

  sidebar?.addEventListener("click", (event) => {
    if (isMobile() && event.target.closest("a")) {
      closeMobileSidebar();
    }
  });

  userTrigger?.addEventListener("click", (event) => {
    event.stopPropagation();
    if (!userDropdown) {
      return;
    }
    const shouldOpen = userDropdown.hidden;
    userDropdown.hidden = !shouldOpen;
    userTrigger.setAttribute("aria-expanded", String(shouldOpen));
  });

  document.addEventListener("click", (event) => {
    if (userDropdown && !event.target.closest(".user-menu")) {
      userDropdown.hidden = true;
      userTrigger?.setAttribute("aria-expanded", "false");
    }
  });

  const panels = [...document.querySelectorAll(".overlay-panel")];
  const closePanels = () => {
    panels.forEach((panel) => {
      panel.hidden = true;
    });
  };

  const openPanel = (selector) => {
    closePanels();
    const panel = document.querySelector(selector);
    if (panel) {
      panel.hidden = false;
    }
  };

  document.querySelector("[data-open-search]")?.addEventListener("click", () => {
    openPanel("#search-panel");
    document.querySelector("[data-page-search]")?.focus();
  });
  document.querySelector("[data-open-notifications]")?.addEventListener("click", () => openPanel("#notification-panel"));
  document.querySelector("[data-open-settings]")?.addEventListener("click", () => openPanel("#settings-panel"));

  document.querySelectorAll("[data-close-panel]").forEach((button) => {
    button.addEventListener("click", closePanels);
  });

  panels.forEach((panel) => {
    panel.addEventListener("click", (event) => {
      if (event.target === panel) {
        closePanels();
      }
    });
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      closePanels();
      closeMobileSidebar();
    }
  });

  const searchInput = document.querySelector("[data-page-search]");
  const searchResults = document.querySelectorAll("[data-search-results] li");
  searchInput?.addEventListener("input", () => {
    const query = searchInput.value.trim().toLowerCase();
    searchResults.forEach((item) => {
      item.hidden = Boolean(query) && !item.textContent.toLowerCase().includes(query);
    });
  });

  document.querySelectorAll("[data-theme]").forEach((button) => {
    button.addEventListener("click", () => {
      shell.dataset.theme = button.dataset.theme;
      document.querySelectorAll("[data-theme]").forEach((item) => {
        const selected = item === button;
        item.classList.toggle("active", selected);
        item.setAttribute("aria-checked", String(selected));
      });
    });
  });

  document.querySelector("[data-toggle-breadcrumb]")?.addEventListener("change", (event) => {
    shell.dataset.breadcrumbs = event.target.checked ? "visible" : "hidden";
  });

  document.querySelector("[data-toggle-tabs]")?.addEventListener("change", (event) => {
    shell.dataset.tabs = event.target.checked ? "visible" : "hidden";
  });

  document.querySelector("[data-toggle-collapse]")?.addEventListener("change", (event) => {
    if (!isMobile()) {
      setCollapsed(event.target.checked);
    }
  });

  document.querySelectorAll("[data-menu-tip]").forEach((target) => {
    target.addEventListener("mouseenter", () => {
      if (shell.dataset.sidebar !== "collapsed" || isMobile()) {
        return;
      }
      const rect = target.getBoundingClientRect();
      target.style.setProperty("--menu-tip-offset", `${Math.round(rect.top + rect.height / 2)}px`);
    });
  });

  const clock = document.querySelector("[data-current-time]");
  if (clock) {
    const tick = () => {
      clock.textContent = new Intl.DateTimeFormat("zh-CN", {
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
        hour12: false,
      }).format(new Date());
    };
    tick();
    window.setInterval(tick, 1000);
  }

  const accountBody = document.querySelector("[data-accounts-body]");
  const accountsTable = document.querySelector("[data-accounts-table]");
  const accountsEmpty = document.querySelector("[data-accounts-empty]");
  const accountsLoading = document.querySelector("[data-accounts-loading]");
  const accountsError = document.querySelector("[data-accounts-error]");
  const accountCount = document.querySelector("[data-account-count]");
  const onlineCount = document.querySelector("[data-online-count]");

  const showNotice = (element, message, isError = false) => {
    if (!element) {
      return;
    }
    element.textContent = message;
    element.hidden = !message;
    element.classList.toggle("form-notice-error", isError);
  };

  const requestJSON = async (path, options = {}) => {
    const headers = new Headers(options.headers || {});
    headers.set("Accept", "application/json");
    if (options.body !== undefined) {
      headers.set("Content-Type", "application/json");
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
      const error = new Error(payload?.error?.message || `请求失败（${response.status}）`);
      error.code = payload?.error?.code || "request_failed";
      error.status = response.status;
      throw error;
    }
    return payload;
  };

  const maskPhone = (phone) => {
    const digits = String(phone || "").replace(/\D/g, "");
    if (digits.length <= 4) {
      return "••••";
    }
    return `••••••${digits.slice(-4)}`;
  };

  const initials = (account) => {
    const name = account.display_name || account.username || `TG${account.id}`;
    return String(name).trim().slice(0, 2).toUpperCase();
  };

  const formatAccountDate = (value) => {
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

  const statusLabels = {
    online: "在线",
    pending: "待验证",
    paused: "已暂停",
    error: "异常",
  };

  const tableCell = (content) => {
    const cell = document.createElement("td");
    if (content instanceof Node) {
      cell.append(content);
    } else {
      cell.textContent = content;
    }
    return cell;
  };

  const renderAccounts = (items) => {
    if (!accountBody) {
      return;
    }
    accountBody.replaceChildren();
    const online = items.filter((item) => item.status === "online").length;
    if (accountCount) {
      accountCount.textContent = String(items.length);
    }
    if (onlineCount) {
      onlineCount.textContent = String(online);
    }

    const hasItems = items.length > 0;
    accountsTable?.toggleAttribute("hidden", !hasItems);
    accountsEmpty?.toggleAttribute("hidden", hasItems);
    accountsLoading?.setAttribute("hidden", "");
    if (!hasItems) {
      return;
    }

    items.forEach((account) => {
      const row = document.createElement("tr");
      row.className = "account-row";
      row.dataset.href = `/accounts/${encodeURIComponent(account.id)}`;

      const identity = document.createElement("div");
      identity.className = "account-identity";
      const monogram = document.createElement("span");
      monogram.className = "account-monogram";
      monogram.textContent = initials(account);
      const identityCopy = document.createElement("span");
      identityCopy.className = "account-identity-copy";
      const name = document.createElement("strong");
      name.textContent = account.display_name || "未设置姓名";
      const telegramID = document.createElement("small");
      telegramID.textContent = `Telegram ID ${account.id}`;
      identityCopy.append(name, telegramID);
      identity.append(monogram, identityCopy);
      const identityLink = document.createElement("a");
      identityLink.className = "account-detail-link";
      identityLink.href = row.dataset.href;
      identityLink.setAttribute("aria-label", `查看 ${name.textContent} 的聊天`);
      identityLink.append(identity);

      const phone = document.createElement("span");
      phone.className = "table-secondary";
      phone.textContent = maskPhone(account.phone_number);

      const username = document.createElement("span");
      username.className = "table-secondary";
      username.textContent = account.username ? `@${account.username}` : "未设置";

      const status = document.createElement("span");
      status.className = `account-status account-status-${account.status}`;
      status.textContent = statusLabels[account.status] || account.status;

      row.append(
        tableCell(identityLink),
        tableCell(phone),
        tableCell(username),
        tableCell(status),
        tableCell(formatAccountDate(account.updated_at)),
      );
      row.addEventListener("click", (event) => {
        if (!event.target.closest("a")) {
          window.location.assign(row.dataset.href);
        }
      });
      accountBody.append(row);
    });
  };

  const loadAccounts = async () => {
    if (!accountBody) {
      return;
    }
    accountsLoading?.removeAttribute("hidden");
    accountsTable?.setAttribute("hidden", "");
    accountsEmpty?.setAttribute("hidden", "");
    showNotice(accountsError, "");
    try {
      const payload = await requestJSON("/api/v1/accounts?limit=100");
      renderAccounts(Array.isArray(payload?.items) ? payload.items : []);
    } catch (error) {
      accountsLoading?.setAttribute("hidden", "");
      showNotice(accountsError, error.message, true);
    }
  };

  const telegramConfig = document.querySelector("[data-telegram-config]");
  let telegramConfigured = false;
  const loadTelegramConfig = async () => {
    try {
      const payload = await requestJSON("/api/v1/telegram/auth/config");
      telegramConfigured = Boolean(payload?.configured);
      if (telegramConfig) {
        telegramConfig.textContent = telegramConfigured
          ? "登录服务已配置"
          : "待配置 API 凭据";
      }
    } catch {
      telegramConfigured = false;
      if (telegramConfig) {
        telegramConfig.textContent = "登录配置检查失败";
      }
    }
  };

  document.querySelector("[data-accounts-refresh]")?.addEventListener("click", loadAccounts);
  if (accountBody) {
    loadAccounts();
    loadTelegramConfig();
  }

  const authDialog = document.getElementById("telegram-login-dialog");
  const authSteps = [...document.querySelectorAll("[data-auth-step]")];
  const progressSteps = [...document.querySelectorAll("[data-progress-step]")];
  const phoneForm = document.querySelector("[data-phone-form]");
  const codeForm = document.querySelector("[data-code-form]");
  const passwordForm = document.querySelector("[data-password-form]");
  const phoneNotice = document.querySelector("[data-phone-notice]");
  const codeNotice = document.querySelector("[data-code-notice]");
  const passwordNotice = document.querySelector("[data-password-notice]");
  const phoneHint = document.querySelector("[data-auth-phone-hint]");
  const codeType = document.querySelector("[data-auth-code-type]");
  const codeInput = codeForm?.querySelector("input[name='code']");
  const fetchCodeButton = document.querySelector("[data-fetch-code]");
  const completeName = document.querySelector("[data-complete-name]");
  let loginID = "";
  let pollTimer = 0;
  let pollAttempts = 0;
  let authCompleted = false;

  const stopCodePolling = () => {
    if (pollTimer) {
      window.clearTimeout(pollTimer);
      pollTimer = 0;
    }
  };

  const setAuthStep = (name) => {
    const order = ["phone", "code", "password", "complete"];
    const index = order.indexOf(name);
    authSteps.forEach((step) => {
      step.hidden = step.dataset.authStep !== name;
    });
    progressSteps.forEach((step) => {
      const stepIndex = order.indexOf(step.dataset.progressStep);
      step.classList.toggle("active", stepIndex <= index && stepIndex >= 0);
    });
  };

  const cancelLogin = () => {
    stopCodePolling();
    const current = loginID;
    loginID = "";
    if (!current || authCompleted) {
      return;
    }
    fetch(`/api/v1/telegram/auth/${encodeURIComponent(current)}`, {method: "DELETE"}).catch(() => {});
  };

  const resetLoginDialog = () => {
    cancelLogin();
    authCompleted = false;
    pollAttempts = 0;
    phoneForm?.reset();
    codeForm?.reset();
    passwordForm?.reset();
    showNotice(phoneNotice, "");
    showNotice(codeNotice, "");
    showNotice(passwordNotice, "");
    setAuthStep("phone");
    const submit = phoneForm?.querySelector("button[type='submit']");
    if (submit) {
      submit.disabled = !telegramConfigured;
    }
    if (!telegramConfigured) {
      showNotice(phoneNotice, "请先在 .env 中配置 TELEGRAM_API_ID 和 TELEGRAM_API_HASH，然后重启服务。", true);
    }
  };

  const openLoginDialog = () => {
    if (!authDialog) {
      return;
    }
    resetLoginDialog();
    if (typeof authDialog.showModal === "function") {
      authDialog.showModal();
    } else {
      authDialog.setAttribute("open", "");
    }
  };

  document.querySelectorAll("[data-open-login]").forEach((button) => {
    button.addEventListener("click", openLoginDialog);
  });
  document.querySelectorAll("[data-login-close]").forEach((button) => {
    button.addEventListener("click", () => {
      cancelLogin();
      authDialog?.close();
    });
  });
  document.querySelector("[data-login-finish]")?.addEventListener("click", () => {
    authDialog?.close();
    resetLoginDialog();
  });
  document.querySelector("[data-login-back]")?.addEventListener("click", () => {
    cancelLogin();
    showNotice(codeNotice, "");
    setAuthStep("phone");
  });

  phoneForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = phoneForm.querySelector("button[type='submit']");
    const formData = new FormData(phoneForm);
    showNotice(phoneNotice, "");
    if (button) {
      button.disabled = true;
    }
    try {
      const challenge = await requestJSON("/api/v1/telegram/auth/send-code", {
        method: "POST",
        body: JSON.stringify({
          phone_number: formData.get("phone_number"),
          code_url: formData.get("code_url"),
        }),
      });
      loginID = challenge.login_id;
      pollAttempts = 0;
      if (phoneHint) {
        phoneHint.textContent = challenge.phone_hint || "Telegram 账号";
      }
      if (codeType) {
        codeType.textContent = challenge.code_type || "Telegram 验证码";
      }
      if (codeInput) {
        codeInput.maxLength = challenge.code_length > 0 ? challenge.code_length : 8;
        codeInput.value = "";
      }
      fetchCodeButton?.toggleAttribute("hidden", !challenge.can_fetch_code);
      setAuthStep("code");
      codeInput?.focus();
      if (challenge.can_fetch_code) {
        pollAttempts = 1;
        pollTimer = window.setTimeout(fetchCodeFromSource, 1800);
      }
    } catch (error) {
      showNotice(phoneNotice, error.message, true);
    } finally {
      if (button) {
        button.disabled = false;
      }
    }
  });

  codeInput?.addEventListener("input", () => {
    const length = Number(codeInput.maxLength) > 0 ? Number(codeInput.maxLength) : 8;
    codeInput.value = codeInput.value.replace(/\D/g, "").slice(0, length);
  });

  const finishAuth = (result) => {
    if (result.next_step === "password") {
      stopCodePolling();
      setAuthStep("password");
      passwordForm?.querySelector("input[name='password']")?.focus();
      return;
    }
    if (result.next_step === "complete") {
      stopCodePolling();
      authCompleted = true;
      loginID = "";
      if (completeName) {
        completeName.textContent = result.account?.display_name || result.account?.username || "Telegram 账号";
      }
      setAuthStep("complete");
      loadAccounts();
    }
  };

  codeForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = codeForm.querySelector("button[type='submit']");
    const formData = new FormData(codeForm);
    stopCodePolling();
    showNotice(codeNotice, "");
    if (button) {
      button.disabled = true;
    }
    try {
      const result = await requestJSON(
        `/api/v1/telegram/auth/${encodeURIComponent(loginID)}/code`,
        {
          method: "POST",
          body: JSON.stringify({code: formData.get("code")}),
        },
      );
      finishAuth(result);
    } catch (error) {
      showNotice(codeNotice, error.message, true);
    } finally {
      if (button) {
        button.disabled = false;
      }
    }
  });

  passwordForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = passwordForm.querySelector("button[type='submit']");
    const formData = new FormData(passwordForm);
    showNotice(passwordNotice, "");
    if (button) {
      button.disabled = true;
    }
    try {
      const result = await requestJSON(
        `/api/v1/telegram/auth/${encodeURIComponent(loginID)}/password`,
        {
          method: "POST",
          body: JSON.stringify({password: formData.get("password")}),
        },
      );
      passwordForm.reset();
      finishAuth(result);
    } catch (error) {
      showNotice(passwordNotice, error.message, true);
    } finally {
      if (button) {
        button.disabled = false;
      }
    }
  });

  async function fetchCodeFromSource() {
    if (!loginID || pollTimer === 0 && pollAttempts === 0) {
      return;
    }
    stopCodePolling();
    if (fetchCodeButton) {
      fetchCodeButton.disabled = true;
    }
    showNotice(codeNotice, "正在从接码链接读取验证码...");
    try {
      const result = await requestJSON(
        `/api/v1/telegram/auth/${encodeURIComponent(loginID)}/fetch-code`,
        {method: "POST", body: "{}"},
      );
      showNotice(codeNotice, "");
      finishAuth(result);
    } catch (error) {
      if (error.code === "code_not_ready" && pollAttempts < 30) {
        pollAttempts += 1;
        showNotice(codeNotice, "验证码尚未到达，正在等待...");
        pollTimer = window.setTimeout(fetchCodeFromSource, 2000);
      } else {
        showNotice(codeNotice, error.message, true);
      }
    } finally {
      if (fetchCodeButton) {
        fetchCodeButton.disabled = false;
      }
    }
  }

  fetchCodeButton?.addEventListener("click", () => {
    pollAttempts = 1;
    fetchCodeFromSource();
  });

  authDialog?.addEventListener("close", () => {
    if (!authCompleted) {
      cancelLogin();
    }
  });

  const loginForm = document.querySelector("[data-login-form]");
  const loginNotice = document.querySelector("[data-login-notice]");
  loginForm?.addEventListener("submit", (event) => {
    event.preventDefault();
    if (loginNotice) {
      loginNotice.textContent = "登录服务将在下一阶段接入。当前可先直接查看管理台。";
      loginNotice.hidden = false;
    }
  });

  const detailRoot = document.querySelector("[data-account-detail]");
  if (detailRoot) {
    const accountID = Number(detailRoot.dataset.accountId);
    const detailName = detailRoot.querySelector("[data-detail-name]");
    const detailMeta = detailRoot.querySelector("[data-detail-meta]");
    const chatList = detailRoot.parentElement.querySelector("[data-chat-list]");
    const chatCount = detailRoot.parentElement.querySelector("[data-chat-count]");
    const chatsLoading = detailRoot.parentElement.querySelector("[data-chats-loading]");
    const chatsError = detailRoot.parentElement.querySelector("[data-chats-error]");
    const chatsRefresh = detailRoot.parentElement.querySelector("[data-chats-refresh]");
    const conversation = detailRoot.parentElement.querySelector("[data-conversation]");
    const conversationEmpty = detailRoot.parentElement.querySelector("[data-messages-empty]");
    const conversationTitle = detailRoot.parentElement.querySelector("[data-conversation-title]");
    const conversationMeta = detailRoot.parentElement.querySelector("[data-conversation-meta]");
    const conversationType = detailRoot.parentElement.querySelector("[data-conversation-type]");
    const messageList = detailRoot.parentElement.querySelector("[data-message-list]");
    const messagesLoading = detailRoot.parentElement.querySelector("[data-messages-loading]");
    const messagesError = detailRoot.parentElement.querySelector("[data-messages-error]");
    const loadOlder = detailRoot.parentElement.querySelector("[data-load-older]");
    let selectedChat = null;
    let nextBeforeID = 0;

    const formatMessageDate = (value) => {
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) {
        return "时间未知";
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

    const renderMessage = (message) => {
      const item = document.createElement("article");
      item.className = `message-item${message.outgoing ? " is-outgoing" : ""}`;
      const meta = document.createElement("div");
      meta.className = "message-meta";
      const sender = document.createElement("strong");
      sender.textContent = message.sender || (message.outgoing ? "我" : "未知用户");
      const time = document.createElement("time");
      time.dateTime = message.sent_at || "";
      time.textContent = formatMessageDate(message.sent_at);
      meta.append(sender, time);
      const body = document.createElement("p");
      body.textContent = message.text || message.media_type || "空消息";
      item.append(meta, body);
      if (message.media_type && message.text) {
        const media = document.createElement("span");
        media.className = "message-media";
        media.textContent = message.media_type;
        item.append(media);
      }
      return item;
    };

    const appendMessages = (items, replace) => {
      if (!messageList) {
        return;
      }
      if (replace) {
        messageList.replaceChildren();
      }
      items.forEach((message) => messageList.append(renderMessage(message)));
    };

    const loadMessages = async (chat, reset = true) => {
      if (!chat) {
        return;
      }
      if (reset) {
        selectedChat = chat;
        nextBeforeID = 0;
        appendMessages([], true);
        conversation?.removeAttribute("hidden");
        conversationEmpty?.setAttribute("hidden", "");
        if (conversationTitle) {
          conversationTitle.textContent = chat.title || "未命名会话";
        }
        if (conversationMeta) {
          conversationMeta.textContent = chat.username ? `@${chat.username}` : chat.id;
        }
        if (conversationType) {
          conversationType.textContent = {user: "私聊", chat: "群组", channel: "频道"}[chat.type] || "Telegram";
        }
      }
      messagesLoading?.removeAttribute("hidden");
      loadOlder?.setAttribute("disabled", "");
      showNotice(messagesError, "");
      try {
        const params = new URLSearchParams({limit: "50"});
        if (!reset && nextBeforeID > 0) {
          params.set("before_id", String(nextBeforeID));
        }
        const payload = await requestJSON(
          `/api/v1/accounts/${encodeURIComponent(accountID)}/chats/${encodeURIComponent(chat.type)}/${encodeURIComponent(chat.peer_id)}/messages?${params}`,
        );
        const items = Array.isArray(payload?.items) ? payload.items : [];
        appendMessages(items, reset);
        nextBeforeID = Number(payload?.next_before_id) || 0;
        loadOlder?.toggleAttribute("hidden", nextBeforeID <= 0);
        if (reset && items.length === 0) {
          appendMessages([], true);
          const empty = document.createElement("p");
          empty.className = "message-empty";
          empty.textContent = "这个聊天还没有可显示的消息。";
          messageList?.append(empty);
        }
      } catch (error) {
        showNotice(messagesError, error.message, true);
      } finally {
        messagesLoading?.setAttribute("hidden", "");
        if (nextBeforeID > 0) {
          loadOlder?.removeAttribute("disabled");
        }
      }
    };

    const renderChats = (items) => {
      if (!chatList) {
        return;
      }
      chatList.replaceChildren();
      items.forEach((chat) => {
        const button = document.createElement("button");
        button.type = "button";
        button.className = "chat-list-item";
        button.dataset.chatId = chat.id;
        const avatar = document.createElement("span");
        avatar.className = "chat-avatar";
        avatar.textContent = String(chat.title || "TG").trim().slice(0, 2).toUpperCase();
        const copy = document.createElement("span");
        copy.className = "chat-list-copy";
        const title = document.createElement("strong");
        title.textContent = chat.title || "未命名会话";
        const preview = document.createElement("span");
        preview.textContent = chat.preview || (chat.username ? `@${chat.username}` : "暂无消息");
        copy.append(title, preview);
        const side = document.createElement("span");
        side.className = "chat-list-side";
        if (chat.unread_count > 0) {
          const badge = document.createElement("b");
          badge.textContent = String(chat.unread_count);
          side.append(badge);
        }
        const time = document.createElement("small");
        time.textContent = chat.last_message_at ? formatMessageDate(chat.last_message_at) : "";
        side.append(time);
        button.append(avatar, copy, side);
        button.addEventListener("click", () => {
          chatList.querySelectorAll(".chat-list-item").forEach((item) => item.classList.remove("active"));
          button.classList.add("active");
          loadMessages(chat);
        });
        chatList.append(button);
      });
      chatList.toggleAttribute("hidden", items.length === 0);
      if (items.length === 0) {
        const empty = document.createElement("p");
        empty.className = "chat-list-empty";
        empty.textContent = "这个账号目前没有可读取的聊天。";
        chatList.append(empty);
        chatList.removeAttribute("hidden");
      }
    };

    const loadChats = async () => {
      chatsLoading?.removeAttribute("hidden");
      chatsError?.setAttribute("hidden", "");
      chatsRefresh?.setAttribute("disabled", "");
      try {
        const payload = await requestJSON(`/api/v1/accounts/${encodeURIComponent(accountID)}/chats?limit=100`);
        const items = Array.isArray(payload?.items) ? payload.items : [];
        renderChats(items);
        if (chatCount) {
          chatCount.textContent = `${items.length} / ${payload?.total ?? items.length} 个会话`;
        }
      } catch (error) {
        showNotice(chatsError, error.message, true);
      } finally {
        chatsLoading?.setAttribute("hidden", "");
        chatsRefresh?.removeAttribute("disabled");
      }
    };

    requestJSON(`/api/v1/accounts/${encodeURIComponent(accountID)}`)
      .then((account) => {
        if (detailName) {
          detailName.textContent = account.display_name || account.username || "Telegram 账号";
        }
        if (detailMeta) {
          detailMeta.textContent = `${account.username ? `@${account.username} · ` : ""}${maskPhone(account.phone_number)} · ${statusLabels[account.status] || account.status}`;
        }
      })
      .catch((error) => {
        if (detailMeta) {
          detailMeta.textContent = error.message;
        }
      });
    loadChats();
    chatsRefresh?.addEventListener("click", loadChats);
    loadOlder?.addEventListener("click", () => loadMessages(selectedChat, false));
  }

  const settingsForm = document.querySelector("[data-settings-form]");
  if (settingsForm) {
    const settingsNotice = document.querySelector("[data-settings-notice]");
    const settingsState = document.querySelector("[data-settings-state]");
    const apiHashState = document.querySelector("[data-api-hash-state]");
    const restartNote = document.querySelector("[data-restart-note]");
    const environmentOverrides = document.querySelector("[data-environment-overrides]");

    const renderSettings = (settings) => {
      const setValue = (name, value) => {
        const input = settingsForm.elements.namedItem(name);
        if (input) {
          input.value = value;
        }
      };
      setValue("telegram_api_id", settings.telegram_api_id || "");
      setValue("telegram_api_hash", "");
      setValue("telegram_code_fetch_hosts", Array.isArray(settings.telegram_code_fetch_hosts)
        ? settings.telegram_code_fetch_hosts.join(", ")
        : "");
      setValue("telegram_code_dns_server", settings.telegram_code_dns_server || "");
      setValue("http_addr", settings.http_addr || "");
      if (apiHashState) {
        apiHashState.textContent = settings.telegram_api_hash_configured
          ? (settings.telegram_api_hash_hint || "已配置")
          : "尚未配置";
      }
      const overrides = Array.isArray(settings.environment_overrides) ? settings.environment_overrides : [];
      if (environmentOverrides) {
        environmentOverrides.textContent = overrides.length
          ? `以下环境变量会覆盖页面设置：${overrides.join("、")}`
          : "未检测到环境变量覆盖。";
      }
      if (settingsState) {
        settingsState.textContent = settings.telegram_api_hash_configured ? "已配置" : "待配置";
        settingsState.className = `badge ${settings.telegram_api_hash_configured ? "badge-blue" : "badge-neutral"}`;
      }
      if (restartNote) {
        restartNote.textContent = settings.restart_required ? "设置已保存，重启服务后生效。" : "";
      }
    };

    const loadSettings = async () => {
      try {
        renderSettings(await requestJSON("/api/v1/settings"));
      } catch (error) {
        showNotice(settingsNotice, error.message, true);
        if (settingsState) {
          settingsState.textContent = "读取失败";
        }
      }
    };

    settingsForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = settingsForm.querySelector("button[type='submit']");
      const data = new FormData(settingsForm);
      const payload = {
        http_addr: String(data.get("http_addr") || "").trim(),
        telegram_api_id: Number(data.get("telegram_api_id") || 0),
        telegram_code_fetch_hosts: String(data.get("telegram_code_fetch_hosts") || "")
          .split(",")
          .map((item) => item.trim())
          .filter(Boolean),
        telegram_code_dns_server: String(data.get("telegram_code_dns_server") || "").trim(),
      };
      const apiHash = String(data.get("telegram_api_hash") || "").trim();
      if (apiHash) {
        payload.telegram_api_hash = apiHash;
      }
      button?.setAttribute("disabled", "");
      showNotice(settingsNotice, "");
      try {
        const saved = await requestJSON("/api/v1/settings", {
          method: "PUT",
          body: JSON.stringify(payload),
        });
        renderSettings(saved);
        showNotice(settingsNotice, "设置已保存。请重启服务使配置生效。");
      } catch (error) {
        showNotice(settingsNotice, error.message, true);
      } finally {
        button?.removeAttribute("disabled");
      }
    });
    loadSettings();
  }
})();
