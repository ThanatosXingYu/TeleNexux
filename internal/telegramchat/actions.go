package telegramchat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
)

const (
	peerCacheTTL     = time.Hour
	avatarCacheTTL   = 15 * time.Minute
	maxAvatarBytes   = 5 << 20
	maxMessageAction = 100
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)

var (
	ErrInvalidInput = errors.New("invalid Telegram chat input")
	ErrNoAvatar     = errors.New("Telegram peer has no avatar")
)

type Profile struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	PhoneNumber string `json:"phone_number,omitempty"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	FirstName   string `json:"first_name,omitempty"`
	LastName    string `json:"last_name,omitempty"`
	Bio         string `json:"bio"`
	CanEdit     bool   `json:"can_edit"`
	HasAvatar   bool   `json:"has_avatar"`
}

type UpdateProfileInput struct {
	DisplayName string `json:"display_name"`
	Username    string `json:"username"`
	Bio         string `json:"bio"`
}

type Avatar struct {
	Data        []byte
	ContentType string
}

type cachedPeer struct {
	peer      tg.InputPeerClass
	expiresAt time.Time
}

type cachedAvatar struct {
	avatar    Avatar
	expiresAt time.Time
}

func (s *Service) SearchChats(
	ctx context.Context,
	accountID int64,
	query string,
	limit int,
) (ChatPage, error) {
	if s == nil {
		return ChatPage{}, ErrNotConfigured
	}
	query = normalizeSearchQuery(query)
	if query == "" || utf8.RuneCountInString(query) > 64 {
		return ChatPage{}, fmt.Errorf("%w: search query must contain 1 to 64 characters", ErrInvalidInput)
	}
	limit = normalizeLimit(limit)
	if limit > 50 {
		limit = 50
	}

	var page ChatPage
	err := s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		result, err := client.API().ContactsSearch(ctx, &tg.ContactsSearchRequest{
			Q:     query,
			Limit: limit,
		})
		if err != nil {
			return err
		}
		users := make(map[int64]*tg.User, len(result.Users))
		for _, item := range result.Users {
			if user, ok := item.(*tg.User); ok {
				users[user.GetID()] = user
				s.cachePeer(accountID, "user", user.GetID(), user.AsInputPeer(), time.Now())
			}
		}
		chats := make(map[int64]tg.ChatClass, len(result.Chats))
		for _, item := range result.Chats {
			switch chat := item.(type) {
			case *tg.Chat:
				chats[chat.GetID()] = chat
				s.cachePeer(accountID, "chat", chat.GetID(), chat.AsInputPeer(), time.Now())
			case *tg.Channel:
				chats[chat.GetID()] = chat
				s.cachePeer(accountID, "channel", chat.GetID(), channelInputPeer(chat), time.Now())
			}
		}

		items := make([]Chat, 0, len(result.Results))
		seen := make(map[string]struct{}, len(result.Results))
		for _, rawPeer := range result.Results {
			identity, ok := identityFromPeer(rawPeer)
			if !ok {
				continue
			}
			key := fmt.Sprintf("%s:%d", identity.kind, identity.id)
			if _, exists := seen[key]; exists {
				continue
			}
			title, username := displayPeer(identity, users, chats)
			items = append(items, Chat{
				ID:        key,
				Type:      identity.kind,
				PeerID:    identity.id,
				Title:     title,
				Username:  username,
				Preview:   "用户名搜索结果",
				HasAvatar: peerHasAvatar(identity, users, chats),
			})
			seen[key] = struct{}{}
			if len(items) >= limit {
				break
			}
		}
		page = ChatPage{Items: items, Total: len(items), Limit: limit}
		return nil
	})
	return page, err
}

func normalizeSearchQuery(query string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(query), "@"))
}

func (s *Service) CurrentProfile(ctx context.Context, accountID int64) (Profile, error) {
	if s == nil {
		return Profile{}, ErrNotConfigured
	}
	var profile Profile
	err := s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		current, err := selfProfile(ctx, client, &tg.InputUserSelf{})
		if err != nil {
			return err
		}
		profile = current
		return nil
	})
	return profile, err
}

func (s *Service) UpdateCurrentProfile(
	ctx context.Context,
	accountID int64,
	input UpdateProfileInput,
) (Profile, error) {
	if s == nil {
		return Profile{}, ErrNotConfigured
	}
	firstName, lastName, username, about, err := normalizeProfileInput(input)
	if err != nil {
		return Profile{}, err
	}

	var profile Profile
	err = s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		update := &tg.AccountUpdateProfileRequest{}
		update.SetFirstName(firstName)
		update.SetLastName(lastName)
		update.SetAbout(about)
		if _, err := client.API().AccountUpdateProfile(ctx, update); err != nil {
			return fmt.Errorf("update Telegram profile: %w", err)
		}
		if username != "" {
			if _, err := client.API().AccountUpdateUsername(ctx, username); err != nil {
				return fmt.Errorf("update Telegram username: %w", err)
			}
		}
		current, err := selfProfile(ctx, client, &tg.InputUserSelf{})
		if err != nil {
			return err
		}
		profile = current
		return nil
	})
	return profile, err
}

func (s *Service) ChatProfile(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
) (Profile, error) {
	if s == nil {
		return Profile{}, ErrNotConfigured
	}
	if peerID <= 0 {
		return Profile{}, fmt.Errorf("%w: peer ID must be positive", ErrInvalidInput)
	}
	var profile Profile
	err := s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		peer, err := s.resolveInputPeer(ctx, client.API(), accountID, peerType, peerID)
		if err != nil {
			return err
		}
		switch value := peer.(type) {
		case *tg.InputPeerUser:
			var userInput tg.InputUserClass = userInputPeer(value)
			if peerID == accountID {
				userInput = &tg.InputUserSelf{}
				current, err := selfProfile(ctx, client, userInput)
				if err != nil {
					return err
				}
				profile = current
				return nil
			}
			current, err := userProfile(ctx, client.API(), userInputPeer(value))
			if err != nil {
				return err
			}
			profile = current
		case *tg.InputPeerChat, *tg.InputPeerChannel:
			full, err := fullPeer(ctx, client.API(), peer)
			if err != nil {
				return err
			}
			profile = full.profile
			profile.ID = peerID
			profile.Type = peerType
		default:
			return ErrPeerNotFound
		}
		return nil
	})
	return profile, err
}

func (s *Service) Avatar(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
) (Avatar, error) {
	if s == nil {
		return Avatar{}, ErrNotConfigured
	}
	if peerID <= 0 {
		return Avatar{}, fmt.Errorf("%w: peer ID must be positive", ErrInvalidInput)
	}
	cacheKey := fmt.Sprintf("%d:%s:%d", accountID, peerType, peerID)
	if cached, ok := s.readAvatar(cacheKey, time.Now()); ok {
		return cached, nil
	}

	var avatar Avatar
	err := s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		var photo *tg.Photo
		if peerType == "user" && peerID == accountID {
			full, err := client.API().UsersGetFullUser(ctx, &tg.InputUserSelf{})
			if err != nil {
				return err
			}
			photo = bestPhoto(full.FullUser.ProfilePhoto, full.FullUser.FallbackPhoto)
		} else {
			peer, err := s.resolveInputPeer(ctx, client.API(), accountID, peerType, peerID)
			if err != nil {
				return err
			}
			switch value := peer.(type) {
			case *tg.InputPeerUser:
				full, err := client.API().UsersGetFullUser(ctx, userInputPeer(value))
				if err != nil {
					return err
				}
				photo = bestPhoto(full.FullUser.ProfilePhoto, full.FullUser.FallbackPhoto)
			default:
				full, err := fullPeer(ctx, client.API(), peer)
				if err != nil {
					return err
				}
				photo = full.photo
			}
		}
		if photo == nil {
			return ErrNoAvatar
		}
		location, err := avatarLocation(photo)
		if err != nil {
			return err
		}
		var output limitedBuffer
		output.limit = maxAvatarBytes
		if _, err := downloader.NewDownloader().Download(client.API(), location).Stream(ctx, &output); err != nil {
			return fmt.Errorf("download Telegram avatar: %w", err)
		}
		avatar = Avatar{
			Data:        append([]byte(nil), output.Bytes()...),
			ContentType: http.DetectContentType(output.Bytes()),
		}
		return nil
	})
	if err != nil {
		return Avatar{}, err
	}
	s.writeAvatar(cacheKey, avatar, time.Now())
	return avatar, nil
}

func (s *Service) DeleteMessages(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
	messageIDs []int,
	revoke bool,
) error {
	ids, err := normalizeMessageIDs(messageIDs)
	if err != nil {
		return err
	}
	if s == nil {
		return ErrNotConfigured
	}
	return s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		peer, err := s.resolveInputPeer(ctx, client.API(), accountID, peerType, peerID)
		if err != nil {
			return err
		}
		if revoke {
			channel, isChannel := peer.(*tg.InputPeerChannel)
			if !isChannel {
				_, err = client.API().MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
					Revoke: true,
					ID:     ids,
				})
				return err
			}
			_, err = client.API().ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
				Channel: &tg.InputChannel{
					ChannelID:  channel.ChannelID,
					AccessHash: channel.AccessHash,
				},
				ID: ids,
			})
			return err
		}
		_, err = client.API().MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
			Revoke: false,
			ID:     ids,
		})
		return err
	})
}

func (s *Service) ForwardMessages(
	ctx context.Context,
	accountID int64,
	fromType string,
	fromID int64,
	toType string,
	toID int64,
	messageIDs []int,
) error {
	ids, err := normalizeMessageIDs(messageIDs)
	if err != nil {
		return err
	}
	if fromID <= 0 || toID <= 0 {
		return fmt.Errorf("%w: peer IDs must be positive", ErrInvalidInput)
	}
	if s == nil {
		return ErrNotConfigured
	}
	return s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		fromPeer, err := s.resolveInputPeer(ctx, client.API(), accountID, fromType, fromID)
		if err != nil {
			return err
		}
		toPeer, err := s.resolveInputPeer(ctx, client.API(), accountID, toType, toID)
		if err != nil {
			return err
		}
		randomIDs := make([]int64, len(ids))
		for i := range randomIDs {
			if err := binary.Read(rand.Reader, binary.LittleEndian, &randomIDs[i]); err != nil {
				return fmt.Errorf("generate Telegram forward ID: %w", err)
			}
		}
		_, err = client.API().MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
			FromPeer: fromPeer,
			ToPeer:   toPeer,
			ID:       ids,
			RandomID: randomIDs,
		})
		return err
	})
}

func (s *Service) resolveInputPeer(
	ctx context.Context,
	api *tg.Client,
	accountID int64,
	peerType string,
	peerID int64,
) (tg.InputPeerClass, error) {
	if peerID <= 0 {
		return nil, ErrPeerNotFound
	}
	if cached, ok := s.readPeer(accountID, peerType, peerID, time.Now()); ok {
		return cached, nil
	}
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
	now := time.Now()
	for _, item := range users {
		if user, ok := item.(*tg.User); ok {
			s.cachePeer(accountID, "user", user.GetID(), user.AsInputPeer(), now)
		}
	}
	for _, item := range chats {
		switch chat := item.(type) {
		case *tg.Chat:
			s.cachePeer(accountID, "chat", chat.GetID(), chat.AsInputPeer(), now)
		case *tg.Channel:
			s.cachePeer(accountID, "channel", chat.GetID(), channelInputPeer(chat), now)
		}
	}
	if peerType == "user" && peerID == accountID {
		peer := &tg.InputPeerUser{UserID: accountID}
		s.cachePeer(accountID, peerType, peerID, peer, now)
		return peer, nil
	}
	if peer, ok := s.readPeer(accountID, peerType, peerID, now); ok {
		return peer, nil
	}
	return nil, ErrPeerNotFound
}

func (s *Service) cachePeer(
	accountID int64,
	peerType string,
	peerID int64,
	peer tg.InputPeerClass,
	now time.Time,
) {
	if peer == nil || peerID <= 0 {
		return
	}
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	s.peers[peerCacheKey(accountID, peerType, peerID)] = cachedPeer{
		peer:      peer,
		expiresAt: now.Add(peerCacheTTL),
	}
}

func (s *Service) readPeer(
	accountID int64,
	peerType string,
	peerID int64,
	now time.Time,
) (tg.InputPeerClass, bool) {
	s.peerMu.RLock()
	cached, ok := s.peers[peerCacheKey(accountID, peerType, peerID)]
	s.peerMu.RUnlock()
	return cached.peer, ok && now.Before(cached.expiresAt)
}

func (s *Service) writeAvatar(key string, avatar Avatar, now time.Time) {
	s.avatarMu.Lock()
	defer s.avatarMu.Unlock()
	s.avatars[key] = cachedAvatar{avatar: avatar, expiresAt: now.Add(avatarCacheTTL)}
}

func (s *Service) readAvatar(key string, now time.Time) (Avatar, bool) {
	s.avatarMu.RLock()
	cached, ok := s.avatars[key]
	s.avatarMu.RUnlock()
	if !ok || !now.Before(cached.expiresAt) {
		return Avatar{}, false
	}
	cached.avatar.Data = append([]byte(nil), cached.avatar.Data...)
	return cached.avatar, true
}

func peerCacheKey(accountID int64, peerType string, peerID int64) string {
	return fmt.Sprintf("%d:%s:%d", accountID, peerType, peerID)
}

func channelInputPeer(channel *tg.Channel) *tg.InputPeerChannel {
	accessHash, _ := channel.GetAccessHash()
	return &tg.InputPeerChannel{
		ChannelID:  channel.GetID(),
		AccessHash: accessHash,
	}
}

func userInputPeer(user *tg.InputPeerUser) *tg.InputUser {
	return &tg.InputUser{
		UserID:     user.UserID,
		AccessHash: user.AccessHash,
	}
}

func selfProfile(
	ctx context.Context,
	client *telegram.Client,
	input tg.InputUserClass,
) (Profile, error) {
	self, err := client.Self(ctx)
	if err != nil {
		return Profile{}, err
	}
	full, err := client.API().UsersGetFullUser(ctx, input)
	if err != nil {
		return Profile{}, err
	}
	firstName, _ := self.GetFirstName()
	lastName, _ := self.GetLastName()
	username, _ := self.GetUsername()
	phone, _ := self.GetPhone()
	about, _ := full.FullUser.GetAbout()
	photo := bestPhoto(full.FullUser.ProfilePhoto, full.FullUser.FallbackPhoto)
	return Profile{
		ID:          self.GetID(),
		Type:        "user",
		PhoneNumber: phone,
		Username:    username,
		DisplayName: profileDisplayName(firstName, lastName, username, self.GetID()),
		FirstName:   firstName,
		LastName:    lastName,
		Bio:         about,
		CanEdit:     true,
		HasAvatar:   photo != nil,
	}, nil
}

func userProfile(
	ctx context.Context,
	api *tg.Client,
	input *tg.InputUser,
) (Profile, error) {
	result, err := api.UsersGetFullUser(ctx, input)
	if err != nil {
		return Profile{}, err
	}
	var user *tg.User
	for _, item := range result.Users {
		candidate, ok := item.(*tg.User)
		if ok && candidate.GetID() == input.UserID {
			user = candidate
			break
		}
	}
	if user == nil {
		return Profile{}, ErrPeerNotFound
	}
	firstName, _ := user.GetFirstName()
	lastName, _ := user.GetLastName()
	username, _ := user.GetUsername()
	phone, _ := user.GetPhone()
	about, _ := result.FullUser.GetAbout()
	photo := bestPhoto(result.FullUser.ProfilePhoto, result.FullUser.FallbackPhoto)
	return Profile{
		ID:          user.GetID(),
		Type:        "user",
		PhoneNumber: phone,
		Username:    username,
		DisplayName: profileDisplayName(firstName, lastName, username, user.GetID()),
		FirstName:   firstName,
		LastName:    lastName,
		Bio:         about,
		HasAvatar:   photo != nil,
	}, nil
}

type fullPeerDetails struct {
	profile Profile
	photo   *tg.Photo
}

func fullPeer(ctx context.Context, api *tg.Client, peer tg.InputPeerClass) (fullPeerDetails, error) {
	switch value := peer.(type) {
	case *tg.InputPeerChat:
		result, err := api.MessagesGetFullChat(ctx, value.ChatID)
		if err != nil {
			return fullPeerDetails{}, err
		}
		full, ok := result.FullChat.(*tg.ChatFull)
		if !ok {
			return fullPeerDetails{}, ErrPeerNotFound
		}
		about := full.GetAbout()
		photo := bestPhoto(full.ChatPhoto)
		title := ""
		for _, item := range result.Chats {
			if chat, ok := item.(*tg.Chat); ok && chat.GetID() == value.ChatID {
				title = chat.GetTitle()
				break
			}
		}
		return fullPeerDetails{
			profile: Profile{
				ID:          value.ChatID,
				Type:        "chat",
				DisplayName: title,
				Bio:         about,
				HasAvatar:   photo != nil,
			},
			photo: photo,
		}, nil
	case *tg.InputPeerChannel:
		result, err := api.ChannelsGetFullChannel(ctx, &tg.InputChannel{
			ChannelID:  value.ChannelID,
			AccessHash: value.AccessHash,
		})
		if err != nil {
			return fullPeerDetails{}, err
		}
		full, ok := result.FullChat.(*tg.ChannelFull)
		if !ok {
			return fullPeerDetails{}, ErrPeerNotFound
		}
		about := full.GetAbout()
		photo := bestPhoto(full.ChatPhoto)
		title := ""
		username := ""
		for _, item := range result.Chats {
			if channel, ok := item.(*tg.Channel); ok && channel.GetID() == value.ChannelID {
				title = channel.GetTitle()
				username, _ = channel.GetUsername()
				break
			}
		}
		return fullPeerDetails{
			profile: Profile{
				ID:          value.ChannelID,
				Type:        "channel",
				Username:    username,
				DisplayName: title,
				Bio:         about,
				HasAvatar:   photo != nil,
			},
			photo: photo,
		}, nil
	default:
		return fullPeerDetails{}, ErrPeerNotFound
	}
}

func bestPhoto(photos ...tg.PhotoClass) *tg.Photo {
	for _, raw := range photos {
		if photo, ok := raw.(*tg.Photo); ok {
			return photo
		}
	}
	return nil
}

func avatarLocation(photo *tg.Photo) (*tg.InputPhotoFileLocation, error) {
	bestType := ""
	bestWidth := 0
	for _, raw := range photo.GetSizes() {
		switch size := raw.(type) {
		case *tg.PhotoSize:
			if size.GetW() > bestWidth {
				bestWidth = size.GetW()
				bestType = size.GetType()
			}
		case *tg.PhotoSizeProgressive:
			if size.GetW() > bestWidth {
				bestWidth = size.GetW()
				bestType = size.GetType()
			}
		}
	}
	if bestType == "" {
		return nil, ErrNoAvatar
	}
	return &tg.InputPhotoFileLocation{
		ID:            photo.GetID(),
		AccessHash:    photo.GetAccessHash(),
		FileReference: photo.GetFileReference(),
		ThumbSize:     bestType,
	}, nil
}

func normalizeMessageIDs(values []int) ([]int, error) {
	if len(values) == 0 || len(values) > maxMessageAction {
		return nil, fmt.Errorf("%w: select between 1 and %d messages", ErrInvalidInput, maxMessageAction)
	}
	seen := make(map[int]struct{}, len(values))
	ids := make([]int, 0, len(values))
	for _, id := range values {
		if id <= 0 {
			return nil, fmt.Errorf("%w: message IDs must be positive", ErrInvalidInput)
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func profileDisplayName(firstName, lastName, username string, userID int64) string {
	name := strings.TrimSpace(strings.Join([]string{firstName, lastName}, " "))
	if name != "" {
		return name
	}
	if username != "" {
		return "@" + username
	}
	return fmt.Sprintf("用户 %d", userID)
}

func normalizeProfileInput(input UpdateProfileInput) (string, string, string, string, error) {
	displayName := strings.Join(strings.Fields(strings.TrimSpace(input.DisplayName)), " ")
	username := strings.TrimPrefix(strings.TrimSpace(input.Username), "@")
	bio := strings.TrimSpace(input.Bio)
	if displayName == "" || utf8.RuneCountInString(displayName) > 64 {
		return "", "", "", "", fmt.Errorf("%w: display name must contain 1 to 64 characters", ErrInvalidInput)
	}
	if username != "" && !usernamePattern.MatchString(username) {
		return "", "", "", "", fmt.Errorf("%w: username must contain 5 to 32 letters, digits, or underscores", ErrInvalidInput)
	}
	if utf8.RuneCountInString(bio) > 70 {
		return "", "", "", "", fmt.Errorf("%w: bio must contain at most 70 characters", ErrInvalidInput)
	}
	parts := strings.SplitN(displayName, " ", 2)
	firstName := parts[0]
	lastName := ""
	if len(parts) == 2 {
		lastName = parts[1]
	}
	return firstName, lastName, username, bio, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.Buffer.Len()+len(data) > b.limit {
		return 0, errors.New("Telegram avatar exceeds size limit")
	}
	return b.Buffer.Write(data)
}
