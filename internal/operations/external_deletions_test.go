package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExternalDeletionResolvesOnlyUniqueProjection(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	messages := []MessageRecord{
		{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    42,
			MessageID: 101,
			Text:      "conversation alpha",
			SentAt:    now,
		},
		{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    43,
			MessageID: 101,
			Text:      "conversation beta",
			SentAt:    now,
		},
	}
	for _, message := range messages {
		if _, _, err := service.IngestMessages(ctx, []MessageRecord{message}); err != nil {
			t.Fatalf("IngestMessages() error = %v", err)
		}
	}
	inserted, err := service.EnqueueExternalDeletion(
		ctx,
		7,
		"unknown",
		0,
		[]int{101, 101},
	)
	if err != nil {
		t.Fatalf("EnqueueExternalDeletion() error = %v", err)
	}
	if inserted != 1 {
		t.Fatalf("EnqueueExternalDeletion() inserted = %d, want 1", inserted)
	}
	inserted, err = service.EnqueueExternalDeletion(
		ctx,
		7,
		"unknown",
		0,
		[]int{101},
	)
	if err != nil {
		t.Fatalf("duplicate EnqueueExternalDeletion() error = %v", err)
	}
	if inserted != 0 {
		t.Fatalf("duplicate EnqueueExternalDeletion() inserted = %d, want 0", inserted)
	}

	resolved, err := service.ResolveExternalDeletions(ctx, 10)
	if err != nil {
		t.Fatalf("ResolveExternalDeletions() error = %v", err)
	}
	if resolved != 0 {
		t.Fatalf("ambiguous ResolveExternalDeletions() resolved = %d, want 0", resolved)
	}
	pending, err := service.ListPendingDeletions(ctx, PendingDeletionAmbiguous, 10)
	if err != nil {
		t.Fatalf("ListPendingDeletions() error = %v", err)
	}
	if len(pending) != 1 ||
		pending[0].MessageID != 101 ||
		pending[0].Attempts != 1 ||
		pending[0].LastAttemptAt == nil {
		t.Fatalf("ambiguous candidates = %+v", pending)
	}

	events, err := service.ListEvidenceEvents(ctx, 7, "chat", 42, 101, 10)
	if err != nil {
		t.Fatalf("ListEvidenceEvents() error = %v", err)
	}
	if len(events) != 1 || events[0].EventType != "message" {
		t.Fatalf("ambiguous candidate created deletion evidence: %+v", events)
	}
}

func TestExternalDeletionRecordsUniqueAndMissingCandidates(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	message := MessageRecord{
		AccountID: 7,
		PeerType:  "channel",
		PeerID:    52,
		MessageID: 201,
		Text:      "channel item",
		SentAt:    now,
	}
	if _, _, err := service.IngestMessages(ctx, []MessageRecord{message}); err != nil {
		t.Fatalf("IngestMessages() error = %v", err)
	}
	if _, err := service.EnqueueExternalDeletion(
		ctx,
		7,
		"channel",
		52,
		[]int{201, 202},
	); err != nil {
		t.Fatalf("EnqueueExternalDeletion() error = %v", err)
	}

	resolved, err := service.ResolveExternalDeletions(ctx, 10)
	if err != nil {
		t.Fatalf("ResolveExternalDeletions() error = %v", err)
	}
	if resolved != 1 {
		t.Fatalf("ResolveExternalDeletions() resolved = %d, want 1", resolved)
	}
	events, err := service.ListEvidenceEvents(ctx, 7, "channel", 52, 201, 10)
	if err != nil {
		t.Fatalf("ListEvidenceEvents() error = %v", err)
	}
	if len(events) != 2 ||
		events[0].EventType != "delete" ||
		events[0].Actor != externalDeletionActor {
		t.Fatalf("external deletion evidence = %+v", events)
	}
	inserted, err := service.EnqueueExternalDeletion(
		ctx,
		7,
		"channel",
		52,
		[]int{201},
	)
	if err != nil {
		t.Fatalf("renewed EnqueueExternalDeletion() error = %v", err)
	}
	if inserted != 1 {
		t.Fatalf("renewed EnqueueExternalDeletion() inserted = %d, want 1", inserted)
	}
	resolved, err = service.ResolveExternalDeletions(ctx, 10)
	if err != nil {
		t.Fatalf("repeat ResolveExternalDeletions() error = %v", err)
	}
	if resolved != 1 {
		t.Fatalf("repeat ResolveExternalDeletions() resolved = %d, want 1", resolved)
	}
	events, err = service.ListEvidenceEvents(ctx, 7, "channel", 52, 201, 10)
	if err != nil {
		t.Fatalf("ListEvidenceEvents() after renewal error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("renewed deletion appended duplicate evidence: %+v", events)
	}
	missing, err := service.ListPendingDeletions(ctx, PendingDeletionMissing, 10)
	if err != nil {
		t.Fatalf("ListPendingDeletions(missing) error = %v", err)
	}
	if len(missing) != 1 ||
		missing[0].MessageID != 202 ||
		missing[0].LastError == "" {
		t.Fatalf("missing candidates = %+v", missing)
	}
}

func TestExternalDeletionRequiredForAccountPurge(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	if _, err := service.EnqueueExternalDeletion(
		ctx,
		7,
		"unknown",
		0,
		[]int{401},
	); err != nil {
		t.Fatalf("EnqueueExternalDeletion() error = %v", err)
	}
	if err := service.PurgeAccount(ctx, 7); err != nil {
		t.Fatalf("PurgeAccount() error = %v", err)
	}
	var count int
	if err := service.db.QueryRowContext(ctx, `
SELECT COUNT(1) FROM pending_message_deletions WHERE account_id = 7`,
	).Scan(&count); err != nil {
		t.Fatalf("count pending deletions: %v", err)
	}
	if count != 0 {
		t.Fatalf("pending deletions after purge = %d, want 0", count)
	}
}

func TestExternalDeletionRequeueUpdatesDiagnosticCandidate(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	if _, err := service.EnqueueExternalDeletion(
		ctx,
		7,
		"channel",
		52,
		[]int{501},
	); err != nil {
		t.Fatalf("EnqueueExternalDeletion() error = %v", err)
	}
	if _, err := service.ResolveExternalDeletions(ctx, 10); err != nil {
		t.Fatalf("ResolveExternalDeletions() error = %v", err)
	}
	missing, err := service.ListPendingDeletions(ctx, PendingDeletionMissing, 10)
	if err != nil || len(missing) != 1 {
		t.Fatalf("missing candidates = %+v, %v", missing, err)
	}
	if err := service.RequeueExternalDeletion(ctx, 7, "channel", 52, 501); err != nil {
		t.Fatalf("RequeueExternalDeletion() error = %v", err)
	}
	pending, err := service.ListPendingDeletions(ctx, PendingDeletionPending, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending candidates = %+v, %v", pending, err)
	}
	if pending[0].LastError != "" || pending[0].LastAttemptAt != nil {
		t.Fatalf("requeued candidate retained diagnosis = %+v", pending[0])
	}
	if err := service.RequeueExternalDeletion(
		ctx,
		7,
		"channel",
		52,
		501,
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RequeueExternalDeletion(pending) error = %v, want ErrNotFound", err)
	}
	if err := service.RequeueExternalDeletion(ctx, 0, "", 0, 0); err == nil {
		t.Fatal("RequeueExternalDeletion(invalid) error = nil")
	}
}
