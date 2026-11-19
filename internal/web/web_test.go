package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardRendersSharedLayout(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"<title>工作台 · TeleNexus</title>",
		"欢迎回来，管理员",
		`class="menu-entry active" href="/"`,
		`data-page-tabs data-current-path="/"`,
		`class="page-tab is-affix active" data-tab-path="/"`,
		"TeleNexus · Telegram 多账号管理平台",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("dashboard body missing %q", expected)
		}
	}
}

func TestEveryNavigationPageRenders(t *testing.T) {
	handler := newTestHandler(t)
	for _, page := range pages {
		t.Run(page.Path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, page.Path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			body := recorder.Body.String()
			if !strings.Contains(body, `aria-current="page"`) {
				t.Error("active page did not expose aria-current")
			}
			if !strings.Contains(body, `aria-expanded="true"`) {
				t.Error("active menu group was not expanded")
			}
			if strings.Contains(body, "当前版本已完成管理台布局") {
				t.Error("navigation page must not render the generic placeholder")
			}
		})
	}
}

func TestBusinessPagesExposeRealDataControls(t *testing.T) {
	handler := newTestHandler(t)
	tests := map[string][]string{
		"/accounts/groups": {"data-groups-grid", "data-group-form"},
		"/monitor":         {"data-monitor-rules-body", "data-monitor-run"},
		"/monitor/tasks":   {"data-tasks-body", "data-monitor-rule-form", "data-monitor-channel"},
		"/monitor/alerts":  {"data-alerts-list", "data-alert-state"},
		"/messages":        {"data-message-filter", "data-messages-body"},
		"/reports":         {"data-report-chart", "data-report-table-body"},
	}
	for path, expected := range tests {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			body := recorder.Body.String()
			for _, marker := range expected {
				if !strings.Contains(body, marker) {
					t.Errorf("%s missing %q", path, marker)
				}
			}
			if !strings.Contains(body, "business.js") {
				t.Errorf("%s does not load business interactions", path)
			}
		})
	}
}

func TestLoginRendersWithoutAdminShell(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(body, `data-login-heading`) {
		t.Error("login page missing form title")
	}
	if strings.Contains(body, `class="sidebar"`) {
		t.Error("login page should not include the dashboard sidebar")
	}
}

func TestUnknownPageReturns404(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/missing", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	handler := newTestHandler(t)
	tests := []string{"/static/css/app.css", "/static/js/app.js", "/static/img/logo.svg"}

	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if recorder.Header().Get("Content-Security-Policy") == "" {
				t.Error("security headers were not set")
			}
		})
	}
}

func TestNavigationUsesExpandableGroups(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/accounts/groups", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if strings.Contains(body, `class="menu-label"`) {
		t.Error("sidebar should not use flat section labels")
	}
	if !strings.Contains(body, `data-submenu-toggle data-menu-tip="账号管理"`) {
		t.Error("account navigation must be an expandable submenu")
	}
	if !strings.Contains(body, `class="submenu is-open" id="submenu-accounts"`) {
		t.Error("current account submenu must be open")
	}
	if !strings.Contains(body, `class="submenu-item active" href="/accounts/groups"`) {
		t.Error("current child page must be highlighted")
	}
}

func TestDashboardUsesVectorIcons(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	body := recorder.Body.String()
	for _, letterIcon := range []string{">账</span>", ">监</span>", ">消</span>", ">警</span>", ">务</span>", ">诊</span>"} {
		if strings.Contains(body, letterIcon) {
			t.Errorf("dashboard still uses letter tile %q", letterIcon)
		}
	}
	if !strings.Contains(body, `class="icon icon-users"`) {
		t.Error("dashboard should use the shared vector icon set")
	}
}

