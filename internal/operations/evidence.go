package operations

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const EvidenceSchemaVersion = 3

var ErrBatchConflict = errors.New("ingestion batch ID conflicts with different content")

type CollectionTarget struct {
	AccountID                  int64      `json:"account_id"`
	PeerType                   string     `json:"peer_type"`
	PeerID                     int64      `json:"peer_id"`
	PeerTitle                  string     `json:"peer_title"`
	LastMessageID              int        `json:"last_message_id"`
	ConsecutiveFailures        int        `json:"consecutive_failures"`
	LastError                  string     `json:"last_error,omitempty"`
	BacklogDetected            bool       `json:"backlog_detected"`
	FloodWait                  bool       `json:"flood_wait"`
	NextCollectionAt           *time.Time `json:"next_collection_at,omitempty"`
	LastSuccessfulAt           *time.Time `json:"last_successful_at,omitempty"`
	CheckpointDelaySeconds     int64      `json:"checkpoint_delay_seconds"`
	LastBatchFetched           int        `json:"last_batch_fetched"`
	LastBatchAt                *time.Time `json:"last_batch_at,omitempty"`
	AccountConsecutiveFailures int        `json:"account_consecutive_failures"`
	AccountLastError           string     `json:"account_last_error,omitempty"`
}

type IngestBatchInput struct {
	BatchID          string
	Source           string
	AccountID        int64
	PeerType         string
	PeerID           int64
	PeerTitle        string
	CheckpointBefore int
	CheckpointAfter  int
	Complete         bool
	BacklogDetected  bool
	Messages         []MessageRecord
}

type IngestBatchResult struct {
	BatchID  string  `json:"batch_id"`
	Inserted int     `json:"inserted"`
	Events   int     `json:"events"`
	Alerts   []Alert `json:"alerts"`
}

type EvidenceEvent struct {
	Sequence      int64           `json:"sequence"`
	EventID       string          `json:"event_id"`
	BatchID       string          `json:"batch_id"`
	AccountID     int64           `json:"account_id"`
	PeerType      string          `json:"peer_type"`
	PeerID        int64           `json:"peer_id"`
	PeerTitle     string          `json:"peer_title"`
	MessageID     int             `json:"message_id"`
	EventType     string          `json:"event_type"`
	Version       int             `json:"version"`
	Actor         string          `json:"actor,omitempty"`
	Sender        string          `json:"sender"`
	Text          string          `json:"text"`
	Media         json.RawMessage `json:"media"`
	Raw           json.RawMessage `json:"raw"`
	SentAt        *time.Time      `json:"sent_at,omitempty"`
	ObservedAt    time.Time       `json:"observed_at"`
	ContentSHA256 string          `json:"content_sha256"`
	SchemaVersion int             `json:"schema_version"`
}

type MonitorSummary struct {
	TargetCount            int        `json:"target_count"`
	BacklogTargetCount     int        `json:"backlog_target_count"`
	FloodWaitTargetCount   int        `json:"flood_wait_target_count"`
	ConsecutiveFailures    int        `json:"consecutive_failures"`
	NeverSuccessfulTargets int        `json:"never_successful_targets"`
	MaxCheckpointDelay     int64      `json:"max_checkpoint_delay_seconds"`
	LastSuccessfulAt       *time.Time `json:"last_successful_at,omitempty"`
	QueuedDeliveries       int        `json:"queued_deliveries"`
	FailedDeliveries       int        `json:"failed_deliveries"`
	OldestQueuedAt         *time.Time `json:"oldest_queued_at,omitempty"`
}

