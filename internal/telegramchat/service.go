// Package telegramchat reads chats and messages from authorized Telegram sessions.
package telegramchat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/accountproxy"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

var (
	ErrNotConfigured  = errors.New("Telegram chat service is not configured")
	ErrSessionMissing = errors.New("Telegram account session is missing")
	ErrSessionInvalid = errors.New("Telegram account session is invalid")
	ErrPeerNotFound   = errors.New("Telegram chat was not found")
	ErrFloodWait      = errors.New("Telegram request is rate limited")
	ErrTimeout        = errors.New("Telegram request timed out")
	ErrTelegram       = errors.New("Telegram request failed")
)

type FloodWaitError struct {
	Wait time.Duration
}

func (e *FloodWaitError) Error() string {
	return fmt.Sprintf("%s: retry after %s", ErrFloodWait, e.Wait)
}

func (e *FloodWaitError) Unwrap() error {
	return ErrFloodWait
}

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
	ID        int             `json:"id"`
	Sender    string          `json:"sender"`
	SenderID  int64           `json:"sender_id,omitempty"`
	Text      string          `json:"text"`
	MediaType string          `json:"media_type,omitempty"`
	Media     *MediaReference `json:"media,omitempty"`
	SentAt    time.Time       `json:"sent_at"`
	Outgoing  bool            `json:"outgoing"`
}

