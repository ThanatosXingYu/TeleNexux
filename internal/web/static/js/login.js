(() => {
  const form = document.querySelector("[data-login-form]");
  if (!form) {
    return;
  }

  const heading = document.querySelector("[data-login-heading]");
  const description = document.querySelector("[data-login-description]");
  const setupHint = document.querySelector("[data-setup-hint]");
  const notice = document.querySelector("[data-login-notice]");
  const submit = form.querySelector("button[type='submit']");
  let setupRequired = false;

  const showNotice = (message, isError = true) => {
    if (!notice) {
      return;
    }
    notice.textContent = message;
    notice.hidden = !message;
    notice.classList.toggle("form-notice-error", isError);
  };

  const setMode = (setup) => {
    setupRequired = setup;
    if (heading) {
      heading.textContent = setup ? "初始化管理员" : "登录 TeleNexus";
    }
    if (description) {
      description.textContent = setup
        ? "首次使用仅允许从本机设置管理员账号和密码。"
        : "使用本机管理员账号进入多账号控制台。";
    }
    if (setupHint) {
      setupHint.hidden = !setup;
    }
    form.elements.namedItem("password")?.setAttribute(
      "autocomplete",
      setup ? "new-password" : "current-password",
    );
    if (submit) {
      submit.textContent = setup ? "创建管理员并登录" : "登录";
      submit.disabled = false;
    }
  };

  const requestJSON = async (path, options = {}) => {
    const response = await fetch(path, {
      ...options,
      headers: {
        "Accept": "application/json",
        "Content-Type": "application/json",
        ...(options.headers || {}),
      },
    });
    const text = await response.text();
    let payload = null;
    if (text) {
      try {
        payload = JSON.parse(text);
      } catch {
        payload = null;
      }
    }
    if (!response.ok) {
      throw new Error(payload?.error?.message || `请求失败（${response.status}）`);
    }
    return payload;
  };

  fetch("/api/v1/auth/session", {headers: {"Accept": "application/json"}})
    .then((response) => {
      if (!response.ok) {
        throw new Error(`请求失败（${response.status}）`);
      }
      return response.json();
    })
    .then((session) => {
      if (session?.authenticated) {
        window.location.replace("/");
        return;
      }
      setMode(!session?.configured);
    })
    .catch(() => {
      if (heading) {
        heading.textContent = "无法读取登录状态";
      }
      if (description) {
        description.textContent = "请确认 Go 服务正在运行后刷新页面。";
      }
      showNotice("登录状态检查失败");
    });

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const data = new FormData(form);
    const username = String(data.get("username") || "").trim();
    const password = String(data.get("password") || "");
    submit?.setAttribute("disabled", "");
    showNotice("");
    try {
      await requestJSON(
        setupRequired ? "/api/v1/auth/setup" : "/api/v1/auth/login",
        {
          method: "POST",
          body: JSON.stringify({username, password}),
        },
      );
      window.location.replace("/");
    } catch (error) {
      showNotice(error.message || "登录失败");
      form.elements.namedItem("password")?.focus();
    } finally {
      submit?.removeAttribute("disabled");
    }
  });
})();
