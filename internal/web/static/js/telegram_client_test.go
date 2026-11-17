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
		`element.hidden = count === 0`,
		`classList.toggle("tg-rail-folder-active", active)`,
		`showToast("当前是只读视图，消息发送功能尚未启用")`,
		`/search?`,
		`chatAvatarURL`,
		`contextmenu`,
		`/messages/forward`,
		`/messages/delete`,
		`openProfileEditor`,
		`/profile`,
		`formatPhone`,
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
