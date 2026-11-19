package monitorscheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

type fakeAccountLookup struct {
	inactive      map[int64]bool
	statuses      map[int64]string
	verifications []accountVerification
}

type accountVerification struct {
	accountID int64
	status    string
	reason    string
}

func (f *fakeAccountLookup) Get(
	_ context.Context,
	id int64,
) (account.Account, error) {
	if f.inactive[id] {
		return account.Account{}, account.ErrNotFound
	}
	status := account.StatusOnline
	if statusValue, ok := f.statuses[id]; ok {
		status = statusValue
	}
	return account.Account{
		ID:         id,
		Status:     status,
		PurgeState: account.PurgeStateActive,
	}, nil
}

func (f *fakeAccountLookup) RecordVerification(
	_ context.Context,
	id int64,
	status string,
	reason string,
) (account.Account, error) {
	f.verifications = append(f.verifications, accountVerification{
		accountID: id,
		status:    status,
		reason:    reason,
	})
	return account.Account{ID: id, Status: status, StatusReason: reason}, nil
}

type fakeChatReader struct {
	collection telegramchat.MessageCollection
	err        error
}

type recordingChatReader struct {
	mu           sync.Mutex
	calls        []int64
	errByAccount map[int64]error
}

func (r *recordingChatReader) CollectMessages(
	_ context.Context,
	accountID int64,
	_ string,
	_ int64,
	_ int,
	_ int,
	_ int,
) (telegramchat.MessageCollection, error) {
	r.mu.Lock()
	r.calls = append(r.calls, accountID)
	r.mu.Unlock()
	if err := r.errByAccount[accountID]; err != nil {
		return telegramchat.MessageCollection{}, err
	}
	return telegramchat.MessageCollection{Complete: true}, nil
}

type concurrencyChatReader struct {
	mu              sync.Mutex
	entered         chan int64
	release         chan struct{}
	activeByAccount map[int64]int
	maxByAccount    map[int64]int
	active          int
	maxActive       int
	calls           map[int64]int
}

func newConcurrencyChatReader() *concurrencyChatReader {
	return &concurrencyChatReader{
		entered:         make(chan int64, 8),
		release:         make(chan struct{}),
		activeByAccount: make(map[int64]int),
		maxByAccount:    make(map[int64]int),
		calls:           make(map[int64]int),
	}
}

func (r *concurrencyChatReader) CollectMessages(
	ctx context.Context,
	accountID int64,
	_ string,
	_ int64,
	_ int,
	_ int,
	_ int,
) (telegramchat.MessageCollection, error) {
	r.mu.Lock()
	r.calls[accountID]++
	r.active++
	r.activeByAccount[accountID]++
	r.maxActive = max(r.maxActive, r.active)
	r.maxByAccount[accountID] = max(r.maxByAccount[accountID], r.activeByAccount[accountID])
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.active--
		r.activeByAccount[accountID]--
		r.mu.Unlock()
	}()
	select {
	case r.entered <- accountID:
	case <-ctx.Done():
		return telegramchat.MessageCollection{}, ctx.Err()
	}
	select {
	case <-r.release:
		return telegramchat.MessageCollection{Complete: true}, nil
	case <-ctx.Done():
		return telegramchat.MessageCollection{}, ctx.Err()
	}
}

type collectionSequence struct {
	collections []telegramchat.MessageCollection
	checkpoints []int
	index       int
}

type updateSourceChatReader struct {
	updates chan int64
	calls   chan int64
}

func (r *updateSourceChatReader) CollectMessages(
	_ context.Context,
	accountID int64,
	_ string,
	_ int64,
	_ int,
	_ int,
	_ int,
) (telegramchat.MessageCollection, error) {
	select {
	case r.calls <- accountID:
	default:
	}
	return telegramchat.MessageCollection{Complete: true}, nil
}

func (r *updateSourceChatReader) Updates() <-chan int64 {
	return r.updates
}

type pollOnlyChatReader struct {
	calls chan int64
}

func (r *pollOnlyChatReader) CollectMessages(
	_ context.Context,
	accountID int64,
	_ string,
	_ int64,
	_ int,
	_ int,
	_ int,
) (telegramchat.MessageCollection, error) {
	select {
	case r.calls <- accountID:
	default:
	}
	return telegramchat.MessageCollection{Complete: true}, nil
}

