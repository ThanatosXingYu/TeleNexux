package telegramauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"rsc.io/qr"
)

const (
	QRStepPending  = "pending"
	QRStepComplete = "complete"
)

type QRChallenge struct {
	LoginID   string    `json:"login_id"`
	NextStep  string    `json:"next_step"`
	Image     string    `json:"image,omitempty"`
	ExpiresAt time.Time `json:"expires_at"`
}

type QRResult struct {
	NextStep string   `json:"next_step"`
	Image    string   `json:"image,omitempty"`
	Profile  *Profile `json:"-"`
}

type QRService struct {
	appID      int
	appHash    string
	sessionDir string
	loginTTL   time.Duration

	mu     sync.Mutex
	logins map[string]*qrLoginState
}

type qrLoginState struct {
	mu          sync.Mutex
	id          string
	sessionPath string
	stage       string
	image       string
	expiresAt   time.Time
	profile     *Profile
	err         error
	ready       chan struct{}
	readyOnce   sync.Once
	cancel      context.CancelFunc
}

func NewQRService(appID int, appHash, sessionDir string, loginTTL time.Duration) (*QRService, error) {
	if appID <= 0 || strings.TrimSpace(appHash) == "" {
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(sessionDir) == "" {
		return nil, errors.New("Telegram session directory must not be empty")
	}
	if loginTTL <= 0 {
		loginTTL = 3 * time.Minute
	}
	return &QRService{
		appID:      appID,
		appHash:    strings.TrimSpace(appHash),
		sessionDir: sessionDir,
		loginTTL:   loginTTL,
		logins:     make(map[string]*qrLoginState),
	}, nil
}

func (s *QRService) Start(ctx context.Context) (QRChallenge, error) {
	if s == nil {
		return QRChallenge{}, ErrNotConfigured
	}
	loginID, err := newLoginID()
	if err != nil {
		return QRChallenge{}, fmt.Errorf("generate QR login ID: %w", err)
	}
	pendingDir := filepath.Join(s.sessionDir, "pending")
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		return QRChallenge{}, fmt.Errorf("create QR login directory: %w", err)
	}
	loginCtx, cancel := context.WithTimeout(context.Background(), s.loginTTL)
	state := &qrLoginState{
		id:          loginID,
		sessionPath: filepath.Join(pendingDir, "qr-"+loginID+".session"),
		stage:       QRStepPending,
		expiresAt:   time.Now().Add(s.loginTTL),
		ready:       make(chan struct{}),
		cancel:      cancel,
	}
	s.mu.Lock()
	s.cleanupExpiredLocked()
	s.logins[loginID] = state
	s.mu.Unlock()

	go s.run(loginCtx, state)
	select {
	case <-state.ready:
		snapshot := state.snapshot()
		if snapshot.err != nil {
			return QRChallenge{}, snapshot.err
		}
		return QRChallenge{
			LoginID:   loginID,
			NextStep:  QRStepPending,
			Image:     snapshot.image,
			ExpiresAt: state.expiresAt,
		}, nil
	case <-ctx.Done():
		s.Cancel(loginID)
		return QRChallenge{}, ctx.Err()
	}
}

func (s *QRService) Poll(loginID string) (QRResult, error) {
	state, err := s.state(loginID)
	if err != nil {
		return QRResult{}, err
	}
	snapshot := state.snapshot()
	if snapshot.err != nil {
		return QRResult{}, snapshot.err
	}
	if snapshot.stage == QRStepComplete {
		return QRResult{NextStep: QRStepComplete, Profile: snapshot.profile}, nil
	}
	return QRResult{NextStep: QRStepPending, Image: snapshot.image}, nil
}

func (s *QRService) Cancel(loginID string) error {
	state, err := s.state(loginID)
	if err != nil {
		return err
	}
	state.mu.Lock()
	state.cancel()
	state.err = ErrLoginNotFound
	state.mu.Unlock()
	state.closeReady()
	s.mu.Lock()
	if current, ok := s.logins[loginID]; ok && current == state {
		delete(s.logins, loginID)
	}
	s.mu.Unlock()
	removeIfPresent(state.sessionPath)
	return nil
}

