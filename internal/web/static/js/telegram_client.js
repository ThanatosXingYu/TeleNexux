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
    profilePhone: root.querySelector("[data-profile-phone]"),
    profilePhoneRow: root.querySelector("[data-profile-phone-row]"),
    profileBio: root.querySelector("[data-profile-bio]"),
    profileHeading: root.querySelector("[data-profile-heading]"),
    profileEdit: root.querySelector("[data-profile-edit]"),
    profileForm: root.querySelector("[data-profile-form]"),
    profileCancel: root.querySelector("[data-profile-cancel]"),
    profileError: root.querySelector("[data-profile-error]"),
    profileNote: root.querySelector("[data-profile-note]"),
    selectionBar: root.querySelector("[data-selection-bar]"),
    selectionCount: root.querySelector("[data-selection-count]"),
    contextMenu: root.querySelector("[data-context-menu]"),
    forwardDialog: root.querySelector("[data-forward-dialog]"),
    forwardForm: root.querySelector("[data-forward-form]"),
    forwardTarget: root.querySelector("[data-forward-target]"),
    forwardCount: root.querySelector("[data-forward-count]"),
    forwardError: root.querySelector("[data-forward-error]"),
    deleteDialog: root.querySelector("[data-delete-dialog]"),
    deleteForm: root.querySelector("[data-delete-form]"),
    deleteCount: root.querySelector("[data-delete-count]"),
    deleteError: root.querySelector("[data-delete-error]"),
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
    searchResults: null,
    searchTimer: 0,
    searchRequest: 0,
    selectionMode: false,
    selectedMessageIDs: new Set(),
    contextMessage: null,
    actionMessageIDs: [],
    forwardChats: new Map(),
    profile: null,
    profileTarget: null,
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
    error: "已掉线",
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

  const countryCodes = [
    "+886", "+852", "+853", "+971", "+966", "+972", "+351", "+358",
    "+353", "+420", "+421", "+385", "+386", "+381", "+380", "+61",
    "+62", "+63", "+64", "+65", "+66", "+60", "+49", "+44", "+43",
    "+41", "+39", "+34", "+33", "+32", "+31", "+30", "+27", "+20",
    "+1", "+7", "+86", "+81", "+82",
  ];

  const formatPhone = (phone) => {
    const digits = String(phone || "").replace(/\D/g, "");
    if (!digits) {
      return "未设置";
    }
    const normalized = `+${digits}`;
    const countryCode = countryCodes.find((code) => normalized.startsWith(code)) || "";
    if (!countryCode || normalized.length === countryCode.length) {
      return normalized;
    }
    return `${countryCode} ${normalized.slice(countryCode.length)}`;
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

  const chatAvatarURL = (chat) => {
    if (!chat?.has_avatar || !chat.type || !chat.peer_id) {
      return "";
    }
    return `/api/v1/accounts/${encodeURIComponent(accountID)}/chats/` +
      `${encodeURIComponent(chat.type)}/${encodeURIComponent(chat.peer_id)}/avatar`;
  };

  const currentAvatarURL = () => (
    `/api/v1/accounts/${encodeURIComponent(accountID)}/chats/user/` +
    `${encodeURIComponent(accountID)}/avatar`
  );

  const avatarLoadConcurrency = 1;
  const avatarLoadTimeout = 12000;
  const avatarLoadQueue = [];
  const activeAvatarLoads = new Set();

  const completeAvatarLoad = (image) => {
    if (!activeAvatarLoads.delete(image)) {
      return;
    }
    window.clearTimeout(Number(image.dataset.avatarTimer) || 0);
    delete image.dataset.avatarTimer;
    while (activeAvatarLoads.size < avatarLoadConcurrency && avatarLoadQueue.length > 0) {
      const next = avatarLoadQueue.shift();
      startAvatarLoad(next.image, next.avatarURL);
    }
  };

  const startAvatarLoad = (image, avatarURL) => {
    if (!image.isConnected || activeAvatarLoads.has(image)) {
      return;
    }
    avatarObservers.forEach((observer) => observer.unobserve(image));
    activeAvatarLoads.add(image);
    image.src = avatarURL;
    image.dataset.avatarTimer = String(window.setTimeout(() => {
      image.src = "";
      completeAvatarLoad(image);
    }, avatarLoadTimeout));
  };

  const queueAvatarLoad = (image, avatarURL) => {
    if (activeAvatarLoads.size < avatarLoadConcurrency) {
      startAvatarLoad(image, avatarURL);
      return;
    }
    avatarLoadQueue.push({image, avatarURL});
  };

  const createAvatarObserver = (root) => {
    if (typeof IntersectionObserver !== "function") {
      return null;
    }
    let observer;
    observer = new IntersectionObserver((entries) => {
      entries.forEach((entry) => {
        if (!entry.isIntersecting) {
          return;
        }
        const image = entry.target;
        observer.unobserve(image);
        if (image.dataset.avatarSrc) {
          queueAvatarLoad(image, image.dataset.avatarSrc);
          delete image.dataset.avatarSrc;
        }
      });
    }, {root, rootMargin: "120px"});
    return observer;
  };
  const avatarObserver = createAvatarObserver(null);
  const chatAvatarObserver = createAvatarObserver(elements.chatList);
  const messageAvatarObserver = createAvatarObserver(elements.messageScroll);
  const avatarObservers = [avatarObserver, chatAvatarObserver, messageAvatarObserver].filter(Boolean);

  const avatarObserverFor = (element) => {
    if (elements.messageScroll?.contains(element)) {
      return messageAvatarObserver;
    }
    if (elements.chatList?.contains(element)) {
      return chatAvatarObserver;
    }
    return avatarObserver;
  };

  const applyAvatar = (element, title, extraClass = "", avatarURL = "") => {
    if (!element) {
      return;
    }
    element.className = `tg-avatar ${extraClass} tg-avatar-color-${stableColorIndex(title)}`.replace(/\s+/g, " ").trim();
    element.replaceChildren();
    const fallback = document.createElement("span");
    fallback.className = "tg-avatar-fallback";
    fallback.textContent = initials(title);
    element.append(fallback);
    if (!avatarURL) {
      return;
    }
    const image = document.createElement("img");
    image.alt = "";
    image.loading = "lazy";
    image.decoding = "async";
    image.addEventListener("load", () => {
      completeAvatarLoad(image);
      element.classList.add("has-image");
    }, {once: true});
    image.addEventListener("error", () => {
      completeAvatarLoad(image);
      avatarObservers.forEach((observer) => observer.unobserve(image));
      element.classList.remove("has-image");
      image.remove();
    }, {once: true});
    element.append(image);
    window.requestAnimationFrame(() => {
      if (!image.isConnected) {
        return;
      }
      const observer = avatarObserverFor(element);
      if (observer) {
        image.dataset.avatarSrc = avatarURL;
        observer.observe(image);
      } else {
        queueAvatarLoad(image, avatarURL);
      }
    });
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

  const normalizeSearchQuery = (value) => String(value || "")
    .trim()
    .replace(/^@+/, "")
    .toLocaleLowerCase();

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
    applyAvatar(avatar, chat.title || chat.username, "", chatAvatarURL(chat));

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
    const sourceChats = state.query && state.searchResults
      ? state.searchResults
      : state.chats;
    state.filteredChats = sourceChats.filter(
      (chat) => chatMatchesFolder(chat) && chatMatchesQuery(chat),
    );
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

  const runSearch = async () => {
    const query = state.query.trim();
    const requestID = ++state.searchRequest;
    if (!query) {
      state.searchResults = null;
      renderChats();
      return;
    }
    try {
      const params = new URLSearchParams({q: query, limit: "30"});
      const payload = await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(accountID)}/search?${params}`,
      );
      if (requestID !== state.searchRequest) {
        return;
      }
      state.searchResults = Array.isArray(payload?.items) ? payload.items : [];
      renderChats();
    } catch (error) {
      if (requestID !== state.searchRequest) {
        return;
      }
      state.searchResults = null;
      renderChats();
      showToast(error.message);
    }
  };

  const updateProfile = (chat) => {
    if (!chat) {
      return;
    }
    applyAvatar(
      elements.profileAvatar,
      chat.title || chat.username,
      "tg-avatar-profile",
      chatAvatarURL(chat),
    );
    if (elements.profileTitle) {
      elements.profileTitle.textContent = chat.title || "未命名会话";
    }
    if (elements.profileUsername) {
      elements.profileUsername.textContent = chat.username ? `@${chat.username}` : typeLabels[chat.type] || "Telegram";
    }
    if (elements.profileType) {
      elements.profileType.textContent = typeLabels[chat.type] || "会话";
    }
    if (elements.profileBio) {
      elements.profileBio.hidden = true;
      elements.profileBio.textContent = "";
    }
    if (elements.profilePhoneRow) {
      elements.profilePhoneRow.hidden = true;
    }
    if (elements.profileUnread) {
      elements.profileUnread.textContent = String(Number(chat.unread_count) || 0);
    }
    if (elements.profileAccount) {
      const accountName = state.account?.display_name || state.account?.username || "Telegram 账号";
      elements.profileAccount.textContent = `${accountName} · ${formatPhone(state.account?.phone_number)}`;
    }
    if (elements.profileHeading) {
      elements.profileHeading.textContent = "聊天信息";
    }
    if (elements.profileNote) {
      elements.profileNote.textContent = "更多资料正在从 Telegram 读取。";
    }
    elements.profileEdit?.setAttribute("hidden", "");
    elements.profileForm?.setAttribute("hidden", "");
  };

  const profileURL = (target) => {
    if (target?.kind === "current") {
      return `/api/v1/accounts/${encodeURIComponent(accountID)}/profile`;
    }
    if (target?.chat) {
      return `/api/v1/accounts/${encodeURIComponent(accountID)}/chats/` +
        `${encodeURIComponent(target.chat.type)}/` +
        `${encodeURIComponent(target.chat.peer_id)}/profile`;
    }
    return "";
  };

  const renderProfile = (profile, target) => {
    state.profile = profile;
    state.profileTarget = target;
    const title = profile.display_name || profile.username || "Telegram";
    const avatarURL = target?.kind === "current"
      ? currentAvatarURL()
      : (profile.has_avatar
        ? `/api/v1/accounts/${encodeURIComponent(accountID)}/chats/` +
          `${encodeURIComponent(profile.type)}/${encodeURIComponent(profile.id)}/avatar`
        : (target?.chat ? chatAvatarURL(target.chat) : ""));
    applyAvatar(elements.profileAvatar, title, "tg-avatar-profile", avatarURL);
    if (elements.profileTitle) {
      elements.profileTitle.textContent = title;
    }
    if (elements.profileUsername) {
      elements.profileUsername.textContent = profile.username
        ? `@${profile.username}`
        : (typeLabels[profile.type] || "Telegram");
    }
    if (elements.profileType) {
      elements.profileType.textContent = typeLabels[profile.type] || "会话";
    }
    if (elements.profilePhone) {
      elements.profilePhone.textContent = formatPhone(profile.phone_number);
    }
    if (elements.profilePhoneRow) {
      elements.profilePhoneRow.hidden = !profile.phone_number;
    }
    if (elements.profileBio) {
      elements.profileBio.textContent = profile.bio || "";
      elements.profileBio.hidden = !profile.bio;
    }
    if (elements.profileUnread) {
      elements.profileUnread.textContent = String(
        Number(target?.chat?.unread_count || state.selectedChat?.unread_count) || 0,
      );
    }
    if (elements.profileAccount) {
      const accountName = state.account?.display_name || state.account?.username || "Telegram 账号";
      elements.profileAccount.textContent = `${accountName} · ${formatPhone(state.account?.phone_number)}`;
    }
    if (elements.profileHeading) {
      elements.profileHeading.textContent = target?.kind === "current" ? "个人资料" : "聊天信息";
    }
    if (elements.profileNote) {
      elements.profileNote.textContent = target?.kind === "current"
        ? "这里显示当前已授权账号的 Telegram 资料，可修改名称、用户名和简介。"
        : "这里显示从 Telegram 实时读取的会话资料。";
    }
    elements.profileEdit?.toggleAttribute("hidden", !profile.can_edit);
    elements.profileForm?.setAttribute("hidden", "");
    showNotice(elements.profileError, "");
  };

  const loadProfile = async (target) => {
    const url = profileURL(target);
    if (!url) {
      return;
    }
    setProfileOpen(true);
    if (elements.profileNote) {
      elements.profileNote.textContent = "正在从 Telegram 读取资料...";
    }
    try {
      const profile = await requestJSON(url);
      renderProfile(profile, target);
    } catch (error) {
      showToast(error.message);
    }
  };

  const openProfileEditor = () => {
    if (!state.profile?.can_edit || !elements.profileForm) {
      return;
    }
    elements.profileForm.elements.namedItem("first_name").value = state.profile.first_name || "";
    elements.profileForm.elements.namedItem("last_name").value = state.profile.last_name || "";
    elements.profileForm.elements.namedItem("username").value = state.profile.username
      ? `@${state.profile.username}`
      : "";
    elements.profileForm.elements.namedItem("bio").value = state.profile.bio || "";
    elements.profileForm.removeAttribute("hidden");
    elements.profileNote?.setAttribute("hidden", "");
    showNotice(elements.profileError, "");
    elements.profileForm.elements.namedItem("first_name")?.focus();
  };

  const selectChat = (chat) => {
    state.selectedChat = chat;
    state.messages = [];
    state.nextBeforeID = 0;
    state.messagesRequest += 1;
    setSelectionMode(false);
    hideContextMenu();
    root.dataset.mobileView = "chat";

    elements.threadEmpty?.setAttribute("hidden", "");
    elements.conversation?.removeAttribute("hidden");
    applyAvatar(
      elements.conversationAvatar,
      chat.title || chat.username,
      "tg-avatar-medium",
      chatAvatarURL(chat),
    );
    if (elements.conversationTitle) {
      elements.conversationTitle.textContent = chat.title || "未命名会话";
    }
    if (elements.conversationMeta) {
      elements.conversationMeta.textContent = chat.username
        ? `@${chat.username}`
        : (typeLabels[chat.type] || "Telegram");
    }
    updateProfile(chat);
    if (elements.profilePanel && !elements.profilePanel.hidden) {
      loadProfile({kind: "chat", chat});
    }
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
    row.dataset.messageId = String(message.id);
    row.classList.toggle("is-selected", state.selectedMessageIDs.has(Number(message.id)));
    const consecutive = sameMessageGroup(previous, message);
    const nextConsecutive = sameMessageGroup(message, next);
    row.classList.toggle("is-consecutive", consecutive);
    row.classList.toggle("is-group-first", !consecutive);
    row.classList.toggle("is-group-last", !nextConsecutive);

    if (!message.outgoing && (chat.type === "chat" || chat.type === "channel")) {
      const avatar = document.createElement("span");
      const senderAvatarURL = Number(message.sender_id) > 0
        ? `/api/v1/accounts/${encodeURIComponent(accountID)}/chats/user/` +
          `${encodeURIComponent(message.sender_id)}/avatar`
        : "";
      applyAvatar(
        avatar,
        message.sender || chat.title,
        `tg-message-avatar${consecutive ? " is-placeholder" : ""}`,
        senderAvatarURL,
      );
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
    row.addEventListener("click", () => {
      if (state.selectionMode) {
        toggleMessageSelection(Number(message.id));
      }
    });
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
    updateSelectionUI();
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

  const findMessage = (id) => state.messages.find((message) => Number(message.id) === Number(id));

  const updateSelectionUI = () => {
    const count = state.selectedMessageIDs.size;
    elements.selectionBar?.toggleAttribute("hidden", !state.selectionMode);
    if (elements.selectionCount) {
      elements.selectionCount.textContent = String(count);
    }
    root.querySelectorAll("[data-selection-forward], [data-selection-delete]").forEach((button) => {
      button.disabled = count === 0;
    });
    root.querySelector("[data-toggle-selection]")?.setAttribute(
      "aria-pressed",
      String(state.selectionMode),
    );
    elements.messageList?.querySelectorAll("[data-message-id]").forEach((row) => {
      const selected = state.selectedMessageIDs.has(Number(row.dataset.messageId));
      row.querySelector(".tg-select-control")?.remove();
      if (state.selectionMode) {
        const control = document.createElement("span");
        control.className = "tg-select-control";
        control.setAttribute("aria-hidden", "true");
        const icon = document.createElement("span");
        icon.className = `icon ${selected ? "icon-circle-check" : "icon-check-check"}`;
        control.append(icon);
        row.prepend(control);
      }
      row.classList.toggle("is-selected", selected);
      row.classList.toggle("is-selectable", state.selectionMode);
      row.setAttribute("aria-selected", String(selected));
    });
  };

  const setSelectionMode = (active) => {
    state.selectionMode = active;
    if (!active) {
      state.selectedMessageIDs.clear();
    }
    updateSelectionUI();
  };

  const toggleMessageSelection = (messageID) => {
    if (!Number.isFinite(messageID)) {
      return;
    }
    if (state.selectedMessageIDs.has(messageID)) {
      state.selectedMessageIDs.delete(messageID);
    } else {
      state.selectedMessageIDs.add(messageID);
    }
    updateSelectionUI();
  };

  const hideContextMenu = () => {
    elements.contextMenu?.setAttribute("hidden", "");
    state.contextMessage = null;
  };

  const openContextMenu = (event, message) => {
    if (!elements.contextMenu || !message) {
      return;
    }
    state.contextMessage = message;
    elements.contextMenu.hidden = false;
    const menuRect = elements.contextMenu.getBoundingClientRect();
    const left = Math.min(event.clientX, window.innerWidth - menuRect.width - 8);
    const top = Math.min(event.clientY, window.innerHeight - menuRect.height - 8);
    elements.contextMenu.style.left = `${Math.max(8, left)}px`;
    elements.contextMenu.style.top = `${Math.max(8, top)}px`;
  };

  const copyMessageText = async (message) => {
    const text = message?.text || "";
    if (!text) {
      return;
    }
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const area = document.createElement("textarea");
      area.value = text;
      area.setAttribute("readonly", "");
      area.style.position = "fixed";
      area.style.opacity = "0";
      root.append(area);
      area.select();
      document.execCommand("copy");
      area.remove();
    }
    showToast("消息文本已复制");
  };

  const openForwardDialog = (messageIDs) => {
    if (!state.selectedChat || !elements.forwardDialog || !elements.forwardTarget) {
      return;
    }
    const ids = [...new Set(messageIDs.map(Number).filter((id) => id > 0))];
    if (ids.length === 0) {
      return;
    }
    state.actionMessageIDs = ids;
    state.forwardChats.clear();
    const currentID = `${state.selectedChat.type}:${state.selectedChat.peer_id}`;
    state.chats.forEach((chat) => {
      const value = `${chat.type}:${chat.peer_id}`;
      if (value === currentID) {
        return;
      }
      state.forwardChats.set(value, chat);
    });
    elements.forwardTarget.replaceChildren(
      ...[...state.forwardChats.entries()].map(([value, chat]) => {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = chat.title || (chat.username ? `@${chat.username}` : value);
        return option;
      }),
    );
    if (state.forwardChats.size === 0) {
      showToast("没有可用的转发目标");
      return;
    }
    if (elements.forwardCount) {
      elements.forwardCount.textContent = String(ids.length);
    }
    showNotice(elements.forwardError, "");
    elements.forwardDialog.showModal();
  };

  const openDeleteDialog = (messageIDs) => {
    if (!elements.deleteDialog) {
      return;
    }
    const ids = [...new Set(messageIDs.map(Number).filter((id) => id > 0))];
    if (ids.length === 0) {
      return;
    }
    state.actionMessageIDs = ids;
    if (elements.deleteCount) {
      elements.deleteCount.textContent = String(ids.length);
    }
    const safeScope = elements.deleteForm?.querySelector('input[name="scope"][value="me"]');
    if (safeScope) {
      safeScope.checked = true;
    }
    showNotice(elements.deleteError, "");
    elements.deleteDialog.showModal();
  };

  const openMessageProfile = (message) => {
    if (message.outgoing || state.selectedChat?.type === "user") {
      if (message.outgoing) {
        loadProfile({kind: "current"});
      } else {
        loadProfile({kind: "chat", chat: state.selectedChat});
      }
      return;
    }
    if (Number(message.sender_id) > 0) {
      loadProfile({
        kind: "chat",
        chat: {
          type: "user",
          peer_id: Number(message.sender_id),
          title: message.sender,
          unread_count: 0,
        },
      });
      return;
    }
    loadProfile({kind: "chat", chat: state.selectedChat});
  };

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
      if (state.query) {
        runSearch();
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
      if (error.code === "session_invalid" || error.code === "session_not_found") {
        elements.accountStatusDot?.classList.add("is-offline");
        if (elements.accountMeta) {
          elements.accountMeta.textContent = "Telegram 授权已失效";
        }
        loadAccount();
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
          formatPhone(account.phone_number),
          statusLabels[account.status] || account.status,
        ].filter(Boolean).join(" · ");
      }
      applyAvatar(elements.accountAvatar, name, "tg-avatar-small", currentAvatarURL());
      elements.accountStatusDot?.classList.toggle("is-offline", !online);
      if (elements.profileAccount) {
        elements.profileAccount.textContent = `${name} · ${formatPhone(account.phone_number)}`;
      }
      if (!online) {
        elements.refreshChats?.setAttribute("disabled", "");
        elements.chatsLoading?.setAttribute("hidden", "");
        elements.chatList?.setAttribute("hidden", "");
        elements.chatsEmpty?.removeAttribute("hidden");
        if (elements.chatsEmptyTitle) {
          elements.chatsEmptyTitle.textContent = "账号已掉线";
        }
        if (elements.chatsEmptyCopy) {
          elements.chatsEmptyCopy.textContent = "请返回账号列表重新登录后再查看会话。";
        }
        showNotice(elements.chatsError, "该账号的 Telegram 授权已失效，未发送任何聊天请求。");
      }
      return online;
    } catch (error) {
      if (elements.accountName) {
        elements.accountName.textContent = "账号读取失败";
      }
      if (elements.accountMeta) {
        elements.accountMeta.textContent = error.message;
      }
      elements.accountStatusDot?.classList.add("is-offline");
      elements.chatsLoading?.setAttribute("hidden", "");
      showNotice(elements.chatsError, error.message);
      return false;
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
    button.addEventListener("click", () => {
      const shouldOpen = elements.profilePanel?.hidden ?? true;
      if (!shouldOpen) {
        setProfileOpen(false);
        return;
      }
      if (state.selectedChat) {
        updateProfile(state.selectedChat);
        loadProfile({kind: "chat", chat: state.selectedChat});
      } else {
        setProfileOpen(true);
      }
    });
  });
  root.querySelector("[data-account-menu]")?.addEventListener("click", () => {
    loadProfile({kind: "current"});
  });
  root.querySelector("[data-close-profile]")?.addEventListener("click", () => setProfileOpen(false));
  elements.profileEdit?.addEventListener("click", openProfileEditor);
  elements.profileCancel?.addEventListener("click", () => {
    elements.profileForm?.setAttribute("hidden", "");
    elements.profileNote?.removeAttribute("hidden");
    showNotice(elements.profileError, "");
  });
  elements.profileForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const submit = elements.profileForm.querySelector("button[type='submit']");
    const data = new FormData(elements.profileForm);
    submit?.setAttribute("disabled", "");
    showNotice(elements.profileError, "");
    try {
      const saved = await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(accountID)}/profile`,
        {
          method: "PUT",
          headers: {"Content-Type": "application/json"},
          body: JSON.stringify({
            first_name: String(data.get("first_name") || "").trim(),
            last_name: String(data.get("last_name") || "").trim(),
            username: String(data.get("username") || "").trim(),
            bio: String(data.get("bio") || "").trim(),
          }),
        },
      );
      if (saved?.account) {
        state.account = saved.account;
      }
      renderProfile(saved.profile, {kind: "current"});
      loadAccount();
      showToast("个人资料已更新");
    } catch (error) {
      showNotice(elements.profileError, error.message);
    } finally {
      submit?.removeAttribute("disabled");
    }
  });

  root.querySelector("[data-toggle-selection]")?.addEventListener("click", () => {
    setSelectionMode(!state.selectionMode);
  });
  root.querySelector("[data-exit-selection]")?.addEventListener("click", () => {
    setSelectionMode(false);
  });
  root.querySelector("[data-selection-forward]")?.addEventListener("click", () => {
    openForwardDialog([...state.selectedMessageIDs]);
  });
  root.querySelector("[data-selection-delete]")?.addEventListener("click", () => {
    openDeleteDialog([...state.selectedMessageIDs]);
  });

  root.addEventListener("contextmenu", (event) => {
    event.preventDefault();
    const row = event.target.closest("[data-message-id]");
    const message = row ? findMessage(row.dataset.messageId) : null;
    hideContextMenu();
    if (message) {
      openContextMenu(event, message);
    }
  });

  elements.contextMenu?.addEventListener("click", (event) => {
    const action = event.target.closest("[data-context-action]")?.dataset.contextAction;
    const message = state.contextMessage;
    if (!action || !message) {
      return;
    }
    const messageIDs = state.selectionMode && state.selectedMessageIDs.size > 0
      ? [...state.selectedMessageIDs]
      : [Number(message.id)];
    hideContextMenu();
    switch (action) {
    case "copy":
      copyMessageText(message);
      break;
    case "profile":
      openMessageProfile(message);
      break;
    case "forward":
      openForwardDialog(messageIDs);
      break;
    case "select":
      setSelectionMode(true);
      toggleMessageSelection(Number(message.id));
      break;
    case "delete":
      openDeleteDialog(messageIDs);
      break;
    }
  });

  root.querySelectorAll("[data-close-dialog]").forEach((button) => {
    button.addEventListener("click", () => button.closest("dialog")?.close());
  });

  elements.forwardForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const submit = elements.forwardForm.querySelector("button[type='submit']");
    const target = elements.forwardChats.get(elements.forwardTarget?.value || "");
    if (!target || !state.selectedChat) {
      showNotice(elements.forwardError, "请选择有效的转发目标。");
      return;
    }
    submit?.setAttribute("disabled", "");
    showNotice(elements.forwardError, "");
    try {
      await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(accountID)}/chats/` +
          `${encodeURIComponent(state.selectedChat.type)}/` +
          `${encodeURIComponent(state.selectedChat.peer_id)}/messages/forward`,
        {
          method: "POST",
          headers: {"Content-Type": "application/json"},
          body: JSON.stringify({
            message_ids: state.actionMessageIDs,
            to_type: target.type,
            to_id: target.peer_id,
          }),
        },
      );
      elements.forwardDialog.close();
      setSelectionMode(false);
      showToast(`已转发 ${state.actionMessageIDs.length} 条消息`);
    } catch (error) {
      showNotice(elements.forwardError, error.message);
    } finally {
      submit?.removeAttribute("disabled");
    }
  });

  elements.deleteForm?.addEventListener("submit", async (event) => {
    event.preventDefault();
    const submit = elements.deleteForm.querySelector("button[type='submit']");
    if (!state.selectedChat) {
      return;
    }
    submit?.setAttribute("disabled", "");
    showNotice(elements.deleteError, "");
    const scope = String(new FormData(elements.deleteForm).get("scope") || "me");
    try {
      await requestJSON(
        `/api/v1/accounts/${encodeURIComponent(accountID)}/chats/` +
          `${encodeURIComponent(state.selectedChat.type)}/` +
          `${encodeURIComponent(state.selectedChat.peer_id)}/messages/delete`,
        {
          method: "POST",
          headers: {"Content-Type": "application/json"},
          body: JSON.stringify({message_ids: state.actionMessageIDs, scope}),
        },
      );
      const deleted = new Set(state.actionMessageIDs);
      state.messages = state.messages.filter((message) => !deleted.has(Number(message.id)));
      elements.deleteDialog.close();
      setSelectionMode(false);
      renderMessages();
      showToast(`已删除 ${deleted.size} 条消息`);
    } catch (error) {
      showNotice(elements.deleteError, error.message);
    } finally {
      submit?.removeAttribute("disabled");
    }
  });

  document.addEventListener("click", (event) => {
    if (!event.target.closest("[data-context-menu]")) {
      hideContextMenu();
    }
  });

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
    state.query = normalizeSearchQuery(elements.search.value);
    elements.clearSearch?.toggleAttribute("hidden", !elements.search.value);
    state.searchResults = null;
    renderChats();
    window.clearTimeout(state.searchTimer);
    if (state.query) {
      state.searchTimer = window.setTimeout(runSearch, 320);
    } else {
      state.searchRequest += 1;
    }
  });

  elements.clearSearch?.addEventListener("click", () => {
    if (!elements.search) {
      return;
    }
    elements.search.value = "";
    state.query = "";
    state.searchResults = null;
    state.searchRequest += 1;
    window.clearTimeout(state.searchTimer);
    elements.clearSearch.hidden = true;
    renderChats();
    elements.search.focus();
  });

  elements.refreshChats?.addEventListener("click", () => loadChats(true));
  elements.loadOlder?.addEventListener("click", () => loadMessages(false));
  elements.messageScroll?.addEventListener("scroll", () => {
    hideContextMenu();
    scheduleStickyDate();
  }, {passive: true});
  window.addEventListener("resize", () => {
    hideContextMenu();
    scheduleStickyDate();
  }, {passive: true});
  root.querySelector("[data-message-composer]")?.addEventListener("submit", (event) => {
    event.preventDefault();
    showToast("当前是只读视图，消息发送功能尚未启用");
  });

  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") {
      return;
    }
    if (!elements.contextMenu?.hidden) {
      hideContextMenu();
      return;
    }
    if (state.selectionMode) {
      setSelectionMode(false);
      return;
    }
    if (elements.profilePanel && !elements.profilePanel.hidden) {
      setProfileOpen(false);
    }
  });

  loadAccount().then((online) => {
    if (online) {
      loadChats();
    }
  });
})();
