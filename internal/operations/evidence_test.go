package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
		Media: MediaRecord{
			Type:          "文件",
			Kind:          "document",
			ID:            7001,
			DCID:          2,
			MimeType:      "application/pdf",
			FileName:      "report.pdf",
			Size:          2048,
			AccessHash:    8002,
			FileReference: "private-reference",
		},
		SentAt: now,
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
		targets[0].LastSuccessfulAt == nil ||
		targets[0].CheckpointDelaySeconds != 0 ||
		targets[0].LastBatchFetched != 1 ||
		targets[0].LastBatchAt == nil {
		t.Fatalf("ListCollectionTargets() = %+v", targets)
	}

	message.Text = "urgent deploy updated"
	message.MediaType = "photo"
	message.Media = MediaRecord{
		Type:   "图片",
		Kind:   "photo",
		ID:     7100,
		Width:  1080,
		Height: 720,
	}
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
	var media map[string]any
	if err := json.Unmarshal(events[1].Media, &media); err != nil {
		t.Fatalf("decode evidence media: %v", err)
	}
	referenceHash, _ := media["reference_sha256"].(string)
	metadataHash, _ := media["metadata_sha256"].(string)
	if media["kind"] != "document" ||
		media["file_name"] != "report.pdf" ||
		media["id"] != float64(7001) ||
		media["storage_state"] != "reference_only" ||
		referenceHash == "" ||
		metadataHash == "" {
		t.Fatalf("evidence media = %+v", media)
	}
	for _, field := range []string{"access_hash", "file_reference"} {
		if _, exists := media[field]; exists {
			t.Fatalf("evidence media retained ephemeral field %q: %+v", field, media)
		}
	}
	if strings.Contains(string(events[1].Raw), "private-reference") ||
		strings.Contains(string(events[1].Raw), "access_hash") {
		t.Fatalf("raw evidence retained media locator: %s", events[1].Raw)
	}
	if events[1].SchemaVersion != 3 {
		t.Fatalf("evidence schema version = %d, want 3", events[1].SchemaVersion)
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

func TestDeletedMessageExcludedFromSearchAndStatistics(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
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
	if _, err := service.db.ExecContext(ctx, `
INSERT INTO accounts (id, status, last_verified_at, purge_state)
VALUES (7, 'offline', ?, 'active')`, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert account: %v", err)
	}
	message := MessageRecord{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Operations",
		MessageID: 101,
		Sender:    "Alice",
		Text:      "deleted search term",
		SentAt:    now,
	}
	if _, _, err := service.IngestMessages(ctx, []MessageRecord{message}); err != nil {
		t.Fatalf("IngestMessages() error = %v", err)
	}
	if _, err := service.RecordDeletion(ctx, 7, "chat", 42, []int{101}, "tester"); err != nil {
		t.Fatalf("RecordDeletion() error = %v", err)
	}

	for _, query := range []string{"", "deleted search term"} {
		page, err := service.SearchMessages(ctx, MessageQuery{Query: query, Limit: 20})
		if err != nil {
			t.Fatalf("SearchMessages(%q) error = %v", query, err)
		}
		if page.Total != 0 || len(page.Items) != 0 {
			t.Fatalf("SearchMessages(%q) returned deleted message: %+v", query, page)
		}
	}
	report, err := service.BuildReport(ctx, 7)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	if report.Total != 0 || len(report.ByDay) != 0 || len(report.ByAccount) != 0 {
		t.Fatalf("BuildReport() counted deleted message: %+v", report)
	}
	dashboard, err := service.Dashboard(ctx, true)
	if err != nil {
		t.Fatalf("Dashboard() error = %v", err)
	}
	if dashboard.TodayMessages != 0 {
		t.Fatalf("Dashboard() counted deleted message: %+v", dashboard)
	}
}

func TestEvidenceMediaReferenceRefreshDoesNotCreateFalseEdit(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 12, 30, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	message := MessageRecord{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Ops",
		MessageID: 150,
		Sender:    "Alice",
		Text:      "photo",
		MediaType: "图片",
		Media: MediaRecord{
			Type:          "图片",
			Kind:          "photo",
			ID:            9001,
			AccessHash:    100,
			FileReference: "first-reference",
		},
		SentAt: now,
	}
	ingest := func(batchID string, item MessageRecord) IngestBatchResult {
		t.Helper()
		result, err := service.IngestBatch(ctx, IngestBatchInput{
			BatchID:         batchID,
			Source:          "test",
			AccountID:       7,
			PeerType:        "chat",
			PeerID:          42,
			PeerTitle:       "Ops",
			Messages:        []MessageRecord{item},
			Complete:        true,
			CheckpointAfter: 150,
		})
		if err != nil {
			t.Fatalf("IngestBatch(%q) error = %v", batchID, err)
		}
		return result
	}
	first := ingest("batch-first-reference", message)
	if first.Events != 1 || first.Inserted != 1 {
		t.Fatalf("first ingest = %+v", first)
	}
	message.Media.AccessHash = 200
	message.Media.FileReference = "refreshed-reference"
	second := ingest("batch-refreshed-reference", message)
	if second.Events != 0 || second.Inserted != 0 {
		t.Fatalf("reference refresh created an edit: %+v", second)
	}
	events, err := service.ListEvidenceEvents(ctx, 7, "chat", 42, 150, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListEvidenceEvents() = %+v, %v", events, err)
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
	if summary.TargetCount != 1 || summary.ConsecutiveFailures != 1 ||
		summary.NeverSuccessfulTargets != 1 {
		t.Fatalf("MonitorSummary() = %+v", summary)
	}
}

func TestMonitorSummaryReportsWorstCheckpointDelayAndFloodWait(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)

	for _, rule := range []struct {
		name      string
		accountID int64
		peerType  string
		peerID    int64
	}{
		{name: "Oldest", accountID: 7, peerType: "chat", peerID: 42},
		{name: "Newest", accountID: 8, peerType: "channel", peerID: 99},
		{name: "Rate limited", accountID: 9, peerType: "user", peerID: 100},
	} {
		if _, err := service.CreateRule(
			ctx,
			rule.name,
			rule.accountID,
			rule.peerType,
			rule.peerID,
			rule.name,
			nil,
		); err != nil {
			t.Fatalf("CreateRule(%q) error = %v", rule.name, err)
		}
	}

	recordSuccess := func(accountID int64, peerType string, peerID int64, messageID int, at time.Time) {
		t.Helper()
		service.now = func() time.Time { return at }
		if _, err := service.IngestBatch(ctx, IngestBatchInput{
			BatchID:         "batch-" + ruleName(accountID),
			Source:          "test",
			AccountID:       accountID,
			PeerType:        peerType,
			PeerID:          peerID,
			CheckpointAfter: messageID,
			Complete:        true,
		}); err != nil {
			t.Fatalf("IngestBatch(%d) error = %v", accountID, err)
		}
	}
	recordSuccess(7, "chat", 42, 10, now.Add(-30*time.Minute))
	recordSuccess(8, "channel", 99, 20, now.Add(-10*time.Minute))

	service.now = func() time.Time { return now }
	if err := service.RecordBatchFailure(ctx, IngestBatchInput{
		Source:           "test",
		AccountID:        9,
		PeerType:         "user",
		PeerID:           100,
		CheckpointBefore: 30,
	}, errors.New("Telegram request is rate limited: retry after 30s")); err != nil {
		t.Fatalf("RecordBatchFailure() error = %v", err)
	}

	summary, err := service.MonitorSummary(ctx)
	if err != nil {
		t.Fatalf("MonitorSummary() error = %v", err)
	}
	if summary.TargetCount != 3 ||
		summary.BacklogTargetCount != 0 ||
		summary.FloodWaitTargetCount != 1 ||
		summary.ConsecutiveFailures != 1 ||
		summary.NeverSuccessfulTargets != 1 ||
		summary.MaxCheckpointDelay != int64((30*time.Minute).Seconds()) {
		t.Fatalf("MonitorSummary() = %+v", summary)
	}
	if summary.LastSuccessfulAt == nil ||
		!summary.LastSuccessfulAt.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("LastSuccessfulAt = %v", summary.LastSuccessfulAt)
	}
}

