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

func (r *SQLiteRepository) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS accounts (
	id INTEGER PRIMARY KEY,
	phone_number TEXT NOT NULL,
	username TEXT NOT NULL DEFAULT '',
	display_name TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL CHECK (status IN ('pending', 'online', 'paused', 'error')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS accounts_status_created_idx
	ON accounts(status, created_at DESC, id DESC);`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate account database: %w", err)
	}
	return nil
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
INSERT INTO accounts (id, phone_number, username, display_name, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	phone_number = excluded.phone_number,
	username = excluded.username,
	display_name = excluded.display_name,
	status = excluded.status,
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
SELECT id, phone_number, username, display_name, status, created_at, updated_at
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
SELECT id, phone_number, username, display_name, status, created_at, updated_at
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

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAccount(row rowScanner) (Account, error) {
	var item Account
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&item.ID,
		&item.PhoneNumber,
		&item.Username,
		&item.DisplayName,
		&item.Status,
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
	return item, nil
}