func (s *collectionSequence) CollectMessages(
	_ context.Context,
	_ int64,
	_ string,
	_ int64,
	checkpointID int,
	_ int,
	_ int,
) (telegramchat.MessageCollection, error) {
	s.checkpoints = append(s.checkpoints, checkpointID)
	if s.index >= len(s.collections) {
		return telegramchat.MessageCollection{}, nil
	}
	collection := s.collections[s.index]
	s.index++
	return collection, nil
}

func (f fakeChatReader) CollectMessages(
	context.Context,
	int64,
	string,
	int64,
	int,
	int,
	int,
) (telegramchat.MessageCollection, error) {
	return f.collection, f.err
}

func testOperationsService(t *testing.T) *operations.Service {
	t.Helper()
	service, err := operations.Open(
		context.Background(),
		filepath.Join(t.TempDir(), "operations.db"),
	)
	if err != nil {
		t.Fatalf("operations.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Errorf("operations.Close() error = %v", err)
		}
	})
	return service
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSchedulerEstablishesBaselineBeforeIndexingNewMessages(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	rule, err := service.CreateRule(
		ctx,
		"Ops",
		7,
		"chat",
		42,
		"Operations",
		[]string{"urgent"},
	)
	if err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	chats := &collectionSequence{collections: []telegramchat.MessageCollection{
		{NewestID: 11, Complete: true},
		{
			Items: []telegramchat.Message{
				{ID: 11, Sender: "Bob", Text: "baseline", SentAt: time.Now().Add(-time.Minute)},
				{ID: 12, Sender: "Alice", Text: "urgent deploy", SentAt: time.Now()},
			},
			NewestID: 12,
			Complete: true,
		},
	}}
	scheduler := New(service, &fakeAccountLookup{}, chats, time.Minute, testLogger())
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	page, err := service.SearchMessages(ctx, operations.MessageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("first run indexed %d historical messages, want 0", page.Total)
	}
	updated, err := service.GetRule(ctx, rule.ID)
	if err != nil {
		t.Fatalf("GetRule() error = %v", err)
	}
	if updated.LastMessageID != 11 {
		t.Fatalf("LastMessageID = %d, want 11", updated.LastMessageID)
	}
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}
	page, err = service.SearchMessages(ctx, operations.MessageQuery{Query: "deploy", Limit: 20})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("second run indexed %d new messages, want 1", page.Total)
	}
	alerts, err := service.ListAlerts(ctx, "pending", 20)
	if err != nil {
		t.Fatalf("ListAlerts() error = %v", err)
	}
	if len(alerts) != 1 || alerts[0].MessageID != 12 {
		t.Fatalf("ListAlerts() = %+v", alerts)
	}
}

func TestRunOncePersistsNewMessagesAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	now := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	if _, err := service.CreateRule(ctx, "Scheduler", 7, "chat", 42, "Ops", nil); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	if _, err := service.IngestBatch(ctx, operations.IngestBatchInput{
		Source:          "test",
		AccountID:       7,
		PeerType:        "chat",
		PeerID:          42,
		PeerTitle:       "Ops",
		CheckpointAfter: 100,
		Complete:        true,
		Messages: []operations.MessageRecord{{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    42,
			PeerTitle: "Ops",
			MessageID: 100,
			Sender:    "Alice",
			Text:      "baseline",
			SentAt:    now.Add(-time.Minute),
		}},
	}); err != nil {
		t.Fatalf("seed IngestBatch() error = %v", err)
	}

	reader := fakeChatReader{collection: telegramchat.MessageCollection{
		Items: []telegramchat.Message{
			{ID: 100, Sender: "Alice", Text: "baseline", SentAt: now.Add(-time.Minute)},
			{
				ID:        101,
				Sender:    "Alice",
				Text:      "scheduled update",
				MediaType: "文件",
				Media: &telegramchat.MediaReference{
					Type:          "文件",
					Kind:          "document",
					ID:            7001,
					MimeType:      "application/pdf",
					FileName:      "report.pdf",
					Size:          2048,
					AccessHash:    8002,
					FileReference: []byte("private-reference"),
				},
				SentAt: now,
			},
		},
		NewestID:  101,
		Complete:  true,
		PagesRead: 1,
	}}
	scheduler := New(service, &fakeAccountLookup{}, reader, time.Minute, testLogger())
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	page, err := service.SearchMessages(ctx, operations.MessageQuery{Query: "scheduled update"})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].MessageID != 101 {
		t.Fatalf("SearchMessages() = %+v", page)
	}
	targets, err := service.ListCollectionTargets(ctx)
	if err != nil {
		t.Fatalf("ListCollectionTargets() error = %v", err)
	}
	if len(targets) != 1 || targets[0].LastMessageID != 101 ||
		targets[0].ConsecutiveFailures != 0 {
		t.Fatalf("ListCollectionTargets() = %+v", targets)
	}
	events, err := service.ListEvidenceEvents(ctx, 7, "chat", 42, 101, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("ListEvidenceEvents() = %+v, %v", events, err)
	}
	var media map[string]any
	if err := json.Unmarshal(events[0].Media, &media); err != nil {
		t.Fatalf("decode scheduler evidence media: %v", err)
	}
	if media["kind"] != "document" ||
		media["file_name"] != "report.pdf" ||
		media["storage_state"] != "reference_only" ||
		media["reference_sha256"] == "" {
		t.Fatalf("scheduler evidence media = %+v", media)
	}
	status := scheduler.Status()
	if status.Running || status.LastError != "" || status.LastFinished == nil {
		t.Fatalf("Status() = %+v", status)
	}
}

func TestRunOnceRecordsCollectionFailure(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	if _, err := service.CreateRule(ctx, "Failure", 8, "channel", 99, "Alerts", nil); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	readerErr := errors.New("Telegram unavailable")
	scheduler := New(
		service,
		&fakeAccountLookup{},
		fakeChatReader{err: readerErr},
		time.Minute,
		testLogger(),
	)
	if err := scheduler.RunOnce(ctx); !errors.Is(err, readerErr) {
		t.Fatalf("RunOnce() error = %v, want %v", err, readerErr)
	}
	targets, err := service.ListCollectionTargets(ctx)
	if err != nil {
		t.Fatalf("ListCollectionTargets() error = %v", err)
	}
	if len(targets) != 1 || targets[0].ConsecutiveFailures != 1 ||
		targets[0].LastError != readerErr.Error() {
		t.Fatalf("ListCollectionTargets() = %+v", targets)
	}
	if status := scheduler.Status(); status.LastError != readerErr.Error() {
		t.Fatalf("Status() = %+v", status)
	}
}

func TestRunOncePersistsExpiredSessionAndStopsAccountWork(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	for _, peerID := range []int64{101, 102} {
		if _, err := service.CreateRule(
			ctx,
			fmt.Sprintf("Expired %d", peerID),
			8,
			"channel",
			peerID,
			"Alerts",
			nil,
		); err != nil {
			t.Fatalf("CreateRule() error = %v", err)
		}
	}
	accounts := &fakeAccountLookup{}
	reader := &recordingChatReader{
		errByAccount: map[int64]error{
			8: fmt.Errorf("%w: AUTH_KEY_UNREGISTERED", telegramchat.ErrSessionInvalid),
		},
	}
	scheduler := New(service, accounts, reader, time.Minute, testLogger())
	if err := scheduler.RunOnce(ctx); !errors.Is(err, telegramchat.ErrSessionInvalid) {
		t.Fatalf("RunOnce() error = %v, want invalid session", err)
	}
	if len(reader.calls) != 1 {
		t.Fatalf("CollectMessages() calls = %v, want one call for the blocked account", reader.calls)
	}
	if len(accounts.verifications) != 1 {
		t.Fatalf("RecordVerification() calls = %d, want 1", len(accounts.verifications))
	}
	verification := accounts.verifications[0]
	if verification.accountID != 8 ||
		verification.status != account.StatusError ||
		!strings.Contains(verification.reason, "重新登录") {
		t.Fatalf("verification = %+v, want persisted offline state", verification)
	}
}

