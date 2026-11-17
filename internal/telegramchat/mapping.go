package telegramchat

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

type peerIdentity struct {
	kind string
	id   int64
}

func mapDialogs(result tg.MessagesDialogsClass, limit int) (ChatPage, error) {
	var (
		dialogs  []tg.DialogClass
		messages []tg.MessageClass
		chats    []tg.ChatClass
		users    []tg.UserClass
		total    int
	)
	switch value := result.(type) {
	case *tg.MessagesDialogs:
		dialogs, messages, chats, users = value.Dialogs, value.Messages, value.Chats, value.Users
		total = len(dialogs)
	case *tg.MessagesDialogsSlice:
		dialogs, messages, chats, users = value.Dialogs, value.Messages, value.Chats, value.Users
		total = value.Count
	default:
		return ChatPage{}, fmt.Errorf("%w: unsupported dialog response %T", ErrTelegram, result)
	}

	usersByID := make(map[int64]*tg.User, len(users))
	for _, item := range users {
		if user, ok := item.(*tg.User); ok {
			usersByID[user.GetID()] = user
		}
	}
	chatsByID := make(map[int64]tg.ChatClass, len(chats))
	for _, item := range chats {
		switch chat := item.(type) {
		case *tg.Chat:
			chatsByID[chat.GetID()] = chat
		case *tg.Channel:
			chatsByID[chat.GetID()] = chat
		}
	}
	lastByPeer := make(map[peerIdentity]tg.MessageClass, len(messages))
	for _, message := range messages {
		if identity, ok := identityFromPeer(messagePeer(message)); ok {
			lastByPeer[identity] = message
		}
	}

	items := make([]Chat, 0, len(dialogs))
	for _, item := range dialogs {
		dialog, ok := item.(*tg.Dialog)
		if !ok {
			continue
		}
		identity, ok := identityFromPeer(dialog.GetPeer())
		if !ok {
			continue
		}
		title, username := displayPeer(identity, usersByID, chatsByID)
		preview, sentAt := messageSummary(lastByPeer[identity])
		items = append(items, Chat{
			ID:          fmt.Sprintf("%s:%d", identity.kind, identity.id),
			Type:        identity.kind,
			PeerID:      identity.id,
			Title:       title,
			Username:    username,
			Preview:     preview,
			LastMessage: sentAt,
			UnreadCount: dialog.GetUnreadCount(),
			Pinned:      dialog.GetPinned(),
			HasAvatar:   peerHasAvatar(identity, usersByID, chatsByID),
		})
	}
	return ChatPage{Items: items, Total: total, Limit: limit}, nil
}

func mapMessages(result tg.MessagesMessagesClass, limit int) (MessagePage, error) {
	var (
		messages []tg.MessageClass
		users    []tg.UserClass
		total    int
	)
	switch value := result.(type) {
	case *tg.MessagesMessages:
		messages, users = value.Messages, value.Users
		total = len(messages)
	case *tg.MessagesMessagesSlice:
		messages, users = value.Messages, value.Users
		total = value.Count
	case *tg.MessagesChannelMessages:
		messages, users = value.Messages, value.Users
		total = value.Count
	default:
		return MessagePage{}, fmt.Errorf("%w: unsupported message response %T", ErrTelegram, result)
	}

	usersByID := make(map[int64]*tg.User, len(users))
	for _, item := range users {
		if user, ok := item.(*tg.User); ok {
			usersByID[user.GetID()] = user
		}
	}
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].GetID() > messages[j].GetID()
	})

	items := make([]Message, 0, len(messages))
	for _, item := range messages {
		switch message := item.(type) {
		case *tg.Message:
			sender := "未知用户"
			var senderID int64
			if from, ok := message.GetFromID(); ok {
				sender = senderName(from, usersByID)
				if identity, ok := identityFromPeer(from); ok && identity.kind == "user" {
					senderID = identity.id
				}
			}
			items = append(items, Message{
				ID:        message.GetID(),
				Sender:    sender,
				SenderID:  senderID,
				Text:      message.GetMessage(),
				MediaType: mediaType(message),
				SentAt:    unixTime(message.GetDate()),
				Outgoing:  message.GetOut(),
			})
		case *tg.MessageService:
			items = append(items, Message{
				ID:       message.GetID(),
				Sender:   "系统",
				Text:     "服务消息",
				SentAt:   unixTime(message.GetDate()),
				Outgoing: message.GetOut(),
			})
		}
	}
	page := MessagePage{Items: items, Total: total, Limit: limit}
	if len(items) == limit && len(items) > 0 {
		page.NextBeforeID = items[len(items)-1].ID
	}
	return page, nil
}

