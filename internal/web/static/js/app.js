(() => {
  const shell = document.querySelector(".app-shell");
  const sidebar = document.getElementById("sidebar");
  const sidebarToggle = document.getElementById("sidebar-toggle");
  const backdrop = document.getElementById("mobile-backdrop");
  const userTrigger = document.getElementById("user-trigger");
  const userDropdown = document.getElementById("user-dropdown");
  const menuToggles = [...document.querySelectorAll("[data-submenu-toggle]")];
  const pageTabs = document.querySelector("[data-page-tabs]");

  if (!shell) {
    return;
  }

  const pageSettingsKey = "telenexus.page-settings";
  const defaultPageSettings = {
    breadcrumbs: true,
    tabs: true,
    sidebar: true,
    compact: true,
    footer: true,
  };

  const readPageSettings = () => {
    try {
      const stored = JSON.parse(window.localStorage.getItem(pageSettingsKey) || "{}");
      return {
        breadcrumbs: stored?.breadcrumbs !== false,
        tabs: stored?.tabs !== false,
        sidebar: stored?.sidebar !== false,
        compact: stored?.compact !== false,
        footer: stored?.footer !== false,
      };
    } catch {
      return {...defaultPageSettings};
    }
  };

  let pageSettings = readPageSettings();

  const syncPageSettingControls = () => {
    document.querySelectorAll("[data-page-setting]").forEach((input) => {
      const key = input.dataset.pageSetting;
      if (Object.hasOwn(pageSettings, key)) {
        input.checked = pageSettings[key];
      }
    });
    document.querySelectorAll("[data-toggle-breadcrumb]").forEach((input) => {
      input.checked = pageSettings.breadcrumbs;
    });
    document.querySelectorAll("[data-toggle-tabs]").forEach((input) => {
      input.checked = pageSettings.tabs;
    });
  };

  const applyPageSettings = () => {
    shell.dataset.breadcrumbs = pageSettings.breadcrumbs ? "visible" : "hidden";
    shell.dataset.tabs = pageSettings.tabs ? "visible" : "hidden";
    shell.dataset.pageSidebar = pageSettings.sidebar ? "visible" : "hidden";
    shell.dataset.compact = pageSettings.compact ? "true" : "false";
    shell.dataset.footer = pageSettings.footer ? "visible" : "hidden";
    syncPageSettingControls();
  };

  const savePageSettings = () => {
    try {
      window.localStorage.setItem(pageSettingsKey, JSON.stringify(pageSettings));
    } catch {
      // Keep the current layout usable when browser storage is unavailable.
    }
    applyPageSettings();
  };

  applyPageSettings();

  const initPageTabs = () => {
    if (!pageTabs) {
      return;
    }
    const track = pageTabs.querySelector("[data-page-tabs-track]");
    const currentPath = pageTabs.dataset.currentPath || "";
    const currentTitle = pageTabs.dataset.currentTitle || "当前页面";
    const storageKey = "telenexus.page-tabs";
    const maxTabs = 20;

    const normalizePath = (value) => {
      if (typeof value !== "string" ||
        !value.startsWith("/") ||
        value.startsWith("//") ||
        value.includes("\\")) {
        return "";
      }
      return value;
    };

    const readTabs = () => {
      try {
        const stored = JSON.parse(window.sessionStorage.getItem(storageKey) || "[]");
        if (!Array.isArray(stored)) {
          return [];
        }
        return stored
          .map((tab) => ({
            path: normalizePath(tab?.path),
            title: String(tab?.title || "").trim().slice(0, 48),
          }))
          .filter((tab) => tab.path && tab.path !== "/" && tab.title);
      } catch {
        return [];
      }
    };

    let tabs = readTabs();
    const activePath = normalizePath(currentPath);
    if (activePath && activePath !== "/") {
      const existing = tabs.find((tab) => tab.path === activePath);
      if (existing) {
        existing.title = currentTitle.trim().slice(0, 48) || existing.title;
      } else {
        tabs.push({path: activePath, title: currentTitle.trim().slice(0, 48) || "当前页面"});
      }
      tabs = tabs.slice(-maxTabs);
    }

    const saveTabs = () => {
      try {
        window.sessionStorage.setItem(storageKey, JSON.stringify(tabs));
      } catch {
        // Keep the current-page tabs functional when storage is unavailable.
      }
    };

    const closeTab = (path) => {
      const index = tabs.findIndex((tab) => tab.path === path);
      if (index < 0) {
        return;
      }
      tabs.splice(index, 1);
      saveTabs();
      if (path === activePath) {
        const next = tabs[index] || tabs[index - 1] || {path: "/"};
        window.location.assign(next.path);
        return;
      }
      renderTabs();
    };

    const makeTab = (tab, active = false, affix = false) => {
      const item = document.createElement("div");
      item.className = `page-tab${active ? " active" : ""}${affix ? " is-affix" : ""}`;
      item.dataset.tabPath = tab.path;
      const link = document.createElement("a");
      link.className = "page-tab-link";
      link.href = tab.path;
      link.textContent = tab.title;
      link.setAttribute("role", "tab");
      link.setAttribute("aria-selected", String(active));
      if (active) {
        link.setAttribute("aria-current", "page");
      }
      item.append(link);
      if (!affix) {
        const close = document.createElement("button");
        close.className = "page-tab-close";
        close.type = "button";
        close.dataset.tabClose = "";
        close.setAttribute("aria-label", `关闭 ${tab.title}`);
        const icon = document.createElement("span");
        icon.className = "icon icon-x";
        icon.setAttribute("aria-hidden", "true");
        close.append(icon);
        item.append(close);
      }
      item.addEventListener("auxclick", (event) => {
        if (event.button === 1 && !affix) {
          event.preventDefault();
          closeTab(tab.path);
        }
      });
      return item;
    };

    const renderTabs = () => {
      if (!track) {
        return;
      }
      track.replaceChildren();
      track.append(makeTab({path: "/", title: "工作台"}, activePath === "/", true));
      tabs.forEach((tab) => {
        track.append(makeTab(tab, tab.path === activePath));
      });
      pageTabs.querySelector("[data-tabs-close-others]")?.toggleAttribute(
        "disabled",
        activePath === "/" && tabs.length === 0,
      );
      track.querySelector(".page-tab.active")?.scrollIntoView({block: "nearest", inline: "nearest"});
    };

    track?.addEventListener("click", (event) => {
      const close = event.target.closest("[data-tab-close]");
      if (!close) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      closeTab(close.closest("[data-tab-path]")?.dataset.tabPath || "");
    });

    pageTabs.querySelectorAll("[data-tabs-scroll]").forEach((button) => {
      button.addEventListener("click", () => {
        const direction = Number(button.dataset.tabsScroll) || 1;
        track?.scrollBy({left: direction * 220, behavior: "smooth"});
      });
    });

    pageTabs.querySelector("[data-tabs-close-others]")?.addEventListener("click", () => {
      if (!activePath || activePath === "/") {
        tabs = [];
      } else {
        tabs = [tabs.find((tab) => tab.path === activePath) || {path: activePath, title: currentTitle}];
      }
      saveTabs();
      renderTabs();
    });

    saveTabs();
    renderTabs();
  };

  initPageTabs();

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

  const loadCurrentUser = async () => {
    try {
      const session = await requestJSON("/api/v1/auth/session");
      if (!session?.authenticated || !session.username) {
        return;
      }
      const name = userTrigger?.querySelector(".user-name");
      if (name) {
        name.textContent = session.username;
      }
      const avatar = userTrigger?.querySelector(".avatar");
      if (avatar) {
        avatar.textContent = session.username.slice(0, 2).toUpperCase();
      }
      const role = userDropdown?.querySelector(".dropdown-caption");
      if (role) {
        const labels = {
          administrator: "系统管理员",
          operator: "操作员",
          viewer: "只读查看者",
        };
        role.textContent = labels[session.role] || session.role || "本地用户";
      }
    } catch {
      // Page-level API calls handle expired sessions consistently.
    }
  };

  loadCurrentUser();

  document.querySelector("[data-logout]")?.addEventListener("click", async (event) => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      await requestJSON("/api/v1/auth/logout", {method: "POST", body: "{}"});
    } catch {
      // The login page remains the safe destination when the session expired.
    } finally {
      window.location.replace("/login");
    }
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

  document.querySelectorAll("[data-toggle-breadcrumb]").forEach((input) => {
    input.addEventListener("change", () => {
      pageSettings.breadcrumbs = input.checked;
      savePageSettings();
    });
  });

  document.querySelectorAll("[data-toggle-tabs]").forEach((input) => {
    input.addEventListener("change", () => {
      pageSettings.tabs = input.checked;
      savePageSettings();
    });
  });

  document.querySelectorAll("[data-page-setting]").forEach((input) => {
    input.addEventListener("change", () => {
      const key = input.dataset.pageSetting;
      if (!Object.hasOwn(pageSettings, key)) {
        return;
      }
      pageSettings[key] = input.checked;
      savePageSettings();
    });
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
  const accountsMobile = document.querySelector("[data-accounts-mobile]");
  const accountsEmpty = document.querySelector("[data-accounts-empty]");
  const accountsFilterEmpty = document.querySelector("[data-accounts-filter-empty]");
  const accountsLoading = document.querySelector("[data-accounts-loading]");
  const accountsError = document.querySelector("[data-accounts-error]");
  const accountSearch = document.querySelector("[data-account-search]");
  const accountStatusFilter = document.querySelector("[data-account-status-filter]");
  const accountGroupFilter = document.querySelector("[data-account-group-filter]");
  const accountFilterReset = document.querySelector("[data-account-filter-reset]");
  const accountBatchBar = document.querySelector("[data-account-batch-bar]");
  const accountSelectedCount = document.querySelector("[data-account-selected-count]");
  const accountSelectAll = document.querySelector("[data-account-select-all]");
  const accountBatchGroup = document.querySelector("[data-account-batch-group]");
  const accountBatchAssign = document.querySelector("[data-account-batch-assign]");
  const accountBatchRefresh = document.querySelector("[data-account-batch-refresh]");
  const accountBatchDelete = document.querySelector("[data-account-batch-delete]");
  const accountShowDeleted = document.querySelector("[data-account-show-deleted]");
  const accountDeletedCount = document.querySelector("[data-account-deleted-count]");
  const accountCounts = [...document.querySelectorAll("[data-account-count]")];
  const onlineCounts = [...document.querySelectorAll("[data-online-count]")];
  const accountToast = document.querySelector("[data-account-toast]");
  const profileDialog = document.getElementById("account-profile-dialog");
  const profileForm = document.querySelector("[data-account-edit-form]");
  const profileName = document.querySelector("[data-account-profile-name]");
  const profileStatus = document.querySelector("[data-account-profile-status]");
  const profileID = document.querySelector("[data-account-profile-id]");
  const profileNotice = document.querySelector("[data-account-edit-notice]");
  const profileWebLink = document.querySelector("[data-account-open-web]");
  const proxyDialog = document.getElementById("account-proxy-dialog");
  const proxyForm = document.querySelector("[data-account-proxy-form]");
  const proxyName = document.querySelector("[data-account-proxy-name]");
  const proxyState = document.querySelector("[data-account-proxy-state]");
  const proxyNotice = document.querySelector("[data-account-proxy-notice]");
  const proxyCheckButton = document.querySelector("[data-account-proxy-check]");
  const proxyClearButton = document.querySelector("[data-account-proxy-clear]");
  const deleteDialog = document.getElementById("account-delete-dialog");
  const deleteForm = document.querySelector("[data-account-delete-form]");
  const deleteName = document.querySelector("[data-account-delete-name]");
  const deleteNotice = document.querySelector("[data-account-delete-notice]");
  let managedAccount = null;
  let accountToastTimer = 0;
  let allAccounts = [];
  let accountGroups = [];
  let accountRefreshWarnings = [];
  let showDeletedAccounts = false;
  const selectedAccountIDs = new Set();

  const showNotice = (element, message, isError = false) => {
    if (!element) {
      return;
    }
    element.textContent = message;
    element.hidden = !message;
    element.classList.toggle("form-notice-error", isError);
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

  const requestJSON = async (path, options = {}) => {
    const headers = new Headers(options.headers || {});
    headers.set("Accept", "application/json");
    if (options.body !== undefined) {
      headers.set("Content-Type", "application/json");
    }
    if (options.method && !["GET", "HEAD", "OPTIONS"].includes(options.method.toUpperCase())) {
      const csrf = document.cookie
        .split(";")
        .map((part) => part.trim())
        .find((part) => part.startsWith("telenexus_csrf="))
        ?.slice("telenexus_csrf=".length);
      if (csrf) {
        headers.set("X-CSRF-Token", decodeURIComponent(csrf));
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
      if (response.status === 401 || payload?.error?.code === "setup_required") {
        window.location.replace("/login");
      }
      const error = new Error(payload?.error?.message || `请求失败（${response.status}）`);
      error.code = payload?.error?.code || "request_failed";
      error.status = response.status;
      throw error;
    }
    return payload;
  };

  const countryCodes = [
    "+886", "+852", "+853", "+971", "+966", "+972", "+351",
    "+358", "+353", "+420", "+421", "+385", "+386", "+381", "+380",
    "+61", "+62", "+63", "+64", "+65", "+66", "+60", "+49", "+44",
    "+43", "+41", "+39", "+34", "+33", "+32", "+31", "+30", "+27",
    "+20", "+1", "+7", "+86", "+81", "+82",
  ];

  const formatPhone = (phone) => {
    const raw = String(phone || "").trim();
    const digits = raw.replace(/\D/g, "");
    if (!digits) {
      return "未设置";
    }
    if (/^\+\d+\s+\d+$/.test(raw)) {
      return raw;
    }
    const normalized = `+${digits}`;
    const countryCode = countryCodes.find((code) => normalized.startsWith(code)) || "";
    if (!countryCode || normalized.length === countryCode.length) {
      return normalized;
    }
    return `${countryCode} ${normalized.slice(countryCode.length)}`;
  };

  const splitPhone = (phone) => {
    const normalized = `+${String(phone || "").replace(/\D/g, "")}`;
    const countryCode = countryCodes.find((code) => normalized.startsWith(code)) || "+1";
    return {
      countryCode,
      localNumber: normalized.slice(countryCode.length),
    };
  };

  const showAccountToast = (message) => {
    if (!accountToast) {
      return;
    }
    window.clearTimeout(accountToastTimer);
    accountToast.textContent = message;
    accountToast.hidden = false;
    accountToastTimer = window.setTimeout(() => {
      accountToast.hidden = true;
    }, 2400);
  };

  const copyText = async (value, successMessage) => {
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      const area = document.createElement("textarea");
      area.value = value;
      area.setAttribute("readonly", "");
      area.style.position = "fixed";
      area.style.opacity = "0";
      document.body.append(area);
      area.select();
      document.execCommand("copy");
      area.remove();
    }
    showAccountToast(successMessage);
  };

  const copyValue = (value, label) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "copy-value";
    button.title = `复制${label}`;
    button.setAttribute("aria-label", `复制${label}：${value}`);
    const text = document.createElement("span");
    text.textContent = value;
    const icon = document.createElement("span");
    icon.className = "icon icon-copy";
    icon.setAttribute("aria-hidden", "true");
    button.append(text, icon);
    button.addEventListener("click", () => copyText(value, `${label}已复制`));
    return button;
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
      second: "2-digit",
      hour12: false,
    }).format(date);
  };

  const statusLabels = {
    online: "在线",
    pending: "待验证",
    paused: "已暂停",
    error: "已掉线",
  };
  const automaticVerificationInterval = 60 * 1000;
  const accountVerificationTTL = 10 * 60 * 1000;
  const verificationTimestampKey = "telenexus.account-verification-at";
  const isDeletedAccount = (account) => (
    Boolean(account?.purge_state) && account.purge_state !== "active"
  );

  const automaticVerificationDue = () => {
    try {
      const checkedAt = Number(window.sessionStorage.getItem(verificationTimestampKey));
      return !Number.isFinite(checkedAt) ||
        Date.now() - checkedAt >= automaticVerificationInterval;
    } catch {
      return true;
    }
  };

  const markAutomaticVerification = () => {
    try {
      window.sessionStorage.setItem(verificationTimestampKey, String(Date.now()));
    } catch {
      // Storage can be unavailable in hardened browser profiles.
    }
  };

  const accountStatus = (account) => {
    if (isDeletedAccount(account)) {
      if (account.purge_state === "scheduled") {
        return {
          key: "deleted",
          label: "待删除",
          reason: `可恢复至 ${formatAccountDate(account.deletion_effective_at)}`,
        };
      }
      if (account.purge_state === "failed") {
        return {
          key: "error",
          label: "清理失败",
          reason: account.purge_error || "后台将自动重试清理",
        };
      }
      return {
        key: "deleted",
        label: "正在清理",
        reason: "账号数据正在后台销毁",
      };
    }
    const status = account?.status || "pending";
    const verifiedAt = account?.last_verified_at
      ? new Date(account.last_verified_at).getTime()
      : Number.NaN;
    const needsVerification = status === "online" &&
      (!Number.isFinite(verifiedAt) ||
        Date.now() - verifiedAt > accountVerificationTTL);
    return {
      key: needsVerification ? "stale" : status,
      label: needsVerification
        ? "状态待复核"
        : (statusLabels[status] || status || "未知"),
      reason: account?.refresh_deferred
        ? "Telegram 暂时不可达，已保留上次状态"
        : needsVerification
          ? "最近一次真实校验已过期，请刷新状态"
        : (account?.status_reason || ""),
    };
  };

  const createSelectOption = (value, label) => {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = label;
    return option;
  };

  const renderAccountGroupOptions = () => {
    const filterValue = accountGroupFilter?.value || "";
    const batchValue = accountBatchGroup?.value || "";
    if (accountGroupFilter) {
      accountGroupFilter.replaceChildren(
        createSelectOption("", "全部分组"),
        ...accountGroups.map((group) => createSelectOption(String(group.id), group.name)),
      );
      accountGroupFilter.value = accountGroups.some((group) => String(group.id) === filterValue)
        ? filterValue
        : "";
    }
    if (accountBatchGroup) {
      accountBatchGroup.replaceChildren(
        createSelectOption("", "选择分组"),
        ...accountGroups.map((group) => createSelectOption(String(group.id), group.name)),
      );
      accountBatchGroup.value = accountGroups.some((group) => String(group.id) === batchValue)
        ? batchValue
        : "";
    }
    if (accountBatchAssign) {
      accountBatchAssign.disabled = !accountBatchGroup?.value;
    }
  };

  const accountMatchesFilters = (account) => {
    if (isDeletedAccount(account) !== showDeletedAccounts) {
      return false;
    }
    const query = String(accountSearch?.value || "").trim().toLocaleLowerCase();
    if (query) {
      const phone = String(account.phone_number || "");
      const haystack = [
        account.display_name,
        account.username ? `@${account.username}` : "",
        account.username,
        formatPhone(phone),
        phone,
        phone.replace(/\D/g, ""),
        String(account.id),
      ].filter(Boolean).join(" ").toLocaleLowerCase();
      if (!haystack.includes(query)) {
        return false;
      }
    }
    const status = accountStatus(account).key;
    if (accountStatusFilter?.value && status !== accountStatusFilter.value) {
      return false;
    }
    const groupID = Number(accountGroupFilter?.value);
    if (groupID > 0) {
      const group = accountGroups.find((item) => Number(item.id) === groupID);
      const accountIDs = Array.isArray(group?.account_ids)
        ? group.account_ids.map(Number)
        : [];
      if (!accountIDs.includes(Number(account.id))) {
        return false;
      }
    }
    return true;
  };

  const filteredAccounts = () => allAccounts.filter(accountMatchesFilters);

  const selectedAccounts = () => filteredAccounts().filter(
    (account) => selectedAccountIDs.has(Number(account.id)),
  );

  const createAccountSelection = (account, className) => {
    const label = document.createElement("label");
    label.className = className;
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.disabled = isDeletedAccount(account);
    checkbox.checked = selectedAccountIDs.has(Number(account.id));
    checkbox.setAttribute(
      "aria-label",
      `选择账号 ${account.display_name || account.username || account.id}`,
    );
    checkbox.addEventListener("change", () => {
      const accountID = Number(account.id);
      if (checkbox.checked) {
        selectedAccountIDs.add(accountID);
      } else {
        selectedAccountIDs.delete(accountID);
      }
      updateSelectionControls();
    });
    label.append(checkbox);
    return label;
  };

  const updateSelectionControls = () => {
    const visible = filteredAccounts();
    const selectable = visible.filter((account) => !isDeletedAccount(account));
    const selectedVisible = selectedAccounts().length;
    if (accountSelectedCount) {
      accountSelectedCount.textContent = String(selectedVisible);
    }
    accountBatchBar?.toggleAttribute("hidden", selectedVisible === 0);
    if (accountSelectAll) {
      accountSelectAll.checked = selectable.length > 0 && selectedVisible === selectable.length;
      accountSelectAll.indeterminate = selectedVisible > 0 && selectedVisible < selectable.length;
      accountSelectAll.disabled = selectable.length === 0;
    }
    if (accountBatchAssign) {
      accountBatchAssign.disabled = !accountBatchGroup?.value || selectedVisible === 0;
    }
  };

  const createStatus = (account) => {
    const state = accountStatus(account);
    const wrapper = document.createElement("span");
    wrapper.className = "account-status-copy";
    const badge = document.createElement("span");
    badge.className = `account-status account-status-${state.key}`;
    badge.textContent = state.label;
    wrapper.append(badge);
    if (state.reason) {
      const reason = document.createElement("small");
      reason.textContent = state.reason;
      wrapper.append(reason);
    }
    return wrapper;
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

  const accountAvatarURL = (account) => (
    accountStatus(account).key === "online"
      ? `/api/v1/accounts/${encodeURIComponent(account.id)}/avatar`
      : ""
  );

  const accountAvatarObserver = typeof IntersectionObserver === "function"
    ? new IntersectionObserver((entries) => {
      entries.forEach((entry) => {
        if (!entry.isIntersecting) {
          return;
        }
        const image = entry.target;
        accountAvatarObserver.unobserve(image);
        if (image.dataset.avatarSrc) {
          queueAccountAvatarLoad(image, image.dataset.avatarSrc);
          delete image.dataset.avatarSrc;
        }
      });
    }, {rootMargin: "120px"})
    : null;
  const accountAvatarQueue = [];
  let accountAvatarActive = null;
  let accountAvatarTimer = 0;
  const accountAvatarTimeout = 12000;
  const clearAccountAvatarTimer = () => {
    window.clearTimeout(accountAvatarTimer);
    accountAvatarTimer = 0;
  };
  const completeAccountAvatarLoad = (image) => {
    if (accountAvatarActive !== image) {
      return;
    }
    clearAccountAvatarTimer();
    accountAvatarActive = null;
    while (accountAvatarQueue.length > 0) {
      const next = accountAvatarQueue.shift();
      if (!next.image.isConnected) {
        continue;
      }
      startAccountAvatarLoad(next.image, next.avatarURL);
      break;
    }
  };
  const startAccountAvatarLoad = (image, avatarURL) => {
    if (!image.isConnected || accountAvatarActive) {
      return;
    }
    accountAvatarActive = image;
    accountAvatarTimer = window.setTimeout(() => {
      if (accountAvatarActive !== image) {
        return;
      }
      image.dispatchEvent(new Event("error"));
    }, accountAvatarTimeout);
    image.src = avatarURL;
  };
  const resetAccountAvatarQueue = () => {
    clearAccountAvatarTimer();
    accountAvatarActive = null;
    accountAvatarQueue.length = 0;
  };
  const queueAccountAvatarLoad = (image, avatarURL) => {
    if (!accountAvatarActive) {
      startAccountAvatarLoad(image, avatarURL);
      return;
    }
    accountAvatarQueue.push({image, avatarURL});
  };

  const applyAccountAvatar = (avatar, account) => {
    const label = account.display_name || account.username || "Telegram";
    if (!accountAvatarURL(account)) {
      resetAccountAvatarQueue();
    }
    avatar.replaceChildren();
    const fallback = document.createElement("span");
    fallback.className = "account-avatar-fallback";
    fallback.textContent = [...label][0] || "T";
    avatar.append(fallback);
    const avatarURL = accountAvatarURL(account);
    if (!avatarURL) {
      return;
    }
    const image = document.createElement("img");
    image.alt = "";
    image.loading = "lazy";
    image.decoding = "async";
    image.addEventListener("load", () => {
      completeAccountAvatarLoad(image);
      avatar.classList.add("has-image");
      avatar.dataset.avatarLoaded = "true";
    }, {once: true});
    image.addEventListener("error", () => {
      completeAccountAvatarLoad(image);
      accountAvatarObserver?.unobserve(image);
      avatar.classList.remove("has-image");
      delete avatar.dataset.avatarLoaded;
      image.remove();
    }, {once: true});
    avatar.append(image);
    if (accountAvatarObserver) {
      image.dataset.avatarSrc = avatarURL;
      accountAvatarObserver.observe(image);
    } else {
      queueAccountAvatarLoad(image, avatarURL);
    }
  };

  const createAccountIdentity = (account) => {
    const identity = document.createElement("div");
    identity.className = "account-identity";
    const avatar = document.createElement("span");
    avatar.className = "account-monogram";
    avatar.setAttribute("aria-hidden", "true");
    applyAccountAvatar(avatar, account);
    const identityCopy = document.createElement("span");
    identityCopy.className = "account-identity-copy";
    const name = document.createElement("strong");
    name.textContent = account.display_name || "未设置姓名";
    const telegramID = document.createElement("small");
    telegramID.textContent = `Telegram ID ${account.id}`;
    identityCopy.append(name, telegramID);
    identity.append(avatar, identityCopy);
    return identity;
  };

  const createAccountActions = (account) => {
    const actionGroup = document.createElement("div");
    actionGroup.className = "account-actions";
    const status = accountStatus(account);
    if (isDeletedAccount(account)) {
      if (account.purge_state === "scheduled") {
        const restore = document.createElement("button");
        restore.className = "button button-primary button-compact";
        restore.type = "button";
        restore.textContent = "恢复账号";
        restore.addEventListener("click", () => restoreAccount(account, restore));
        actionGroup.append(restore);
      } else {
        actionGroup.append(element("span", "button button-secondary button-compact", "后台清理中"));
      }
      return actionGroup;
    }
    if (status.key === "online") {
      const webAction = document.createElement("a");
      webAction.className = "button button-primary button-compact";
      webAction.href = `/accounts/${encodeURIComponent(account.id)}/web`;
      webAction.target = "_blank";
      webAction.rel = "noopener";
      webAction.textContent = "打开 Web";
      actionGroup.append(webAction);
    }
    const refreshAction = document.createElement("button");
    refreshAction.className = "button button-secondary button-compact";
    refreshAction.type = "button";
    const refreshIcon = document.createElement("span");
    refreshIcon.className = "icon icon-refresh-cw";
    refreshIcon.setAttribute("aria-hidden", "true");
    const refreshLabel = document.createElement("span");
    refreshLabel.textContent = "校验状态";
    refreshAction.append(refreshIcon, refreshLabel);
    refreshAction.addEventListener("click", () => refreshSingleAccount(account, refreshAction));

    const relogin = document.createElement("button");
    relogin.className = ["error", "stale"].includes(status.key)
      ? "button button-primary button-compact"
      : "button button-secondary button-compact";
    relogin.type = "button";
    relogin.textContent = "重新登录";
    relogin.addEventListener("click", () => openLoginDialog(account));

    const profileAction = document.createElement("button");
    profileAction.className = "button button-secondary button-compact";
    profileAction.type = "button";
    profileAction.textContent = "资料";
    profileAction.addEventListener("click", () => openProfileDialog(account));
    const deleteAction = document.createElement("button");
    deleteAction.className = "button button-danger-ghost button-compact";
    deleteAction.type = "button";
    deleteAction.textContent = "删除";
    deleteAction.addEventListener("click", () => openDeleteDialog(account));
    const proxyAction = document.createElement("button");
    proxyAction.className = "button button-secondary button-compact";
    proxyAction.type = "button";
    proxyAction.textContent = account.proxy?.enabled ? "代理设置" : "配置代理";
    proxyAction.addEventListener("click", () => openProxyDialog(account));
    if (["error", "stale"].includes(status.key)) {
      actionGroup.append(relogin, refreshAction, profileAction, proxyAction, deleteAction);
    } else {
      actionGroup.append(refreshAction, relogin, profileAction, proxyAction, deleteAction);
    }
    return actionGroup;
  };

  const createAccountMobileCard = (account) => {
    const card = document.createElement("article");
    card.className = "account-mobile-card";
    card.dataset.accountId = String(account.id);

    const header = document.createElement("div");
    header.className = "account-mobile-card-header";
    const status = createStatus(account);
    header.append(
      createAccountSelection(account, "account-mobile-select"),
      createAccountIdentity(account),
      status,
    );

    const details = document.createElement("dl");
    details.className = "account-mobile-details";
    const addDetail = (label, content) => {
      const wrapper = document.createElement("div");
      const term = document.createElement("dt");
      const description = document.createElement("dd");
      term.textContent = label;
      if (content instanceof Node) {
        description.append(content);
      } else {
        description.textContent = content;
      }
      wrapper.append(term, description);
      details.append(wrapper);
    };
    addDetail("手机号", copyValue(formatPhone(account.phone_number), "手机号"));
    addDetail(
      "用户名",
      account.username ? copyValue(`@${account.username}`, "用户名") : "未设置",
    );
    addDetail("最近更新", formatAccountDate(account.updated_at));
    addDetail(
      "最近校验",
      account.last_verified_at ? formatAccountDate(account.last_verified_at) : "尚未校验",
    );
    addDetail("代理", proxyLabelFor(account));

    card.append(header, details, createAccountActions(account));
    return card;
  };

  const closeProfileDialog = () => {
    profileDialog?.close();
    managedAccount = null;
  };

  const openProfileDialog = (account) => {
    if (!profileDialog || !profileForm) {
      return;
    }
    managedAccount = account;
    const name = account.display_name || account.username || "Telegram 账号";
    if (profileName) {
      profileName.textContent = name;
    }
    if (profileStatus) {
      const state = accountStatus(account);
      profileStatus.textContent = state.reason
        ? `${state.label} · ${state.reason}`
        : state.label;
    }
    if (profileID) {
      profileID.textContent = `Telegram ID ${account.id}`;
    }
    profileForm.elements.namedItem("display_name").value = account.display_name || "";
    const phoneNumber = String(account.phone_number || "");
    profileForm.elements.namedItem("phone_number").value = phoneNumber
      ? formatPhone(phoneNumber)
      : "";
    profileForm.elements.namedItem("username").value = account.username ? `@${account.username}` : "";
    profileWebLink?.toggleAttribute("hidden", accountStatus(account).key !== "online");
    if (profileWebLink) {
      profileWebLink.href = `/accounts/${encodeURIComponent(account.id)}/web`;
    }
    showNotice(profileNotice, "");
    profileDialog.showModal();
  };

  const openDeleteDialog = (account) => {
    if (!deleteDialog) {
      return;
    }
    managedAccount = account;
    if (deleteName) {
      deleteName.textContent = account.display_name || account.username || `账号 ${account.id}`;
    }
    showNotice(deleteNotice, "");
    deleteDialog.showModal();
  };

  const proxyLabelFor = (account) => {
    const proxy = account.proxy;
    if (!proxy?.enabled) {
      return "未配置";
    }
    const host = [proxy.host, proxy.port].filter(Boolean).join(":");
    return proxy.last_error
      ? `${host} · 连接异常`
      : proxy.last_checked_at
        ? `${host} · 已校验`
        : host;
  };

  const openProxyDialog = (account) => {
    if (!proxyDialog || !proxyForm) {
      return;
    }
    managedAccount = account;
    if (proxyName) {
      proxyName.textContent =
        account.display_name || account.username || `账号 ${account.id}`;
    }
    const proxy = account.proxy;
    if (proxyState) {
      proxyState.textContent = proxy?.enabled
        ? `${proxy.host}:${proxy.port}${proxy.last_error
          ? ` · ${proxy.last_error}`
          : proxy.last_checked_at
            ? " · 连接正常"
            : " · 待校验"}`
        : "仅影响该账号的 Telegram 连接";
    }
    proxyForm.reset();
    const configuredHint = proxy?.enabled
      ? "输入新的 SOCKS5 地址以替换现有代理；密码不会回显。"
      : "例如 socks5://127.0.0.1:1080";
    proxyForm.elements.namedItem("proxy_url").placeholder = configuredHint;
    proxyCheckButton?.toggleAttribute("hidden", !proxy?.enabled);
    proxyClearButton?.toggleAttribute("hidden", !proxy?.enabled);
    showNotice(proxyNotice, "");
    proxyDialog.showModal();
  };

  const closeProxyDialog = () => {
    proxyDialog?.close();
    managedAccount = null;
  };

  const renderAccounts = (items) => {
    if (!accountBody || !accountsMobile) {
      return;
    }
    accountBody.replaceChildren();
    accountsMobile.replaceChildren();
    const activeAccounts = allAccounts.filter((item) => !isDeletedAccount(item));
    const deletedAccounts = allAccounts.filter(isDeletedAccount);
    const online = activeAccounts.filter(
      (item) => accountStatus(item).key === "online",
    ).length;
    accountCounts.forEach((element) => {
      element.textContent = String(activeAccounts.length);
    });
    onlineCounts.forEach((element) => {
      element.textContent = String(online);
    });

    const hasItems = items.length > 0;
    accountsTable?.toggleAttribute("hidden", !hasItems);
    accountsMobile.toggleAttribute("hidden", !hasItems);
    accountsEmpty?.toggleAttribute(
      "hidden",
      activeAccounts.length > 0 || showDeletedAccounts,
    );
    accountsFilterEmpty?.toggleAttribute(
      "hidden",
      hasItems || (!showDeletedAccounts && activeAccounts.length === 0),
    );
    if (accountDeletedCount) {
      accountDeletedCount.textContent = String(deletedAccounts.length);
    }
    accountsLoading?.setAttribute("hidden", "");
    if (!hasItems) {
      updateSelectionControls();
      return;
    }

    items.forEach((account) => {
      const row = document.createElement("tr");
      row.className = "account-row";
      row.dataset.accountId = String(account.id);

      const identity = createAccountIdentity(account);
      const phone = copyValue(formatPhone(account.phone_number), "手机号");
      const username = account.username
        ? copyValue(`@${account.username}`, "用户名")
        : "未设置";

      const status = createStatus(account);
      const proxy = element(
        "span",
        `proxy-summary${account.proxy?.enabled ? " is-configured" : ""}`,
        proxyLabelFor(account),
      );

      row.append(
        tableCell(createAccountSelection(account, "account-row-select")),
        tableCell(identity),
        tableCell(phone),
        tableCell(username),
        tableCell(proxy),
        tableCell(status),
        tableCell(formatAccountDate(account.last_verified_at || account.updated_at)),
        tableCell(createAccountActions(account)),
      );
      accountBody.append(row);
      accountsMobile.append(createAccountMobileCard(account));
    });
    updateSelectionControls();
  };

  document.querySelectorAll("[data-account-profile-close]").forEach((button) => {
    button.addEventListener("click", closeProfileDialog);
  });
  document.querySelectorAll("[data-account-delete-close]").forEach((button) => {
    button.addEventListener("click", () => {
      deleteDialog?.close();
      managedAccount = null;
    });
  });
  document.querySelectorAll("[data-account-proxy-close]").forEach((button) => {
    button.addEventListener("click", closeProxyDialog);
  });
  proxyForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!managedAccount) {
      return;
    }
    const data = new FormData(proxyForm);
    const proxyURL = String(data.get("proxy_url") || "").trim();
    if (!proxyURL) {
      showNotice(proxyNotice, "请输入 SOCKS5 代理地址", true);
      return;
    }
    const submit = proxyForm.querySelector("button[type='submit']");
    submit?.setAttribute("disabled", "");
    showNotice(proxyNotice, "正在保存并重连该账号...");
    try {
      await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(managedAccount.id)}/proxy`,
        {method: "PUT", body: JSON.stringify({proxy_url: proxyURL})},
      );
      closeProxyDialog();
      showAccountToast("代理已保存，账号连接正在重连");
      await loadAccounts();
    } catch (error) {
      showNotice(proxyNotice, error.message, true);
    } finally {
      submit?.removeAttribute("disabled");
    }
  });
  proxyCheckButton?.addEventListener("click", async () => {
    if (!managedAccount) {
      return;
    }
    proxyCheckButton.setAttribute("disabled", "");
    showNotice(proxyNotice, "正在通过代理连接 Telegram...");
    try {
      const result = await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(managedAccount.id)}/proxy/check`,
        {method: "POST", body: "{}"},
      );
      showNotice(proxyNotice, result?.message || "代理连接正常");
      showAccountToast("代理连接正常");
    } catch (error) {
      showNotice(proxyNotice, error.message, true);
    } finally {
      proxyCheckButton.removeAttribute("disabled");
    }
  });
  proxyClearButton?.addEventListener("click", async () => {
    if (!managedAccount) {
      return;
    }
    proxyClearButton.setAttribute("disabled", "");
    showNotice(proxyNotice, "正在移除代理...");
    try {
      await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(managedAccount.id)}/proxy`,
        {method: "DELETE", body: "{}"},
      );
      closeProxyDialog();
      showAccountToast("代理已移除，账号连接正在重连");
      await loadAccounts();
    } catch (error) {
      showNotice(proxyNotice, error.message, true);
    } finally {
      proxyClearButton.removeAttribute("disabled");
    }
  });
  profileForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!managedAccount) {
      return;
    }
    const submit = profileForm.querySelector("button[type='submit']");
    const data = new FormData(profileForm);
    submit?.setAttribute("disabled", "");
    showNotice(profileNotice, "");
    try {
      await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(managedAccount.id)}`,
        {
          method: "PUT",
          body: JSON.stringify({
            display_name: String(data.get("display_name") || "").trim(),
            phone_number: `+${String(data.get("phone_number") || "").replace(/\D/g, "")}`,
            username: String(data.get("username") || "").trim(),
          }),
        },
      );
      closeProfileDialog();
      showAccountToast("账号资料已保存");
      await loadAccounts();
    } catch (error) {
      showNotice(profileNotice, error.message, true);
    } finally {
      submit?.removeAttribute("disabled");
    }
  });
  deleteForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!managedAccount) {
      return;
    }
    const submit = deleteForm.querySelector("button[type='submit']");
    submit?.setAttribute("disabled", "");
    showNotice(deleteNotice, "");
    try {
      await requestJSON(`/api/v1/accounts/${encodeURIComponent(managedAccount.id)}`, {
        method: "DELETE",
      });
      deleteDialog?.close();
      managedAccount = null;
      showDeletedAccounts = true;
      accountShowDeleted?.setAttribute("aria-pressed", "true");
      showAccountToast("账号已进入待删除状态，可在 24 小时内恢复");
      await loadAccounts();
    } catch (error) {
      showNotice(deleteNotice, error.message, true);
    } finally {
      submit?.removeAttribute("disabled");
    }
  });

  const loadAccountGroups = async () => {
    try {
      const payload = await requestJSON("/api/v1/groups");
      accountGroups = Array.isArray(payload?.items) ? payload.items : [];
    } catch {
      accountGroups = [];
    }
    renderAccountGroupOptions();
    return accountGroups;
  };

  const loadAccounts = async (verifySessions = false, accountIDs = null) => {
    if (!accountBody) {
      return false;
    }
    if (!verifySessions) {
      accountsLoading?.removeAttribute("hidden");
      accountsTable?.setAttribute("hidden", "");
      accountsEmpty?.setAttribute("hidden", "");
      accountsFilterEmpty?.setAttribute("hidden", "");
    }
    showNotice(accountsError, "");
    try {
      const payload = verifySessions
        ? (Array.isArray(accountIDs) && accountIDs.length > 0
            ? await refreshSelectedAccounts(accountIDs)
            : await requestJSON("/api/v1/accounts/refresh", {method: "POST", body: "{}"}))
        : await requestJSON("/api/v1/accounts?limit=100&include_deleted=true");
      if (verifySessions) {
        showDeletedAccounts = false;
        accountShowDeleted?.setAttribute("aria-pressed", "false");
        accountRefreshWarnings = Array.isArray(payload?.warnings) ? payload.warnings : [];
      } else {
        accountRefreshWarnings = [];
      }
      const items = Array.isArray(payload?.items) ? payload.items : [];
      if (verifySessions) {
        const outcomes = new Map(
          (Array.isArray(payload?.outcomes) ? payload.outcomes : [])
            .map((outcome) => [Number(outcome.account_id), outcome]),
        );
        items.forEach((item) => {
          const outcome = outcomes.get(Number(item.id));
          if (!outcome) {
            return;
          }
          item.status = outcome.status || item.status;
          item.status_reason = outcome.reason || "";
          item.refresh_deferred = Boolean(outcome.deferred);
          item.refresh_skipped = Boolean(outcome.skipped);
          if (!outcome.deferred && !outcome.skipped) {
            item.last_verified_at = outcome.verified_at || item.last_verified_at;
          }
        });
      }
      allAccounts = items;
      const validAccountIDs = new Set(allAccounts.map((account) => Number(account.id)));
      for (const accountID of selectedAccountIDs) {
        if (!validAccountIDs.has(accountID)) {
          selectedAccountIDs.delete(accountID);
        }
      }
      renderAccounts(filteredAccounts());
      showNotice(accountsError, "");
      return true;
    } catch (error) {
      accountsLoading?.setAttribute("hidden", "");
      if (verifySessions) {
        renderAccounts(filteredAccounts());
      }
      showNotice(accountsError, error.message, true);
      return false;
    }
  };

  const refreshSelectedAccounts = async (accountIDs) => {
    const results = await Promise.allSettled(accountIDs.map(async (accountID) => {
      const response = await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(accountID)}/refresh`,
        {method: "POST", body: "{}"},
      );
      return {
        item: response?.item || response,
        outcome: Array.isArray(response?.outcomes) ? response.outcomes[0] : null,
        verifiedAt: response?.verified_at || "",
      };
    }));
    const items = [];
    const outcomes = [];
    const warnings = [];
    results.forEach((result, index) => {
      if (result.status === "fulfilled") {
        const {item, outcome, verifiedAt} = result.value;
        items.push(item);
        outcomes.push({
          account_id: Number(item.id),
          status: outcome?.status || item.status,
          reason: outcome?.reason || item.status_reason || "",
          deferred: Boolean(outcome?.deferred),
          skipped: Boolean(outcome?.skipped),
          verified_at: outcome?.verified_at || verifiedAt || item.last_verified_at || "",
        });
        return;
      }
      warnings.push({
        account_id: Number(accountIDs[index]),
        message: result.reason?.message || "账号状态暂时无法校验",
      });
    });
    const current = new Map(allAccounts.map((account) => [Number(account.id), account]));
    items.forEach((item) => current.set(Number(item.id), item));
    return {
      items: [...current.values()],
      outcomes,
      warnings,
      verified_at: outcomes.map((outcome) => outcome.verified_at).filter(Boolean).sort().at(-1) || "",
    };
  };

  const setBatchButtonsDisabled = (disabled) => {
    [accountBatchAssign, accountBatchRefresh, accountBatchDelete].forEach((button) => {
      if (button) {
        button.disabled = disabled;
      }
    });
  };

  const refreshSingleAccount = async (account, button) => {
    if (!account?.id) {
      return;
    }
    button?.setAttribute("disabled", "");
    button?.setAttribute("aria-busy", "true");
    showNotice(accountsError, "");
    try {
      const updated = await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(account.id)}/refresh`,
        {method: "POST", body: "{}"},
      );
      const refreshed = updated?.item || updated;
      const outcome = Array.isArray(updated?.outcomes) ? updated.outcomes[0] : null;
      if (outcome) {
        refreshed.status = outcome.status || refreshed.status;
        refreshed.status_reason = outcome.reason || "";
        refreshed.refresh_deferred = Boolean(outcome.deferred);
        refreshed.refresh_skipped = Boolean(outcome.skipped);
        if (!outcome.deferred && !outcome.skipped) {
          refreshed.last_verified_at = outcome.verified_at || refreshed.last_verified_at;
        }
      }
      allAccounts = allAccounts.map((item) => (
        Number(item.id) === Number(refreshed.id) ? refreshed : item
      ));
      renderAccounts(filteredAccounts());
      const state = accountStatus(refreshed);
      showAccountToast(
        refreshed.refresh_deferred
          ? `账号校验未完成：${state.reason}`
          : refreshed.refresh_skipped
            ? `已跳过校验：${state.reason}`
          : state.key === "error"
            ? `账号校验完成：${state.label}${state.reason ? ` · ${state.reason}` : ""}`
            : state.key === "stale"
              ? "状态校验结果缺少最新校验时间，请重试"
              : `${formatPhone(refreshed.phone_number)} · 授权有效`,
      );
    } catch (error) {
      showNotice(accountsError, error.message, true);
    } finally {
      button?.removeAttribute("disabled");
      button?.removeAttribute("aria-busy");
    }
  };

  const restoreAccount = async (account, button) => {
    button?.setAttribute("disabled", "");
    button?.setAttribute("aria-busy", "true");
    showNotice(accountsError, "");
    try {
      await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(account.id)}/restore`,
        {method: "POST", body: "{}"},
      );
      showDeletedAccounts = false;
      accountShowDeleted?.setAttribute("aria-pressed", "false");
      showAccountToast("账号已恢复，请重新校验 Telegram 授权状态");
      await loadAccounts();
    } catch (error) {
      showNotice(accountsError, error.message, true);
    } finally {
      button?.removeAttribute("disabled");
      button?.removeAttribute("aria-busy");
    }
  };

  [accountSearch, accountStatusFilter, accountGroupFilter].forEach((control) => {
    control?.addEventListener("input", () => renderAccounts(filteredAccounts()));
    control?.addEventListener("change", () => renderAccounts(filteredAccounts()));
  });
  accountFilterReset?.addEventListener("click", () => {
    if (accountSearch) {
      accountSearch.value = "";
    }
    if (accountStatusFilter) {
      accountStatusFilter.value = "";
    }
    if (accountGroupFilter) {
      accountGroupFilter.value = "";
    }
    showDeletedAccounts = false;
    accountShowDeleted?.setAttribute("aria-pressed", "false");
    renderAccounts(filteredAccounts());
    accountSearch?.focus();
  });
  accountShowDeleted?.addEventListener("click", () => {
    showDeletedAccounts = !showDeletedAccounts;
    selectedAccountIDs.clear();
    accountShowDeleted.setAttribute("aria-pressed", String(showDeletedAccounts));
    renderAccounts(filteredAccounts());
  });
  accountSelectAll?.addEventListener("change", () => {
    filteredAccounts().forEach((account) => {
      if (isDeletedAccount(account)) {
        return;
      }
      const accountID = Number(account.id);
      if (accountSelectAll.checked) {
        selectedAccountIDs.add(accountID);
      } else {
        selectedAccountIDs.delete(accountID);
      }
    });
    accountBody?.querySelectorAll(
      ".account-row-select input, .account-mobile-select input",
    )
      .forEach((checkbox) => {
        checkbox.checked = accountSelectAll.checked;
      });
    updateSelectionControls();
  });
  accountBatchGroup?.addEventListener("change", updateSelectionControls);

  accountBatchAssign?.addEventListener("click", async () => {
    const groupID = Number(accountBatchGroup?.value);
    const group = accountGroups.find((item) => Number(item.id) === groupID);
    if (!group || selectedAccountIDs.size === 0) {
      return;
    }
    setBatchButtonsDisabled(true);
    showNotice(accountsError, "正在更新账号分组...");
    try {
      const accountIDs = new Set(
        (Array.isArray(group.account_ids) ? group.account_ids : []).map(Number),
      );
      selectedAccountIDs.forEach((accountID) => accountIDs.add(accountID));
      await requestJSON(`/api/v1/groups/${encodeURIComponent(group.id)}/accounts`, {
        method: "PUT",
        body: JSON.stringify({account_ids: [...accountIDs]}),
      });
      await loadAccountGroups();
      await loadAccounts();
      showAccountToast(`已将 ${selectedAccountIDs.size} 个账号加入“${group.name}”`);
    } catch (error) {
      showNotice(accountsError, error.message, true);
    } finally {
      setBatchButtonsDisabled(false);
      updateSelectionControls();
    }
  });

  accountBatchRefresh?.addEventListener("click", async () => {
    const targets = selectedAccounts().filter(
      (account) => !isDeletedAccount(account),
    );
    if (targets.length === 0) {
      showNotice(accountsError, "所选账号当前没有可刷新的会话状态。", true);
      return;
    }
    setBatchButtonsDisabled(true);
    showNotice(accountsError, `正在验证 ${targets.length} 个账号的 Telegram 授权...`);
    try {
      if (await loadAccounts(true, targets.map((account) => Number(account.id)))) {
        markAutomaticVerification();
        const active = allAccounts.filter((account) => !isDeletedAccount(account));
        const offline = active.filter((account) => accountStatus(account).key === "error").length;
        const skipped = active.filter((account) => account.refresh_skipped).length;
        const deferred = new Set(accountRefreshWarnings.map((warning) => Number(warning.account_id))).size;
        const checked = active.length - skipped - deferred;
        if (deferred > 0) {
          showAccountToast(
            `状态校验完成：${checked} 个已校验，${deferred} 个暂时不可达，${skipped} 个已跳过`,
          );
        } else {
          showAccountToast(
            skipped > 0
              ? `状态校验完成：${checked} 个已校验，${skipped} 个已跳过${offline > 0 ? `，${offline} 个需要重新登录` : ""}`
              : offline > 0
                ? `状态校验完成，${offline} 个账号需要重新登录`
                : `状态校验完成，${checked} 个账号授权有效`,
          );
        }
      } else {
        showNotice(accountsError, "账号状态校验失败，请稍后重试。", true);
      }
    } finally {
      setBatchButtonsDisabled(false);
      updateSelectionControls();
    }
  });

  accountBatchDelete?.addEventListener("click", async () => {
    const targets = selectedAccounts();
    if (targets.length === 0) {
      return;
    }
    if (!window.confirm(
      `确定删除所选 ${targets.length} 个本地账号吗？账号将进入 24 小时待删除状态，期间可以恢复。`,
    )) {
      return;
    }
    setBatchButtonsDisabled(true);
    showNotice(accountsError, `正在将 ${targets.length} 个账号加入待删除...`);
    let failed = 0;
    try {
      for (const account of targets) {
        try {
          await requestJSON(`/api/v1/accounts/${encodeURIComponent(account.id)}`, {
            method: "DELETE",
          });
          selectedAccountIDs.delete(Number(account.id));
        } catch {
          failed += 1;
        }
      }
      await loadAccounts();
      showDeletedAccounts = true;
      accountShowDeleted?.setAttribute("aria-pressed", "true");
      renderAccounts(filteredAccounts());
      if (failed > 0) {
        showNotice(accountsError, `${failed} 个账号未能加入待删除，请稍后重试。`, true);
      } else {
        showAccountToast(`${targets.length} 个账号已进入待删除状态`);
      }
    } finally {
      setBatchButtonsDisabled(false);
      updateSelectionControls();
    }
  });

  const telegramConfig = document.querySelector("[data-telegram-config]");
  let telegramConfigured = false;
  let accountVerificationInFlight = false;
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

  const verifyAccountStatuses = async (automatic = false) => {
    if (accountVerificationInFlight) {
      return;
    }
    accountVerificationInFlight = true;
    const button = document.querySelector("[data-accounts-refresh]");
    const label = button?.querySelector("[data-accounts-refresh-label]");
    if (button) {
      button.disabled = true;
      button.setAttribute("aria-busy", "true");
    }
    if (label) {
      label.textContent = "正在校验";
    }
    try {
      if (!automatic) {
        showNotice(accountsError, "正在验证 Telegram 授权状态...");
      }
      if (await loadAccounts(true)) {
        markAutomaticVerification();
        const active = allAccounts.filter((account) => !isDeletedAccount(account));
        const offline = active.filter((account) => accountStatus(account).key === "error").length;
        const skipped = active.filter((account) => account.refresh_skipped).length;
        const deferred = new Set(accountRefreshWarnings.map((warning) => Number(warning.account_id))).size;
        const checked = active.length - skipped - deferred;
        if (deferred > 0) {
          showAccountToast(
            `状态校验完成：${checked} 个已校验，${deferred} 个暂时不可达，${skipped} 个已跳过`,
          );
        } else {
          showAccountToast(
            skipped > 0
              ? `状态校验完成：${checked} 个已校验，${skipped} 个已跳过${offline > 0 ? `，${offline} 个需要重新登录` : ""}`
              : offline > 0
                ? `状态校验完成，${offline} 个账号需要重新登录`
                : `状态校验完成，${checked} 个账号授权有效`,
          );
        }
      }
    } finally {
      if (button) {
        button.disabled = false;
        button.removeAttribute("aria-busy");
      }
      if (label) {
        label.textContent = "刷新状态";
      }
      accountVerificationInFlight = false;
    }
  };

  const automaticStatusCheckNeeded = () => allAccounts.some((account) => (
    !isDeletedAccount(account) &&
    ["online", "error"].includes(accountStatus(account).key)
  ));

  const maybeVerifyExpiredAccounts = () => {
    if (document.visibilityState !== "visible" ||
        !automaticVerificationDue() ||
        accountVerificationInFlight) {
      return;
    }
    if (!automaticStatusCheckNeeded()) {
      markAutomaticVerification();
      return;
    }
    verifyAccountStatuses(true);
  };

  document.querySelector("[data-accounts-refresh]")?.addEventListener("click", () => {
    verifyAccountStatuses();
  });

  if (accountBody) {
    loadAccountGroups().then(async () => {
      await loadAccounts();
      const requestedID = Number(new URLSearchParams(window.location.search).get("relogin"));
      if (Number.isSafeInteger(requestedID) && requestedID > 0) {
        const requestedAccount = allAccounts.find(
          (account) => Number(account.id) === requestedID,
        );
        if (requestedAccount) {
          openLoginDialog(requestedAccount);
          window.history.replaceState({}, "", "/accounts");
          return;
        }
      }
      if (automaticVerificationDue()) {
        verifyAccountStatuses(true);
      }
    });
    loadTelegramConfig();
    window.setInterval(maybeVerifyExpiredAccounts, automaticVerificationInterval);
    document.addEventListener("visibilitychange", maybeVerifyExpiredAccounts);
  }

  const authDialog = document.getElementById("telegram-login-dialog");
  const reloginDialog = document.getElementById("account-relogin-dialog");
  const reloginForm = document.querySelector("[data-account-relogin-form]");
  const reloginNotice = document.querySelector("[data-account-relogin-notice]");
  const authSteps = [...document.querySelectorAll("[data-auth-step]")];
  const progressSteps = [...document.querySelectorAll("[data-progress-step]")];
  const phoneForm = document.querySelector("[data-phone-form]");
  const usernameForm = document.querySelector("[data-username-form]");
  const codeForm = document.querySelector("[data-code-form]");
  const passwordForm = document.querySelector("[data-password-form]");
  const phoneNotice = document.querySelector("[data-phone-notice]");
  const codeNotice = document.querySelector("[data-code-notice]");
  const passwordNotice = document.querySelector("[data-password-notice]");
  const usernameNotice = document.querySelector("[data-username-notice]");
  const qrNotice = document.querySelector("[data-qr-notice]");
  const qrImage = document.querySelector("[data-qr-image]");
  const qrLoading = document.querySelector("[data-qr-loading]");
  const qrCountdown = document.querySelector("[data-qr-countdown]");
  const loginModeButtons = [...document.querySelectorAll("[data-login-mode]")];
  const phoneHint = document.querySelector("[data-auth-phone-hint]");
  const codeType = document.querySelector("[data-auth-code-type]");
  const codeInput = codeForm?.querySelector("input[name='code']");
  const completeName = document.querySelector("[data-complete-name]");
  const viewChatButton = document.querySelector("[data-login-view-chat]");
  const viewWebButton = document.querySelector("[data-login-view-web]");
  const qrRestart = document.querySelector("[data-qr-restart]");
  const loginKicker = document.querySelector("[data-login-kicker]");
  const loginTitle = document.querySelector("[data-login-title]");
  let loginID = "";
  let qrLoginID = "";
  let qrPollTimer = 0;
  let qrPollAttempts = 0;
  let qrRefreshAt = 0;
  let qrCountdownTimer = 0;
  let authCompleted = false;
  let reloginAccountID = 0;

  const stopQRPolling = () => {
    if (qrPollTimer) {
      window.clearTimeout(qrPollTimer);
      qrPollTimer = 0;
    }
  };

  const stopQRCountdown = () => {
    qrRefreshAt = 0;
    if (qrCountdownTimer) {
      window.clearInterval(qrCountdownTimer);
      qrCountdownTimer = 0;
    }
    if (qrCountdown) {
      qrCountdown.hidden = true;
      qrCountdown.textContent = "";
    }
  };

  const updateQRCountdown = () => {
    if (!qrRefreshAt || !qrCountdown) {
      return;
    }
    const seconds = Math.max(0, Math.ceil((qrRefreshAt - Date.now()) / 1000));
    qrCountdown.hidden = false;
    qrCountdown.textContent = seconds > 0
      ? `距二维码刷新还有 ${seconds} 秒`
      : "正在向 Telegram 获取新二维码...";
  };

  const setQRRefreshAt = (value) => {
    const refreshAt = new Date(value || "").getTime();
    stopQRCountdown();
    if (!Number.isFinite(refreshAt) || refreshAt <= 0) {
      return;
    }
    qrRefreshAt = refreshAt;
    updateQRCountdown();
    qrCountdownTimer = window.setInterval(updateQRCountdown, 1000);
  };

  const setAuthStep = (name) => {
    const order = ["qr", "phone", "code", "password", "complete"];
    const index = order.indexOf(name);
    authSteps.forEach((step) => {
      step.hidden = step.dataset.authStep !== name;
    });
    progressSteps.forEach((step) => {
      const stepIndex = order.indexOf(step.dataset.progressStep);
      step.classList.toggle("active", stepIndex <= index && stepIndex >= 0);
    });
  };

  const setAuthMode = (mode, start = true) => {
    if (!["qr", "phone", "username"].includes(mode)) {
      return;
    }
    loginModeButtons.forEach((button) => {
      const active = button.dataset.loginMode === mode;
      button.classList.toggle("active", active);
      button.setAttribute("aria-selected", String(active));
    });
    if (start) {
      cancelLogin();
      showNotice(phoneNotice, "");
      showNotice(usernameNotice, "");
      showNotice(qrNotice, "");
      showNotice(codeNotice, "");
      showNotice(passwordNotice, "");
    }
    setAuthStep(mode);
    if (!start) {
      return;
    }
    if (mode === "qr") {
      if (telegramConfigured) {
        startQRAuth();
      } else {
        showNotice(qrNotice, "请先在系统设置中配置 Telegram API ID 和 API Hash。", true);
      }
    } else if (mode === "phone") {
      phoneForm?.querySelector("input[name='phone_number']")?.focus();
    } else {
      usernameForm?.querySelector("input[name='username']")?.focus();
    }
  };

  const cancelLogin = () => {
    stopQRPolling();
    stopQRCountdown();
    const current = loginID;
    const currentQR = qrLoginID;
    loginID = "";
    qrLoginID = "";
    if (authCompleted) {
      return;
    }
    if (current) {
      fetch(`/api/v1/telegram/auth/${encodeURIComponent(current)}`, {method: "DELETE"}).catch(() => {});
    }
    if (currentQR) {
      fetch(`/api/v1/telegram/auth/qr/${encodeURIComponent(currentQR)}`, {method: "DELETE"}).catch(() => {});
    }
  };

  async function startQRAuth() {
    if (!telegramConfigured || authCompleted) {
      return;
    }
    stopQRPolling();
    const previousLoginID = qrLoginID;
    qrLoginID = "";
    qrPollAttempts = 0;
    showNotice(qrNotice, "");
    qrImage?.setAttribute("hidden", "");
    qrLoading?.removeAttribute("hidden");
    stopQRCountdown();
    if (qrRestart) {
      qrRestart.disabled = true;
    }
    if (previousLoginID) {
      fetch(`/api/v1/telegram/auth/qr/${encodeURIComponent(previousLoginID)}`, {
        method: "DELETE",
      }).catch(() => {});
    }
    try {
      const challenge = await requestJSON("/api/v1/telegram/auth/qr", {
        method: "POST",
        body: JSON.stringify({account_id: reloginAccountID}),
      });
      qrLoginID = challenge.login_id;
      setQRRefreshAt(challenge.refresh_at);
      if (qrImage && challenge.image) {
        qrImage.src = challenge.image;
        qrImage.removeAttribute("hidden");
      }
      qrLoading?.setAttribute("hidden", "");
      pollQRAuth();
    } catch (error) {
      qrLoading?.setAttribute("hidden", "");
      showNotice(qrNotice, error.message, true);
    } finally {
      if (qrRestart) {
        qrRestart.disabled = false;
      }
    }
  }

  async function pollQRAuth() {
    if (!qrLoginID || authCompleted) {
      return;
    }
    const currentLoginID = qrLoginID;
    try {
      const result = await requestJSON(
        `/api/v1/telegram/auth/qr/${encodeURIComponent(currentLoginID)}`,
      );
      if (qrLoginID !== currentLoginID || authCompleted) {
        return;
      }
      qrPollAttempts = 0;
      if (result.refresh_at) {
        setQRRefreshAt(result.refresh_at);
      }
      if (result.image && qrImage && result.image !== qrImage.src) {
        qrImage.src = result.image;
        qrImage.removeAttribute("hidden");
        qrLoading?.setAttribute("hidden", "");
      }
      if (result.next_step === "complete") {
        stopQRPolling();
        stopQRCountdown();
        qrLoginID = "";
        finishAuth(result);
        return;
      }
    } catch (error) {
      if (error.code === "login_not_found") {
        stopQRPolling();
        stopQRCountdown();
        qrLoginID = "";
        showNotice(qrNotice, "二维码已过期，请点击“重新生成二维码”。", true);
        return;
      }
      if (error.code === "telegram_account_mismatch") {
        stopQRPolling();
        stopQRCountdown();
        qrLoginID = "";
        qrImage?.setAttribute("hidden", "");
        showNotice(qrNotice, error.message, true);
        return;
      }
      qrPollAttempts += 1;
      if (qrPollAttempts >= 8) {
        showNotice(qrNotice, error.message, true);
      }
    }
    if (qrLoginID === currentLoginID && !authCompleted) {
      qrPollTimer = window.setTimeout(pollQRAuth, qrPollAttempts >= 8 ? 5000 : 1800);
    }
  }

  const resetLoginDialog = () => {
    cancelLogin();
    authCompleted = false;
    reloginAccountID = 0;
    phoneForm?.reset();
    usernameForm?.reset();
    codeForm?.reset();
    passwordForm?.reset();
    showNotice(phoneNotice, "");
    showNotice(codeNotice, "");
    showNotice(passwordNotice, "");
    showNotice(usernameNotice, "");
    showNotice(qrNotice, "");
    qrImage?.setAttribute("hidden", "");
    if (qrImage) {
      qrImage.removeAttribute("src");
    }
    setAuthMode("qr", false);
    viewChatButton?.setAttribute("hidden", "");
    viewChatButton?.removeAttribute("href");
    viewWebButton?.setAttribute("hidden", "");
    viewWebButton?.removeAttribute("href");
    setAuthStep("qr");
    const submit = phoneForm?.querySelector("button[type='submit']");
    if (submit) {
      submit.disabled = !telegramConfigured;
    }
    if (!telegramConfigured) {
      const message = "请先在系统设置中配置 Telegram API ID 和 API Hash，然后重启服务。";
      showNotice(qrNotice, message, true);
      showNotice(phoneNotice, message, true);
    }
  };

  const openLoginDialog = (account = null) => {
    if (!authDialog) {
      return;
    }
    resetLoginDialog();
    const relogin = Boolean(account?.phone_number);
    reloginAccountID = Number(account?.id) > 0 ? Number(account.id) : 0;
    if (loginKicker) {
      loginKicker.textContent = relogin ? "恢复 Telegram 授权" : "官方 MTProto 登录";
    }
    if (loginTitle) {
      loginTitle.textContent = relogin
        ? `重新登录 ${account.display_name || account.username || formatPhone(account.phone_number)}`
        : "登录 Telegram 账号";
    }
    if (relogin && phoneForm) {
      const phone = splitPhone(account.phone_number);
      const countrySelect = phoneForm.elements.namedItem("country_code");
      const localNumberInput = phoneForm.elements.namedItem("phone_number");
      if (countrySelect) {
        if (![...countrySelect.options].some((option) => option.value === phone.countryCode)) {
          countrySelect.add(new Option(`${phone.countryCode} 其他`, phone.countryCode));
        }
        countrySelect.value = phone.countryCode;
      }
      if (localNumberInput) {
        localNumberInput.value = phone.localNumber;
      }
    }
    if (relogin && usernameForm?.elements.namedItem("username") && account.username) {
      usernameForm.elements.namedItem("username").value = `@${account.username}`;
    }
    if (typeof authDialog.showModal === "function") {
      authDialog.showModal();
    } else {
      authDialog.setAttribute("open", "");
    }
    if (telegramConfigured) {
      startQRAuth();
    }
  };

  document.querySelectorAll("[data-open-login]").forEach((button) => {
    button.addEventListener("click", () => openLoginDialog());
  });
  document.querySelectorAll("[data-open-relogin]").forEach((button) => {
    button.addEventListener("click", (event) => {
      event.preventDefault();
      const candidates = allAccounts.filter((account) => !isDeletedAccount(account));
      const unavailable = candidates.filter(
        (account) => accountStatus(account).key !== "online",
      );
      if (unavailable.length === 1) {
        openLoginDialog(unavailable[0]);
        return;
      }
      if (candidates.length === 1) {
        openLoginDialog(candidates[0]);
        return;
      }
      if (candidates.length > 1) {
        showNotice(accountsError, unavailable.length > 0
          ? "请在账号列表中点击对应账号的“重新登录”；带红色标识的账号需要优先处理。"
          : "请在账号列表中点击需要重新授权的账号的“重新登录”。");
        accountSearch?.focus();
        return;
      }
      showNotice(accountsError, "还没有可重新登录的 Telegram 账号，请先添加账号。", true);
      accountSearch?.focus();
    });
  });
  document.querySelectorAll("[data-account-relogin-close]").forEach((button) => {
    button.addEventListener("click", () => {
      reloginForm?.reset();
      reloginDialog?.close();
    });
  });
  reloginForm?.addEventListener("submit", (event) => {
    event.preventDefault();
    const phoneNumber = String(new FormData(reloginForm).get("phone_number") || "")
      .replace(/[^\d+]/g, "");
    if (!/^\+\d{8,15}$/.test(phoneNumber)) {
      showNotice(reloginNotice, "请输入包含国家区号的完整手机号。", true);
      return;
    }
    const existing = allAccounts.find(
      (account) => `+${String(account.phone_number || "").replace(/\D/g, "")}` === phoneNumber,
    );
    reloginForm.reset();
    reloginDialog?.close();
    openLoginDialog(existing || {phone_number: phoneNumber});
  });
  loginModeButtons.forEach((button) => {
    button.addEventListener("click", () => setAuthMode(button.dataset.loginMode));
  });
  qrRestart?.addEventListener("click", () => {
    setAuthMode("qr");
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

  usernameForm?.addEventListener("submit", (event) => {
    event.preventDefault();
    const username = String(new FormData(usernameForm).get("username") || "")
      .trim()
      .replace(/^@/, "");
    if (!/^[A-Za-z0-9_]{5,32}$/.test(username)) {
      showNotice(usernameNotice, "请输入 5 到 32 位 Telegram 用户名。", true);
      return;
    }
    const message = `请使用 @${username} 对应账号扫描二维码完成授权。`;
    showNotice(usernameNotice, message);
    setAuthMode("qr");
    showNotice(qrNotice, message);
  });

  phoneForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = phoneForm.querySelector("button[type='submit']");
    const formData = new FormData(phoneForm);
    const countryCode = String(formData.get("country_code") || "").trim();
    const localNumber = String(formData.get("phone_number") || "").replace(/\D/g, "");
    const phoneNumber = `${countryCode}${localNumber}`;
    showNotice(phoneNotice, "");
    if (!/^\+\d{7,15}$/.test(phoneNumber)) {
      showNotice(phoneNotice, "请输入有效的国家区号和完整手机号。", true);
      return;
    }
    if (button) {
      button.disabled = true;
    }
    try {
      const challenge = await requestJSON("/api/v1/telegram/auth/send-code", {
        method: "POST",
        body: JSON.stringify({
          phone_number: phoneNumber,
          account_id: reloginAccountID,
        }),
      });
      loginID = challenge.login_id;
      if (phoneHint) {
        phoneHint.textContent = challenge.phone_hint
          ? formatPhone(challenge.phone_hint)
          : "Telegram 账号";
      }
      if (codeType) {
        codeType.textContent = challenge.code_type || "Telegram 验证码";
      }
      if (codeInput) {
        codeInput.maxLength = challenge.code_length > 0 ? challenge.code_length : 8;
        codeInput.value = "";
      }
      setAuthStep("code");
      codeInput?.focus();
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
      stopQRPolling();
      setAuthStep("password");
      passwordForm?.querySelector("input[name='password']")?.focus();
      return;
    }
    if (result.next_step === "complete") {
      stopQRPolling();
      stopQRCountdown();
      authCompleted = true;
      loginID = "";
      qrLoginID = "";
      if (completeName) {
        completeName.textContent = result.account?.display_name || result.account?.username || "Telegram 账号";
      }
      if (viewChatButton && result.account?.id) {
        viewChatButton.href = `/accounts/${encodeURIComponent(result.account.id)}/web`;
        viewChatButton.target = "_blank";
        viewChatButton.rel = "noopener";
        viewChatButton.removeAttribute("hidden");
      }
      if (viewWebButton && result.account?.id) {
        viewWebButton.href = `/accounts/${encodeURIComponent(result.account.id)}/web`;
        viewWebButton.removeAttribute("hidden");
      }
      setAuthStep("complete");
      loadAccounts();
    }
  };

  codeForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = codeForm.querySelector("button[type='submit']");
    const formData = new FormData(codeForm);
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

  authDialog?.addEventListener("close", () => {
    if (!authCompleted) {
      cancelLogin();
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
    const offlineState = detailRoot.parentElement.querySelector("[data-account-offline]");
    const offlineCopy = detailRoot.parentElement.querySelector("[data-account-offline-copy]");
    const chatWorkspace = detailRoot.parentElement.querySelector(".chat-workspace");
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
        const avatarIcon = document.createElement("span");
        avatarIcon.className = "icon icon-message-square-text";
        avatarIcon.setAttribute("aria-hidden", "true");
        avatar.append(avatarIcon);
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
        if (items.length > 0) {
          chatList?.querySelector(".chat-list-item")?.classList.add("active");
          loadMessages(items[0]);
        }
      } catch (error) {
        showNotice(chatsError, error.message, true);
      } finally {
        chatsLoading?.setAttribute("hidden", "");
        chatsRefresh?.removeAttribute("disabled");
      }
    };

    const initializeDetail = async () => {
      try {
        const account = await requestJSON(`/api/v1/accounts/${encodeURIComponent(accountID)}`);
        if (detailName) {
          detailName.textContent = account.display_name || account.username || "Telegram 账号";
        }
        if (detailMeta) {
          detailMeta.textContent = `${account.username ? `@${account.username} · ` : ""}${formatPhone(account.phone_number)} · ${statusLabels[account.status] || account.status}`;
        }
        const online = account.status === "online";
        offlineState?.toggleAttribute("hidden", online);
        chatWorkspace?.toggleAttribute("hidden", !online);
        if (!online) {
          chatsRefresh?.setAttribute("disabled", "");
          if (offlineCopy) {
            offlineCopy.textContent = "该账号的 Telegram 授权已失效。请返回账号列表重新扫码登录，登录成功后再查看会话。";
          }
          return;
        }
        await loadChats();
      } catch (error) {
        if (detailMeta) {
          detailMeta.textContent = error.message;
        }
        offlineState?.removeAttribute("hidden");
        chatWorkspace?.setAttribute("hidden", "");
        if (offlineCopy) {
          offlineCopy.textContent = error.message;
        }
      }
    };
    initializeDetail();
    chatsRefresh?.addEventListener("click", loadChats);
    loadOlder?.addEventListener("click", () => loadMessages(selectedChat, false));
  }

  const settingsForm = document.querySelector("[data-settings-form]");
  if (settingsForm) {
    const settingsNotice = document.querySelector("[data-settings-notice]");
    const settingsState = document.querySelector("[data-settings-state]");
    const apiHashStates = [...document.querySelectorAll("[data-api-hash-state]")];
    const restartNotes = [...document.querySelectorAll("[data-restart-note]")];
    const environmentOverrides = document.querySelector("[data-environment-overrides]");
    const telegramLoginState = document.querySelector("[data-telegram-login-state]");
    const settingsTabs = [...document.querySelectorAll("[data-settings-tab]")];
    const settingsPanels = [...document.querySelectorAll("[data-settings-panel]")];
    const settingsActions = document.querySelector("[data-settings-actions]");
    const healthStatus = document.querySelector("[data-health-status]");
    const healthChecked = document.querySelector("[data-health-checked]");
    const healthChecks = document.querySelector("[data-health-checks]");
    const healthRefresh = document.querySelector("[data-health-refresh]");
    const authUsersState = document.querySelector("[data-auth-users-state]");
    const authUsersNotice = document.querySelector("[data-auth-users-notice]");
    const authUsersBody = document.querySelector("[data-auth-users-body]");
    const authUserRefresh = document.querySelector("[data-auth-user-refresh]");
    const authUserCreate = document.querySelector("[data-auth-user-create]");
    const readOnlySettingsTabs = new Set(["page", "display", "device", "security", "status"]);

    const activateSettingsTab = (name, updateURL = false) => {
      settingsTabs.forEach((button) => {
        const active = button.dataset.settingsTab === name;
        button.classList.toggle("active", active);
        button.setAttribute("aria-selected", String(active));
        button.tabIndex = active ? 0 : -1;
      });
      settingsPanels.forEach((panel) => {
        panel.hidden = panel.dataset.settingsPanel !== name;
      });
      settingsActions?.toggleAttribute("hidden", readOnlySettingsTabs.has(name));
      if (updateURL) {
        const url = new URL(window.location.href);
        if (name === "basic") {
          url.searchParams.delete("tab");
        } else {
          url.searchParams.set("tab", name);
        }
        window.history.replaceState({}, "", url);
      }
    };

    settingsTabs.forEach((button) => {
      button.addEventListener("click", () => {
        activateSettingsTab(button.dataset.settingsTab || "basic", true);
      });
    });
    settingsTabs.forEach((button, index) => {
      button.addEventListener("keydown", (event) => {
        let nextIndex = index;
        if (event.key === "ArrowRight") {
          nextIndex = (index + 1) % settingsTabs.length;
        } else if (event.key === "ArrowLeft") {
          nextIndex = (index - 1 + settingsTabs.length) % settingsTabs.length;
        } else if (event.key === "Home") {
          nextIndex = 0;
        } else if (event.key === "End") {
          nextIndex = settingsTabs.length - 1;
        } else {
          return;
        }
        event.preventDefault();
        const next = settingsTabs[nextIndex];
        activateSettingsTab(next.dataset.settingsTab || "basic", true);
        next.focus();
      });
    });
    const requestedTab = new URLSearchParams(window.location.search).get("tab");
    const initialTab = settingsTabs.some((button) => button.dataset.settingsTab === requestedTab)
      ? requestedTab
      : (settingsTabs.find((button) => button.classList.contains("active"))
        ?.dataset.settingsTab || "basic");
    activateSettingsTab(initialTab);

    const renderSettings = (settings) => {
      const setValue = (name, value) => {
        const input = settingsForm.elements.namedItem(name);
        if (input) {
          input.value = value;
        }
      };
      setValue("app_env", settings.app_env || "");
      setValue("log_level", settings.log_level || "info");
      setValue("telegram_api_id", settings.telegram_api_id || "");
      setValue("telegram_api_hash", "");
      setValue("http_addr", settings.http_addr || "");
      apiHashStates.forEach((apiHashState) => {
        apiHashState.textContent = settings.telegram_api_hash_configured
          ? (settings.telegram_api_hash_hint || "已配置")
          : "尚未配置";
      });
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
      if (telegramLoginState) {
        telegramLoginState.textContent = settings.telegram_api_hash_configured
          ? "登录凭据已配置"
          : "待配置";
      }
      restartNotes.forEach((restartNote) => {
        restartNote.textContent = settings.restart_required
          ? "设置已保存，重启服务后生效。"
          : "当前配置已生效。";
      });
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

    const healthLabels = {
      sqlite: "SQLite 数据库",
      configuration: "运行配置",
      telegram_sessions: "Telegram 会话目录",
      telegram_credentials: "Telegram API 凭据",
      administrator: "管理员鉴权",
    };

    const roleLabels = {
      administrator: "管理员",
      operator: "操作员",
      viewer: "只读查看者",
    };

    const renderAuthUsers = (payload) => {
      const items = Array.isArray(payload?.items) ? payload.items : [];
      const canManage = Array.isArray(payload?.permissions) &&
        payload.permissions.includes("user:write");
      authUserCreate?.toggleAttribute("hidden", !canManage);
      if (authUsersState) {
        authUsersState.textContent = `${items.length} 个本地用户`;
        authUsersState.className = "badge badge-neutral";
      }
      if (!authUsersBody) {
        return;
      }
      authUsersBody.replaceChildren();
      items.forEach((user) => {
        const row = document.createElement("tr");
        row.className = "auth-user-row";
        const username = document.createElement("td");
        const name = document.createElement("strong");
        name.textContent = user.username || "";
        username.append(name);
        if (user.username === payload.current_user) {
          const current = document.createElement("span");
          current.className = "badge badge-blue";
          current.textContent = "当前用户";
          username.append(" ", current);
        }
        const role = document.createElement("td");
        const roleSelect = document.createElement("select");
        roleSelect.dataset.authUserRole = "";
        Object.entries(roleLabels).forEach(([value, label]) => {
          const option = document.createElement("option");
          option.value = value;
          option.textContent = label;
          option.selected = value === user.role;
          roleSelect.append(option);
        });
        roleSelect.disabled = !canManage;
        role.append(roleSelect);
        const actions = document.createElement("td");
        const wrap = document.createElement("div");
        wrap.className = "auth-user-actions";
        const status = document.createElement("button");
        status.type = "button";
        status.className = "text-button";
        status.dataset.authUserStatus = "";
        status.textContent = user.disabled ? "已停用" : "启用中";
        status.setAttribute("aria-pressed", String(!user.disabled));
        status.disabled = !canManage;
        const save = document.createElement("button");
        save.type = "button";
        save.className = "text-button";
        save.dataset.authUserSave = "";
        save.textContent = "保存";
        save.disabled = !canManage;
        const reset = document.createElement("button");
        reset.type = "button";
        reset.className = "text-button";
        reset.dataset.authUserPassword = "";
        reset.textContent = "重置密码";
        reset.disabled = !canManage;
        const password = document.createElement("input");
        password.type = "password";
        password.autocomplete = "new-password";
        password.placeholder = "12 位以上新密码";
        password.hidden = true;
        password.dataset.authUserPasswordInput = "";
        const passwordSave = document.createElement("button");
        passwordSave.type = "button";
        passwordSave.className = "text-button";
        passwordSave.dataset.authUserPasswordSave = "";
        passwordSave.textContent = "确认重置";
        passwordSave.hidden = true;
        wrap.append(status, save, reset, password, passwordSave);
        actions.append(wrap);
        row.append(username, role, actions);
        if (canManage) {
          save.addEventListener("click", async () => {
            save.disabled = true;
            try {
              await requestJSON(`/api/v1/auth/users/${encodeURIComponent(user.username)}`, {
                method: "PATCH",
                body: JSON.stringify({
                  role: roleSelect.value,
                }),
              });
              showNotice(authUsersNotice, "用户角色已保存");
              await loadAuthUsers();
            } catch (error) {
              showNotice(authUsersNotice, error.message, true);
            } finally {
              save.disabled = false;
            }
          });
          status.addEventListener("click", async () => {
            status.disabled = true;
            try {
              await requestJSON(`/api/v1/auth/users/${encodeURIComponent(user.username)}`, {
                method: "PATCH",
                body: JSON.stringify({disabled: !user.disabled}),
              });
              showNotice(authUsersNotice, user.disabled ? "用户已启用" : "用户已停用");
              await loadAuthUsers();
            } catch (error) {
              showNotice(authUsersNotice, error.message, true);
            } finally {
              status.disabled = false;
            }
          });
          reset.addEventListener("click", () => {
            password.hidden = false;
            passwordSave.hidden = false;
            password.focus();
          });
          passwordSave.addEventListener("click", async () => {
            const value = password.value;
            if (value.length < 12 || value.length > 128) {
              showNotice(authUsersNotice, "密码需为 12 到 128 个字符", true);
              return;
            }
            passwordSave.disabled = true;
            try {
              await requestJSON(`/api/v1/auth/users/${encodeURIComponent(user.username)}`, {
                method: "PATCH",
                body: JSON.stringify({password: value}),
              });
              password.value = "";
              password.hidden = true;
              passwordSave.hidden = true;
              showNotice(authUsersNotice, "密码已更新，相关旧会话已失效");
            } catch (error) {
              showNotice(authUsersNotice, error.message, true);
            } finally {
              passwordSave.disabled = false;
            }
          });
        }
        authUsersBody.append(row);
      });
      if (!items.length) {
        const row = document.createElement("tr");
        const cell = document.createElement("td");
        cell.colSpan = 3;
        cell.textContent = "暂无本地用户";
        row.append(cell);
        authUsersBody.append(row);
      }
    };

    const loadAuthUsers = async () => {
      authUserRefresh?.setAttribute("disabled", "");
      try {
        renderAuthUsers(await requestJSON("/api/v1/auth/users"));
      } catch (error) {
        if (authUsersState) {
          authUsersState.textContent = "读取失败";
          authUsersState.className = "badge badge-red";
        }
        showNotice(authUsersNotice, error.message, true);
      } finally {
        authUserRefresh?.removeAttribute("disabled");
      }
    };

    authUserRefresh?.addEventListener("click", loadAuthUsers);
    authUserCreate?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const data = new FormData(authUserCreate);
      const submit = authUserCreate.querySelector("button[type='submit']");
      submit?.setAttribute("disabled", "");
      try {
        await requestJSON("/api/v1/auth/users", {
          method: "POST",
          body: JSON.stringify({
            username: String(data.get("username") || "").trim(),
            password: String(data.get("password") || ""),
            role: String(data.get("role") || "viewer"),
          }),
        });
        authUserCreate.reset();
        showNotice(authUsersNotice, "用户已创建");
        await loadAuthUsers();
      } catch (error) {
        showNotice(authUsersNotice, error.message, true);
      } finally {
        submit?.removeAttribute("disabled");
      }
    });

    const loadHealth = async () => {
      healthRefresh?.setAttribute("disabled", "");
      if (healthStatus) {
        healthStatus.textContent = "检查中";
        healthStatus.className = "badge badge-neutral";
      }
      try {
        const report = await requestJSON("/api/v1/health");
        const status = String(report?.status || "error");
        if (healthStatus) {
          healthStatus.textContent = status === "ok"
            ? "正常"
            : (status === "degraded" ? "部分可用" : "异常");
          healthStatus.className = `badge ${
            status === "ok" ? "badge-blue" : (status === "degraded" ? "badge-neutral" : "badge-red")
          }`;
        }
        if (healthChecked) {
          healthChecked.textContent = report?.checked_at
            ? formatAccountDate(report.checked_at)
            : "—";
        }
        if (healthChecks) {
          healthChecks.replaceChildren();
          const items = Array.isArray(report?.checks) ? report.checks : [];
          items.forEach((check) => {
            const item = document.createElement("li");
            item.className = `health-check health-check-${check.status || "error"}`;
            const name = document.createElement("strong");
            name.textContent = healthLabels[check.name] || check.name || "未知检查";
            const message = document.createElement("span");
            message.textContent = check.message || "";
            item.append(name, message);
            healthChecks.append(item);
          });
        }
      } catch (error) {
        if (healthStatus) {
          healthStatus.textContent = "检查失败";
          healthStatus.className = "badge badge-red";
        }
        if (healthChecks) {
          const item = document.createElement("li");
          item.className = "health-check health-check-error";
          item.textContent = error.message;
          healthChecks.replaceChildren(item);
        }
      } finally {
        healthRefresh?.removeAttribute("disabled");
      }
    };

    healthRefresh?.addEventListener("click", loadHealth);

    settingsForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = settingsForm.querySelector("button[type='submit']");
      const data = new FormData(settingsForm);
      const payload = {
        app_env: String(data.get("app_env") || "").trim(),
        log_level: String(data.get("log_level") || "").trim(),
        http_addr: String(data.get("http_addr") || "").trim(),
        telegram_api_id: Number(data.get("telegram_api_id") || 0),
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
        showNotice(
          settingsNotice,
          saved.restart_required
            ? "设置已保存。请重启服务使配置生效。"
            : "设置已保存。",
        );
      } catch (error) {
        showNotice(settingsNotice, error.message, true);
      } finally {
        button?.removeAttribute("disabled");
      }
    });
    loadSettings();
    loadHealth();
    loadAuthUsers();
  }
})();