func TestRunOncePersistsMissingSession(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	if _, err := service.CreateRule(ctx, "Missing", 9, "chat", 99, "Archive", nil); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	accounts := &fakeAccountLookup{}
	scheduler := New(
		service,
		accounts,
		&recordingChatReader{
			errByAccount: map[int64]error{
				9: telegramchat.ErrSessionMissing,
			},
		},
		time.Minute,
		testLogger(),
	)
	if err := scheduler.RunOnce(ctx); !errors.Is(err, telegramchat.ErrSessionMissing) {
		t.Fatalf("RunOnce() error = %v, want missing session", err)
	}
	if len(accounts.verifications) != 1 ||
		accounts.verifications[0].status != account.StatusError ||
		!strings.Contains(accounts.verifications[0].reason, "不存在") {
		t.Fatalf("verifications = %+v, want missing-session offline state", accounts.verifications)
	}
}

func TestRunOnceKeepsOnlineStatusForTransientFailure(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	if _, err := service.CreateRule(ctx, "Transient", 10, "chat", 99, "Archive", nil); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	accounts := &fakeAccountLookup{}
	scheduler := New(
		service,
		accounts,
		fakeChatReader{err: fmt.Errorf("%w: connection reset", telegramchat.ErrTelegram)},
		time.Minute,
		testLogger(),
	)
	if err := scheduler.RunOnce(ctx); err == nil {
		t.Fatal("RunOnce() error = nil, want transient failure")
	}
	if len(accounts.verifications) != 0 {
		t.Fatalf("RecordVerification() calls = %d, want 0", len(accounts.verifications))
	}
}

func TestStartWakesCollectionFromTelegramUpdate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := testOperationsService(t)
	if _, err := service.CreateRule(ctx, "Realtime", 7, "chat", 42, "Ops", nil); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	reader := &updateSourceChatReader{
		updates: make(chan int64, 1),
		calls:   make(chan int64, 1),
	}
	scheduler := New(service, &fakeAccountLookup{}, reader, time.Hour, testLogger())
	scheduler.initialDelay = time.Hour
	scheduler.Start(ctx)
	reader.updates <- 7

	select {
	case accountID := <-reader.calls:
		if accountID != 7 {
			t.Fatalf("collection account ID = %d, want 7", accountID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Telegram update did not wake monitor collection")
	}
	status := scheduler.Status()
	if status.Mode != "event_driven_with_poll_fallback" ||
		status.UpdateDrivenRuns != 1 ||
		status.LastTrigger != "update" {
		t.Fatalf("Status() = %+v", status)
	}
}

func TestStartKeepsPeriodicPollingWithoutUpdateSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := testOperationsService(t)
	if _, err := service.CreateRule(ctx, "Fallback", 7, "chat", 42, "Ops", nil); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	reader := &pollOnlyChatReader{calls: make(chan int64, 1)}
	scheduler := New(service, &fakeAccountLookup{}, reader, time.Hour, testLogger())
	scheduler.initialDelay = time.Millisecond
	scheduler.Start(ctx)

	select {
	case accountID := <-reader.calls:
		if accountID != 7 {
			t.Fatalf("collection account ID = %d, want 7", accountID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("periodic polling did not run")
	}
	status := scheduler.Status()
	if status.Mode != "polling" || status.PollRuns != 1 {
		t.Fatalf("Status() = %+v", status)
	}
}

func TestRunOnceSkipsAccountsPendingDeletion(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	if _, err := service.CreateRule(ctx, "Deleting", 9, "channel", 99, "Archive", nil); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	reader := &collectionSequence{}
	scheduler := New(
		service,
		&fakeAccountLookup{inactive: map[int64]bool{9: true}},
		reader,
		time.Minute,
		testLogger(),
	)
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if reader.index != 0 {
		t.Fatal("scheduler read Telegram for an account pending deletion")
	}
	targets, err := service.ListCollectionTargets(ctx)
	if err != nil {
		t.Fatalf("ListCollectionTargets() error = %v", err)
	}
	if len(targets) != 1 || targets[0].ConsecutiveFailures != 0 {
		t.Fatalf("pending deletion target changed checkpoint state: %+v", targets)
	}
}

func TestRunOnceSkipsInactiveAccounts(t *testing.T) {
	for _, status := range []string{
		account.StatusPending,
		account.StatusPaused,
		account.StatusError,
	} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			service := testOperationsService(t)
			if _, err := service.CreateRule(
				ctx,
				"Inactive",
				10,
				"chat",
				99,
				"Operations",
				nil,
			); err != nil {
				t.Fatalf("CreateRule() error = %v", err)
			}
			reader := &collectionSequence{}
			scheduler := New(
				service,
				&fakeAccountLookup{statuses: map[int64]string{10: status}},
				reader,
				time.Minute,
				testLogger(),
			)
			if err := scheduler.RunOnce(ctx); err != nil {
				t.Fatalf("RunOnce() error = %v", err)
			}
			if reader.index != 0 {
				t.Fatalf("scheduler read Telegram for account with status %q", status)
			}
			targets, err := service.ListCollectionTargets(ctx)
			if err != nil {
				t.Fatalf("ListCollectionTargets() error = %v", err)
			}
			if len(targets) != 1 ||
				targets[0].ConsecutiveFailures != 0 ||
				targets[0].LastError != "" {
				t.Fatalf("inactive account changed collection state: %+v", targets)
			}
		})
	}
}

