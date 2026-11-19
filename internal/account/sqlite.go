package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type SQLiteRepository struct {
	db *sql.DB
}

func NewSQLiteRepository(ctx context.Context, path string) (*SQLiteRepository, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open account database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout = 5000",
		"PRAGMA journal_mode = WAL",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("configure account database: %w", err)
		}
	}

	repository := &SQLiteRepository{db: db}
	if err := repository.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure account database %q: %w", path, err)
	}
	return repository, nil
}

func (r *SQLiteRepository) Close() error {
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("close account database: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) PingContext(ctx context.Context) error {
	if err := r.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping account database: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS accounts (
	id INTEGER PRIMARY KEY,
	phone_number TEXT NOT NULL,
	username TEXT NOT NULL DEFAULT '',
	display_name TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL CHECK (status IN ('pending', 'online', 'paused', 'error')),
	status_reason TEXT NOT NULL DEFAULT '',
	last_verified_at TEXT NOT NULL DEFAULT '',
	deletion_requested_at TEXT NOT NULL DEFAULT '',
	deletion_effective_at TEXT NOT NULL DEFAULT '',
	purge_state TEXT NOT NULL DEFAULT 'active',
	purge_error TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS accounts_status_created_idx
	ON accounts(status, created_at DESC, id DESC);`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate account database: %w", err)
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "status_reason", definition: "status_reason TEXT NOT NULL DEFAULT ''"},
		{name: "last_verified_at", definition: "last_verified_at TEXT NOT NULL DEFAULT ''"},
		{
			name:       "deletion_requested_at",
			definition: "deletion_requested_at TEXT NOT NULL DEFAULT ''",
		},
		{
			name:       "deletion_effective_at",
			definition: "deletion_effective_at TEXT NOT NULL DEFAULT ''",
		},
		{name: "purge_state", definition: "purge_state TEXT NOT NULL DEFAULT 'active'"},
		{name: "purge_error", definition: "purge_error TEXT NOT NULL DEFAULT ''"},
	} {
		exists, err := r.columnExists(ctx, "accounts", column.name)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := r.db.ExecContext(
			ctx,
			"ALTER TABLE accounts ADD COLUMN "+column.definition,
		); err != nil {
			return fmt.Errorf("add accounts.%s column: %w", column.name, err)
		}
	}
	if _, err := r.db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS accounts_purge_due_idx
	ON accounts(purge_state, deletion_effective_at, id)`); err != nil {
		return fmt.Errorf("create account purge index: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) columnExists(
	ctx context.Context,
	table string,
	column string,
) (bool, error) {
	rows, err := r.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, fmt.Errorf("inspect %s schema: %w", table, err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var (
			cid        int
			name       string
			dataType   string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(
			&cid,
			&name,
			&dataType,
			&notNull,
			&defaultVal,
			&primaryKey,
		); err != nil {
			return false, fmt.Errorf("scan %s schema: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate %s schema: %w", table, err)
	}
	return false, nil
}

func (r *SQLiteRepository) Create(ctx context.Context, input CreateInput, now time.Time) (Account, error) {
	const query = `
INSERT INTO accounts (
	id, phone_number, username, display_name, status, purge_state, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, 'active', ?, ?)`
	createdAt := now.Format(time.RFC3339Nano)
	_, err := r.db.ExecContext(
		ctx,
		query,
		input.ID,
		input.PhoneNumber,
		input.Username,
		input.DisplayName,
		StatusPending,
		createdAt,
		createdAt,
	)
	if err != nil {
		return Account{}, fmt.Errorf("insert account %d: %w", input.ID, err)
	}
	return Account{
		ID:          input.ID,
		PhoneNumber: input.PhoneNumber,
		Username:    input.Username,
		DisplayName: input.DisplayName,
		Status:      StatusPending,
		PurgeState:  PurgeStateActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func (r *SQLiteRepository) Upsert(
	ctx context.Context,
	input CreateInput,
	status string,
	now time.Time,
) (Account, error) {
	const query = `
INSERT INTO accounts (
	id, phone_number, username, display_name, status, status_reason, last_verified_at,
	created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, '', ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	phone_number = excluded.phone_number,
	username = excluded.username,
	display_name = excluded.display_name,
	status = excluded.status,
	status_reason = '',
	last_verified_at = excluded.last_verified_at,
	updated_at = excluded.updated_at
WHERE accounts.purge_state = 'active'`
	timestamp := now.Format(time.RFC3339Nano)
	result, err := r.db.ExecContext(
		ctx,
		query,
		input.ID,
		input.PhoneNumber,
		input.Username,
		input.DisplayName,
		status,
		timestamp,
		timestamp,
		timestamp,
	)
	if err != nil {
		return Account{}, fmt.Errorf("upsert account %d: %w", input.ID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Account{}, fmt.Errorf("check upserted account %d: %w", input.ID, err)
	}
	if affected == 0 {
		return Account{}, ErrConflict
	}

	item, err := r.Get(ctx, input.ID)
	if err != nil {
		return Account{}, fmt.Errorf("read upserted account %d: %w", input.ID, err)
	}
	return item, nil
}

func (r *SQLiteRepository) List(ctx context.Context, filter Filter) ([]Account, error) {
	query := `
SELECT id, phone_number, username, display_name, status, status_reason,
	last_verified_at, created_at, updated_at, purge_state, purge_error,
	deletion_requested_at, deletion_effective_at
FROM accounts`
	args := make([]any, 0, 3)
	where := make([]string, 0, 2)
	if !filter.IncludeDeleted {
		where = append(where, "purge_state = 'active'")
	}
	if filter.Status != "" {
		where = append(where, "status = ?")
		args = append(args, filter.Status)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?"
	args = append(args, filter.Limit, filter.Offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			// Database/sql returns a closed-rows error when iteration already
			// exhausted the result set. Keep the iterator's error authoritative.
			_ = closeErr
		}
	}()

	accounts := make([]Account, 0, filter.Limit)
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounts: %w", err)
	}
	return accounts, nil
}

func (r *SQLiteRepository) Get(ctx context.Context, id int64) (Account, error) {
	const query = `
SELECT id, phone_number, username, display_name, status, status_reason,
	last_verified_at, created_at, updated_at, purge_state, purge_error,
	deletion_requested_at, deletion_effective_at
FROM accounts
WHERE id = ? AND purge_state = 'active'`
	item, err := scanAccount(r.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return item, err
}

func (r *SQLiteRepository) GetIncludingDeleted(ctx context.Context, id int64) (Account, error) {
	const query = `
SELECT id, phone_number, username, display_name, status, status_reason,
	last_verified_at, created_at, updated_at, purge_state, purge_error,
	deletion_requested_at, deletion_effective_at
FROM accounts
WHERE id = ?`
	item, err := scanAccount(r.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return item, err
}

func (r *SQLiteRepository) UpdateStatus(
	ctx context.Context,
	id int64,
	status string,
	now time.Time,
) (Account, error) {
	const query = `
UPDATE accounts
SET status = ?, updated_at = ?
WHERE id = ? AND purge_state = 'active'`
	result, err := r.db.ExecContext(ctx, query, status, now.Format(time.RFC3339Nano), id)
	if err != nil {
		return Account{}, fmt.Errorf("update account %d status: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Account{}, fmt.Errorf("check updated account %d: %w", id, err)
	}
	if affected == 0 {
		return Account{}, ErrNotFound
	}
	item, err := r.Get(ctx, id)
	if err != nil {
		return Account{}, fmt.Errorf("read account %d after status update: %w", id, err)
	}
	return item, nil
}

func (r *SQLiteRepository) RecordVerification(
	ctx context.Context,
	id int64,
	status string,
	reason string,
	now time.Time,
) (Account, error) {
	const query = `
UPDATE accounts
SET status = ?, status_reason = ?, last_verified_at = ?, updated_at = ?
WHERE id = ? AND purge_state = 'active'`
	timestamp := now.Format(time.RFC3339Nano)
	result, err := r.db.ExecContext(ctx, query, status, reason, timestamp, timestamp, id)
	if err != nil {
		return Account{}, fmt.Errorf("record account %d verification: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Account{}, fmt.Errorf("check verified account %d: %w", id, err)
	}
	if affected == 0 {
		return Account{}, ErrNotFound
	}
	item, err := r.Get(ctx, id)
	if err != nil {
		return Account{}, fmt.Errorf("read account %d after verification: %w", id, err)
	}
	return item, nil
}

func (r *SQLiteRepository) Update(
	ctx context.Context,
	id int64,
	input UpdateInput,
	now time.Time,
) (Account, error) {
	const query = `
UPDATE accounts
SET phone_number = ?, username = ?, display_name = ?, updated_at = ?
WHERE id = ? AND purge_state = 'active'`
	result, err := r.db.ExecContext(
		ctx,
		query,
		input.PhoneNumber,
		input.Username,
		input.DisplayName,
		now.Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return Account{}, fmt.Errorf("update account %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Account{}, fmt.Errorf("check updated account %d: %w", id, err)
	}
	if affected == 0 {
		return Account{}, ErrNotFound
	}
	item, err := r.Get(ctx, id)
	if err != nil {
		return Account{}, fmt.Errorf("read account %d after update: %w", id, err)
	}
	return item, nil
}

func (r *SQLiteRepository) ScheduleDeletion(
	ctx context.Context,
	id int64,
	now time.Time,
	effectiveAt time.Time,
) (Account, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("begin account deletion scheduling: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	item, err := getAccountIncludingDeletedTx(ctx, tx, id)
	if err != nil {
		return Account{}, err
	}
	if item.PurgeState == PurgeStatePurging {
		return Account{}, ErrDeletionInProgress
	}
	if item.PurgeState != PurgeStateActive {
		return item, nil
	}
	timestamp := now.UTC().Format(time.RFC3339Nano)
	effectiveTimestamp := effectiveAt.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE accounts
SET purge_state = 'scheduled', purge_error = '',
	deletion_requested_at = ?, deletion_effective_at = ?, updated_at = ?
WHERE id = ? AND purge_state = 'active'`,
		timestamp,
		effectiveTimestamp,
		timestamp,
		id,
	)
	if err != nil {
		return Account{}, fmt.Errorf("schedule account %d deletion: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Account{}, fmt.Errorf("check scheduled account %d deletion: %w", id, err)
	}
	if affected != 1 {
		return Account{}, ErrDeletionInProgress
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("commit account deletion scheduling: %w", err)
	}
	item.PurgeState = PurgeStateScheduled
	item.PurgeError = ""
	item.DeletionRequestedAt = &now
	item.DeletionEffectiveAt = &effectiveAt
	item.UpdatedAt = now
	return item, nil
}

func (r *SQLiteRepository) Restore(
	ctx context.Context,
	id int64,
	now time.Time,
) (Account, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("begin account restoration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	item, err := getAccountIncludingDeletedTx(ctx, tx, id)
	if err != nil {
		return Account{}, err
	}
	if item.PurgeState == PurgeStatePurging {
		return Account{}, ErrDeletionInProgress
	}
	if item.PurgeState == PurgeStateActive {
		return item, nil
	}
	if item.PurgeState != PurgeStateScheduled {
		return Account{}, ErrDeletionInProgress
	}
	timestamp := now.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
UPDATE accounts
SET purge_state = 'active', purge_error = '',
	deletion_requested_at = '', deletion_effective_at = '', updated_at = ?
WHERE id = ? AND purge_state = 'scheduled'`, timestamp, id); err != nil {
		return Account{}, fmt.Errorf("restore account %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("commit account restoration: %w", err)
	}
	item.PurgeState = PurgeStateActive
	item.PurgeError = ""
	item.DeletionRequestedAt = nil
	item.DeletionEffectiveAt = nil
	item.UpdatedAt = now
	return item, nil
}

func (r *SQLiteRepository) ListDueForPurge(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]Account, error) {
	claimedBefore := now.UTC().Add(-PurgeClaimTimeout).Format(time.RFC3339Nano)
	rows, err := r.db.QueryContext(ctx, `
SELECT id, phone_number, username, display_name, status, status_reason,
	last_verified_at, created_at, updated_at, purge_state, purge_error,
	deletion_requested_at, deletion_effective_at
FROM accounts
WHERE deletion_effective_at != ''
	AND deletion_effective_at <= ?
	AND (
		purge_state IN ('scheduled', 'failed')
		OR (purge_state = 'purging' AND updated_at <= ?)
	)
ORDER BY deletion_effective_at, id
LIMIT ?`,
		now.UTC().Format(time.RFC3339Nano),
		claimedBefore,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list due account purges: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]Account, 0, limit)
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due account purges: %w", err)
	}
	return items, nil
}

func (r *SQLiteRepository) ClaimPurge(
	ctx context.Context,
	id int64,
	now time.Time,
) (Account, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("begin account purge claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	item, err := getAccountIncludingDeletedTx(ctx, tx, id)
	if err != nil {
		return Account{}, err
	}
	staleBefore := now.UTC().Add(-PurgeClaimTimeout)
	if item.PurgeState == PurgeStatePurging && item.UpdatedAt.After(staleBefore) {
		return Account{}, ErrDeletionInProgress
	}
	if item.PurgeState != PurgeStateScheduled &&
		item.PurgeState != PurgeStateFailed &&
		item.PurgeState != PurgeStatePurging {
		return Account{}, ErrNotFound
	}
	if item.DeletionEffectiveAt == nil || item.DeletionEffectiveAt.After(now.UTC()) {
		return Account{}, ErrNotFound
	}
	timestamp := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `
UPDATE accounts
SET purge_state = 'purging', purge_error = '', updated_at = ?
WHERE id = ?
	AND (
		purge_state IN ('scheduled', 'failed')
		OR (purge_state = 'purging' AND updated_at <= ?)
	)`,
		timestamp,
		id,
		staleBefore.Format(time.RFC3339Nano),
	)
	if err != nil {
		return Account{}, fmt.Errorf("claim account %d purge: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Account{}, fmt.Errorf("check claimed account %d purge: %w", id, err)
	}
	if affected != 1 {
		return Account{}, ErrDeletionInProgress
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("commit account purge claim: %w", err)
	}
	item.PurgeState = PurgeStatePurging
	item.PurgeError = ""
	item.UpdatedAt = now
	return item, nil
}

func (r *SQLiteRepository) FailPurge(
	ctx context.Context,
	id int64,
	reason string,
	now time.Time,
) error {
	result, err := r.db.ExecContext(ctx, `
UPDATE accounts
SET purge_state = 'failed', purge_error = ?, updated_at = ?
WHERE id = ? AND purge_state = 'purging'`,
		reason,
		now.UTC().Format(time.RFC3339Nano),
		id,
	)
	if err != nil {
		return fmt.Errorf("mark account %d purge failed: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check failed account %d purge: %w", id, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLiteRepository) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(
		ctx,
		`DELETE FROM accounts WHERE id = ? AND purge_state = 'purging'`,
		id,
	)
	if err != nil {
		return fmt.Errorf("delete account %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted account %d: %w", id, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func getAccountIncludingDeletedTx(
	ctx context.Context,
	tx *sql.Tx,
	id int64,
) (Account, error) {
	const query = `
SELECT id, phone_number, username, display_name, status, status_reason,
	last_verified_at, created_at, updated_at, purge_state, purge_error,
	deletion_requested_at, deletion_effective_at
FROM accounts
WHERE id = ?`
	item, err := scanAccount(tx.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return item, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(row rowScanner) (Account, error) {
	var item Account
	var createdAt string
	var updatedAt string
	var lastVerifiedAt string
	var deletionRequestedAt string
	var deletionEffectiveAt string
	if err := row.Scan(
		&item.ID,
		&item.PhoneNumber,
		&item.Username,
		&item.DisplayName,
		&item.Status,
		&item.StatusReason,
		&lastVerifiedAt,
		&createdAt,
		&updatedAt,
		&item.PurgeState,
		&item.PurgeError,
		&deletionRequestedAt,
		&deletionEffectiveAt,
	); err != nil {
		return Account{}, err
	}

	var err error
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return Account{}, fmt.Errorf("parse created_at for account %d: %w", item.ID, err)
	}
	item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return Account{}, fmt.Errorf("parse updated_at for account %d: %w", item.ID, err)
	}
	if lastVerifiedAt != "" {
		verifiedAt, parseErr := time.Parse(time.RFC3339Nano, lastVerifiedAt)
		if parseErr != nil {
			return Account{}, fmt.Errorf(
				"parse last_verified_at for account %d: %w",
				item.ID,
				parseErr,
			)
		}
		item.LastVerifiedAt = &verifiedAt
	}
	if deletionRequestedAt != "" {
		value, parseErr := time.Parse(time.RFC3339Nano, deletionRequestedAt)
		if parseErr != nil {
			return Account{}, fmt.Errorf(
				"parse deletion_requested_at for account %d: %w",
				item.ID,
				parseErr,
			)
		}
		item.DeletionRequestedAt = &value
	}
	if deletionEffectiveAt != "" {
		value, parseErr := time.Parse(time.RFC3339Nano, deletionEffectiveAt)
		if parseErr != nil {
			return Account{}, fmt.Errorf(
				"parse deletion_effective_at for account %d: %w",
				item.ID,
				parseErr,
			)
		}
		item.DeletionEffectiveAt = &value
	}
	return item, nil
}
