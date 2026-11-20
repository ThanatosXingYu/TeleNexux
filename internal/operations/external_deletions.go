package operations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PendingDeletionPending   = "pending"
	PendingDeletionAmbiguous = "ambiguous"
	PendingDeletionMissing   = "missing"
	externalDeletionActor    = "telegram-update"
)

type PendingDeletion struct {
	AccountID     int64      `json:"account_id"`
	PeerType      string     `json:"peer_type"`
	PeerID        int64      `json:"peer_id"`
	MessageID     int        `json:"message_id"`
	ObservedAt    time.Time  `json:"observed_at"`
	State         string     `json:"state"`
	Attempts      int        `json:"attempts"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
}

// EnqueueExternalDeletion records a Telegram deletion notification without
// guessing which chat a message belonged to when the update lacks peer data.
func (s *Service) EnqueueExternalDeletion(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
	messageIDs []int,
) (int, error) {
	peerType = strings.ToLower(strings.TrimSpace(peerType))
	if err := validateExternalDeletion(accountID, peerType, peerID, messageIDs); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin external deletion candidates: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := s.now().UTC().Format(time.RFC3339Nano)
	inserted := 0
	seen := make(map[int]struct{}, len(messageIDs))
	for _, messageID := range messageIDs {
		if _, duplicate := seen[messageID]; duplicate {
			continue
		}
		seen[messageID] = struct{}{}
		result, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO pending_message_deletions (
	account_id, peer_type, peer_id, message_id, observed_at, state,
	attempts, last_attempt_at, last_error
) VALUES (?, ?, ?, ?, ?, 'pending', 0, '', '')`,
			accountID,
			peerType,
			peerID,
			messageID,
			now,
		)
		if err != nil {
			return 0, fmt.Errorf("enqueue external deletion candidate: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("check external deletion candidate: %w", err)
		}
		inserted += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit external deletion candidates: %w", err)
	}
	return inserted, nil
}

func validateExternalDeletion(
	accountID int64,
	peerType string,
	peerID int64,
	messageIDs []int,
) error {
	peerType = strings.ToLower(strings.TrimSpace(peerType))
	if accountID <= 0 || utf8.RuneCountInString(peerType) == 0 {
		return errors.New("external deletion requires account and peer type")
	}
	if peerType != "unknown" && peerType != "user" &&
		peerType != "chat" && peerType != "channel" {
		return errors.New("external deletion peer type is invalid")
	}
	if peerID < 0 || (peerType != "unknown" && peerID <= 0) {
		return errors.New("external deletion peer ID is invalid")
	}
	if len(messageIDs) == 0 {
		return errors.New("external deletion requires message IDs")
	}
	for _, messageID := range messageIDs {
		if messageID <= 0 {
			return errors.New("external deletion message IDs must be positive")
		}
	}
	return nil
}