func (s *Service) ListCollectionTargets(ctx context.Context) ([]CollectionTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.account_id, r.peer_type, r.peer_id, MAX(r.peer_title),
	COALESCE(c.last_message_id, MAX(r.last_message_id)),
	COALESCE(c.consecutive_failures, 0),
	COALESCE(c.last_error, ''),
	COALESCE(c.backlog_detected, 0),
	COALESCE(st.next_collection_at, ''),
	COALESCE(st.consecutive_failures, 0),
	COALESCE(st.last_error, ''),
	COALESCE(c.last_success_at, ''),
	COALESCE(b.fetched_count, 0),
	COALESCE(b.finished_at, '')
FROM monitor_rules r
LEFT JOIN monitor_checkpoints c
	ON c.account_id = r.account_id
	AND c.peer_type = r.peer_type
	AND c.peer_id = r.peer_id
LEFT JOIN monitor_account_states st ON st.account_id = r.account_id
LEFT JOIN ingestion_batches b ON b.batch_id = (
	SELECT latest.batch_id
	FROM ingestion_batches latest
	WHERE latest.account_id = r.account_id
		AND latest.peer_type = r.peer_type
		AND latest.peer_id = r.peer_id
	ORDER BY latest.finished_at DESC
	LIMIT 1
)
WHERE r.enabled = 1
GROUP BY r.account_id, r.peer_type, r.peer_id
ORDER BY r.account_id, r.peer_type, r.peer_id`)
	if err != nil {
		return nil, fmt.Errorf("list collection targets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	targets := make([]CollectionTarget, 0)
	for rows.Next() {
		var (
			target                    CollectionTarget
			backlog                   int
			nextCollection            string
			lastSuccessful, lastBatch string
		)
		if err := rows.Scan(
			&target.AccountID,
			&target.PeerType,
			&target.PeerID,
			&target.PeerTitle,
			&target.LastMessageID,
			&target.ConsecutiveFailures,
			&target.LastError,
			&backlog,
			&nextCollection,
			&target.AccountConsecutiveFailures,
			&target.AccountLastError,
			&lastSuccessful,
			&target.LastBatchFetched,
			&lastBatch,
		); err != nil {
			return nil, fmt.Errorf("scan collection target: %w", err)
		}
		target.BacklogDetected = backlog != 0
		if nextCollection != "" {
			value, err := time.Parse(time.RFC3339Nano, nextCollection)
			if err != nil {
				return nil, fmt.Errorf("parse account collection cooldown: %w", err)
			}
			target.NextCollectionAt = &value
		}
		if lastSuccessful != "" {
			value, err := time.Parse(time.RFC3339Nano, lastSuccessful)
			if err != nil {
				return nil, fmt.Errorf("parse collection success time: %w", err)
			}
			target.LastSuccessfulAt = &value
			target.CheckpointDelaySeconds = max(
				0,
				int64(s.now().UTC().Sub(value).Seconds()),
			)
		}
		target.FloodWait = strings.Contains(strings.ToLower(target.LastError), "rate limited")
		if lastBatch != "" {
			value, err := time.Parse(time.RFC3339Nano, lastBatch)
			if err != nil {
				return nil, fmt.Errorf("parse collection batch time: %w", err)
			}
			target.LastBatchAt = &value
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate collection targets: %w", err)
	}
	return targets, nil
}

func (s *Service) IngestMessages(
	ctx context.Context,
	messages []MessageRecord,
) (int, []Alert, error) {
	if len(messages) == 0 {
		return 0, nil, nil
	}
	first := messages[0]
	result, err := s.IngestBatch(ctx, IngestBatchInput{
		Source:    "manual",
		AccountID: first.AccountID,
		PeerType:  first.PeerType,
		PeerID:    first.PeerID,
		PeerTitle: first.PeerTitle,
		Messages:  messages,
	})
	if err != nil {
		return 0, nil, err
	}
	return result.Inserted, result.Alerts, nil
}

func (s *Service) IngestBatch(
	ctx context.Context,
	input IngestBatchInput,
) (IngestBatchResult, error) {
	normalized, err := normalizeBatchInput(input)
	if err != nil {
		return IngestBatchResult{}, err
	}
	rules, err := s.ListRules(ctx, true)
	if err != nil {
		return IngestBatchResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestBatchResult{}, fmt.Errorf("begin evidence ingestion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := s.now().UTC()
	existing, found, err := existingBatchResultTx(ctx, tx, normalized)
	if err != nil {
		return IngestBatchResult{}, err
	}
	if found {
		if err := tx.Commit(); err != nil {
			return IngestBatchResult{}, fmt.Errorf("commit idempotent evidence ingestion: %w", err)
		}
		return existing, nil
	}
	result, err := s.ingestBatchTx(ctx, tx, normalized, rules, now)
	if err != nil {
		return IngestBatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return IngestBatchResult{}, fmt.Errorf("commit evidence ingestion: %w", err)
	}
	return result, nil
}

func (s *Service) ingestBatchTx(
	ctx context.Context,
	tx *sql.Tx,
	input IngestBatchInput,
	rules []Rule,
	now time.Time,
) (IngestBatchResult, error) {
	result := IngestBatchResult{BatchID: input.BatchID}
	for _, message := range input.Messages {
		event, inserted, err := s.ingestMessageTx(ctx, tx, input, message, now)
		if err != nil {
			return IngestBatchResult{}, err
		}
		if event != nil {
			result.Events++
		}
		result.Inserted += inserted
		for _, rule := range rules {
			if !rule.AppliesTo(message) {
				continue
			}
			if input.CheckpointBefore > 0 && message.MessageID <= input.CheckpointBefore {
				continue
			}
			keyword, matched := matchesRule(rule, message.Text)
			if !matched {
				continue
			}
			alert, err := s.insertAlert(ctx, tx, rule, message, keyword, now)
			if err != nil {
				return IngestBatchResult{}, err
			}
			if alert.ID > 0 {
				result.Alerts = append(result.Alerts, alert)
			}
		}
	}
	status := "partial"
	checkpointAfter := input.CheckpointBefore
	if input.Complete {
		status = "completed"
		checkpointAfter = max(checkpointAfter, input.CheckpointAfter)
	}
	if err := s.insertBatchTx(ctx, tx, input, status, result, checkpointAfter, now); err != nil {
		return IngestBatchResult{}, err
	}
	if input.Complete {
		if err := s.advanceCheckpointTx(ctx, tx, input, checkpointAfter, now); err != nil {
			return IngestBatchResult{}, err
		}
	} else if input.BacklogDetected {
		if err := s.markBacklogTx(ctx, tx, input, now); err != nil {
			return IngestBatchResult{}, err
		}
	}
	return result, nil
}

func (s *Service) ingestMessageTx(
	ctx context.Context,
	tx *sql.Tx,
	input IngestBatchInput,
	message MessageRecord,
	now time.Time,
) (*EvidenceEvent, int, error) {
	contentHash := messageContentHash(message)
	latest, exists, err := latestEvidence(ctx, tx, message)
	if err != nil {
		return nil, 0, err
	}
	if exists && latest.contentHash == contentHash {
		return nil, 0, nil
	}
	eventType := "message"
	version := 1
	if exists {
		eventType = "edit"
		version = latest.version + 1
	}
	event, err := s.appendMessageEventTx(
		ctx,
		tx,
		input,
		message,
		eventType,
		version,
		contentHash,
		now,
	)
	if err != nil {
		return nil, 0, err
	}
	inserted, err := upsertMessageProjectionTx(ctx, tx, input, message, event, now)
	if err != nil {
		return nil, 0, err
	}
	return event, inserted, nil
}

type latestEvidenceRecord struct {
	version     int
	contentHash string
}

func latestEvidence(
	ctx context.Context,
	tx *sql.Tx,
	message MessageRecord,
) (latestEvidenceRecord, bool, error) {
	var latest latestEvidenceRecord
	err := tx.QueryRowContext(ctx, `
SELECT version, content_sha256
FROM evidence_events
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?
ORDER BY version DESC
LIMIT 1`,
		message.AccountID,
		message.PeerType,
		message.PeerID,
		message.MessageID,
	).Scan(&latest.version, &latest.contentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return latestEvidenceRecord{}, false, nil
	}
	if err != nil {
		return latestEvidenceRecord{}, false, fmt.Errorf("read latest evidence event: %w", err)
	}
	return latest, true, nil
}

func (s *Service) appendMessageEventTx(
	ctx context.Context,
	tx *sql.Tx,
	input IngestBatchInput,
	message MessageRecord,
	eventType string,
	version int,
	contentHash string,
	now time.Time,
) (*EvidenceEvent, error) {
	media, err := json.Marshal(mediaEvidence(message))
	if err != nil {
		return nil, fmt.Errorf("encode evidence media: %w", err)
	}
	raw, err := json.Marshal(evidenceRawMessage(message))
	if err != nil {
		return nil, fmt.Errorf("encode raw evidence message: %w", err)
	}
	eventID := evidenceEventID(
		input.BatchID,
		message,
		eventType,
		version,
	)
	sentAt := message.SentAt.UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `
INSERT INTO evidence_events (
	event_id, batch_id, account_id, peer_type, peer_id, peer_title,
	message_id, event_type, version, actor, sender, text, media_json,
	raw_json, sent_at, observed_at, content_sha256, schema_version
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID,
		input.BatchID,
		message.AccountID,
		message.PeerType,
		message.PeerID,
		truncate(message.PeerTitle, 120),
		message.MessageID,
		eventType,
		version,
		truncate(message.Sender, 120),
		truncate(message.Text, 16<<10),
		string(media),
		string(raw),
		sentAt,
		now.Format(time.RFC3339Nano),
		contentHash,
		EvidenceSchemaVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("append evidence event: %w", err)
	}
	return &EvidenceEvent{
		EventID:       eventID,
		BatchID:       input.BatchID,
		AccountID:     message.AccountID,
		PeerType:      message.PeerType,
		PeerID:        message.PeerID,
		PeerTitle:     truncate(message.PeerTitle, 120),
		MessageID:     message.MessageID,
		EventType:     eventType,
		Version:       version,
		Sender:        truncate(message.Sender, 120),
		Text:          truncate(message.Text, 16<<10),
		Media:         media,
		Raw:           raw,
		ObservedAt:    now,
		ContentSHA256: contentHash,
		SchemaVersion: EvidenceSchemaVersion,
	}, nil
}