type MediaReference struct {
	Type     string `json:"type,omitempty"`
	Kind     string `json:"kind,omitempty"`
	ID       int64  `json:"id,omitempty"`
	DCID     int    `json:"dc_id,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	Spoiler  bool   `json:"spoiler,omitempty"`
	Video    bool   `json:"video,omitempty"`
	Voice    bool   `json:"voice,omitempty"`
	Round    bool   `json:"round,omitempty"`
	// Telegram file locators remain backend-only and must not reach browser responses.
	AccessHash    int64  `json:"-"`
	FileReference []byte `json:"-"`
}

type MessagePage struct {
	Items        []Message `json:"items"`
	Total        int       `json:"total"`
	Limit        int       `json:"limit"`
	NextBeforeID int       `json:"next_before_id,omitempty"`
}

// DeletionNotice carries the minimum stable fields from a Telegram deletion
// update. An unknown peer type means the update did not identify a chat.
type DeletionNotice struct {
	AccountID  int64  `json:"account_id"`
	PeerType   string `json:"peer_type"`
	PeerID     int64  `json:"peer_id"`
	MessageIDs []int  `json:"message_ids"`
}

type Service struct {
	appID            int
	appHash          string
	sessionDirectory string
	operationTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	workersMu sync.Mutex
	workers   map[int64]*accountWorker
	workersWG sync.WaitGroup
	closing   bool
	closeOnce sync.Once

	listenerMu sync.RWMutex
	listeners  map[int64]ListenerStatus
	updates    chan int64
	deletions  chan DeletionNotice

	deletionsMu     sync.Mutex
	deletionsClosed bool
	deletionWake    chan struct{}
	deletionPending []DeletionNotice
	deletionDone    chan struct{}

	peerMu   sync.RWMutex
	peers    map[string]cachedPeer
	avatarMu sync.RWMutex
	avatars  map[string]cachedAvatar

	proxyProvider accountproxy.DialProvider
}

func (s *Service) ValidateSession(ctx context.Context, accountID int64) error {
	if s == nil {
		return ErrNotConfigured
	}
	return s.withClient(ctx, accountID, func(ctx context.Context, client *telegram.Client) error {
		_, err := client.Self(ctx)
		return err
	})
}

// InspectSession verifies the Telegram authorization and returns fresh profile
// metadata so account refreshes can update the local projection atomically.
func (s *Service) InspectSession(ctx context.Context, accountID int64) (Profile, error) {
	return s.CurrentProfile(ctx, accountID)
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
	s.ResetAccount(accountID)
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
	return NewServiceWithProxy(appID, appHash, sessionDirectory, nil)
}

func NewServiceWithProxy(
	appID int,
	appHash string,
	sessionDirectory string,
	proxyProvider accountproxy.DialProvider,
) (*Service, error) {
	if appID <= 0 || strings.TrimSpace(appHash) == "" {
		return nil, ErrNotConfigured
	}
	sessionDirectory = strings.TrimSpace(sessionDirectory)
	if sessionDirectory == "" {
		return nil, errors.New("Telegram session directory must not be empty")
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		appID:            appID,
		appHash:          strings.TrimSpace(appHash),
		sessionDirectory: sessionDirectory,
		operationTimeout: 30 * time.Second,
		ctx:              ctx,
		cancel:           cancel,
		workers:          make(map[int64]*accountWorker),
		listeners:        make(map[int64]ListenerStatus),
		updates:          make(chan int64, 64),
		deletions:        make(chan DeletionNotice, 64),
		peers:            make(map[string]cachedPeer),
		avatars:          make(map[string]cachedAvatar),
		proxyProvider:    proxyProvider,
		deletionWake:     make(chan struct{}, 1),
		deletionDone:     make(chan struct{}),
	}
	go service.deliverDeletionNotices()
	return service, nil
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
	if s == nil {
		return ErrNotConfigured
	}
	if _, err := s.sessionPath(accountID); err != nil {
		return err
	}
	worker, err := s.accountWorker(accountID)
	if err != nil {
		return err
	}
	operationCtx, cancel := context.WithTimeout(ctx, s.operationTimeout)
	defer cancel()
	return worker.submit(operationCtx, run)
}

func isInvalidSessionError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToUpper(err.Error())
	for _, marker := range []string{
		"AUTH_KEY_UNREGISTERED",
		"AUTH_KEY_INVALID",
		"AUTH_KEY_NOT_FOUND",
		"AUTH_KEY_DUPLICATED",
		"AUTH_BYTES_INVALID",
		"SESSION_REVOKED",
		"SESSION_EXPIRED",
		"SESSION_PASSWORD_NEEDED",
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

// ResetAccount closes the account's active Telegram connection so a newly
// authorized session can be opened safely by the next request.
func (s *Service) ResetAccount(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.workersMu.Lock()
	worker := s.workers[accountID]
	delete(s.workers, accountID)
	s.workersMu.Unlock()
	if worker == nil {
		return
	}
	worker.cancel()
	<-worker.exited
}

// Close stops all account workers and waits for their connections to exit.
func (s *Service) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.workersMu.Lock()
		s.closing = true
		s.cancel()
		s.workersMu.Unlock()
		s.workersWG.Wait()
		s.deletionsMu.Lock()
		s.deletionsClosed = true
		s.deletionsMu.Unlock()
		<-s.deletionDone
	})
}

// Updates emits an account ID when a message-related Telegram update is
// observed. Events are coalesced by consumers and never contain message data.
func (s *Service) Updates() <-chan int64 {
	if s == nil {
		return nil
	}
	return s.updates
}

// Deletions emits a deletion notice without message content or reusable
// Telegram file locators. Consumers persist notices before resolving them.
func (s *Service) Deletions() <-chan DeletionNotice {
	if s == nil {
		return nil
	}
	return s.deletions
}

// ListenerStatuses returns connection and real-time update health by account.
func (s *Service) ListenerStatuses() []ListenerStatus {
	if s == nil {
		return nil
	}
	s.listenerMu.RLock()
	items := make([]ListenerStatus, 0, len(s.listeners))
	queuedDeletions := len(s.deletions)
	s.deletionsMu.Lock()
	queuedDeletions += len(s.deletionPending)
	s.deletionsMu.Unlock()
	for _, item := range s.listeners {
		item.QueuedEvents = len(s.updates)
		item.QueuedDeletionEvents = queuedDeletions
		items = append(items, item)
	}
	s.listenerMu.RUnlock()
	sort.Slice(items, func(i, j int) bool {
		return items[i].AccountID < items[j].AccountID
	})
	return items
}
