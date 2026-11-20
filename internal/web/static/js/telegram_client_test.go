package static

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestTelegramClientScriptBehaviors(t *testing.T) {
	content, err := os.ReadFile("telegram_client.js")
	if err != nil {
		t.Fatalf("read telegram_client.js: %v", err)
	}
	script := string(content)

	for _, behavior := range []string{
		`/api/v1/accounts/${encodeURIComponent(accountID)}/chats?limit=100`,
		`state.messages = [...byID.values()]`,
		`root.dataset.mobileView = "chat"`,
		`before_id`,
		`setProfileOpen`,
		`error.code === "session_invalid"`,
		`data-relogin-account`,
		`/accounts?relogin=`,
		`element.hidden = count === 0`,
		`classList.toggle("tg-rail-folder-active", active)`,
		`showToast("当前是只读视图，消息发送功能尚未启用")`,
		`/search?`,
		`chatAvatarURL`,
		`contextmenu`,
		`data-app-context-menu`,
		`placeContextMenu(elements.appContextMenu, event)`,
		`state.actionMessageIDs = []`,
		`setSelectionMode(true)`,
		`/messages/forward`,
		`/messages/delete`,
		`openProfileEditor`,
		`data-account-profile`,
		`elements.accountProfile?.addEventListener`,
		`/profile`,
		`formatPhone`,
		`avatarObserver`,
		`createAvatarObserver(elements.chatList)`,
		`createAvatarObserver(elements.messageScroll)`,
		`avatarLoadConcurrency`,
		`const avatarLoadConcurrency = 1`,
		`queueAvatarLoad`,
		`window.requestAnimationFrame`,
		`element.append(image)`,
		`image.loading = "lazy"`,
		`tg-avatar-fallback`,
		`classList.add("has-image")`,
		`normalizeSearchQuery`,
		`.replace(/^@+/, "")`,
		`state.searchFallback = true`,
		`已搜索最近会话和 Telegram 联系人`,
		`最近一次真实校验已过期`,
		`tg-select-control`,
		`scope}`,
		`error: "已掉线"`,
		`textContent`,
	} {
		if !strings.Contains(script, behavior) {
			t.Errorf("telegram_client.js missing behavior %q", behavior)
		}
	}

	for _, forbidden := range []string{"innerHTML", "insertAdjacentHTML", "eval(", "document.write(", "localStorage"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("telegram_client.js must not use %q", forbidden)
		}
	}
}

func TestTelegramClientAvatarObserverUsesOwnInstance(t *testing.T) {
	content, err := os.ReadFile("telegram_client.js")
	if err != nil {
		t.Fatalf("read telegram_client.js: %v", err)
	}
	script := string(content)

	if !strings.Contains(script, "observer.unobserve(image)") {
		t.Fatal("avatar observer must unobserve through its own instance")
	}
	applyAvatar := strings.Index(script, "const applyAvatar =")
	if applyAvatar < 0 {
		t.Fatal("avatar application behavior is missing")
	}
	applyAvatarBody := script[applyAvatar:]
	appendImage := strings.Index(applyAvatarBody, "element.append(image)")
	requestFrame := strings.Index(applyAvatarBody, "window.requestAnimationFrame")
	if appendImage < 0 || requestFrame < 0 {
		t.Fatal("avatar mounting or observation behavior is missing")
	}
	if appendImage > requestFrame {
		t.Fatal("avatar image must be mounted before scheduling observation")
	}
}

func TestTelegramClientUsesWebGeometry(t *testing.T) {
	content, err := os.ReadFile("../css/telegram_client.css")
	if err != nil {
		t.Fatalf("read telegram_client.css: %v", err)
	}
	css := string(content)

	for _, expected := range []string{
		"--tg-rail-width: 72px",
		"--tg-chatlist-width: 360px",
		"--tg-chat-width: 696px",
		"--tg-topbar-height: 48px",
		"--tg-input-height: 48px",
		"--tg-row-height: 72px",
		"--tg-column-radius: 24px",
	} {
		if !strings.Contains(css, expected) {
			t.Errorf("telegram_client.css missing geometry %q", expected)
		}
	}
}

func TestTelegramClientIconsExist(t *testing.T) {
	css, err := os.ReadFile("../css/telegram_client.css")
	if err != nil {
		t.Fatalf("read telegram_client.css: %v", err)
	}
	pattern := regexp.MustCompile(`url\("/static/img/icons/([a-z0-9-]+\.svg)"\)`)
	matches := pattern.FindAllStringSubmatch(string(css), -1)
	if len(matches) == 0 {
		t.Fatal("telegram_client.css does not reference any SVG icons")
	}
	for _, match := range matches {
		iconPath := "../img/icons/" + match[1]
		if _, err := os.Stat(iconPath); err != nil {
			t.Errorf("icon %q is referenced but missing: %v", iconPath, err)
		}
	}
}
