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

const retentionBatchSize = 1000

var ErrInvalidRetention = errors.New("invalid retention request")

type RetentionPolicy struct {
	MessageRetentionDays int       `json:"message_retention_days"`
	Version              int       `json:"version"`
	UpdatedBy            string    `json:"updated_by"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type LegalHold struct {
	ID         int64      `json:"id"`
	Scope      string     `json:"scope"`
	AccountID  int64      `json:"account_id"`
	PeerType   string     `json:"peer_type,omitempty"`
	PeerID     int64      `json:"peer_id,omitempty"`
	MessageID  int        `json:"message_id,omitempty"`
	Reason     string     `json:"reason"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ReleasedBy string     `json:"released_by,omitempty"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}

type LegalHoldInput struct {
	Scope     string
	AccountID int64
	PeerType  string
	PeerID    int64
	MessageID int
	Reason    string
	CreatedBy string
}

type RetentionCleanupInput struct {
	RetentionDays int
	DryRun        bool
	Actor         string
}

type RetentionResult struct {
	DryRun                  bool      `json:"dry_run"`
	RetentionDays           int       `json:"retention_days"`
	Cutoff                  time.Time `json:"cutoff"`
	CandidateMessages       int       `json:"candidate_messages"`
	CandidateEvidence       int       `json:"candidate_evidence_events"`
	HeldMessages            int       `json:"held_messages"`
	DeletedMessages         int       `json:"deleted_messages"`
	DestroyedEvidenceEvents int       `json:"destroyed_evidence_events"`
	HasMore                 bool      `json:"has_more"`
}

type retentionCandidate struct {
	AccountID   int64
	PeerType    string
	PeerID      int64
	MessageID   int
	ContentHash string
}

func (s *Service) GetRetentionPolicy(ctx context.Context) (RetentionPolicy, error) {
	var (
		policy    RetentionPolicy
		updatedAt string
	)
	if err := s.db.QueryRowContext(ctx, `
SELECT message_retention_days, version, updated_by, updated_at
FROM retention_policies
WHERE id = 1`).Scan(
		&policy.MessageRetentionDays,
		&policy.Version,
		&policy.UpdatedBy,
		&updatedAt,
	); err != nil {
		return RetentionPolicy{}, fmt.Errorf("read retention policy: %w", err)
	}
	value, err := time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return RetentionPolicy{}, fmt.Errorf("parse retention policy update time: %w", err)
	}
	policy.UpdatedAt = value
	return policy, nil
}

func (s *Service) SetRetentionPolicy(
	ctx context.Context,
	retentionDays int,
	actor string,
) (RetentionPolicy, error) {
	if retentionDays < 1 || retentionDays > 3650 {
		return RetentionPolicy{}, invalidRetention("retention days must be between 1 and 3650")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" || utf8.RuneCountInString(actor) > 64 {
		return RetentionPolicy{}, invalidRetention("retention policy actor is required")
	}
	if _, err := s.db.ExecContext(ctx, `
UPDATE retention_policies
SET message_retention_days = ?, version = version + 1,
	updated_by = ?, updated_at = ?
WHERE id = 1`,
		retentionDays,
		actor,
		s.now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return RetentionPolicy{}, fmt.Errorf("update retention policy: %w", err)
	}
	return s.GetRetentionPolicy(ctx)
}

func (s *Service) ListLegalHolds(ctx context.Context) ([]LegalHold, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, scope, account_id, peer_type, peer_id, message_id, reason,
	created_by, created_at, released_by, released_at
FROM legal_holds
ORDER BY CASE WHEN released_at = '' THEN 0 ELSE 1 END, created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list legal holds: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]LegalHold, 0)
	for rows.Next() {
		item, err := scanLegalHold(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legal holds: %w", err)
	}
	return items, nil
}

func (s *Service) CreateLegalHold(
	ctx context.Context,
	input LegalHoldInput,
) (LegalHold, error) {
	normalized, err := normalizeLegalHoldInput(input)
	if err != nil {
		return LegalHold{}, err
	}
	result, err := s.db.ExecContext(ctx, `
INSERT INTO legal_holds (
	scope, account_id, peer_type, peer_id, message_id, reason, created_by,
	created_at, released_by, released_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '')`,
		normalized.Scope,
		normalized.AccountID,
		normalized.PeerType,
		normalized.PeerID,
		normalized.MessageID,
		normalized.Reason,
		normalized.CreatedBy,
		s.now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return LegalHold{}, fmt.Errorf("create legal hold: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return LegalHold{}, fmt.Errorf("read legal hold ID: %w", err)
	}
	return s.getLegalHold(ctx, id)
}

func (s *Service) ReleaseLegalHold(
	ctx context.Context,
	id int64,
	actor string,
) (LegalHold, error) {
	actor = strings.TrimSpace(actor)
	if id <= 0 || actor == "" || utf8.RuneCountInString(actor) > 64 {
		return LegalHold{}, invalidRetention("legal hold ID and release actor are required")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE legal_holds
SET released_by = ?, released_at = ?
WHERE id = ? AND released_at = ''`,
		actor,
		s.now().UTC().Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return LegalHold{}, fmt.Errorf("release legal hold: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return LegalHold{}, fmt.Errorf("check released legal hold: %w", err)
	}
	if affected == 0 {
		return LegalHold{}, ErrNotFound
	}
	return s.getLegalHold(ctx, id)
}

