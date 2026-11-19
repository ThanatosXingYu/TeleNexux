package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetentionCleanupHonorsLegalHoldAndRecordsDestruction(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }

	oldMessage := MessageRecord{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Operations",
		MessageID: 101,
		Sender:    "Alice",
		Text:      "expired evidence",
		SentAt:    now.AddDate(0, 0, -40),
	}
	recentMessage := MessageRecord{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Operations",
		MessageID: 102,
		Sender:    "Bob",
		Text:      "current evidence",
		SentAt:    now.AddDate(0, 0, -5),
	}
	if _, err := service.IngestBatch(ctx, IngestBatchInput{
		BatchID:   "retention-seed",
		Source:    "test",
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Operations",
		Messages:  []MessageRecord{oldMessage, recentMessage},
	}); err != nil {
		t.Fatalf("IngestBatch() error = %v", err)
	}

	hold, err := service.CreateLegalHold(ctx, LegalHoldInput{
		Scope:     "chat",
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		Reason:    "Matter 2026-01",
		CreatedBy: "operator",
	})
	if err != nil {
		t.Fatalf("CreateLegalHold() error = %v", err)
	}
	preview, err := service.CleanupMessages(ctx, RetentionCleanupInput{DryRun: true})
	if err != nil {
		t.Fatalf("CleanupMessages(preview) error = %v", err)
	}
	if preview.CandidateMessages != 0 || preview.HeldMessages != 1 {
		t.Fatalf("preview with hold = %+v", preview)
	}
	result, err := service.CleanupMessages(ctx, RetentionCleanupInput{Actor: "operator"})
	if err != nil {
		t.Fatalf("CleanupMessages(held) error = %v", err)
	}
	if result.DeletedMessages != 0 || result.HeldMessages != 1 {
		t.Fatalf("cleanup with hold = %+v", result)
	}
	if evidenceCount(t, service, oldMessage.MessageID) != 1 {
		t.Fatal("held message evidence was destroyed")
	}

	released, err := service.ReleaseLegalHold(ctx, hold.ID, "operator")
	if err != nil {
		t.Fatalf("ReleaseLegalHold() error = %v", err)
	}
	if released.ReleasedAt == nil || released.ReleasedBy != "operator" {
		t.Fatalf("released legal hold = %+v", released)
	}
	preview, err = service.CleanupMessages(ctx, RetentionCleanupInput{DryRun: true})
	if err != nil {
		t.Fatalf("CleanupMessages(preview after release) error = %v", err)
	}
	if preview.CandidateMessages != 1 || preview.CandidateEvidence != 1 ||
		preview.HeldMessages != 0 {
		t.Fatalf("preview after release = %+v", preview)
	}

	result, err = service.CleanupMessages(ctx, RetentionCleanupInput{Actor: "operator"})
	if err != nil {
		t.Fatalf("CleanupMessages() error = %v", err)
	}
	if result.DeletedMessages != 1 || result.DestroyedEvidenceEvents != 1 {
		t.Fatalf("cleanup result = %+v", result)
	}
	if evidenceCount(t, service, oldMessage.MessageID) != 0 {
		t.Fatal("expired evidence still exists")
	}
	if evidenceCount(t, service, recentMessage.MessageID) != 1 {
		t.Fatal("recent evidence was destroyed")
	}
	var contentHash, destroyedBy string
	if err := service.db.QueryRowContext(ctx, `
SELECT content_hash, destroyed_by
FROM retention_destructions
WHERE account_id = 7 AND message_id = 101`).Scan(
		&contentHash,
		&destroyedBy,
	); err != nil {
		t.Fatalf("read destruction record: %v", err)
	}
	if contentHash == "" || destroyedBy != "operator" {
		t.Fatalf("destruction record = %q/%q", contentHash, destroyedBy)
	}
	if _, err := service.db.ExecContext(ctx, `
UPDATE retention_destructions SET destroyed_by = 'other' WHERE message_id = 101`,
	); err == nil {
		t.Fatal("destruction record update unexpectedly succeeded")
	}
	if _, err := service.ReleaseLegalHold(ctx, hold.ID, "operator"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second ReleaseLegalHold() error = %v, want ErrNotFound", err)
	}
}

