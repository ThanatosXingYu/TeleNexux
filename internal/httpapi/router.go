// Package httpapi exposes the TeleNexus HTTP interface.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/settings"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramauth"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

const maxRequestBody = 1 << 20

type AccountService interface {
	Create(ctx context.Context, input account.CreateInput) (account.Account, error)
	UpsertAuthorized(ctx context.Context, input account.CreateInput) (account.Account, error)
	List(ctx context.Context, filter account.Filter) ([]account.Account, error)
	Get(ctx context.Context, id int64) (account.Account, error)
	UpdateStatus(ctx context.Context, id int64, status string) (account.Account, error)
}

type TelegramAuthService interface {
	Configured() bool
	SendCode(ctx context.Context, phone, codeSource string) (telegramauth.Challenge, error)
	SubmitCode(ctx context.Context, loginID, code string) (telegramauth.Result, error)
	SubmitPassword(ctx context.Context, loginID, password string) (telegramauth.Result, error)
	FetchCode(ctx context.Context, loginID string) (telegramauth.Result, error)
	Cancel(loginID string) error
	StartQR(ctx context.Context) (telegramauth.QRChallenge, error)
	PollQR(loginID string) (telegramauth.QRResult, error)
	CancelQR(loginID string) error
}

type TelegramChatService interface {
	ValidateSession(ctx context.Context, accountID int64) error
	Chats(ctx context.Context, accountID int64, limit int) (telegramchat.ChatPage, error)
	SearchChats(ctx context.Context, accountID int64, query string, limit int) (telegramchat.ChatPage, error)
	Messages(
		ctx context.Context,
		accountID int64,
		peerType string,
		peerID int64,
		beforeID int,
		limit int,
	) (telegramchat.MessagePage, error)
	CurrentProfile(ctx context.Context, accountID int64) (telegramchat.Profile, error)
	UpdateCurrentProfile(
		ctx context.Context,
		accountID int64,
		input telegramchat.UpdateProfileInput,
	) (telegramchat.Profile, error)
	ChatProfile(
		ctx context.Context,
		accountID int64,
		peerType string,
		peerID int64,
	) (telegramchat.Profile, error)
	Avatar(ctx context.Context, accountID int64, peerType string, peerID int64) (telegramchat.Avatar, error)
	DeleteMessages(
		ctx context.Context,
		accountID int64,
		peerType string,
		peerID int64,
		messageIDs []int,
	) error
	ForwardMessages(
		ctx context.Context,
		accountID int64,
		fromType string,
		fromID int64,
		toType string,
		toID int64,
		messageIDs []int,
	) error
}

type SettingsService interface {
	Snapshot() settings.Values
	Save(values settings.Values) error
	RestartRequired() bool
}

type API struct {
	accounts AccountService
	telegram TelegramAuthService
	chats    TelegramChatService
	settings SettingsService
	web      http.Handler
	logger   *slog.Logger
}

