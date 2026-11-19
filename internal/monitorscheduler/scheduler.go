// Package monitorscheduler polls enabled Telegram monitor targets.
package monitorscheduler

import (
	"context"
	"encoding/base64"
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

type accountTargetGroup struct {
	accountID        int64
	targets          []operations.CollectionTarget
	next             int
	nextCollectionAt time.Time
	blocked          bool
	active           bool
	checked          bool
}

type Scheduler struct {
	operations *operations.Service
	accounts   AccountLookup
	chats      ChatReader
	interval   time.Duration
	logger     *slog.Logger
	now        func() time.Time
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
		now:        time.Now,
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

	started := s.now().UTC()
	s.setRunning(true, started)
	targets, err := s.operations.ListCollectionTargets(ctx)
	if err != nil {
		s.finish(started, err)
		return err
	}
	groups := groupTargetsByAccount(targets)
	var runErr error
	for {
		processed := false
		for index := range groups {
			group := &groups[index]
			if group.next >= len(group.targets) ||
				group.blocked ||
				(!group.nextCollectionAt.IsZero() &&
					s.now().Before(group.nextCollectionAt)) {
				continue
			}
			if err := ctx.Err(); err != nil {
				runErr = errors.Join(runErr, err)
				s.finish(started, runErr)
				return runErr
			}
			target := group.targets[group.next]
			group.next++
			processed = true
			if !group.checked {
				group.checked = true
				item, err := s.accounts.Get(ctx, group.accountID)
				if errors.Is(err, account.ErrNotFound) {
					group.blocked = true
					continue
				}
				if err != nil {
					group.blocked = true
					runErr = errors.Join(runErr, err)
					continue
				}
				group.active = item.Status == account.StatusOnline
			}
			if !group.active {
				group.blocked = true
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
				runErr = errors.Join(runErr, err)
				if errors.Is(err, telegramchat.ErrFloodWait) {
					wait := s.interval
					var floodWait *telegramchat.FloodWaitError
					if errors.As(err, &floodWait) && floodWait.Wait > 0 {
						wait = floodWait.Wait
					}
					if wait <= 0 {
						wait = time.Minute
					}
					group.nextCollectionAt = s.now().UTC().Add(wait)
					if deferErr := s.operations.DeferAccountCollection(
						context.WithoutCancel(ctx),
						group.accountID,
						group.nextCollectionAt,
						err.Error(),
					); deferErr != nil {
						s.logger.Warn("defer account collection",
							"account_id", group.accountID,
							"error", deferErr)
					}
					group.blocked = true
				}
				continue
			}
			if err := s.ingestCollection(ctx, target, collection); err != nil {
				runErr = errors.Join(runErr, err)
				continue
			}
			if err := s.operations.ClearAccountCollectionDeferral(
				context.WithoutCancel(ctx),
				group.accountID,
			); err != nil {
				runErr = errors.Join(runErr, err)
			}
			group.nextCollectionAt = time.Time{}
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
		if !processed {
			break
		}
	}
	s.finish(started, runErr)
	return runErr
}

func groupTargetsByAccount(
	targets []operations.CollectionTarget,
) []accountTargetGroup {
	groups := make([]accountTargetGroup, 0)
	indexByAccount := make(map[int64]int)
	for _, target := range targets {
		index, found := indexByAccount[target.AccountID]
		if !found {
			index = len(groups)
			indexByAccount[target.AccountID] = index
			groups = append(groups, accountTargetGroup{accountID: target.AccountID})
		}
		groups[index].targets = append(groups[index].targets, target)
		if target.NextCollectionAt != nil &&
			target.NextCollectionAt.After(groups[index].nextCollectionAt) {
			groups[index].nextCollectionAt = target.NextCollectionAt.UTC()
		}
	}
	return groups
}

func (s *Scheduler) ingestCollection(
	ctx context.Context,
	target operations.CollectionTarget,
	collection telegramchat.MessageCollection,
) error {
	messages := make([]operations.MessageRecord, 0, len(collection.Items))
	for _, message := range collection.Items {
		media := operations.MediaRecord{}
		if message.Media != nil {
			media = operations.MediaRecord{
				Type:          message.Media.Type,
				Kind:          message.Media.Kind,
				ID:            message.Media.ID,
				DCID:          message.Media.DCID,
				MimeType:      message.Media.MimeType,
				FileName:      message.Media.FileName,
				Size:          message.Media.Size,
				Width:         message.Media.Width,
				Height:        message.Media.Height,
				Spoiler:       message.Media.Spoiler,
				Video:         message.Media.Video,
				Voice:         message.Media.Voice,
				Round:         message.Media.Round,
				AccessHash:    message.Media.AccessHash,
				FileReference: base64.StdEncoding.EncodeToString(message.Media.FileReference),
			}
		}
		messages = append(messages, operations.MessageRecord{
			AccountID: target.AccountID,
			PeerType:  target.PeerType,
			PeerID:    target.PeerID,
			PeerTitle: target.PeerTitle,
			MessageID: message.ID,
			Sender:    message.Sender,
			Text:      message.Text,
			MediaType: message.MediaType,
			Media:     media,
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
		return err
	}
	return nil
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
