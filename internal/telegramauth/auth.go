// Package telegramauth manages short-lived Telegram MTProto login attempts.
package telegramauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/accountproxy"
)

const (
	StepCode     = "code"
	StepPassword = "password"
	StepComplete = "complete"
)

var (
	ErrNotConfigured   = errors.New("Telegram API credentials are not configured")
	ErrInvalidPhone    = errors.New("phone number is invalid")
	ErrInvalidCode     = errors.New("verification code is invalid")
	ErrCodeExpired     = errors.New("verification code has expired")
	ErrInvalidPassword = errors.New("two-step verification password is invalid")
	ErrAccountMismatch = errors.New("authorized Telegram account does not match the requested account")
	ErrLoginNotFound   = errors.New("login attempt was not found or has expired")
	ErrLoginStage      = errors.New("login attempt is not at the requested step")
	ErrFloodWait       = errors.New("Telegram rate limit reached")
	ErrTelegram        = errors.New("Telegram authentication failed")
)

var (
	loginIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)
	codePattern    = regexp.MustCompile(`^\d{4,8}$`)
)

type SentCode struct {
	CodeHash   string
	CodeType   string
	CodeLength int
}

type Profile struct {
	ID          int64
	PhoneNumber string
	Username    string
	DisplayName string
}

type Driver interface {
	SendCode(ctx context.Context, sessionPath, phone string) (SentCode, error)
	SignIn(ctx context.Context, sessionPath, phone, code, codeHash string) (*Profile, bool, error)
	Password(ctx context.Context, sessionPath, password string) (*Profile, error)
}

type accountDriver interface {
	SendCodeForAccount(
		ctx context.Context,
		sessionPath string,
		phone string,
		accountID int64,
	) (SentCode, error)
	SignInForAccount(
		ctx context.Context,
		sessionPath string,
		phone string,
		code string,
		codeHash string,
		accountID int64,
	) (*Profile, bool, error)
	PasswordForAccount(
		ctx context.Context,
		sessionPath string,
		password string,
		accountID int64,
	) (*Profile, error)
}

type ServiceConfig struct {
	SessionDir       string
	LoginTTL         time.Duration
	OperationTimeout time.Duration
	Now              func() time.Time
	AppID            int
	AppHash          string
	ProxyProvider    accountproxy.DialProvider
}

