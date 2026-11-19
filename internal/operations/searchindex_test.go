package operations

import (
	"context"
	"testing"
	"time"
)

func TestSearchIndexHealthDetectsMissingDocumentAndRebuildRestoresIt(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	if _, err := service.IngestBatch(ctx, IngestBatchInput{
		BatchID:         "search-index-health",
		Source:          "test",
		AccountID:       7,
		PeerType:        "chat",
		PeerID:          42,
		PeerTitle:       "Operations",
		CheckpointAfter: 2,
		Complete:        true,
		Messages: []MessageRecord{
			{
				AccountID: 7,
				PeerType:  "chat",
				PeerID:    42,
				PeerTitle: "Operations",
				MessageID: 1,
				Sender:    "Alice",
				Text:      "active search document",
				SentAt:    now,
			},
			{
				AccountID: 7,
				PeerType:  "chat",
				PeerID:    42,
				PeerTitle: "Operations",
				MessageID: 2,
				Sender:    "Bob",
				Text:      "deleted search document",
				SentAt:    now,
			},
		},
	}); err != nil {
		t.Fatalf("IngestBatch() error = %v", err)
	}
	if _, err := service.RecordDeletion(ctx, 7, "chat", 42, []int{2}, "tester"); err != nil {
		t.Fatalf("RecordDeletion() error = %v", err)
	}

	health, err := service.SearchIndexHealth(ctx)
	if err != nil {
		t.Fatalf("SearchIndexHealth() error = %v", err)
	}
	if health.Status != SearchIndexHealthy ||
		health.ProjectionCount != 2 ||
		health.SearchableCount != 1 ||
		health.DocumentCount != 2 ||
		health.DeletedIndexedCount != 1 ||
		!health.IntegrityOK ||
		health.NeedsRebuild {
		t.Fatalf("initial search index health = %+v", health)
	}

	var rowID int
	var text, sender, peerTitle string
	if err := service.db.QueryRowContext(ctx, `
SELECT rowid, text, sender, peer_title
FROM indexed_messages
WHERE account_id = 7 AND peer_type = 'chat' AND peer_id = 42 AND message_id = 1`,
	).Scan(&rowID, &text, &sender, &peerTitle); err != nil {
		t.Fatalf("read active projection: %v", err)
	}
	if _, err := service.db.ExecContext(ctx, `
INSERT INTO indexed_messages_fts(
	indexed_messages_fts, rowid, text, sender, peer_title
) VALUES ('delete', ?, ?, ?, ?)`,
		rowID,
		text,
		sender,
		peerTitle,
	); err != nil {
		t.Fatalf("remove active search document: %v", err)
	}
	health, err = service.SearchIndexHealth(ctx)
	if err != nil {
		t.Fatalf("SearchIndexHealth(degraded) error = %v", err)
	}
	if health.Status != SearchIndexDegraded ||
		health.MissingCount != 1 ||
		health.IntegrityOK ||
		!health.NeedsRebuild {
		t.Fatalf("degraded search index health = %+v", health)
	}

	eventsBefore, err := service.ListEvidenceEvents(ctx, 7, "", 0, 0, 20)
	if err != nil {
		t.Fatalf("ListEvidenceEvents(before rebuild) error = %v", err)
	}
	rebuilt, err := service.RebuildSearchIndex(ctx)
	if err != nil {
		t.Fatalf("RebuildSearchIndex() error = %v", err)
	}
	if rebuilt.Status != SearchIndexHealthy ||
		rebuilt.MissingCount != 0 ||
		rebuilt.OrphanCount != 0 ||
		!rebuilt.IntegrityOK ||
		rebuilt.NeedsRebuild ||
		rebuilt.LastRebuiltAt == nil ||
		rebuilt.RebuildCount != 1 ||
		!rebuilt.RebuildDoesNotChangeEvidence {
		t.Fatalf("rebuilt search index health = %+v", rebuilt)
	}
	eventsAfter, err := service.ListEvidenceEvents(ctx, 7, "", 0, 0, 20)
	if err != nil {
		t.Fatalf("ListEvidenceEvents(after rebuild) error = %v", err)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("evidence event count changed from %d to %d", len(eventsBefore), len(eventsAfter))
	}
	for index := range eventsBefore {
		if eventsBefore[index].EventID != eventsAfter[index].EventID ||
			eventsBefore[index].ContentSHA256 != eventsAfter[index].ContentSHA256 {
			t.Fatalf("evidence changed after rebuild: before=%+v after=%+v",
				eventsBefore[index], eventsAfter[index])
		}
	}
}

func TestSearchIndexHealthDetectsOrphanDocument(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	if _, err := service.db.ExecContext(ctx, `
INSERT INTO indexed_messages_fts(rowid, text, sender, peer_title)
VALUES (99999, 'orphan', '', 'orphan')`); err != nil {
		t.Fatalf("insert orphan search document: %v", err)
	}
	health, err := service.SearchIndexHealth(ctx)
	if err != nil {
		t.Fatalf("SearchIndexHealth() error = %v", err)
	}
	if health.Status != SearchIndexDegraded ||
		health.OrphanCount != 1 ||
		!health.NeedsRebuild {
		t.Fatalf("orphan search index health = %+v", health)
	}
	if _, err := service.RebuildSearchIndex(ctx); err != nil {
		t.Fatalf("RebuildSearchIndex() error = %v", err)
	}
	health, err = service.SearchIndexHealth(ctx)
	if err != nil {
		t.Fatalf("SearchIndexHealth(after rebuild) error = %v", err)
	}
	if health.Status != SearchIndexHealthy || health.OrphanCount != 0 {
		t.Fatalf("search index health after orphan cleanup = %+v", health)
	}
}