func (s *Service) CleanupMessages(
	ctx context.Context,
	input RetentionCleanupInput,
) (RetentionResult, error) {
	policy, err := s.GetRetentionPolicy(ctx)
	if err != nil {
		return RetentionResult{}, err
	}
	retentionDays := input.RetentionDays
	if retentionDays == 0 {
		retentionDays = policy.MessageRetentionDays
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return RetentionResult{}, invalidRetention("retention days must be between 1 and 3650")
	}
	now := s.now().UTC()
	cutoff := now.AddDate(0, 0, -retentionDays)
	result := RetentionResult{
		DryRun:        input.DryRun,
		RetentionDays: retentionDays,
		Cutoff:        cutoff,
	}
	if input.DryRun {
		if err := s.fillRetentionPreview(ctx, cutoff, &result); err != nil {
			return RetentionResult{}, err
		}
		return result, nil
	}
	actor := strings.TrimSpace(input.Actor)
	if actor == "" || utf8.RuneCountInString(actor) > 64 {
		return RetentionResult{}, invalidRetention("retention cleanup actor is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RetentionResult{}, fmt.Errorf("begin retention cleanup: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	candidates, hasMore, err := retentionCandidatesTx(
		ctx,
		tx,
		cutoff,
		retentionBatchSize,
	)
	if err != nil {
		return RetentionResult{}, err
	}
	result.HasMore = hasMore
	result.CandidateMessages = len(candidates)
	for _, candidate := range candidates {
		evidenceCount, err := s.destroyRetentionCandidateTx(
			ctx,
			tx,
			candidate,
			retentionDays,
			actor,
			cutoff,
			now,
		)
		if err != nil {
			return RetentionResult{}, err
		}
		result.DeletedMessages++
		result.DestroyedEvidenceEvents += evidenceCount
		result.CandidateEvidence += evidenceCount
	}
	orphanEvidence, orphanHasMore, err := orphanEvidenceCandidatesTx(
		ctx,
		tx,
		cutoff,
		retentionBatchSize,
	)
	if err != nil {
		return RetentionResult{}, err
	}
	result.HasMore = result.HasMore || orphanHasMore
	for _, candidate := range orphanEvidence {
		evidenceCount, err := destroyOrphanEvidenceTx(
			ctx,
			tx,
			candidate,
			retentionDays,
			actor,
			now,
		)
		if err != nil {
			return RetentionResult{}, err
		}
		result.DestroyedEvidenceEvents += evidenceCount
		result.CandidateEvidence += evidenceCount
	}
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM indexed_messages AS m
WHERE m.sent_at < ? AND `+activeLegalHoldCondition("m"),
		cutoff.Format(time.RFC3339Nano),
	).Scan(&result.HeldMessages); err != nil {
		return RetentionResult{}, fmt.Errorf("count retained messages: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RetentionResult{}, fmt.Errorf("commit retention cleanup: %w", err)
	}
	return result, nil
}

func (s *Service) fillRetentionPreview(
	ctx context.Context,
	cutoff time.Time,
	result *RetentionResult,
) error {
	cutoffText := cutoff.Format(time.RFC3339Nano)
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM indexed_messages AS m
WHERE m.sent_at < ? AND NOT `+activeLegalHoldCondition("m"),
		cutoffText,
	).Scan(&result.CandidateMessages); err != nil {
		return fmt.Errorf("count retention candidates: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM evidence_events AS e
WHERE (
	EXISTS (
		SELECT 1
		FROM indexed_messages AS m
		WHERE m.account_id = e.account_id
			AND m.peer_type = e.peer_type
			AND m.peer_id = e.peer_id
			AND m.message_id = e.message_id
			AND m.sent_at < ?
			AND NOT `+activeLegalHoldCondition("m")+`
	)
	OR (
		NOT EXISTS (
			SELECT 1
			FROM indexed_messages AS m
			WHERE m.account_id = e.account_id
				AND m.peer_type = e.peer_type
				AND m.peer_id = e.peer_id
				AND m.message_id = e.message_id
		)
		AND COALESCE(NULLIF(e.sent_at, ''), e.observed_at) < ?
		AND NOT `+activeLegalHoldCondition("e")+`
	)
)`,
		cutoffText,
		cutoffText,
	).Scan(&result.CandidateEvidence); err != nil {
		return fmt.Errorf("count retention evidence: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(1)
FROM indexed_messages AS m
WHERE m.sent_at < ? AND `+activeLegalHoldCondition("m"),
		cutoffText,
	).Scan(&result.HeldMessages); err != nil {
		return fmt.Errorf("count held retention messages: %w", err)
	}
	return nil
}

func retentionCandidatesTx(
	ctx context.Context,
	tx *sql.Tx,
	cutoff time.Time,
	limit int,
) ([]retentionCandidate, bool, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT m.account_id, m.peer_type, m.peer_id, m.message_id,
	COALESCE(m.content_hash, '')
FROM indexed_messages AS m
WHERE m.sent_at < ? AND NOT `+activeLegalHoldCondition("m")+`
ORDER BY m.sent_at, m.account_id, m.peer_type, m.peer_id, m.message_id
LIMIT ?`,
		cutoff.Format(time.RFC3339Nano),
		limit+1,
	)
	if err != nil {
		return nil, false, fmt.Errorf("list retention candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]retentionCandidate, 0, limit)
	for rows.Next() {
		var item retentionCandidate
		if err := rows.Scan(
			&item.AccountID,
			&item.PeerType,
			&item.PeerID,
			&item.MessageID,
			&item.ContentHash,
		); err != nil {
			return nil, false, fmt.Errorf("scan retention candidate: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate retention candidates: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func orphanEvidenceCandidatesTx(
	ctx context.Context,
	tx *sql.Tx,
	cutoff time.Time,
	limit int,
) ([]retentionCandidate, bool, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT e.account_id, e.peer_type, e.peer_id, e.message_id,
	MAX(e.content_sha256)
FROM evidence_events AS e
WHERE COALESCE(NULLIF(e.sent_at, ''), e.observed_at) < ?
	AND NOT EXISTS (
		SELECT 1
		FROM indexed_messages AS m
		WHERE m.account_id = e.account_id
			AND m.peer_type = e.peer_type
			AND m.peer_id = e.peer_id
			AND m.message_id = e.message_id
	)
	AND NOT `+activeLegalHoldCondition("e")+`
GROUP BY e.account_id, e.peer_type, e.peer_id, e.message_id
ORDER BY MIN(e.observed_at), e.account_id, e.peer_type, e.peer_id, e.message_id
LIMIT ?`,
		cutoff.Format(time.RFC3339Nano),
		limit+1,
	)
	if err != nil {
		return nil, false, fmt.Errorf("list orphan evidence candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]retentionCandidate, 0, limit)
	for rows.Next() {
		var item retentionCandidate
		if err := rows.Scan(
			&item.AccountID,
			&item.PeerType,
			&item.PeerID,
			&item.MessageID,
			&item.ContentHash,
		); err != nil {
			return nil, false, fmt.Errorf("scan orphan evidence candidate: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate orphan evidence candidates: %w", err)
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func destroyOrphanEvidenceTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate retentionCandidate,
	retentionDays int,
	actor string,
	now time.Time,
) (int, error) {
	result, err := tx.ExecContext(ctx, `
DELETE FROM evidence_events
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?
	AND COALESCE(NULLIF(sent_at, ''), observed_at) < ?
	AND NOT EXISTS (
		SELECT 1
		FROM indexed_messages AS m
		WHERE m.account_id = evidence_events.account_id
			AND m.peer_type = evidence_events.peer_type
			AND m.peer_id = evidence_events.peer_id
			AND m.message_id = evidence_events.message_id
	)
	AND NOT `+activeLegalHoldCondition("evidence_events"),
		candidate.AccountID,
		candidate.PeerType,
		candidate.PeerID,
		candidate.MessageID,
		now.AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("delete orphan message evidence: %w", err)
	}
	evidenceCount, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count orphan message evidence: %w", err)
	}
	if evidenceCount == 0 {
		return 0, nil
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE monitor_alerts
SET message_preview = ''
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?`,
		candidate.AccountID,
		candidate.PeerType,
		candidate.PeerID,
		candidate.MessageID,
	); err != nil {
		return 0, fmt.Errorf("scrub expired orphan alert preview: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO retention_destructions (
	account_id, peer_type, peer_id, message_id, content_hash,
	evidence_event_count, retention_days, destroyed_by, reason, destroyed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'retention_expired_orphan_evidence', ?)`,
		candidate.AccountID,
		candidate.PeerType,
		candidate.PeerID,
		candidate.MessageID,
		candidate.ContentHash,
		evidenceCount,
		retentionDays,
		actor,
		now.Format(time.RFC3339Nano),
	); err != nil {
		return 0, fmt.Errorf("record orphan evidence destruction: %w", err)
	}
	return int(evidenceCount), nil
}

func (s *Service) destroyRetentionCandidateTx(
	ctx context.Context,
	tx *sql.Tx,
	candidate retentionCandidate,
	retentionDays int,
	actor string,
	cutoff time.Time,
	now time.Time,
) (int, error) {
	deleted, err := tx.ExecContext(ctx, `
DELETE FROM indexed_messages
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?
	AND sent_at < ?
	AND NOT `+activeLegalHoldCondition("indexed_messages"),
		candidate.AccountID,
		candidate.PeerType,
		candidate.PeerID,
		candidate.MessageID,
		cutoff.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("delete retained message projection: %w", err)
	}
	affected, err := deleted.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("check retained message deletion: %w", err)
	}
	if affected != 1 {
		return 0, errors.New("retention candidate changed before destruction")
	}
	evidence, err := tx.ExecContext(ctx, `
DELETE FROM evidence_events
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?`,
		candidate.AccountID,
		candidate.PeerType,
		candidate.PeerID,
		candidate.MessageID,
	)
	if err != nil {
		return 0, fmt.Errorf("delete retained message evidence: %w", err)
	}
	evidenceCount, err := evidence.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count destroyed message evidence: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE monitor_alerts
SET message_preview = ''
WHERE account_id = ? AND peer_type = ? AND peer_id = ? AND message_id = ?`,
		candidate.AccountID,
		candidate.PeerType,
		candidate.PeerID,
		candidate.MessageID,
	); err != nil {
		return 0, fmt.Errorf("scrub expired alert preview: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO retention_destructions (
	account_id, peer_type, peer_id, message_id, content_hash,
	evidence_event_count, retention_days, destroyed_by, reason, destroyed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'retention_expired', ?)`,
		candidate.AccountID,
		candidate.PeerType,
		candidate.PeerID,
		candidate.MessageID,
		candidate.ContentHash,
		evidenceCount,
		retentionDays,
		actor,
		now.Format(time.RFC3339Nano),
	); err != nil {
		return 0, fmt.Errorf("record retention destruction: %w", err)
	}
	return int(evidenceCount), nil
}

func normalizeLegalHoldInput(input LegalHoldInput) (LegalHoldInput, error) {
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	input.PeerType = strings.ToLower(strings.TrimSpace(input.PeerType))
	input.Reason = strings.TrimSpace(input.Reason)
	input.CreatedBy = strings.TrimSpace(input.CreatedBy)
	if input.AccountID <= 0 {
		return LegalHoldInput{}, invalidRetention("legal hold account ID must be positive")
	}
	if utf8.RuneCountInString(input.Reason) < 1 ||
		utf8.RuneCountInString(input.Reason) > 500 {
		return LegalHoldInput{}, invalidRetention("legal hold reason must contain 1 to 500 characters")
	}
	if input.CreatedBy == "" || utf8.RuneCountInString(input.CreatedBy) > 64 {
		return LegalHoldInput{}, invalidRetention("legal hold actor is required")
	}
	switch input.Scope {
	case "account":
		input.PeerType = ""
		input.PeerID = 0
		input.MessageID = 0
	case "chat":
		if input.PeerType != "user" && input.PeerType != "chat" &&
			input.PeerType != "channel" {
			return LegalHoldInput{}, invalidRetention("chat legal hold requires a valid peer type")
		}
		if input.PeerID <= 0 {
			return LegalHoldInput{}, invalidRetention("chat legal hold requires a positive peer ID")
		}
		input.MessageID = 0
	case "message":
		if input.PeerType != "user" && input.PeerType != "chat" &&
			input.PeerType != "channel" {
			return LegalHoldInput{}, invalidRetention("message legal hold requires a valid peer type")
		}
		if input.PeerID <= 0 || input.MessageID <= 0 {
			return LegalHoldInput{}, invalidRetention("message legal hold requires chat and message IDs")
		}
	default:
		return LegalHoldInput{}, invalidRetention("legal hold scope must be account, chat, or message")
	}
	return input, nil
}

func activeLegalHoldCondition(alias string) string {
	return `EXISTS (
	SELECT 1
	FROM legal_holds AS h
	WHERE h.released_at = ''
		AND h.account_id = ` + alias + `.account_id
		AND (
			h.scope = 'account'
			OR (
				h.scope = 'chat'
				AND h.peer_type = ` + alias + `.peer_type
				AND h.peer_id = ` + alias + `.peer_id
			)
			OR (
				h.scope = 'message'
				AND h.peer_type = ` + alias + `.peer_type
				AND h.peer_id = ` + alias + `.peer_id
				AND h.message_id = ` + alias + `.message_id
			)
		)
)`
}

func invalidRetention(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidRetention, message)
}

func (s *Service) getLegalHold(ctx context.Context, id int64) (LegalHold, error) {
	item, err := scanLegalHold(s.db.QueryRowContext(ctx, `
SELECT id, scope, account_id, peer_type, peer_id, message_id, reason,
	created_by, created_at, released_by, released_at
FROM legal_holds
WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return LegalHold{}, ErrNotFound
	}
	return item, err
}

func scanLegalHold(row scanner) (LegalHold, error) {
	var (
		item                  LegalHold
		createdAt, releasedAt string
	)
	if err := row.Scan(
		&item.ID,
		&item.Scope,
		&item.AccountID,
		&item.PeerType,
		&item.PeerID,
		&item.MessageID,
		&item.Reason,
		&item.CreatedBy,
		&createdAt,
		&item.ReleasedBy,
		&releasedAt,
	); err != nil {
		return LegalHold{}, fmt.Errorf("scan legal hold: %w", err)
	}
	value, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return LegalHold{}, fmt.Errorf("parse legal hold creation time: %w", err)
	}
	item.CreatedAt = value
	if releasedAt != "" {
		value, err := time.Parse(time.RFC3339Nano, releasedAt)
		if err != nil {
			return LegalHold{}, fmt.Errorf("parse legal hold release time: %w", err)
		}
		item.ReleasedAt = &value
	}
	return item, nil
}