func New(
	accounts AccountService,
	telegram TelegramAuthService,
	chats TelegramChatService,
	settingsService SettingsService,
	webHandler http.Handler,
	logger *slog.Logger,
) *API {
	return &API{
		accounts: accounts,
		telegram: telegram,
		chats:    chats,
		settings: settingsService,
		web:      webHandler,
		logger:   logger,
	}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", a.web)
	mux.Handle("GET /", a.web)
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /api/v1/accounts", a.listAccounts)
	mux.HandleFunc("POST /api/v1/accounts", a.createAccount)
	mux.HandleFunc("POST /api/v1/accounts/refresh", a.refreshAccounts)
	mux.HandleFunc("GET /api/v1/accounts/{id}", a.getAccount)
	mux.HandleFunc("POST /api/v1/accounts/{id}/refresh", a.refreshAccount)
	mux.HandleFunc("GET /api/v1/accounts/{id}/chats", a.listAccountChats)
	mux.HandleFunc("GET /api/v1/accounts/{id}/search", a.searchAccountChats)
	mux.HandleFunc("GET /api/v1/accounts/{id}/chats/{peerType}/{peerID}/messages", a.listChatMessages)
	mux.HandleFunc("POST /api/v1/accounts/{id}/chats/{peerType}/{peerID}/messages/delete", a.deleteChatMessages)
	mux.HandleFunc("POST /api/v1/accounts/{id}/chats/{peerType}/{peerID}/messages/forward", a.forwardChatMessages)
	mux.HandleFunc("GET /api/v1/accounts/{id}/profile", a.getCurrentProfile)
	mux.HandleFunc("PUT /api/v1/accounts/{id}/profile", a.updateCurrentProfile)
	mux.HandleFunc("GET /api/v1/accounts/{id}/chats/{peerType}/{peerID}/profile", a.getChatProfile)
	mux.HandleFunc("GET /api/v1/accounts/{id}/chats/{peerType}/{peerID}/avatar", a.getChatAvatar)
	mux.HandleFunc("GET /api/v1/settings", a.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", a.updateSettings)
	mux.HandleFunc("GET /api/v1/telegram/auth/config", a.telegramAuthConfig)
	mux.HandleFunc("POST /api/v1/telegram/auth/send-code", a.sendTelegramCode)
	mux.HandleFunc("POST /api/v1/telegram/auth/qr", a.startTelegramQR)
	mux.HandleFunc("GET /api/v1/telegram/auth/qr/{loginID}", a.pollTelegramQR)
	mux.HandleFunc("DELETE /api/v1/telegram/auth/qr/{loginID}", a.cancelTelegramQR)
	mux.HandleFunc("POST /api/v1/telegram/auth/{loginID}/code", a.submitTelegramCode)
	mux.HandleFunc("POST /api/v1/telegram/auth/{loginID}/password", a.submitTelegramPassword)
	mux.HandleFunc("POST /api/v1/telegram/auth/{loginID}/fetch-code", a.fetchTelegramCode)
	mux.HandleFunc("DELETE /api/v1/telegram/auth/{loginID}", a.cancelTelegramLogin)
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

func (a *API) refreshAccounts(w http.ResponseWriter, r *http.Request) {
	if !prepareJSONMutation(w, r) {
		return
	}
	if !a.prepareChatService(w) {
		return
	}
	items, err := a.accounts.List(r.Context(), account.Filter{Limit: 100})
	if err != nil {
		a.logger.Error("list accounts for refresh", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not list accounts")
		return
	}
	var wait sync.WaitGroup
	semaphore := make(chan struct{}, 3)
	for i := range items {
		if items[i].Status != account.StatusOnline && items[i].Status != account.StatusError {
			continue
		}
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			items[index] = a.refreshAccountStatus(r.Context(), items[index])
		}(i)
	}
	wait.Wait()
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  items,
		"limit":  len(items),
		"offset": 0,
	})
}

func (a *API) refreshAccount(w http.ResponseWriter, r *http.Request) {
	if !prepareJSONMutation(w, r) {
		return
	}
	if !a.prepareChatService(w) {
		return
	}
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
		a.logger.Error("get account for refresh", "account_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not load account")
		return
	}
	writeJSON(w, http.StatusOK, a.refreshAccountStatus(r.Context(), item))
}

func (a *API) refreshAccountStatus(ctx context.Context, item account.Account) account.Account {
	if item.Status != account.StatusOnline && item.Status != account.StatusError {
		return item
	}
	err := a.chats.ValidateSession(ctx, item.ID)
	status := account.StatusOnline
	if err != nil {
		if errors.Is(err, telegramchat.ErrSessionInvalid) ||
			errors.Is(err, telegramchat.ErrSessionMissing) {
			status = account.StatusError
		} else {
			a.logger.Warn("validate Telegram account session",
				"account_id", item.ID, "error", err)
			return item
		}
	}
	if status == item.Status {
		return item
	}
	updated, err := a.accounts.UpdateStatus(ctx, item.ID, status)
	if err != nil {
		a.logger.Error("persist Telegram account status",
			"account_id", item.ID, "status", status, "error", err)
		return item
	}
	return updated
}

