package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const EvidenceManifestFormatVersion = 1

type EvidenceManifestFilter struct {
	Days        int
	AccountID   int64
	GeneratedBy string
}

type EvidenceManifestIssue struct {
	BatchID   string `json:"batch_id"`
	AccountID int64  `json:"account_id"`
	PeerType  string `json:"peer_type"`
	PeerID    int64  `json:"peer_id"`
	Status    string `json:"status"`
}

type EvidenceManifest struct {
	FormatVersion     int                     `json:"format_version"`
	EvidenceSchema    int                     `json:"evidence_schema_version"`
	GeneratedAt       time.Time               `json:"generated_at"`
	GeneratedBy       string                  `json:"generated_by"`
	Days              int                     `json:"days"`
	AccountID         int64                   `json:"account_id,omitempty"`
	Since             time.Time               `json:"since"`
	Until             time.Time               `json:"until"`
	DataState         string                  `json:"data_state"`
	EventCount        int                     `json:"event_count"`
	MessageCount      int                     `json:"message_count"`
	BatchCount        int                     `json:"batch_count"`
	CompletedBatches  int                     `json:"completed_batches"`
	PartialBatches    int                     `json:"partial_batches"`
	FailedBatches     int                     `json:"failed_batches"`
	IncompleteBatches []EvidenceManifestIssue `json:"incomplete_batches"`
	FirstSequence     int64                   `json:"first_sequence"`
	LastSequence      int64                   `json:"last_sequence"`
	FirstObservedAt   *time.Time              `json:"first_observed_at,omitempty"`
	LastObservedAt    *time.Time              `json:"last_observed_at,omitempty"`
	EvidenceSHA256    string                  `json:"evidence_sha256"`
}

func (s *Service) BuildEvidenceManifest(
	ctx context.Context,
	filter EvidenceManifestFilter,
) (EvidenceManifest, error) {
	if filter.AccountID < 0 {
		return EvidenceManifest{}, errors.New("manifest account ID must not be negative")
	}
	if filter.Days <= 0 {
		filter.Days = 7
	}
	if filter.Days > 3650 {
		return EvidenceManifest{}, errors.New("manifest range must not exceed 3650 days")
	}
	now := s.now().UTC()
	since := now.AddDate(0, 0, -filter.Days)
	manifest := EvidenceManifest{
		FormatVersion:     EvidenceManifestFormatVersion,
		EvidenceSchema:    EvidenceSchemaVersion,
		GeneratedAt:       now,
		GeneratedBy:       truncate(strings.TrimSpace(filter.GeneratedBy), 120),
		Days:              filter.Days,
		AccountID:         filter.AccountID,
		Since:             since,
		Until:             now,
		DataState:         "empty",
		IncompleteBatches: []EvidenceManifestIssue{},
	}
	if err := s.summarizeManifestEvents(ctx, filter, since, &manifest); err != nil {
		return EvidenceManifest{}, err
	}
	if err := s.summarizeManifestBatches(ctx, filter, since, &manifest); err != nil {
		return EvidenceManifest{}, err
	}
	switch {
	case manifest.EventCount == 0 && manifest.BatchCount == 0:
		manifest.DataState = "empty"
	case manifest.PartialBatches > 0 || manifest.FailedBatches > 0:
		manifest.DataState = "incomplete"
	default:
		manifest.DataState = "complete"
	}
	return manifest, nil
}

func (s *Service) summarizeManifestEvents(
	ctx context.Context,
	filter EvidenceManifestFilter,
	since time.Time,
	manifest *EvidenceManifest,
) error {
	where, args := manifestEventWhere(filter, since)
	var firstObserved, lastObserved string
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1),
	COUNT(DISTINCT account_id || ':' || peer_type || ':' || peer_id || ':' || message_id),
	COALESCE(MIN(sequence), 0),
	COALESCE(MAX(sequence), 0),
	COALESCE(MIN(observed_at), ''),
	COALESCE(MAX(observed_at), '')
