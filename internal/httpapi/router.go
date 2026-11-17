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
}

type TelegramAuthService interface {
	Configured() bool
	SendCode(ctx context.Context, phone, codeSource string) (telegramauth.Challenge, error)
	SubmitCode(ctx context.Context, loginID, code string) (telegramauth.Result, error)
	SubmitPassword(ctx context.Context, loginID, password string) (telegramauth.Result, error)
	FetchCode(ctx context.Context, loginID string) (telegramauth.Result, error)
	Cancel(loginID string) error
}

type TelegramChatService interface {
	Chats(ctx context.Context, accountID int64, limit int) (telegramchat.ChatPage, error)
	Messages(
		ctx context.Context,
		accountID int64,
		peerType string,
		peerID int64,
		beforeID int,
		limit int,
	) (telegramchat.MessagePage, error)
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
	mux.HandleFunc("GET /api/v1/accounts/{id}", a.getAccount)
	mux.HandleFunc("GET /api/v1/accounts/{id}/chats", a.listAccountChats)
	mux.HandleFunc("GET /api/v1/accounts/{id}/chats/{peerType}/{peerID}/messages", a.listChatMessages)
	mux.HandleFunc("GET /api/v1/settings", a.getSettings)
	mux.HandleFunc("PUT /api/v1/settings", a.updateSettings)
	mux.HandleFunc("GET /api/v1/telegram/auth/config", a.telegramAuthConfig)
	mux.HandleFunc("POST /api/v1/telegram/auth/send-code", a.sendTelegramCode)
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
		a.writeChatError(w, accountID, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) listChatMessages(w http.ResponseWriter, r *http.Request) {
	accountID, ok := a.authorizedAccount(w, r)
	if !ok {
		return
	}
	peerType := r.PathValue("peerType")
	if peerType != "user" && peerType != "chat" && peerType != "channel" {
		writeError(w, http.StatusBadRequest, "invalid_peer", "chat type must be user, chat, or channel")
		return
	}
	peerID, err := strconv.ParseInt(r.PathValue("peerID"), 10, 64)
	if err != nil || peerID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_peer", "chat ID must be a positive integer")
		return
	}
	limit, ok := parseLimit(w, r, 50)
	if !ok {
		return
	}
	beforeID := 0
	if raw := r.URL.Query().Get("before_id"); raw != "" {
		beforeID, err = strconv.Atoi(raw)
		if err != nil || beforeID < 0 {
			writeError(w, http.StatusBadRequest, "invalid_query", "before_id must be a non-negative integer")
			return
		}
	}
	page, err := a.chats.Messages(r.Context(), accountID, peerType, peerID, beforeID, limit)
	if err != nil {
		a.writeChatError(w, accountID, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
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

func (a *API) writeChatError(w http.ResponseWriter, accountID int64, err error) {
	switch {
	case errors.Is(err, telegramchat.ErrNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "telegram_not_configured",
			"Telegram 聊天服务尚未配置")
	case errors.Is(err, telegramchat.ErrSessionMissing):
		writeError(w, http.StatusNotFound, "session_not_found", "该账号的授权会话不存在，请重新登录")
	case errors.Is(err, telegramchat.ErrPeerNotFound):
		writeError(w, http.StatusNotFound, "chat_not_found", "未找到该聊天")
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
		AppEnvironment        *string   `json:"app_env"`
		LogLevel              *string   `json:"log_level"`
		HTTPAddr              *string   `json:"http_addr"`
		TelegramAPIID         *int      `json:"telegram_api_id"`
		TelegramAPIHash       *string   `json:"telegram_api_hash"`
		TelegramCodeHosts     *[]string `json:"telegram_code_fetch_hosts"`
		TelegramCodeDNSServer *string   `json:"telegram_code_dns_server"`
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
	if input.TelegramCodeHosts != nil {
		next.TelegramCodeHosts = append([]string(nil), (*input.TelegramCodeHosts)...)
	}
	if input.TelegramCodeDNSServer != nil {
		next.TelegramCodeDNSServer = *input.TelegramCodeDNSServer
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
	TelegramCodeFetchHosts    []string `json:"telegram_code_fetch_hosts"`
	TelegramCodeDNSServer     string   `json:"telegram_code_dns_server"`
	RestartRequired           bool     `json:"restart_required"`
	EnvironmentOverrides      []string `json:"environment_overrides"`
}

func settingsResponse(service SettingsService) settingsAPIResponse {
	values := service.Snapshot()
	response := settingsAPIResponse{
		AppEnvironment:         values.AppEnvironment,
		LogLevel:               values.LogLevel,
		HTTPAddr:               values.HTTPAddr,
		TelegramAPIID:          values.TelegramAPIID,
		TelegramCodeFetchHosts: values.TelegramCodeHosts,
		TelegramCodeDNSServer:  values.TelegramCodeDNSServer,
		RestartRequired:        service.RestartRequired(),
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
		"TELEGRAM_CODE_FETCH_HOSTS",
		"TELEGRAM_CODE_DNS_SERVER",
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