func upsertMessageProjectionTx(
	ctx context.Context,
	tx *sql.Tx,
	input IngestBatchInput,
	message MessageRecord,
	event *EvidenceEvent,
	now time.Time,
) (int, error) {
	inserted := 0
	result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO indexed_messages (
	account_id, peer_type, peer_id, peer_title, message_id, sender, text,
	sent_at, indexed_at, latest_event_id, content_hash, schema_version,
	deleted_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?)`,
		message.AccountID,
		message.PeerType,
		message.PeerID,
		truncate(message.PeerTitle, 120),
		message.MessageID,
		truncate(message.Sender, 120),
		truncate(message.Text, 16<<10),
		message.SentAt.UTC().Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		event.EventID,
		event.ContentSHA256,
		EvidenceSchemaVersion,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("insert message projection: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("check inserted message projection: %w", err)
	}
	inserted = int(affected)
	if inserted == 0 {
		if _, err := tx.ExecContext(ctx, `
UPDATE indexed_messages
SET peer_title = ?, sender = ?, text = ?, sent_at = ?, indexed_at = ?,
	latest_event_id = ?, content_hash = ?, schema_version = ?,
	deleted_at = '', updated_at = ?
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?`,
			inputPeerTitle(input, message),
			truncate(message.Sender, 120),
			truncate(message.Text, 16<<10),
			message.SentAt.UTC().Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano),
			event.EventID,
			event.ContentSHA256,
			EvidenceSchemaVersion,
			now.Format(time.RFC3339Nano),
			message.AccountID,
			message.PeerType,
			message.PeerID,
			message.MessageID,
		); err != nil {
			return 0, fmt.Errorf("update message projection: %w", err)
		}
	}
	return inserted, nil
}

func (s *Service) insertBatchTx(
	ctx context.Context,
	tx *sql.Tx,
	input IngestBatchInput,
	status string,
	result IngestBatchResult,
	checkpointAfter int,
	now time.Time,
) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO ingestion_batches (
	batch_id, source, account_id, peer_type, peer_id, status, started_at,
	finished_at, fetched_count, event_count, inserted_count,
	checkpoint_before, checkpoint_after, backlog_detected, payload_sha256,
	error_text
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
		input.BatchID,
		input.Source,
		input.AccountID,
		input.PeerType,
		input.PeerID,
		status,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		len(input.Messages),
		result.Events,
		result.Inserted,
		input.CheckpointBefore,
		checkpointAfter,
		boolInt(input.BacklogDetected),
		batchPayloadHash(input),
	)
	if err != nil {
		return fmt.Errorf("record ingestion batch: %w", err)
	}
	return nil
}

func (s *Service) advanceCheckpointTx(
	ctx context.Context,
	tx *sql.Tx,
	input IngestBatchInput,
	checkpointAfter int,
	now time.Time,
) error {
	timestamp := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO monitor_checkpoints (
	account_id, peer_type, peer_id, last_message_id, last_success_at,
	consecutive_failures, last_error, backlog_detected, updated_at
) VALUES (?, ?, ?, ?, ?, 0, '', 0, ?)
ON CONFLICT(account_id, peer_type, peer_id) DO UPDATE SET
	last_message_id = MAX(monitor_checkpoints.last_message_id, excluded.last_message_id),
	last_success_at = excluded.last_success_at,
	consecutive_failures = 0,
	last_error = '',
	backlog_detected = 0,
	updated_at = excluded.updated_at`,
		input.AccountID,
		input.PeerType,
		input.PeerID,
		checkpointAfter,
		timestamp,
		timestamp,
	); err != nil {
		return fmt.Errorf("advance monitor checkpoint: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE monitor_rules
SET last_message_id = MAX(last_message_id, ?), last_run_at = ?,
	last_error = '', updated_at = ?
WHERE account_id = ? AND peer_type = ? AND peer_id = ?`,
		checkpointAfter,
		timestamp,
		timestamp,
		input.AccountID,
		input.PeerType,
		input.PeerID,
	); err != nil {
		return fmt.Errorf("update monitor rule checkpoints: %w", err)
	}
	return nil
}

func (s *Service) markBacklogTx(
	ctx context.Context,
	tx *sql.Tx,
	input IngestBatchInput,
	now time.Time,
) error {
	timestamp := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO monitor_checkpoints (
	account_id, peer_type, peer_id, last_message_id, consecutive_failures,
	last_error, backlog_detected, updated_at
) VALUES (?, ?, ?, ?, 0, '', 1, ?)
ON CONFLICT(account_id, peer_type, peer_id) DO UPDATE SET
	backlog_detected = 1,
	updated_at = excluded.updated_at`,
		input.AccountID,
		input.PeerType,
		input.PeerID,
		input.CheckpointBefore,
		timestamp,
	); err != nil {
		return fmt.Errorf("mark monitor checkpoint backlog: %w", err)
	}
	return nil
}

