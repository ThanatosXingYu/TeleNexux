// Package auth manages the local administrator credential and in-memory sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	CookieName = "telenexus_session"
	CSRFCookie = "telenexus_csrf"
	CSRFHeader = "X-CSRF-Token"

	RoleAdministrator = "administrator"
	RoleOperator      = "operator"
	RoleViewer        = "viewer"

	PermissionSession        = "session"
	PermissionHealthRead     = "health:read"
	PermissionAuditRead      = "audit:read"
	PermissionDashboardRead  = "dashboard:read"
	PermissionGroupRead      = "group:read"
	PermissionGroupWrite     = "group:write"
	PermissionAccountRead    = "account:read"
	PermissionAccountWrite   = "account:write"
	PermissionChatRead       = "chat:read"
	PermissionChatOperate    = "chat:operate"
	PermissionMonitorRead    = "monitor:read"
	PermissionMonitorWrite   = "monitor:write"
	PermissionMonitorOperate = "monitor:operate"
	PermissionMessageRead    = "message:read"
	PermissionMessageWrite   = "message:write"
	PermissionReportRead     = "report:read"
	PermissionRetentionRead  = "retention:read"
	PermissionRetentionWrite = "retention:write"
	PermissionSettingsRead   = "settings:read"
	PermissionSettingsWrite  = "settings:write"
	PermissionTelegramAuth   = "telegram:auth"
)

