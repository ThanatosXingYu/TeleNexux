package health

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

type fakePinger struct {
	err error
}

func (p fakePinger) PingContext(context.Context) error {
	return p.err
}

func TestCheckerReportsHealthyDependencies(t *testing.T) {
	directory := t.TempDir()
	checker := NewChecker(Options{
		Database:           fakePinger{},
		SessionDirectory:   directory,
		AuthPath:           filepath.Join(directory, "auth.json"),
		TelegramConfigured: true,
		AdminConfigured:    func() bool { return true },
	})
	report := checker.Check(context.Background(), true)
	if report.Status != "ok" {
		t.Fatalf("Check() status = %q, checks = %+v", report.Status, report.Checks)
	}
	if len(report.Checks) != 5 {
		t.Fatalf("Check() returned %d checks, want 5", len(report.Checks))
	}
}

func TestCheckerReportsDatabaseFailure(t *testing.T) {
	checker := NewChecker(Options{
		Database:         fakePinger{err: errors.New("database unavailable")},
		SessionDirectory: t.TempDir(),
		AuthPath:         "/tmp/auth.json",
		AdminConfigured:  func() bool { return false },
	})
	report := checker.Check(context.Background(), false)
	if report.Status != "error" {
		t.Fatalf("Check() status = %q, want error", report.Status)
	}
	for _, check := range report.Checks {
		if check.Status != "ok" && check.Message != "依赖不可用" {
			t.Fatalf("public health check leaked detail: %+v", check)
		}
	}
}
