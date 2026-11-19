// Package health checks local dependencies without performing Telegram network requests.
package health

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Pinger interface {
	PingContext(ctx context.Context) error
}

type Options struct {
	Database           Pinger
	SessionDirectory   string
	AuthPath           string
	TelegramConfigured bool
	AdminConfigured    func() bool
}

type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type Report struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at"`
	Checks    []Check   `json:"checks"`
}

type Checker struct {
	options Options
	now     func() time.Time
}

func NewChecker(options Options) *Checker {
	return &Checker{options: options, now: time.Now}
}

func (c *Checker) Check(ctx context.Context, detailed bool) Report {
	report := Report{
		Status:    "ok",
		CheckedAt: c.now().UTC(),
		Checks:    make([]Check, 0, 5),
	}
	add := func(name, status, message string) {
		if !detailed && status != "ok" {
			message = "依赖不可用"
		}
		report.Checks = append(report.Checks, Check{
			Name:    name,
			Status:  status,
			Message: message,
		})
		switch status {
		case "error":
			report.Status = "error"
		case "degraded":
			if report.Status == "ok" {
				report.Status = "degraded"
			}
		}
	}

	if c.options.Database == nil {
		add("sqlite", "error", "数据库未初始化")
	} else if err := c.options.Database.PingContext(ctx); err != nil {
		add("sqlite", "error", fmt.Sprintf("数据库不可用：%v", err))
	} else {
		add("sqlite", "ok", "SQLite 连接正常")
	}

	if strings.TrimSpace(c.options.AuthPath) == "" {
		add("configuration", "error", "管理员凭据路径未配置")
	} else {
		add("configuration", "ok", "运行配置已加载")
	}

	if err := checkWritableDirectory(c.options.SessionDirectory); err != nil {
		add("telegram_sessions", "error", err.Error())
	} else {
		add("telegram_sessions", "ok", "Telegram 会话目录可写")
	}

	if c.options.TelegramConfigured {
		add("telegram_credentials", "ok", "Telegram API 凭据已配置")
	} else {
		add("telegram_credentials", "degraded", "Telegram API 凭据尚未配置")
	}

	if c.options.AdminConfigured == nil || !c.options.AdminConfigured() {
		add("administrator", "degraded", "管理员账号尚未初始化")
	} else {
		add("administrator", "ok", "管理员鉴权已启用")
	}
	return report
}

func checkWritableDirectory(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("Telegram 会话目录未配置")
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("Telegram 会话目录不存在")
	}
	if err != nil {
		return fmt.Errorf("无法读取 Telegram 会话目录：%w", err)
	}
	if !info.IsDir() {
		return errors.New("Telegram 会话路径不是目录")
	}
	if info.Mode().Perm()&0o200 == 0 {
		return errors.New("Telegram 会话目录不可写")
	}
	file, err := os.CreateTemp(path, ".telenexus-health-*")
	if err != nil {
		return fmt.Errorf("Telegram 会话目录写入失败：%w", err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("关闭健康检查文件失败：%w", err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("清理健康检查文件失败：%w", err)
	}
	return nil
}

func EnsureSessionDirectory(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("Telegram session directory must not be empty")
	}
	if err := os.MkdirAll(filepath.Join(path, "accounts"), 0o700); err != nil {
		return fmt.Errorf("create Telegram session directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(path, "pending"), 0o700); err != nil {
		return fmt.Errorf("create pending Telegram session directory: %w", err)
	}
	return nil
}