func (s *Service) RecordBatchFailure(
	ctx context.Context,
	input IngestBatchInput,
	runErr error,
) error {
	normalized, err := normalizeBatchInput(input)
	if err != nil {
		return err
	}
	if runErr == nil {
		runErr = errors.New("collection failed")
	}
	now := s.now().UTC()
	timestamp := now.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin failed ingestion batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO ingestion_batches (
	batch_id, source, account_id, peer_type, peer_id, status, started_at,
	finished_at, fetched_count, event_count, inserted_count,
	checkpoint_before, checkpoint_after, backlog_detected, payload_sha256,
	error_text
) VALUES (?, ?, ?, ?, ?, 'failed', ?, ?, 0, 0, 0, ?, ?, ?, ?, ?)`,
		normalized.BatchID,
		normalized.Source,
		normalized.AccountID,
		normalized.PeerType,
		normalized.PeerID,
		timestamp,
		timestamp,
		normalized.CheckpointBefore,
		normalized.CheckpointBefore,
		boolInt(normalized.BacklogDetected),
		batchPayloadHash(normalized),
		truncate(runErr.Error(), 500),
	); err != nil {
		return fmt.Errorf("record failed ingestion batch: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO monitor_checkpoints (
	account_id, peer_type, peer_id, last_message_id, consecutive_failures,
	last_error, backlog_detected, updated_at
) VALUES (?, ?, ?, ?, 1, ?, ?, ?)
ON CONFLICT(account_id, peer_type, peer_id) DO UPDATE SET
	consecutive_failures = monitor_checkpoints.consecutive_failures + 1,
	last_error = excluded.last_error,
	backlog_detected = excluded.backlog_detected,
	updated_at = excluded.updated_at`,
		normalized.AccountID,
		normalized.PeerType,
		normalized.PeerID,
		normalized.CheckpointBefore,
		truncate(runErr.Error(), 500),
		boolInt(normalized.BacklogDetected),
		timestamp,
	); err != nil {
		return fmt.Errorf("record collection failure: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE monitor_rules
SET last_run_at = ?, last_error = ?, updated_at = ?
WHERE account_id = ? AND peer_type = ? AND peer_id = ?`,
		timestamp,
		truncate(runErr.Error(), 500),
		timestamp,
		normalized.AccountID,
		normalized.PeerType,
		normalized.PeerID,
	); err != nil {
		return fmt.Errorf("record monitor rule failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit failed ingestion batch: %w", err)
	}
	return nil
}

func (s *Service) RecordDeletion(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
	messageIDs []int,
	actor string,
) (string, error) {
	if accountID <= 0 || peerID <= 0 || len(messageIDs) == 0 {
		return "", errors.New("deletion evidence requires account, peer, and message IDs")
	}
	batchID, err := newBatchID()
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin deletion evidence: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	eventCount := 0
	seen := make(map[int]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		if messageID <= 0 {
			continue
		}
		if _, duplicate := seen[messageID]; duplicate {
			continue
		}
		seen[messageID] = struct{}{}
		added, err := s.recordDeletionEventTx(
			ctx,
			tx,
			batchID,
			accountID,
			peerType,
			peerID,
			messageID,
			actor,
			now,
		)
		if err != nil {
			return "", err
		}
		if added {
			eventCount++
		}
	}
	if err := s.insertBatchTx(ctx, tx, IngestBatchInput{
		BatchID:   batchID,
		Source:    "message_delete",
		AccountID: accountID,
		PeerType:  peerType,
		PeerID:    peerID,
	}, "completed", IngestBatchResult{Events: eventCount}, 0, now); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit deletion evidence: %w", err)
	}
	return batchID, nil
}

func (s *Service) recordDeletionEventTx(
	ctx context.Context,
	tx *sql.Tx,
	batchID string,
	accountID int64,
	peerType string,
	peerID int64,
	messageID int,
	actor string,
	now time.Time,
) (bool, error) {
	latest, exists, err := latestEvidence(ctx, tx, MessageRecord{
		AccountID: accountID,
		PeerType:  peerType,
		PeerID:    peerID,
		MessageID: messageID,
	})
	if err != nil {
		return false, err
	}
	if exists {
		var latestType string
		if err := tx.QueryRowContext(ctx, `
SELECT event_type FROM evidence_events
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?
ORDER BY version DESC LIMIT 1`,
			accountID,
			peerType,
			peerID,
			messageID,
		).Scan(&latestType); err != nil {
			return false, fmt.Errorf("read latest evidence type: %w", err)
		}
		if latestType == "delete" {
			return false, nil
		}
	}
	version := latest.version + 1
	contentHash := sha256Hex(fmt.Sprintf(
		"delete|%d|%s|%d|%d",
		accountID,
		peerType,
		peerID,
		messageID,
	))
	eventID := evidenceDeletionID(batchID, accountID, peerType, peerID, messageID, version)
	_, err = tx.ExecContext(ctx, `
INSERT INTO evidence_events (
	event_id, batch_id, account_id, peer_type, peer_id, peer_title,
	message_id, event_type, version, actor, sender, text, media_json,
	raw_json, sent_at, observed_at, content_sha256, schema_version
) VALUES (?, ?, ?, ?, ?, '', ?, 'delete', ?, ?, '', '', '{}', '{}', '', ?, ?, ?)`,
		eventID,
		batchID,
		accountID,
		peerType,
		peerID,
		messageID,
		version,
		truncate(actor, 64),
		now.Format(time.RFC3339Nano),
		contentHash,
		EvidenceSchemaVersion,
	)
	if err != nil {
		return false, fmt.Errorf("append deletion evidence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE indexed_messages
SET latest_event_id = ?, content_hash = ?, schema_version = ?,
	deleted_at = ?, updated_at = ?
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?`,
		eventID,
		contentHash,
		EvidenceSchemaVersion,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		accountID,
		peerType,
		peerID,
		messageID,
	); err != nil {
		return false, fmt.Errorf("tombstone message projection: %w", err)
	}
	return true, nil
}

