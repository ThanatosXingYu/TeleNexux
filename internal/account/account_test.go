package account

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type memoryRepository struct {
	accounts map[int64]Account
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{accounts: make(map[int64]Account)}
}

func (r *memoryRepository) Create(_ context.Context, input CreateInput, now time.Time) (Account, error) {
	if _, exists := r.accounts[input.ID]; exists {
		return Account{}, ErrConflict
	}
	item := Account{
		ID:          input.ID,
		PhoneNumber: input.PhoneNumber,
		Username:    input.Username,
		DisplayName: input.DisplayName,
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	r.accounts[input.ID] = item
	return item, nil
}

func (r *memoryRepository) Upsert(
	_ context.Context,
	input CreateInput,
	status string,
	now time.Time,
) (Account, error) {
	item, exists := r.accounts[input.ID]
	if !exists {
		item.CreatedAt = now
	}
	item.ID = input.ID
	item.PhoneNumber = input.PhoneNumber
	item.Username = input.Username
	item.DisplayName = input.DisplayName
	item.Status = status
	item.UpdatedAt = now
	r.accounts[input.ID] = item
	return item, nil
}

func (r *memoryRepository) List(_ context.Context, filter Filter) ([]Account, error) {
	items := make([]Account, 0, len(r.accounts))
	for _, item := range r.accounts {
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (r *memoryRepository) Get(_ context.Context, id int64) (Account, error) {
	item, exists := r.accounts[id]
	if !exists {
		return Account{}, ErrNotFound
	}
	return item, nil
}

func (r *memoryRepository) UpdateStatus(
	_ context.Context,
	id int64,
	status string,
	now time.Time,
) (Account, error) {
	item, exists := r.accounts[id]
	if !exists {
		return Account{}, ErrNotFound
	}
	item.Status = status
	item.UpdatedAt = now
	r.accounts[id] = item
	return item, nil
}

func TestServiceCreateNormalizesAccount(t *testing.T) {
	service := NewService(newMemoryRepository())
	created, err := service.Create(context.Background(), CreateInput{
		ID:          42,
		PhoneNumber: " +8613800138000 ",
		Username:    "@alice",
		DisplayName: " Alice ",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Username != "alice" || created.PhoneNumber != "+8613800138000" {
		t.Errorf("Create() account = %+v, expected normalized values", created)
	}
	if created.Status != StatusPending {
		t.Errorf("Create() status = %q, want %q", created.Status, StatusPending)
	}
}

func TestServiceCreateValidation(t *testing.T) {
	tests := []struct {
		name    string
		input   CreateInput
		wantErr string
	}{
		{
			name:    "invalid ID",
			input:   CreateInput{ID: 0, PhoneNumber: "13800138000"},
			wantErr: "id",
		},
		{
			name:    "short phone",
			input:   CreateInput{ID: 1, PhoneNumber: "123"},
			wantErr: "phone_number",
		},
		{
			name:    "invalid username",
			input:   CreateInput{ID: 1, PhoneNumber: "13800138000", Username: "not valid"},
			wantErr: "username",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewService(newMemoryRepository()).Create(context.Background(), tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Create() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestServiceRejectsUnknownStatus(t *testing.T) {
	_, err := NewService(newMemoryRepository()).List(context.Background(), Filter{Status: "offline"})
	if err == nil {
		t.Fatal("List() expected status validation error")
	}
}

func TestServiceRejectsMissingAccount(t *testing.T) {
	_, err := NewService(newMemoryRepository()).Get(context.Background(), 100)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestServiceUpsertAuthorizedMarksAccountOnline(t *testing.T) {
	service := NewService(newMemoryRepository())
	item, err := service.UpsertAuthorized(context.Background(), CreateInput{
		ID:          42,
		PhoneNumber: "+15550001001",
		DisplayName: "Telegram User",
	})
	if err != nil {
		t.Fatalf("UpsertAuthorized() error = %v", err)
	}
	if item.Status != StatusOnline {
		t.Fatalf("UpsertAuthorized() status = %q, want %q", item.Status, StatusOnline)
	}
}

func TestServiceUpdateStatusPersistsRealSessionState(t *testing.T) {
	service := NewService(newMemoryRepository())
	item, err := service.UpsertAuthorized(context.Background(), CreateInput{
		ID:          42,
		PhoneNumber: "+15550001001",
		DisplayName: "Telegram User",
	})
	if err != nil {
		t.Fatalf("UpsertAuthorized() error = %v", err)
	}
	updated, err := service.UpdateStatus(context.Background(), item.ID, StatusError)
	if err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if updated.Status != StatusError {
		t.Fatalf("UpdateStatus() status = %q, want %q", updated.Status, StatusError)
	}
}
