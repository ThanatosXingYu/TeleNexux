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
	StatusPending  = "pending"
	StatusOnline   = "online"
	StatusPaused   = "paused"
	StatusError    = "error"
	MaxDisplayName = 64
	MaxUsername    = 32
	MaxPhoneNumber = 20
)

var (
	ErrNotFound = errors.New("account not found")
	ErrConflict = errors.New("account already exists")
)

type Account struct {
	ID             int64      `json:"id"`
	PhoneNumber    string     `json:"phone_number"`
	Username       string     `json:"username"`
	DisplayName    string     `json:"display_name"`
	Status         string     `json:"status"`
	StatusReason   string     `json:"status_reason,omitempty"`
	LastVerifiedAt *time.Time `json:"last_verified_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
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

type Filter struct {
	Status string
	Limit  int
	Offset int
}

type Repository interface {
	Create(ctx context.Context, input CreateInput, now time.Time) (Account, error)
	Upsert(ctx context.Context, input CreateInput, status string, now time.Time) (Account, error)
	List(ctx context.Context, filter Filter) ([]Account, error)
	Get(ctx context.Context, id int64) (Account, error)
	UpdateStatus(ctx context.Context, id int64, status string, now time.Time) (Account, error)
	RecordVerification(
		ctx context.Context,
		id int64,
		status string,
		reason string,
		now time.Time,
	) (Account, error)
	Update(ctx context.Context, id int64, input UpdateInput, now time.Time) (Account, error)
	Delete(ctx context.Context, id int64) error
}

type Service struct {
	repository Repository
	clock      func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{
		repository: repository,
		clock:      time.Now,
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

func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("account ID must be positive")
	}
	return s.repository.Delete(ctx, id)
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
