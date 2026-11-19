// Package monitorscheduler polls enabled Telegram monitor targets.
package monitorscheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

type ChatReader interface {
	Messages(
		ctx context.Context,
		accountID int64,
		peerType string,
		peerID int64,
		beforeID int,
		limit int,
	) (telegramchat.MessagePage, error)
}

type Status struct {
	Running      bool       `json:"running"`
	LastStarted  *time.Time `json:"last_started_at,omitempty"`
	LastFinished *time.Time `json:"last_finished_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
}

type Scheduler struct {
	operations *operations.Service
	chats      ChatReader
	interval   time.Duration
	logger     *slog.Logger
	mu         sync.Mutex
	runMu      sync.Mutex
	status     Status
}

func New(
	service *operations.Service,
	chats ChatReader,
	interval time.Duration,
	logger *slog.Logger,
) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	return &Scheduler{
		operations: service,
		chats:      chats,
		interval:   interval,
		logger:     logger,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.operations == nil || s.chats == nil {
		return
	}
	go func() {
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if err := s.RunOnce(ctx); err != nil {
					s.logger.Warn("monitor scheduler run failed", "error", err)
				}
				timer.Reset(s.interval)
			}
		}
	}()
}

func (s *Scheduler) RunOnce(ctx context.Context) error {
	if s == nil || s.operations == nil || s.chats == nil {
		return errors.New("monitor scheduler is not configured")
	}
	s.runMu.Lock()
	defer s.runMu.Unlock()

	started := time.Now().UTC()
	s.setRunning(true, started)
	rules, err := s.operations.ListRules(ctx, true)
	if err != nil {
		s.finish(started, err)
		return err
	}
	var runErr error
	for _, rule := range rules {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		page, err := s.chats.Messages(
			ctx,
			rule.AccountID,
			rule.PeerType,
			rule.PeerID,
			0,
			50,
		)
		if err != nil {
			if markErr := s.operations.MarkRuleRun(ctx, rule.ID, rule.LastMessageID, err); markErr != nil {
				s.logger.Warn("record monitor rule failure",
					"rule_id", rule.ID, "error", markErr)
			}
			runErr = err
			if errors.Is(err, telegramchat.ErrFloodWait) {
				break
			}
			continue
		}

		maxID := rule.LastMessageID
		messages := make([]operations.MessageRecord, 0, len(page.Items))
		for _, message := range page.Items {
			if message.ID > maxID {
				maxID = message.ID
			}
			if rule.LastMessageID > 0 && message.ID > rule.LastMessageID {
				messages = append(messages, operations.MessageRecord{
					AccountID: rule.AccountID,
					PeerType:  rule.PeerType,
					PeerID:    rule.PeerID,
					PeerTitle: rule.PeerTitle,
					MessageID: message.ID,
					Sender:    message.Sender,
					Text:      message.Text,
					SentAt:    message.SentAt,
				})
			}
		}
		if len(messages) > 0 {
			if _, _, err := s.operations.IngestMessages(ctx, messages); err != nil {
				runErr = err
			}
		}
		if err := s.operations.MarkRuleRun(ctx, rule.ID, maxID, nil); err != nil {
			runErr = err
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
