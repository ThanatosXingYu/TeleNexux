// Package httpapi exposes the TeleNexus HTTP interface.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
)

const maxRequestBody = 1 << 20

type AccountService interface {
	Create(ctx context.Context, input account.CreateInput) (account.Account, error)
	List(ctx context.Context, filter account.Filter) ([]account.Account, error)
	Get(ctx context.Context, id int64) (account.Account, error)
}

type API struct {
	accounts AccountService
	logger   *slog.Logger
}

func New(accounts AccountService, logger *slog.Logger) *API {
	return &API{accounts: accounts, logger: logger}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /api/v1/accounts", a.listAccounts)
	mux.HandleFunc("POST /api/v1/accounts", a.createAccount)
	mux.HandleFunc("GET /api/v1/accounts/{id}", a.getAccount)
	return a.recoverPanic(a.logRequests(mux))
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "telenexus",
	})
}

func (a *API) createAccount(w http.ResponseWriter, r *http.Request) {
	var input account.CreateInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be valid JSON with known fields")
		return
	}

	created, err := a.accounts.Create(r.Context(), input)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_account", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *API) listAccounts(w http.ResponseWriter, r *http.Request) {
	filter, err := parseFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
		return
	}
	items, err := a.accounts.List(r.Context(), filter)
	if err != nil {
		a.logger.Error("list accounts", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list accounts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}

func (a *API) getAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "account ID must be a positive integer")
		return
	}
	item, err := a.accounts.Get(r.Context(), id)
	if errors.Is(err, account.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account was not found")
		return
	}
	if err != nil {
		a.logger.Error("get account", "account_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not get account")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func parseFilter(r *http.Request) (account.Filter, error) {
	filter := account.Filter{Status: r.URL.Query().Get("status"), Limit: 20}
	if filter.Status != "" && !account.IsValidStatus(filter.Status) {
		return account.Filter{}, errors.New("status must be one of pending, online, paused, error")
	}

	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		filter.Limit, err = strconv.Atoi(raw)
		if err != nil || filter.Limit < 1 || filter.Limit > 100 {
			return account.Filter{}, errors.New("limit must be between 1 and 100")
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		filter.Offset, err = strconv.Atoi(raw)
		if err != nil || filter.Offset < 0 {
			return account.Filter{}, errors.New("offset must be a non-negative integer")
		}
	}
	return filter, nil
}

func (a *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(started),
		)
	})
}

func (a *API) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("panic serving request", "path", r.URL.Path, "panic", fmt.Sprint(recovered))
				writeError(w, http.StatusInternalServerError, "internal_error", "unexpected server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var response errorResponse
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(w, status, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("encode HTTP response", "error", err)
	}
}
