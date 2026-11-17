package account

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSQLiteRepositoryPersistsAccounts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "accounts.db")
	repository, err := NewSQLiteRepository(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteRepository() error = %v", err)
	}

	service := NewService(repository)
	created, err := service.Create(ctx, CreateInput{
		ID:          7001,
		PhoneNumber: "13800138000",
		DisplayName: "Primary account",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := repository.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := NewSQLiteRepository(ctx, path)
	if err != nil {
		t.Fatalf("reopen repository error = %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	got, err := NewService(reopened).Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.DisplayName != created.DisplayName || got.Status != StatusPending {
		t.Errorf("Get() = %+v, want persisted account", got)
	}
}

func TestSQLiteRepositoryRejectsDuplicateID(t *testing.T) {
	ctx := context.Background()
	repository, err := NewSQLiteRepository(ctx, filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("NewSQLiteRepository() error = %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	service := NewService(repository)
	input := CreateInput{ID: 1, PhoneNumber: "13800138000"}
	if _, err := service.Create(ctx, input); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if _, err := service.Create(ctx, input); err == nil {
		t.Fatal("second Create() should fail with a unique ID error")
	}
}

func TestSQLiteRepositoryReturnsNotFound(t *testing.T) {
	repository, err := NewSQLiteRepository(context.Background(), filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("NewSQLiteRepository() error = %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	_, err = repository.Get(context.Background(), 404)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestSQLiteRepositoryUpsertsAuthorizedAccount(t *testing.T) {
	ctx := context.Background()
	repository, err := NewSQLiteRepository(ctx, filepath.Join(t.TempDir(), "accounts.db"))
	if err != nil {
		t.Fatalf("NewSQLiteRepository() error = %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	service := NewService(repository)
	input := CreateInput{ID: 8001, PhoneNumber: "+15550001001", DisplayName: "Initial"}
	if _, err := service.UpsertAuthorized(ctx, input); err != nil {
		t.Fatalf("first UpsertAuthorized() error = %v", err)
	}
	input.DisplayName = "Updated"
	item, err := service.UpsertAuthorized(ctx, input)
	if err != nil {
		t.Fatalf("second UpsertAuthorized() error = %v", err)
	}
	if item.DisplayName != "Updated" || item.Status != StatusOnline {
		t.Fatalf("UpsertAuthorized() = %+v, want updated online account", item)
	}
}
