package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreSavesPrivateSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	want := Values{
		AppEnvironment:  "production",
		LogLevel:        "INFO",
		HTTPAddr:        "127.0.0.1:9487",
		TelegramAPIID:   12345,
		TelegramAPIHash: "test-hash",
	}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat settings: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %o, want 600", info.Mode().Perm())
	}
	got := store.Snapshot()
	if got.AppEnvironment != "production" || got.LogLevel != "info" {
		t.Fatalf("runtime settings = %q/%q, want normalized production/info", got.AppEnvironment, got.LogLevel)
	}
}

func TestLoadIgnoresRemovedCodeFetchSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{
		"telegram_api_id": 12345,
		"telegram_api_hash": "test-hash",
		"telegram_code_fetch_hosts": ["legacy.example"],
		"telegram_code_dns_server": "1.1.1.1:53"
	}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	values, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if values.TelegramAPIID != 12345 || values.TelegramAPIHash != "test-hash" {
		t.Fatalf("Load() = %+v, want retained Telegram credentials", values)
	}
}

func TestStoreRejectsInvalidLogLevel(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(Values{LogLevel: "verbose"}); err == nil {
		t.Fatal("Save() accepted an unsupported log level")
	}
}

func TestStoreRejectsPartialTelegramCredentials(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Save(Values{TelegramAPIID: 123}); err == nil {
		t.Fatal("Save() accepted an API ID without an API hash")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted an unknown field")
	}
}

func TestUseDefaultsSeedsMissingValuesWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	store.UseDefaults(Values{
		AppEnvironment:  "development",
		LogLevel:        "info",
		HTTPAddr:        "127.0.0.1:9487",
		TelegramAPIID:   123,
		TelegramAPIHash: "hash",
	})
	if got := store.Snapshot(); got.AppEnvironment != "development" ||
		got.LogLevel != "info" ||
		got.HTTPAddr != "127.0.0.1:9487" ||
		got.TelegramAPIID != 123 {
		t.Fatalf("Snapshot() = %+v, want seeded defaults", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("UseDefaults() unexpectedly wrote the settings file: %v", err)
	}
}
