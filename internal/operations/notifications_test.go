package operations

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWebhookNotificationQueuesAndCompletesDelivery(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	channel, err := service.CreateWebhookChannel(
		ctx,
		"Security webhook",
		"https://hooks.example.test/telenexus",
		"signing-secret-1234",
	)
	if err != nil {
		t.Fatalf("CreateWebhookChannel() error = %v", err)
	}
	if channel.Kind != NotificationKindWebhook || !channel.Configured {
		t.Fatalf("CreateWebhookChannel() = %+v", channel)
	}
	if _, err := service.CreateRuleWithChannel(
		ctx,
		"Webhook alerts",
		7,
		"chat",
		42,
		"Ops",
		channel.ID,
		[]string{"deploy"},
	); err != nil {
		t.Fatalf("CreateRuleWithChannel() error = %v", err)
	}
	_, alerts, err := service.IngestMessages(ctx, []MessageRecord{{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Ops",
		MessageID: 201,
		Text:      "deploy now",
		SentAt:    now,
	}})
	if err != nil {
		t.Fatalf("IngestMessages() error = %v", err)
	}
	if len(alerts) != 1 ||
		alerts[0].DeliveryState != "queued" ||
		alerts[0].DeliveryCount != 0 ||
		alerts[0].DeliveryAt != nil {
		t.Fatalf("IngestMessages() delivery = %+v", alerts)
	}

	delivery, found, err := service.ClaimNotificationDelivery(ctx)
	if err != nil {
		t.Fatalf("ClaimNotificationDelivery() error = %v", err)
	}
	if !found || delivery.ID == 0 || delivery.AttemptCount != 1 ||
		delivery.ChannelKind != NotificationKindWebhook {
		t.Fatalf("ClaimNotificationDelivery() = %+v, %v", delivery, found)
	}
	if err := service.CompleteNotificationDelivery(ctx, delivery.ID); err != nil {
		t.Fatalf("CompleteNotificationDelivery() error = %v", err)
	}
	listed, err := service.ListAlerts(ctx, "pending", 20)
	if err != nil {
		t.Fatalf("ListAlerts() error = %v", err)
	}
	if len(listed) != 1 ||
		listed[0].DeliveryState != "delivered" ||
		listed[0].DeliveryCount != 1 ||
		listed[0].DeliveryAt == nil {
		t.Fatalf("ListAlerts() delivery = %+v", listed)
	}
}

func TestWebhookNotificationRetriesWithBackoffAndStops(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	channel, err := service.CreateWebhookChannel(
		ctx,
		"Retry webhook",
		"https://hooks.example.test/retry",
		"",
	)
	if err != nil {
		t.Fatalf("CreateWebhookChannel() error = %v", err)
	}
	if _, err := service.CreateRuleWithChannel(
		ctx,
		"Retry alerts",
		7,
		"chat",
		42,
		"Ops",
		channel.ID,
		nil,
	); err != nil {
		t.Fatalf("CreateRuleWithChannel() error = %v", err)
	}
	if _, _, err := service.IngestMessages(ctx, []MessageRecord{{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		MessageID: 202,
		Text:      "message",
		SentAt:    now,
	}}); err != nil {
		t.Fatalf("IngestMessages() error = %v", err)
	}
	delivery, found, err := service.ClaimNotificationDelivery(ctx)
	if err != nil || !found {
		t.Fatalf("ClaimNotificationDelivery() = %+v, %v, %v", delivery, found, err)
	}
	state, nextAttempt, err := service.RetryNotificationDelivery(
		ctx,
		delivery.ID,
		"temporary failure",
	)
	if err != nil {
		t.Fatalf("RetryNotificationDelivery() error = %v", err)
	}
	if state != "queued" || nextAttempt == nil ||
		!nextAttempt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("RetryNotificationDelivery() = %q, %v", state, nextAttempt)
	}
	if _, err := service.db.ExecContext(
		ctx,
		`UPDATE notification_deliveries SET attempt_count = ? WHERE id = ?`,
		notificationMaxAttempts,
		delivery.ID,
	); err != nil {
		t.Fatalf("set delivery attempts: %v", err)
	}
	state, nextAttempt, err = service.RetryNotificationDelivery(
		ctx,
		delivery.ID,
		"final failure",
	)
	if err != nil {
		t.Fatalf("RetryNotificationDelivery(final) error = %v", err)
	}
	if state != "failed" || nextAttempt != nil {
		t.Fatalf("RetryNotificationDelivery(final) = %q, %v", state, nextAttempt)
	}
}

