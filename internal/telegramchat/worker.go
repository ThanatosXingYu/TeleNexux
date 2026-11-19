package telegramchat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/telegramauth"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// ListenerStatus reports the lifecycle of one account's Telegram connection.
type ListenerStatus struct {
	AccountID          int64      `json:"account_id"`
	Connected          bool       `json:"connected"`
	Connections        int        `json:"connections"`
	Disconnections     int        `json:"disconnections"`
	EventsObserved     int64      `json:"events_observed"`
	QueuedEvents       int        `json:"queued_events"`
	GapRecoveryCount   int        `json:"gap_recovery_count"`
	LastConnectedAt    *time.Time `json:"last_connected_at,omitempty"`
	LastEventAt        *time.Time `json:"last_event_at,omitempty"`
	LastDisconnectedAt *time.Time `json:"last_disconnected_at,omitempty"`
	LastError          string     `json:"last_error,omitempty"`
}

type clientCommand struct {
	ctx  context.Context
	run  func(context.Context, *telegram.Client) error
	done chan error
}

type accountWorker struct {
	service   *Service
	accountID int64
	ctx       context.Context
	cancel    context.CancelFunc
	commands  chan clientCommand
	exited    chan struct{}
	exitOnce  sync.Once

	errMu sync.RWMutex
	err   error
}

func (s *Service) accountWorker(accountID int64) (*accountWorker, error) {
	s.workersMu.Lock()
	defer s.workersMu.Unlock()
	if s.closing {
		return nil, ErrNotConfigured
	}
	if worker := s.workers[accountID]; worker != nil {
		return worker, nil
	}
	if err := s.ctx.Err(); err != nil {
		return nil, ErrNotConfigured
	}
	ctx, cancel := context.WithCancel(s.ctx)
	worker := &accountWorker{
		service:   s,
		accountID: accountID,
		ctx:       ctx,
		cancel:    cancel,
		commands:  make(chan clientCommand),
		exited:    make(chan struct{}),
	}
	s.workers[accountID] = worker
	s.workersWG.Add(1)
	go worker.run()
	return worker, nil
}

func (w *accountWorker) submit(
	ctx context.Context,
	run func(context.Context, *telegram.Client) error,
) error {
	reply := make(chan error, 1)
	command := clientCommand{ctx: ctx, run: run, done: reply}
	select {
	case <-w.exited:
		return w.terminalError()
	case w.commands <- command:
	case <-ctx.Done():
		return fmt.Errorf("%w: request was canceled before starting", ErrTimeout)
	}
	select {
	case err := <-reply:
		return err
	case <-w.exited:
		return w.terminalError()
	case <-ctx.Done():
		return fmt.Errorf("%w: request was canceled", ErrTimeout)
	}
}

func (w *accountWorker) run() {
	defer w.service.workersWG.Done()
	defer w.finish()
	err := w.connect()
	if errors.Is(err, context.Canceled) && w.ctx.Err() != nil {
		err = nil
	}
	w.setTerminalError(w.service.classifyTelegramError(err))
}

func (w *accountWorker) connect() error {
	sessionPath, err := w.service.sessionPath(w.accountID)
	if err != nil {
		return err
	}
	if err := telegramauth.PrepareDeviceSession(sessionPath); err != nil {
		return fmt.Errorf("prepare Telegram device: %w", err)
	}

	manager := updates.New(updates.Config{
		Handler: telegram.UpdateHandlerFunc(func(_ context.Context, value tg.UpdatesClass) error {
			w.service.observeUpdate(w.accountID, value)
			return nil
		}),
		OnChannelTooLong: func(_ int64) {
			w.service.observeGap(w.accountID)
		},
	})
	client := telegram.NewClient(w.service.appID, w.service.appHash, telegram.Options{
		SessionStorage:  &session.FileStorage{Path: sessionPath},
		Device:          telegramauth.DeviceForSession(sessionPath),
		DialTimeout:     15 * time.Second,
		ExchangeTimeout: 15 * time.Second,
		UpdateHandler:   manager,
		OnDead: func(err error) {
			w.disconnected(err)
		},
	})

	return client.Run(w.ctx, func(runCtx context.Context) error {
		self, err := client.Self(runCtx)
		if err != nil {
			return err
		}
		w.connected()

		updateCtx, stopUpdates := context.WithCancel(runCtx)
		defer stopUpdates()
		managerErrors := make(chan error, 1)
		go func() {
			managerErrors <- manager.Run(updateCtx, client.API(), self.ID, updates.AuthOptions{})
		}()

		for {
			select {
			case <-runCtx.Done():
				return nil
			case err := <-managerErrors:
				if err == nil || errors.Is(err, context.Canceled) {
					return nil
				}
				return fmt.Errorf("run Telegram update manager: %w", err)
			case command := <-w.commands:
				runErr := command.run(command.ctx, client)
				classified := w.service.classifyTelegramError(runErr)
				command.done <- classified
				if errors.Is(classified, ErrSessionMissing) ||
					errors.Is(classified, ErrSessionInvalid) {
					w.noteError(classified)
					return classified
				}
			}
		}
	})
}