func (a *API) prepareChatService(w http.ResponseWriter) bool {
	if a.chats == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram_not_configured",
			"请先配置 Telegram API 凭据并重启服务")
		return false
	}
	return true
}

func (a *API) listAccountChats(w http.ResponseWriter, r *http.Request) {
	accountID, ok := a.authorizedAccount(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r, 50)
	if !ok {
		return
	}
	page, err := a.chats.Chats(r.Context(), accountID, limit)
	if err != nil {
		a.writeChatError(r.Context(), w, accountID, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) searchAccountChats(w http.ResponseWriter, r *http.Request) {
	accountID, ok := a.authorizedAccount(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r, 30)
	if !ok {
		return
	}
	page, err := a.chats.SearchChats(r.Context(), accountID, r.URL.Query().Get("q"), limit)
	if err != nil {
		a.writeChatError(r.Context(), w, accountID, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) listChatMessages(w http.ResponseWriter, r *http.Request) {
	accountID, ok := a.authorizedAccount(w, r)
	if !ok {
		return
	}
	peerType, peerID, ok := parseChatPeer(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r, 50)
	if !ok {
		return
	}
	beforeID := 0
	if raw := r.URL.Query().Get("before_id"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid_query", "before_id must be a non-negative integer")
			return
		}
		beforeID = parsed
	}
	page, err := a.chats.Messages(r.Context(), accountID, peerType, peerID, beforeID, limit)
	if err != nil {
		a.writeChatError(r.Context(), w, accountID, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) deleteChatMessages(w http.ResponseWriter, r *http.Request) {
	accountID, peerType, peerID, ok := a.authorizedChatPeer(w, r)
	if !ok {
		return
	}
	var input struct {
		MessageIDs []int `json:"message_ids"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if err := a.chats.DeleteMessages(r.Context(), accountID, peerType, peerID, input.MessageIDs); err != nil {
		a.writeChatError(r.Context(), w, accountID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) forwardChatMessages(w http.ResponseWriter, r *http.Request) {
	accountID, fromType, fromID, ok := a.authorizedChatPeer(w, r)
	if !ok {
		return
	}
	var input struct {
		MessageIDs []int  `json:"message_ids"`
		ToType     string `json:"to_type"`
		ToID       int64  `json:"to_id"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if err := a.chats.ForwardMessages(
		r.Context(),
		accountID,
		fromType,
		fromID,
		input.ToType,
		input.ToID,
		input.MessageIDs,
	); err != nil {
		a.writeChatError(r.Context(), w, accountID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) getCurrentProfile(w http.ResponseWriter, r *http.Request) {
	accountID, ok := a.authorizedAccount(w, r)
	if !ok {
		return
	}
	profile, err := a.chats.CurrentProfile(r.Context(), accountID)
	if err != nil {
		a.writeChatError(r.Context(), w, accountID, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (a *API) updateCurrentProfile(w http.ResponseWriter, r *http.Request) {
	accountID, ok := a.authorizedAccount(w, r)
	if !ok {
		return
	}
	if !prepareJSONMutation(w, r) {
		return
	}
	var input telegramchat.UpdateProfileInput
	if !decodeJSONBody(w, r, &input) {
		return
	}
	profile, err := a.chats.UpdateCurrentProfile(r.Context(), accountID, input)
	if err != nil {
		a.writeChatError(r.Context(), w, accountID, err)
		return
	}
	item, err := a.accounts.UpsertAuthorized(r.Context(), account.CreateInput{
		ID:          profile.ID,
		PhoneNumber: profile.PhoneNumber,
		Username:    profile.Username,
		DisplayName: profile.DisplayName,
	})
	if err != nil {
		a.logger.Error("persist updated Telegram profile", "account_id", accountID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "资料已更新，但本地账号信息保存失败")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"profile": profile,
		"account": item,
	})
}

func (a *API) getChatProfile(w http.ResponseWriter, r *http.Request) {
	accountID, peerType, peerID, ok := a.authorizedChatPeer(w, r)
	if !ok {
		return
	}
	profile, err := a.chats.ChatProfile(r.Context(), accountID, peerType, peerID)
	if err != nil {
		a.writeChatError(r.Context(), w, accountID, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (a *API) getChatAvatar(w http.ResponseWriter, r *http.Request) {
	accountID, peerType, peerID, ok := a.authorizedChatPeer(w, r)
	if !ok {
		return
	}
	avatar, err := a.chats.Avatar(r.Context(), accountID, peerType, peerID)
	if err != nil {
		a.writeChatError(r.Context(), w, accountID, err)
		return
	}
	w.Header().Set("Content-Type", avatar.ContentType)
	w.Header().Set("Cache-Control", "private, max-age=900")
	w.Header().Set("Content-Length", strconv.Itoa(len(avatar.Data)))
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(avatar.Data); err != nil {
		a.logger.Debug("write Telegram avatar", "account_id", accountID, "error", err)
	}
}

func (a *API) authorizedChatPeer(w http.ResponseWriter, r *http.Request) (int64, string, int64, bool) {
	accountID, ok := a.authorizedAccount(w, r)
	if !ok {
		return 0, "", 0, false
	}
	peerType, peerID, ok := parseChatPeer(w, r)
	if !ok {
		return 0, "", 0, false
	}
	return accountID, peerType, peerID, true
}

func parseChatPeer(w http.ResponseWriter, r *http.Request) (string, int64, bool) {
	peerType := r.PathValue("peerType")
	if peerType != "user" && peerType != "chat" && peerType != "channel" {
		writeError(w, http.StatusBadRequest, "invalid_peer", "chat type must be user, chat, or channel")
		return "", 0, false
	}
	peerID, err := strconv.ParseInt(r.PathValue("peerID"), 10, 64)
	if err != nil || peerID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_peer", "chat ID must be a positive integer")
		return "", 0, false
	}
	return peerType, peerID, true
}

func (a *API) authorizedAccount(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if a.chats == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram_not_configured",
			"请先在系统设置中配置 Telegram API 凭据并重启服务")
		return 0, false
	}
	accountID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || accountID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "account ID must be a positive integer")
		return 0, false
	}
	item, err := a.accounts.Get(r.Context(), accountID)
	if errors.Is(err, account.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account was not found")
		return 0, false
	}
	if err != nil {
		a.logger.Error("get account for chat access", "account_id", accountID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "could not load account")
		return 0, false
	}
	if item.Status != account.StatusOnline {
		writeError(w, http.StatusConflict, "account_not_online", "account is not currently online")
		return 0, false
	}
	return accountID, true
}

func (a *API) writeChatError(
	ctx context.Context,
	w http.ResponseWriter,
	accountID int64,
	err error,
) {
	switch {
	case errors.Is(err, telegramchat.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "telegram_not_configured",
			"Telegram 聊天服务尚未配置")
	case errors.Is(err, telegramchat.ErrSessionMissing):
		if _, updateErr := a.accounts.UpdateStatus(ctx, accountID, account.StatusError); updateErr != nil {
			a.logger.Error("persist missing Telegram session status",
				"account_id", accountID, "error", updateErr)
		}
		writeError(w, http.StatusNotFound, "session_not_found", "该账号的授权会话不存在，请重新登录")
	case errors.Is(err, telegramchat.ErrSessionInvalid):
		a.logger.Warn("Telegram session is no longer authorized", "account_id", accountID, "error", err)
		if _, updateErr := a.accounts.UpdateStatus(ctx, accountID, account.StatusError); updateErr != nil {
			a.logger.Error("persist invalid Telegram session status",
				"account_id", accountID, "error", updateErr)
		}
		writeError(w, http.StatusConflict, "session_invalid", "该账号的 Telegram 授权已失效，请重新登录后再查看会话")
	case errors.Is(err, telegramchat.ErrPeerNotFound):
		writeError(w, http.StatusNotFound, "chat_not_found", "未找到该聊天")
	case errors.Is(err, telegramchat.ErrNoAvatar):
		writeError(w, http.StatusNotFound, "avatar_not_found", "该会话没有头像")
	case errors.Is(err, telegramchat.ErrInvalidInput):
		writeError(w, http.StatusUnprocessableEntity, "invalid_telegram_input", err.Error())
	case errors.Is(err, telegramchat.ErrTelegram):
		a.logger.Warn("load Telegram chats", "account_id", accountID, "error", err)
		writeError(w, http.StatusBadGateway, "telegram_error", "无法从 Telegram 读取聊天数据")
	default:
		a.logger.Error("load Telegram chats", "account_id", accountID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "聊天数据暂时不可用")
	}
}

func (a *API) getSettings(w http.ResponseWriter, _ *http.Request) {
	if a.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "系统设置尚未初始化")
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse(a.settings))
}

func (a *API) updateSettings(w http.ResponseWriter, r *http.Request) {
	if a.settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "系统设置尚未初始化")
		return
	}
	if !prepareJSONMutation(w, r) {
		return
	}
	var input struct {
		AppEnvironment  *string `json:"app_env"`
		LogLevel        *string `json:"log_level"`
		HTTPAddr        *string `json:"http_addr"`
		TelegramAPIID   *int    `json:"telegram_api_id"`
		TelegramAPIHash *string `json:"telegram_api_hash"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	next := a.settings.Snapshot()
	if input.AppEnvironment != nil {
		next.AppEnvironment = *input.AppEnvironment
	}
	if input.LogLevel != nil {
		next.LogLevel = *input.LogLevel
	}
	if input.HTTPAddr != nil {
		next.HTTPAddr = *input.HTTPAddr
	}
	if input.TelegramAPIID != nil {
		next.TelegramAPIID = *input.TelegramAPIID
		if next.TelegramAPIID == 0 {
			next.TelegramAPIHash = ""
		}
	}
	if input.TelegramAPIHash != nil && strings.TrimSpace(*input.TelegramAPIHash) != "" {
		next.TelegramAPIHash = *input.TelegramAPIHash
	}
	if err := a.settings.Save(next); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_settings", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settingsResponse(a.settings))
}

type settingsAPIResponse struct {
	AppEnvironment            string   `json:"app_env"`
	LogLevel                  string   `json:"log_level"`
	HTTPAddr                  string   `json:"http_addr"`
	TelegramAPIID             int      `json:"telegram_api_id"`
	TelegramAPIHashConfigured bool     `json:"telegram_api_hash_configured"`
	TelegramAPIHashHint       string   `json:"telegram_api_hash_hint"`
	RestartRequired           bool     `json:"restart_required"`
	EnvironmentOverrides      []string `json:"environment_overrides"`
}

func settingsResponse(service SettingsService) settingsAPIResponse {
	values := service.Snapshot()
	response := settingsAPIResponse{
		AppEnvironment:  values.AppEnvironment,
		LogLevel:        values.LogLevel,
		HTTPAddr:        values.HTTPAddr,
		TelegramAPIID:   values.TelegramAPIID,
		RestartRequired: service.RestartRequired(),
	}
	if values.TelegramAPIHash != "" {
		response.TelegramAPIHashConfigured = true
		response.TelegramAPIHashHint = "已配置（不会回显）"
	}
	for _, key := range []string{
		"APP_ENV",
		"LOG_LEVEL",
		"HTTP_ADDR",
		"TELEGRAM_API_ID",
		"TELEGRAM_API_HASH",
	} {
		if _, ok := os.LookupEnv(key); ok {
			response.EnvironmentOverrides = append(response.EnvironmentOverrides, key)
		}
	}
	return response
}

func (a *API) telegramAuthConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"configured": a.telegram != nil && a.telegram.Configured(),
	})
}

func (a *API) sendTelegramCode(w http.ResponseWriter, r *http.Request) {
	if !a.prepareAuthRequest(w, r) {
		return
	}
	var input struct {
		PhoneNumber string `json:"phone_number"`
		CodeSource  string `json:"code_url"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	challenge, err := a.telegram.SendCode(r.Context(), input.PhoneNumber, input.CodeSource)
	if err != nil {
		a.writeTelegramError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, challenge)
}

func (a *API) startTelegramQR(w http.ResponseWriter, r *http.Request) {
	if a.telegram == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram_not_configured",
			"请先配置 TELEGRAM_API_ID 和 TELEGRAM_API_HASH")
		return
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, http.StatusForbidden, "cross_site_request", "cross-site requests are not allowed")
		return
	}
	challenge, err := a.telegram.StartQR(r.Context())
	if err != nil {
		a.writeTelegramError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, challenge)
}

func (a *API) pollTelegramQR(w http.ResponseWriter, r *http.Request) {
	if a.telegram == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram_not_configured",
			"请先配置 TELEGRAM_API_ID 和 TELEGRAM_API_HASH")
		return
	}
	result, err := a.telegram.PollQR(r.PathValue("loginID"))
	if err != nil {
		a.writeTelegramError(w, err)
		return
	}
	a.writeQRResult(w, r, result)
}

