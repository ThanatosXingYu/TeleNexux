package static

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestScriptUsesOnlyReferencedControls(t *testing.T) {
	content, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(content)

	requiredBehavior := []string{
		`sidebarToggle?.addEventListener`,
		`sidebar?.classList.toggle("is-open"`,
		`sidebar?.classList.remove("is-open")`,
		`data-submenu-toggle`,
		`setSubmenuOpen`,
		`aria-expanded`,
		`userTrigger?.addEventListener`,
		`document.querySelector("[data-open-search]")`,
		`document.querySelector("[data-toggle-breadcrumb]")`,
		`document.querySelector("[data-toggle-tabs]")`,
		`document.querySelector("[data-login-form]")`,
		`/api/v1/telegram/auth/send-code`,
		`data-auth-step`,
		`requestJSON`,
		`fetchCodeFromSource`,
		`passwordForm?.addEventListener`,
		`data-login-view-chat`,
		`loadMessages(items[0])`,
	}
	for _, behavior := range requiredBehavior {
		if !strings.Contains(script, behavior) {
			t.Errorf("app.js missing behavior %q", behavior)
		}
	}

	if regexp.MustCompile(`\balert\(`).MatchString(script) {
		t.Error("app.js must not block navigation with alert dialogs")
	}
}

func TestScriptAvoidsUnsafeDOMAPIs(t *testing.T) {
	content, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(content)
	for _, forbidden := range []string{"innerHTML", "eval(", "document.write(", "localStorage"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("app.js must not use %q", forbidden)
		}
	}
}

func TestReferencedIconsExist(t *testing.T) {
	css, err := os.ReadFile("../css/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	pattern := regexp.MustCompile(`url\("/static/img/icons/([a-z0-9-]+\.svg)"\)`)
	matches := pattern.FindAllStringSubmatch(string(css), -1)
	if len(matches) == 0 {
		t.Fatal("app.css does not reference any SVG icons")
	}
	for _, match := range matches {
		iconPath := "../img/icons/" + match[1]
		if _, err := os.Stat(iconPath); err != nil {
			t.Errorf("icon %q is referenced but missing: %v", iconPath, err)
		}
	}
}
