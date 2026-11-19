package operations

import (
	"context"
	"database/sql"
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

func TestNotificationChannelIsAppliedToRulesAndAlerts(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	channels, err := service.ListNotificationChannels(ctx)
	if err != nil {
		t.Fatalf("ListNotificationChannels() error = %v", err)
	}
	if len(channels) != 1 || channels[0].ID != 1 ||
		channels[0].Kind != "in_app" || !channels[0].Enabled {
		t.Fatalf("ListNotificationChannels() = %+v", channels)
	}

	rule, err := service.CreateRuleWithChannel(
		ctx,
		"Channel alerts",
		7,
		"chat",
		42,
		"Ops",
		channels[0].ID,
		[]string{"deploy"},
	)
	if err != nil {
		t.Fatalf("CreateRuleWithChannel() error = %v", err)
	}
	if rule.NotificationChannelID != 1 || rule.NotificationChannelName != "站内通知" {
		t.Fatalf("CreateRuleWithChannel() = %+v", rule)
	}

	_, alerts, err := service.IngestMessages(ctx, []MessageRecord{{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Ops",
		MessageID: 200,
		Text:      "deploy now",
		SentAt:    now,
	}})
	if err != nil {
		t.Fatalf("IngestMessages() error = %v", err)
	}
	if len(alerts) != 1 || alerts[0].ChannelID != 1 ||
		alerts[0].ChannelName != "站内通知" {
		t.Fatalf("IngestMessages() alerts = %+v", alerts)
	}
}

func TestCreateRuleValidatesNotificationChannel(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	if _, err := service.db.ExecContext(
		ctx,
		`UPDATE notification_channels SET enabled = 0 WHERE id = 1`,
	); err != nil {
		t.Fatalf("disable notification channel: %v", err)
	}
	if _, err := service.CreateRuleWithChannel(
		ctx,
		"Disabled",
		7,
		"chat",
		42,
		"Ops",
		1,
		nil,
	); err == nil || err.Error() != "notification channel is disabled" {
		t.Fatalf("CreateRuleWithChannel() disabled error = %v", err)
	}
	if _, err := service.CreateRuleWithChannel(
		ctx,
		"Missing",
		7,
		"chat",
		42,
		"Ops",
		999,
		nil,
	); err == nil || err.Error() != "notification channel was not found" {
		t.Fatalf("CreateRuleWithChannel() missing error = %v", err)
	}
}

func TestOpenMigratesLegacyMonitorRulesToDefaultChannel(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE monitor_rules (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	peer_title TEXT NOT NULL DEFAULT '',
	keywords_json TEXT NOT NULL DEFAULT '[]',
	enabled INTEGER NOT NULL DEFAULT 1,
	last_message_id INTEGER NOT NULL DEFAULT 0,
	last_run_at TEXT NOT NULL DEFAULT '',
	last_error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	service, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() migrated database error = %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	result, err := service.db.ExecContext(ctx, `
INSERT INTO monitor_rules (
	name, account_id, peer_type, peer_id, peer_title, keywords_json, enabled,
	last_message_id, last_run_at, last_error, created_at, updated_at
) VALUES ('Legacy', 7, 'chat', 42, 'Ops', '[]', 1, 0, '', '', ?, ?)`,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insert legacy rule: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read legacy rule ID: %v", err)
	}
	rule, err := service.GetRule(ctx, id)
	if err != nil {
		t.Fatalf("GetRule() error = %v", err)
	}
	if rule.NotificationChannelID != 1 || rule.NotificationChannelName != "站内通知" {
		t.Fatalf("migrated rule = %+v", rule)
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