func (s *Service) ListEvidenceEvents(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
	messageID int,
	limit int,
) ([]EvidenceEvent, error) {
	limit = normalizeLimit(limit, 50, 200)
	query := `
SELECT sequence, event_id, batch_id, account_id, peer_type, peer_id,
	peer_title, message_id, event_type, version, actor, sender, text,
	media_json, raw_json, sent_at, observed_at, content_sha256, schema_version
FROM evidence_events
WHERE account_id = ?`
	args := []any{accountID}
	if peerType != "" {
		query += " AND peer_type = ? AND peer_id = ?"
		args = append(args, peerType, peerID)
	}
	if messageID > 0 {
		query += " AND message_id = ?"
		args = append(args, messageID)
	}
	query += " ORDER BY sequence DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list evidence events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]EvidenceEvent, 0, limit)
	for rows.Next() {
		item, err := scanEvidenceEvent(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate evidence events: %w", err)
	}
	return items, nil
}

func (s *Service) MonitorSummary(ctx context.Context) (MonitorSummary, error) {
	var summary MonitorSummary
	var (
		lastSuccessful string
		oldestSuccess  string
	)
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1),
	COALESCE(SUM(CASE WHEN COALESCE(c.backlog_detected, 0) = 1 THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN LOWER(COALESCE(c.last_error, '')) LIKE '%rate limited%'
		THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(COALESCE(c.consecutive_failures, 0)), 0),
	COALESCE(SUM(CASE WHEN COALESCE(c.last_success_at, '') = '' THEN 1 ELSE 0 END), 0),
	COALESCE(MAX(c.last_success_at), ''),
	COALESCE(MIN(CASE WHEN COALESCE(c.last_success_at, '') <> ''
		THEN c.last_success_at END), '')
FROM (
	SELECT DISTINCT account_id, peer_type, peer_id
	FROM monitor_rules
	WHERE enabled = 1
) AS targets
LEFT JOIN monitor_checkpoints AS c
	ON c.account_id = targets.account_id
	AND c.peer_type = targets.peer_type
	AND c.peer_id = targets.peer_id`).Scan(
		&summary.TargetCount,
		&summary.BacklogTargetCount,
		&summary.FloodWaitTargetCount,
		&summary.ConsecutiveFailures,
		&summary.NeverSuccessfulTargets,
		&lastSuccessful,
		&oldestSuccess,
	); err != nil {
		return MonitorSummary{}, fmt.Errorf("summarize monitor checkpoints: %w", err)
	}
	if lastSuccessful != "" {
		value, err := time.Parse(time.RFC3339Nano, lastSuccessful)
		if err != nil {
			return MonitorSummary{}, fmt.Errorf("parse monitor success time: %w", err)
		}
		summary.LastSuccessfulAt = &value
	}
	if oldestSuccess != "" {
		value, err := time.Parse(time.RFC3339Nano, oldestSuccess)
		if err != nil {
			return MonitorSummary{}, fmt.Errorf("parse oldest monitor success time: %w", err)
		}
		summary.MaxCheckpointDelay = max(0, int64(s.now().UTC().Sub(value).Seconds()))
	}
	var oldestQueued string
	if err := s.db.QueryRowContext(ctx, `
SELECT
	COALESCE(SUM(CASE WHEN state = 'queued' THEN 1 ELSE 0 END), 0),
	COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0),
	COALESCE(MIN(CASE WHEN state = 'queued' THEN created_at END), '')
FROM notification_deliveries`).Scan(
		&summary.QueuedDeliveries,
		&summary.FailedDeliveries,
		&oldestQueued,
	); err != nil {
		return MonitorSummary{}, fmt.Errorf("summarize notification deliveries: %w", err)
	}
	if oldestQueued != "" {
		value, err := time.Parse(time.RFC3339Nano, oldestQueued)
		if err != nil {
			return MonitorSummary{}, fmt.Errorf("parse queued notification time: %w", err)
		}
		summary.OldestQueuedAt = &value
	}
	return summary, nil
}

func (s *Service) DeferAccountCollection(
	ctx context.Context,
	accountID int64,
	nextAttempt time.Time,
	reason string,
) error {
	if accountID <= 0 {
		return errors.New("account ID must be positive")
	}
	if nextAttempt.IsZero() {
		return errors.New("next collection time must be provided")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO monitor_account_states (
	account_id, next_collection_at, consecutive_failures, last_error, updated_at
) VALUES (?, ?, 1, ?, ?)
ON CONFLICT(account_id) DO UPDATE SET
	next_collection_at = excluded.next_collection_at,
	consecutive_failures = monitor_account_states.consecutive_failures + 1,
	last_error = excluded.last_error,
	updated_at = excluded.updated_at`,
		accountID,
		nextAttempt.UTC().Format(time.RFC3339Nano),
		truncate(reason, 500),
		now,
	); err != nil {
		return fmt.Errorf("defer account collection: %w", err)
	}
	return nil
}