FROM evidence_events`+where, args...).Scan(
		&manifest.EventCount,
		&manifest.MessageCount,
		&manifest.FirstSequence,
		&manifest.LastSequence,
		&firstObserved,
		&lastObserved,
	); err != nil {
		return fmt.Errorf("summarize evidence manifest events: %w", err)
	}
	if firstObserved != "" {
		value, err := time.Parse(time.RFC3339Nano, firstObserved)
		if err != nil {
			return fmt.Errorf("parse first manifest event time: %w", err)
		}
		manifest.FirstObservedAt = &value
	}
	if lastObserved != "" {
		value, err := time.Parse(time.RFC3339Nano, lastObserved)
		if err != nil {
			return fmt.Errorf("parse last manifest event time: %w", err)
		}
		manifest.LastObservedAt = &value
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT sequence, event_id, batch_id, content_sha256, schema_version
FROM evidence_events`+where+`
ORDER BY sequence`, args...)
	if err != nil {
		return fmt.Errorf("list evidence manifest events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	digest := sha256.New()
	_, _ = io.WriteString(digest, "telenexus-evidence-manifest-v1\n")
	for rows.Next() {
		var (
			sequence      int64
			eventID       string
			batchID       string
			contentHash   string
			schemaVersion int
		)
		if err := rows.Scan(
			&sequence,
			&eventID,
			&batchID,
			&contentHash,
			&schemaVersion,
		); err != nil {
			return fmt.Errorf("scan evidence manifest event: %w", err)
		}
		if _, err := fmt.Fprintf(
			digest,
			"%d\x00%s\x00%s\x00%s\x00%d\n",
			sequence,
			eventID,
			batchID,
			contentHash,
			schemaVersion,
		); err != nil {
			return fmt.Errorf("hash evidence manifest event: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate evidence manifest events: %w", err)
	}
	manifest.EvidenceSHA256 = hex.EncodeToString(digest.Sum(nil))
	return nil
}

func (s *Service) summarizeManifestBatches(
	ctx context.Context,
	filter EvidenceManifestFilter,
	since time.Time,
	manifest *EvidenceManifest,
) error {
	where, args := manifestBatchWhere(filter, since)
	rows, err := s.db.QueryContext(ctx, `
SELECT status, COUNT(1)
FROM ingestion_batches`+where+`
GROUP BY status`, args...)
	if err != nil {
		return fmt.Errorf("summarize evidence manifest batches: %w", err)
	}
	for rows.Next() {
		var (
			status string
			count  int
		)
		if err := rows.Scan(&status, &count); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan evidence manifest batch summary: %w", err)
		}
		manifest.BatchCount += count
		switch status {
		case "completed":
			manifest.CompletedBatches = count
		case "partial":
			manifest.PartialBatches = count
		case "failed":
			manifest.FailedBatches = count
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate evidence manifest batch summary: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close evidence manifest batch summary: %w", err)
	}
	issueRows, err := s.db.QueryContext(ctx, `
SELECT batch_id, account_id, peer_type, peer_id, status
FROM ingestion_batches`+where+`
	AND status IN ('partial', 'failed')
ORDER BY finished_at, batch_id
LIMIT 50`, args...)
	if err != nil {
		return fmt.Errorf("list incomplete evidence manifest batches: %w", err)
	}
	defer func() { _ = issueRows.Close() }()
	for issueRows.Next() {
		var issue EvidenceManifestIssue
		if err := issueRows.Scan(
			&issue.BatchID,
			&issue.AccountID,
			&issue.PeerType,
			&issue.PeerID,
			&issue.Status,
		); err != nil {
			return fmt.Errorf("scan incomplete evidence manifest batch: %w", err)
		}
		manifest.IncompleteBatches = append(manifest.IncompleteBatches, issue)
	}
	if err := issueRows.Err(); err != nil {
		return fmt.Errorf("iterate incomplete evidence manifest batches: %w", err)
	}
	return nil
}

func manifestEventWhere(
	filter EvidenceManifestFilter,
	since time.Time,
) (string, []any) {
	where := " WHERE observed_at >= ?"
	args := []any{since.Format(time.RFC3339Nano)}
	if filter.AccountID > 0 {
		where += " AND account_id = ?"
		args = append(args, filter.AccountID)
	}
	return where, args
}

func manifestBatchWhere(
	filter EvidenceManifestFilter,
	since time.Time,
) (string, []any) {
	where := " WHERE finished_at >= ?"
	args := []any{since.Format(time.RFC3339Nano)}
	if filter.AccountID > 0 {
		where += " AND account_id = ?"
		args = append(args, filter.AccountID)
	}
	return where, args
}
