package telegramchat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestUpdateTouchesMessages(t *testing.T) {
	tests := []struct {
		name  string
		value tg.UpdatesClass
		want  bool
	}{
		{
			name: "new message container",
			value: &tg.Updates{Updates: []tg.UpdateClass{
				&tg.UpdateNewMessage{},
			}},
			want: true,
		},
		{
			name:  "short delete",
			value: &tg.UpdateShort{Update: &tg.UpdateDeleteMessages{}},
			want:  true,
		},
		{
			name:  "typing ignored",
			value: &tg.UpdateShort{Update: &tg.UpdateUserTyping{}},
			want:  false,
		},
		{
			name:  "updates too long",
			value: &tg.UpdatesTooLong{},
			want:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := updateTouchesMessages(test.value); got != test.want {
				t.Fatalf("updateTouchesMessages() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestObserveUpdateQueuesOnlyMessageActivities(t *testing.T) {
	service, err := NewService(1, "test-hash", t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	defer service.Close()

	service.observeUpdate(7, &tg.UpdateShort{Update: &tg.UpdateUserTyping{}})
	select {
	case accountID := <-service.Updates():
		t.Fatalf("typing update queued account %d", accountID)
	default:
	}

	service.observeUpdate(7, &tg.UpdateShort{Update: &tg.UpdateDeleteMessages{}})
	select {
	case accountID := <-service.Updates():
		if accountID != 7 {
			t.Fatalf("queued account ID = %d, want 7", accountID)
		}
	case <-time.After(time.Second):
		t.Fatal("message update was not queued")
	}
	statuses := service.ListenerStatuses()
	if len(statuses) != 1 || statuses[0].EventsObserved != 1 ||
		statuses[0].LastEventAt == nil {
		t.Fatalf("ListenerStatuses() = %+v", statuses)
	}
}

func TestObserveUpdateDoesNotBlockOnUnreadDeletionQueue(t *testing.T) {
	service, err := NewService(1, "test-hash", t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	defer service.Close()

	service.deletionsMu.Lock()
	for range cap(service.deletions) {
		service.deletionPending = append(service.deletionPending, DeletionNotice{
			AccountID:  7,
			PeerType:   "unknown",
			MessageIDs: []int{1},
		})
	}
	service.deletionsMu.Unlock()

	done := make(chan struct{})
	go func() {
		service.observeUpdate(7, &tg.UpdateShort{Update: &tg.UpdateDeleteMessages{
			Messages: []int{9},
		}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observeUpdate blocked on an unread deletion queue")
	}
	statuses := service.ListenerStatuses()
	if len(statuses) != 1 || statuses[0].QueuedDeletionEvents == 0 {
		t.Fatalf("ListenerStatuses() = %+v, want queued deletion events", statuses)
	}
}

func TestCloseStopsDeletionDelivery(t *testing.T) {
	service, err := NewService(1, "test-hash", t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	done := make(chan struct{})
	go func() {
		service.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close() did not stop deletion delivery")
	}
	select {
	case _, ok := <-service.Deletions():
		if ok {
			t.Fatal("Deletions() remained open after Close()")
		}
	default:
		t.Fatal("Deletions() was not closed")
	}
}

func TestObserveGapRequestsCatchup(t *testing.T) {
	service, err := NewService(1, "test-hash", t.TempDir())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	defer service.Close()

	service.observeGap(9)
	select {
	case accountID := <-service.Updates():
		if accountID != 9 {
			t.Fatalf("queued account ID = %d, want 9", accountID)
		}
	case <-time.After(time.Second):
		t.Fatal("gap recovery did not queue a catch-up")
	}
	statuses := service.ListenerStatuses()
	if len(statuses) != 1 || statuses[0].GapRecoveryCount != 1 {
		t.Fatalf("ListenerStatuses() = %+v", statuses)
	}
}

func TestDeletionNoticesPreserveProtocolPeerContext(t *testing.T) {
	tests := []struct {
		name  string
		value tg.UpdatesClass
		want  DeletionNotice
	}{
		{
			name: "private or chat deletion lacks peer",
			value: &tg.UpdateShort{Update: &tg.UpdateDeleteMessages{
				Messages: []int{7, 8},
			}},
			want: DeletionNotice{
				AccountID:  9,
				PeerType:   "unknown",
				MessageIDs: []int{7, 8},
			},
		},
		{
			name: "channel deletion retains channel ID",
			value: &tg.Updates{
				Updates: []tg.UpdateClass{
					&tg.UpdateDeleteChannelMessages{
						ChannelID: 42,
						Messages:  []int{11},
					},
				},
			},
			want: DeletionNotice{
				AccountID:  9,
				PeerType:   "channel",
				PeerID:     42,
				MessageIDs: []int{11},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			notices := deletionNotices(9, test.value)
			if len(notices) != 1 {
				t.Fatalf("deletionNotices() = %+v", notices)
			}
			got := notices[0]
			if got.AccountID != test.want.AccountID ||
				got.PeerType != test.want.PeerType ||
				got.PeerID != test.want.PeerID ||
				len(got.MessageIDs) != len(test.want.MessageIDs) {
				t.Fatalf("deletionNotices() = %+v, want %+v", got, test.want)
			}
			for index := range got.MessageIDs {
				if got.MessageIDs[index] != test.want.MessageIDs[index] {
					t.Fatalf("deletionNotices() = %+v, want %+v", got, test.want)
				}
			}
		})
	}
}

func TestClassifyTelegramErrorPreservesDomainErrors(t *testing.T) {
	service := &Service{}
	if got := service.classifyTelegramError(context.Canceled); !errors.Is(got, ErrTimeout) {
		t.Fatalf("classifyTelegramError(context.Canceled) = %v, want timeout", got)
	}
}