func (s *Service) ClearAccountCollectionDeferral(
	ctx context.Context,
	accountID int64,
) error {
	if accountID <= 0 {
		return errors.New("account ID must be positive")
	}
	if _, err := s.db.ExecContext(ctx, `
DELETE FROM monitor_account_states WHERE account_id = ?`, accountID); err != nil {
		return fmt.Errorf("clear account collection deferral: %w", err)
	}
	return nil
}

func normalizeBatchInput(input IngestBatchInput) (IngestBatchInput, error) {
	input, err := normalizeBatchFields(input)
	if err != nil {
		return IngestBatchInput{}, err
	}
	if input.BatchID == "" {
		value, err := newBatchID()
		if err != nil {
			return IngestBatchInput{}, err
		}
		input.BatchID = value
	}
	return input, nil
}

// IdempotentBatchID derives a stable ID from the complete batch payload.
// Replaying an identical collection therefore reuses its original result.
func IdempotentBatchID(input IngestBatchInput) (string, error) {
	input.BatchID = ""
	normalized, err := normalizeBatchFields(input)
	if err != nil {
		return "", err
	}
	value := batchPayloadHash(normalized)
	if value == "" {
		return "", errors.New("hash ingestion batch payload")
	}
	return value, nil
}

func normalizeBatchFields(input IngestBatchInput) (IngestBatchInput, error) {
	input.Source = strings.TrimSpace(input.Source)
	input.PeerType = strings.TrimSpace(input.PeerType)
	input.PeerTitle = strings.TrimSpace(input.PeerTitle)
	if input.Source == "" {
		input.Source = "unknown"
	}
	if input.AccountID <= 0 || input.PeerID <= 0 {
		return IngestBatchInput{}, errors.New("batch account and peer IDs must be positive")
	}
	if input.PeerType != "user" && input.PeerType != "chat" && input.PeerType != "channel" {
		return IngestBatchInput{}, errors.New("batch peer type must be user, chat, or channel")
	}
	if input.CheckpointBefore < 0 || input.CheckpointAfter < 0 {
		return IngestBatchInput{}, errors.New("batch checkpoints must not be negative")
	}
	return input, nil
}

