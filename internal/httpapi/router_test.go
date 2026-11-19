package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/auth"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/settings"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramauth"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
	"github.com/ThanatosXingYu/TeleNexux/internal/web"
)

var (
	testAuthOnce    sync.Once
	testAuthManager *auth.Manager
	testAuthToken   string
	testAuthCSRF    string
	testAuthErr     error
)

func testAuthentication() (*auth.Manager, string, string) {
	testAuthOnce.Do(func() {
		testAuthManager, testAuthErr = auth.NewTestManager("admin", "strong-password-123")
		if testAuthErr != nil {
			return
		}
		var session auth.Session
		testAuthToken, session, testAuthErr = testAuthManager.CreateSession()
		testAuthCSRF = session.CSRF
	})
	if testAuthErr != nil {
		panic(testAuthErr)
	}
	return testAuthManager, testAuthToken, testAuthCSRF
}

type authenticatedTestHandler struct {
	next  http.Handler
	token string
	csrf  string
}

func (h authenticatedTestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	request := r.Clone(r.Context())
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: h.token})
	request.Header.Set(auth.CSRFHeader, h.csrf)
	h.next.ServeHTTP(w, request)
}

type fakeAccountService struct {
	items     []account.Account
	created   account.Account
	createErr error
	upsertErr error
	listErr   error
	getErr    error
	updateErr error
	deleteErr error
	deletedID int64
}

func (s *fakeAccountService) Create(_ context.Context, input account.CreateInput) (account.Account, error) {
	if s.createErr != nil {
		return account.Account{}, s.createErr
	}
	s.created = account.Account{
		ID:          input.ID,
		PhoneNumber: input.PhoneNumber,
		Username:    input.Username,
		DisplayName: input.DisplayName,
		Status:      account.StatusPending,
		CreatedAt:   time.Unix(1, 0).UTC(),
		UpdatedAt:   time.Unix(1, 0).UTC(),
	}
	return s.created, nil
}

func (s *fakeAccountService) UpsertAuthorized(
	_ context.Context,
	input account.CreateInput,
) (account.Account, error) {
	if s.upsertErr != nil {
		return account.Account{}, s.upsertErr
	}
	s.created = account.Account{
		ID:          input.ID,
		PhoneNumber: input.PhoneNumber,
		Username:    input.Username,
		DisplayName: input.DisplayName,
		Status:      account.StatusOnline,
	}
	return s.created, nil
}

func (s *fakeAccountService) List(_ context.Context, _ account.Filter) ([]account.Account, error) {
	return s.items, s.listErr
}

func (s *fakeAccountService) Get(_ context.Context, _ int64) (account.Account, error) {
	if s.getErr != nil {
		return account.Account{}, s.getErr
	}
	return s.created, nil
}

func (s *fakeAccountService) UpdateStatus(
	_ context.Context,
	_ int64,
	status string,
) (account.Account, error) {
	if s.updateErr != nil {
		return account.Account{}, s.updateErr
	}
	s.created.Status = status
	return s.created, nil
}

func (s *fakeAccountService) RecordVerification(
	_ context.Context,
	_ int64,
	status string,
	reason string,
) (account.Account, error) {
	if s.updateErr != nil {
		return account.Account{}, s.updateErr
	}
	now := time.Unix(10, 0).UTC()
	s.created.Status = status
	s.created.StatusReason = reason
	s.created.LastVerifiedAt = &now
	return s.created, nil
}

func (s *fakeAccountService) Update(
	_ context.Context,
	id int64,
	input account.UpdateInput,
) (account.Account, error) {
	if s.updateErr != nil {
		return account.Account{}, s.updateErr
	}
	s.created.ID = id
	s.created.PhoneNumber = input.PhoneNumber
	s.created.Username = input.Username
	s.created.DisplayName = input.DisplayName
	return s.created, nil
}

func (s *fakeAccountService) Delete(_ context.Context, id int64) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.deletedID = id
	return nil
}

type fakeTelegramAuthService struct {
	configured  bool
	challenge   telegramauth.Challenge
	result      telegramauth.Result
	sendErr     error
	submitErr   error
	sendStarted chan struct{}
	sendRelease <-chan struct{}
}

func (s *fakeTelegramAuthService) Configured() bool {
	return s.configured
}

