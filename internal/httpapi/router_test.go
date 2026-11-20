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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/accountproxy"
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
	proxy       account.Proxy
	proxyID     int64
	clearedID   int64
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
	if s.upsertErr != nil || s.updateErr != nil {
		return account.Account{}, errors.Join(s.upsertErr, s.updateErr)
	}
	now := time.Unix(10, 0).UTC()
	s.created = account.Account{
		ID:             input.ID,
		PhoneNumber:    input.PhoneNumber,
		Username:       input.Username,
		DisplayName:    input.DisplayName,
		Status:         account.StatusOnline,
		StatusReason:   "",
		LastVerifiedAt: &now,
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

func (s *fakeAccountService) SetProxy(
	_ context.Context,
	id int64,
	proxy account.Proxy,
) (account.Account, error) {
	if s.updateErr != nil {
		return account.Account{}, s.updateErr
	}
	s.proxyID = id
	s.proxy = proxy
	s.created.ID = id
	s.created.Proxy = proxy
	return s.created, nil
}

func (s *fakeAccountService) ClearProxy(
	_ context.Context,
	id int64,
) (account.Account, error) {
	if s.updateErr != nil {
		return account.Account{}, s.updateErr
	}
	s.clearedID = id
	s.created.ID = id
	s.created.Proxy = account.Proxy{}
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
	mu          sync.Mutex
	configured  bool
	challenge   telegramauth.Challenge
	result      telegramauth.Result
	sendErr     error
	submitErr   error
	accountID   int64
	qrAccountID int64
	sendStarted chan struct{}
	sendRelease <-chan struct{}
}

func (s *fakeTelegramAuthService) Configured() bool {
	return s.configured
}

func (s *fakeTelegramAuthService) SendCode(
	ctx context.Context,
	_ string,
	expectedAccountID int64,
) (telegramauth.Challenge, error) {
	s.mu.Lock()
	s.accountID = expectedAccountID
	s.mu.Unlock()
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

func (s *fakeTelegramAuthService) StartQR(
	_ context.Context,
	expectedAccountID int64,
) (telegramauth.QRChallenge, error) {
	s.mu.Lock()
	s.qrAccountID = expectedAccountID
	s.mu.Unlock()
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
	inspectErr    error
	profile       telegramchat.Profile
	deleteErr     error
	sessionID     int64
	deleteScope   bool
	validateCalls int
	inspectCalls  int
	resetID       int64
}

func (s *fakeTelegramChatService) ValidateSession(
	_ context.Context,
	_ int64,
) error {
	s.validateCalls++
	return s.validateErr
}

func (s *fakeTelegramChatService) InspectSession(
	_ context.Context,
	accountID int64,
) (telegramchat.Profile, error) {
	s.inspectCalls++
	if s.inspectErr != nil {
		return telegramchat.Profile{}, s.inspectErr
	}
	if s.profile.ID == 0 {
		s.profile.ID = accountID
	}
	return s.profile, nil
}

func (s *fakeTelegramChatService) CurrentProfile(
	_ context.Context,
	accountID int64,
) (telegramchat.Profile, error) {
	if s.validateErr != nil {
		return telegramchat.Profile{}, s.validateErr
	}
	if s.profile.ID == 0 {
		s.profile.ID = accountID
	}
	return s.profile, nil
}

func (s *fakeTelegramChatService) DeleteSession(accountID int64) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.sessionID = accountID
	return nil
}

func (s *fakeTelegramChatService) ResetAccount(accountID int64) {
	s.resetID = accountID
}

type fakeAccountProxyStore struct {
	items map[int64]accountproxy.Profile
}

func (s *fakeAccountProxyStore) Get(accountID int64) (accountproxy.Profile, bool) {
	item, ok := s.items[accountID]
	return item, ok
}

func (s *fakeAccountProxyStore) Set(
	_ context.Context,
	accountID int64,
	profile accountproxy.Profile,
) error {
	if s.items == nil {
		s.items = make(map[int64]accountproxy.Profile)
	}
	s.items[accountID] = profile
	return nil
}

func (s *fakeAccountProxyStore) Delete(
	_ context.Context,
	accountID int64,
) (bool, error) {
	_, ok := s.items[accountID]
	delete(s.items, accountID)
	return ok, nil
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

func TestAuthSessionReportsRolePermissions(t *testing.T) {
	manager, err := auth.NewTestManager("viewer", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	if _, err := manager.CreateUser("reader", "strong-password-456", auth.RoleViewer); err != nil {
		t.Fatalf("CreateUser(viewer) error = %v", err)
	}
	token, session, err := manager.CreateUserSession("reader", auth.RoleViewer)
	if err != nil {
		t.Fatalf("CreateRoleSession() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	recorder := httptest.NewRecorder()
	newRawTestHandler(t, manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	var payload authSessionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if payload.Role != auth.RoleViewer || !payload.Authenticated {
		t.Fatalf("session response = %+v", payload)
	}
	if payload.CSRFToken != session.CSRF {
		t.Fatal("session response did not contain the active CSRF token")
	}
	if !slices.Contains(payload.Permissions, auth.PermissionChatRead) ||
		slices.Contains(payload.Permissions, auth.PermissionChatOperate) ||
		slices.Contains(payload.Permissions, auth.PermissionAccountWrite) {
		t.Fatalf("viewer permissions = %v", payload.Permissions)
	}
}

func TestViewerLoginCreatesViewerSession(t *testing.T) {
	manager, err := auth.NewTestManager("owner", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	if _, err := manager.CreateUser("reader", "strong-password-456", auth.RoleViewer); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"username":"reader","password":"strong-password-456"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "127.0.0.1:50001"
	recorder := httptest.NewRecorder()
	newRawTestHandler(t, manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", recorder.Code, recorder.Body.String())
	}
	var payload authSessionResponse
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	if payload.Username != "reader" || payload.Role != auth.RoleViewer {
		t.Fatalf("login principal = %q/%q", payload.Username, payload.Role)
	}
	if slices.Contains(payload.Permissions, auth.PermissionUserWrite) {
		t.Fatalf("viewer permissions = %v", payload.Permissions)
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

func TestSendTelegramCodePreservesTargetAccount(t *testing.T) {
	telegram := &fakeTelegramAuthService{
		configured: true,
		challenge:  telegramauth.Challenge{LoginID: "login-id"},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telegram/auth/send-code",
		strings.NewReader(`{"phone_number":"+15550001001","account_id":42}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithTelegram(&fakeAccountService{}, telegram).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s",
			recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	telegram.mu.Lock()
	accountID := telegram.accountID
	telegram.mu.Unlock()
	if accountID != 42 {
		t.Fatalf("SendCode() account ID = %d, want 42", accountID)
	}
}

func TestStartTelegramQRPreservesTargetAccount(t *testing.T) {
	telegram := &fakeTelegramAuthService{configured: true}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telegram/auth/qr",
		strings.NewReader(`{"account_id":42}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithTelegram(&fakeAccountService{}, telegram).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s",
			recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	telegram.mu.Lock()
	accountID := telegram.qrAccountID
	telegram.mu.Unlock()
	if accountID != 42 {
		t.Fatalf("StartQR() account ID = %d, want 42", accountID)
	}
}

func TestTelegramAccountMismatchHasActionableResponse(t *testing.T) {
	telegram := &fakeTelegramAuthService{
		configured: true,
		sendErr:    telegramauth.ErrAccountMismatch,
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/telegram/auth/send-code",
		strings.NewReader(`{"phone_number":"+15550001001","account_id":42}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithTelegram(&fakeAccountService{}, telegram).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body: %s",
			recorder.Code, http.StatusUnprocessableEntity, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"telegram_account_mismatch"`) {
		t.Fatalf("body = %s, want telegram_account_mismatch", recorder.Body.String())
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
	chats := &fakeTelegramChatService{inspectErr: telegramchat.ErrSessionInvalid}
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
	if !strings.Contains(recorder.Body.String(),
		`"outcomes":[{"account_id":42,"status":"error","reason":"Telegram 授权已失效，请重新登录","verified_at":"1970-01-01T00:00:10Z"}]`) {
		t.Fatalf("response = %s, want explicit refresh outcome", recorder.Body.String())
	}
}

func TestSetAccountProxyStoresCredentialsWithoutEchoingPassword(t *testing.T) {
	accounts := &fakeAccountService{created: account.Account{ID: 42}}
	chats := &fakeTelegramChatService{}
	proxies := &fakeAccountProxyStore{}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/accounts/42/proxy",
		strings.NewReader(
			`{"proxy_url":"socks5://alice:super-secret@proxy.example:1080"}`,
		),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	newTestHandlerWithProxy(accounts, chats, proxies).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "super-secret") {
		t.Fatal("response exposed proxy password")
	}
	profile, ok := proxies.Get(42)
	if !ok || profile.Host != "proxy.example" || profile.Password != "super-secret" {
		t.Fatalf("stored proxy = %+v, ok = %t", profile, ok)
	}
	if accounts.proxy.Host != "proxy.example" || accounts.proxy.Username != "alice" ||
		accounts.proxy.Port != 1080 || !accounts.proxy.Enabled {
		t.Fatalf("account proxy metadata = %+v", accounts.proxy)
	}
	if chats.resetID != 42 {
		t.Fatalf("ResetAccount() account = %d, want 42", chats.resetID)
	}
}

func TestSetAccountProxyRejectsInvalidURL(t *testing.T) {
	accounts := &fakeAccountService{created: account.Account{ID: 42}}
	request := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/accounts/42/proxy",
		strings.NewReader(`{"proxy_url":"http://proxy.example:8080"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	newTestHandlerWithProxy(accounts, &fakeTelegramChatService{}, &fakeAccountProxyStore{}).
		ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(recorder.Body.String(), `"code":"invalid_proxy"`) {
		t.Fatalf("status = %d; body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestCheckAccountProxyDoesNotMarkAccountOffline(t *testing.T) {
	accounts := &fakeAccountService{
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	proxies := &fakeAccountProxyStore{items: map[int64]accountproxy.Profile{
		42: {Enabled: true, Host: "proxy.example", Port: 1080},
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/accounts/42/proxy/check",
		strings.NewReader(`{}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	newTestHandlerWithProxy(
		accounts,
		&fakeTelegramChatService{},
		proxies,
		WithProxyChecker(func(context.Context, accountproxy.Profile) error {
			return errors.New("proxy unavailable")
		}),
	).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", recorder.Code, recorder.Body.String())
	}
	if accounts.created.Status != account.StatusOnline {
		t.Fatalf("account status = %q, proxy check must not mark it offline",
			accounts.created.Status)
	}
	if accounts.proxy.LastChecked == nil || accounts.proxy.LastError == "" {
		t.Fatalf("proxy check result = %+v", accounts.proxy)
	}
}

func TestRefreshAccountsPersistsValidSession(t *testing.T) {
	now := time.Unix(10, 0).UTC()
	accounts := &fakeAccountService{
		items: []account.Account{{
			ID:     42,
			Status: account.StatusError,
		}},
		created: account.Account{
			ID:             42,
			Status:         account.StatusError,
			LastVerifiedAt: &now,
		},
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
	if accounts.verifyCalls != 0 {
		t.Fatalf("RecordVerification() calls = %d, want 0; successful refresh uses UpsertAuthorized",
			accounts.verifyCalls)
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
		inspectErr: fmt.Errorf("%w: connection reset", telegramchat.ErrTelegram),
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
	if !strings.Contains(recorder.Body.String(), `"deferred":true`) {
		t.Fatalf("response = %s, want deferred refresh outcome", recorder.Body.String())
	}
}

func TestRefreshAccountsSkipsPausedAccountWithoutTelegramRequest(t *testing.T) {
	accounts := &fakeAccountService{
		items:   []account.Account{{ID: 42, Status: account.StatusPaused, StatusReason: "manual pause"}},
		created: account.Account{ID: 42, Status: account.StatusPaused, StatusReason: "manual pause"},
	}
	chats := &fakeTelegramChatService{}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(
		accounts,
		&fakeTelegramAuthService{},
		chats,
		nil,
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if chats.inspectCalls != 0 {
		t.Fatalf("InspectSession() calls = %d, want 0", chats.inspectCalls)
	}
	if !strings.Contains(recorder.Body.String(), `"skipped":true`) ||
		!strings.Contains(recorder.Body.String(), "已跳过授权校验") {
		t.Fatalf("response = %s, want skipped refresh outcome", recorder.Body.String())
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
		inspectErr: fmt.Errorf("%w: timeout", telegramchat.ErrTimeout),
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
	if !strings.Contains(recorder.Body.String(), `"deferred":true`) ||
		!strings.Contains(recorder.Body.String(), `"status":"online"`) {
		t.Fatalf("response = %s, want preserved status with deferred outcome", recorder.Body.String())
	}
	if accounts.verifyCalls != 0 {
		t.Fatalf("RecordVerification() calls = %d, want 0", accounts.verifyCalls)
	}
	if chats.inspectCalls != 1 || chats.validateCalls != 0 {
		t.Fatalf("inspect calls = %d, validate calls = %d, want one real session check",
			chats.inspectCalls, chats.validateCalls)
	}
}

func TestRefreshAccountSynchronizesTelegramProfile(t *testing.T) {
	accounts := &fakeAccountService{
		created: account.Account{
			ID:          42,
			PhoneNumber: "10000",
			Username:    "stale",
			DisplayName: "Old Name",
			Status:      account.StatusOnline,
		},
	}
	chats := &fakeTelegramChatService{profile: telegramchat.Profile{
		ID:          42,
		PhoneNumber: "15550001005",
		Username:    "fresh_user",
		DisplayName: "Fresh Name",
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/42/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if chats.inspectCalls != 1 || chats.validateCalls != 0 {
		t.Fatalf("inspect calls = %d, validate calls = %d, want one real session check",
			chats.inspectCalls, chats.validateCalls)
	}
	if accounts.created.PhoneNumber != "15550001005" ||
		accounts.created.Username != "fresh_user" ||
		accounts.created.DisplayName != "Fresh Name" {
		t.Fatalf("account profile = %+v, want synchronized Telegram metadata", accounts.created)
	}
}

func TestRefreshAccountUsesTelegramInspection(t *testing.T) {
	accounts := &fakeAccountService{
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	chats := &fakeTelegramChatService{
		profile: telegramchat.Profile{ID: 42, Username: "verified_user"},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/42/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if chats.inspectCalls != 1 || chats.validateCalls != 0 {
		t.Fatalf("inspect calls = %d, validate calls = %d, want a real Telegram inspection",
			chats.inspectCalls, chats.validateCalls)
	}
	if !strings.Contains(recorder.Body.String(),
		`"outcomes":[{"account_id":42,"status":"online","verified_at":"1970-01-01T00:00:10Z"}]`) {
		t.Fatalf("response = %s, want a successful refresh outcome", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"verified_at":`) {
		t.Fatalf("response = %s, want a server verification timestamp", recorder.Body.String())
	}
}

func TestRefreshAccountRejectsMismatchedTelegramIdentity(t *testing.T) {
	accounts := &fakeAccountService{
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	chats := &fakeTelegramChatService{
		profile: telegramchat.Profile{ID: 99, Username: "other_user"},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/42/refresh", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if accounts.created.Status != account.StatusError ||
		!strings.Contains(accounts.created.StatusReason, "不一致") {
		t.Fatalf("account = %+v, want mismatched identity persisted as offline", accounts.created)
	}
	if !strings.Contains(recorder.Body.String(), `"status":"error"`) {
		t.Fatalf("response = %s, want corrected offline account", recorder.Body.String())
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
	chats := &fakeTelegramChatService{inspectErr: telegramchat.ErrSessionInvalid}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/42/chats", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", recorder.Code, recorder.Body.String())
	}
	if chats.inspectCalls != 1 || chats.validateCalls != 0 {
		t.Fatalf("InspectSession() calls = %d, ValidateSession() calls = %d, want one validation",
			chats.inspectCalls, chats.validateCalls)
	}
	if accounts.created.Status != account.StatusError {
		t.Fatalf("account status = %q, want %q", accounts.created.Status, account.StatusError)
	}
	if !strings.Contains(recorder.Body.String(), `"code":"account_not_online"`) {
		t.Fatalf("response = %s, want account_not_online", recorder.Body.String())
	}
}

func TestOfflineAccountDoesNotReachTelegramChatAPIs(t *testing.T) {
	accounts := &fakeAccountService{
		created: account.Account{
			ID:           42,
			Status:       account.StatusError,
			StatusReason: "Telegram 授权已失效，请重新登录",
		},
	}
	chats := &fakeTelegramChatService{}
	handler := newTestHandlerWithDependencies(
		accounts,
		&fakeTelegramAuthService{},
		chats,
		nil,
	)
	paths := []string{
		"/api/v1/accounts/42/chats",
		"/api/v1/accounts/42/profile",
		"/api/v1/accounts/42/chats/user/99/avatar",
	}
	for _, path := range paths {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusConflict {
			t.Fatalf("%s status = %d, want 409; body: %s",
				path, recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), `"code":"account_not_online"`) {
			t.Fatalf("%s body = %s, want account_not_online", path, recorder.Body.String())
		}
	}
	if chats.inspectCalls != 0 || chats.validateCalls != 0 {
		t.Fatalf("offline account reached Telegram: %d inspect, %d validate",
			chats.inspectCalls, chats.validateCalls)
	}
	if accounts.verifyCalls != 0 {
		t.Fatalf("offline account verification calls = %d, want 0", accounts.verifyCalls)
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
	chats := &fakeTelegramChatService{
		profile: telegramchat.Profile{ID: 42, PhoneNumber: "15550001005", Username: "alice"},
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/accounts/42/chats", nil)
	recorder := httptest.NewRecorder()

	newTestHandlerWithDependencies(accounts, &fakeTelegramAuthService{}, chats, nil).
		ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	if chats.inspectCalls != 0 || chats.validateCalls != 0 {
		t.Fatalf("session checks = %d inspect, %d validate, want 0",
			chats.inspectCalls, chats.validateCalls)
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

	createChannelRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/monitor/channels",
		strings.NewReader(`{
			"name":"Security webhook",
			"kind":"webhook",
			"url":"https://hooks.example.test/telenexus",
			"secret":"signing-secret-1234"
		}`),
	)
	createChannelRequest.Header.Set("Content-Type", "application/json")
	createChannelRecorder := httptest.NewRecorder()
	handler.ServeHTTP(createChannelRecorder, createChannelRequest)
	if createChannelRecorder.Code != http.StatusCreated {
		t.Fatalf("create channel response = %d %s",
			createChannelRecorder.Code, createChannelRecorder.Body.String())
	}
	if strings.Contains(createChannelRecorder.Body.String(), "hooks.example.test") ||
		strings.Contains(createChannelRecorder.Body.String(), "signing-secret-1234") {
		t.Fatalf("create channel leaked configuration: %s", createChannelRecorder.Body.String())
	}
	var createdChannel operations.NotificationChannel
	if err := json.NewDecoder(createChannelRecorder.Body).Decode(&createdChannel); err != nil {
		t.Fatalf("decode created channel: %v", err)
	}
	if createdChannel.ID <= 1 ||
		createdChannel.Kind != operations.NotificationKindWebhook ||
		!createdChannel.Configured {
		t.Fatalf("created channel = %+v", createdChannel)
	}
	protectedDeleteRequest := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/monitor/channels/1",
		nil,
	)
	protectedDeleteRecorder := httptest.NewRecorder()
	handler.ServeHTTP(protectedDeleteRecorder, protectedDeleteRequest)
	if protectedDeleteRecorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("delete built-in channel response = %d %s",
			protectedDeleteRecorder.Code, protectedDeleteRecorder.Body.String())
	}
	deleteChannelRequest := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/monitor/channels/"+strconv.FormatInt(createdChannel.ID, 10),
		nil,
	)
	deleteChannelRecorder := httptest.NewRecorder()
	handler.ServeHTTP(deleteChannelRecorder, deleteChannelRequest)
	if deleteChannelRecorder.Code != http.StatusNoContent {
		t.Fatalf("delete channel response = %d %s",
			deleteChannelRecorder.Code, deleteChannelRecorder.Body.String())
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
			"keywords":["deploy"],
			"exclude_keywords":["test"],
			"owner":"alice",
			"cooldown_seconds":120
		}`),
	)
	ruleRequest.Header.Set("Content-Type", "application/json")
	ruleRecorder := httptest.NewRecorder()
	handler.ServeHTTP(ruleRecorder, ruleRequest)
	if ruleRecorder.Code != http.StatusCreated ||
		!strings.Contains(ruleRecorder.Body.String(), `"cooldown_seconds":120`) ||
		!strings.Contains(ruleRecorder.Body.String(), `"owner":"alice"`) ||
		!strings.Contains(ruleRecorder.Body.String(), `"version":1`) ||
		!strings.Contains(ruleRecorder.Body.String(), `"notification_channel_name":"站内通知"`) {
		t.Fatalf("create rule response = %d %s",
			ruleRecorder.Code, ruleRecorder.Body.String())
	}
	var createdRule operations.Rule
	if err := json.NewDecoder(ruleRecorder.Body).Decode(&createdRule); err != nil {
		t.Fatalf("decode created rule: %v", err)
	}
	now := time.Date(2026, 11, 20, 16, 0, 0, 0, time.UTC)
	if _, _, err := operationsService.IngestMessages(ctx, []operations.MessageRecord{
		{
			AccountID: 7, PeerType: "chat", PeerID: 42, PeerTitle: "Ops",
			MessageID: 701, Sender: "Alice", Text: "deploy production", SentAt: now,
		},
		{
			AccountID: 7, PeerType: "chat", PeerID: 42, PeerTitle: "Ops",
			MessageID: 702, Sender: "Alice", Text: "deploy test", SentAt: now,
		},
	}); err != nil {
		t.Fatalf("seed preview messages: %v", err)
	}
	previewRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/monitor/rules/preview",
		strings.NewReader(`{
			"account_id":7,
			"peer_type":"chat",
			"peer_id":42,
			"keywords":["deploy"],
			"exclude_keywords":["test"],
			"limit":5
		}`),
	)
	previewRequest.Header.Set("Content-Type", "application/json")
	previewRecorder := httptest.NewRecorder()
	handler.ServeHTTP(previewRecorder, previewRequest)
	if previewRecorder.Code != http.StatusOK {
		t.Fatalf("preview rule response = %d %s",
			previewRecorder.Code, previewRecorder.Body.String())
	}
	var preview operations.RulePreview
	if err := json.NewDecoder(previewRecorder.Body).Decode(&preview); err != nil {
		t.Fatalf("decode rule preview: %v", err)
	}
	if preview.Matched != 1 || preview.Excluded != 1 ||
		len(preview.Samples) != 1 || len(preview.ExcludedItems) != 1 {
		t.Fatalf("rule preview = %+v", preview)
	}

	updateRuleRequest := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/monitor/rules/"+strconv.FormatInt(createdRule.ID, 10),
		strings.NewReader(`{"enabled":false,"owner":"bob"}`),
	)
	updateRuleRequest.Header.Set("Content-Type", "application/json")
	updateRuleRecorder := httptest.NewRecorder()
	handler.ServeHTTP(updateRuleRecorder, updateRuleRequest)
	if updateRuleRecorder.Code != http.StatusOK ||
		!strings.Contains(updateRuleRecorder.Body.String(), `"version":2`) ||
		!strings.Contains(updateRuleRecorder.Body.String(), `"owner":"bob"`) {
		t.Fatalf("update rule response = %d %s",
			updateRuleRecorder.Code, updateRuleRecorder.Body.String())
	}
	versionsRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/monitor/rules/"+strconv.FormatInt(createdRule.ID, 10)+"/versions",
		nil,
	)
	versionsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(versionsRecorder, versionsRequest)
	if versionsRecorder.Code != http.StatusOK ||
		!strings.Contains(versionsRecorder.Body.String(), `"version":2`) ||
		!strings.Contains(versionsRecorder.Body.String(), `"actor":"admin"`) {
		t.Fatalf("rule versions response = %d %s",
			versionsRecorder.Code, versionsRecorder.Body.String())
	}
	reenableRequest := httptest.NewRequest(
		http.MethodPut,
		"/api/v1/monitor/rules/"+strconv.FormatInt(createdRule.ID, 10),
		strings.NewReader(`{"enabled":true}`),
	)
	reenableRequest.Header.Set("Content-Type", "application/json")
	reenableRecorder := httptest.NewRecorder()
	handler.ServeHTTP(reenableRecorder, reenableRequest)
	if reenableRecorder.Code != http.StatusOK ||
		!strings.Contains(reenableRecorder.Body.String(), `"version":3`) {
		t.Fatalf("reenable rule response = %d %s",
			reenableRecorder.Code, reenableRecorder.Body.String())
	}

	targetsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/monitor/targets", nil)
	targetsRecorder := httptest.NewRecorder()
	handler.ServeHTTP(targetsRecorder, targetsRequest)
	if targetsRecorder.Code != http.StatusOK ||
		!strings.Contains(targetsRecorder.Body.String(), `"account_id":7`) ||
		!strings.Contains(targetsRecorder.Body.String(), `"checkpoint_delay_seconds":0`) {
		t.Fatalf("targets response = %d %s",
			targetsRecorder.Code, targetsRecorder.Body.String())
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
	historyRequest := httptest.NewRequest(
		http.MethodGet,
		fmt.Sprintf("/api/v1/monitor/alerts/%d/events", alerts[0].ID),
		nil,
	)
	historyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(historyRecorder, historyRequest)
	if historyRecorder.Code != http.StatusOK ||
		!strings.Contains(historyRecorder.Body.String(), `"action":"acknowledged"`) ||
		!strings.Contains(historyRecorder.Body.String(), `"actor":"admin"`) {
		t.Fatalf("alert history response = %d %s",
			historyRecorder.Code,
			historyRecorder.Body.String(),
		)
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
	handler := api.logRequests(api.requireAPIPermission(
		auth.PermissionAccountRead,
		func(http.ResponseWriter, *http.Request) {
			panic("synthetic panic")
		},
	))
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

func TestAPIPermissionEnforcesRoleScope(t *testing.T) {
	manager, err := auth.NewTestManager("permission-test", "strong-password-123")
	if err != nil {
		t.Fatalf("NewTestManager() error = %v", err)
	}
	if _, err := manager.CreateUser("reader", "strong-password-456", auth.RoleViewer); err != nil {
		t.Fatalf("CreateUser(viewer) error = %v", err)
	}
	if _, err := manager.CreateUser("operator", "strong-password-789", auth.RoleOperator); err != nil {
		t.Fatalf("CreateUser(operator) error = %v", err)
	}
	viewerToken, viewer, err := manager.CreateUserSession("reader", auth.RoleViewer)
	if err != nil {
		t.Fatalf("CreateUserSession(viewer) error = %v", err)
	}
	operatorToken, operator, err := manager.CreateUserSession("operator", auth.RoleOperator)
	if err != nil {
		t.Fatalf("CreateUserSession(operator) error = %v", err)
	}
	api := &API{
		auth:   manager,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	forbiddenHit := false
	forbidden := api.requireAPIPermission(
		auth.PermissionAuditRead,
		func(http.ResponseWriter, *http.Request) {
			forbiddenHit = true
		},
	)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: viewerToken})
	recorder := httptest.NewRecorder()
	forbidden(recorder, request)
	if recorder.Code != http.StatusForbidden || forbiddenHit {
		t.Fatalf(
			"viewer audit request status = %d, handler hit = %v; body: %s",
			recorder.Code,
			forbiddenHit,
			recorder.Body.String(),
		)
	}

	allowedHit := false
	allowed := api.requireAPIPermission(
		auth.PermissionMonitorOperate,
		func(w http.ResponseWriter, _ *http.Request) {
			allowedHit = true
			w.WriteHeader(http.StatusNoContent)
		},
	)
	request = httptest.NewRequest(http.MethodPost, "/api/v1/monitor/run", strings.NewReader(`{}`))
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: operatorToken})
	request.Header.Set(auth.CSRFHeader, operator.CSRF)
	recorder = httptest.NewRecorder()
	allowed(recorder, request)
	if recorder.Code != http.StatusNoContent || !allowedHit {
		t.Fatalf(
			"operator monitor request status = %d, handler hit = %v; body: %s",
			recorder.Code,
			allowedHit,
			recorder.Body.String(),
		)
	}
	if viewer.Role != auth.RoleViewer {
		t.Fatalf("viewer session role = %q", viewer.Role)
	}
}

func TestUserManagementRequiresAdministrator(t *testing.T) {
	manager, adminToken, adminCSRF := testAuthentication()
	if _, err := manager.CreateUser("user-reader", "strong-password-456", auth.RoleViewer); err != nil {
		t.Fatalf("CreateUser(viewer) error = %v", err)
	}
	viewerToken, viewer, err := manager.CreateUserSession("user-reader", auth.RoleViewer)
	if err != nil {
		t.Fatalf("CreateUserSession() error = %v", err)
	}
	handler := newRawTestHandler(t, manager)
	viewerHandler := authenticatedTestHandler{
		next:  handler,
		token: viewerToken,
		csrf:  viewer.CSRF,
	}
	createBody := `{"username":"new-operator","password":"strong-password-789","role":"operator"}`
	forbidden := httptest.NewRecorder()
	viewerHandler.ServeHTTP(forbidden, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/users",
		strings.NewReader(createBody),
	))
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("viewer create user status = %d; body: %s",
			forbidden.Code, forbidden.Body.String())
	}

	adminHandler := authenticatedTestHandler{
		next:  handler,
		token: adminToken,
		csrf:  adminCSRF,
	}
	createRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/users",
		strings.NewReader(createBody),
	)
	createRequest.Header.Set("Content-Type", "application/json")
	created := httptest.NewRecorder()
	adminHandler.ServeHTTP(created, createRequest)
	if created.Code != http.StatusCreated {
		t.Fatalf("admin create user status = %d; body: %s",
			created.Code, created.Body.String())
	}
	if strings.Contains(strings.ToLower(created.Body.String()), "password") {
		t.Fatalf("user response exposed password data: %s", created.Body.String())
	}
	list := httptest.NewRecorder()
	adminHandler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/v1/auth/users", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "new-operator") {
		t.Fatalf("list users status = %d; body: %s", list.Code, list.Body.String())
	}
}

func TestChangedUserRoleRevokesExistingSession(t *testing.T) {
	manager, adminToken, adminCSRF := testAuthentication()
	if _, err := manager.CreateUser("role-reader", "strong-password-456", auth.RoleViewer); err != nil {
		t.Fatalf("CreateUser() error = %v", err)
	}
	viewerToken, viewer, err := manager.CreateUserSession("role-reader", auth.RoleViewer)
	if err != nil {
		t.Fatalf("CreateUserSession() error = %v", err)
	}
	handler := newRawTestHandler(t, manager)
	viewerHandler := authenticatedTestHandler{
		next:  handler,
		token: viewerToken,
		csrf:  viewer.CSRF,
	}
	before := httptest.NewRecorder()
	viewerHandler.ServeHTTP(before, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/accounts",
		nil,
	))
	if before.Code != http.StatusOK {
		t.Fatalf("viewer accounts status = %d; body: %s", before.Code, before.Body.String())
	}

	adminHandler := authenticatedTestHandler{
		next:  handler,
		token: adminToken,
		csrf:  adminCSRF,
	}
	updateRequest := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/auth/users/role-reader",
		strings.NewReader(`{"role":"operator"}`),
	)
	updateRequest.Header.Set("Content-Type", "application/json")
	updated := httptest.NewRecorder()
	adminHandler.ServeHTTP(updated, updateRequest)
	if updated.Code != http.StatusOK {
		t.Fatalf("update role status = %d; body: %s", updated.Code, updated.Body.String())
	}

	after := httptest.NewRecorder()
	viewerHandler.ServeHTTP(after, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/accounts",
		nil,
	))
	if after.Code != http.StatusUnauthorized {
		t.Fatalf("stale session status = %d; body: %s", after.Code, after.Body.String())
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
			Running:       false,
			QueuedTargets: 2,
			NextRunAt:     &nextRun,
			LastError:     "partial",
			Accounts: []monitorscheduler.AccountStatus{{
				AccountID:           7,
				TargetCount:         2,
				QueuedTargets:       2,
				ConsecutiveFailures: 1,
			}},
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
		Running   bool       `json:"running"`
		Queued    int        `json:"queued_targets"`
		NextRunAt *time.Time `json:"next_run_at"`
		Accounts  []struct {
			AccountID int64 `json:"account_id"`
			Queued    int   `json:"queued_targets"`
		} `json:"accounts"`
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
		payload.Queued != 2 ||
		payload.NextRunAt == nil ||
		!payload.NextRunAt.Equal(nextRun) ||
		len(payload.Accounts) != 1 ||
		payload.Accounts[0].AccountID != 7 ||
		payload.Accounts[0].Queued != 2 ||
		payload.Collection.TargetCount != 1 ||
		payload.Collection.FloodWaitTargetCount != 1 ||
		payload.Collection.ConsecutiveFailures != 1 {
		t.Fatalf("monitor status payload = %+v", payload)
	}
}

func TestEvidenceManifestAPIIncludesIntegritySummary(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "manifest-api.db"),
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
		BatchID:         "manifest-api-batch",
		Source:          "test",
		AccountID:       7,
		PeerType:        "chat",
		PeerID:          42,
		PeerTitle:       "Ops",
		CheckpointAfter: 101,
		Complete:        true,
		Messages: []operations.MessageRecord{{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    42,
			PeerTitle: "Ops",
			MessageID: 101,
			Sender:    "Alice",
			Text:      "manifest API",
			SentAt:    time.Now().UTC(),
		}},
	}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
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
		nil,
	).Handler()
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/evidence/manifest?days=7&account_id=7",
		nil,
	)
	request.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", recorder.Code, recorder.Body.String())
	}
	var manifest operations.EvidenceManifest
	if err := json.NewDecoder(recorder.Body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.DataState != "complete" ||
		manifest.EventCount != 1 ||
		manifest.BatchCount != 1 ||
		manifest.AccountID != 7 ||
		manifest.GeneratedBy == "" ||
		len(manifest.EvidenceSHA256) != 64 {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func TestSearchIndexHealthAndRebuildAPI(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "search-index-api.db"),
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
		BatchID:         "search-index-api",
		Source:          "test",
		AccountID:       7,
		PeerType:        "chat",
		PeerID:          42,
		PeerTitle:       "Operations",
		CheckpointAfter: 101,
		Complete:        true,
		Messages: []operations.MessageRecord{{
			AccountID: 7,
			PeerType:  "chat",
			PeerID:    42,
			PeerTitle: "Operations",
			MessageID: 101,
			Sender:    "Alice",
			Text:      "search index API",
			SentAt:    time.Now().UTC(),
		}},
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	manager, adminToken, adminCSRF := testAuthentication()
	if _, err := manager.CreateUser("search-reader", "strong-password-456", auth.RoleViewer); err != nil {
		t.Fatalf("create viewer user: %v", err)
	}
	viewerToken, viewerSession, err := manager.CreateUserSession("search-reader", auth.RoleViewer)
	if err != nil {
		t.Fatalf("create viewer session: %v", err)
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
	admin := authenticatedTestHandler{next: handler, token: adminToken, csrf: adminCSRF}
	viewer := authenticatedTestHandler{next: handler, token: viewerToken, csrf: viewerSession.CSRF}

	healthRecorder := httptest.NewRecorder()
	admin.ServeHTTP(healthRecorder, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/search/health",
		nil,
	))
	if healthRecorder.Code != http.StatusOK {
		t.Fatalf("health status = %d; body: %s",
			healthRecorder.Code, healthRecorder.Body.String())
	}
	var health operations.SearchIndexHealth
	if err := json.NewDecoder(healthRecorder.Body).Decode(&health); err != nil {
		t.Fatalf("decode search index health: %v", err)
	}
	if health.Status != operations.SearchIndexHealthy ||
		health.SearchableCount != 1 ||
		!health.IntegrityOK {
		t.Fatalf("search index health = %+v", health)
	}

	viewerHealthRecorder := httptest.NewRecorder()
	viewer.ServeHTTP(viewerHealthRecorder, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/search/health",
		nil,
	))
	if viewerHealthRecorder.Code != http.StatusOK {
		t.Fatalf("viewer health status = %d; body: %s",
			viewerHealthRecorder.Code, viewerHealthRecorder.Body.String())
	}

	viewerRebuildRecorder := httptest.NewRecorder()
	viewer.ServeHTTP(viewerRebuildRecorder, httptest.NewRequest(
		http.MethodPost,
		"/api/v1/search/rebuild",
		strings.NewReader(`{}`),
	))
	if viewerRebuildRecorder.Code != http.StatusForbidden {
		t.Fatalf("viewer rebuild status = %d; body: %s",
			viewerRebuildRecorder.Code, viewerRebuildRecorder.Body.String())
	}

	rebuildRequest := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/search/rebuild",
		strings.NewReader(`{}`),
	)
	rebuildRequest.Header.Set("Content-Type", "application/json")
	rebuildRecorder := httptest.NewRecorder()
	admin.ServeHTTP(rebuildRecorder, rebuildRequest)
	if rebuildRecorder.Code != http.StatusOK {
		t.Fatalf("rebuild status = %d; body: %s",
			rebuildRecorder.Code, rebuildRecorder.Body.String())
	}
	if err := json.NewDecoder(rebuildRecorder.Body).Decode(&health); err != nil {
		t.Fatalf("decode rebuilt search index health: %v", err)
	}
	if health.Status != operations.SearchIndexHealthy ||
		health.RebuildCount != 1 ||
		!health.RebuildDoesNotChangeEvidence {
		t.Fatalf("rebuilt search index health = %+v", health)
	}

	auditPage, err := operationsService.ListAuditEvents(ctx, operations.AuditFilter{
		Actor: "admin",
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("ListAuditEvents() error = %v", err)
	}
	foundRebuildAudit := false
	for _, event := range auditPage.Items {
		if event.Route == "/api/v1/search/rebuild" &&
			event.StatusCode == http.StatusOK &&
			event.Outcome == "success" {
			foundRebuildAudit = true
			break
		}
	}
	if !foundRebuildAudit {
		t.Fatalf("rebuild audit not recorded: %+v", auditPage.Items)
	}
}

func TestCorrelationAnalysisAPIReturnsTraceableGroups(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "correlation-api.db"),
	)
	if err != nil {
		t.Fatalf("open operations service: %v", err)
	}
	t.Cleanup(func() {
		if err := operationsService.Close(); err != nil {
			t.Errorf("close operations service: %v", err)
		}
	})
	const cryptoAddress = "0x52908400098527886E0F7030069857D2E4169EE7"
	for _, accountID := range []int64{1, 2} {
		if _, err := operationsService.IngestBatch(ctx, operations.IngestBatchInput{
			BatchID:         fmt.Sprintf("correlation-api-%d", accountID),
			Source:          "test",
			AccountID:       accountID,
			PeerType:        "chat",
			PeerID:          100 + accountID,
			PeerTitle:       "Operations",
			CheckpointAfter: 101,
			Complete:        true,
			Messages: []operations.MessageRecord{{
				AccountID: accountID,
				PeerType:  "chat",
				PeerID:    100 + accountID,
				PeerTitle: "Operations",
				MessageID: 101,
				Sender:    "Alice Smith",
				Text:      "shared incident link https://example.com/incident?id=42 address " + strings.ToLower(cryptoAddress),
				SentAt:    time.Now().UTC(),
			}},
		}); err != nil {
			t.Fatalf("seed account %d: %v", accountID, err)
		}
	}
	manager, _, _ := testAuthentication()
	if _, err := manager.CreateUser("correlation-reader", "strong-password-456", auth.RoleViewer); err != nil {
		t.Fatalf("create viewer user: %v", err)
	}
	viewerToken, viewerSession, err := manager.CreateUserSession(
		"correlation-reader",
		auth.RoleViewer,
	)
	if err != nil {
		t.Fatalf("create viewer session: %v", err)
	}
	webHandler, err := web.New()
	if err != nil {
		t.Fatalf("create web handler: %v", err)
	}
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
		token: viewerToken,
		csrf:  viewerSession.CSRF,
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/correlations?days=30&types=link",
		nil,
	))
	if recorder.Code != http.StatusOK {
		t.Fatalf("correlation status = %d; body: %s", recorder.Code, recorder.Body.String())
	}
	var analysis operations.CorrelationAnalysis
	if err := json.NewDecoder(recorder.Body).Decode(&analysis); err != nil {
		t.Fatalf("decode correlation analysis: %v", err)
	}
	if len(analysis.Groups) != 1 ||
		analysis.Groups[0].Type != operations.CorrelationLink ||
		analysis.Groups[0].AccountCount != 2 ||
		analysis.Groups[0].Sources[0].EvidenceID == "" ||
		analysis.GoverningNote == "" {
		t.Fatalf("correlation analysis = %+v", analysis)
	}
	cryptoRecorder := httptest.NewRecorder()
	handler.ServeHTTP(cryptoRecorder, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/correlations?days=30&types=crypto_address",
		nil,
	))
	if cryptoRecorder.Code != http.StatusOK {
		t.Fatalf("crypto correlation status = %d; body: %s",
			cryptoRecorder.Code, cryptoRecorder.Body.String())
	}
	var cryptoAnalysis operations.CorrelationAnalysis
	if err := json.NewDecoder(cryptoRecorder.Body).Decode(&cryptoAnalysis); err != nil {
		t.Fatalf("decode crypto correlation analysis: %v", err)
	}
	if len(cryptoAnalysis.Groups) != 1 ||
		cryptoAnalysis.Groups[0].Type != operations.CorrelationCrypto ||
		cryptoAnalysis.Groups[0].Network != operations.CryptoNetworkEthereum ||
		!strings.HasPrefix(cryptoAnalysis.Groups[0].ExplorerURL, "https://etherscan.io/address/") ||
		cryptoAnalysis.Groups[0].Sources[0].EvidenceID == "" {
		t.Fatalf("crypto correlation analysis = %+v", cryptoAnalysis)
	}

	invalidRecorder := httptest.NewRecorder()
	handler.ServeHTTP(invalidRecorder, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/correlations?types=identity",
		nil,
	))
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid correlation status = %d; body: %s",
			invalidRecorder.Code, invalidRecorder.Body.String())
	}
	negativeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(negativeRecorder, httptest.NewRequest(
		http.MethodGet,
		"/api/v1/correlations?days=-1",
		nil,
	))
	if negativeRecorder.Code != http.StatusBadRequest {
		t.Fatalf("negative correlation status = %d; body: %s",
			negativeRecorder.Code, negativeRecorder.Body.String())
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

func TestDeleteChatMessagesRecordsEvidenceAndTombstone(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "delete-evidence.db"),
	)
	if err != nil {
		t.Fatalf("operations.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := operationsService.Close(); err != nil {
			t.Errorf("operations.Close() error = %v", err)
		}
	})
	if _, _, err := operationsService.IngestMessages(ctx, []operations.MessageRecord{{
		AccountID: 42,
		PeerType:  "user",
		PeerID:    99,
		PeerTitle: "Alice",
		MessageID: 7,
		Sender:    "Alice",
		Text:      "delete this",
		SentAt:    time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	accounts := &fakeAccountService{
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	chats := &fakeTelegramChatService{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/accounts/42/chats/user/99/messages/delete",
		strings.NewReader(`{"message_ids":[7],"scope":"me"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	newTestHandlerWithOperations(accounts, chats, operationsService).
		ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", recorder.Code, recorder.Body.String())
	}
	events, err := operationsService.ListEvidenceEvents(ctx, 42, "user", 99, 7, 10)
	if err != nil {
		t.Fatalf("ListEvidenceEvents() error = %v", err)
	}
	if len(events) != 2 ||
		events[0].EventType != "delete" ||
		events[0].Actor == "" ||
		events[1].EventType != "message" {
		t.Fatalf("deletion evidence = %+v", events)
	}
	page, err := operationsService.SearchMessages(ctx, operations.MessageQuery{
		Query: "delete this",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("SearchMessages() error = %v", err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("deleted message remained searchable: %+v", page)
	}
}

func TestDeleteChatMessagesReportsEvidenceFailure(t *testing.T) {
	ctx := context.Background()
	operationsService, err := operations.Open(
		ctx,
		filepath.Join(t.TempDir(), "delete-evidence-failure.db"),
	)
	if err != nil {
		t.Fatalf("operations.Open() error = %v", err)
	}
	if err := operationsService.Close(); err != nil {
		t.Fatalf("operations.Close() error = %v", err)
	}
	accounts := &fakeAccountService{
		created: account.Account{ID: 42, Status: account.StatusOnline},
	}
	chats := &fakeTelegramChatService{}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/accounts/42/chats/user/99/messages/delete",
		strings.NewReader(`{"message_ids":[7],"scope":"me"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	newTestHandlerWithOperations(accounts, chats, operationsService).
		ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "deletion_evidence_failed") {
		t.Fatalf("body = %s, want evidence failure code", recorder.Body.String())
	}
}

func newTestHandlerWithOperations(
	accounts AccountService,
	chats TelegramChatService,
	operationsService OperationsService,
) http.Handler {
	webHandler, err := web.New()
	if err != nil {
		panic(err)
	}
	manager, token, csrf := testAuthentication()
	return authenticatedTestHandler{
		next: New(
			accounts,
			&fakeTelegramAuthService{},
			chats,
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

func newTestHandlerWithProxy(
	accounts AccountService,
	chats TelegramChatService,
	proxies AccountProxyStore,
	options ...Option,
) http.Handler {
	webHandler, err := web.New()
	if err != nil {
		panic(err)
	}
	manager, token, csrf := testAuthentication()
	apiOptions := []Option{WithAccountProxyStore(proxies)}
	apiOptions = append(apiOptions, options...)
	return authenticatedTestHandler{
		next: New(
			accounts,
			&fakeTelegramAuthService{},
			chats,
			nil,
			manager,
			webHandler.Handler(),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			nil,
			nil,
			nil,
			apiOptions...,
		).Handler(),
		token: token,
		csrf:  csrf,
	}
}
