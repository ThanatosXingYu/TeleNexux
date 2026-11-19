package operations

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	service, err := Open(context.Background(), filepath.Join(t.TempDir(), "operations.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return service
}

func TestGroupsCanBeCreatedAssignedAndDeleted(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)

	group, err := service.CreateGroup(ctx, "  Primary  ", "green")
	if err != nil {
		t.Fatalf("CreateGroup() error = %v", err)
	}
	if group.Name != "Primary" || group.Color != "green" {
		t.Fatalf("CreateGroup() = %+v", group)
	}
	if err := service.SetGroupAccounts(ctx, group.ID, []int64{3, 3, 2}); err != nil {
		t.Fatalf("SetGroupAccounts() error = %v", err)
	}
	groups, err := service.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups() error = %v", err)
	}
	if len(groups) != 1 || groups[0].AccountCount != 2 {
		t.Fatalf("ListGroups() = %+v", groups)
	}
	if err := service.DeleteGroup(ctx, group.ID); err != nil {
		t.Fatalf("DeleteGroup() error = %v", err)
	}
	if err := service.DeleteGroup(ctx, group.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteGroup() second error = %v, want ErrNotFound", err)
	}
}

func TestMessagesCreateDeduplicatedAlertsAndRemainSearchable(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	rule, err := service.CreateRule(ctx, "Urgent alerts", 7, "chat", 42, "Ops", []string{"urgent"})
	if err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	message := MessageRecord{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Ops",
		MessageID: 101,
		Sender:    "Alice",
		Text:      "urgent deploy 100%_ok!",
		SentAt:    now,
	}
	inserted, alerts, err := service.IngestMessages(ctx, []MessageRecord{message})
	if err != nil {
		t.Fatalf("IngestMessages() error = %v", err)
	}
	if inserted != 1 || len(alerts) != 1 || alerts[0].RuleID != rule.ID {
		t.Fatalf("IngestMessages() = inserted %d alerts %+v", inserted, alerts)
	}
	inserted, alerts, err = service.IngestMessages(ctx, []MessageRecord{message})
	if err != nil {
		t.Fatalf("IngestMessages() duplicate error = %v", err)
	}
	if inserted != 0 || len(alerts) != 0 {
		t.Fatalf("duplicate ingest = inserted %d alerts %+v", inserted, alerts)
	}

	page, err := service.SearchMessages(ctx, MessageQuery{Query: "100%_ok!", Limit: 20})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("SearchMessages() = %+v", page)
	}
	pending, err := service.ListAlerts(ctx, "pending", 20)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListAlerts() = %+v, %v", pending, err)
	}
	if err := service.ResolveAlert(ctx, pending[0].ID); err != nil {
		t.Fatalf("ResolveAlert() error = %v", err)
	}
	resolved, err := service.ListAlerts(ctx, "resolved", 20)
	if err != nil || len(resolved) != 1 {
		t.Fatalf("resolved alerts = %+v, %v", resolved, err)
	}
	report, err := service.BuildReport(ctx, 7)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	if report.Total != 1 || len(report.ByDay) != 1 || len(report.ByAccount) != 1 {
		t.Fatalf("BuildReport() = %+v", report)
	}
}

func TestDashboardCountsVerifiedOnlineAccountsOnly(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	if _, err := service.db.ExecContext(ctx, `
CREATE TABLE accounts (
	id INTEGER PRIMARY KEY,
	status TEXT NOT NULL,
	last_verified_at TEXT NOT NULL
)`); err != nil {
		t.Fatalf("create accounts table: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO accounts (id, status, last_verified_at) VALUES (1, 'online', ?)`,
		`INSERT INTO accounts (id, status, last_verified_at) VALUES (2, 'online', ?)`,
		`INSERT INTO accounts (id, status, last_verified_at) VALUES (3, 'error', ?)`,
	} {
		verifiedAt := now.Add(-time.Minute).Format(time.RFC3339Nano)
		if query == `INSERT INTO accounts (id, status, last_verified_at) VALUES (2, 'online', ?)` {
			verifiedAt = now.Add(-20 * time.Minute).Format(time.RFC3339Nano)
		}
		if _, err := service.db.ExecContext(ctx, query, verifiedAt); err != nil {
			t.Fatalf("insert account: %v", err)
		}
	}

	dashboard, err := service.Dashboard(ctx, true)
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	if dashboard.AccountCount != 3 || dashboard.OnlineCount != 1 || !dashboard.TelegramReady {
		t.Fatalf("Dashboard() = %+v", dashboard)
	}
}