func (s *Service) ListPendingDeletions(
	ctx context.Context,
	state string,
	limit int,
) ([]PendingDeletion, error) {
	state = strings.ToLower(strings.TrimSpace(state))
	if state != "" && state != PendingDeletionPending &&
		state != PendingDeletionAmbiguous && state != PendingDeletionMissing {
		return nil, errors.New("pending deletion state is invalid")
	}
	limit = normalizeLimit(limit, 100, 500)
	query := `
SELECT account_id, peer_type, peer_id, message_id, observed_at, state,
	attempts, last_attempt_at, last_error
FROM pending_message_deletions`
	args := make([]any, 0, 2)
	if state != "" {
		query += ` WHERE state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY observed_at, account_id, message_id LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list pending deletions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]PendingDeletion, 0, limit)
	for rows.Next() {
		var (
			item          PendingDeletion
			observedAt    string
			lastAttemptAt string
		)
		if err := rows.Scan(
			&item.AccountID,
			&item.PeerType,
			&item.PeerID,
			&item.MessageID,
			&observedAt,
			&item.State,
			&item.Attempts,
			&lastAttemptAt,
			&item.LastError,
		); err != nil {
			return nil, fmt.Errorf("scan pending deletion: %w", err)
		}
		item.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return nil, fmt.Errorf("parse pending deletion observed_at: %w", err)
		}
		if lastAttemptAt != "" {
			value, err := time.Parse(time.RFC3339Nano, lastAttemptAt)
			if err != nil {
				return nil, fmt.Errorf("parse pending deletion attempt time: %w", err)
			}
			item.LastAttemptAt = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending deletions: %w", err)
	}
	return items, nil
}

// ResolveExternalDeletions drains candidates that have an unambiguous local
// projection match. Ambiguous and unmatched candidates remain visible for
// operator diagnosis and never invent a chat or evidence event.
func (s *Service) ResolveExternalDeletions(
	ctx context.Context,
	limit int,
) (int, error) {
	limit = normalizeLimit(limit, 100, 500)
	candidates, err := s.ListPendingDeletions(ctx, PendingDeletionPending, limit)
	if err != nil {
		return 0, err
	}
	resolved := 0
	for _, candidate := range candidates {
		didResolve, err := s.resolveExternalDeletion(
			ctx,
			candidate,
			s.now().UTC(),
		)
		if err != nil {
			return resolved, err
		}
		if didResolve {
			resolved++
		}
	}
	return resolved, nil
}

type externalDeletionMatch struct {
	PeerType string
	PeerID   int64
}

func (s *Service) resolveExternalDeletion(
	ctx context.Context,
	candidate PendingDeletion,
	now time.Time,
) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin external deletion resolution: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	matches, err := externalDeletionMatchesTx(ctx, tx, candidate)
	if err != nil {
		return false, err
	}
	switch len(matches) {
	case 0:
		recorded, err := recordedExternalDeletionMatchesTx(ctx, tx, candidate)
		if err != nil {
			return false, err
		}
		if len(recorded) == 1 {
			if err := deletePendingDeletionTx(ctx, tx, candidate); err != nil {
				return false, err
			}
			if err := tx.Commit(); err != nil {
				return false, fmt.Errorf("commit recorded external deletion: %w", err)
			}
			return true, nil
		}
		if len(recorded) > 1 {
			if err := updatePendingDeletionTx(
				ctx,
				tx,
				candidate,
				PendingDeletionAmbiguous,
				now,
				"multiple deletion evidence records matched the notice",
			); err != nil {
				return false, err
			}
			break
		}
		if err := updatePendingDeletionTx(
			ctx,
			tx,
			candidate,
			PendingDeletionMissing,
			now,
			"local message projection was not found",
		); err != nil {
			return false, err
		}
	case 1:
		batchID, err := newBatchID()
		if err != nil {
			return false, err
		}
		added, err := s.recordDeletionEventTx(
			ctx,
			tx,
			batchID,
			candidate.AccountID,
			matches[0].PeerType,
			matches[0].PeerID,
			candidate.MessageID,
			externalDeletionActor,
			now,
		)
		if err != nil {
			return false, err
		}
		if added {
			if err := s.insertBatchTx(ctx, tx, IngestBatchInput{
				BatchID:   batchID,
				Source:    "message_delete",
				AccountID: candidate.AccountID,
				PeerType:  matches[0].PeerType,
				PeerID:    matches[0].PeerID,
			}, "completed", IngestBatchResult{Events: 1}, 0, now); err != nil {
				return false, err
			}
		}
		if err := deletePendingDeletionTx(ctx, tx, candidate); err != nil {
			return false, err
		}
	default:
		if err := updatePendingDeletionTx(
			ctx,
			tx,
			candidate,
			PendingDeletionAmbiguous,
			now,
			"multiple local message projections matched the deletion",
		); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit external deletion resolution: %w", err)
	}
	return len(matches) == 1, nil
}

func externalDeletionMatchesTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate PendingDeletion,
) ([]externalDeletionMatch, error) {
	query := `
SELECT DISTINCT peer_type, peer_id
FROM indexed_messages
WHERE account_id = ? AND message_id = ? AND deleted_at = ''`
	args := []any{candidate.AccountID, candidate.MessageID}
	if candidate.PeerType != "unknown" {
		query += ` AND peer_type = ? AND peer_id = ?`
		args = append(args, candidate.PeerType, candidate.PeerID)
	}
	query += ` ORDER BY peer_type, peer_id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("match external deletion: %w", err)
	}
	defer func() { _ = rows.Close() }()
	matches := make([]externalDeletionMatch, 0, 2)
	for rows.Next() {
		var match externalDeletionMatch
		if err := rows.Scan(&match.PeerType, &match.PeerID); err != nil {
			return nil, fmt.Errorf("scan external deletion match: %w", err)
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate external deletion matches: %w", err)
	}
	return matches, nil
}

func recordedExternalDeletionMatchesTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate PendingDeletion,
) ([]externalDeletionMatch, error) {
	query := `
SELECT DISTINCT e.peer_type, e.peer_id
FROM evidence_events e
WHERE e.account_id = ? AND e.message_id = ? AND e.event_type = 'delete'
	AND e.version = (
		SELECT MAX(latest.version)
		FROM evidence_events latest
		WHERE latest.account_id = e.account_id
			AND latest.peer_type = e.peer_type
			AND latest.peer_id = e.peer_id
			AND latest.message_id = e.message_id
	)`
	args := []any{candidate.AccountID, candidate.MessageID}
	if candidate.PeerType != "unknown" {
		query += ` AND e.peer_type = ? AND e.peer_id = ?`
		args = append(args, candidate.PeerType, candidate.PeerID)
	}
	query += ` ORDER BY e.peer_type, e.peer_id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("match recorded external deletion: %w", err)
	}
	defer func() { _ = rows.Close() }()
	matches := make([]externalDeletionMatch, 0, 2)
	for rows.Next() {
		var match externalDeletionMatch
		if err := rows.Scan(&match.PeerType, &match.PeerID); err != nil {
			return nil, fmt.Errorf("scan recorded deletion match: %w", err)
		}
		matches = append(matches, match)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recorded deletion matches: %w", err)
	}
	return matches, nil
}

func deletePendingDeletionTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate PendingDeletion,
) error {
	if _, err := tx.ExecContext(ctx, `
DELETE FROM pending_message_deletions
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?
	AND state = ?`,
		candidate.AccountID,
		candidate.PeerType,
		candidate.PeerID,
		candidate.MessageID,
		PendingDeletionPending,
	); err != nil {
		return fmt.Errorf("remove resolved external deletion: %w", err)
	}
	return nil
}

// RequeueExternalDeletion makes a diagnosed candidate eligible for another
// resolution pass. Completed candidates are already removed and cannot be
// requeued through this method.
func (s *Service) RequeueExternalDeletion(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
	messageID int,
) error {
	peerType = strings.ToLower(strings.TrimSpace(peerType))
	if err := validateExternalDeletion(
		accountID,
		peerType,
		peerID,
		[]int{messageID},
	); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE pending_message_deletions
SET state = 'pending', last_attempt_at = '', last_error = ''
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?
	AND state IN (?, ?)`,
		accountID,
		peerType,
		peerID,
		messageID,
		PendingDeletionAmbiguous,
		PendingDeletionMissing,
	)
	if err != nil {
		return fmt.Errorf("requeue external deletion: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check requeued external deletion: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func updatePendingDeletionTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate PendingDeletion,
	state string,
	now time.Time,
	reason string,
) error {
	_, err := tx.ExecContext(ctx, `
UPDATE pending_message_deletions
SET state = ?, attempts = attempts + 1, last_attempt_at = ?, last_error = ?
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?
	AND state = ?`,
		state,
		now.Format(time.RFC3339Nano),
		truncate(reason, 300),
		candidate.AccountID,
		candidate.PeerType,
		candidate.PeerID,
		candidate.MessageID,
		PendingDeletionPending,
	)
	if err != nil {
		return fmt.Errorf("update external deletion candidate: %w", err)
	}
	return nil
}