func resolveInputPeer(
	ctx context.Context,
	api *tg.Client,
	peerType string,
	peerID int64,
) (tg.InputPeerClass, error) {
	result, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
		OffsetPeer: &tg.InputPeerEmpty{},
		Limit:      100,
	})
	if err != nil {
		return nil, err
	}
	var chats []tg.ChatClass
	var users []tg.UserClass
	switch value := result.(type) {
	case *tg.MessagesDialogs:
		chats, users = value.Chats, value.Users
	case *tg.MessagesDialogsSlice:
		chats, users = value.Chats, value.Users
	default:
		return nil, ErrPeerNotFound
	}

	switch peerType {
	case "user":
		for _, item := range users {
			if user, ok := item.(*tg.User); ok && user.GetID() == peerID {
				return user.AsInputPeer(), nil
			}
		}
	case "chat":
		for _, item := range chats {
			if chat, ok := item.(*tg.Chat); ok && chat.GetID() == peerID {
				return chat.AsInputPeer(), nil
			}
		}
	case "channel":
		for _, item := range chats {
			if channel, ok := item.(*tg.Channel); ok && channel.GetID() == peerID {
				return channel.AsInputPeer(), nil
			}
		}
	}
	return nil, ErrPeerNotFound
}

func messagePeer(message tg.MessageClass) tg.PeerClass {
	switch value := message.(type) {
	case *tg.Message:
		return value.GetPeerID()
	case *tg.MessageService:
		return value.GetPeerID()
	case *tg.MessageEmpty:
		peer, _ := value.GetPeerID()
		return peer
	default:
		return nil
	}
}

func identityFromPeer(peer tg.PeerClass) (peerIdentity, bool) {
	switch value := peer.(type) {
	case *tg.PeerUser:
		return peerIdentity{kind: "user", id: value.GetUserID()}, true
	case *tg.PeerChat:
		return peerIdentity{kind: "chat", id: value.GetChatID()}, true
	case *tg.PeerChannel:
		return peerIdentity{kind: "channel", id: value.GetChannelID()}, true
	default:
		return peerIdentity{}, false
	}
}

func displayPeer(
	identity peerIdentity,
	users map[int64]*tg.User,
	chats map[int64]tg.ChatClass,
) (string, string) {
	switch identity.kind {
	case "user":
		if user := users[identity.id]; user != nil {
			username, _ := user.GetUsername()
			return userDisplayName(user), username
		}
	case "chat":
		if chat, ok := chats[identity.id].(*tg.Chat); ok {
			return fallback(chat.GetTitle(), "未命名群组"), ""
		}
	case "channel":
		if channel, ok := chats[identity.id].(*tg.Channel); ok {
			username, _ := channel.GetUsername()
			return fallback(channel.GetTitle(), "未命名频道"), username
		}
	}
	return "未命名会话", ""
}

func peerHasAvatar(
	identity peerIdentity,
	users map[int64]*tg.User,
	chats map[int64]tg.ChatClass,
) bool {
	switch identity.kind {
	case "user":
		if user := users[identity.id]; user != nil {
			photo, ok := user.GetPhoto()
			if !ok {
				return false
			}
			_, visible := photo.(*tg.UserProfilePhoto)
			return visible
		}
	case "chat":
		if chat, ok := chats[identity.id].(*tg.Chat); ok {
			_, visible := chat.GetPhoto().(*tg.ChatPhoto)
			return visible
		}
	case "channel":
		if channel, ok := chats[identity.id].(*tg.Channel); ok {
			_, visible := channel.GetPhoto().(*tg.ChatPhoto)
			return visible
		}
	}
	return false
}

func senderName(peer tg.PeerClass, users map[int64]*tg.User) string {
	identity, ok := identityFromPeer(peer)
	if !ok || identity.kind != "user" {
		return "未知用户"
	}
	if user := users[identity.id]; user != nil {
		return userDisplayName(user)
	}
	return "未知用户"
}

func userDisplayName(user *tg.User) string {
	first, _ := user.GetFirstName()
	last, _ := user.GetLastName()
	name := strings.TrimSpace(strings.Join([]string{first, last}, " "))
	if name != "" {
		return name
	}
	if username, _ := user.GetUsername(); username != "" {
		return "@" + username
	}
	return fmt.Sprintf("用户 %d", user.GetID())
}

func messageSummary(message tg.MessageClass) (string, time.Time) {
	switch value := message.(type) {
	case *tg.Message:
		text := strings.TrimSpace(value.GetMessage())
		if text == "" {
			text = mediaType(value)
		}
		return fallback(text, "空消息"), unixTime(value.GetDate())
	case *tg.MessageService:
		return "服务消息", unixTime(value.GetDate())
	default:
		return "", time.Time{}
	}
}

func mediaType(message *tg.Message) string {
	media, ok := message.GetMedia()
	if !ok {
		return ""
	}
	name := fmt.Sprintf("%T", media)
	switch {
	case strings.Contains(name, "Photo"):
		return "图片"
	case strings.Contains(name, "Document"):
		return "文件"
	case strings.Contains(name, "Geo"):
		return "位置"
	case strings.Contains(name, "Contact"):
		return "联系人"
	case strings.Contains(name, "Poll"):
		return "投票"
	case strings.Contains(name, "WebPage"):
		return "链接预览"
	default:
		return "媒体消息"
	}
}

func unixTime(seconds int) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(seconds), 0).UTC()
}

func fallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}
