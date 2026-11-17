package telegramchat

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestMapDialogsBuildsStableChatItems(t *testing.T) {
	user := &tg.User{ID: 42, FirstName: "Ada", LastName: "Lovelace", Username: "ada"}
	user.SetFlags()
	channel := &tg.Channel{ID: 84, Title: "Telegram News", Username: "telegram"}
	channel.SetFlags()
	message := &tg.Message{
		ID:      7,
		PeerID:  &tg.PeerUser{UserID: 42},
		Message: "hello",
		Date:    1_700_000_000,
	}
	message.SetFlags()
	userDialog := &tg.Dialog{Peer: &tg.PeerUser{UserID: 42}, UnreadCount: 3, Pinned: true}
	userDialog.SetFlags()
	result := &tg.MessagesDialogs{
		Dialogs: []tg.DialogClass{
			userDialog,
			&tg.Dialog{Peer: &tg.PeerChannel{ChannelID: 84}},
		},
		Messages: []tg.MessageClass{message},
		Users:    []tg.UserClass{user},
		Chats:    []tg.ChatClass{channel},
	}

	page, err := mapDialogs(result, 50)
	if err != nil {
		t.Fatalf("mapDialogs() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(page.Items))
	}
	if page.Items[0].ID != "user:42" || page.Items[0].Title != "Ada Lovelace" {
		t.Fatalf("user dialog = %+v", page.Items[0])
	}
	if page.Items[0].Preview != "hello" || page.Items[0].UnreadCount != 3 || !page.Items[0].Pinned {
		t.Fatalf("user message metadata = %+v", page.Items[0])
	}
	if page.Items[1].ID != "channel:84" || page.Items[1].Title != "Telegram News" {
		t.Fatalf("channel dialog = %+v", page.Items[1])
	}
}

func TestMapMessagesSortsNewestFirstAndPaginates(t *testing.T) {
	user := &tg.User{ID: 42, FirstName: "Ada"}
	user.SetFlags()
	older := &tg.Message{ID: 5, FromID: &tg.PeerUser{UserID: 42}, Message: "older"}
	older.SetFlags()
	newer := &tg.Message{ID: 8, FromID: &tg.PeerUser{UserID: 42}, Message: "newer"}
	newer.SetFlags()
	result := &tg.MessagesMessages{
		Messages: []tg.MessageClass{
			older,
			newer,
		},
		Users: []tg.UserClass{user},
	}

	page, err := mapMessages(result, 2)
	if err != nil {
		t.Fatalf("mapMessages() error = %v", err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != 8 || page.Items[1].ID != 5 {
		t.Fatalf("message order = %+v", page.Items)
	}
	if page.Items[0].Sender != "Ada" || page.NextBeforeID != 5 {
		t.Fatalf("message page = %+v", page)
	}
}
