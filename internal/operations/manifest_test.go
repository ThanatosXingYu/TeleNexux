package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBuildEvidenceManifestReportsIntegrityAndIncompleteBatches(t *testing.T) {
	ctx := context.Background()
	service := newTestService(t)
	now := time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	message := MessageRecord{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Ops",
		MessageID: 101,
		Sender:    "Alice",
		Text:      "manifest evidence",
		SentAt:    now.Add(-time.Minute),
	}
	if _, err := service.IngestBatch(ctx, IngestBatchInput{
		BatchID:         "manifest-complete",
		Source:          "test",
		AccountID:       7,
		PeerType:        "chat",
		PeerID:          42,
		PeerTitle:       "Ops",
		CheckpointAfter: 101,
		Complete:        true,
		Messages:        []MessageRecord{message},
	}); err != nil {
		t.Fatalf("IngestBatch() error = %v", err)
	}
	if err := service.RecordBatchFailure(ctx, IngestBatchInput{
		BatchID:          "manifest-failed",
		Source:           "test",
		AccountID:        8,
		PeerType:         "channel",
		PeerID:           99,
		PeerTitle:        "Alerts",
		CheckpointBefore: 50,
	}, errors.New("temporary Telegram failure")); err != nil {
		t.Fatalf("RecordBatchFailure() error = %v", err)
	}

	manifest, err := service.BuildEvidenceManifest(
		ctx,
		EvidenceManifestFilter{Days: 7, GeneratedBy: "operator"},
	)
	if err != nil {
		t.Fatalf("BuildEvidenceManifest() error = %v", err)
	}
	if manifest.DataState != "incomplete" ||
		manifest.EventCount != 1 ||
		manifest.MessageCount != 1 ||
		manifest.BatchCount != 2 ||
		manifest.CompletedBatches != 1 ||
		manifest.FailedBatches != 1 ||
		manifest.FailureWindows != 1 ||
		manifest.GeneratedBy != "operator" ||
		len(manifest.IncompleteBatches) != 1 ||
		manifest.IncompleteBatches[0].BatchID != "manifest-failed" ||
		len(manifest.EvidenceSHA256) != 64 {
		t.Fatalf("manifest = %+v", manifest)
	}
	accountManifest, err := service.BuildEvidenceManifest(
		ctx,
		EvidenceManifestFilter{Days: 7, AccountID: 7},
	)
	if err != nil {
		t.Fatalf("BuildEvidenceManifest(account) error = %v", err)
	}
	if accountManifest.DataState != "complete" ||
		accountManifest.BatchCount != 1 ||
		accountManifest.CompletedBatches != 1 ||
		len(accountManifest.IncompleteBatches) != 0 {
		t.Fatalf("account manifest = %+v", accountManifest)
	}
}

func TestBuildEvidenceManifestHandlesEmptyRange(t *testing.T) {
	service := newTestService(t)
	service.now = func() time.Time {
		return time.Date(2026, 11, 20, 18, 0, 0, 0, time.UTC)
	}
	manifest, err := service.BuildEvidenceManifest(
		context.Background(),
		EvidenceManifestFilter{Days: 7},
	)
	if err != nil {
		t.Fatalf("BuildEvidenceManifest() error = %v", err)
	}
	if manifest.DataState != "empty" ||
		manifest.EventCount != 0 ||
		manifest.BatchCount != 0 ||
		len(manifest.EvidenceSHA256) != 64 {
		t.Fatalf("empty manifest = %+v", manifest)
	}
}
