// Package auth manages local users, role-based access, and in-memory sessions.
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
	"sort"
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
	PermissionUserRead       = "user:read"
	PermissionUserWrite      = "user:write"
)

var (
	ErrAlreadyConfigured = errors.New("administrator is already configured")
	ErrNotConfigured     = errors.New("administrator is not configured")
	ErrInvalidLogin      = errors.New("invalid administrator login")
	ErrInvalidSession    = errors.New("invalid administrator session")
	ErrUserNotFound      = errors.New("user not found")
	ErrUserExists        = errors.New("user already exists")
	ErrLastAdministrator = errors.New("at least one active administrator is required")
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
	PermissionUserRead:       {},
	PermissionUserWrite:      {},
}

// Permissions returns the permissions granted to a supported local role.
func Permissions(role string) []string {
	permissions := make([]string, 0, len(knownPermissions))
	for permission := range knownPermissions {
		if CanAccess(role, permission) {
			permissions = append(permissions, permission)
		}
	}
	sort.Strings(permissions)
	return permissions
}

type storedUser struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Role         string `json:"role,omitempty"`
	Disabled     bool   `json:"disabled,omitempty"`
}

type credentials struct {
	Username     string       `json:"username,omitempty"`
	PasswordHash string       `json:"password_hash,omitempty"`
	Users        []storedUser `json:"users,omitempty"`
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

// User is the public, credential-free view of a local management user.
type User struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
}

type principalContextKey struct{}

type Manager struct {
	path       string
	sessionTTL time.Duration
	now        func() time.Time
	mu         sync.RWMutex
	users      map[string]storedUser
	sessions   map[string]session
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
		users:      make(map[string]storedUser),
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
		users:      make(map[string]storedUser),
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
	return hasActiveAdministrator(m.users)
}

func (m *Manager) Setup(username, password string) error {
	username = normalizeUsername(username)
	if !usernamePattern.MatchString(username) {
		return errors.New("管理员账号需为 3 到 64 位字母、数字、点、下划线或连字符")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.users) > 0 {
		return ErrAlreadyConfigured
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash administrator password: %w", err)
	}
	stored := credentials{Users: []storedUser{{
		Username:     username,
		PasswordHash: string(passwordHash),
		Role:         RoleAdministrator,
	}}}
	if m.path != "" {
		if err := writeCredentials(m.path, stored); err != nil {
			return err
		}
	}
	m.users[username] = stored.Users[0]
	return nil
}

func (m *Manager) Login(username, password string) error {
	_, err := m.Authenticate(username, password)
	return err
}

// Authenticate verifies a local user and returns the actor and role for its session.
func (m *Manager) Authenticate(username, password string) (Principal, error) {
	m.mu.RLock()
	user, exists := m.users[normalizeUsername(username)]
	configured := len(m.users) > 0
	m.mu.RUnlock()
	if !configured {
		return Principal{}, ErrNotConfigured
	}
	if !exists {
		// Keep unknown-user responses comparable to a wrong password.
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash(), []byte(password))
		return Principal{}, ErrInvalidLogin
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return Principal{}, ErrInvalidLogin
	}
	if user.Disabled {
		return Principal{}, ErrInvalidLogin
	}
	return Principal{Actor: user.Username, Role: user.Role}, nil
}

func (m *Manager) CreateSession() (string, Session, error) {
	actor := m.firstActiveAdministrator()
	return m.CreateUserSession(actor, RoleAdministrator)
}

// CreateRoleSession creates a session under the first administrator for a
// focused role authorization test. Production login uses CreateUserSession.
func (m *Manager) CreateRoleSession(role string) (string, Session, error) {
	if role != RoleAdministrator {
		if !CanAccess(role, PermissionSession) {
			return "", Session{}, errors.New("unsupported session role")
		}
		m.mu.RLock()
		if !hasActiveAdministrator(m.users) {
			m.mu.RUnlock()
			return "", Session{}, ErrNotConfigured
		}
		actor := m.firstActiveAdministratorLocked()
		m.mu.RUnlock()
		return m.createSession(actor, role)
	}
	return m.CreateUserSession(m.firstActiveAdministrator(), role)
}

// CreateUserSession creates a session for an active local user.
func (m *Manager) CreateUserSession(username, role string) (string, Session, error) {
	if !CanAccess(role, PermissionSession) {
		return "", Session{}, errors.New("unsupported session role")
	}
	username = normalizeUsername(username)
	m.mu.RLock()
	user, exists := m.users[username]
	m.mu.RUnlock()
	if !exists || user.Disabled || user.Role != role {
		return "", Session{}, errors.New("inactive or mismatched local user")
	}
	if role != RoleAdministrator && !m.Configured() {
		return "", Session{}, ErrNotConfigured
	}
	return m.createSession(username, user.Role)
}

