// Package accountpurge coordinates the recoverable account deletion lifecycle.
package accountpurge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
)

const operationTimeout = 2 * time.Minute

type AccountLifecycle interface {
	ListDueForPurge(ctx context.Context, limit int) ([]account.Account, error)
	ClaimPurge(ctx context.Context, id int64) (account.Account, error)
	FailPurge(ctx context.Context, id int64, reason string) error
	FinalizePurge(ctx context.Context, id int64) error
}

type SessionCleaner interface {
	DeleteSession(accountID int64) error
}

type EvidenceStore interface {
	CheckAccountPurge(ctx context.Context, accountID int64) error
	PurgeAccount(ctx context.Context, accountID int64) error
}

type Service struct {
	accounts  AccountLifecycle
	sessions  SessionCleaner
	evidence  EvidenceStore
	batchSize int
}

func New(
	accounts AccountLifecycle,
	sessions SessionCleaner,
	evidence EvidenceStore,
) (*Service, error) {
	if accounts == nil {
		return nil, errors.New("account lifecycle is required")
	}
	if evidence == nil {
		return nil, errors.New("evidence store is required")
	}
	return &Service{
		accounts:  accounts,
		sessions:  sessions,
		evidence:  evidence,
		batchSize: 50,
	}, nil
}

func (s *Service) RunOnce(ctx context.Context) error {
	items, err := s.accounts.ListDueForPurge(ctx, s.batchSize)
	if err != nil {
		return fmt.Errorf("list due account purges: %w", err)
	}
	var failures []error
	for _, due := range items {
		if err := s.purge(ctx, due.ID); err != nil {
			failures = append(failures, fmt.Errorf("purge account %d: %w", due.ID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) purge(ctx context.Context, accountID int64) error {
	if err := s.evidence.CheckAccountPurge(ctx, accountID); err != nil {
		if errors.Is(err, operations.ErrActiveLegalHold) {
			return nil
		}
		return err
	}
	claimed, err := s.accounts.ClaimPurge(ctx, accountID)
	if errors.Is(err, account.ErrDeletionInProgress) ||
		errors.Is(err, account.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("claim account: %w", err)
	}

	operationCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), operationTimeout)
	defer cancel()
	if s.sessions != nil {
		if err := s.sessions.DeleteSession(accountID); err != nil {
			return s.fail(
				operationCtx,
				accountID,
				"授权会话清理失败，后台将重试",
				fmt.Errorf("delete session: %w", err),
			)
		}
	}
	if err := s.evidence.PurgeAccount(operationCtx, accountID); err != nil {
		return s.fail(
			operationCtx,
			accountID,
			"证据与索引清理失败，后台将重试",
			fmt.Errorf("purge evidence: %w", err),
		)
	}
	if err := s.accounts.FinalizePurge(operationCtx, accountID); err != nil &&
		!errors.Is(err, account.ErrNotFound) {
		return s.fail(
			operationCtx,
			accountID,
			"账号记录销毁失败，后台将重试",
			fmt.Errorf("finalize account deletion: %w", err),
		)
	}
	if claimed.PurgeState != account.PurgeStatePurging {
		return fmt.Errorf("account entered unexpected purge state %q", claimed.PurgeState)
	}
	return nil
}

func (s *Service) fail(
	ctx context.Context,
	accountID int64,
	reason string,
	cause error,
) error {
	if err := s.accounts.FailPurge(ctx, accountID, reason); err != nil {
		return errors.Join(cause, fmt.Errorf("record purge failure: %w", err))
	}
	return cause
}
