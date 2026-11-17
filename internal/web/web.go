// Package web serves the TeleNexus administration interface.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
)

//go:embed templates/*.html static/*
var assets embed.FS

type pageData struct {
	Title       string
	Section     string
	Description string
	ActivePath  string
	TabPath     string
	Page        string
	Breadcrumbs []breadcrumb
	Navigation  []navGroup
	Account     accountSummary
	AccountID   int64
}

type breadcrumb struct {
	Label string
	Path  string
}

type navGroup struct {
	ID     string
	Label  string
	Icon   string
	Path   string
	Active bool
	Items  []navItem
}

type navItem struct {
	Label string
	Path  string
}

type pageDefinition struct {
	Path        string
	Group       string
	Title       string
	Description string
}

type accountSummary struct {
	Name     string
	Initials string
	Role     string
}

type Renderer struct {
	templates  *template.Template
	fileServer http.Handler
}

func New() (*Renderer, error) {
	templates, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse web templates: %w", err)
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, fmt.Errorf("open static assets: %w", err)
	}
	return &Renderer{
		templates:  templates,
		fileServer: http.FileServer(http.FS(staticFS)),
	}, nil
}

func (r *Renderer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", r.fileServer))
	mux.HandleFunc("GET /", r.dashboard)
	mux.HandleFunc("GET /login", r.login)
	mux.HandleFunc("GET /accounts", r.accounts)
	mux.HandleFunc("GET /accounts/{id}/web", r.accountWorkspace)
	mux.HandleFunc("GET /accounts/{id}", r.accountDetail)
	for _, page := range pages {
		if page.Path == "/accounts" || page.Path == "/system" {
			continue
		}
		mux.HandleFunc("GET "+page.Path, r.placeholder(page))
	}
	mux.HandleFunc("GET /system", r.system)
	return secureHeaders(mux)
}

func (r *Renderer) dashboard(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		r.notFound(w, req)
		return
	}
	r.render(w, pageData{
		Title:      "工作台",
		Section:    "工作台",
		ActivePath: "/",
		TabPath:    "/",
		Page:       "dashboard",
		Navigation: navigation("/"),
		Breadcrumbs: []breadcrumb{
			{Label: "工作台"},
		},
		Account: defaultAccount,
	})
}

func (r *Renderer) login(w http.ResponseWriter, _ *http.Request) {
	data := pageData{
		Title:   "登录",
		Section: "登录",
		Page:    "login",
	}
	if err := r.templates.ExecuteTemplate(w, "login.html", data); err != nil {
		http.Error(w, "render login page", http.StatusInternalServerError)
	}
}

func (r *Renderer) accounts(w http.ResponseWriter, _ *http.Request) {
	r.render(w, pageData{
		Title:       "账号列表",
		Section:     "账号列表",
		ActivePath:  "/accounts",
		TabPath:     "/accounts",
		Page:        "accounts",
		Navigation:  navigation("/accounts"),
		Breadcrumbs: []breadcrumb{{Label: "工作台", Path: "/"}, {Label: "账号管理"}, {Label: "账号列表"}},
		Account:     defaultAccount,
	})
}

func (r *Renderer) accountDetail(w http.ResponseWriter, req *http.Request) {
	accountID, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil || accountID <= 0 {
		r.notFound(w, req)
		return
	}
	r.render(w, pageData{
		Title:       "账号详情",
		Section:     "账号详情",
		ActivePath:  "/accounts",
		TabPath:     req.URL.Path,
		Page:        "account-detail",
		Navigation:  navigation("/accounts"),
		Breadcrumbs: []breadcrumb{{Label: "工作台", Path: "/"}, {Label: "账号管理", Path: "/accounts"}, {Label: "聊天查看"}},
		Account:     defaultAccount,
		AccountID:   accountID,
	})
}

