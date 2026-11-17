(() => {
  const root = document.querySelector("[data-telegram-client]");
  if (!root) {
    return;
  }

  const accountID = Number(root.dataset.accountId);
  if (!Number.isSafeInteger(accountID) || accountID <= 0) {
    return;
  }

  const elements = {
    chatList: root.querySelector("[data-chat-list]"),
    chatsLoading: root.querySelector("[data-chats-loading]"),
    chatsEmpty: root.querySelector("[data-chats-empty]"),
    chatsEmptyTitle: root.querySelector("[data-chats-empty-title]"),
    chatsEmptyCopy: root.querySelector("[data-chats-empty-copy]"),
    chatsError: root.querySelector("[data-chats-error]"),
    search: root.querySelector("[data-chat-search]"),
    clearSearch: root.querySelector("[data-clear-search]"),
    refreshChats: root.querySelector("[data-refresh-chats]"),
    folderCountAll: root.querySelector('[data-folder-count="all"]'),
    folderCountUnread: root.querySelector('[data-folder-count="unread"]'),
    accountAvatar: root.querySelector("[data-account-avatar]"),
    accountName: root.querySelector("[data-account-name]"),
    accountMeta: root.querySelector("[data-account-meta]"),
    accountStatusDot: root.querySelector("[data-account-status-dot]"),
    threadEmpty: root.querySelector("[data-thread-empty]"),
    conversation: root.querySelector("[data-conversation]"),
    conversationAvatar: root.querySelector("[data-conversation-avatar]"),
    conversationTitle: root.querySelector("[data-conversation-title]"),
    conversationMeta: root.querySelector("[data-conversation-meta]"),
    stickyDate: root.querySelector("[data-sticky-date]"),
    stickyDateLabel: root.querySelector("[data-sticky-date-label]"),
    messageScroll: root.querySelector("[data-message-scroll]"),
    messageList: root.querySelector("[data-message-list]"),
    messagesLoading: root.querySelector("[data-messages-loading]"),
    messagesError: root.querySelector("[data-messages-error]"),
    loadOlder: root.querySelector("[data-load-older]"),
    profilePanel: root.querySelector("[data-profile-panel]"),
    profileAvatar: root.querySelector("[data-profile-avatar]"),
    profileTitle: root.querySelector("[data-profile-title]"),
    profileUsername: root.querySelector("[data-profile-username]"),
    profileType: root.querySelector("[data-profile-type]"),
    profileUnread: root.querySelector("[data-profile-unread]"),
    profileAccount: root.querySelector("[data-profile-account]"),
    toast: root.querySelector("[data-toast]"),
  };

  const state = {
    account: null,
    chats: [],
    filteredChats: [],
    selectedChat: null,
    messages: [],
    nextBeforeID: 0,
    activeFolder: "all",
    query: "",
    toastTimer: 0,
    messagesRequest: 0,
    dateFrame: 0,
  };

  const typeLabels = {
    user: "私聊",
    chat: "群组",
    channel: "频道",
  };

  const statusLabels = {
    online: "在线",
    pending: "待验证",
    paused: "已暂停",
    error: "异常",
  };

  const requestJSON = async (path, options = {}) => {
    const headers = new Headers(options.headers || {});
    headers.set("Accept", "application/json");
    const controller = new AbortController();
    const timeout = window.setTimeout(() => controller.abort(), 35000);
    try {
      const response = await fetch(path, {...options, headers, signal: controller.signal});
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
        const error = new Error(payload?.error?.message || `请求失败（${response.status}）`);
        error.code = payload?.error?.code || "request_failed";
        error.status = response.status;
        throw error;
      }
      return payload;
    } catch (error) {
      if (error.name === "AbortError") {
        throw new Error("请求超时，请稍后重试");
      }
      throw error;
    } finally {
      window.clearTimeout(timeout);
    }
  };

  const showNotice = (element, message) => {
    if (!element) {
      return;
    }
    element.textContent = message || "";
    element.hidden = !message;
  };

  const showToast = (message) => {
    if (!elements.toast) {
      return;
    }
    window.clearTimeout(state.toastTimer);
    elements.toast.textContent = message;
    elements.toast.hidden = false;
    state.toastTimer = window.setTimeout(() => {
      elements.toast.hidden = true;
    }, 2800);
  };

  const maskPhone = (phone) => {
    const digits = String(phone || "").replace(/\D/g, "");
    return digits.length > 4 ? `••••••${digits.slice(-4)}` : "••••";
  };

  const stableColorIndex = (value) => {
    let hash = 0;
    for (const char of String(value || "Telegram")) {
      hash = (hash * 31 + char.codePointAt(0)) >>> 0;
    }
    return hash % 8;
  };

  const initials = (title) => {
    const words = String(title || "Telegram").trim().split(/\s+/).filter(Boolean);
    if (words.length > 1) {
      return `${Array.from(words[0])[0] || ""}${Array.from(words[1])[0] || ""}`;
    }
    return Array.from(words[0] || "TG").slice(0, 2).join("");
  };

  const applyAvatar = (element, title, extraClass = "") => {
    if (!element) {
      return;
    }
    element.className = `tg-avatar ${extraClass} tg-avatar-color-${stableColorIndex(title)}`.replace(/\s+/g, " ").trim();
    element.textContent = initials(title);
  };

  const parseDate = (value) => {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? null : date;
  };

  const startOfDay = (date) => new Date(date.getFullYear(), date.getMonth(), date.getDate());

  const formatListTime = (value) => {
    const date = parseDate(value);
    if (!date) {
      return "";
    }
    const now = new Date();
    const day = startOfDay(date);
    const today = startOfDay(now);
    const difference = Math.round((today - day) / 86400000);
    if (difference === 0) {
      return new Intl.DateTimeFormat("zh-CN", {hour: "2-digit", minute: "2-digit", hour12: false}).format(date);
    }
    if (difference === 1) {
      return "昨天";
    }
    if (difference > 1 && difference < 7) {
      return new Intl.DateTimeFormat("zh-CN", {weekday: "short"}).format(date);
    }
    return new Intl.DateTimeFormat("zh-CN", {year: "2-digit", month: "2-digit", day: "2-digit"}).format(date);
  };

  const formatMessageTime = (value) => {
    const date = parseDate(value);
    if (!date) {
      return "";
    }
    return new Intl.DateTimeFormat("zh-CN", {hour: "2-digit", minute: "2-digit", hour12: false}).format(date);
  };

  const formatDateDivider = (value) => {
    const date = parseDate(value);
    if (!date) {
      return "未知日期";
    }
    const now = new Date();
    const difference = Math.round((startOfDay(now) - startOfDay(date)) / 86400000);
    if (difference === 0) {
      return "今天";
    }
    if (difference === 1) {
      return "昨天";
    }
    return new Intl.DateTimeFormat("zh-CN", {
      year: date.getFullYear() === now.getFullYear() ? undefined : "numeric",
      month: "long",
      day: "numeric",
    }).format(date);
  };

  const dateKey = (value) => {
    const date = parseDate(value);
    if (!date) {
      return "unknown";
    }
    return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
  };

  const mediaLabel = (type) => {
    if (!type) {
      return "";
    }
    const labels = {
      photo: "照片",
      video: "视频",
      audio: "音频",
      document: "文件",
      sticker: "贴纸",
      gif: "GIF",
      voice: "语音",
    };
    return labels[type] || type;
  };

  const chatMatchesFolder = (chat) => {
    if (state.activeFolder === "unread") {
      return Number(chat.unread_count) > 0;
    }
    if (state.activeFolder === "private") {
      return chat.type === "user";
    }
    if (state.activeFolder === "groups") {
      return chat.type === "chat" || chat.type === "channel";
    }
    return true;
  };

  const chatMatchesQuery = (chat) => {
    if (!state.query) {
      return true;
    }
    const haystack = [chat.title, chat.username, chat.preview].filter(Boolean).join(" ").toLocaleLowerCase();
    return haystack.includes(state.query);
  };

  const updateFolderCounts = () => {
    const unreadChats = state.chats.filter((chat) => Number(chat.unread_count) > 0).length;
    [
      [elements.folderCountAll, unreadChats],
      [elements.folderCountUnread, unreadChats],
    ].forEach(([element, count]) => {
      if (!element) {
        return;
      }
      element.textContent = String(count);
      element.hidden = count === 0;
    });
  };

  const updateSelectedChatRow = () => {
    elements.chatList?.querySelectorAll(".tg-chat-item").forEach((item) => {
      const selected = item.dataset.chatId === state.selectedChat?.id;
      item.classList.toggle("tg-chat-item-selected", selected);
      item.setAttribute("aria-current", selected ? "true" : "false");
    });
  };

  const makeChatRow = (chat) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "tg-chat-item";
    button.dataset.chatId = chat.id || `${chat.type}:${chat.peer_id}`;
    button.setAttribute("aria-label", `打开与 ${chat.title || "未命名会话"} 的聊天`);

    const avatar = document.createElement("span");
    avatar.setAttribute("aria-hidden", "true");
    applyAvatar(avatar, chat.title || chat.username);

    const content = document.createElement("span");
    content.className = "tg-chat-content";
    const top = document.createElement("span");
    top.className = "tg-chat-top";
    const title = document.createElement("strong");
    title.className = "tg-chat-title";
    title.textContent = chat.title || "未命名会话";
    const time = document.createElement("time");
    time.className = "tg-chat-time";
    time.dateTime = chat.last_message_at || "";
    time.textContent = formatListTime(chat.last_message_at);
    const timeDetails = document.createElement("span");
    timeDetails.className = "tg-chat-time-details";
    if (chat.pinned) {
      timeDetails.classList.add("is-pinned");
      const pin = document.createElement("span");
      pin.className = "icon icon-pin tg-pin";
      pin.setAttribute("aria-label", "已置顶");
      timeDetails.append(pin);
    }
    timeDetails.append(time);
    top.append(title, timeDetails);

    const bottom = document.createElement("span");
    bottom.className = "tg-chat-bottom";
    const preview = document.createElement("span");
    preview.className = "tg-chat-preview";
    preview.textContent = chat.preview || (chat.username ? `@${chat.username}` : "暂无消息");
    const badges = document.createElement("span");
    badges.className = "tg-chat-badges";
    const unreadCount = Number(chat.unread_count) || 0;
    if (unreadCount > 0) {
      const badge = document.createElement("span");
      badge.className = "tg-unread-badge";
      badge.textContent = unreadCount > 99 ? "99+" : String(unreadCount);
      badge.setAttribute("aria-label", `${unreadCount} 条未读消息`);
      badges.append(badge);
    }
    bottom.append(preview, badges);
    content.append(top, bottom);
    button.append(avatar, content);
    button.addEventListener("click", () => selectChat(chat));
    return button;
  };

  const renderChats = () => {
    if (!elements.chatList) {
      return;
    }
    state.filteredChats = state.chats.filter((chat) => chatMatchesFolder(chat) && chatMatchesQuery(chat));
    elements.chatList.replaceChildren(...state.filteredChats.map(makeChatRow));
    updateSelectedChatRow();

    const hasChats = state.filteredChats.length > 0;
    elements.chatList.toggleAttribute("hidden", !hasChats);
    elements.chatsEmpty?.toggleAttribute("hidden", hasChats || Boolean(elements.chatsError && !elements.chatsError.hidden));
    if (!hasChats && elements.chatsEmptyTitle && elements.chatsEmptyCopy) {
      if (state.query) {
        elements.chatsEmptyTitle.textContent = "没有搜索结果";
        elements.chatsEmptyCopy.textContent = "尝试其他关键词或清空搜索。";
      } else {
        elements.chatsEmptyTitle.textContent = state.activeFolder === "all" ? "暂无会话" : "这个分类暂无会话";
        elements.chatsEmptyCopy.textContent = "Telegram 中没有符合当前筛选条件的聊天。";
      }
    }
  };

  const updateProfile = (chat) => {
    if (!chat) {
      return;
    }
    applyAvatar(elements.profileAvatar, chat.title || chat.username, "tg-avatar-profile");
    if (elements.profileTitle) {
      elements.profileTitle.textContent = chat.title || "未命名会话";
    }
    if (elements.profileUsername) {
      elements.profileUsername.textContent = chat.username ? `@${chat.username}` : typeLabels[chat.type] || "Telegram";
    }
    if (elements.profileType) {
      elements.profileType.textContent = typeLabels[chat.type] || "会话";
    }
    if (elements.profileUnread) {
      elements.profileUnread.textContent = String(Number(chat.unread_count) || 0);
    }
    if (elements.profileAccount) {
      const accountName = state.account?.display_name || state.account?.username || "Telegram 账号";
      elements.profileAccount.textContent = `${accountName} · ${maskPhone(state.account?.phone_number)}`;
    }
  };

  const selectChat = (chat) => {
    state.selectedChat = chat;
    state.messages = [];
    state.nextBeforeID = 0;
    state.messagesRequest += 1;
    root.dataset.mobileView = "chat";

    elements.threadEmpty?.setAttribute("hidden", "");
    elements.conversation?.removeAttribute("hidden");
    applyAvatar(elements.conversationAvatar, chat.title || chat.username, "tg-avatar-medium");
    if (elements.conversationTitle) {
      elements.conversationTitle.textContent = chat.title || "未命名会话";
    }
    if (elements.conversationMeta) {
      elements.conversationMeta.textContent = chat.username
        ? `@${chat.username}`
        : (typeLabels[chat.type] || "Telegram");
    }
    updateProfile(chat);
    updateSelectedChatRow();
    loadMessages(true);
  };

  const sameMessageGroup = (left, right) => {
    const leftDate = parseDate(left?.sent_at)?.getTime() || 0;
    const rightDate = parseDate(right?.sent_at)?.getTime() || 0;
    return Boolean(
      left &&
      right &&
      left.sender === right.sender &&
      left.outgoing === right.outgoing &&
      dateKey(left.sent_at) === dateKey(right.sent_at) &&
      Math.abs(rightDate - leftDate) < 5 * 60 * 1000,
    );
  };

  function createMessageRow(message, previous, next, chat) {
    const row = document.createElement("article");
    row.className = `tg-message-row${message.outgoing ? " is-outgoing" : ""}`;
    const consecutive = sameMessageGroup(previous, message);
    const nextConsecutive = sameMessageGroup(message, next);
    row.classList.toggle("is-consecutive", consecutive);
    row.classList.toggle("is-group-first", !consecutive);
    row.classList.toggle("is-group-last", !nextConsecutive);

    if (!message.outgoing && (chat.type === "chat" || chat.type === "channel")) {
      const avatar = document.createElement("span");
      avatar.className = `tg-avatar tg-message-avatar${consecutive ? " is-placeholder" : ""}`;
      applyAvatar(avatar, message.sender || chat.title);
      avatar.setAttribute("aria-hidden", "true");
      row.append(avatar);
    }

    const column = document.createElement("div");
    column.className = "tg-message-column";
    if (!message.outgoing && !consecutive && (chat.type === "chat" || chat.type === "channel")) {
      const author = document.createElement("span");
      author.className = "tg-message-author";
      author.textContent = message.sender || "未知用户";
      column.append(author);
    }

    const bubble = document.createElement("div");
    bubble.className = "tg-bubble";
    const text = document.createElement("p");
    text.className = "tg-message-text";
    text.textContent = message.text || mediaLabel(message.media_type) || "空消息";
    bubble.append(text);
    if (message.media_type && message.text) {
      const media = document.createElement("span");
      media.className = "tg-message-media";
      media.textContent = `附件 · ${mediaLabel(message.media_type)}`;
      bubble.append(media);
    }

    const meta = document.createElement("div");
    meta.className = "tg-message-meta";
    const time = document.createElement("time");
    time.dateTime = message.sent_at || "";
    time.textContent = formatMessageTime(message.sent_at);
    meta.append(time);
    if (message.outgoing) {
      const check = document.createElement("span");
      check.className = "icon icon-check-check tg-message-check";
      check.setAttribute("aria-label", "已发送");
      meta.append(check);
    }
    bubble.append(meta);
    column.append(bubble);
    row.append(column);
    return row;
  }

  const renderMessages = () => {
    if (!elements.messageList || !state.selectedChat) {
      return;
    }
    const nodes = [];
    let previous = null;
    let previousDateKey = "";
    state.messages.forEach((message, index) => {
      const currentDateKey = dateKey(message.sent_at);
      if (currentDateKey !== previousDateKey) {
        const divider = document.createElement("div");
        divider.className = "tg-date-divider";
        const label = document.createElement("span");
        label.textContent = formatDateDivider(message.sent_at);
        divider.dataset.dateLabel = label.textContent;
        divider.append(label);
        nodes.push(divider);
        previousDateKey = currentDateKey;
        previous = null;
      }
      nodes.push(createMessageRow(message, previous, state.messages[index + 1], state.selectedChat));
      previous = message;
    });
    if (nodes.length === 0) {
      const empty = document.createElement("p");
      empty.className = "tg-message-empty";
      empty.textContent = "这个聊天还没有可显示的消息。";
      nodes.push(empty);
    }
    elements.messageList.replaceChildren(...nodes);
    scheduleStickyDate();
  };

  const updateStickyDate = () => {
    state.dateFrame = 0;
    if (!elements.stickyDate || !elements.stickyDateLabel || !elements.messageScroll || !elements.messageList) {
      return;
    }
    const visibleTop = elements.messageScroll.getBoundingClientRect().top +
      parseFloat(getComputedStyle(elements.messageScroll).paddingTop || "0");
    let activeDivider = null;
    elements.messageList.querySelectorAll(".tg-date-divider").forEach((divider) => {
      if (divider.getBoundingClientRect().top <= visibleTop + 1) {
        activeDivider = divider;
      }
    });
    elements.stickyDate.hidden = !activeDivider;
    elements.stickyDateLabel.textContent = activeDivider?.dataset.dateLabel || "";
  };

  function scheduleStickyDate() {
    if (state.dateFrame) {
      return;
    }
    state.dateFrame = window.requestAnimationFrame(updateStickyDate);
  }

  const mergeMessages = (items) => {
    const byID = new Map(state.messages.map((message) => [message.id, message]));
    items.forEach((message) => {
      if (Number.isFinite(Number(message.id))) {
        byID.set(Number(message.id), message);
      }
    });
    state.messages = [...byID.values()].sort((left, right) => Number(left.id) - Number(right.id));
  };

  const scrollToBottom = () => {
    window.requestAnimationFrame(() => {
      if (elements.messageScroll) {
        elements.messageScroll.scrollTop = elements.messageScroll.scrollHeight;
      }
    });
  };

  async function loadMessages(reset) {
    const chat = state.selectedChat;
    if (!chat) {
      return;
    }
    const requestID = ++state.messagesRequest;
    const previousHeight = elements.messageScroll?.scrollHeight || 0;
    const previousTop = elements.messageScroll?.scrollTop || 0;
    elements.messagesLoading?.removeAttribute("hidden");
    elements.loadOlder?.setAttribute("disabled", "");
    showNotice(elements.messagesError, "");
    if (reset) {
      state.messages = [];
      state.nextBeforeID = 0;
      elements.messageList?.replaceChildren();
      elements.stickyDate?.setAttribute("hidden", "");
    }

    try {
      const params = new URLSearchParams({limit: "50"});
      if (!reset && state.nextBeforeID > 0) {
        params.set("before_id", String(state.nextBeforeID));
      }
      const payload = await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(accountID)}/chats/` +
          `${encodeURIComponent(chat.type)}/${encodeURIComponent(chat.peer_id)}/messages?${params}`,
      );
      if (requestID !== state.messagesRequest || state.selectedChat?.id !== chat.id) {
        return;
      }
      const items = Array.isArray(payload?.items) ? payload.items.slice().reverse() : [];
      mergeMessages(items);
      state.nextBeforeID = Number(payload?.next_before_id) || 0;
      renderMessages();
      elements.loadOlder?.toggleAttribute("hidden", state.nextBeforeID <= 0);
      if (reset) {
        scrollToBottom();
      } else if (elements.messageScroll) {
        elements.messageScroll.scrollTop = elements.messageScroll.scrollHeight - previousHeight + previousTop;
      }
      scheduleStickyDate();
    } catch (error) {
      if (requestID === state.messagesRequest) {
        showNotice(elements.messagesError, error.message);
        showToast(error.message);
      }
    } finally {
      if (requestID === state.messagesRequest) {
        elements.messagesLoading?.setAttribute("hidden", "");
        if (state.nextBeforeID > 0) {
          elements.loadOlder?.removeAttribute("disabled");
        }
      }
    }
  }

  const loadChats = async (notify = false) => {
    elements.chatsLoading?.removeAttribute("hidden");
    elements.chatsEmpty?.setAttribute("hidden", "");
    elements.refreshChats?.setAttribute("disabled", "");
    showNotice(elements.chatsError, "");
    try {
      const payload = await requestJSON(`/api/v1/accounts/${encodeURIComponent(accountID)}/chats?limit=100`);
      state.chats = Array.isArray(payload?.items) ? payload.items : [];
      updateFolderCounts();
      renderChats();
      if (state.chats.length > 0 && !state.selectedChat) {
        selectChat(state.chats[0]);
      } else if (state.selectedChat) {
        state.selectedChat = state.chats.find((chat) => chat.id === state.selectedChat.id) || state.selectedChat;
        updateSelectedChatRow();
      }
      if (notify) {
        showToast("会话已刷新");
      }
    } catch (error) {
      state.chats = [];
      state.filteredChats = [];
      updateFolderCounts();
      renderChats();
      elements.chatList?.setAttribute("hidden", "");
      elements.chatsEmpty?.setAttribute("hidden", "");
      if (error.code === "session_invalid") {
        elements.accountStatusDot?.classList.add("is-offline");
        if (elements.accountMeta) {
          elements.accountMeta.textContent = "Telegram 授权已失效";
        }
      }
      showNotice(elements.chatsError, error.message);
      showToast(error.message);
    } finally {
      elements.chatsLoading?.setAttribute("hidden", "");
      elements.refreshChats?.removeAttribute("disabled");
    }
  };

  const loadAccount = async () => {
    try {
      const account = await requestJSON(`/api/v1/accounts/${encodeURIComponent(accountID)}`);
      state.account = account;
      const name = account.display_name || account.username || "Telegram 账号";
      const online = account.status === "online";
      if (elements.accountName) {
        elements.accountName.textContent = name;
      }
      if (elements.accountMeta) {
        elements.accountMeta.textContent = [
          account.username ? `@${account.username}` : "",
          maskPhone(account.phone_number),
          statusLabels[account.status] || account.status,
        ].filter(Boolean).join(" · ");
      }
      applyAvatar(elements.accountAvatar, name, "tg-avatar-small");
      elements.accountStatusDot?.classList.toggle("is-offline", !online);
      if (elements.profileAccount) {
        elements.profileAccount.textContent = `${name} · ${maskPhone(account.phone_number)}`;
      }
    } catch (error) {
      if (elements.accountName) {
        elements.accountName.textContent = "账号读取失败";
      }
      if (elements.accountMeta) {
        elements.accountMeta.textContent = error.message;
      }
      elements.accountStatusDot?.classList.add("is-offline");
    }
  };

  const setProfileOpen = (open) => {
    if (!elements.profilePanel) {
      return;
    }
    elements.profilePanel.hidden = !open;
    root.querySelectorAll('[aria-label="聊天信息"], [aria-label="账号信息"]').forEach((button) => {
      button.setAttribute("aria-expanded", String(open));
    });
    if (open) {
      elements.profilePanel.querySelector("[data-close-profile]")?.focus();
    }
  };

  root.querySelector("[data-back-to-list]")?.addEventListener("click", () => {
    root.dataset.mobileView = "list";
  });
  root.querySelector("[data-open-list]")?.addEventListener("click", () => {
    root.dataset.mobileView = "list";
  });
  root.querySelectorAll("[data-toggle-profile]").forEach((button) => {
    button.addEventListener("click", () => setProfileOpen(elements.profilePanel?.hidden ?? true));
  });
  root.querySelector("[data-account-menu]")?.addEventListener("click", () => setProfileOpen(true));
  root.querySelector("[data-close-profile]")?.addEventListener("click", () => setProfileOpen(false));

  root.querySelectorAll("[data-folder]").forEach((button) => {
    button.addEventListener("click", () => {
      state.activeFolder = button.dataset.folder || "all";
      root.querySelectorAll("[data-folder]").forEach((item) => {
        const active = item === button;
        item.classList.toggle("tg-rail-folder-active", active);
        item.setAttribute("aria-selected", String(active));
      });
      renderChats();
    });
  });

  elements.search?.addEventListener("input", () => {
    state.query = elements.search.value.trim().toLocaleLowerCase();
    elements.clearSearch?.toggleAttribute("hidden", !elements.search.value);
    renderChats();
  });

  elements.clearSearch?.addEventListener("click", () => {
    if (!elements.search) {
      return;
    }
    elements.search.value = "";
    state.query = "";
    elements.clearSearch.hidden = true;
    renderChats();
    elements.search.focus();
  });

  elements.refreshChats?.addEventListener("click", () => loadChats(true));
  elements.loadOlder?.addEventListener("click", () => loadMessages(false));
  elements.messageScroll?.addEventListener("scroll", scheduleStickyDate, {passive: true});
  window.addEventListener("resize", scheduleStickyDate, {passive: true});
  root.querySelector("[data-message-composer]")?.addEventListener("submit", (event) => {
    event.preventDefault();
    showToast("当前是只读视图，消息发送功能尚未启用");
  });

  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape" && elements.profilePanel && !elements.profilePanel.hidden) {
      setProfileOpen(false);
    }
  });

  loadAccount();
  loadChats();
})();
