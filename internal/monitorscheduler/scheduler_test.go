package monitorscheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

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

func TestSchedulerEstablishesBaselineBeforeIndexingNewMessages(t *testing.T) {
	ctx := context.Background()
	store, err := operations.Open(ctx, filepath.Join(t.TempDir(), "scheduler.db"))
	if err != nil {
		t.Fatalf("operations.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("operations.Close() error = %v", err)
		}
	})
	rule, err := store.CreateRule(ctx, "Ops", 7, "chat", 42, "Operations", []string{"urgent"})
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
	scheduler := New(store, chats, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("first RunOnce() error = %v", err)
	}
	page, err := store.SearchMessages(ctx, operations.MessageQuery{Limit: 20})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("first run indexed %d historical messages, want 0", page.Total)
	}
	updated, err := store.GetRule(ctx, rule.ID)
	if err != nil {
		t.Fatalf("GetRule() error = %v", err)
	}
	if updated.LastMessageID != 11 {
		t.Fatalf("LastMessageID = %d, want 11", updated.LastMessageID)
	}

	if err := scheduler.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce() error = %v", err)
	}
	page, err = store.SearchMessages(ctx, operations.MessageQuery{Query: "deploy", Limit: 20})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("second run indexed %d new messages, want 1", page.Total)
	}
	alerts, err := store.ListAlerts(ctx, "pending", 20)
	if err != nil {
		t.Fatalf("ListAlerts() error = %v", err)
	}
	if len(alerts) != 1 || alerts[0].MessageID != 12 {
		t.Fatalf("ListAlerts() = %+v", alerts)
	}
}