func (r *Renderer) accountWorkspace(w http.ResponseWriter, req *http.Request) {
	accountID, err := strconv.ParseInt(req.PathValue("id"), 10, 64)
	if err != nil || accountID <= 0 {
		r.notFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.templates.ExecuteTemplate(w, "telegram_client.html", struct {
		AccountID int64
	}{AccountID: accountID}); err != nil {
		http.Error(w, "render Telegram client", http.StatusInternalServerError)
	}
}

func (r *Renderer) system(w http.ResponseWriter, _ *http.Request) {
	r.render(w, pageData{
		Title:       "系统配置",
		Section:     "系统配置",
		Description: "配置运行环境、Telegram 凭据、接码网络和服务监听地址",
		ActivePath:  "/system",
		TabPath:     "/system",
		Page:        "system",
		Navigation:  navigation("/system"),
		Breadcrumbs: []breadcrumb{{Label: "工作台", Path: "/"}, {Label: "系统配置"}, {Label: "基础配置"}},
		Account:     defaultAccount,
	})
}

func (r *Renderer) placeholder(page pageDefinition) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		path := "/" + strings.TrimPrefix(req.URL.Path, "/")
		groupLabel := groupLabelForPath(page.Path)
		breadcrumbs := []breadcrumb{{Label: "工作台", Path: "/"}}
		if groupLabel != "" {
			breadcrumbs = append(breadcrumbs, breadcrumb{Label: groupLabel})
		}
		breadcrumbs = append(breadcrumbs, breadcrumb{Label: page.Title})
		r.render(w, pageData{
			Title:       page.Title,
			Section:     page.Title,
			Description: page.Description,
			ActivePath:  path,
			TabPath:     path,
			Page:        "placeholder",
			Navigation:  navigation(path),
			Breadcrumbs: breadcrumbs,
			Account:     defaultAccount,
		})
	}
}

var pages = []pageDefinition{
	{Path: "/accounts", Group: "accounts", Title: "账号列表", Description: "管理、验证、监控 Telegram 账号"},
	{Path: "/accounts/groups", Group: "accounts", Title: "账号分组", Description: "按业务场景组织多个 Telegram 账号"},
	{Path: "/monitor", Group: "monitor", Title: "实时监控", Description: "订阅群组、关键词和消息告警"},
	{Path: "/monitor/tasks", Group: "monitor", Title: "监控任务", Description: "配置采集范围、频率和通知渠道"},
	{Path: "/monitor/alerts", Group: "monitor", Title: "告警记录", Description: "查看已触发的监控告警"},
	{Path: "/messages", Group: "data", Title: "消息记录", Description: "检索已采集的 Telegram 消息"},
	{Path: "/reports", Group: "data", Title: "数据报表", Description: "查看账号活跃度和监控趋势"},
	{Path: "/system", Group: "system", Title: "系统配置", Description: "配置运行环境、Telegram 凭据与服务参数"},
	{Path: "/system/health", Group: "system", Title: "服务状态", Description: "查看 Go 服务与基础设施状态"},
}

func navigation(activePath string) []navGroup {
	groups := []navGroup{
		{ID: "workspace", Label: "工作台", Icon: "layout-dashboard", Path: "/"},
		{
			ID:    "accounts",
			Label: "账号管理",
			Icon:  "users",
			Items: []navItem{
				{Label: "账号列表", Path: "/accounts"},
				{Label: "账号分组", Path: "/accounts/groups"},
			},
		},
		{
			ID:    "monitor",
			Label: "监控中心",
			Icon:  "radio-tower",
			Items: []navItem{
				{Label: "实时监控", Path: "/monitor"},
				{Label: "监控任务", Path: "/monitor/tasks"},
				{Label: "告警记录", Path: "/monitor/alerts"},
			},
		},
		{
			ID:    "data",
			Label: "数据中心",
			Icon:  "messages-square",
			Items: []navItem{
				{Label: "消息记录", Path: "/messages"},
				{Label: "数据报表", Path: "/reports"},
			},
		},
		{
			ID:    "system",
			Label: "系统配置",
			Icon:  "settings",
			Items: []navItem{
				{Label: "基础配置", Path: "/system"},
				{Label: "服务状态", Path: "/system/health"},
			},
		},
	}

	for i := range groups {
		if groups[i].Path == activePath {
			groups[i].Active = true
		}
		for _, item := range groups[i].Items {
			if item.Path == activePath {
				groups[i].Active = true
				break
			}
		}
	}
	return groups
}

func groupLabelForPath(path string) string {
	for _, page := range pages {
		if page.Path != path {
			continue
		}
		for _, group := range navigation(path) {
			if group.ID == page.Group {
				return group.Label
			}
		}
	}
	return ""
}

var defaultAccount = accountSummary{
	Name:     "管理员",
	Initials: "TN",
	Role:     "系统管理员",
}

func (r *Renderer) notFound(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	r.render(w, pageData{
		Title:       "页面不存在",
		Section:     "页面不存在",
		Description: "你访问的页面尚未创建，或链接已经失效。",
		ActivePath:  req.URL.Path,
		Page:        "notfound",
		Navigation:  navigation(req.URL.Path),
		Breadcrumbs: []breadcrumb{
			{Label: "工作台", Path: "/"},
			{Label: "页面不存在"},
		},
		Account: defaultAccount,
	})
}

func (r *Renderer) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := r.templates.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "render page", http.StatusInternalServerError)
	}
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