func (s *QRService) run(ctx context.Context, state *qrLoginState) {
	if err := PrepareDeviceSession(state.sessionPath); err != nil {
		state.fail(fmt.Errorf("prepare QR login device: %w", err))
		return
	}
	dispatcher := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(dispatcher)
	client := telegram.NewClient(s.appID, s.appHash, telegram.Options{
		SessionStorage:  &session.FileStorage{Path: state.sessionPath},
		Device:          DeviceForSession(state.sessionPath),
		UpdateHandler:   dispatcher,
		DialTimeout:     15 * time.Second,
		ExchangeTimeout: 15 * time.Second,
	})
	var profile *Profile
	err := client.Run(ctx, func(runCtx context.Context) error {
		authorization, err := client.QR().Auth(
			runCtx,
			loggedIn,
			func(_ context.Context, token qrlogin.Token) error {
				qrPNG, err := renderQRPNG(token.URL())
				if err != nil {
					return err
				}
				var encoded bytes.Buffer
				if _, err := encoded.Write(qrPNG); err != nil {
					return fmt.Errorf("buffer Telegram QR code: %w", err)
				}
				state.setImage("data:image/png;base64," +
					base64.StdEncoding.EncodeToString(encoded.Bytes()))
				return nil
			},
		)
		if err != nil {
			return err
		}
		if authorization == nil {
			return fmt.Errorf("%w: QR authorization returned no result", ErrTelegram)
		}
		self, err := client.Self(runCtx)
		if err != nil {
			return fmt.Errorf("%w: load QR login profile: %v", ErrTelegram, err)
		}
		profile = profileFromUser(self, "")
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			state.fail(ErrLoginNotFound)
			return
		}
		if wait, ok := tgerr.AsFloodWait(err); ok {
			state.fail(&FloodWaitError{Duration: wait})
			return
		}
		state.fail(fmt.Errorf("%w: QR login failed", ErrTelegram))
		return
	}
	if profile == nil || profile.ID <= 0 {
		state.fail(fmt.Errorf("%w: QR login returned an invalid profile", ErrTelegram))
		return
	}
	_, err = persistSession(state.sessionPath, s.sessionDir, profile)
	if err != nil {
		state.fail(err)
		return
	}
	state.complete(profile)
}

func renderQRPNG(loginURL string) ([]byte, error) {
	code, err := qr.Encode(loginURL, qr.M)
	if err != nil {
		return nil, fmt.Errorf("encode Telegram QR code: %w", err)
	}
	return code.PNG(), nil
}

func (s *QRService) state(loginID string) (*qrLoginState, error) {
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

func (s *QRService) cleanupExpiredLocked() {
	now := time.Now()
	for id, state := range s.logins {
		if now.Before(state.expiresAt) {
			continue
		}
		state.cancel()
		delete(s.logins, id)
		removeIfPresent(state.sessionPath)
	}
}

type qrStateSnapshot struct {
	stage   string
	image   string
	profile *Profile
	err     error
}

func (s *qrLoginState) snapshot() qrStateSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return qrStateSnapshot{
		stage:   s.stage,
		image:   s.image,
		profile: s.profile,
		err:     s.err,
	}
}

func (s *qrLoginState) setImage(image string) {
	s.mu.Lock()
	s.image = image
	s.mu.Unlock()
	s.closeReady()
}

func (s *qrLoginState) complete(profile *Profile) {
	s.mu.Lock()
	s.stage = QRStepComplete
	s.profile = profile
	s.image = ""
	s.mu.Unlock()
	s.closeReady()
}

func (s *qrLoginState) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.closeReady()
	removeIfPresent(s.sessionPath)
}

func (s *qrLoginState) closeReady() {
	s.readyOnce.Do(func() {
		close(s.ready)
	})
}

func profileFromUser(user *tg.User, fallbackPhone string) *Profile {
	if user == nil {
		return nil
	}
	firstName, _ := user.GetFirstName()
	lastName, _ := user.GetLastName()
	username, _ := user.GetUsername()
	phone, _ := user.GetPhone()
	if phone == "" {
		phone = fallbackPhone
	}
	displayName := strings.TrimSpace(strings.Join([]string{firstName, lastName}, " "))
	if displayName == "" {
		displayName = username
	}
	return &Profile{
		ID:          user.GetID(),
		PhoneNumber: phone,
		Username:    username,
		DisplayName: displayName,
	}
}

func persistSession(source, sessionDir string, profile *Profile) (string, error) {
	accountDir := filepath.Join(sessionDir, "accounts")
	if err := os.MkdirAll(accountDir, 0o700); err != nil {
		return "", fmt.Errorf("create account session directory: %w", err)
	}
	destination := filepath.Join(accountDir, fmt.Sprintf("%d.session", profile.ID))
	removeIfPresent(destination)
	if err := bridgeDeviceProfile(source, destination); err != nil {
		return "", err
	}
	if err := os.Rename(source, destination); err != nil {
		removeIfPresent(deviceSidecarPath(destination))
		return "", fmt.Errorf("persist authorized Telegram session: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return "", fmt.Errorf("secure authorized Telegram session: %w", err)
	}
	return destination, nil
}
