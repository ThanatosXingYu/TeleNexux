package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/auth"
	"github.com/ThanatosXingYu/TeleNexux/internal/monitorscheduler"
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

type fakeMonitorRunner struct {
	status monitorscheduler.Status
}

func (f fakeMonitorRunner) RunOnce(context.Context) error {
	return nil
}

func (f fakeMonitorRunner) Status() monitorscheduler.Status {
	return f.status
}

func (h authenticatedTestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	request := r.Clone(r.Context())
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: h.token})
	request.Header.Set(auth.CSRFHeader, h.csrf)
	h.next.ServeHTTP(w, request)
}

type fakeAccountService struct {
	items       []account.Account
	created     account.Account
	createErr   error
	upsertErr   error
	listErr     error
	listFilter  account.Filter
	getErr      error
	updateErr   error
	deleteErr   error
	scheduledID int64
	restoredID  int64
	verifyCalls int
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

func (s *fakeAccountService) List(
	_ context.Context,
	filter account.Filter,
) ([]account.Account, error) {
	s.listFilter = filter
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
	s.verifyCalls++
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

func (s *fakeAccountService) ScheduleDeletion(
	_ context.Context,
	id int64,
) (account.Account, error) {
	if s.deleteErr != nil {
		return account.Account{}, s.deleteErr
	}
	now := time.Unix(20, 0).UTC()
	effectiveAt := now.Add(24 * time.Hour)
	s.scheduledID = id
	s.created.ID = id
	s.created.PurgeState = account.PurgeStateScheduled
	s.created.DeletionRequestedAt = &now
	s.created.DeletionEffectiveAt = &effectiveAt
	return s.created, nil
}

func (s *fakeAccountService) Restore(
	_ context.Context,
	id int64,
) (account.Account, error) {
	if s.deleteErr != nil {
		return account.Account{}, s.deleteErr
	}
	s.restoredID = id
	s.created.ID = id
	s.created.PurgeState = account.PurgeStateActive
	s.created.DeletionRequestedAt = nil
	s.created.DeletionEffectiveAt = nil
	return s.created, nil
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
	chats         telegramchat.ChatPage
	messages      telegramchat.MessagePage
	chatsErr      error
	messagesErr   error
	validateErr   error
	deleteErr     error
	sessionID     int64
	deleteScope   bool
	validateCalls int
}

func (s *fakeTelegramChatService) ValidateSession(_ context.Context, _ int64) error {
	s.validateCalls++
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

func TestDeleteAccountSchedulesRecoverablePurge(t *testing.T) {
	accounts := &fakeAccountService{}
	chats := &fakeTelegramChatService{}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/42", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", recorder.Code, recorder.Body.String())
	}
	if accounts.scheduledID != 42 || chats.sessionID != 0 {
		t.Fatalf("scheduled ID = %d, session ID = %d, want 42 and no synchronous cleanup",
			accounts.scheduledID, chats.sessionID)
	}
}

func TestDeleteAccountReportsSchedulingFailure(t *testing.T) {
	accounts := &fakeAccountService{deleteErr: errors.New("disk is unavailable")}
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/accounts/42", nil)
	recorder := httptest.NewRecorder()

	newTestHandler(accounts).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "disk is unavailable") {
		t.Fatal("response must not expose storage details")
	}
}

