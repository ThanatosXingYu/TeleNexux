package account

import (
	"context"
	"errors"
	"os"
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
	path := filepath.Join(t.TempDir(), "accounts.db")
	repository, err := NewSQLiteRepository(ctx, path)
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
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSQLiteRepositoryUpdatesAccountStatus(t *testing.T) {
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
	item, err := service.UpsertAuthorized(ctx, CreateInput{
		ID:          8002,
		PhoneNumber: "+15550001002",
		DisplayName: "Status account",
	})
	if err != nil {
		t.Fatalf("UpsertAuthorized() error = %v", err)
	}
	updated, err := service.UpdateStatus(ctx, item.ID, StatusError)
	if err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if updated.Status != StatusError || !updated.UpdatedAt.After(item.UpdatedAt) {
		t.Fatalf("UpdateStatus() = %+v, want error status and newer timestamp", updated)
	}
}

func TestSQLiteRepositoryUpdatesAndDeletesAccount(t *testing.T) {
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
	item, err := service.Create(ctx, CreateInput{
		ID:          8003,
		PhoneNumber: "+15550001003",
		DisplayName: "Before update",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	updated, err := service.Update(ctx, item.ID, UpdateInput{
		PhoneNumber: "+15550001004",
		Username:    "updated_user",
		DisplayName: "After update",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.PhoneNumber != "+15550001004" || updated.Username != "updated_user" {
		t.Fatalf("Update() = %+v, want persisted account details", updated)
	}
	if err := service.Delete(ctx, item.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := repository.Get(ctx, item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after Delete() error = %v, want ErrNotFound", err)
	}
}