func TestRunOnceKeepsCheckpointWhenCatchupRemainsBacklogged(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	if _, err := service.CreateRule(ctx, "Backlog", 7, "chat", 42, "Ops", nil); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	if _, err := service.IngestBatch(ctx, operations.IngestBatchInput{
		Source:          "test",
		AccountID:       7,
		PeerType:        "chat",
		PeerID:          42,
		PeerTitle:       "Ops",
		CheckpointAfter: 100,
		Complete:        true,
		Messages: []operations.MessageRecord{{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    42,
			PeerTitle: "Ops",
			MessageID: 100,
			SentAt:    time.Now().Add(-time.Hour),
		}},
	}); err != nil {
		t.Fatalf("seed IngestBatch() error = %v", err)
	}

	reader := fakeChatReader{collection: telegramchat.MessageCollection{
		Items: []telegramchat.Message{
			{ID: 151, Text: "newer", SentAt: time.Now()},
			{ID: 150, Text: "older", SentAt: time.Now()},
		},
		NewestID:  151,
		OldestID:  150,
		Backlog:   true,
		PagesRead: 20,
	}}
	scheduler := New(service, &fakeAccountLookup{}, reader, time.Minute, testLogger())
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	targets, err := service.ListCollectionTargets(ctx)
	if err != nil {
		t.Fatalf("ListCollectionTargets() error = %v", err)
	}
	if len(targets) != 1 || targets[0].LastMessageID != 100 || !targets[0].BacklogDetected {
		t.Fatalf("backlogged target advanced checkpoint: %+v", targets)
	}
	page, err := service.SearchMessages(ctx, operations.MessageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if page.Total != 3 {
		t.Fatalf("backlogged run indexed %d messages, want 3", page.Total)
	}
}

func TestFloodWaitOnlyBlocksAffectedAccountForCurrentRun(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	for _, accountID := range []int64{7, 8, 9} {
		if _, err := service.CreateRule(
			ctx,
			"Flood isolation",
			accountID,
			"chat",
			42,
			"Ops",
			nil,
		); err != nil {
			t.Fatalf("CreateRule(%d) error = %v", accountID, err)
		}
	}
	reader := &recordingChatReader{errByAccount: map[int64]error{
		7: &telegramchat.FloodWaitError{Wait: 5 * time.Minute},
	}}
	scheduler := New(service, &fakeAccountLookup{}, reader, time.Minute, testLogger())
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return now }
	if err := scheduler.RunOnce(ctx); !errors.Is(err, telegramchat.ErrFloodWait) {
		t.Fatalf("RunOnce() error = %v, want FloodWait", err)
	}
	if !sameAccountCalls(reader.calls, 7, 8, 9) {
		t.Fatalf("collection calls = %+v", reader.calls)
	}
	targets, err := service.ListCollectionTargets(ctx)
	if err != nil {
		t.Fatalf("ListCollectionTargets() error = %v", err)
	}
	for _, target := range targets {
		switch target.AccountID {
		case 7:
			if target.ConsecutiveFailures != 1 ||
				!target.FloodWait ||
				target.NextCollectionAt == nil ||
				!target.NextCollectionAt.Equal(now.Add(5*time.Minute)) {
				t.Fatalf("flooded target = %+v", target)
			}
		case 8, 9:
			if target.ConsecutiveFailures != 0 || target.LastSuccessfulAt == nil {
				t.Fatalf("healthy target = %+v", target)
			}
		}
	}
	reader = &recordingChatReader{errByAccount: reader.errByAccount}
	scheduler = New(service, &fakeAccountLookup{}, reader, time.Minute, testLogger())
	scheduler.now = func() time.Time { return now }
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("cooldown RunOnce() error = %v", err)
	}
	if !sameAccountCalls(reader.calls, 8, 9) {
		t.Fatalf("cooldown collection calls = %+v", reader.calls)
	}
	now = now.Add(6 * time.Minute)
	reader = &recordingChatReader{errByAccount: reader.errByAccount}
	scheduler = New(service, &fakeAccountLookup{}, reader, time.Minute, testLogger())
	scheduler.now = func() time.Time { return now }
	if err := scheduler.RunOnce(ctx); !errors.Is(err, telegramchat.ErrFloodWait) {
		t.Fatalf("retry RunOnce() error = %v, want FloodWait", err)
	}
	if !sameAccountCalls(reader.calls, 7, 8, 9) {
		t.Fatalf("retry collection calls = %+v", reader.calls)
	}
}