func TestRetentionPolicyAndLegalHoldValidation(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	service.now = func() time.Time {
		return time.Date(2026, 11, 20, 17, 0, 0, 0, time.UTC)
	}

	policy, err := service.GetRetentionPolicy(ctx)
	if err != nil {
		t.Fatalf("GetRetentionPolicy() error = %v", err)
	}
	if policy.MessageRetentionDays != 30 || policy.Version != 1 {
		t.Fatalf("default policy = %+v", policy)
	}
	policy, err = service.SetRetentionPolicy(ctx, 45, "operator")
	if err != nil {
		t.Fatalf("SetRetentionPolicy() error = %v", err)
	}
	if policy.MessageRetentionDays != 45 || policy.Version != 2 {
		t.Fatalf("updated policy = %+v", policy)
	}
	if _, err := service.SetRetentionPolicy(ctx, 0, "operator"); !errors.Is(err, ErrInvalidRetention) {
		t.Fatalf("SetRetentionPolicy(invalid) error = %v", err)
	}
	if _, err := service.CreateLegalHold(ctx, LegalHoldInput{
		Scope:     "message",
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    0,
		MessageID: 100,
		Reason:    "Matter",
		CreatedBy: "operator",
	}); !errors.Is(err, ErrInvalidRetention) {
		t.Fatalf("CreateLegalHold(invalid) error = %v", err)
	}
}

func TestRetentionCleansLegacyOrphanEvidence(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	message := MessageRecord{
		AccountID: 11,
		PeerType:  "channel",
		PeerID:    88,
		PeerTitle: "Archive",
		MessageID: 501,
		Sender:    "Editor",
		Text:      "legacy orphan",
		SentAt:    now.AddDate(0, 0, -45),
	}
	if _, err := service.IngestBatch(ctx, IngestBatchInput{
		BatchID:   "orphan-seed",
		Source:    "test",
		AccountID: message.AccountID,
		PeerType:  message.PeerType,
		PeerID:    message.PeerID,
		PeerTitle: message.PeerTitle,
		Messages:  []MessageRecord{message},
	}); err != nil {
		t.Fatalf("IngestBatch() error = %v", err)
	}
	if _, err := service.db.ExecContext(ctx, `
DELETE FROM indexed_messages
WHERE account_id = 11 AND peer_type = 'channel' AND peer_id = 88 AND message_id = 501`,
	); err != nil {
		t.Fatalf("simulate legacy orphan: %v", err)
	}

	preview, err := service.CleanupMessages(ctx, RetentionCleanupInput{DryRun: true})
	if err != nil {
		t.Fatalf("CleanupMessages(preview) error = %v", err)
	}
	if preview.CandidateMessages != 0 || preview.CandidateEvidence != 1 {
		t.Fatalf("orphan preview = %+v", preview)
	}
	result, err := service.CleanupMessages(ctx, RetentionCleanupInput{Actor: "operator"})
	if err != nil {
		t.Fatalf("CleanupMessages() error = %v", err)
	}
	if result.DeletedMessages != 0 || result.DestroyedEvidenceEvents != 1 {
		t.Fatalf("orphan cleanup = %+v", result)
	}
	if evidenceCount(t, service, message.MessageID) != 0 {
		t.Fatal("legacy orphan evidence still exists")
	}
	var reason string
	if err := service.db.QueryRowContext(ctx, `
SELECT reason FROM retention_destructions
WHERE account_id = 11 AND message_id = 501`).Scan(&reason); err != nil {
		t.Fatalf("read orphan destruction record: %v", err)
	}
	if reason != "retention_expired_orphan_evidence" {
		t.Fatalf("orphan destruction reason = %q", reason)
	}
}

func evidenceCount(t *testing.T, service *Service, messageID int) int {
	t.Helper()
	var count int
	if err := service.db.QueryRowContext(context.Background(), `
SELECT COUNT(1)
FROM evidence_events
WHERE account_id = 7 AND message_id = ?`, messageID).Scan(&count); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	return count
}
