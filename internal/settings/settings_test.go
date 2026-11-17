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
		HTTPAddr:              "127.0.0.1:9487",
		TelegramAPIID:         12345,
		TelegramAPIHash:       "test-hash",
		TelegramCodeHosts:     []string{"API.didiapi.uk", "api.didiapi.uk"},
		TelegramCodeDNSServer: "1.1.1.1:53",
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
	if len(got.TelegramCodeHosts) != 1 || got.TelegramCodeHosts[0] != "api.didiapi.uk" {
		t.Fatalf("hosts = %#v, want normalized unique host", got.TelegramCodeHosts)
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
		HTTPAddr:        "127.0.0.1:9487",
		TelegramAPIID:   123,
		TelegramAPIHash: "hash",
	})
	if got := store.Snapshot(); got.HTTPAddr != "127.0.0.1:9487" || got.TelegramAPIID != 123 {
		t.Fatalf("Snapshot() = %+v, want seeded defaults", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("UseDefaults() unexpectedly wrote the settings file: %v", err)
	}
}