func (w *accountWorker) finish() {
	w.exitOnce.Do(func() {
		w.service.workersMu.Lock()
		if w.service.workers[w.accountID] == w {
			delete(w.service.workers, w.accountID)
		}
		w.service.workersMu.Unlock()
		w.service.updateListener(w.accountID, func(status *ListenerStatus) {
			status.Connected = false
		})
		close(w.exited)
	})
}

func (w *accountWorker) terminalError() error {
	w.errMu.RLock()
	defer w.errMu.RUnlock()
	if w.err != nil {
		return w.err
	}
	return fmt.Errorf("%w: Telegram connection closed", ErrTelegram)
}

func (w *accountWorker) setTerminalError(err error) {
	w.errMu.Lock()
	w.err = err
	w.errMu.Unlock()
	if err != nil {
		w.noteError(err)
	}
}

func (w *accountWorker) connected() {
	now := time.Now().UTC()
	w.service.updateListener(w.accountID, func(status *ListenerStatus) {
		status.Connected = true
		status.Connections++
		status.LastConnectedAt = &now
		status.LastError = ""
	})
}

func (w *accountWorker) disconnected(err error) {
	now := time.Now().UTC()
	w.service.updateListener(w.accountID, func(status *ListenerStatus) {
		status.Connected = false
		status.Disconnections++
		status.LastDisconnectedAt = &now
		if err != nil {
			status.LastError = truncateError(err)
		}
	})
}

func (w *accountWorker) noteError(err error) {
	if err == nil {
		return
	}
	w.service.updateListener(w.accountID, func(status *ListenerStatus) {
		status.LastError = truncateError(err)
	})
}

func (s *Service) classifyTelegramError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotConfigured) ||
		errors.Is(err, ErrSessionMissing) ||
		errors.Is(err, ErrSessionInvalid) ||
		errors.Is(err, ErrPeerNotFound) ||
		errors.Is(err, ErrFloodWait) ||
		errors.Is(err, ErrTimeout) ||
		errors.Is(err, ErrTelegram) {
		return err
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return &FloodWaitError{Wait: wait}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: request timed out", ErrTimeout)
	}
	if tgerr.Is(err, tg.ErrTimeout) {
		return fmt.Errorf("%w: Telegram timeout", ErrTimeout)
	}
	if isInvalidSessionError(err) {
		return fmt.Errorf("%w: %v", ErrSessionInvalid, err)
	}
	return fmt.Errorf("%w: %v", ErrTelegram, err)
}

func (s *Service) updateListener(accountID int64, apply func(*ListenerStatus)) {
	s.listenerMu.Lock()
	defer s.listenerMu.Unlock()
	status := s.listeners[accountID]
	status.AccountID = accountID
	apply(&status)
	s.listeners[accountID] = status
}

func (s *Service) observeUpdate(accountID int64, value tg.UpdatesClass) {
	if !updateTouchesMessages(value) {
		return
	}
	now := time.Now().UTC()
	s.updateListener(accountID, func(status *ListenerStatus) {
		status.EventsObserved++
		status.LastEventAt = &now
	})
	select {
	case s.updates <- accountID:
	default:
		// The periodic collection remains the source of truth when the event
		// queue is full, so dropping a duplicate wake-up is safe.
		s.observeGap(accountID)
	}
}

func (s *Service) observeGap(accountID int64) {
	s.updateListener(accountID, func(status *ListenerStatus) {
		status.GapRecoveryCount++
	})
	select {
	case s.updates <- accountID:
	default:
	}
}

func updateTouchesMessages(value tg.UpdatesClass) bool {
	switch update := value.(type) {
	case *tg.Updates:
		return anyUpdateTouchesMessages(update.Updates)
	case *tg.UpdatesCombined:
		return anyUpdateTouchesMessages(update.Updates)
	case *tg.UpdateShort:
		return updateClassTouchesMessages(update.Update)
	case *tg.UpdatesTooLong:
		return true
	case *tg.UpdateShortMessage,
		*tg.UpdateShortChatMessage,
		*tg.UpdateShortSentMessage:
		return true
	default:
		return false
	}
}

func anyUpdateTouchesMessages(items []tg.UpdateClass) bool {
	for _, item := range items {
		if updateClassTouchesMessages(item) {
			return true
		}
	}
	return false
}

func updateClassTouchesMessages(value tg.UpdateClass) bool {
	switch value.(type) {
	case *tg.UpdateNewMessage,
		*tg.UpdateNewChannelMessage,
		*tg.UpdateEditMessage,
		*tg.UpdateEditChannelMessage,
		*tg.UpdateDeleteMessages,
		*tg.UpdateDeleteChannelMessages,
		*tg.UpdateChannelTooLong:
		return true
	default:
		return false
	}
}

func truncateError(err error) string {
	const maxRunes = 300
	message := []rune(err.Error())
	if len(message) > maxRunes {
		message = message[:maxRunes]
	}
	return string(message)
}
