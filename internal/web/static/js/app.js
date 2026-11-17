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
