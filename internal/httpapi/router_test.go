package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramauth"
	"github.com/ThanatosXingYu/TeleNexux/internal/web"
)

type fakeAccountService struct {
	items     []account.Account
	created   account.Account
	createErr error
	upsertErr error
	listErr   error
	getErr    error
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

type fakeTelegramAuthService struct {
	configured bool
	challenge  telegramauth.Challenge
	result     telegramauth.Result
	sendErr    error
	submitErr  error
	fetchErr   error
}

func (s *fakeTelegramAuthService) Configured() bool {
	return s.configured
}

func (s *fakeTelegramAuthService) SendCode(
	_ context.Context,
	_ string,
	_ string,
) (telegramauth.Challenge, error) {
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

func (s *fakeTelegramAuthService) FetchCode(
	_ context.Context,
	_ string,
) (telegramauth.Result, error) {
	return s.result, s.fetchErr
}

func (s *fakeTelegramAuthService) Cancel(_ string) error {
	return nil
}

func newTestHandler(service AccountService) http.Handler {
	return newTestHandlerWithTelegram(service, &fakeTelegramAuthService{})
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
	return New(service, telegram, webHandler.Handler(), logger).Handler()
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
