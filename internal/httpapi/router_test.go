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
	"github.com/ThanatosXingYu/TeleNexux/internal/web"
)

type fakeAccountService struct {
	items     []account.Account
	created   account.Account
	createErr error
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

func (s *fakeAccountService) List(_ context.Context, _ account.Filter) ([]account.Account, error) {
	return s.items, s.listErr
}

func (s *fakeAccountService) Get(_ context.Context, _ int64) (account.Account, error) {
	if s.getErr != nil {
		return account.Account{}, s.getErr
	}
	return s.created, nil
}

func newTestHandler(service AccountService) http.Handler {
	webHandler, err := web.New()
	if err != nil {
		panic(err)
	}
	return New(service, webHandler.Handler(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
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
