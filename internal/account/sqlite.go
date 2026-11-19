package account

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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
INSERT INTO accounts (id, phone_number, username, display_name, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`
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
	updated_at = excluded.updated_at`
	timestamp := now.Format(time.RFC3339Nano)
	if _, err := r.db.ExecContext(
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
	); err != nil {
		return Account{}, fmt.Errorf("upsert account %d: %w", input.ID, err)
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
	last_verified_at, created_at, updated_at
FROM accounts`
	args := make([]any, 0, 3)
	if filter.Status != "" {
		query += " WHERE status = ?"
		args = append(args, filter.Status)
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
	last_verified_at, created_at, updated_at
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
WHERE id = ?`
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
WHERE id = ?`
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
WHERE id = ?`
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

func (r *SQLiteRepository) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
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

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(row rowScanner) (Account, error) {
	var item Account
	var createdAt string
	var updatedAt string
	var lastVerifiedAt string
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
	return item, nil
}
