// Package web serves the TeleNexus administration interface.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed templates/*.html static/*
var assets embed.FS

type pageData struct {
	Title       string
	Section     string
	Description string
	ActivePath  string
	Page        string
	Breadcrumbs []breadcrumb
	Account     accountSummary
}

type breadcrumb struct {
	Label string
	Path  string
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
	mux.HandleFunc("GET /accounts", r.placeholder("账号管理", "管理、验证、监控 Telegram 账号"))
	mux.HandleFunc("GET /accounts/groups", r.placeholder("账号分组", "按业务场景组织多个 Telegram 账号"))
	mux.HandleFunc("GET /monitor", r.placeholder("实时监控", "订阅群组、关键词和消息告警"))
	mux.HandleFunc("GET /monitor/tasks", r.placeholder("监控任务", "配置采集范围、频率和通知渠道"))
	mux.HandleFunc("GET /monitor/alerts", r.placeholder("告警记录", "查看已触发的监控告警"))
	mux.HandleFunc("GET /messages", r.placeholder("消息记录", "检索已采集的 Telegram 消息"))
	mux.HandleFunc("GET /reports", r.placeholder("数据报表", "查看账号活跃度和监控趋势"))
	mux.HandleFunc("GET /system", r.placeholder("系统设置", "配置服务、日志与通知渠道"))
	mux.HandleFunc("GET /system/health", r.placeholder("服务状态", "查看 Go 服务与基础设施状态"))
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
		Page:       "dashboard",
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

func (r *Renderer) placeholder(section, description string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		path := "/" + strings.TrimPrefix(req.URL.Path, "/")
		r.render(w, pageData{
			Title:       section,
			Section:     section,
			Description: description,
			ActivePath:  path,
			Page:        "placeholder",
			Breadcrumbs: []breadcrumb{
				{Label: "工作台", Path: "/"},
				{Label: section},
			},
			Account: defaultAccount,
		})
	}
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