var (
	ErrAlreadyConfigured = errors.New("administrator is already configured")
	ErrNotConfigured     = errors.New("administrator is not configured")
	ErrInvalidLogin      = errors.New("invalid administrator login")
	ErrInvalidSession    = errors.New("invalid administrator session")
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,64}$`)

var knownPermissions = map[string]struct{}{
	PermissionSession:        {},
	PermissionHealthRead:     {},
	PermissionAuditRead:      {},
	PermissionDashboardRead:  {},
	PermissionGroupRead:      {},
	PermissionGroupWrite:     {},
	PermissionAccountRead:    {},
	PermissionAccountWrite:   {},
	PermissionChatRead:       {},
	PermissionChatOperate:    {},
	PermissionMonitorRead:    {},
	PermissionMonitorWrite:   {},
	PermissionMonitorOperate: {},
	PermissionMessageRead:    {},
	PermissionMessageWrite:   {},
	PermissionReportRead:     {},
	PermissionRetentionRead:  {},
	PermissionRetentionWrite: {},
	PermissionSettingsRead:   {},
	PermissionSettingsWrite:  {},
	PermissionTelegramAuth:   {},
}

type credentials struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

type session struct {
	Actor     string
	Role      string
	CSRF      string
	ExpiresAt time.Time
}

type Principal struct {
	Actor string
	Role  string
}

type principalContextKey struct{}

type Manager struct {
	path         string
	sessionTTL   time.Duration
	now          func() time.Time
	mu           sync.RWMutex
	username     string
	passwordHash string
	sessions     map[string]session
}

func NewManager(path string) (*Manager, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("auth store path must not be empty")
	}
	manager := &Manager{
		path:       path,
		sessionTTL: 12 * time.Hour,
		now:        time.Now,
		sessions:   make(map[string]session),
	}
	if err := manager.load(); err != nil {
		return nil, err
	}
	return manager, nil
}

func NewTestManager(username, password string) (*Manager, error) {
	manager := &Manager{
		sessionTTL: time.Hour,
		now:        time.Now,
		sessions:   make(map[string]session),
	}
	if err := manager.Setup(username, password); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) Configured() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.username != "" && m.passwordHash != ""
}

func (m *Manager) Username() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.username
}

func (m *Manager) Setup(username, password string) error {
	username = normalizeUsername(username)
	if !usernamePattern.MatchString(username) {
		return errors.New("管理员账号需为 3 到 64 位字母、数字、点、下划线或连字符")
	}
	if len(password) < 12 || len(password) > 128 {
		return errors.New("管理员密码需为 12 到 128 个字符")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.username != "" || m.passwordHash != "" {
		return ErrAlreadyConfigured
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash administrator password: %w", err)
	}
	stored := credentials{Username: username, PasswordHash: string(passwordHash)}
	if m.path != "" {
		if err := writeCredentials(m.path, stored); err != nil {
			return err
		}
	}
	m.username = stored.Username
	m.passwordHash = stored.PasswordHash
	return nil
}

func (m *Manager) Login(username, password string) error {
	m.mu.RLock()
	storedUsername := m.username
	passwordHash := m.passwordHash
	m.mu.RUnlock()
	if storedUsername == "" || passwordHash == "" {
		return ErrNotConfigured
	}
	if !constantTimeStringEqual(storedUsername, normalizeUsername(username)) {
		_ = bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password))
		return ErrInvalidLogin
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return ErrInvalidLogin
	}
	return nil
}

func (m *Manager) CreateSession() (string, Session, error) {
	return m.CreateRoleSession(RoleAdministrator)
}

// CreateRoleSession creates a session for a supported local role.
// Role assignment is intentionally kept inside the process until account
// management endpoints are added.
func (m *Manager) CreateRoleSession(role string) (string, Session, error) {
	if !CanAccess(role, PermissionSession) {
		return "", Session{}, errors.New("unsupported session role")
	}
	m.mu.RLock()
	actor := m.username
	configured := m.username != "" && m.passwordHash != ""
	m.mu.RUnlock()
	if !configured {
		return "", Session{}, ErrNotConfigured
	}
	token, err := randomToken()
	if err != nil {
		return "", Session{}, fmt.Errorf("generate session token: %w", err)
	}
	csrf, err := randomToken()
	if err != nil {
		return "", Session{}, fmt.Errorf("generate CSRF token: %w", err)
	}
	now := m.now().UTC()
	item := session{
		Actor:     actor,
		Role:      role,
		CSRF:      csrf,
		ExpiresAt: now.Add(m.sessionTTL),
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for key, existing := range m.sessions {
		if !existing.ExpiresAt.After(now) {
			delete(m.sessions, key)
		}
	}
	m.sessions[sessionKey(token)] = item
	return token, publicSession(item), nil
}

func (m *Manager) Lookup(token string) (Session, error) {
	key := sessionKey(token)
	m.mu.RLock()
	item, exists := m.sessions[key]
	m.mu.RUnlock()
	if !exists || !item.ExpiresAt.After(m.now().UTC()) {
		if exists {
			m.Delete(token)
		}
		return Session{}, ErrInvalidSession
	}
	return publicSession(item), nil
}

func (m *Manager) Delete(token string) {
	if token == "" {
		return
	}
	m.mu.Lock()
	delete(m.sessions, sessionKey(token))
	m.mu.Unlock()
}

type Session struct {
	Actor     string
	Role      string
	CSRF      string
	ExpiresAt time.Time
}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok && principal.Actor != "" && principal.Role != ""
}

func CanAccess(role, permission string) bool {
	if _, known := knownPermissions[permission]; !known {
		return false
	}
	if role == RoleAdministrator {
		return true
	}
	switch role {
	case RoleOperator:
		switch permission {
		case PermissionSession,
			PermissionHealthRead,
			PermissionDashboardRead,
			PermissionGroupRead,
			PermissionAccountRead,
			PermissionAccountWrite,
			PermissionChatRead,
			PermissionChatOperate,
			PermissionMonitorRead,
			PermissionMonitorWrite,
			PermissionMonitorOperate,
			PermissionMessageRead,
			PermissionReportRead,
			PermissionSettingsRead,
			PermissionTelegramAuth:
			return true
		}
	case RoleViewer:
		switch permission {
		case PermissionSession,
			PermissionHealthRead,
			PermissionDashboardRead,
			PermissionGroupRead,
			PermissionAccountRead,
			PermissionChatRead,
			PermissionMonitorRead,
			PermissionMessageRead,
			PermissionReportRead,
			PermissionSettingsRead:
			return true
		}
	}
	return false
}

func (m *Manager) load() error {
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read administrator credentials: %w", err)
	}
	if len(data) > 64<<10 {
		return errors.New("administrator credential file is too large")
	}
	var stored credentials
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return fmt.Errorf("decode administrator credentials: %w", err)
	}
	stored.Username = normalizeUsername(stored.Username)
	if !usernamePattern.MatchString(stored.Username) || stored.PasswordHash == "" {
		return errors.New("administrator credential file is invalid")
	}
	if err := os.Chmod(m.path, 0o600); err != nil {
		return fmt.Errorf("secure administrator credential file: %w", err)
	}
	m.username = stored.Username
	m.passwordHash = stored.PasswordHash
	return nil
}

func writeCredentials(path string, stored credentials) (err error) {
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("encode administrator credentials: %w", err)
	}
	data = append(data, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create administrator credential directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".telenexus-auth-*.tmp")
	if err != nil {
		return fmt.Errorf("create administrator credential file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		removeErr := os.Remove(temporaryPath)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("remove temporary administrator credential file: %w", removeErr)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure administrator credential file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write administrator credentials: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync administrator credentials: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close administrator credential file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace administrator credential file: %w", err)
	}
	return nil
}

func normalizeUsername(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func constantTimeStringEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func publicSession(item session) Session {
	return Session{
		Actor:     item.Actor,
		Role:      item.Role,
		CSRF:      item.CSRF,
		ExpiresAt: item.ExpiresAt,
	}
}
