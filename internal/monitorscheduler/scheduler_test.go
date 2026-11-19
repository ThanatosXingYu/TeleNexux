package monitorscheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

type fakeAccountLookup struct {
	inactive map[int64]bool
}

func (f fakeAccountLookup) Get(
	_ context.Context,
	id int64,
) (account.Account, error) {
	if f.inactive[id] {
		return account.Account{}, account.ErrNotFound
	}
	return account.Account{ID: id, PurgeState: account.PurgeStateActive}, nil
}

type fakeChatReader struct {
	page telegramchat.MessagePage
	err  error
}

type chatSequence struct {
	pages []telegramchat.MessagePage
	index int
}

func (s *chatSequence) Messages(
	_ context.Context,
	_ int64,
	_ string,
	_ int64,
	_ int,
	_ int,
) (telegramchat.MessagePage, error) {
	if s.index >= len(s.pages) {
		return telegramchat.MessagePage{Items: []telegramchat.Message{}}, nil
	}
	page := s.pages[s.index]
	s.index++
	return page, nil
}

func (f fakeChatReader) Messages(
	context.Context,
	int64,
	string,
	int64,
	int,
	int,
) (telegramchat.MessagePage, error) {
	return f.page, f.err
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
	chats := &chatSequence{pages: []telegramchat.MessagePage{
		{Items: []telegramchat.Message{
			{ID: 10, Sender: "Alice", Text: "baseline urgent", SentAt: time.Now().Add(-2 * time.Minute)},
			{ID: 11, Sender: "Bob", Text: "baseline", SentAt: time.Now().Add(-time.Minute)},
		}},
		{Items: []telegramchat.Message{
			{ID: 12, Sender: "Alice", Text: "urgent deploy", SentAt: time.Now()},
			{ID: 11, Sender: "Bob", Text: "baseline", SentAt: time.Now().Add(-time.Minute)},
		}},
	}}
	scheduler := New(service, fakeAccountLookup{}, chats, time.Minute, testLogger())
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

	reader := fakeChatReader{page: telegramchat.MessagePage{Items: []telegramchat.Message{
		{ID: 100, Sender: "Alice", Text: "baseline", SentAt: now.Add(-time.Minute)},
		{ID: 101, Sender: "Alice", Text: "scheduled update", MediaType: "photo", SentAt: now},
	}}}
	scheduler := New(service, fakeAccountLookup{}, reader, time.Minute, testLogger())
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
		fakeAccountLookup{},
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

func TestRunOnceSkipsAccountsPendingDeletion(t *testing.T) {
	ctx := context.Background()
	service := testOperationsService(t)
	if _, err := service.CreateRule(ctx, "Deleting", 9, "channel", 99, "Archive", nil); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	reader := &chatSequence{}
	scheduler := New(
		service,
		fakeAccountLookup{inactive: map[int64]bool{9: true}},
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