func TestSchedulerRotatesTargetsAcrossAccountsForFairness(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	for _, rule := range []struct {
		accountID int64
		peerID    int64
	}{
		{accountID: 7, peerID: 42},
		{accountID: 7, peerID: 43},
		{accountID: 8, peerID: 99},
	} {
		if _, err := service.CreateRule(
			ctx,
			fmt.Sprintf("Target %d", rule.peerID),
			rule.accountID,
			"chat",
			rule.peerID,
			"Ops",
			nil,
		); err != nil {
			t.Fatalf("CreateRule() error = %v", err)
		}
	}
	reader := &recordingChatReader{}
	scheduler := New(service, &fakeAccountLookup{}, reader, time.Minute, testLogger())
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if len(reader.calls) != 3 {
		t.Fatalf("collection calls = %+v, want three targets", reader.calls)
	}
	counts := make(map[int64]int)
	for _, accountID := range reader.calls {
		counts[accountID]++
	}
	if counts[7] != 2 || counts[8] != 1 {
		t.Fatalf("collection calls = %+v, want account 7 twice and account 8 once", reader.calls)
	}
}

func TestSchedulerRunsAccountsConcurrentlyButSerializesEachAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service := testOperationsService(t)
	for _, rule := range []struct {
		accountID int64
		peerID    int64
	}{
		{accountID: 7, peerID: 42},
		{accountID: 7, peerID: 43},
		{accountID: 8, peerID: 99},
	} {
		if _, err := service.CreateRule(
			ctx,
			fmt.Sprintf("Target %d", rule.peerID),
			rule.accountID,
			"chat",
			rule.peerID,
			"Ops",
			nil,
		); err != nil {
			t.Fatalf("CreateRule() error = %v", err)
		}
	}
	reader := newConcurrencyChatReader()
	scheduler := New(service, &fakeAccountLookup{}, reader, time.Minute, testLogger())
	result := make(chan error, 1)
	go func() {
		result <- scheduler.RunOnce(ctx)
	}()

	entered := make(map[int64]bool)
	for len(entered) < 2 {
		select {
		case accountID := <-reader.entered:
			entered[accountID] = true
		case <-ctx.Done():
			close(reader.release)
			t.Fatalf("accounts did not run concurrently: %v", entered)
		}
	}
	if !entered[7] || !entered[8] {
		close(reader.release)
		t.Fatalf("first collection accounts = %v, want 7 and 8", entered)
	}
	close(reader.release)
	if err := <-result; err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}

	reader.mu.Lock()
	maxActive := reader.maxActive
	accountSevenMax := reader.maxByAccount[7]
	reader.mu.Unlock()
	if maxActive != 2 || accountSevenMax != 1 {
		t.Fatalf("max concurrency = %d, account 7 max = %d; want 2 and 1",
			maxActive, accountSevenMax)
	}
}