func existingBatchResultTx(
	ctx context.Context,
	tx *sql.Tx,
	input IngestBatchInput,
) (IngestBatchResult, bool, error) {
	var (
		result      IngestBatchResult
		payloadHash string
	)
	err := tx.QueryRowContext(ctx, `
SELECT inserted_count, event_count, payload_sha256
FROM ingestion_batches
WHERE batch_id = ?`, input.BatchID).Scan(
		&result.Inserted,
		&result.Events,
		&payloadHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return IngestBatchResult{}, false, nil
	}
	if err != nil {
		return IngestBatchResult{}, false, fmt.Errorf("read existing ingestion batch: %w", err)
	}
	if payloadHash == "" || payloadHash != batchPayloadHash(input) {
		return IngestBatchResult{}, false, ErrBatchConflict
	}
	result.BatchID = input.BatchID
	result.Alerts = []Alert{}
	return result, true, nil
}

func batchPayloadHash(input IngestBatchInput) string {
	value, err := json.Marshal(struct {
		Source           string          `json:"source"`
		AccountID        int64           `json:"account_id"`
		PeerType         string          `json:"peer_type"`
		PeerID           int64           `json:"peer_id"`
		PeerTitle        string          `json:"peer_title"`
		CheckpointBefore int             `json:"checkpoint_before"`
		CheckpointAfter  int             `json:"checkpoint_after"`
		Complete         bool            `json:"complete"`
		BacklogDetected  bool            `json:"backlog_detected"`
		Messages         []MessageRecord `json:"messages"`
	}{
		Source:           input.Source,
		AccountID:        input.AccountID,
		PeerType:         input.PeerType,
		PeerID:           input.PeerID,
		PeerTitle:        input.PeerTitle,
		CheckpointBefore: input.CheckpointBefore,
		CheckpointAfter:  input.CheckpointAfter,
		Complete:         input.Complete,
		BacklogDetected:  input.BacklogDetected,
		Messages:         input.Messages,
	})
	if err != nil {
		return ""
	}
	return sha256Hex(string(value))
}

func messageContentHash(message MessageRecord) string {
	return sha256Hex(strings.Join([]string{
		message.Sender,
		message.Text,
		message.MediaType,
		message.Media.Kind,
		fmt.Sprintf("%d", message.Media.ID),
		message.Media.MimeType,
		message.Media.FileName,
		fmt.Sprintf("%d", message.Media.Size),
		fmt.Sprintf("%d", message.Media.Width),
		fmt.Sprintf("%d", message.Media.Height),
		message.SentAt.UTC().Format(time.RFC3339Nano),
	}, "\x00"))
}