func (m *Manager) createSession(actor, role string) (string, Session, error) {
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

// SessionFor resolves a principal again so disabled users and role changes
// revoke access without waiting for a session to expire.
func (m *Manager) SessionFor(token string) (Session, error) {
	item, err := m.Lookup(token)
	if err != nil {
		return Session{}, err
	}
	m.mu.RLock()
	user, exists := m.users[normalizeUsername(item.Actor)]
	m.mu.RUnlock()
	if !exists || user.Disabled || user.Role != item.Role {
		m.Delete(token)
		return Session{}, ErrInvalidSession
	}
	return item, nil
}

// ListUsers returns local users in stable username order without credentials.
func (m *Manager) ListUsers() []User {
	m.mu.RLock()
	defer m.mu.RUnlock()
	users := make([]User, 0, len(m.users))
	for _, user := range m.users {
		users = append(users, publicUser(user))
	}
	sort.Slice(users, func(left, right int) bool {
		return users[left].Username < users[right].Username
	})
	return users
}

// CreateUser adds a local management user. The administrator remains the
// initial owner; subsequent users can be operator or viewer accounts.
func (m *Manager) CreateUser(username, password, role string) (User, error) {
	username = normalizeUsername(username)
	if !usernamePattern.MatchString(username) {
		return User{}, errors.New("账号需为 3 到 64 位字母、数字、点、下划线或连字符")
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	if !CanAccess(role, PermissionSession) {
		return User{}, errors.New("不支持的用户角色")
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, fmt.Errorf("hash user password: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.users[username]; exists {
		return User{}, ErrUserExists
	}
	next := cloneUsers(m.users)
	user := storedUser{
		Username:     username,
		PasswordHash: string(passwordHash),
		Role:         role,
	}
	next[username] = user
	if err := m.persistLocked(next); err != nil {
		return User{}, err
	}
	m.users = next
	return publicUser(user), nil
}

// UserUpdate changes only the provided fields. An empty password means the
// existing password is retained and is never returned by any API.
type UserUpdate struct {
	Role     *string
	Password *string
	Disabled *bool
}

func (m *Manager) UpdateUser(username string, input UserUpdate) (User, error) {
	username = normalizeUsername(username)
	m.mu.Lock()
	defer m.mu.Unlock()
	current, exists := m.users[username]
	if !exists {
		return User{}, ErrUserNotFound
	}
	nextUser := current
	if input.Role != nil {
		if !CanAccess(*input.Role, PermissionSession) {
			return User{}, errors.New("不支持的用户角色")
		}
		nextUser.Role = *input.Role
	}
	if input.Disabled != nil {
		nextUser.Disabled = *input.Disabled
	}
	if input.Password != nil && *input.Password != "" {
		if err := validatePassword(*input.Password); err != nil {
			return User{}, err
		}
		passwordHash, err := bcrypt.GenerateFromPassword(
			[]byte(*input.Password),
			bcrypt.DefaultCost,
		)
		if err != nil {
			return User{}, fmt.Errorf("hash user password: %w", err)
		}
		nextUser.PasswordHash = string(passwordHash)
	}
	next := cloneUsers(m.users)
	next[username] = nextUser
	if !hasActiveAdministrator(next) {
		return User{}, ErrLastAdministrator
	}
	if err := m.persistLocked(next); err != nil {
		return User{}, err
	}
	m.users = next
	m.deleteSessionsForLocked(username)
	return publicUser(nextUser), nil
}

func (m *Manager) persistLocked(users map[string]storedUser) error {
	if m.path == "" {
		return nil
	}
	return writeCredentials(m.path, credentials{Users: sortedUsers(users)})
}

func (m *Manager) firstActiveAdministrator() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.firstActiveAdministratorLocked()
}

func (m *Manager) firstActiveAdministratorLocked() string {
	usernames := make([]string, 0, len(m.users))
	for username, user := range m.users {
		if user.Role == RoleAdministrator && !user.Disabled {
			usernames = append(usernames, username)
		}
	}
	sort.Strings(usernames)
	if len(usernames) == 0 {
		return ""
	}
	return usernames[0]
}

func (m *Manager) deleteSessionsForLocked(username string) {
	for token, item := range m.sessions {
		if normalizeUsername(item.Actor) == username {
			delete(m.sessions, token)
		}
	}
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
	users := stored.Users
	if len(users) == 0 && stored.Username != "" && stored.PasswordHash != "" {
		users = []storedUser{{
			Username:     stored.Username,
			PasswordHash: stored.PasswordHash,
			Role:         RoleAdministrator,
		}}
	}
	loaded := make(map[string]storedUser, len(users))
	for _, user := range users {
		user.Username = normalizeUsername(user.Username)
		if !usernamePattern.MatchString(user.Username) || user.PasswordHash == "" {
			return errors.New("local credential file contains an invalid user")
		}
		if user.Role == "" {
			user.Role = RoleViewer
		}
		if !CanAccess(user.Role, PermissionSession) {
			return errors.New("local credential file contains an invalid role")
		}
		if _, exists := loaded[user.Username]; exists {
			return errors.New("local credential file contains duplicate users")
		}
		loaded[user.Username] = user
	}
	if len(loaded) == 0 || !hasActiveAdministrator(loaded) {
		return errors.New("local credential file must contain an active administrator")
	}
	if err := os.Chmod(m.path, 0o600); err != nil {
		return fmt.Errorf("secure administrator credential file: %w", err)
	}
	m.users = loaded
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

func validatePassword(password string) error {
	if len(password) < 12 || len(password) > 128 {
		return errors.New("密码需为 12 到 128 个字符")
	}
	return nil
}

func dummyPasswordHash() []byte {
	// A valid bcrypt hash keeps unknown-user and wrong-password timing similar.
	return []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")
}

func hasActiveAdministrator(users map[string]storedUser) bool {
	for _, user := range users {
		if user.Role == RoleAdministrator && !user.Disabled {
			return true
		}
	}
	return false
}

func cloneUsers(users map[string]storedUser) map[string]storedUser {
	cloned := make(map[string]storedUser, len(users))
	for username, user := range users {
		cloned[username] = user
	}
	return cloned
}

func sortedUsers(users map[string]storedUser) []storedUser {
	usernames := make([]string, 0, len(users))
	for username := range users {
		usernames = append(usernames, username)
	}
	sort.Strings(usernames)
	ordered := make([]storedUser, 0, len(usernames))
	for _, username := range usernames {
		ordered = append(ordered, users[username])
	}
	return ordered
}

func publicUser(user storedUser) User {
	return User{
		Username: user.Username,
		Role:     user.Role,
		Disabled: user.Disabled,
	}
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
