// Package monitorscheduler polls enabled Telegram monitor targets.
package monitorscheduler

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

type AccountLookup interface {
	Get(ctx context.Context, id int64) (account.Account, error)
	RecordVerification(
		ctx context.Context,
		id int64,
		status string,
		reason string,
	) (account.Account, error)
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
	collectionPageSize    = 50
	collectionMaxPages    = 20
	defaultAccountWorkers = 3
	defaultTargetBudget   = 4
)

type AccountStatus struct {
	AccountID           int64      `json:"account_id"`
	TargetCount         int        `json:"target_count"`
	QueuedTargets       int        `json:"queued_targets"`
	ProcessedTargets    int        `json:"processed_targets"`
	Running             bool       `json:"running"`
	NextCollectionAt    *time.Time `json:"next_collection_at,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	FloodWait           bool       `json:"flood_wait"`
	LastError           string     `json:"last_error,omitempty"`
}

type Status struct {
	Running             bool                          `json:"running"`
	Mode                string                        `json:"mode"`
	QueuedTargets       int                           `json:"queued_targets"`
	LastTrigger         string                        `json:"last_trigger,omitempty"`
	PollIntervalSeconds int64                         `json:"poll_interval_seconds"`
	PollRuns            int64                         `json:"poll_runs"`
	UpdateDrivenRuns    int64                         `json:"update_driven_runs"`
	ListenerCount       int                           `json:"listener_count"`
	ConnectedListeners  int                           `json:"connected_listeners"`
	QueuedUpdateEvents  int                           `json:"queued_update_events"`
	LastStarted         *time.Time                    `json:"last_started_at,omitempty"`
	LastFinished        *time.Time                    `json:"last_finished_at,omitempty"`
	NextRunAt           *time.Time                    `json:"next_run_at,omitempty"`
	LastError           string                        `json:"last_error,omitempty"`
	Accounts            []AccountStatus               `json:"accounts,omitempty"`
	Listeners           []telegramchat.ListenerStatus `json:"listeners,omitempty"`
}

type accountTargetGroup struct {
	accountID        int64
	targets          []operations.CollectionTarget
	next             int
	nextCollectionAt time.Time
	blocked          bool
	active           bool
	checked          bool
	failures         int
	floodWait        bool
	lastError        string
}

type Scheduler struct {
	operations     *operations.Service
	accounts       AccountLookup
	chats          ChatReader
	interval       time.Duration
	initialDelay   time.Duration
	logger         *slog.Logger
	now            func() time.Time
	accountWorkers int
	targetBudget   int
	mu             sync.Mutex
	runMu          sync.Mutex
	status         Status
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
		operations:     service,
		accounts:       accounts,
		chats:          chats,
		interval:       interval,
		initialDelay:   15 * time.Second,
		logger:         logger,
		now:            time.Now,
		accountWorkers: defaultAccountWorkers,
		targetBudget:   defaultTargetBudget,
		status: Status{
			Mode:                "polling",
			PollIntervalSeconds: int64(interval / time.Second),
		},
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	if s == nil || s.operations == nil || s.accounts == nil || s.chats == nil {
		return
	}
	updateSource, _ := s.chats.(interface {
		Updates() <-chan int64
	})
	var updates <-chan int64
	if updateSource != nil {
		updates = updateSource.Updates()
		s.mu.Lock()
		s.status.Mode = "event_driven_with_poll_fallback"
		s.mu.Unlock()
	}
	go func() {
		nextRun := time.Now().UTC().Add(s.initialDelay)
		s.setNextRun(nextRun)
		timer := time.NewTimer(time.Until(nextRun))
		defer timer.Stop()
		for {
			trigger := ""
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				trigger = "poll"
			case <-updates:
				trigger = "update"
				for {
					select {
					case <-updates:
					default:
						goto run
					}
				}
			}
		run:
			s.recordTrigger(trigger)
			if trigger == "update" {
				if err := ctx.Err(); err != nil {
					return
				}
			}
			if err := s.RunOnce(ctx); err != nil {
				s.logger.Warn("monitor scheduler run failed", "trigger", trigger, "error", err)
			}
			nextRun = time.Now().UTC().Add(s.interval)
			s.setNextRun(nextRun)
			timer.Reset(time.Until(nextRun))
		}
	}()
}

func (s *Scheduler) recordTrigger(trigger string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.LastTrigger = trigger
	switch trigger {
	case "update":
		s.status.UpdateDrivenRuns++
	case "poll":
		s.status.PollRuns++
	}
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
		s.resetAccountStatuses(nil)
		s.finish(started, err)
		return err
	}
	groups := groupTargetsByAccount(targets)
	s.resetAccountStatuses(groups)
	workerCount := s.accountWorkers
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > len(groups) {
		workerCount = len(groups)
	}
	jobs := make(chan int, len(groups))
	errs := make([]error, len(groups))
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				errs[index] = s.runAccountGroup(ctx, &groups[index])
			}
		}()
	}
	for index := range groups {
		jobs <- index
	}
	close(jobs)
	workers.Wait()

	var runErr error
	for _, err := range errs {
		runErr = errors.Join(runErr, err)
	}
	s.finish(started, runErr)
	return runErr
}

func (s *Scheduler) runAccountGroup(
	ctx context.Context,
	group *accountTargetGroup,
) error {
	if group == nil || group.blocked {
		return nil
	}
	s.updateAccountStatus(*group, true)
	defer func() {
		s.updateAccountStatus(*group, false)
	}()
	if !group.nextCollectionAt.IsZero() &&
		s.now().Before(group.nextCollectionAt) {
		return nil
	}

	budget := s.targetBudget
	if budget <= 0 {
		budget = 1
	}
	var runErr error
	processed := 0
	for group.next < len(group.targets) && processed < budget {
		if err := ctx.Err(); err != nil {
			return errors.Join(runErr, err)
		}
		target := group.targets[group.next]
		group.next++
		processed++
		stop, err := s.runAccountTarget(ctx, group, target)
		runErr = errors.Join(runErr, err)
		if stop {
			group.blocked = true
			return runErr
		}
	}
	return runErr
}

func (s *Scheduler) runAccountTarget(
	ctx context.Context,
	group *accountTargetGroup,
	target operations.CollectionTarget,
) (bool, error) {
	if !group.checked {
		group.checked = true
		item, err := s.accounts.Get(ctx, group.accountID)
		if errors.Is(err, account.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return true, err
		}
		group.active = item.Status == account.StatusOnline
	}
	if !group.active {
		return true, nil
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
		if ctx.Err() != nil {
			return true, errors.Join(err, ctx.Err())
		}
		return s.handleCollectionFailure(ctx, group, target, collection, err)
	}
	if err := s.ingestCollection(ctx, target, collection); err != nil {
		return false, err
	}
	clearErr := s.clearAccountDeferral(ctx, group.accountID)
	group.nextCollectionAt = time.Time{}
	group.failures = 0
	group.floodWait = false
	group.lastError = ""
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
	return false, clearErr
}

func (s *Scheduler) handleCollectionFailure(
	ctx context.Context,
	group *accountTargetGroup,
	target operations.CollectionTarget,
	collection telegramchat.MessageCollection,
	collectionErr error,
) (bool, error) {
	var runErr error
	if err := s.recordBatchFailure(ctx, target, collection, collectionErr); err != nil {
		s.logger.Warn("record monitor batch failure",
			"account_id", target.AccountID,
			"peer_type", target.PeerType,
			"peer_id", target.PeerID,
			"error", err)
		runErr = errors.Join(runErr, err)
	}
	runErr = errors.Join(runErr, collectionErr)
	group.lastError = collectionErr.Error()
	group.floodWait = errors.Is(collectionErr, telegramchat.ErrFloodWait)
	if errors.Is(collectionErr, telegramchat.ErrSessionMissing) ||
		errors.Is(collectionErr, telegramchat.ErrSessionInvalid) {
		reason := "授权会话不存在，请重新登录"
		if errors.Is(collectionErr, telegramchat.ErrSessionInvalid) {
			reason = "Telegram 授权已失效，请重新登录"
		}
		if err := s.recordAccountOffline(ctx, group.accountID, reason); err != nil {
			runErr = errors.Join(runErr, err)
		}
		return true, runErr
	}
	if !isAccountCollectionFailure(collectionErr) {
		return false, runErr
	}
	wait := s.accountFailureBackoff(group.failures + 1)
	var floodWait *telegramchat.FloodWaitError
	if errors.As(collectionErr, &floodWait) && floodWait.Wait > 0 {
		wait = floodWait.Wait
	}
	if err := s.deferAccountCollection(
		ctx,
		group,
		wait,
		collectionErr.Error(),
	); err != nil {
		runErr = errors.Join(runErr, err)
	}
	return true, runErr
}

func isAccountCollectionFailure(err error) bool {
	return errors.Is(err, telegramchat.ErrFloodWait) ||
		errors.Is(err, telegramchat.ErrTimeout) ||
		errors.Is(err, telegramchat.ErrTelegram) ||
		errors.Is(err, context.DeadlineExceeded)
}

func (s *Scheduler) accountFailureBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	wait := s.interval
	for attempt := 1; attempt < failures && wait < 30*time.Minute; attempt++ {
		wait *= 2
	}
	if wait > 30*time.Minute {
		wait = 30 * time.Minute
	}
	if wait <= 0 {
		wait = time.Minute
	}
	return wait
}

func (s *Scheduler) recordBatchFailure(
	ctx context.Context,
	target operations.CollectionTarget,
	collection telegramchat.MessageCollection,
	collectionErr error,
) error {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.operations.RecordBatchFailure(recordCtx, operations.IngestBatchInput{
		Source:           "scheduler",
		AccountID:        target.AccountID,
		PeerType:         target.PeerType,
		PeerID:           target.PeerID,
		PeerTitle:        target.PeerTitle,
		CheckpointBefore: target.LastMessageID,
		BacklogDetected:  target.BacklogDetected || collection.Backlog,
	}, collectionErr)
}

func (s *Scheduler) deferAccountCollection(
	ctx context.Context,
	group *accountTargetGroup,
	wait time.Duration,
	reason string,
) error {
	group.nextCollectionAt = s.now().UTC().Add(wait)
	group.failures++
	deferCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.operations.DeferAccountCollection(
		deferCtx,
		group.accountID,
		group.nextCollectionAt,
		reason,
	); err != nil {
		return fmt.Errorf("defer account %d collection: %w", group.accountID, err)
	}
	return nil
}

func (s *Scheduler) clearAccountDeferral(
	ctx context.Context,
	accountID int64,
) error {
	clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.operations.ClearAccountCollectionDeferral(clearCtx, accountID); err != nil {
		return fmt.Errorf("clear account %d collection deferral: %w", accountID, err)
	}
	return nil
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
		groups[index].failures = max(
			groups[index].failures,
			target.AccountConsecutiveFailures,
		)
		if target.AccountLastError != "" {
			groups[index].lastError = target.AccountLastError
		}
		groups[index].floodWait = groups[index].floodWait || target.FloodWait
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

func (s *Scheduler) recordAccountOffline(
	ctx context.Context,
	accountID int64,
	reason string,
) error {
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.accounts.RecordVerification(
		updateCtx,
		accountID,
		account.StatusError,
		reason,
	); err != nil {
		return fmt.Errorf("record Telegram account %d offline: %w", accountID, err)
	}
	return nil
}

func (s *Scheduler) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.Lock()
	status := s.status
	status.Accounts = append([]AccountStatus(nil), s.status.Accounts...)
	s.mu.Unlock()
	if reporter, ok := s.chats.(interface {
		ListenerStatuses() []telegramchat.ListenerStatus
	}); ok {
		status.Listeners = reporter.ListenerStatuses()
		status.ListenerCount = len(status.Listeners)
		for _, listener := range status.Listeners {
			if listener.Connected {
				status.ConnectedListeners++
			}
			if listener.QueuedEvents > status.QueuedUpdateEvents {
				status.QueuedUpdateEvents = listener.QueuedEvents
			}
		}
	}
	return status
}

func (s *Scheduler) resetAccountStatuses(groups []accountTargetGroup) {
	statuses := make([]AccountStatus, 0, len(groups))
	queued := 0
	for index := range groups {
		item := accountStatus(groups[index], false)
		statuses = append(statuses, item)
		queued += item.QueuedTargets
	}
	s.mu.Lock()
	s.status.Accounts = statuses
	s.status.QueuedTargets = queued
	s.mu.Unlock()
}

func (s *Scheduler) updateAccountStatus(
	group accountTargetGroup,
	running bool,
) {
	item := accountStatus(group, running)
	s.mu.Lock()
	defer s.mu.Unlock()
	queued := 0
	found := false
	for index := range s.status.Accounts {
		if s.status.Accounts[index].AccountID == item.AccountID {
			s.status.Accounts[index] = item
			found = true
		}
		queued += s.status.Accounts[index].QueuedTargets
	}
	if !found {
		s.status.Accounts = append(s.status.Accounts, item)
		queued += item.QueuedTargets
	}
	s.status.QueuedTargets = queued
}

func accountStatus(
	group accountTargetGroup,
	running bool,
) AccountStatus {
	processed := min(group.next, len(group.targets))
	var nextCollectionAt *time.Time
	if !group.nextCollectionAt.IsZero() {
		value := group.nextCollectionAt.UTC()
		nextCollectionAt = &value
	}
	return AccountStatus{
		AccountID:           group.accountID,
		TargetCount:         len(group.targets),
		QueuedTargets:       max(0, len(group.targets)-processed),
		ProcessedTargets:    processed,
		Running:             running,
		NextCollectionAt:    nextCollectionAt,
		ConsecutiveFailures: group.failures,
		FloodWait:           group.floodWait,
		LastError:           group.lastError,
	}
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