func TestRestoreAccountClearsScheduledDeletion(t *testing.T) {
	accounts := &fakeAccountService{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/accounts/42/restore",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandler(accounts).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if accounts.restoredID != 42 || accounts.created.PurgeState != account.PurgeStateActive {
		t.Fatalf("restored account = %+v", accounts.created)
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

func TestListAccountsCanIncludePendingDeletion(t *testing.T) {
	service := &fakeAccountService{}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/accounts?include_deleted=true&limit=10",
		nil,
	)
	recorder := httptest.NewRecorder()

	newTestHandler(service).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if !service.listFilter.IncludeDeleted {
		t.Fatal("List() filter did not include pending deletion accounts")
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
	if !strings.Contains(recorder.Body.String(), `"status":"error"`) {
		t.Fatalf("response = %s, want refreshed offline account", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "重新登录") {
		t.Fatalf("response = %s, want actionable refresh reason", recorder.Body.String())
	}
}

func TestRefreshAccountsPersistsValidSession(t *testing.T) {
	accounts := &fakeAccountService{
		items: []account.Account{{
			ID:     42,
			Status: account.StatusError,
		}},
		created: account.Account{ID: 42, Status: account.StatusError},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(
		accounts,
		&fakeTelegramAuthService{},
		&fakeTelegramChatService{},
		nil,
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if accounts.verifyCalls != 1 {
		t.Fatalf("RecordVerification() calls = %d, want 1", accounts.verifyCalls)
	}
	if accounts.created.Status != account.StatusOnline || accounts.created.LastVerifiedAt == nil {
		t.Fatalf("account verification = %+v, want persisted online status", accounts.created)
	}
}

func TestRefreshAccountsKeepsStatusForTransientTelegramFailure(t *testing.T) {
	accounts := &fakeAccountService{
		items: []account.Account{{
			ID:     42,
			Status: account.StatusOnline,
		}},
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	chats := &fakeTelegramChatService{
		validateErr: fmt.Errorf("%w: connection reset", telegramchat.ErrTelegram),
	}

	newTestHandlerWithDependencies(
		accounts,
		&fakeTelegramAuthService{},
		chats,
		nil,
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if accounts.verifyCalls != 0 {
		t.Fatalf("RecordVerification() calls = %d, want 0", accounts.verifyCalls)
	}
	if !strings.Contains(recorder.Body.String(), `"status":"online"`) {
		t.Fatalf("response = %s, want preserved online account", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"account_id":42`) ||
		!strings.Contains(recorder.Body.String(), "已保留原状态") {
		t.Fatalf("response = %s, want transient validation warning", recorder.Body.String())
	}
}

func TestRefreshAccountDefersTransientTelegramFailure(t *testing.T) {
	accounts := &fakeAccountService{
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/42/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	chats := &fakeTelegramChatService{
		validateErr: fmt.Errorf("%w: timeout", telegramchat.ErrTimeout),
	}

	newTestHandlerWithDependencies(
		accounts,
		&fakeTelegramAuthService{},
		chats,
		nil,
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"account_validation_deferred"`) {
		t.Fatalf("response = %s, want deferred validation code", recorder.Body.String())
	}
	if accounts.verifyCalls != 0 {
		t.Fatalf("RecordVerification() calls = %d, want 0", accounts.verifyCalls)
	}
}

func TestRefreshAccountsReportsPersistenceFailure(t *testing.T) {
	accounts := &fakeAccountService{
		items:     []account.Account{{ID: 42, Status: account.StatusOnline}},
		created:   account.Account{ID: 42, Status: account.StatusOnline},
		updateErr: errors.New("write failed"),
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(
		accounts,
		&fakeTelegramAuthService{},
		&fakeTelegramChatService{},
		nil,
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"account_refresh_incomplete"`) {
		t.Fatalf("response = %s, want persistence failure code", recorder.Body.String())
	}
}

func TestChatAccessRevalidatesStaleOnlineAccount(t *testing.T) {
	accounts := &fakeAccountService{
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	chats := &fakeTelegramChatService{validateErr: telegramchat.ErrSessionInvalid}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/42/chats", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", recorder.Code, recorder.Body.String())
	}
	if chats.validateCalls != 1 {
		t.Fatalf("ValidateSession() calls = %d, want 1", chats.validateCalls)
	}
	if accounts.created.Status != account.StatusError {
		t.Fatalf("account status = %q, want %q", accounts.created.Status, account.StatusError)
	}
	if !strings.Contains(recorder.Body.String(), `"code":"account_not_online"`) {
		t.Fatalf("response = %s, want account_not_online", recorder.Body.String())
	}
}

func TestChatAccessSkipsValidationForFreshAccount(t *testing.T) {
	verifiedAt := time.Now().UTC()
	accounts := &fakeAccountService{
		created: account.Account{
			ID:             42,
			Status:         account.StatusOnline,
			LastVerifiedAt: &verifiedAt,
		},
	}
	chats := &fakeTelegramChatService{}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/42/chats", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if chats.validateCalls != 0 {
		t.Fatalf("ValidateSession() calls = %d, want 0", chats.validateCalls)
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

func TestMonitorAlertUpdateUsesAuthenticatedActor(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "alert-lifecycle.db"),
	)
	if err != nil {
		t.Fatalf("open operations service: %v", err)
	}
	t.Cleanup(func() {
		if err := operationsService.Close(); err != nil {
			t.Errorf("close operations service: %v", err)
		}
	})
	if _, err := operationsService.CreateRule(
		ctx,
		"Lifecycle API",
		7,
		"chat",
		42,
		"Ops",
		[]string{"urgent"},
	); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	_, alerts, err := operationsService.IngestMessages(ctx, []operations.MessageRecord{{
		AccountID: 7,
		PeerType:  "chat",
		PeerID:    42,
		PeerTitle: "Ops",
		MessageID: 401,
		Text:      "urgent review",
		SentAt:    time.Now().UTC(),
	}})
	if err != nil || len(alerts) != 1 {
		t.Fatalf("IngestMessages() = %+v, %v", alerts, err)
	}

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
	request := httptest.NewRequest(
		http.MethodPatch,
		fmt.Sprintf("/api/v1/monitor/alerts/%d", alerts[0].ID),
		strings.NewReader(`{
			"action":"acknowledge",
			"note":"under review"
		}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s",
			recorder.Code, recorder.Body.String())
	}
	updated, err := operationsService.GetAlert(ctx, alerts[0].ID)
	if err != nil {
		t.Fatalf("GetAlert() error = %v", err)
	}
	if updated.State != "acknowledged" ||
		updated.AcknowledgedBy != "admin" ||
		updated.AcknowledgedAt == nil {
		t.Fatalf("updated alert = %+v", updated)
	}
}

func TestAuthenticatedAPIAccessIsAudited(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "audit.db"),
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
			&fakeAccountService{created: account.Account{ID: 42}},
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

	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/42?secret=ignored", nil)
	request.RemoteAddr = "127.0.0.1:50123"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}

	deniedRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/accounts",
		strings.NewReader(`{}`),
	)
	deniedRequest.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	deniedRequest.Header.Set("Content-Type", "application/json")
	deniedRecorder := httptest.NewRecorder()
	handler.next.ServeHTTP(deniedRecorder, deniedRequest)
	if deniedRecorder.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d, want 403; body: %s",
			deniedRecorder.Code, deniedRecorder.Body.String())
	}

	page, err := operationsService.ListAuditEvents(ctx, operations.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("audit events = %+v", page.Items)
	}
	readEvent := page.Items[1]
	if readEvent.Actor != "admin" || readEvent.Role != auth.RoleAdministrator ||
		readEvent.Method != http.MethodGet ||
		readEvent.ResourceType != "account" || readEvent.ResourceID != "42" ||
		readEvent.StatusCode != http.StatusOK || readEvent.Outcome != "success" ||
		readEvent.RequestID == "" {
		t.Fatalf("read audit event = %+v", readEvent)
	}
	if strings.Contains(readEvent.Route, "secret") {
		t.Fatalf("audit route leaked query data: %q", readEvent.Route)
	}
	deniedEvent := page.Items[0]
	if deniedEvent.StatusCode != http.StatusForbidden ||
		deniedEvent.Outcome != "denied" ||
		deniedEvent.Method != http.MethodPost {
		t.Fatalf("denied audit event = %+v", deniedEvent)
	}
}

func TestAuthenticationEventsAreAuditedWithoutCredentials(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "auth-audit.db"),
	)
	if err != nil {
		t.Fatalf("open operations service: %v", err)
	}
	t.Cleanup(func() {
		if err := operationsService.Close(); err != nil {
			t.Errorf("close operations service: %v", err)
		}
	})
	manager, err := auth.NewManager(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatalf("create auth manager: %v", err)
	}
	webHandler, err := web.New()
	if err != nil {
		t.Fatalf("create web handler: %v", err)
	}
	handler := New(
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
	).Handler()

	submit := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/auth/login?redirect=/secret",
			strings.NewReader(body),
		)
		request.RemoteAddr = "127.0.0.1:50200"
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	setupRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/setup",
		strings.NewReader(`{"username":"audit-admin","password":"strong-password-123"}`),
	)
	setupRequest.RemoteAddr = "127.0.0.1:50200"
	setupRequest.Header.Set("Content-Type", "application/json")
	setupRecorder := httptest.NewRecorder()
	handler.ServeHTTP(setupRecorder, setupRequest)
	if setupRecorder.Code != http.StatusOK {
		t.Fatalf("setup status = %d; body: %s",
			setupRecorder.Code, setupRecorder.Body.String())
	}

	failed := submit(`{"username":"audit-admin","password":"wrong-password-456"}`)
	if failed.Code != http.StatusUnauthorized {
		t.Fatalf("failed login status = %d; body: %s", failed.Code, failed.Body.String())
	}
	succeeded := submit(`{"username":"audit-admin","password":"strong-password-123"}`)
	if succeeded.Code != http.StatusOK {
		t.Fatalf("login status = %d; body: %s", succeeded.Code, succeeded.Body.String())
	}

	page, err := operationsService.ListAuditEvents(ctx, operations.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("audit events = %+v", page.Items)
	}
	successEvent := page.Items[0]
	if successEvent.Actor != "audit-admin" ||
		successEvent.Role != auth.RoleAdministrator ||
		successEvent.Outcome != "success" {
		t.Fatalf("login audit event = %+v", successEvent)
	}
	failedEvent := page.Items[1]
	if failedEvent.Actor != anonymousAuditActor ||
		failedEvent.Role != anonymousAuditRole ||
		failedEvent.Outcome != "denied" {
		t.Fatalf("failed login audit event = %+v", failedEvent)
	}
	setupEvent := page.Items[2]
	if setupEvent.Actor != "audit-admin" ||
		setupEvent.Method != http.MethodPost ||
		setupEvent.Route != "/api/v1/auth/setup" {
		t.Fatalf("setup audit event = %+v", setupEvent)
	}
	for _, item := range page.Items {
		serialized := strings.Join([]string{
			item.RequestID,
			item.Actor,
			item.Role,
			item.Method,
			item.Route,
			item.ResourceType,
			item.ResourceID,
			item.ClientIP,
			item.Outcome,
		}, " ")
		for _, secret := range []string{
			"strong-password-123",
			"wrong-password-456",
			"redirect",
			"secret",
		} {
			if strings.Contains(serialized, secret) {
				t.Fatalf("audit event leaked %q: %+v", secret, item)
			}
		}
	}
}

