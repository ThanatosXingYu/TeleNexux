// Package telegramchat reads chats and messages from authorized Telegram sessions.
package telegramchat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/telegramauth"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

var (
	ErrNotConfigured  = errors.New("Telegram chat service is not configured")
	ErrSessionMissing = errors.New("Telegram account session is missing")
	ErrSessionInvalid = errors.New("Telegram account session is invalid")
	ErrPeerNotFound   = errors.New("Telegram chat was not found")
	ErrTelegram       = errors.New("Telegram request failed")
)

type Chat struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"`
	PeerID      int64     `json:"peer_id"`
	Title       string    `json:"title"`
	Username    string    `json:"username,omitempty"`
	Preview     string    `json:"preview"`
	LastMessage time.Time `json:"last_message_at"`
	UnreadCount int       `json:"unread_count"`
	Pinned      bool      `json:"pinned"`
	HasAvatar   bool      `json:"has_avatar"`
}

type ChatPage struct {
	Items []Chat `json:"items"`
	Total int    `json:"total"`
	Limit int    `json:"limit"`
}

type Message struct {
	ID        int       `json:"id"`
	Sender    string    `json:"sender"`
	SenderID  int64     `json:"sender_id,omitempty"`
	Text      string    `json:"text"`
	MediaType string    `json:"media_type,omitempty"`
	SentAt    time.Time `json:"sent_at"`
	Outgoing  bool      `json:"outgoing"`
}

type MessagePage struct {
	Items        []Message `json:"items"`
	Total        int       `json:"total"`
	Limit        int       `json:"limit"`
	NextBeforeID int       `json:"next_before_id,omitempty"`
}

type Service struct {
	appID            int
	appHash          string
	sessionDirectory string
	operationTimeout time.Duration
	peerMu           sync.RWMutex
	peers            map[string]cachedPeer
	avatarMu         sync.RWMutex
	avatars          map[string]cachedAvatar
}

func (s *Service) ValidateSession(ctx context.Context, accountID int64) error {
	if s == nil {
		return ErrNotConfigured
	}
	return s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		if _, err := client.Self(ctx); err != nil {
			return err
		}
		return nil
	})
}

// DeleteSession removes the local authorization files for an account without
// changing the Telegram account itself.
func (s *Service) DeleteSession(accountID int64) error {
	if s == nil {
		return ErrNotConfigured
	}
	if accountID <= 0 {
		return ErrSessionMissing
	}
	path := filepath.Join(s.sessionDirectory, "accounts", fmt.Sprintf("%d.session", accountID))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Telegram account session: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse to delete unsafe Telegram session path")
	}
	for _, candidate := range []string{path + ".device", path} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete Telegram session file: %w", err)
		}
	}
	return nil
}

func NewService(appID int, appHash, sessionDirectory string) (*Service, error) {
	if appID <= 0 || strings.TrimSpace(appHash) == "" {
		return nil, ErrNotConfigured
	}
	sessionDirectory = strings.TrimSpace(sessionDirectory)
	if sessionDirectory == "" {
		return nil, errors.New("Telegram session directory must not be empty")
	}
	return &Service{
		appID:            appID,
		appHash:          strings.TrimSpace(appHash),
		sessionDirectory: sessionDirectory,
		operationTimeout: 30 * time.Second,
		peers:            make(map[string]cachedPeer),
		avatars:          make(map[string]cachedAvatar),
	}, nil
}

func (s *Service) Chats(ctx context.Context, accountID int64, limit int) (ChatPage, error) {
	if s == nil {
		return ChatPage{}, ErrNotConfigured
	}
	limit = normalizeLimit(limit)
	var page ChatPage
	err := s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		result, err := client.API().MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      limit,
		})
		if err != nil {
			return err
		}
		s.cacheDialogsResult(accountID, result, time.Now())
		page, err = mapDialogs(result, limit)
		return err
	})
	if err != nil {
		return ChatPage{}, err
	}
	return page, nil
}

func (s *Service) Messages(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
	beforeID int,
	limit int,
) (MessagePage, error) {
	if s == nil {
		return MessagePage{}, ErrNotConfigured
	}
	if peerID <= 0 || beforeID < 0 {
		return MessagePage{}, ErrPeerNotFound
	}
	limit = normalizeLimit(limit)
	var page MessagePage
	err := s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		inputPeer, err := s.resolveInputPeer(ctx, client.API(), accountID, peerType, peerID)
		if err != nil {
			return err
		}
		result, err := client.API().MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:     inputPeer,
			OffsetID: beforeID,
			Limit:    limit,
		})
		if err != nil {
			return err
		}
		s.cacheMessagesResult(accountID, result, time.Now())
		page, err = mapMessages(result, limit)
		return err
	})
	if err != nil {
		return MessagePage{}, err
	}
	return page, nil
}

func (s *Service) withClient(
	ctx context.Context,
	accountID int64,
	run func(context.Context, *telegram.Client) error,
) error {
	sessionPath, err := s.sessionPath(accountID)
	if err != nil {
		return err
	}
	if err := telegramauth.PrepareDeviceSession(sessionPath); err != nil {
		return fmt.Errorf("prepare Telegram device: %w", err)
	}
	operationCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()

	client := telegram.NewClient(s.appID, s.appHash, telegram.Options{
		SessionStorage:  &session.FileStorage{Path: sessionPath},
		Device:          telegramauth.DeviceForSession(sessionPath),
		DialTimeout:     15 * time.Second,
		ExchangeTimeout: 15 * time.Second,
		NoUpdates:       true,
	})
	if err := client.Run(operationCtx, func(ctx context.Context) error {
		return run(ctx, client)
	}); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%w: request timed out", ErrTelegram)
		}
		if isInvalidSessionError(err) {
			return fmt.Errorf("%w: %v", ErrSessionInvalid, err)
		}
		return fmt.Errorf("%w: %v", ErrTelegram, err)
	}
	return nil
}

func isInvalidSessionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(err.Error())
	for _, marker := range []string{
		"AUTH_KEY_UNREGISTERED",
		"AUTH_KEY_INVALID",
		"SESSION_REVOKED",
		"USER_DEACTIVATED",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (s *Service) sessionPath(accountID int64) (string, error) {
	if accountID <= 0 {
		return "", ErrSessionMissing
	}
	path := filepath.Join(s.sessionDirectory, "accounts", fmt.Sprintf("%d.session", accountID))
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrSessionMissing
	}
	if err != nil {
		return "", fmt.Errorf("inspect Telegram account session: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrSessionMissing
	}
	return path, nil
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}
