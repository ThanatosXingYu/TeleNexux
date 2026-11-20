// Package accountproxy manages account-scoped SOCKS5 proxy credentials.
package accountproxy

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

var (
	ErrInvalidProxy = errors.New("invalid proxy configuration")
	ErrNotFound     = errors.New("account proxy not found")
)

const (
	MaxHostLength     = 253
	MaxUsernameLength = 128
	MaxPasswordLength = 256
	keySize           = 32
)

type Profile struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type Binding struct {
	AccountID   int64      `json:"account_id"`
	Configured  bool       `json:"configured"`
	Enabled     bool       `json:"enabled"`
	Host        string     `json:"host,omitempty"`
	Port        int        `json:"port,omitempty"`
	Username    string     `json:"username,omitempty"`
	LastChecked *time.Time `json:"last_checked_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

type Store struct {
	path  string
	key   []byte
	mu    sync.RWMutex
	items map[int64]Profile
}

func NewStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("account proxy path must not be empty")
	}
	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		absolute, err := filepath.Abs(cleanPath)
		if err != nil {
			return nil, fmt.Errorf("resolve account proxy path: %w", err)
		}
		cleanPath = absolute
	}
	key, err := loadOrCreateKey(cleanPath + ".key")
	if err != nil {
		return nil, err
	}
	store := &Store{
		path:  cleanPath,
		key:   key,
		items: make(map[int64]Profile),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Get(accountID int64) (Profile, bool) {
	if s == nil || accountID <= 0 {
		return Profile{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	profile, ok := s.items[accountID]
	return profile, ok
}

func (s *Store) Set(ctx context.Context, accountID int64, profile Profile) error {
	if s == nil {
		return errors.New("account proxy store is unavailable")
	}
	if accountID <= 0 {
		return fmt.Errorf("%w: account ID must be positive", ErrInvalidProxy)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, err := Normalize(profile)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := make(map[int64]Profile, len(s.items)+1)
	for id, item := range s.items {
		updated[id] = item
	}
	updated[accountID] = normalized
	if err := s.persist(updated); err != nil {
		return err
	}
	s.items = updated
	return nil
}

func (s *Store) Delete(ctx context.Context, accountID int64) (bool, error) {
	if s == nil {
		return false, errors.New("account proxy store is unavailable")
	}
	if accountID <= 0 {
		return false, ErrInvalidProxy
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[accountID]; !ok {
		return false, nil
	}
	updated := make(map[int64]Profile, len(s.items))
	for id, item := range s.items {
		if id != accountID {
			updated[id] = item
		}
	}
	if err := s.persist(updated); err != nil {
		return false, err
	}
	s.items = updated
	return true, nil
}

func (s *Store) DialContext(
	ctx context.Context,
	accountID int64,
) (func(context.Context, string, string) (net.Conn, error), error) {
	profile, ok := s.Get(accountID)
	if !ok || !profile.Enabled {
		return nil, nil
	}
	dialer, err := NewDialer(profile)
	if err != nil {
		return nil, err
	}
	return dialer.DialContext, nil
}

func NewDialer(profile Profile) (proxy.ContextDialer, error) {
	normalized, err := Normalize(profile)
	if err != nil {
		return nil, err
	}
	if !normalized.Enabled {
		return nil, ErrNotFound
	}
	var auth *proxy.Auth
	if normalized.Username != "" || normalized.Password != "" {
		auth = &proxy.Auth{User: normalized.Username, Password: normalized.Password}
	}
	dialer, err := proxy.SOCKS5(
		"tcp",
		net.JoinHostPort(normalized.Host, strconv.Itoa(normalized.Port)),
		auth,
		proxy.Direct,
	)
	if err != nil {
		return nil, fmt.Errorf("create SOCKS5 dialer: %w", err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS5 dialer does not support context cancellation")
	}
	return contextDialer, nil
}

func Normalize(profile Profile) (Profile, error) {
	profile.Host = strings.ToLower(strings.TrimSpace(profile.Host))
	profile.Username = strings.TrimSpace(profile.Username)
	if !profile.Enabled {
		return Profile{}, nil
	}
	if profile.Host == "" || len(profile.Host) > MaxHostLength {
		return Profile{}, fmt.Errorf("%w: host is required", ErrInvalidProxy)
	}
	if strings.ContainsAny(profile.Host, "/@?#\\ \t\r\n") {
		return Profile{}, fmt.Errorf("%w: host contains invalid characters", ErrInvalidProxy)
	}
	if profile.Port < 1 || profile.Port > 65535 {
		return Profile{}, fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalidProxy)
	}
	if len(profile.Username) > MaxUsernameLength || len(profile.Password) > MaxPasswordLength {
		return Profile{}, fmt.Errorf("%w: credentials are too long", ErrInvalidProxy)
	}
	if profile.Password != "" && profile.Username == "" {
		return Profile{}, fmt.Errorf("%w: username is required with a password", ErrInvalidProxy)
	}
	return profile, nil
}

func Parse(value string) (Profile, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil {
		return Profile{}, fmt.Errorf("%w: %v", ErrInvalidProxy, err)
	}
	if parsed.Scheme != "socks5" && parsed.Scheme != "socks5h" {
		return Profile{}, fmt.Errorf("%w: only socks5:// is supported", ErrInvalidProxy)
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Profile{}, fmt.Errorf("%w: URL must contain only credentials, host, and port", ErrInvalidProxy)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		return Profile{}, fmt.Errorf("%w: port is required", ErrInvalidProxy)
	}
	username := ""
	password := ""
	if parsed.User != nil {
		username = parsed.User.Username()
		password, _ = parsed.User.Password()
	}
	return Normalize(Profile{
		Enabled:  true,
		Host:     parsed.Hostname(),
		Port:     port,
		Username: username,
		Password: password,
	})
}

func (s *Store) load() error {
	if err := ensurePrivateDirectory(s.path); err != nil {
		return err
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.items = make(map[int64]Profile)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read account proxy store: %w", err)
	}
	if len(data) == 0 {
		s.items = make(map[int64]Profile)
		return nil
	}
	if err := ensurePrivateRegularFile(s.path); err != nil {
		return err
	}
	plaintext, err := decrypt(s.key, data)
	if err != nil {
		return fmt.Errorf("decrypt account proxy store: %w", err)
	}
	var accounts map[string]Profile
	if err := json.Unmarshal(plaintext, &accounts); err != nil {
		return fmt.Errorf("decode account proxy store: %w", err)
	}
	items := make(map[int64]Profile, len(accounts))
	for rawID, item := range accounts {
		id, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil || id <= 0 {
			return errors.New("account proxy store contains an invalid account ID")
		}
		normalized, err := Normalize(item)
		if err != nil {
			return fmt.Errorf("validate account proxy %d: %w", id, err)
		}
		items[id] = normalized
	}
	s.items = items
	return nil
}

func (s *Store) persist(items map[int64]Profile) error {
	if err := ensurePrivateDirectory(s.path); err != nil {
		return err
	}
	accounts := make(map[string]Profile, len(items))
	for id, item := range items {
		accounts[strconv.FormatInt(id, 10)] = item
	}
	plaintext, err := json.Marshal(accounts)
	if err != nil {
		return fmt.Errorf("encode account proxy store: %w", err)
	}
	data, err := encrypt(s.key, plaintext)
	if err != nil {
		return fmt.Errorf("encrypt account proxy store: %w", err)
	}
	directory := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(directory, ".account-proxies-*")
	if err != nil {
		return fmt.Errorf("create account proxy temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { removeIfPresent(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure account proxy temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write account proxy temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync account proxy temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close account proxy temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace account proxy store: %w", err)
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create account proxy directory: %w", err)
	}
	if info, err := os.Lstat(directory); err != nil {
		return fmt.Errorf("inspect account proxy directory: %w", err)
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("account proxy directory must be a regular directory")
	}
	return nil
}

func ensurePrivateRegularFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect account proxy store: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("account proxy store must be a regular file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure account proxy store: %w", err)
	}
	return nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != keySize {
			return nil, errors.New("account proxy key must be exactly 32 bytes")
		}
		if err := ensurePrivateRegularFile(path); err != nil {
			return nil, err
		}
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read account proxy key: %w", err)
	}
	if err := ensurePrivateDirectory(path); err != nil {
		return nil, err
	}
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate account proxy key: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create account proxy key: %w", err)
	}
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write account proxy key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("sync account proxy key: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close account proxy key: %w", err)
	}
	return key, nil
}

func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func decrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("encrypted account proxy store is truncated")
	}
	nonce := ciphertext[:gcm.NonceSize()]
	return gcm.Open(nil, nonce, ciphertext[gcm.NonceSize():], nil)
}

func removeIfPresent(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}
