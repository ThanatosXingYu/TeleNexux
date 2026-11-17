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
		`class="menu-item active" href="/"`,
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
	paths := []string{
		"/accounts",
		"/accounts/groups",
		"/monitor",
		"/monitor/tasks",
		"/monitor/alerts",
		"/messages",
		"/reports",
		"/system",
		"/system/health",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if !strings.Contains(recorder.Body.String(), `aria-current="page"`) {
				t.Error("active page did not expose aria-current")
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

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	renderer, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return renderer.Handler()
}