func TestAccountsPageRendersTelegramLoginFlow(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/accounts", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"登录 Telegram 账号",
		`data-auth-step="phone"`,
		`data-auth-step="qr"`,
		`data-login-mode="qr"`,
		`data-login-mode="phone"`,
		`data-login-mode="username"`,
		`data-auth-step="code"`,
		`data-auth-step="password"`,
		`data-accounts-body`,
		`data-account-search`,
		`data-account-status-filter`,
		`data-account-batch-bar`,
		`name="country_code"`,
		`data-login-title`,
		`id="account-profile-dialog"`,
		`data-account-edit-form`,
		`id="account-delete-dialog"`,
		`data-qr-countdown`,
		`data-login-view-chat`,
		`data-login-view-web`,
		`class="menu-group current"`,
		`class="submenu-item active" href="/accounts"`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("accounts page missing %q", expected)
		}
	}
	if strings.Contains(body, "账号列表尚未接入") {
		t.Error("accounts page must not render the generic placeholder")
	}
	if strings.Contains(body, "TELEGRAM ACCOUNT OPERATIONS") {
		t.Error("accounts page must not repeat the page title in the content header")
	}
	if strings.Contains(body, "<h2>已接入账号</h2>") {
		t.Error("accounts page must not repeat a content title above the account list")
	}
	for _, removed := range []string{`name="code_url"`, `data-fetch-code`, "接码链接读取"} {
		if strings.Contains(body, removed) {
			t.Errorf("accounts page still renders code receiving control %q", removed)
		}
	}
}

func TestSystemPageRendersEditableSettings(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/system", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-settings-form`,
		`name="app_env"`,
		`name="log_level"`,
		`name="telegram_api_id"`,
		`name="telegram_api_hash"`,
		`name="http_addr"`,
		`data-settings-tab="page"`,
		`data-settings-panel="page"`,
		`data-settings-actions`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("system page missing %q", expected)
		}
	}
	if strings.Contains(body, "系统设置尚未接入") {
		t.Error("system page must not render the generic placeholder")
	}
	for _, removedField := range []string{`name="code_url"`, `name="code_source"`, `name="fetch_code"`} {
		if strings.Contains(body, removedField) {
			t.Errorf("system page still renders code receiving field %q", removedField)
		}
	}
}

func TestSystemHealthOpensServiceStatus(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/system/health", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`data-settings-tab="status">服务状态</button>`,
		`data-health-checks`,
		`class="submenu-item active" href="/system/health"`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("health settings page missing %q", expected)
		}
	}
	if !strings.Contains(body, `data-settings-panel="status" >`) {
		t.Error("service status panel must be visible by default on /system/health")
	}
}

func TestAccountDetailRendersChatViewer(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/accounts/42", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `data-account-id="42"`) {
		t.Error("account detail missing the account ID")
	}
	for _, expected := range []string{"data-chat-list", "data-message-list", "data-load-older"} {
		if !strings.Contains(body, expected) {
			t.Errorf("account detail missing chat viewer element %q", expected)
		}
	}
}

func TestAccountWorkspaceRendersStandaloneChatViewer(t *testing.T) {
	handler := newTestHandler(t)
	request := httptest.NewRequest(http.MethodGet, "/accounts/42/web", nil)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`class="telegram-client"`,
		`data-account-id="42"`,
		`data-telegram-client`,
		`data-chat-list`,
		`data-message-list`,
		`data-profile-panel`,
		`data-app-context-menu`,
		`搜索会话或用户名`,
		`data-app-context-action="select"`,
		`仅为我删除`,
		`name="scope" value="me"`,
		`class="tg-rail-folders"`,
		`class="tg-composer"`,
		`/static/css/telegram_client.css`,
		`/static/js/telegram_client.js`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("account workspace missing %q", expected)
		}
	}
	for _, unexpected := range []string{`class="sidebar"`, `class="topbar"`, `class="tg-folders"`, `data-page-tabs`} {
		if strings.Contains(body, unexpected) {
			t.Errorf("account workspace must not include administration shell %q", unexpected)
		}
	}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	renderer, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return renderer.Handler()
}
