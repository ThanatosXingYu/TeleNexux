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
		`data-page-tabs`,
		`sessionStorage`,
		`data-tab-close`,
		`data-tabs-close-others`,
		`data-login-view-web`,
		`target = "_blank"`,
		`userTrigger?.addEventListener`,
		`document.querySelector("[data-open-search]")`,
		`querySelectorAll("[data-toggle-breadcrumb]")`,
		`querySelectorAll("[data-toggle-tabs]")`,
		`data-account-profile`,
		`/api/v1/telegram/auth/send-code`,
		`/api/v1/telegram/auth/qr`,
		`startQRAuth`,
		`pollQRAuth`,
		`setAuthMode`,
		`data-auth-step`,
		`requestJSON`,
		`const element = (tag, className = "", text = "") => {`,
		`node.textContent = text`,
		`passwordForm?.addEventListener`,
		`data-login-view-chat`,
		`loadMessages(items[0])`,
		`data-settings-tab`,
		`data-settings-panel`,
		`data-settings-actions`,
		`readOnlySettingsTabs`,
		`data-page-setting`,
		`footer: true`,
		`shell.dataset.footer`,
		`accountRefreshWarnings`,
		`item.last_verified_at = outcome.verified_at || item.last_verified_at`,
		`refreshed.last_verified_at = outcome.verified_at || refreshed.last_verified_at`,
		`item.refresh_skipped = Boolean(outcome.skipped)`,
		`refresh_skipped`,
		`已跳过`,
		`授权有效`,
		`data-qr-countdown`,
		`countryCode`,
		`openLoginDialog(account)`,
		`reloginAccountID = Number(account?.id) > 0 ? Number(account.id) : 0`,
		`body: JSON.stringify({account_id: reloginAccountID})`,
		`account_id: reloginAccountID`,
		`splitPhone(account.phone_number)`,
		`copyValue(formatPhone(account.phone_number), "手机号")`,
		`if (/^\+\d+\s+\d+$/.test(raw)) {`,
		`return raw;`,
		"const normalized = `+${digits}`;",
		"return `${countryCode} ${normalized.slice(countryCode.length)}`;",
		`/api/v1/accounts/${encodeURIComponent(managedAccount.id)}`,
		`relogin.textContent = "重新登录"`,
		`["error", "stale"].includes(status.key)`,
		`button button-primary button-compact`,
		`refreshSingleAccount(account, refreshAction)`,
		`refreshSelectedAccounts(accountIDs)`,
		`Promise.allSettled(accountIDs.map`,
		`outcome: Array.isArray(response?.outcomes) ? response.outcomes[0] : null`,
		`deferred: Boolean(outcome?.deferred)`,
		`skipped: Boolean(outcome?.skipped)`,
		`loadAccounts(true, targets.map((account) => Number(account.id)))`,
		`new Set(accountRefreshWarnings.map((warning) => Number(warning.account_id))).size`,
		`data-accounts-refresh-label`,
		`if (!verifySessions) {`,
		`accountsLoading?.setAttribute("hidden", "");`,
		`automaticVerificationDue`,
		`automaticStatusCheckNeeded`,
		`verifyAccountStatuses(true)`,
		`if (automaticVerificationDue()) {`,
		`window.setInterval(maybeVerifyExpiredAccounts, automaticVerificationInterval)`,
		`document.visibilityState !== "visible"`,
		`accountVerificationInFlight`,
		`telenexus.account-verification-at`,
		`new URLSearchParams(window.location.search).get("relogin")`,
		`accountVerificationTTL = 10 * 60 * 1000`,
		`accountAvatarObserver`,
		`/chats/user/${`,
		`data-account-offline`,
		`data-account-search`,
		`data-account-batch-bar`,
		`/api/v1/accounts/refresh`,
		`include_deleted=true`,
		`data-account-show-deleted`,
		`data-open-relogin`,
		`account-relogin-dialog`,
		`openLoginDialog(existing || {phone_number: phoneNumber})`,
		`/restore`,
		`restoreAccount(account, restore)`,
		`const offline = candidates.filter((account) => accountStatus(account).key !== "online");`,
		`const online = activeAccounts.filter(`,
		`(item) => accountStatus(item).key === "online",`,
		`.account-row-select input`,
		`/proxy/check`,
		`openProxyDialog(account)`,
		`proxy_url`,
	}
	for _, behavior := range requiredBehavior {
		if !strings.Contains(script, behavior) {
			t.Errorf("app.js missing behavior %q", behavior)
		}
	}

	if regexp.MustCompile(`\balert\(`).MatchString(script) {
		t.Error("app.js must not block navigation with alert dialogs")
	}
	for _, removed := range []string{"code_url", "fetch-code", "data-fetch-code"} {
		if strings.Contains(script, removed) {
			t.Errorf("app.js must not use code receiving behavior %q", removed)
		}
	}
}

func TestScriptAvoidsUnsafeDOMAPIs(t *testing.T) {
	content, err := os.ReadFile("app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	script := string(content)
	for _, forbidden := range []string{"innerHTML", "eval(", "document.write("} {
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