func TestSchedulerBacksOffFailedAccountWithoutBlockingOthers(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	for _, accountID := range []int64{7, 8} {
		if _, err := service.CreateRule(
			ctx,
			fmt.Sprintf("Account %d", accountID),
			accountID,
			"chat",
			42,
			"Ops",
			nil,
		); err != nil {
			t.Fatalf("CreateRule(%d) error = %v", accountID, err)
		}
	}
	now := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)
	transientErr := fmt.Errorf("%w: temporary network failure", telegramchat.ErrTelegram)
	failingReader := &recordingChatReader{errByAccount: map[int64]error{7: transientErr}}
	scheduler := New(service, &fakeAccountLookup{}, failingReader, time.Minute, testLogger())
	scheduler.now = func() time.Time { return now }
	if err := scheduler.RunOnce(ctx); !errors.Is(err, telegramchat.ErrTelegram) {
		t.Fatalf("RunOnce() error = %v, want Telegram failure", err)
	}
	if !sameAccountCalls(failingReader.calls, 7, 8) {
		t.Fatalf("collection calls = %+v, want both accounts", failingReader.calls)
	}

	targets, err := service.ListCollectionTargets(ctx)
	if err != nil {
		t.Fatalf("ListCollectionTargets() error = %v", err)
	}
	for _, target := range targets {
		switch target.AccountID {
		case 7:
			if target.AccountConsecutiveFailures != 1 ||
				target.NextCollectionAt == nil ||
				!target.NextCollectionAt.Equal(now.Add(time.Minute)) ||
				target.FloodWait {
				t.Fatalf("failed account state = %+v", target)
			}
		case 8:
			if target.AccountConsecutiveFailures != 0 ||
				target.LastSuccessfulAt == nil {
				t.Fatalf("healthy account state = %+v", target)
			}
		}
	}

	recoveringReader := &recordingChatReader{}
	scheduler = New(
		service,
		&fakeAccountLookup{},
		recoveringReader,
		time.Minute,
		testLogger(),
	)
	scheduler.now = func() time.Time { return now }
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("cooldown RunOnce() error = %v", err)
	}
	if len(recoveringReader.calls) != 1 || recoveringReader.calls[0] != 8 {
		t.Fatalf("cooldown collection calls = %+v, want only account 8", recoveringReader.calls)
	}
}

func TestSchedulerAppliesPerAccountTargetBudget(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	for _, rule := range []struct {
		accountID int64
		peerID    int64
	}{
		{accountID: 7, peerID: 42},
		{accountID: 7, peerID: 43},
		{accountID: 7, peerID: 44},
		{accountID: 7, peerID: 45},
		{accountID: 8, peerID: 99},
	} {
		if _, err := service.CreateRule(
			ctx,
			fmt.Sprintf("Target %d", rule.peerID),
			rule.accountID,
			"chat",
			rule.peerID,
			"Ops",
			nil,
		); err != nil {
			t.Fatalf("CreateRule() error = %v", err)
		}
	}
	reader := &recordingChatReader{}
	scheduler := New(service, &fakeAccountLookup{}, reader, time.Minute, testLogger())
	scheduler.targetBudget = 2
	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	counts := make(map[int64]int)
	for _, accountID := range reader.calls {
		counts[accountID]++
	}
	if counts[7] != 2 || counts[8] != 1 {
		t.Fatalf("collection calls = %+v, want account 7 budgeted twice", reader.calls)
	}
	status := scheduler.Status()
	if status.QueuedTargets != 2 {
		t.Fatalf("queued targets = %d, want 2", status.QueuedTargets)
	}
}

func sameAccountCalls(actual []int64, expected ...int64) bool {
	if len(actual) != len(expected) {
		return false
	}
	counts := make(map[int64]int, len(actual))
	for _, accountID := range actual {
		counts[accountID]++
	}
	for _, accountID := range expected {
		counts[accountID]--
		if counts[accountID] < 0 {
			return false
		}
	}
	return true
}
