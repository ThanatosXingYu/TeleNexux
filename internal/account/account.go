// Package account defines Telegram account metadata and its storage contract.
package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	StatusPending              = "pending"
	StatusOnline               = "online"
	StatusPaused               = "paused"
	StatusError                = "error"
	PurgeStateActive           = "active"
	PurgeStateScheduled        = "scheduled"
	PurgeStatePurging          = "purging"
	PurgeStateFailed           = "failed"
	MaxDisplayName             = 64
	MaxUsername                = 32
	MaxPhoneNumber             = 20
	DefaultDeletionGracePeriod = 24 * time.Hour
	PurgeClaimTimeout          = 15 * time.Minute
)

var (
	ErrNotFound           = errors.New("account not found")
	ErrConflict           = errors.New("account already exists")
	ErrDeletionInProgress = errors.New("account deletion is in progress")
)

type Account struct {
	ID                  int64      `json:"id"`
	PhoneNumber         string     `json:"phone_number"`
	Username            string     `json:"username"`
	DisplayName         string     `json:"display_name"`
	Status              string     `json:"status"`
	StatusReason        string     `json:"status_reason,omitempty"`
	LastVerifiedAt      *time.Time `json:"last_verified_at,omitempty"`
	DeletionRequestedAt *time.Time `json:"deletion_requested_at,omitempty"`
	DeletionEffectiveAt *time.Time `json:"deletion_effective_at,omitempty"`
	PurgeState          string     `json:"purge_state"`
	PurgeError          string     `json:"purge_error,omitempty"`
	Proxy               Proxy      `json:"proxy"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

type Proxy struct {
	Enabled     bool       `json:"enabled"`
	Host        string     `json:"host,omitempty"`
	Port        int        `json:"port,omitempty"`
	Username    string     `json:"username,omitempty"`
	Configured  bool       `json:"configured"`
	LastChecked *time.Time `json:"last_checked_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

type CreateInput struct {
	ID          int64  `json:"id"`
	PhoneNumber string `json:"phone_number"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type UpdateInput struct {
	PhoneNumber string `json:"phone_number"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type VerificationInput struct {
	ID          int64  `json:"id"`
	PhoneNumber string `json:"phone_number"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

type Filter struct {
	Status         string
	Limit          int
	Offset         int
	IncludeDeleted bool
}

type Repository interface {
	Create(ctx context.Context, input CreateInput, now time.Time) (Account, error)
	Upsert(ctx context.Context, input CreateInput, status string, now time.Time) (Account, error)
	List(ctx context.Context, filter Filter) ([]Account, error)
	Get(ctx context.Context, id int64) (Account, error)
	GetIncludingDeleted(ctx context.Context, id int64) (Account, error)
	UpdateStatus(ctx context.Context, id int64, status string, now time.Time) (Account, error)
	RecordVerification(
		ctx context.Context,
		id int64,
		status string,
		reason string,
		now time.Time,
	) (Account, error)
	RecordAuthorized(
		ctx context.Context,
		input VerificationInput,
		now time.Time,
	) (Account, error)
	Update(ctx context.Context, id int64, input UpdateInput, now time.Time) (Account, error)
	ScheduleDeletion(
		ctx context.Context,
		id int64,
		now time.Time,
		effectiveAt time.Time,
	) (Account, error)
	Restore(ctx context.Context, id int64, now time.Time) (Account, error)
	ListDueForPurge(ctx context.Context, now time.Time, limit int) ([]Account, error)
	ClaimPurge(ctx context.Context, id int64, now time.Time) (Account, error)
	FailPurge(ctx context.Context, id int64, reason string, now time.Time) error
	Delete(ctx context.Context, id int64) error
	SetProxy(ctx context.Context, id int64, proxy Proxy, now time.Time) (Account, error)
	ClearProxy(ctx context.Context, id int64, now time.Time) (Account, error)
}

type Service struct {
	repository          Repository
	clock               func() time.Time
	deletionGracePeriod time.Duration
}

func NewService(repository Repository) *Service {
	return NewServiceWithDeletionGrace(repository, DefaultDeletionGracePeriod)
}

func NewServiceWithDeletionGrace(
	repository Repository,
	deletionGracePeriod time.Duration,
) *Service {
	if deletionGracePeriod <= 0 {
		deletionGracePeriod = DefaultDeletionGracePeriod
	}
	return &Service{
		repository:          repository,
		clock:               time.Now,
		deletionGracePeriod: deletionGracePeriod,
	}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Account, error) {
	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return Account{}, err
	}
	return s.repository.Create(ctx, normalized, s.clock().UTC())
}

func (s *Service) UpsertAuthorized(ctx context.Context, input CreateInput) (Account, error) {
	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return Account{}, err
	}
	return s.repository.Upsert(ctx, normalized, StatusOnline, s.clock().UTC())
}

func (s *Service) List(ctx context.Context, filter Filter) ([]Account, error) {
	if filter.Status != "" && !IsValidStatus(filter.Status) {
		return nil, fmt.Errorf("status must be one of pending, online, paused, error")
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 20
	}
	if filter.Offset < 0 {
		return nil, errors.New("offset must not be negative")
	}
	return s.repository.List(ctx, filter)
}

func (s *Service) Get(ctx context.Context, id int64) (Account, error) {
	if id <= 0 {
		return Account{}, errors.New("account ID must be positive")
	}
	return s.repository.Get(ctx, id)
}

func (s *Service) UpdateStatus(ctx context.Context, id int64, status string) (Account, error) {
	if id <= 0 {
		return Account{}, errors.New("account ID must be positive")
	}
	if !IsValidStatus(status) {
		return Account{}, fmt.Errorf("status must be one of pending, online, paused, error")
	}
	return s.repository.UpdateStatus(ctx, id, status, s.clock().UTC())
}

func (s *Service) RecordVerification(
	ctx context.Context,
	id int64,
	status string,
	reason string,
) (Account, error) {
	if id <= 0 {
		return Account{}, errors.New("account ID must be positive")
	}
	if !IsValidStatus(status) {
		return Account{}, fmt.Errorf("status must be one of pending, online, paused, error")
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 160 {
		return Account{}, errors.New("status reason must be at most 160 characters")
	}
	return s.repository.RecordVerification(ctx, id, status, reason, s.clock().UTC())
}

func (s *Service) RecordAuthorized(
	ctx context.Context,
	input VerificationInput,
) (Account, error) {
	input.PhoneNumber = strings.TrimSpace(input.PhoneNumber)
	input.Username = strings.TrimPrefix(strings.TrimSpace(input.Username), "@")
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.ID <= 0 {
		return Account{}, errors.New("id must be a positive Telegram user ID")
	}
	if err := validatePhoneNumber(input.PhoneNumber); err != nil {
		return Account{}, err
	}
	if err := validateUsername(input.Username); err != nil {
		return Account{}, err
	}
	if utf8.RuneCountInString(input.DisplayName) > MaxDisplayName {
		return Account{}, fmt.Errorf("display_name must be at most %d characters", MaxDisplayName)
	}
	return s.repository.RecordAuthorized(ctx, input, s.clock().UTC())
}

func (s *Service) Update(ctx context.Context, id int64, input UpdateInput) (Account, error) {
	if id <= 0 {
		return Account{}, errors.New("account ID must be positive")
	}
	normalized, err := normalizeUpdateInput(input)
	if err != nil {
		return Account{}, err
	}
	return s.repository.Update(ctx, id, normalized, s.clock().UTC())
}

func (s *Service) ScheduleDeletion(ctx context.Context, id int64) (Account, error) {
	if id <= 0 {
		return Account{}, errors.New("account ID must be positive")
	}
	now := s.clock().UTC()
	return s.repository.ScheduleDeletion(
		ctx,
		id,
		now,
		now.Add(s.deletionGracePeriod),
	)
}

func (s *Service) Restore(ctx context.Context, id int64) (Account, error) {
	if id <= 0 {
		return Account{}, errors.New("account ID must be positive")
	}
	return s.repository.Restore(ctx, id, s.clock().UTC())
}

func (s *Service) ListDueForPurge(ctx context.Context, limit int) ([]Account, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repository.ListDueForPurge(ctx, s.clock().UTC(), limit)
}

func (s *Service) ClaimPurge(ctx context.Context, id int64) (Account, error) {
	if id <= 0 {
		return Account{}, errors.New("account ID must be positive")
	}
	return s.repository.ClaimPurge(ctx, id, s.clock().UTC())
}

func (s *Service) FailPurge(ctx context.Context, id int64, reason string) error {
	if id <= 0 {
		return errors.New("account ID must be positive")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "account purge failed"
	}
	if utf8.RuneCountInString(reason) > 300 {
		reason = string([]rune(reason)[:300])
	}
	return s.repository.FailPurge(ctx, id, reason, s.clock().UTC())
}

func (s *Service) FinalizePurge(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("account ID must be positive")
	}
	return s.repository.Delete(ctx, id)
}

func (s *Service) SetProxy(ctx context.Context, id int64, proxy Proxy) (Account, error) {
	if id <= 0 {
		return Account{}, errors.New("account ID must be positive")
	}
	proxy.Host = strings.TrimSpace(proxy.Host)
	proxy.Username = strings.TrimSpace(proxy.Username)
	proxy.LastError = strings.TrimSpace(proxy.LastError)
	if proxy.Enabled {
		proxy.Configured = true
		if proxy.Host == "" || len(proxy.Host) > 253 {
			return Account{}, errors.New("proxy host is invalid")
		}
		if proxy.Port < 1 || proxy.Port > 65535 {
			return Account{}, errors.New("proxy port must be between 1 and 65535")
		}
		if len(proxy.Username) > 128 {
			return Account{}, errors.New("proxy username is too long")
		}
		if proxy.LastChecked == nil {
			proxy.LastError = ""
		}
	} else {
		proxy = Proxy{}
	}
	if utf8.RuneCountInString(proxy.LastError) > 160 {
		proxy.LastError = string([]rune(proxy.LastError)[:160])
	}
	return s.repository.SetProxy(ctx, id, proxy, s.clock().UTC())
}

func (s *Service) ClearProxy(ctx context.Context, id int64) (Account, error) {
	if id <= 0 {
		return Account{}, errors.New("account ID must be positive")
	}
	return s.repository.ClearProxy(ctx, id, s.clock().UTC())
}

func IsValidStatus(status string) bool {
	switch status {
	case StatusPending, StatusOnline, StatusPaused, StatusError:
		return true
	default:
		return false
	}
}

func normalizeCreateInput(input CreateInput) (CreateInput, error) {
	input.PhoneNumber = strings.TrimSpace(input.PhoneNumber)
	input.Username = strings.TrimPrefix(strings.TrimSpace(input.Username), "@")
	input.DisplayName = strings.TrimSpace(input.DisplayName)

	if input.ID <= 0 {
		return CreateInput{}, errors.New("id must be a positive Telegram user ID")
	}
	if err := validatePhoneNumber(input.PhoneNumber); err != nil {
		return CreateInput{}, err
	}
	if err := validateUsername(input.Username); err != nil {
		return CreateInput{}, err
	}
	if utf8.RuneCountInString(input.DisplayName) > MaxDisplayName {
		return CreateInput{}, fmt.Errorf("display_name must be at most %d characters", MaxDisplayName)
	}
	return input, nil
}

func normalizeUpdateInput(input UpdateInput) (UpdateInput, error) {
	input.PhoneNumber = strings.TrimSpace(input.PhoneNumber)
	input.Username = strings.TrimPrefix(strings.TrimSpace(input.Username), "@")
	input.DisplayName = strings.TrimSpace(input.DisplayName)

	if err := validatePhoneNumber(input.PhoneNumber); err != nil {
		return UpdateInput{}, err
	}
	if err := validateUsername(input.Username); err != nil {
		return UpdateInput{}, err
	}
	if utf8.RuneCountInString(input.DisplayName) > MaxDisplayName {
		return UpdateInput{}, fmt.Errorf("display_name must be at most %d characters", MaxDisplayName)
	}
	return input, nil
}

func validatePhoneNumber(phone string) error {
	if len(phone) < 8 || len(phone) > MaxPhoneNumber {
		return fmt.Errorf("phone_number must contain between 8 and %d digits, with optional leading +", MaxPhoneNumber)
	}
	for i, char := range phone {
		if i == 0 && char == '+' {
			continue
		}
		if char < '0' || char > '9' {
			return errors.New("phone_number may contain only digits and an optional leading +")
		}
	}
	return nil
}

func validateUsername(username string) error {
	if username == "" {
		return nil
	}
	if utf8.RuneCountInString(username) > MaxUsername {
		return fmt.Errorf("username must be at most %d characters", MaxUsername)
	}
	for _, char := range username {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != '_' {
			return errors.New("username may contain only letters, digits, and underscores")
		}
	}
	return nil
}