func TestPanickedAPIRequestIsAuditedAsFailure(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "panic-audit.db"),
	)
	if err != nil {
		t.Fatalf("open operations service: %v", err)
	}
	t.Cleanup(func() {
		if err := operationsService.Close(); err != nil {
			t.Errorf("close operations service: %v", err)
		}
	})
	manager, token, _ := testAuthentication()
	api := &API{
		auth:       manager,
		operations: operationsService,
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	handler := api.logRequests(api.requireAPI(func(http.ResponseWriter, *http.Request) {
		panic("synthetic panic")
	}))
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
	request.Pattern = "/api/v1/accounts"
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500; body: %s",
			recorder.Code, recorder.Body.String())
	}
	page, err := operationsService.ListAuditEvents(ctx, operations.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	if len(page.Items) != 1 ||
		page.Items[0].StatusCode != http.StatusInternalServerError ||
		page.Items[0].Outcome != "failure" {
		t.Fatalf("panic audit events = %+v", page.Items)
	}
}

func TestMonitorStatusIncludesCollectionHealth(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "monitor-status.db"),
	)
	if err != nil {
		t.Fatalf("operations.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := operationsService.Close(); err != nil {
			t.Errorf("operations.Close() error = %v", err)
		}
	})
	if _, err := operationsService.CreateRule(
		ctx,
		"Status",
		7,
		"chat",
		42,
		"Ops",
		nil,
	); err != nil {
		t.Fatalf("CreateRule() error = %v", err)
	}
	if err := operationsService.RecordBatchFailure(ctx, operations.IngestBatchInput{
		Source:           "test",
		AccountID:        7,
		PeerType:         "chat",
		PeerID:           42,
		CheckpointBefore: 100,
	}, errors.New("Telegram request is rate limited: retry after 30s")); err != nil {
		t.Fatalf("RecordBatchFailure() error = %v", err)
	}
	nextRun := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	manager, token, _ := testAuthentication()
	webHandler, err := web.New()
	if err != nil {
		t.Fatalf("web.New() error = %v", err)
	}
	handler := New(
		nil,
		nil,
		nil,
		nil,
		manager,
		webHandler.Handler(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
		operationsService,
		fakeMonitorRunner{status: monitorscheduler.Status{
			Running:   false,
			NextRunAt: &nextRun,
			LastError: "partial",
		}},
	).Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/monitor/status", nil)
	request = request.Clone(ctx)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Running    bool       `json:"running"`
		NextRunAt  *time.Time `json:"next_run_at"`
		Collection struct {
			TargetCount          int `json:"target_count"`
			FloodWaitTargetCount int `json:"flood_wait_target_count"`
			ConsecutiveFailures  int `json:"consecutive_failures"`
		} `json:"collection"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode monitor status: %v", err)
	}
	if payload.Running ||
		payload.NextRunAt == nil ||
		!payload.NextRunAt.Equal(nextRun) ||
		payload.Collection.TargetCount != 1 ||
		payload.Collection.FloodWaitTargetCount != 1 ||
		payload.Collection.ConsecutiveFailures != 1 {
		t.Fatalf("monitor status payload = %+v", payload)
	}
}

func TestRetentionAndLegalHoldAPI(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "retention-api.db"),
	)
	if err != nil {
		t.Fatalf("open operations service: %v", err)
	}
	t.Cleanup(func() {
		if err := operationsService.Close(); err != nil {
			t.Errorf("close operations service: %v", err)
		}
	})
	if _, err := operationsService.IngestBatch(ctx, operations.IngestBatchInput{
		BatchID:   "retention-api-seed",
		Source:    "test",
		AccountID: 9,
		PeerType:  "chat",
		PeerID:    77,
		PeerTitle: "Operations",
		Messages: []operations.MessageRecord{{
			AccountID: 9,
			PeerType:  "chat",
			PeerID:    77,
			PeerTitle: "Operations",
			MessageID: 900,
			Sender:    "Alice",
			Text:      "expired",
			SentAt:    time.Now().UTC().AddDate(0, 0, -60),
		}},
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
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

	holdRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/retention/legal-holds",
		strings.NewReader(
			`{"scope":"chat","account_id":9,"peer_type":"chat","peer_id":77,"reason":"Matter 2026-02"}`,
		),
	)
	holdRecorder := httptest.NewRecorder()
	handler.ServeHTTP(holdRecorder, holdRequest)
	if holdRecorder.Code != http.StatusCreated {
		t.Fatalf("create hold status = %d; body: %s",
			holdRecorder.Code, holdRecorder.Body.String())
	}
	var hold operations.LegalHold
	if err := json.NewDecoder(holdRecorder.Body).Decode(&hold); err != nil {
		t.Fatalf("decode legal hold: %v", err)
	}
	if hold.ID <= 0 || hold.CreatedBy != "admin" {
		t.Fatalf("legal hold = %+v", hold)
	}

	previewRecorder := httptest.NewRecorder()
	handler.ServeHTTP(previewRecorder, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/messages/cleanup",
		strings.NewReader(`{"dry_run":true}`),
	))
	if previewRecorder.Code != http.StatusOK {
		t.Fatalf("cleanup preview status = %d; body: %s",
			previewRecorder.Code, previewRecorder.Body.String())
	}
	var preview operations.RetentionResult
	if err := json.NewDecoder(previewRecorder.Body).Decode(&preview); err != nil {
		t.Fatalf("decode cleanup preview: %v", err)
	}
	if preview.CandidateMessages != 0 || preview.HeldMessages != 1 {
		t.Fatalf("cleanup preview = %+v", preview)
	}

	releaseRecorder := httptest.NewRecorder()
	handler.ServeHTTP(releaseRecorder, httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/retention/legal-holds/"+strconv.FormatInt(hold.ID, 10),
		nil,
	))
	if releaseRecorder.Code != http.StatusOK {
		t.Fatalf("release hold status = %d; body: %s",
			releaseRecorder.Code, releaseRecorder.Body.String())
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