func mediaEvidence(message MessageRecord) map[string]any {
	media := message.Media
	result := map[string]any{}
	hasMedia := message.MediaType != "" || media.Type != "" ||
		media.Kind != "" || media.ID != 0
	if !hasMedia {
		return result
	}
	if message.MediaType != "" {
		result["type"] = message.MediaType
	}
	if media.Type != "" {
		result["type"] = media.Type
	}
	if media.Kind != "" {
		result["kind"] = media.Kind
	}
	if media.ID != 0 {
		result["id"] = media.ID
	}
	if media.DCID != 0 {
		result["dc_id"] = media.DCID
	}
	if media.MimeType != "" {
		result["mime_type"] = media.MimeType
	}
	if media.FileName != "" {
		result["file_name"] = media.FileName
	}
	if media.Size != 0 {
		result["size"] = media.Size
	}
	if media.Width != 0 {
		result["width"] = media.Width
	}
	if media.Height != 0 {
		result["height"] = media.Height
	}
	if media.Spoiler {
		result["spoiler"] = true
	}
	if media.Video {
		result["video"] = true
	}
	if media.Voice {
		result["voice"] = true
	}
	if media.Round {
		result["round"] = true
	}
	if media.AccessHash != 0 || media.FileReference != "" {
		result["reference_sha256"] = sha256Hex(fmt.Sprintf(
			"%d\x00%s\x00%d",
			media.ID,
			media.FileReference,
			media.AccessHash,
		))
	}
	result["metadata_sha256"] = mediaMetadataHash(message)
	result["storage_state"] = "reference_only"
	return result
}

func mediaMetadataHash(message MessageRecord) string {
	media := message.Media
	mediaType := media.Type
	if mediaType == "" {
		mediaType = message.MediaType
	}
	return sha256Hex(strings.Join([]string{
		mediaType,
		media.Kind,
		fmt.Sprintf("%d", media.ID),
		fmt.Sprintf("%d", media.DCID),
		media.MimeType,
		media.FileName,
		fmt.Sprintf("%d", media.Size),
		fmt.Sprintf("%d", media.Width),
		fmt.Sprintf("%d", media.Height),
		fmt.Sprintf("%t", media.Spoiler),
		fmt.Sprintf("%t", media.Video),
		fmt.Sprintf("%t", media.Voice),
		fmt.Sprintf("%t", media.Round),
	}, "\x00"))
}

type rawEvidenceMessage struct {
	AccountID int64       `json:"account_id"`
	PeerType  string      `json:"peer_type"`
	PeerID    int64       `json:"peer_id"`
	PeerTitle string      `json:"peer_title"`
	MessageID int         `json:"message_id"`
	Sender    string      `json:"sender"`
	Text      string      `json:"text"`
	MediaType string      `json:"media_type,omitempty"`
	Media     MediaRecord `json:"media"`
	SentAt    time.Time   `json:"sent_at"`
}

func evidenceRawMessage(message MessageRecord) rawEvidenceMessage {
	media := message.Media
	media.AccessHash = 0
	media.FileReference = ""
	return rawEvidenceMessage{
		AccountID: message.AccountID,
		PeerType:  message.PeerType,
		PeerID:    message.PeerID,
		PeerTitle: message.PeerTitle,
		MessageID: message.MessageID,
		Sender:    message.Sender,
		Text:      message.Text,
		MediaType: message.MediaType,
		Media:     media,
		SentAt:    message.SentAt,
	}
}

func evidenceEventID(
	batchID string,
	message MessageRecord,
	eventType string,
	version int,
) string {
	return sha256Hex(fmt.Sprintf(
		"%s|%d|%s|%d|%d|%s|%d",
		batchID,
		message.AccountID,
		message.PeerType,
		message.PeerID,
		message.MessageID,
		eventType,
		version,
	))
}

func evidenceDeletionID(
	batchID string,
	accountID int64,
	peerType string,
	peerID int64,
	messageID int,
	version int,
) string {
	return sha256Hex(fmt.Sprintf(
		"%s|%d|%s|%d|%d|delete|%d",
		batchID,
		accountID,
		peerType,
		peerID,
		messageID,
		version,
	))
}

func inputPeerTitle(input IngestBatchInput, message MessageRecord) string {
	if message.PeerTitle != "" {
		return truncate(message.PeerTitle, 120)
	}
	return truncate(input.PeerTitle, 120)
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newBatchID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate ingestion batch ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func scanEvidenceEvent(row scanner) (EvidenceEvent, error) {
	var (
		item               EvidenceEvent
		mediaJSON, rawJSON string
		sentAt, observedAt string
	)
	if err := row.Scan(
		&item.Sequence,
		&item.EventID,
		&item.BatchID,
		&item.AccountID,
		&item.PeerType,
		&item.PeerID,
		&item.PeerTitle,
		&item.MessageID,
		&item.EventType,
		&item.Version,
		&item.Actor,
		&item.Sender,
		&item.Text,
		&mediaJSON,
		&rawJSON,
		&sentAt,
		&observedAt,
		&item.ContentSHA256,
		&item.SchemaVersion,
	); err != nil {
		return EvidenceEvent{}, fmt.Errorf("scan evidence event: %w", err)
	}
	item.Media = json.RawMessage(mediaJSON)
	item.Raw = json.RawMessage(rawJSON)
	if sentAt != "" {
		value, err := time.Parse(time.RFC3339Nano, sentAt)
		if err != nil {
			return EvidenceEvent{}, fmt.Errorf("parse evidence sent_at: %w", err)
		}
		item.SentAt = &value
	}
	value, err := time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return EvidenceEvent{}, fmt.Errorf("parse evidence observed_at: %w", err)
	}
	item.ObservedAt = value
	return item, nil
}