func (s *fakeTelegramAuthService) SendCode(
	ctx context.Context,
	_ string,
) (telegramauth.Challenge, error) {
	if s.sendStarted != nil {
		s.sendStarted <- struct{}{}
	}
	if s.sendRelease != nil {
		select {
		case <-ctx.Done():
			return telegramauth.Challenge{}, ctx.Err()
		case <-s.sendRelease:
		}
	}
	return s.challenge, s.sendErr
}

func (s *fakeTelegramAuthService) SubmitCode(
	_ context.Context,
	_ string,
	_ string,
) (telegramauth.Result, error) {
	return s.result, s.submitErr
}

func (s *fakeTelegramAuthService) SubmitPassword(
	_ context.Context,
	_ string,
	_ string,
) (telegramauth.Result, error) {
	return s.result, s.submitErr
}

func (s *fakeTelegramAuthService) Cancel(_ string) error {
	return nil
}

func (s *fakeTelegramAuthService) StartQR(_ context.Context) (telegramauth.QRChallenge, error) {
	return telegramauth.QRChallenge{}, nil
}

func (s *fakeTelegramAuthService) PollQR(_ string) (telegramauth.QRResult, error) {
	return telegramauth.QRResult{}, nil
}

func (s *fakeTelegramAuthService) CancelQR(_ string) error {
	return nil
}

type fakeTelegramChatService struct {
	chats       telegramchat.ChatPage
	messages    telegramchat.MessagePage
	chatsErr    error
	messagesErr error
	validateErr error
	deleteErr   error
	sessionID   int64
	deleteScope bool
}

func (s *fakeTelegramChatService) ValidateSession(_ context.Context, _ int64) error {
	return s.validateErr
}

func (s *fakeTelegramChatService) DeleteSession(accountID int64) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.sessionID = accountID
	return nil
}

func (s *fakeTelegramChatService) Chats(
	_ context.Context,
	_ int64,
	_ int,
) (telegramchat.ChatPage, error) {
	return s.chats, s.chatsErr
}

func (s *fakeTelegramChatService) Messages(
	_ context.Context,
	_ int64,
	_ string,
	_ int64,
	_ int,
	_ int,
) (telegramchat.MessagePage, error) {
	return s.messages, s.messagesErr
}

func (s *fakeTelegramChatService) SearchChats(
	_ context.Context,
	_ int64,
	_ string,
	_ int,
) (telegramchat.ChatPage, error) {
	return s.chats, s.chatsErr
}

func (s *fakeTelegramChatService) CurrentProfile(
	_ context.Context,
	_ int64,
) (telegramchat.Profile, error) {
	return telegramchat.Profile{}, s.messagesErr
}

func (s *fakeTelegramChatService) UpdateCurrentProfile(
	_ context.Context,
	_ int64,
	_ telegramchat.UpdateProfileInput,
) (telegramchat.Profile, error) {
	return telegramchat.Profile{}, s.messagesErr
}

func (s *fakeTelegramChatService) ChatProfile(
	_ context.Context,
	_ int64,
	_ string,
	_ int64,
) (telegramchat.Profile, error) {
	return telegramchat.Profile{}, s.messagesErr
}

func (s *fakeTelegramChatService) Avatar(
	_ context.Context,
	_ int64,
	_ string,
	_ int64,
) (telegramchat.Avatar, error) {
	return telegramchat.Avatar{}, s.messagesErr
}

func (s *fakeTelegramChatService) DeleteMessages(
	_ context.Context,
	_ int64,
	_ string,
	_ int64,
	_ []int,
	revoke bool,
) error {
	s.deleteScope = revoke
	return s.messagesErr
}

func (s *fakeTelegramChatService) ForwardMessages(
	_ context.Context,
	_ int64,
	_ string,
	_ int64,
	_ string,
	_ int64,
	_ []int,
) error {
	return s.messagesErr
}

type fakeSettingsService struct {
	values          settings.Values
	restartRequired bool
	saveErr         error
}

func (s *fakeSettingsService) Snapshot() settings.Values {
	return s.values
}

func (s *fakeSettingsService) Save(values settings.Values) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.values = values
	s.restartRequired = true
	return nil
}

func (s *fakeSettingsService) RestartRequired() bool {
	return s.restartRequired
}

