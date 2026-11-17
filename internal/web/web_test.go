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
		`class="page-tab active" href="/"`,
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
	if !strings.Contains(body, "登录 TeleNexus") {
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
		`data-auth-step="code"`,
		`data-auth-step="password"`,
		`data-accounts-body`,
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
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	renderer, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return renderer.Handler()
}
