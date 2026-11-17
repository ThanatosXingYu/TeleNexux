// Package telegramauth manages short-lived Telegram MTProto login attempts.
package telegramauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	StepCode     = "code"
	StepPassword = "password"
	StepComplete = "complete"
)

var (
	ErrNotConfigured    = errors.New("Telegram API credentials are not configured")
	ErrInvalidPhone     = errors.New("phone number is invalid")
	ErrInvalidCode      = errors.New("verification code is invalid")
	ErrCodeExpired      = errors.New("verification code has expired")
	ErrInvalidPassword  = errors.New("two-step verification password is invalid")
	ErrLoginNotFound    = errors.New("login attempt was not found or has expired")
	ErrLoginStage       = errors.New("login attempt is not at the requested step")
	ErrCodeSourceReject = errors.New("code source is not allowed")
	ErrCodeNotReady     = errors.New("verification code is not available yet")
	ErrFloodWait        = errors.New("Telegram rate limit reached")
	ErrTelegram         = errors.New("Telegram authentication failed")
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

type CodeFetcher interface {
	Validate(source string) error
	Fetch(ctx context.Context, source string, expectedLength int) (string, error)
}

type ServiceConfig struct {
	SessionDir       string
	LoginTTL         time.Duration
	OperationTimeout time.Duration
	Now              func() time.Time
}

type Challenge struct {
	LoginID      string    `json:"login_id"`
	PhoneHint    string    `json:"phone_hint"`
	CodeType     string    `json:"code_type"`
	CodeLength   int       `json:"code_length"`
	ExpiresAt    time.Time `json:"expires_at"`
	CanFetchCode bool      `json:"can_fetch_code"`
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
	fetcher          CodeFetcher
	sessionDir       string
	loginTTL         time.Duration
	operationTimeout time.Duration
	now              func() time.Time

	mu     sync.Mutex
	logins map[string]*loginState
}

type loginState struct {
	mu          sync.Mutex
	id          string
	phone       string
	codeHash    string
	sessionPath string
	codeSource  string
	codeLength  int
	stage       string
	expiresAt   time.Time
}

func NewService(driver Driver, fetcher CodeFetcher, config ServiceConfig) (*Service, error) {
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
	return &Service{
		driver:           driver,
		fetcher:          fetcher,
		sessionDir:       config.SessionDir,
		loginTTL:         config.LoginTTL,
		operationTimeout: config.OperationTimeout,
		now:              config.Now,
		logins:           make(map[string]*loginState),
	}, nil
}

func (s *Service) Configured() bool {
	return s != nil && s.driver != nil
}

func (s *Service) SendCode(ctx context.Context, phone, codeSource string) (Challenge, error) {
	if !s.Configured() {
		return Challenge{}, ErrNotConfigured
	}
	phone, err := normalizePhone(phone)
	if err != nil {
		return Challenge{}, err
	}
	codeSource = strings.TrimSpace(codeSource)
	if codeSource != "" {
		if s.fetcher == nil {
			return Challenge{}, ErrCodeSourceReject
		}
		if err := s.fetcher.Validate(codeSource); err != nil {
			return Challenge{}, fmt.Errorf("%w: %v", ErrCodeSourceReject, err)
		}
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
	sent, err := s.driver.SendCode(operationCtx, sessionPath, phone)
	if err != nil {
		removeIfPresent(sessionPath)
		return Challenge{}, err
	}
	if sent.CodeHash == "" {
		removeIfPresent(sessionPath)
		return Challenge{}, fmt.Errorf("%w: Telegram returned an empty code hash", ErrTelegram)
	}

	state := &loginState{
		id:          loginID,
		phone:       phone,
		codeHash:    sent.CodeHash,
		sessionPath: sessionPath,
		codeSource:  codeSource,
		codeLength:  sent.CodeLength,
		stage:       StepCode,
		expiresAt:   s.now().Add(s.loginTTL),
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
		LoginID:      loginID,
		PhoneHint:    maskPhone(phone),
		CodeType:     sent.CodeType,
		CodeLength:   sent.CodeLength,
		ExpiresAt:    state.expiresAt,
		CanFetchCode: codeSource != "",
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
	profile, passwordRequired, err := s.driver.SignIn(
		operationCtx,
		state.sessionPath,
		state.phone,
		code,
		state.codeHash,
	)
	if err != nil {
		if errors.Is(err, ErrCodeExpired) {
			s.removeLogin(state)
		}
		return Result{}, err
	}
	if passwordRequired {
		state.stage = StepPassword
		state.codeHash = ""
		state.codeSource = ""
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
	profile, err := s.driver.Password(operationCtx, state.sessionPath, password)
	if err != nil {
		return Result{}, err
	}
	return s.complete(state, profile)
}

func (s *Service) FetchCode(ctx context.Context, loginID string) (Result, error) {
	state, err := s.state(loginID)
	if err != nil {
		return Result{}, err
	}
	state.mu.Lock()
	if state.stage != StepCode {
		state.mu.Unlock()
		return Result{}, ErrLoginStage
	}
	source := state.codeSource
	length := state.codeLength
	state.mu.Unlock()
	if source == "" || s.fetcher == nil {
		return Result{}, ErrCodeSourceReject
	}

	operationCtx, cancel := s.operationContext(ctx)
	defer cancel()
	code, err := s.fetcher.Fetch(operationCtx, source, length)
	if err != nil {
		return Result{}, err
	}
	return s.SubmitCode(ctx, loginID, code)
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

func (s *Service) complete(state *loginState, profile *Profile) (Result, error) {
	if profile == nil || profile.ID <= 0 {
		return Result{}, fmt.Errorf("%w: Telegram returned an invalid user", ErrTelegram)
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
	if err := os.Rename(state.sessionPath, destination); err != nil {
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

func maskPhone(phone string) string {
	digits := strings.TrimPrefix(phone, "+")
	if len(digits) <= 4 {
		return "••••"
	}
	return "••••••" + digits[len(digits)-4:]
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
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}
