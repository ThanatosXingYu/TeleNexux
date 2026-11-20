package accountproxy

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorePersistsAccountProxyWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxies.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	profile, err := Parse("socks5://alice:secret@Proxy.Example:1080")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if profile.Host != "proxy.example" || profile.Port != 1080 ||
		profile.Username != "alice" || profile.Password != "secret" {
		t.Fatalf("profile = %+v", profile)
	}
	if err := store.Set(context.Background(), 42, profile); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat proxy store: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("proxy store mode = %o, want 600", info.Mode().Perm())
	}
	keyInfo, err := os.Stat(path + ".key")
	if err != nil {
		t.Fatalf("stat proxy key: %v", err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("proxy key mode = %o, want 600", keyInfo.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read proxy store: %v", err)
	}
	for _, secret := range []string{"secret", "alice", "proxy.example"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("encrypted proxy store contains plaintext %q", secret)
		}
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload store error = %v", err)
	}
	got, ok := reloaded.Get(42)
	if !ok || got.Host != "proxy.example" || got.Password != "secret" {
		t.Fatalf("reloaded profile = %+v, ok = %t", got, ok)
	}
}

func TestStoreRejectsTamperedCiphertext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxies.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Set(context.Background(), 42, Profile{
		Enabled:  true,
		Host:     "proxy.example",
		Port:     1080,
		Username: "alice",
		Password: "secret",
	}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read proxy store: %v", err)
	}
	data[len(data)-1] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("tamper proxy store: %v", err)
	}
	if _, err := NewStore(path); err == nil ||
		!strings.Contains(err.Error(), "decrypt account proxy store") {
		t.Fatalf("NewStore() error = %v, want decryption failure", err)
	}
}

func TestStoreRejectsInvalidKeyLength(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "proxies.json")
	if err := os.WriteFile(path+".key", []byte("short"), 0o600); err != nil {
		t.Fatalf("write invalid key: %v", err)
	}
	if _, err := NewStore(path); err == nil ||
		!strings.Contains(err.Error(), "exactly 32 bytes") {
		t.Fatalf("NewStore() error = %v, want invalid key length", err)
	}
}

func TestStoreUsesEmptyEncryptedMapForNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxies.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Set(context.Background(), 42, Profile{
		Enabled: true,
		Host:    "proxy.example",
		Port:    1080,
	}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read proxy store: %v", err)
	}
	plaintext, err := decrypt(store.key, data)
	if err != nil {
		t.Fatalf("decrypt proxy store: %v", err)
	}
	var accounts map[string]Profile
	if err := json.Unmarshal(plaintext, &accounts); err != nil {
		t.Fatalf("decode proxy store: %v", err)
	}
	if len(accounts) != 1 || accounts["42"].Host != "proxy.example" {
		t.Fatalf("decoded proxy store = %+v", accounts)
	}
}

func TestStoreDoesNotReturnProxyForUnboundAccount(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "proxies.json"))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if dial, err := store.DialContext(context.Background(), 99); err != nil || dial != nil {
		t.Fatalf("DialContext() error = %v, dial configured = %t, want direct connection",
			err, dial != nil)
	}
}

func TestParseRejectsUnsupportedAndIncompleteProxyURLs(t *testing.T) {
	for _, value := range []string{
		"http://proxy.example:8080",
		"socks5://proxy.example",
		"socks5://proxy.example:70000",
		"socks5://proxy.example:1080/path",
	} {
		if _, err := Parse(value); !errors.Is(err, ErrInvalidProxy) {
			t.Errorf("Parse(%q) error = %v, want ErrInvalidProxy", value, err)
		}
	}
}

func TestNormalizeRejectsPasswordWithoutUsername(t *testing.T) {
	_, err := Normalize(Profile{
		Enabled:  true,
		Host:     "proxy.example",
		Port:     1080,
		Password: "secret",
	})
	if !errors.Is(err, ErrInvalidProxy) || !strings.Contains(err.Error(), "username") {
		t.Fatalf("Normalize() error = %v", err)
	}
}

func TestStoreDeletePersistsRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxies.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Set(context.Background(), 7, Profile{
		Enabled: true,
		Host:    "proxy.example",
		Port:    1080,
	}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	removed, err := store.Delete(context.Background(), 7)
	if err != nil || !removed {
		t.Fatalf("Delete() = %t, %v", removed, err)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatalf("reload store error = %v", err)
	}
	if _, ok := reloaded.Get(7); ok {
		t.Fatal("deleted proxy is still present after reload")
	}
}