func (a *API) cancelTelegramQR(w http.ResponseWriter, r *http.Request) {
	if a.telegram == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram_not_configured",
			"请先配置 TELEGRAM_API_ID 和 TELEGRAM_API_HASH")
		return
	}
	if err := a.telegram.CancelQR(r.PathValue("loginID")); err != nil {
		a.writeTelegramError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) submitTelegramCode(w http.ResponseWriter, r *http.Request) {
	if !a.prepareAuthRequest(w, r) {
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	result, err := a.telegram.SubmitCode(r.Context(), r.PathValue("loginID"), input.Code)
	if err != nil {
		a.writeTelegramError(w, err)
		return
	}
	a.writeTelegramResult(w, r, result)
}

func (a *API) submitTelegramPassword(w http.ResponseWriter, r *http.Request) {
	if !a.prepareAuthRequest(w, r) {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	result, err := a.telegram.SubmitPassword(r.Context(), r.PathValue("loginID"), input.Password)
	if err != nil {
		a.writeTelegramError(w, err)
		return
	}
	a.writeTelegramResult(w, r, result)
}

func (a *API) fetchTelegramCode(w http.ResponseWriter, r *http.Request) {
	if !a.prepareAuthRequest(w, r) {
		return
	}
	result, err := a.telegram.FetchCode(r.Context(), r.PathValue("loginID"))
	if err != nil {
		a.writeTelegramError(w, err)
		return
	}
	a.writeTelegramResult(w, r, result)
}

func (a *API) cancelTelegramLogin(w http.ResponseWriter, r *http.Request) {
	if !a.prepareAuthRequest(w, r) {
		return
	}
	if err := a.telegram.Cancel(r.PathValue("loginID")); err != nil {
		a.writeTelegramError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) writeTelegramResult(w http.ResponseWriter, r *http.Request, result telegramauth.Result) {
	response := struct {
		NextStep string           `json:"next_step"`
		Account  *account.Account `json:"account,omitempty"`
	}{NextStep: result.NextStep}
	if result.NextStep == telegramauth.StepComplete {
		if result.Profile == nil {
			a.logger.Error("Telegram authentication completed without a profile")
			writeError(w, http.StatusBadGateway, "telegram_error", "Telegram login did not return an account")
			return
		}
		item, err := a.accounts.UpsertAuthorized(r.Context(), account.CreateInput{
			ID:          result.Profile.ID,
			PhoneNumber: result.Profile.PhoneNumber,
			Username:    result.Profile.Username,
			DisplayName: result.Profile.DisplayName,
		})
		if err != nil {
			a.logger.Error("save Telegram account", "telegram_user_id", result.Profile.ID, "error", err)
			writeError(w, http.StatusUnprocessableEntity, "invalid_telegram_account", "Telegram account metadata is invalid")
			return
		}
		response.Account = &item
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) writeQRResult(w http.ResponseWriter, r *http.Request, result telegramauth.QRResult) {
	response := struct {
		NextStep string           `json:"next_step"`
		Image    string           `json:"image,omitempty"`
		Account  *account.Account `json:"account,omitempty"`
	}{
		NextStep: result.NextStep,
		Image:    result.Image,
	}
	if result.NextStep == telegramauth.QRStepComplete {
		if result.Profile == nil {
			writeError(w, http.StatusBadGateway, "telegram_error", "Telegram QR 登录未返回账号")
			return
		}
		item, err := a.accounts.UpsertAuthorized(r.Context(), account.CreateInput{
			ID:          result.Profile.ID,
			PhoneNumber: result.Profile.PhoneNumber,
			Username:    result.Profile.Username,
			DisplayName: result.Profile.DisplayName,
		})
		if err != nil {
			a.logger.Error("save QR Telegram account", "telegram_user_id", result.Profile.ID, "error", err)
			writeError(w, http.StatusUnprocessableEntity, "invalid_telegram_account", "Telegram 账号资料无效")
			return
		}
		response.Account = &item
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) writeTelegramError(w http.ResponseWriter, err error) {
	var floodWait *telegramauth.FloodWaitError
	switch {
	case errors.As(err, &floodWait):
		seconds := max(1, int(floodWait.Duration.Round(time.Second)/time.Second))
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, "telegram_flood_wait",
			fmt.Sprintf("Telegram 要求等待 %d 秒后重试", seconds))
	case errors.Is(err, telegramauth.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "telegram_not_configured",
			"请先配置 TELEGRAM_API_ID 和 TELEGRAM_API_HASH")
	case errors.Is(err, telegramauth.ErrInvalidPhone):
		writeError(w, http.StatusUnprocessableEntity, "invalid_phone", "手机号格式不正确")
	case errors.Is(err, telegramauth.ErrInvalidCode):
		writeError(w, http.StatusUnprocessableEntity, "invalid_code", "验证码不正确")
	case errors.Is(err, telegramauth.ErrCodeExpired):
		writeError(w, http.StatusUnprocessableEntity, "code_expired", "验证码已过期，请重新获取")
	case errors.Is(err, telegramauth.ErrInvalidPassword):
		writeError(w, http.StatusUnprocessableEntity, "invalid_password", "二次验证密码不正确")
	case errors.Is(err, telegramauth.ErrLoginNotFound):
		writeError(w, http.StatusNotFound, "login_not_found", "登录会话不存在或已过期")
	case errors.Is(err, telegramauth.ErrLoginStage):
		writeError(w, http.StatusConflict, "invalid_login_step", "当前登录步骤不允许此操作")
	case errors.Is(err, telegramauth.ErrCodeSourceReject):
		writeError(w, http.StatusUnprocessableEntity, "code_source_rejected",
			"接码链接不在允许的主机列表中")
	case errors.Is(err, telegramauth.ErrCodeNotReady):
		writeError(w, http.StatusConflict, "code_not_ready", "验证码尚未到达")
	case errors.Is(err, telegramauth.ErrTelegram):
		a.logger.Warn("Telegram authentication failed", "error", err)
		writeError(w, http.StatusBadGateway, "telegram_error", "Telegram 登录失败，请稍后重试")
	default:
		a.logger.Error("telegram authentication error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "登录服务暂时不可用")
	}
}

func (a *API) prepareAuthRequest(w http.ResponseWriter, r *http.Request) bool {
	if a.telegram == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram_not_configured",
			"请先配置 TELEGRAM_API_ID 和 TELEGRAM_API_HASH")
		return false
	}
	return prepareJSONMutation(w, r)
}

func prepareJSONMutation(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		writeError(w, http.StatusForbidden, "cross_site_request", "cross-site requests are not allowed")
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != r.Host {
			writeError(w, http.StatusForbidden, "cross_site_request", "origin is not allowed")
			return false
		}
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.EqualFold(mediaType, "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "invalid_content_type",
				"Content-Type must be application/json")
			return false
		}
	}
	return true
}

func parseLimit(w http.ResponseWriter, r *http.Request, fallback int) (int, bool) {
	limit := fallback
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_query", "limit must be between 1 and 100")
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be valid JSON with known fields")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain a single JSON object")
		return false
	}
	return true
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
