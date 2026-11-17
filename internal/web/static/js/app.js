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
        tableCell(identity),
        tableCell(phone),
        tableCell(username),
        tableCell(status),
        tableCell(formatAccountDate(account.updated_at)),
      );
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
})();