func TestNotificationClaimUsesLeaseToAvoidConcurrentDelivery(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	channel, err := service.CreateWebhookChannel(
		ctx,
		"Leased webhook",
		"https://hooks.example.test/lease",
		"",
	)
	if err != nil {
		t.Fatalf("CreateWebhookChannel() error = %v", err)
	}
	if _, err := service.CreateRuleWithChannel(
		ctx,
		"Leased alerts",
		7,
		"chat",
		42,
		"Ops",
		channel.ID,
		nil,
	); err != nil {
		t.Fatalf("CreateRuleWithChannel() error = %v", err)
	}
	if _, _, err := service.IngestMessages(ctx, []MessageRecord{{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		MessageID: 203,
		Text:      "message",
		SentAt:    now,
	}}); err != nil {
		t.Fatalf("IngestMessages() error = %v", err)
	}

	first, found, err := service.ClaimNotificationDelivery(ctx)
	if err != nil || !found || first.AttemptCount != 1 {
		t.Fatalf("first ClaimNotificationDelivery() = %+v, %v, %v", first, found, err)
	}
	if _, found, err := service.ClaimNotificationDelivery(ctx); err != nil || found {
		t.Fatalf("concurrent ClaimNotificationDelivery() found = %v, error = %v", found, err)
	}
	now = now.Add(notificationClaimLease + time.Second)
	reclaimed, found, err := service.ClaimNotificationDelivery(ctx)
	if err != nil || !found || reclaimed.ID != first.ID || reclaimed.AttemptCount != 2 {
		t.Fatalf("expired lease ClaimNotificationDelivery() = %+v, %v, %v",
			reclaimed, found, err)
	}
}

func TestWebhookChannelRejectsUnsafeInput(t *testing.T) {
	service := newTestService(t)
	if _, err := service.CreateWebhookChannel(
		context.Background(),
		"FTP",
		"ftp://example.test/hook",
		"",
	); err == nil {
		t.Fatal("CreateWebhookChannel() accepted a non-HTTP URL")
	}
	if _, err := service.CreateWebhookChannel(
		context.Background(),
		"Bad secret",
		"https://example.test/hook",
		"short",
	); err == nil {
		t.Fatal("CreateWebhookChannel() accepted a short signing secret")
	}
}

func TestDeleteNotificationChannelProtectsBuiltInAndReferencedChannels(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	if err := service.DeleteNotificationChannel(ctx, 1); !errors.Is(err, ErrProtectedChannel) {
		t.Fatalf("DeleteNotificationChannel(built-in) error = %v", err)
	}
	channel, err := service.CreateWebhookChannel(
		ctx,
		"Disposable webhook",
		"https://hooks.example.test/disposable",
		"",
	)
	if err != nil {
		t.Fatalf("CreateWebhookChannel() error = %v", err)
	}
	rule, err := service.CreateRuleWithChannel(
		ctx,
		"Uses disposable channel",
		7,
		"chat",
		42,
		"Ops",
		channel.ID,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateRuleWithChannel() error = %v", err)
	}
	if err := service.DeleteNotificationChannel(
		ctx,
		channel.ID,
	); !errors.Is(err, ErrNotificationChannelUse) {
		t.Fatalf("DeleteNotificationChannel(referenced) error = %v", err)
	}
	if err := service.DeleteRule(ctx, rule.ID); err != nil {
		t.Fatalf("DeleteRule() error = %v", err)
	}
	if err := service.DeleteNotificationChannel(ctx, channel.ID); err != nil {
		t.Fatalf("DeleteNotificationChannel() error = %v", err)
	}
	if err := service.DeleteNotificationChannel(ctx, channel.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteNotificationChannel(missing) error = %v", err)
	}
}

func TestOpenMigratesNotificationDeliveryRetryColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-notifications.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE notification_deliveries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	alert_id INTEGER NOT NULL,
	channel_id INTEGER NOT NULL,
	channel_kind TEXT NOT NULL,
	state TEXT NOT NULL,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	last_attempt_at TEXT NOT NULL DEFAULT '',
	delivered_at TEXT NOT NULL DEFAULT '',
	error_text TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`); err != nil {
		t.Fatalf("create legacy delivery table: %v", err)
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
	var columnCount int
	if err := service.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM pragma_table_info('notification_deliveries')
WHERE name = 'next_attempt_at'`).Scan(&columnCount); err != nil {
		t.Fatalf("inspect migrated delivery table: %v", err)
	}
	if columnCount != 1 {
		t.Fatalf("next_attempt_at column count = %d", columnCount)
	}
}