type Challenge struct {
	LoginID    string    `json:"login_id"`
	PhoneHint  string    `json:"phone_hint"`
	CodeType   string    `json:"code_type"`
	CodeLength int       `json:"code_length"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type Result struct {
	NextStep string   `json:"next_step"`
	Profile  *Profile `json:"-"`
}

type FloodWaitError struct {
	Duration time.Duration
}

func (e *FloodWaitError) Error() string {
	return fmt.Sprintf("Telegram rate limit reached; retry after %s", e.Duration.Round(time.Second))
}

func (e *FloodWaitError) Unwrap() error {
	return ErrFloodWait
}

type Service struct {
	driver           Driver
	sessionDir       string
	loginTTL         time.Duration
	operationTimeout time.Duration
	now              func() time.Time
	qr               *QRService
	proxyProvider    accountproxy.DialProvider

	mu     sync.Mutex
	logins map[string]*loginState
}

type loginState struct {
	mu                sync.Mutex
	id                string
	phone             string
	codeHash          string
	sessionPath       string
	stage             string
	expiresAt         time.Time
	expectedAccountID int64
}

func NewService(driver Driver, config ServiceConfig) (*Service, error) {
	config.SessionDir = strings.TrimSpace(config.SessionDir)
	if config.SessionDir == "" {
		return nil, errors.New("Telegram session directory must not be empty")
	}
	if config.LoginTTL <= 0 {
		config.LoginTTL = 10 * time.Minute
	}
	if config.OperationTimeout <= 0 {
		config.OperationTimeout = 45 * time.Second
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	if err := os.MkdirAll(config.SessionDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Telegram session directory: %w", err)
	}
	if err := os.Chmod(config.SessionDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure Telegram session directory: %w", err)
	}
	pendingDir := filepath.Join(config.SessionDir, "pending")
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		return nil, fmt.Errorf("create pending session directory: %w", err)
	}
	if err := cleanupPendingSessions(pendingDir); err != nil {
		return nil, err
	}
	service := &Service{
		driver:           driver,
		sessionDir:       config.SessionDir,
		proxyProvider:    config.ProxyProvider,
		loginTTL:         config.LoginTTL,
		operationTimeout: config.OperationTimeout,
		now:              config.Now,
		logins:           make(map[string]*loginState),
	}
	if config.AppID > 0 && strings.TrimSpace(config.AppHash) != "" {
		qrService, err := NewQRServiceWithProxy(
			config.AppID,
			config.AppHash,
			config.SessionDir,
			3*time.Minute,
			config.ProxyProvider,
		)
		if err != nil {
			return nil, err
		}
		service.qr = qrService
	}
	return service, nil
}

func cleanupPendingSessions(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("list pending Telegram sessions: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() ||
			(!strings.HasSuffix(entry.Name(), ".session") && !strings.HasSuffix(entry.Name(), ".session.device")) {
			continue
		}
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil &&
			!errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale Telegram session %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func (s *Service) Configured() bool {
	return s != nil && s.driver != nil
}

func (s *Service) StartQR(ctx context.Context, expectedAccountID int64) (QRChallenge, error) {
	if s == nil || s.qr == nil {
		return QRChallenge{}, ErrNotConfigured
	}
	return s.qr.Start(ctx, expectedAccountID)
}

func (s *Service) PollQR(loginID string) (QRResult, error) {
	if s == nil || s.qr == nil {
		return QRResult{}, ErrNotConfigured
	}
	return s.qr.Poll(loginID)
}

func (s *Service) CancelQR(loginID string) error {
	if s == nil || s.qr == nil {
		return ErrNotConfigured
	}
	return s.qr.Cancel(loginID)
}

func (s *Service) SendCode(
	ctx context.Context,
	phone string,
	expectedAccountID int64,
) (Challenge, error) {
	if !s.Configured() {
		return Challenge{}, ErrNotConfigured
	}
	if expectedAccountID < 0 {
		return Challenge{}, ErrAccountMismatch
	}
	phone, err := normalizePhone(phone)
	if err != nil {
		return Challenge{}, err
	}

	loginID, err := newLoginID()
	if err != nil {
		return Challenge{}, fmt.Errorf("generate login ID: %w", err)
	}
	pendingDir := filepath.Join(s.sessionDir, "pending")
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		return Challenge{}, fmt.Errorf("create pending session directory: %w", err)
	}
	sessionPath := filepath.Join(pendingDir, loginID+".session")

	operationCtx, cancel := s.operationContext(ctx)
	defer cancel()
	var sent SentCode
	if expectedAccountID > 0 && s.proxyProvider != nil {
		driver, ok := s.driver.(accountDriver)
		if !ok {
			return Challenge{}, errors.New("Telegram driver cannot honor account proxy")
		}
		sent, err = driver.SendCodeForAccount(operationCtx, sessionPath, phone, expectedAccountID)
	} else {
		sent, err = s.driver.SendCode(operationCtx, sessionPath, phone)
	}
	if err != nil {
		removeIfPresent(sessionPath)
		return Challenge{}, err
	}
	if sent.CodeHash == "" {
		removeIfPresent(sessionPath)
		return Challenge{}, fmt.Errorf("%w: Telegram returned an empty code hash", ErrTelegram)
	}

	state := &loginState{
		id:                loginID,
		phone:             phone,
		codeHash:          sent.CodeHash,
		sessionPath:       sessionPath,
		stage:             StepCode,
		expiresAt:         s.now().Add(s.loginTTL),
		expectedAccountID: expectedAccountID,
	}
	s.mu.Lock()
	s.cleanupExpiredLocked()
	for id, existing := range s.logins {
		if existing.phone == phone {
			delete(s.logins, id)
			removeIfPresent(existing.sessionPath)
		}
	}
	s.logins[loginID] = state
	s.mu.Unlock()

	return Challenge{
		LoginID:    loginID,
		PhoneHint:  phone,
		CodeType:   sent.CodeType,
		CodeLength: sent.CodeLength,
		ExpiresAt:  state.expiresAt,
	}, nil
}

func (s *Service) SubmitCode(ctx context.Context, loginID, code string) (Result, error) {
	state, err := s.state(loginID)
	if err != nil {
		return Result{}, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stage != StepCode {
		return Result{}, ErrLoginStage
	}
	code = strings.TrimSpace(code)
	if !codePattern.MatchString(code) {
		return Result{}, ErrInvalidCode
	}

	operationCtx, cancel := s.operationContext(ctx)
	defer cancel()
	var profile *Profile
	var passwordRequired bool
	if state.expectedAccountID > 0 && s.proxyProvider != nil {
		driver, ok := s.driver.(accountDriver)
		if !ok {
			return Result{}, errors.New("Telegram driver cannot honor account proxy")
		}
		profile, passwordRequired, err = driver.SignInForAccount(
			operationCtx,
			state.sessionPath,
			state.phone,
			code,
			state.codeHash,
			state.expectedAccountID,
		)
	} else {
		profile, passwordRequired, err = s.driver.SignIn(
			operationCtx,
			state.sessionPath,
			state.phone,
			code,
			state.codeHash,
		)
	}
	if err != nil {
		if errors.Is(err, ErrCodeExpired) {
			s.removeLogin(state)
		}
		return Result{}, err
	}
	if passwordRequired {
		state.stage = StepPassword
		state.codeHash = ""
		state.expiresAt = s.now().Add(s.loginTTL)
		return Result{NextStep: StepPassword}, nil
	}
	return s.complete(state, profile)
}

func (s *Service) SubmitPassword(ctx context.Context, loginID, password string) (Result, error) {
	state, err := s.state(loginID)
	if err != nil {
		return Result{}, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.stage != StepPassword {
		return Result{}, ErrLoginStage
	}
	if password == "" || len(password) > 256 {
		return Result{}, ErrInvalidPassword
	}

	operationCtx, cancel := s.operationContext(ctx)
	defer cancel()
	var profile *Profile
	if state.expectedAccountID > 0 && s.proxyProvider != nil {
		driver, ok := s.driver.(accountDriver)
		if !ok {
			return Result{}, errors.New("Telegram driver cannot honor account proxy")
		}
		profile, err = driver.PasswordForAccount(
			operationCtx,
			state.sessionPath,
			password,
			state.expectedAccountID,
		)
	} else {
		profile, err = s.driver.Password(operationCtx, state.sessionPath, password)
	}
	if err != nil {
		return Result{}, err
	}
	return s.complete(state, profile)
}

func (s *Service) Cancel(loginID string) error {
	state, err := s.state(loginID)
	if err != nil {
		return err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	s.removeLogin(state)
	return nil
}

func (s *Service) DialContextFor(
	ctx context.Context,
	accountID int64,
) (func(context.Context, string, string) (net.Conn, error), error) {
	return accountproxy.Resolve(s.proxyProvider, ctx, accountID)
}

func (s *Service) complete(state *loginState, profile *Profile) (Result, error) {
	if profile == nil || profile.ID <= 0 {
		return Result{}, fmt.Errorf("%w: Telegram returned an invalid user", ErrTelegram)
	}
	if state.expectedAccountID > 0 && profile.ID != state.expectedAccountID {
		s.removeLogin(state)
		return Result{}, ErrAccountMismatch
	}
	if strings.TrimSpace(profile.PhoneNumber) == "" {
		profile.PhoneNumber = state.phone
	}
	accountDir := filepath.Join(s.sessionDir, "accounts")
	if err := os.MkdirAll(accountDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create account session directory: %w", err)
	}
	destination := filepath.Join(accountDir, fmt.Sprintf("%d.session", profile.ID))
	removeIfPresent(destination)
	if err := bridgeDeviceProfile(state.sessionPath, destination); err != nil {
		return Result{}, err
	}
	if err := os.Rename(state.sessionPath, destination); err != nil {
		removeIfPresent(deviceSidecarPath(destination))
		return Result{}, fmt.Errorf("persist authorized Telegram session: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return Result{}, fmt.Errorf("secure authorized Telegram session: %w", err)
	}
	s.removeLogin(state)
	return Result{NextStep: StepComplete, Profile: profile}, nil
}

func (s *Service) state(loginID string) (*loginState, error) {
	if !loginIDPattern.MatchString(loginID) {
		return nil, ErrLoginNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked()
	state, ok := s.logins[loginID]
	if !ok {
		return nil, ErrLoginNotFound
	}
	return state, nil
}

func (s *Service) removeLogin(state *loginState) {
	s.mu.Lock()
	if current, ok := s.logins[state.id]; ok && current == state {
		delete(s.logins, state.id)
	}
	s.mu.Unlock()
	removeIfPresent(state.sessionPath)
}

func (s *Service) cleanupExpiredLocked() {
	now := s.now()
	for id, state := range s.logins {
		if state.expiresAt.After(now) {
			continue
		}
		delete(s.logins, id)
		removeIfPresent(state.sessionPath)
	}
}

func (s *Service) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, s.operationTimeout)
}

func normalizePhone(phone string) (string, error) {
	phone = strings.TrimSpace(phone)
	var builder strings.Builder
	builder.Grow(len(phone) + 1)
	for i, char := range phone {
		if i == 0 && char == '+' {
			builder.WriteRune(char)
			continue
		}
		if char < '0' || char > '9' {
			return "", ErrInvalidPhone
		}
		builder.WriteRune(char)
	}
	normalized := builder.String()
	if !strings.HasPrefix(normalized, "+") {
		normalized = "+" + normalized
	}
	digits := strings.TrimPrefix(normalized, "+")
	if len(digits) < 8 || len(digits) > 15 {
		return "", ErrInvalidPhone
	}
	return normalized, nil
}

func newLoginID() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func removeIfPresent(path string) {
	if path == "" {
		return
	}
	removeSessionFiles(path)
}