func ruleName(accountID int64) string {
	return fmt.Sprintf("%d", accountID)
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

func TestEvidenceAndIngestionBatchesAreAppendOnly(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	service.now = func() time.Time {
		return time.Date(2026, 11, 20, 14, 30, 0, 0, time.UTC)
	}
	result, err := service.IngestBatch(ctx, IngestBatchInput{
		BatchID:         "append-only-batch",
		Source:          "test",
		AccountID:       7,
		PeerType:        "chat",
		PeerID:          42,
		CheckpointAfter: 1,
		Complete:        true,
		Messages: []MessageRecord{{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    42,
			MessageID: 1,
			Sender:    "Alice",
			Text:      "original",
			SentAt:    service.now(),
		}},
	})
	if err != nil || result.Events != 1 {
		t.Fatalf("IngestBatch() = %+v, %v", result, err)
	}
	if _, err := service.db.ExecContext(ctx, `
UPDATE evidence_events SET text = 'tampered'
WHERE batch_id = 'append-only-batch'`); err == nil {
		t.Fatal("evidence event update unexpectedly succeeded")
	}
	if _, err := service.db.ExecContext(ctx, `
UPDATE ingestion_batches SET status = 'failed'
WHERE batch_id = 'append-only-batch'`); err == nil {
		t.Fatal("ingestion batch update unexpectedly succeeded")
	}
	var text string
	if err := service.db.QueryRowContext(ctx, `
SELECT text FROM evidence_events WHERE batch_id = 'append-only-batch'`).Scan(&text); err != nil {
		t.Fatalf("read evidence text: %v", err)
	}
	if text != "original" {
		t.Fatalf("evidence text = %q, want original", text)
	}
}

func TestIngestBatchRetryIsIdempotentAndRejectsConflictingContent(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	service.now = func() time.Time {
		return time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	}
	if _, err := service.CreateRule(
		ctx,
		"Retry alerts",
		7,
		"chat",
		42,
		"Operations",
		[]string{"urgent"},
	); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	input := IngestBatchInput{
		BatchID:          "batch-retry",
		Source:           "test",
		AccountID:        7,
		PeerType:         "chat",
		PeerID:           42,
		PeerTitle:        "Operations",
		CheckpointBefore: 100,
		CheckpointAfter:  101,
		Complete:         true,
		Messages: []MessageRecord{{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    42,
			PeerTitle: "Operations",
			MessageID: 101,
			Sender:    "Alice",
			Text:      "urgent deploy",
			SentAt:    service.now(),
		}},
	}
	first, err := service.IngestBatch(ctx, input)
	if err != nil {
		t.Fatalf("first IngestBatch() error = %v", err)
	}
	second, err := service.IngestBatch(ctx, input)
	if err != nil {
		t.Fatalf("retry IngestBatch() error = %v", err)
	}
	if second.BatchID != first.BatchID ||
		second.Inserted != first.Inserted ||
		second.Events != first.Events ||
		len(second.Alerts) != 0 {
		t.Fatalf("retry result = %+v, first = %+v", second, first)
	}
	var batchCount, evidenceCount int
	if err := service.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM ingestion_batches WHERE batch_id = 'batch-retry'`).Scan(
		&batchCount,
	); err != nil {
		t.Fatalf("count ingestion batches: %v", err)
	}
	if err := service.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM evidence_events WHERE batch_id = 'batch-retry'`).Scan(
		&evidenceCount,
	); err != nil {
		t.Fatalf("count evidence events: %v", err)
	}
	if batchCount != 1 || evidenceCount != 1 {
		t.Fatalf("batch/evidence counts = %d/%d, want 1/1", batchCount, evidenceCount)
	}

	input.Messages[0].Text = "different content"
	if _, err := service.IngestBatch(ctx, input); !errors.Is(err, ErrBatchConflict) {
		t.Fatalf("conflicting IngestBatch() error = %v, want ErrBatchConflict", err)
	}
}
