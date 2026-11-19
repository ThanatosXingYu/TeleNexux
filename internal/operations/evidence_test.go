package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIngestBatchTracksMessageVersionsAndDeletion(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	rule, err := service.CreateRule(
		ctx,
		"Evidence alerts",
		7,
		"chat",
		42,
		"Operations",
		[]string{"urgent"},
	)
	if err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	message := MessageRecord{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Operations",
		MessageID: 101,
		Sender:    "Alice",
		Text:      "urgent deploy",
		MediaType: "document",
		SentAt:    now,
	}
	result, err := service.IngestBatch(ctx, IngestBatchInput{
		BatchID:         "batch-initial",
		Source:          "test",
		AccountID:       7,
		PeerType:        "chat",
		PeerID:          42,
		PeerTitle:       "Operations",
		CheckpointAfter: 101,
		Complete:        true,
		Messages:        []MessageRecord{message},
	})
	if err != nil {
		t.Fatalf("IngestBatch() error = %v", err)
	}
	if result.Inserted != 1 || result.Events != 1 ||
		len(result.Alerts) != 1 || result.Alerts[0].RuleID != rule.ID {
		t.Fatalf("IngestBatch() = %+v", result)
	}

	targets, err := service.ListCollectionTargets(ctx)
	if err != nil {
		t.Fatalf("ListCollectionTargets() error = %v", err)
	}
	if len(targets) != 1 || targets[0].LastMessageID != 101 ||
		targets[0].LastSuccessfulAt == nil {
		t.Fatalf("ListCollectionTargets() = %+v", targets)
	}

	message.Text = "urgent deploy updated"
	message.MediaType = "photo"
	result, err = service.IngestBatch(ctx, IngestBatchInput{
		BatchID:          "batch-edit",
		Source:           "test",
		AccountID:        7,
		PeerType:         "chat",
		PeerID:           42,
		PeerTitle:        "Operations",
		CheckpointBefore: 101,
		CheckpointAfter:  101,
		Complete:         true,
		Messages:         []MessageRecord{message},
	})
	if err != nil {
		t.Fatalf("IngestBatch() edit error = %v", err)
	}
	if result.Inserted != 0 || result.Events != 1 || len(result.Alerts) != 0 {
		t.Fatalf("IngestBatch() edit = %+v", result)
	}

	events, err := service.ListEvidenceEvents(ctx, 7, "chat", 42, 101, 10)
	if err != nil {
		t.Fatalf("ListEvidenceEvents() error = %v", err)
	}
	if len(events) != 2 ||
		events[0].EventType != "edit" || events[0].Version != 2 ||
		events[1].EventType != "message" || events[1].Version != 1 {
		t.Fatalf("ListEvidenceEvents() = %+v", events)
	}

	if _, err := service.RecordDeletion(ctx, 7, "chat", 42, []int{101}, "test"); err != nil {
		t.Fatalf("RecordDeletion() error = %v", err)
	}
	events, err = service.ListEvidenceEvents(ctx, 7, "chat", 42, 101, 10)
	if err != nil {
		t.Fatalf("ListEvidenceEvents() after delete error = %v", err)
	}
	if len(events) != 3 || events[0].EventType != "delete" || events[0].Version != 3 {
		t.Fatalf("deletion events = %+v", events)
	}
	var deletedAt string
	if err := service.db.QueryRowContext(ctx, `
SELECT deleted_at
FROM indexed_messages
WHERE account_id = 7 AND peer_type = 'chat' AND peer_id = 42 AND message_id = 101`,
	).Scan(&deletedAt); err != nil {
		t.Fatalf("read message tombstone: %v", err)
	}
	if deletedAt == "" {
		t.Fatal("message projection was not tombstoned")
	}
}

func TestRecordBatchFailureUpdatesCollectionHealth(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	if _, err := service.CreateRule(
		ctx,
		"Failure health",
		8,
		"channel",
		99,
		"Alerts",
		nil,
	); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	input := IngestBatchInput{
		BatchID:          "batch-failed",
		Source:           "test",
		AccountID:        8,
		PeerType:         "channel",
		PeerID:           99,
		PeerTitle:        "Alerts",
		CheckpointBefore: 50,
	}
	if err := service.RecordBatchFailure(ctx, input, errors.New("Telegram unavailable")); err != nil {
		t.Fatalf("RecordBatchFailure() error = %v", err)
	}

	targets, err := service.ListCollectionTargets(ctx)
	if err != nil {
		t.Fatalf("ListCollectionTargets() error = %v", err)
	}
	if len(targets) != 1 || targets[0].ConsecutiveFailures != 1 ||
		targets[0].LastError != "Telegram unavailable" ||
		targets[0].LastMessageID != 50 {
		t.Fatalf("ListCollectionTargets() = %+v", targets)
	}
	summary, err := service.MonitorSummary(ctx)
	if err != nil {
		t.Fatalf("MonitorSummary() error = %v", err)
	}
	if summary.TargetCount != 1 || summary.ConsecutiveFailures != 1 {
		t.Fatalf("MonitorSummary() = %+v", summary)
	}
}

func TestIngestBatchRejectsInvalidScope(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	if _, err := service.IngestBatch(ctx, IngestBatchInput{
		Source:    "test",
		AccountID: 7,
		PeerType:  "unsupported",
		PeerID:    42,
	}); err == nil {
		t.Fatal("IngestBatch() accepted an unsupported peer type")
	}
}
