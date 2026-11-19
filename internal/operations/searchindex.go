package operations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	SearchIndexHealthy  = "healthy"
	SearchIndexDegraded = "degraded"
)

type SearchIndexHealth struct {
	Status                       string     `json:"status"`
	ProjectionCount              int        `json:"projection_count"`
	SearchableCount              int        `json:"searchable_count"`
	DocumentCount                int        `json:"document_count"`
	MissingCount                 int        `json:"missing_count"`
	OrphanCount                  int        `json:"orphan_count"`
	DeletedIndexedCount          int        `json:"deleted_indexed_count"`
	IntegrityOK                  bool       `json:"integrity_ok"`
	NeedsRebuild                 bool       `json:"needs_rebuild"`
	IntegrityError               string     `json:"integrity_error,omitempty"`
	LastRebuildError             string     `json:"last_rebuild_error,omitempty"`
	CheckedAt                    time.Time  `json:"checked_at"`
	LastRebuiltAt                *time.Time `json:"last_rebuilt_at,omitempty"`
	RebuildCount                 int        `json:"rebuild_count"`
	RebuildDoesNotChangeEvidence bool       `json:"rebuild_does_not_change_evidence"`
}

func (s *Service) SearchIndexHealth(ctx context.Context) (SearchIndexHealth, error) {
	s.searchIndexMu.Lock()
	defer s.searchIndexMu.Unlock()
	return s.readSearchIndexHealth(ctx)
}

func (s *Service) RebuildSearchIndex(ctx context.Context) (SearchIndexHealth, error) {
	s.searchIndexMu.Lock()
	defer s.searchIndexMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SearchIndexHealth{}, fmt.Errorf("begin search index rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO indexed_messages_fts(indexed_messages_fts) VALUES('rebuild')`,
	); err != nil {
		s.recordSearchIndexRebuildFailure(err)
		return SearchIndexHealth{}, fmt.Errorf("rebuild message search index: %w", err)
	}
	if err := checkSearchIndexIntegrity(ctx, tx); err != nil {
		s.recordSearchIndexRebuildFailure(err)
		return SearchIndexHealth{}, fmt.Errorf("verify rebuilt message search index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		s.recordSearchIndexRebuildFailure(err)
		return SearchIndexHealth{}, fmt.Errorf("commit search index rebuild: %w", err)
	}
	s.searchIndexLastRebuild = s.now().UTC()
	s.searchIndexRebuildCount++
	s.searchIndexLastRebuildErr = ""
	return s.readSearchIndexHealth(ctx)
}

func (s *Service) readSearchIndexHealth(ctx context.Context) (SearchIndexHealth, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SearchIndexHealth{}, fmt.Errorf("begin search index health check: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	count := func(query string) (int, error) {
		var value int
		if err := tx.QueryRowContext(ctx, query).Scan(&value); err != nil {
			return 0, err
		}
		return value, nil
	}
	projectionCount, err := count(`SELECT COUNT(1) FROM indexed_messages`)
	if err != nil {
		return SearchIndexHealth{}, fmt.Errorf("count message projections: %w", err)
	}
	searchableCount, err := count(`
SELECT COUNT(1) FROM indexed_messages WHERE deleted_at = ''`)
	if err != nil {
		return SearchIndexHealth{}, fmt.Errorf("count searchable message projections: %w", err)
	}
	documentCount, err := count(`
SELECT COUNT(1) FROM indexed_messages_fts_docsize`)
	if err != nil {
		return SearchIndexHealth{}, fmt.Errorf("count full-text documents: %w", err)
	}
	missingCount, err := count(`
SELECT COUNT(1)
FROM indexed_messages AS m
LEFT JOIN indexed_messages_fts_docsize AS f ON f.id = m.rowid
WHERE m.deleted_at = '' AND f.id IS NULL`)
	if err != nil {
		return SearchIndexHealth{}, fmt.Errorf("count missing full-text documents: %w", err)
	}
	orphanCount, err := count(`
SELECT COUNT(1)
FROM indexed_messages_fts_docsize AS f
LEFT JOIN indexed_messages AS m ON m.rowid = f.id
WHERE m.rowid IS NULL`)
	if err != nil {
		return SearchIndexHealth{}, fmt.Errorf("count orphaned full-text documents: %w", err)
	}
	deletedIndexedCount, err := count(`
SELECT COUNT(1)
FROM indexed_messages AS m
JOIN indexed_messages_fts_docsize AS f ON f.id = m.rowid
WHERE m.deleted_at <> ''`)
	if err != nil {
		return SearchIndexHealth{}, fmt.Errorf("count deleted full-text documents: %w", err)
	}

	integrityErr := checkSearchIndexIntegrity(ctx, tx)
	health := SearchIndexHealth{
		ProjectionCount:              projectionCount,
		SearchableCount:              searchableCount,
		DocumentCount:                documentCount,
		MissingCount:                 missingCount,
		OrphanCount:                  orphanCount,
		DeletedIndexedCount:          deletedIndexedCount,
		IntegrityOK:                  integrityErr == nil,
		NeedsRebuild:                 missingCount > 0 || orphanCount > 0 || integrityErr != nil,
		CheckedAt:                    s.now().UTC(),
		RebuildCount:                 s.searchIndexRebuildCount,
		LastRebuildError:             s.searchIndexLastRebuildErr,
		RebuildDoesNotChangeEvidence: true,
	}
	if integrityErr != nil {
		health.IntegrityError = truncate(strings.TrimSpace(integrityErr.Error()), 240)
	}
	if !s.searchIndexLastRebuild.IsZero() {
		lastRebuiltAt := s.searchIndexLastRebuild
		health.LastRebuiltAt = &lastRebuiltAt
	}
	if health.NeedsRebuild {
		health.Status = SearchIndexDegraded
	} else {
		health.Status = SearchIndexHealthy
	}
	return health, nil
}

func checkSearchIndexIntegrity(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO indexed_messages_fts(indexed_messages_fts, rank)
VALUES('integrity-check', 1)`,
	)
	return err
}

func (s *Service) recordSearchIndexRebuildFailure(err error) {
	s.searchIndexLastRebuildErr = truncate(strings.TrimSpace(err.Error()), 240)
}
