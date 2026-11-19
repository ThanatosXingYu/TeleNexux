// Package monitorscheduler polls enabled Telegram monitor targets.
package monitorscheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

type AccountLookup interface {
	Get(ctx context.Context, id int64) (account.Account, error)
}

type ChatReader interface {
	CollectMessages(
		ctx context.Context,
		accountID int64,
		peerType string,
		peerID int64,
		checkpointID int,
		pageSize int,
		maxPages int,
	) (telegramchat.MessageCollection, error)
}

const (
	collectionPageSize = 50
	collectionMaxPages = 20
)

type Status struct {
	Running      bool       `json:"running"`
	LastStarted  *time.Time `json:"last_started_at,omitempty"`
	LastFinished *time.Time `json:"last_finished_at,omitempty"`
	NextRunAt    *time.Time `json:"next_run_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

type Scheduler struct {
	operations *operations.Service
	accounts   AccountLookup
	chats      ChatReader
	interval   time.Duration
	logger     *slog.Logger
	mu         sync.Mutex
	runMu      sync.Mutex
	status     Status
}

func New(
	service *operations.Service,
	accounts AccountLookup,
	chats ChatReader,
	interval time.Duration,
	logger *slog.Logger,
) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	return &Scheduler{
		operations: service,
		accounts:   accounts,
		chats:      chats,
		interval:   interval,
		logger:     logger,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.operations == nil || s.accounts == nil || s.chats == nil {
		return
	}
	go func() {
		nextRun := time.Now().UTC().Add(15 * time.Second)
		s.setNextRun(nextRun)
		timer := time.NewTimer(time.Until(nextRun))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if err := s.RunOnce(ctx); err != nil {
					s.logger.Warn("monitor scheduler run failed", "error", err)
				}
				nextRun = time.Now().UTC().Add(s.interval)
				s.setNextRun(nextRun)
				timer.Reset(time.Until(nextRun))
			}
		}
	}()
}

func (s *Scheduler) RunOnce(ctx context.Context) error {
	if s == nil || s.operations == nil || s.accounts == nil || s.chats == nil {
		return errors.New("monitor scheduler is not configured")
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()

	started := time.Now().UTC()
	s.setRunning(true, started)
	targets, err := s.operations.ListCollectionTargets(ctx)
	if err != nil {
		s.finish(started, err)
		return err
	}
	var runErr error
	activeAccounts := make(map[int64]bool)
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		active, checked := activeAccounts[target.AccountID]
		if !checked {
			item, err := s.accounts.Get(ctx, target.AccountID)
			if errors.Is(err, account.ErrNotFound) {
				activeAccounts[target.AccountID] = false
				continue
			}
			if err != nil {
				runErr = err
				continue
			}
			active = item.Status == account.StatusOnline
			activeAccounts[target.AccountID] = active
		}
		if !active {
			continue
		}
		collection, err := s.chats.CollectMessages(
			ctx,
			target.AccountID,
			target.PeerType,
			target.PeerID,
			target.LastMessageID,
			collectionPageSize,
			collectionMaxPages,
		)
		if err != nil {
			if recordErr := s.operations.RecordBatchFailure(ctx, operations.IngestBatchInput{
				Source:           "scheduler",
				AccountID:        target.AccountID,
				PeerType:         target.PeerType,
				PeerID:           target.PeerID,
				PeerTitle:        target.PeerTitle,
				CheckpointBefore: target.LastMessageID,
				BacklogDetected:  target.BacklogDetected || collection.Backlog,
			}, err); recordErr != nil {
				s.logger.Warn("record monitor batch failure",
					"account_id", target.AccountID,
					"peer_type", target.PeerType,
					"peer_id", target.PeerID,
					"error", recordErr)
			}
			runErr = err
			if errors.Is(err, telegramchat.ErrFloodWait) {
				break
			}
			continue
		}

		messages := make([]operations.MessageRecord, 0, len(collection.Items))
		for _, message := range collection.Items {
			messages = append(messages, operations.MessageRecord{
				AccountID: target.AccountID,
				PeerType:  target.PeerType,
				PeerID:    target.PeerID,
				PeerTitle: target.PeerTitle,
				MessageID: message.ID,
				Sender:    message.Sender,
				Text:      message.Text,
				MediaType: message.MediaType,
				SentAt:    message.SentAt,
			})
		}
		if _, err := s.operations.IngestBatch(ctx, operations.IngestBatchInput{
			Source:           "scheduler",
			AccountID:        target.AccountID,
			PeerType:         target.PeerType,
			PeerID:           target.PeerID,
			PeerTitle:        target.PeerTitle,
			CheckpointBefore: target.LastMessageID,
			CheckpointAfter:  collection.NewestID,
			Complete:         collection.Complete,
			BacklogDetected:  collection.Backlog,
			Messages:         messages,
		}); err != nil {
			runErr = err
			continue
		}
		if collection.Backlog {
			s.logger.Warn(
				"monitor collection remains backlogged",
				"account_id", target.AccountID,
				"peer_type", target.PeerType,
				"peer_id", target.PeerID,
				"checkpoint", target.LastMessageID,
				"pages_read", collection.PagesRead,
			)
		}
	}
	s.finish(started, runErr)
	return runErr
}

func (s *Scheduler) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Scheduler) setRunning(running bool, started time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Running = running
	if running {
		s.status.LastStarted = &started
	}
}

func (s *Scheduler) setNextRun(nextRun time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.NextRunAt = &nextRun
}

func (s *Scheduler) finish(started time.Time, runErr error) {
	finished := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Running = false
	s.status.LastStarted = &started
	s.status.LastFinished = &finished
	s.status.LastError = ""
	if runErr != nil {
		s.status.LastError = runErr.Error()
	}
}
