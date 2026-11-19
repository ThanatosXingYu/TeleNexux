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

func TestClassifyTelegramErrorPreservesDomainErrors(t *testing.T) {
	service := &Service{}
	if got := service.classifyTelegramError(context.Canceled); !errors.Is(got, ErrTimeout) {
		t.Fatalf("classifyTelegramError(context.Canceled) = %v, want timeout", got)
	}
}
