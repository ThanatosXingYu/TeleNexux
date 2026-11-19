// Package accounthealth revalidates persisted Telegram account status.
package accounthealth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

const (
	accountPageSize   = 100
	validationWorkers = 2
)

type AccountStore interface {
	List(ctx context.Context, filter account.Filter) ([]account.Account, error)
	RecordVerification(
		ctx context.Context,
		id int64,
		status string,
		reason string,
	) (account.Account, error)
}

type SessionValidator interface {
	ValidateSession(ctx context.Context, accountID int64) error
}

type Summary struct {
	Checked  int `json:"checked"`
	Online   int `json:"online"`
	Offline  int `json:"offline"`
	Deferred int `json:"deferred"`
	Failed   int `json:"failed"`
	Duration time.Duration
}

type Checker struct {
	accounts AccountStore
	sessions SessionValidator
}

func New(accounts AccountStore, sessions SessionValidator) *Checker {
	return &Checker{accounts: accounts, sessions: sessions}
}

func (c *Checker) RunOnce(ctx context.Context) (Summary, error) {
	if c == nil || c.accounts == nil || c.sessions == nil {
		return Summary{}, errors.New("account health checker is not configured")
	}
	started := time.Now()
	items, err := c.listCandidates(ctx)
	if err != nil {
		return Summary{}, err
	}
	summary := Summary{Checked: len(items)}
	if len(items) == 0 {
		summary.Duration = time.Since(started)
		return summary, nil
	}

	results := make(chan accountResult, len(items))
	jobs := make(chan account.Account)
	var workers sync.WaitGroup
	for range min(validationWorkers, len(items)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for item := range jobs {
				results <- c.validate(ctx, item)
			}
		}()
	}
	for _, item := range items {
		jobs <- item
	}
	close(jobs)
	workers.Wait()
	close(results)

	var runErr error
	for result := range results {
		switch result.kind {
		case resultOnline:
			summary.Online++
		case resultOffline:
			summary.Offline++
		case resultDeferred:
			summary.Deferred++
		case resultFailed:
			summary.Failed++
		}
		if result.err != nil {
			runErr = errors.Join(runErr, result.err)
		}
	}
	summary.Duration = time.Since(started)
	return summary, runErr
}

type resultKind int

const (
	resultOnline resultKind = iota
	resultOffline
	resultDeferred
	resultFailed
)

type accountResult struct {
	kind resultKind
	err  error
}

func (c *Checker) validate(ctx context.Context, item account.Account) accountResult {
	if err := ctx.Err(); err != nil {
		return accountResult{kind: resultFailed, err: err}
	}
	validateCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	err := c.sessions.ValidateSession(validateCtx, item.ID)
	cancel()
	if err == nil {
		if err := c.record(ctx, item.ID, account.StatusOnline, ""); err != nil {
			return accountResult{kind: resultFailed, err: err}
		}
		return accountResult{kind: resultOnline}
	}
	if !errors.Is(err, telegramchat.ErrSessionMissing) &&
		!errors.Is(err, telegramchat.ErrSessionInvalid) {
		return accountResult{kind: resultDeferred}
	}
	reason := "授权会话不存在，请重新登录"
	if errors.Is(err, telegramchat.ErrSessionInvalid) {
		reason = "Telegram 授权已失效，请重新登录"
	}
	if err := c.record(ctx, item.ID, account.StatusError, reason); err != nil {
		return accountResult{kind: resultFailed, err: err}
	}
	return accountResult{kind: resultOffline}
}

func (c *Checker) record(
	ctx context.Context,
	accountID int64,
	status string,
	reason string,
) error {
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := c.accounts.RecordVerification(updateCtx, accountID, status, reason); err != nil {
		return fmt.Errorf("persist account %d status: %w", accountID, err)
	}
	return nil
}

func (c *Checker) listCandidates(ctx context.Context) ([]account.Account, error) {
	var items []account.Account
	for _, status := range []string{account.StatusOnline, account.StatusError} {
		for offset := 0; ; offset += accountPageSize {
			page, err := c.accounts.List(ctx, account.Filter{
				Status: status,
				Limit:  accountPageSize,
				Offset: offset,
			})
			if err != nil {
				return nil, fmt.Errorf("list %s accounts for validation: %w", status, err)
			}
			items = append(items, page...)
			if len(page) < accountPageSize {
				break
			}
		}
	}
	return items, nil
}
