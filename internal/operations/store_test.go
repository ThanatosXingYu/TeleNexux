package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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
	if _, err := service.UpdateAlert(ctx, pending[0].ID, AlertUpdateInput{
		Action: "resolve",
		Actor:  "tester",
	}); err != nil {
		t.Fatalf("UpdateAlert(resolve) error = %v", err)
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

func TestMessageSearchUsesFullTextIndexAndKeepsShortQueryFallback(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	message := MessageRecord{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "运营中心",
		MessageID: 301,
		Sender:    "Alice",
		Text:      "中华人民共和国 项目更新",
		SentAt:    now,
	}
	if _, _, err := service.IngestMessages(ctx, []MessageRecord{message}); err != nil {
		t.Fatalf("IngestMessages() error = %v", err)
	}
	for _, query := range []string{"人民共和", "人民", "运营"} {
		page, err := service.SearchMessages(ctx, MessageQuery{Query: query, Limit: 20})
		if err != nil {
			t.Fatalf("SearchMessages(%q) error = %v", query, err)
		}
		if page.Total != 1 || len(page.Items) != 1 {
			t.Fatalf("SearchMessages(%q) = %+v", query, page)
		}
	}

	message.Text = "内容已经更新"
	if _, _, err := service.IngestMessages(ctx, []MessageRecord{message}); err != nil {
		t.Fatalf("IngestMessages(update) error = %v", err)
	}
	page, err := service.SearchMessages(ctx, MessageQuery{Query: "人民共和", Limit: 20})
	if err != nil {
		t.Fatalf("SearchMessages(updated) error = %v", err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("stale full-text result = %+v", page)
	}
}

func TestRuleCooldownSuppressesOnlyRepeatedAlerts(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 12, 30, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	rule, err := service.CreateRuleWithOptions(
		ctx,
		"Cooled alerts",
		7,
		"chat",
		42,
		"Ops",
		1,
		[]string{"deploy"},
		60,
	)
	if err != nil {
		t.Fatalf("CreateRuleWithOptions() error = %v", err)
	}
	if rule.CooldownSeconds != 60 {
		t.Fatalf("rule cooldown = %d, want 60", rule.CooldownSeconds)
	}
	ingest := func(messageID int) int {
		t.Helper()
		inserted, alerts, err := service.IngestMessages(ctx, []MessageRecord{{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    42,
			MessageID: messageID,
			Text:      "deploy now",
			SentAt:    now,
		}})
		if err != nil || inserted != 1 {
			t.Fatalf("IngestMessages(%d) inserted = %d, error = %v", messageID, inserted, err)
		}
		return len(alerts)
	}
	if alerts := ingest(501); alerts != 1 {
		t.Fatalf("first alert count = %d, want 1", alerts)
	}
	if alerts := ingest(502); alerts != 0 {
		t.Fatalf("cooled alert count = %d, want 0", alerts)
	}
	now = now.Add(61 * time.Second)
	if alerts := ingest(503); alerts != 1 {
		t.Fatalf("alert count after cooldown = %d, want 1", alerts)
	}
	if _, err := service.CreateRuleWithOptions(
		ctx,
		"Invalid cooldown",
		7,
		"chat",
		42,
		"Ops",
		1,
		nil,
		-1,
	); err == nil {
		t.Fatal("CreateRuleWithOptions() accepted a negative cooldown")
	}
}

func TestRuleVersionsExclusionsAndPreview(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "rule-versions.db")
	service, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	rule, err := service.CreateRuleWithInput(ctx, RuleCreateInput{
		Name:                  "Production deploys",
		AccountID:             7,
		PeerType:              "chat",
		PeerID:                42,
		PeerTitle:             "Operations",
		NotificationChannelID: 1,
		Keywords:              []string{"deploy", "urgent"},
		ExcludeKeywords:       []string{"test"},
		CooldownSeconds:       30,
		Owner:                 "alice",
		Actor:                 "alice",
	})
	if err != nil {
		t.Fatalf("CreateRuleWithInput() error = %v", err)
	}
	if rule.Version != 1 || rule.Owner != "alice" || rule.CreatedBy != "alice" ||
		len(rule.ExcludeKeywords) != 1 || rule.ExcludeKeywords[0] != "test" {
		t.Fatalf("created rule = %+v", rule)
	}

	for _, message := range []struct {
		id   int
		text string
	}{
		{id: 601, text: "urgent production deploy"},
		{id: 602, text: "urgent test deploy"},
		{id: 603, text: "routine update"},
	} {
		if _, _, err := service.IngestMessages(ctx, []MessageRecord{{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    42,
			PeerTitle: "Operations",
			MessageID: message.id,
			Sender:    "Alice",
			Text:      message.text,
			SentAt:    now,
		}}); err != nil {
			t.Fatalf("IngestMessages(%d) error = %v", message.id, err)
		}
	}
	alerts, err := service.ListAlerts(ctx, "", 20)
	if err != nil {
		t.Fatalf("ListAlerts() error = %v", err)
	}
	if len(alerts) != 1 || alerts[0].MessageID != 601 || alerts[0].RuleVersion != 1 {
		t.Fatalf("alerts = %+v, want only production match", alerts)
	}

	preview, err := service.PreviewRule(ctx, RulePreviewInput{
		AccountID:       7,
		PeerType:        "chat",
		PeerID:          42,
		Keywords:        []string{"deploy", "urgent"},
		ExcludeKeywords: []string{"test"},
		Limit:           10,
	})
	if err != nil {
		t.Fatalf("PreviewRule() error = %v", err)
	}
	if preview.Scanned != 3 || preview.Matched != 1 || preview.Excluded != 1 ||
		len(preview.Samples) != 1 || len(preview.ExcludedItems) != 1 ||
		preview.ExcludedItems[0].ExcludedBy != "test" {
		t.Fatalf("PreviewRule() = %+v", preview)
	}

	now = now.Add(time.Minute)
	enabled := false
	newOwner := "bob"
	updated, err := service.UpdateRule(ctx, rule.ID, RuleUpdateInput{
		Owner:   &newOwner,
		Enabled: &enabled,
		Actor:   "bob",
	})
	if err != nil {
		t.Fatalf("UpdateRule() error = %v", err)
	}
	if updated.Version != 2 || updated.Owner != "bob" || updated.Enabled {
		t.Fatalf("updated rule = %+v", updated)
	}
	versions, err := service.ListRuleVersions(ctx, rule.ID)
	if err != nil {
		t.Fatalf("ListRuleVersions() error = %v", err)
	}
	if len(versions) != 2 || versions[0].Version != 2 || versions[0].Actor != "bob" ||
		versions[0].Enabled || versions[1].Version != 1 {
		t.Fatalf("rule versions = %+v", versions)
	}

	if err := service.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	service, err = Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("reopen operations database: %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	persisted, err := service.GetRule(ctx, rule.ID)
	if err != nil {
		t.Fatalf("GetRule() after reopen error = %v", err)
	}
	if persisted.Version != 2 || persisted.Owner != "bob" || persisted.Enabled {
		t.Fatalf("persisted rule = %+v", persisted)
	}
}

func TestRuleRejectsOverlappingAndOversizedTerms(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	_, err := service.CreateRuleWithInput(ctx, RuleCreateInput{
		Name:                  "Invalid terms",
		AccountID:             7,
		PeerType:              "chat",
		PeerID:                42,
		NotificationChannelID: 1,
		Keywords:              []string{"Deploy"},
		ExcludeKeywords:       []string{"deploy"},
		Actor:                 "tester",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot also be an exclude keyword") {
		t.Fatalf("overlapping terms error = %v", err)
	}
	oversized := make([]string, 51)
	for index := range oversized {
		oversized[index] = fmt.Sprintf("term-%d", index)
	}
	_, err = service.CreateRuleWithInput(ctx, RuleCreateInput{
		Name:                  "Too many terms",
		AccountID:             7,
		PeerType:              "chat",
		PeerID:                42,
		NotificationChannelID: 1,
		Keywords:              oversized,
		Actor:                 "tester",
	})
	if err == nil || !strings.Contains(err.Error(), "at most 50") {
		t.Fatalf("oversized terms error = %v", err)
	}
}

func TestMonitorAlertLifecycleTracksOwnershipAndState(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	if _, err := service.CreateRule(
		ctx,
		"Lifecycle alerts",
		7,
		"chat",
		42,
		"Ops",
		[]string{"urgent"},
	); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	_, alerts, err := service.IngestMessages(ctx, []MessageRecord{{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Ops",
		MessageID: 301,
		Text:      "urgent review",
		SentAt:    now,
	}})
	if err != nil {
		t.Fatalf("IngestMessages() error = %v", err)
	}
	if len(alerts) != 1 ||
		alerts[0].State != "pending" ||
		alerts[0].Severity != "medium" {
		t.Fatalf("created alert = %+v", alerts)
	}
	alertID := alerts[0].ID

	updated, err := service.UpdateAlert(ctx, alertID, AlertUpdateInput{
		Action: "acknowledge",
		Actor:  "operator-a",
		Note:   "started review",
	})
	if err != nil {
		t.Fatalf("UpdateAlert(acknowledge) error = %v", err)
	}
	if updated.State != "acknowledged" ||
		updated.AcknowledgedBy != "operator-a" ||
		updated.AcknowledgedAt == nil {
		t.Fatalf("acknowledged alert = %+v", updated)
	}

	updated, err = service.UpdateAlert(ctx, alertID, AlertUpdateInput{
		Action:   "assign",
		Assignee: "operator-b",
		Actor:    "operator-a",
	})
	if err != nil {
		t.Fatalf("UpdateAlert(assign) error = %v", err)
	}
	if updated.AssignedTo != "operator-b" {
		t.Fatalf("assigned alert = %+v", updated)
	}

	updated, err = service.UpdateAlert(ctx, alertID, AlertUpdateInput{
		Action:   "set_severity",
		Severity: "critical",
		Actor:    "operator-a",
	})
	if err != nil {
		t.Fatalf("UpdateAlert(set_severity) error = %v", err)
	}
	if updated.Severity != "critical" {
		t.Fatalf("severity update = %+v", updated)
	}

	updated, err = service.UpdateAlert(ctx, alertID, AlertUpdateInput{
		Action: "resolve",
		Actor:  "operator-b",
		Note:   "verified",
	})
	if err != nil {
		t.Fatalf("UpdateAlert(resolve) error = %v", err)
	}
	if updated.State != "resolved" ||
		updated.ResolvedBy != "operator-b" ||
		updated.ResolvedAt == nil {
		t.Fatalf("resolved alert = %+v", updated)
	}
	if _, err := service.UpdateAlert(ctx, alertID, AlertUpdateInput{
		Action:   "assign",
		Assignee: "operator-c",
		Actor:    "operator-b",
	}); !errors.Is(err, ErrAlertConflict) {
		t.Fatalf("resolved assignment error = %v, want ErrAlertConflict", err)
	}

	updated, err = service.UpdateAlert(ctx, alertID, AlertUpdateInput{
		Action: "reopen",
		Actor:  "operator-a",
	})
	if err != nil {
		t.Fatalf("UpdateAlert(reopen) error = %v", err)
	}
	if updated.State != "pending" ||
		updated.AcknowledgedBy != "" ||
		updated.ResolvedBy != "" ||
		updated.AssignedTo != "operator-b" {
		t.Fatalf("reopened alert = %+v", updated)
	}

	rows, err := service.db.QueryContext(ctx, `
SELECT action, actor, note
FROM monitor_alert_events
WHERE alert_id = ?
ORDER BY sequence`, alertID)
	if err != nil {
		t.Fatalf("query monitor alert events: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var events []string
	for rows.Next() {
		var action, actor, note string
		if err := rows.Scan(&action, &actor, &note); err != nil {
			t.Fatalf("scan monitor alert event: %v", err)
		}
		events = append(events, action+":"+actor+":"+note)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate monitor alert events: %v", err)
	}
	want := []string{
		"created:system:",
		"acknowledged:operator-a:started review",
		"assigned:operator-a:assignee=operator-b",
		"severity_changed:operator-a:severity=critical",
		"resolved:operator-b:verified",
		"reopened:operator-a:",
	}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("monitor alert events = %v, want %v", events, want)
	}
}

func TestMonitorAlertMigrationBackfillsLifecycleDefaults(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-operations.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE monitor_alerts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	rule_id INTEGER NOT NULL,
	account_id INTEGER NOT NULL,
	peer_type TEXT NOT NULL,
	peer_id INTEGER NOT NULL,
	peer_title TEXT NOT NULL DEFAULT '',
	message_id INTEGER NOT NULL,
	matched_keyword TEXT NOT NULL DEFAULT '',
	message_preview TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL DEFAULT 'pending',
	created_at TEXT NOT NULL,
	UNIQUE(rule_id, message_id)
)`); err != nil {
		t.Fatalf("create legacy monitor alerts: %v", err)
	}
	const createdAt = "2026-11-20T10:00:00Z"
	if _, err := db.ExecContext(ctx, `
INSERT INTO monitor_alerts (
	rule_id, account_id, peer_type, peer_id, peer_title, message_id,
	matched_keyword, message_preview, state, created_at
) VALUES (1, 7, 'chat', 42, 'Ops', 501, 'urgent', 'legacy alert', 'pending', ?)`,
		createdAt,
	); err != nil {
		t.Fatalf("insert legacy monitor alert: %v", err)
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
	alert, err := service.GetAlert(ctx, 1)
	if err != nil {
		t.Fatalf("GetAlert() error = %v", err)
	}
	if alert.Severity != "medium" ||
		alert.UpdatedAt.Format(time.RFC3339) != createdAt ||
		alert.DeliveryState != "" {
		t.Fatalf("migrated alert = %+v", alert)
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
	if alerts[0].DeliveryID <= 0 ||
		alerts[0].DeliveryState != "delivered" ||
		alerts[0].DeliveryCount != 1 ||
		alerts[0].DeliveryAt == nil ||
		!alerts[0].DeliveryAt.Equal(now) {
		t.Fatalf("IngestMessages() delivery = %+v", alerts[0])
	}
	listed, err := service.ListAlerts(ctx, "pending", 20)
	if err != nil {
		t.Fatalf("ListAlerts() error = %v", err)
	}
	if len(listed) != 1 ||
		listed[0].DeliveryState != "delivered" ||
		listed[0].DeliveryAt == nil ||
		!listed[0].DeliveryAt.Equal(now) {
		t.Fatalf("ListAlerts() delivery = %+v", listed)
	}
	var deliveryCount int
	if err := service.db.QueryRowContext(
		ctx,
		`SELECT COUNT(1) FROM notification_deliveries`,
	).Scan(&deliveryCount); err != nil {
		t.Fatalf("count notification deliveries: %v", err)
	}
	if deliveryCount != 1 {
		t.Fatalf("notification delivery count = %d, want 1", deliveryCount)
	}
	_, duplicateAlerts, err := service.IngestMessages(ctx, []MessageRecord{{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Ops",
		MessageID: 200,
		Text:      "deploy now",
		SentAt:    now,
	}})
	if err != nil {
		t.Fatalf("duplicate IngestMessages() error = %v", err)
	}
	if len(duplicateAlerts) != 0 {
		t.Fatalf("duplicate IngestMessages() alerts = %+v", duplicateAlerts)
	}
	if err := service.db.QueryRowContext(
		ctx,
		`SELECT COUNT(1) FROM notification_deliveries`,
	).Scan(&deliveryCount); err != nil {
		t.Fatalf("count duplicate notification deliveries: %v", err)
	}
	if deliveryCount != 1 {
		t.Fatalf("duplicate notification delivery count = %d, want 1", deliveryCount)
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

func TestPurgeAccountHonorsLegalHoldAndRetainsDestructionRecord(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	hold, err := service.CreateLegalHold(ctx, LegalHoldInput{
		Scope:     "account",
		AccountID: 7,
		Reason:    "Matter 2026-11",
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatalf("CreateLegalHold() error = %v", err)
	}
	for _, query := range []string{
		`INSERT INTO monitor_checkpoints
			(account_id, peer_type, peer_id, last_message_id, updated_at)
			VALUES (7, 'chat', 42, 101, '2026-11-20T10:00:00Z')`,
		`INSERT INTO ingestion_batches
			(batch_id, source, account_id, peer_type, peer_id, status, started_at, finished_at)
			VALUES ('batch-7', 'test', 7, 'chat', 42, 'completed',
				'2026-11-20T10:00:00Z', '2026-11-20T10:00:00Z')`,
		`INSERT INTO evidence_events
			(event_id, batch_id, account_id, peer_type, peer_id, message_id, event_type,
				version, observed_at, content_sha256, schema_version)
			VALUES ('event-7', 'batch-7', 7, 'chat', 42, 101, 'message', 1,
				'2026-11-20T10:00:00Z', 'hash', 1)`,
	} {
		if _, err := service.db.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed account data: %v", err)
		}
	}

	if err := service.PurgeAccount(ctx, 7); !errors.Is(err, ErrActiveLegalHold) {
		t.Fatalf("PurgeAccount() error = %v, want ErrActiveLegalHold", err)
	}
	if _, err := service.ReleaseLegalHold(ctx, hold.ID, "operator"); err != nil {
		t.Fatalf("ReleaseLegalHold() error = %v", err)
	}
	if err := service.PurgeAccount(ctx, 7); err != nil {
		t.Fatalf("PurgeAccount() error = %v", err)
	}

	for _, table := range []string{
		"monitor_checkpoints",
		"ingestion_batches",
		"evidence_events",
	} {
		var count int
		if err := service.db.QueryRowContext(
			ctx,
			"SELECT COUNT(1) FROM "+table+" WHERE account_id = 7",
		).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s kept %d account rows", table, count)
		}
	}
	var destructionCount int
	if err := service.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM account_destructions WHERE account_id = 7`).Scan(
		&destructionCount,
	); err != nil || destructionCount != 1 {
		t.Fatalf("account destruction count = %d, %v", destructionCount, err)
	}
	if _, err := service.db.ExecContext(ctx, `
DELETE FROM account_destructions WHERE account_id = 7`); err == nil {
		t.Fatal("account destruction record must be append-only")
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
	last_verified_at TEXT NOT NULL,
	purge_state TEXT NOT NULL DEFAULT 'active'
)`); err != nil {
		t.Fatalf("create accounts table: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO accounts (id, status, last_verified_at, purge_state)
			VALUES (1, 'online', ?, 'active')`,
		`INSERT INTO accounts (id, status, last_verified_at, purge_state)
			VALUES (2, 'online', ?, 'active')`,
		`INSERT INTO accounts (id, status, last_verified_at, purge_state)
			VALUES (3, 'error', ?, 'active')`,
		`INSERT INTO accounts (id, status, last_verified_at, purge_state)
			VALUES (4, 'online', ?, 'scheduled')`,
	} {
		verifiedAt := now.Add(-time.Minute).Format(time.RFC3339Nano)
		if strings.Contains(query, "VALUES (2,") {
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