func newTestHandler(service AccountService) http.Handler {
	return newTestHandlerWithTelegram(service, &fakeTelegramAuthService{})
}

func newRawTestHandler(t *testing.T, manager *auth.Manager) http.Handler {
	t.Helper()
	webHandler, err := web.New()
	if err != nil {
		t.Fatalf("web.New() error = %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(
		&fakeAccountService{},
		&fakeTelegramAuthService{},
		nil,
		nil,
		manager,
		webHandler.Handler(),
		logger,
		nil,
		nil,
		nil,
	).Handler()
}

func newTestHandlerWithTelegram(
	service AccountService,
	telegram TelegramAuthService,
) http.Handler {
	webHandler, err := web.New()
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, token, csrf := testAuthentication()
	return authenticatedTestHandler{
		next: New(
			service,
			telegram,
			nil,
			nil,
			manager,
			webHandler.Handler(),
			logger,
			nil,
			nil,
			nil,
		).Handler(),
		token: token,
		csrf:  csrf,
	}
}

func TestCreateAccount(t *testing.T) {
	service := &fakeAccountService{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(
		`{"id":42,"phone_number":"13800138000","username":"alice","display_name":"Alice"}`,
	))
	recorder := httptest.NewRecorder()

	newTestHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if service.created.ID != 42 {
		t.Errorf("created ID = %d, want 42", service.created.ID)
	}
}

func TestManagementAPIsRequireAuthentication(t *testing.T) {
	manager, err := auth.NewTestManager("admin", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
	recorder := httptest.NewRecorder()
	newRawTestHandler(t, manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", recorder.Code, recorder.Body.String())
	}
}

func TestManagementMutationsRequireCSRF(t *testing.T) {
	manager, err := auth.NewTestManager("admin", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	token, _, err := manager.CreateSession()
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	newRawTestHandler(t, manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", recorder.Code, recorder.Body.String())
	}
}

func TestAdministratorLoginCreatesSession(t *testing.T) {
	manager, err := auth.NewTestManager("admin", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"username":"admin","password":"strong-password-123"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:50000"
	recorder := httptest.NewRecorder()
	newRawTestHandler(t, manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"authenticated":true`) {
		t.Fatalf("body = %s, want authenticated session", recorder.Body.String())
	}
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("login did not set a session cookie")
	}
}

func TestCreateAccountRejectsUnknownFields(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(
		`{"id":42,"phone_number":"13800138000","session":"secret"}`,
	))
	recorder := httptest.NewRecorder()

	newTestHandler(&fakeAccountService{}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestListAccounts(t *testing.T) {
	service := &fakeAccountService{items: []account.Account{{ID: 5, Status: account.StatusPaused}}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts?status=paused&limit=10", nil)
	recorder := httptest.NewRecorder()

	newTestHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var body struct {
		Items []account.Account `json:"items"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != 5 {
		t.Errorf("items = %+v, want account 5", body.Items)
	}
}

func TestListAccountsRejectsInvalidQuery(t *testing.T) {
	tests := []string{
		"/api/v1/accounts?status=offline",
		"/api/v1/accounts?limit=101",
		"/api/v1/accounts?offset=-1",
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			recorder := httptest.NewRecorder()
			newTestHandler(&fakeAccountService{}).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
			}
		})
	}
}

func TestGetAccountNotFound(t *testing.T) {
	service := &fakeAccountService{getErr: account.ErrNotFound}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/9", nil)
	recorder := httptest.NewRecorder()

	newTestHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
}

func TestUpdateAccount(t *testing.T) {
	service := &fakeAccountService{}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/accounts/42",
		strings.NewReader(`{"phone_number":"+15550001005","username":"updated_user","display_name":"Updated"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if service.created.ID != 42 ||
		service.created.PhoneNumber != "+15550001005" ||
		service.created.Username != "updated_user" {
		t.Fatalf("updated account = %+v", service.created)
	}
}

func TestDeleteAccountRemovesSessionAndAccount(t *testing.T) {
	accounts := &fakeAccountService{}
	chats := &fakeTelegramChatService{}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/42", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", recorder.Code, recorder.Body.String())
	}
	if chats.sessionID != 42 || accounts.deletedID != 42 {
		t.Fatalf("session ID = %d, deleted ID = %d, want 42", chats.sessionID, accounts.deletedID)
	}
}

func TestDeleteAccountStopsWhenSessionCleanupFails(t *testing.T) {
	accounts := &fakeAccountService{}
	chats := &fakeTelegramChatService{deleteErr: errors.New("unsafe session path")}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/42", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", recorder.Code, recorder.Body.String())
	}
	if accounts.deletedID != 0 {
		t.Fatal("account must not be deleted when session cleanup fails")
	}
}

func TestListAccountsHidesInfrastructureErrors(t *testing.T) {
	service := &fakeAccountService{listErr: errors.New("disk is unavailable")}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
	recorder := httptest.NewRecorder()

	newTestHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if strings.Contains(recorder.Body.String(), "disk is unavailable") {
		t.Fatal("response must not expose infrastructure details")
	}
}

func TestTelegramAuthConfigReportsConfigurationState(t *testing.T) {
	telegram := &fakeTelegramAuthService{configured: true}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/telegram/auth/config", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithTelegram(&fakeAccountService{}, telegram).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), `"configured":true`) {
		t.Fatalf("response = %s, want configured true", recorder.Body.String())
	}
}

func TestSendTelegramCodeRequiresJSON(t *testing.T) {
	telegram := &fakeTelegramAuthService{configured: true}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/telegram/auth/send-code",
		strings.NewReader(`{"phone_number":"+15550001001"}`))
	recorder := httptest.NewRecorder()

	newTestHandlerWithTelegram(&fakeAccountService{}, telegram).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnsupportedMediaType)
	}
}

func TestTelegramAuthRateLimitRejectsRepeatedRequests(t *testing.T) {
	telegram := &fakeTelegramAuthService{
		configured: true,
		challenge:  telegramauth.Challenge{LoginID: "login-id"},
	}
	handler := newTestHandlerWithTelegram(&fakeAccountService{}, telegram)
	for index := 0; index < telegramAuthAttemptLimit; index++ {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/telegram/auth/send-code",
			strings.NewReader(`{"phone_number":"+15550001001"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.RemoteAddr = "203.0.113.10:50000"
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("request %d status = %d, want %d; body: %s",
				index+1, recorder.Code, http.StatusCreated, recorder.Body.String())
		}
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telegram/auth/send-code",
		strings.NewReader(`{"phone_number":"+15550001001"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.10:50000"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; body: %s",
			recorder.Code, http.StatusTooManyRequests, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"telegram_auth_rate_limited"`) {
		t.Fatalf("body = %s, want telegram_auth_rate_limited", recorder.Body.String())
	}
}

func TestTelegramAuthConcurrencyLimit(t *testing.T) {
	started := make(chan struct{}, telegramAuthConcurrency)
	release := make(chan struct{})
	telegram := &fakeTelegramAuthService{
		configured:  true,
		challenge:   telegramauth.Challenge{LoginID: "login-id"},
		sendStarted: started,
		sendRelease: release,
	}
	handler := newTestHandlerWithTelegram(&fakeAccountService{}, telegram)
	responses := make(chan int, telegramAuthConcurrency)
	for index := 0; index < telegramAuthConcurrency; index++ {
		go func() {
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/telegram/auth/send-code",
				strings.NewReader(`{"phone_number":"+15550001001"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			request.RemoteAddr = "203.0.113.11:50000"
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			responses <- recorder.Code
		}()
	}
	for index := 0; index < telegramAuthConcurrency; index++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for Telegram authentication to start")
		}
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telegram/auth/send-code",
		strings.NewReader(`{"phone_number":"+15550001001"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "203.0.113.11:50000"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; body: %s",
			recorder.Code, http.StatusTooManyRequests, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"telegram_auth_busy"`) {
		t.Fatalf("body = %s, want telegram_auth_busy", recorder.Body.String())
	}

	close(release)
	for index := 0; index < telegramAuthConcurrency; index++ {
		select {
		case status := <-responses:
			if status != http.StatusCreated {
				t.Fatalf("concurrent status = %d, want %d", status, http.StatusCreated)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for concurrent authentication")
		}
	}
}

func TestTelegramAuthTimeoutHasDistinctResponse(t *testing.T) {
	telegram := &fakeTelegramAuthService{
		configured: true,
		sendErr:    context.DeadlineExceeded,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telegram/auth/send-code",
		strings.NewReader(`{"phone_number":"+15550001001"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithTelegram(&fakeAccountService{}, telegram).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d; body: %s",
			recorder.Code, http.StatusGatewayTimeout, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"telegram_timeout"`) {
		t.Fatalf("body = %s, want telegram_timeout", recorder.Body.String())
	}
}

func TestTelegramCodeSubmissionSavesOnlineAccount(t *testing.T) {
	accounts := &fakeAccountService{}
	telegram := &fakeTelegramAuthService{
		configured: true,
		result: telegramauth.Result{
			NextStep: telegramauth.StepComplete,
			Profile: &telegramauth.Profile{
				ID:          9001,
				PhoneNumber: "+15550001001",
				Username:    "telenexus_test",
				DisplayName: "Test Account",
			},
		},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telegram/auth/login-id/code",
		strings.NewReader(`{"code":"12345"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithTelegram(accounts, telegram).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if accounts.created.Status != account.StatusOnline || accounts.created.ID != 9001 {
		t.Fatalf("saved account = %+v, want online Telegram account", accounts.created)
	}
}

func TestTelegramInvalidPasswordIsSanitized(t *testing.T) {
	telegram := &fakeTelegramAuthService{
		configured: true,
		submitErr:  telegramauth.ErrInvalidPassword,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telegram/auth/login-id/password",
		strings.NewReader(`{"password":"secret-value"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithTelegram(&fakeAccountService{}, telegram).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
	if strings.Contains(recorder.Body.String(), "secret-value") {
		t.Fatal("response must not expose the two-step password")
	}
}

func TestSettingsResponseNeverEchoesAPIHash(t *testing.T) {
	settingsService := &fakeSettingsService{values: settings.Values{
		AppEnvironment:  "production",
		LogLevel:        "warn",
		TelegramAPIID:   12345,
		TelegramAPIHash: "highly-secret-hash",
	}}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(
		&fakeAccountService{},
		&fakeTelegramAuthService{},
		nil,
		settingsService,
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "highly-secret-hash") {
		t.Fatal("API hash was echoed in the response")
	}
	if !strings.Contains(recorder.Body.String(), `"telegram_api_hash_configured":true`) {
		t.Fatalf("response = %s, want configured indicator", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"app_env":"production"`) ||
		!strings.Contains(recorder.Body.String(), `"log_level":"warn"`) {
		t.Fatalf("response = %s, want runtime configuration", recorder.Body.String())
	}
}

func TestUpdateSettingsRequiresJSON(t *testing.T) {
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{}`))
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(
		&fakeAccountService{},
		&fakeTelegramAuthService{},
		nil,
		&fakeSettingsService{},
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", recorder.Code)
	}
}

func TestUpdateSettingsSavesRuntimeConfiguration(t *testing.T) {
	settingsService := &fakeSettingsService{values: settings.Values{LogLevel: "info"}}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/settings",
		strings.NewReader(`{"app_env":"staging","log_level":"warn"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(
		&fakeAccountService{},
		&fakeTelegramAuthService{},
		nil,
		settingsService,
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if settingsService.values.AppEnvironment != "staging" || settingsService.values.LogLevel != "warn" {
		t.Fatalf("saved settings = %+v, want staging/warn", settingsService.values)
	}
	if !settingsService.restartRequired {
		t.Fatal("saving settings must require a restart")
	}
}

func TestListChatsSanitizesSessionErrors(t *testing.T) {
	accounts := &fakeAccountService{created: account.Account{ID: 42, Status: account.StatusOnline}}
	chats := &fakeTelegramChatService{chatsErr: telegramchat.ErrSessionMissing}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/42/chats", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "session file") {
		t.Fatal("response must not expose session paths")
	}
}

func TestListChatsReportsInvalidSession(t *testing.T) {
	accounts := &fakeAccountService{created: account.Account{ID: 42, Status: account.StatusOnline}}
	chats := &fakeTelegramChatService{chatsErr: telegramchat.ErrSessionInvalid}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/42/chats", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"session_invalid"`) {
		t.Fatalf("response code = %s, want session_invalid", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "授权已失效") {
		t.Fatalf("response message = %s, want an actionable message", recorder.Body.String())
	}
}

func TestRefreshAccountsPersistsExpiredSession(t *testing.T) {
	accounts := &fakeAccountService{
		items:   []account.Account{{ID: 42, Status: account.StatusOnline}},
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	chats := &fakeTelegramChatService{validateErr: telegramchat.ErrSessionInvalid}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if accounts.created.Status != account.StatusError {
		t.Fatalf("account status = %q, want %q", accounts.created.Status, account.StatusError)
	}
}

func TestMonitorNotificationChannelAPI(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "operations.db"),
	)
	if err != nil {
		t.Fatalf("open operations service: %v", err)
	}
	t.Cleanup(func() {
		if err := operationsService.Close(); err != nil {
			t.Errorf("close operations service: %v", err)
		}
	})
	webHandler, err := web.New()
	if err != nil {
		t.Fatalf("create web handler: %v", err)
	}
	manager, token, csrf := testAuthentication()
	handler := authenticatedTestHandler{
		next: New(
			nil,
			nil,
			nil,
			nil,
			manager,
			webHandler.Handler(),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			nil,
			operationsService,
			nil,
		).Handler(),
		token: token,
		csrf:  csrf,
	}

	channelsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/monitor/channels", nil)
	channelsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(channelsRecorder, channelsRequest)
	if channelsRecorder.Code != http.StatusOK ||
		!strings.Contains(channelsRecorder.Body.String(), `"name":"站内通知"`) {
		t.Fatalf("channels response = %d %s",
			channelsRecorder.Code, channelsRecorder.Body.String())
	}

	ruleRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/monitor/rules",
		strings.NewReader(`{
			"name":"Deploy alerts",
			"account_id":7,
			"peer_type":"chat",
			"peer_id":42,
			"peer_title":"Ops",
			"notification_channel_id":1,
			"keywords":["deploy"]
		}`),
	)
	ruleRequest.Header.Set("Content-Type", "application/json")
	ruleRecorder := httptest.NewRecorder()
	handler.ServeHTTP(ruleRecorder, ruleRequest)
	if ruleRecorder.Code != http.StatusCreated ||
		!strings.Contains(ruleRecorder.Body.String(), `"notification_channel_name":"站内通知"`) {
		t.Fatalf("create rule response = %d %s",
			ruleRecorder.Code, ruleRecorder.Body.String())
	}
}

func TestDeleteChatMessagesUsesRequestedScope(t *testing.T) {
	for _, test := range []struct {
		name       string
		scope      string
		wantRevoke bool
	}{
		{name: "delete for me", scope: "me", wantRevoke: false},
		{name: "delete for everyone", scope: "all", wantRevoke: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			accounts := &fakeAccountService{
				created: account.Account{ID: 42, Status: account.StatusOnline},
			}
			chats := &fakeTelegramChatService{}
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/accounts/42/chats/user/99/messages/delete",
				strings.NewReader(`{"message_ids":[1,2],"scope":"`+test.scope+`"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()

			newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
				ServeHTTP(recorder, request)

			if recorder.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204; body: %s", recorder.Code, recorder.Body.String())
			}
			if chats.deleteScope != test.wantRevoke {
				t.Fatalf("delete scope revoke = %v, want %v", chats.deleteScope, test.wantRevoke)
			}
		})
	}
}

func TestDeleteChatMessagesRejectsUnknownScope(t *testing.T) {
	accounts := &fakeAccountService{
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	chats := &fakeTelegramChatService{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/accounts/42/chats/user/99/messages/delete",
		strings.NewReader(`{"message_ids":[1],"scope":"everyone"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body: %s", recorder.Code, recorder.Body.String())
	}
}

func newTestHandlerWithDependencies(
	accounts AccountService,
	telegram TelegramAuthService,
	chats TelegramChatService,
	settingsService SettingsService,
) http.Handler {
	webHandler, err := web.New()
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, token, csrf := testAuthentication()
	return authenticatedTestHandler{
		next: New(
			accounts,
			telegram,
			chats,
			settingsService,
			manager,
			webHandler.Handler(),
			logger,
			nil,
			nil,
			nil,
		).Handler(),
		token: token,
		csrf:  csrf,
	}
}
