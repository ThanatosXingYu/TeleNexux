// Package httpapi exposes the TeleNexus HTTP interface.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/accountproxy"
	"github.com/ThanatosXingYu/TeleNexux/internal/auth"
	healthcheck "github.com/ThanatosXingYu/TeleNexux/internal/health"
	"github.com/ThanatosXingYu/TeleNexux/internal/monitorscheduler"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/settings"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramauth"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
)

const (
	maxRequestBody           = 1 << 20
	telegramAuthWindow       = time.Minute
	telegramAuthAttemptLimit = 12
	telegramAuthConcurrency  = 3
	accountVerificationTTL   = 10 * time.Minute
	accountValidationTimeout = 35 * time.Second
)

var errAccountValidationDeferred = errors.New("Telegram account validation deferred")

type AccountService interface {
	Create(ctx context.Context, input account.CreateInput) (account.Account, error)
	UpsertAuthorized(ctx context.Context, input account.CreateInput) (account.Account, error)
	List(ctx context.Context, filter account.Filter) ([]account.Account, error)
	Get(ctx context.Context, id int64) (account.Account, error)
	UpdateStatus(ctx context.Context, id int64, status string) (account.Account, error)
	RecordVerification(
		ctx context.Context,
		id int64,
		status string,
		reason string,
	) (account.Account, error)
	Update(ctx context.Context, id int64, input account.UpdateInput) (account.Account, error)
	SetProxy(ctx context.Context, id int64, proxy account.Proxy) (account.Account, error)
	ClearProxy(ctx context.Context, id int64) (account.Account, error)
	ScheduleDeletion(ctx context.Context, id int64) (account.Account, error)
	Restore(ctx context.Context, id int64) (account.Account, error)
}

type TelegramAuthService interface {
	Configured() bool
	SendCode(ctx context.Context, phone string, expectedAccountID int64) (telegramauth.Challenge, error)
	SubmitCode(ctx context.Context, loginID, code string) (telegramauth.Result, error)
	SubmitPassword(ctx context.Context, loginID, password string) (telegramauth.Result, error)
	Cancel(loginID string) error
	StartQR(ctx context.Context, expectedAccountID int64) (telegramauth.QRChallenge, error)
	PollQR(loginID string) (telegramauth.QRResult, error)
	CancelQR(loginID string) error
}

type TelegramChatService interface {
	InspectSession(ctx context.Context, accountID int64) (telegramchat.Profile, error)
	DeleteSession(accountID int64) error
	ResetAccount(accountID int64)
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
		revoke bool,
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

type AccountProxyStore interface {
	Get(accountID int64) (accountproxy.Profile, bool)
	Set(ctx context.Context, accountID int64, profile accountproxy.Profile) error
	Delete(ctx context.Context, accountID int64) (bool, error)
}

type ProxyChecker func(ctx context.Context, profile accountproxy.Profile) error

type HealthService interface {
	Check(ctx context.Context, detailed bool) healthcheck.Report
}

type OperationsService interface {
	CreateGroup(ctx context.Context, name, color string) (operations.Group, error)
	ListGroups(ctx context.Context) ([]operations.Group, error)
	DeleteGroup(ctx context.Context, id int64) error
	SetGroupAccounts(ctx context.Context, groupID int64, accountIDs []int64) error
	CreateRule(
		ctx context.Context,
		name string,
		accountID int64,
		peerType string,
		peerID int64,
		peerTitle string,
		keywords []string,
	) (operations.Rule, error)
	CreateRuleWithChannel(
		ctx context.Context,
		name string,
		accountID int64,
		peerType string,
		peerID int64,
		peerTitle string,
		notificationChannelID int64,
		keywords []string,
	) (operations.Rule, error)
	CreateRuleWithOptions(
		ctx context.Context,
		name string,
		accountID int64,
		peerType string,
		peerID int64,
		peerTitle string,
		notificationChannelID int64,
		keywords []string,
		cooldownSeconds int,
	) (operations.Rule, error)
	CreateRuleWithInput(ctx context.Context, input operations.RuleCreateInput) (operations.Rule, error)
	UpdateRule(ctx context.Context, id int64, input operations.RuleUpdateInput) (operations.Rule, error)
	ListRules(ctx context.Context, enabledOnly bool) ([]operations.Rule, error)
	ListRuleVersions(ctx context.Context, id int64) ([]operations.RuleVersion, error)
	PreviewRule(ctx context.Context, input operations.RulePreviewInput) (operations.RulePreview, error)
	ListNotificationChannels(ctx context.Context) ([]operations.NotificationChannel, error)
	CreateWebhookChannel(
		ctx context.Context,
		name string,
		url string,
		secret string,
	) (operations.NotificationChannel, error)
	DeleteNotificationChannel(ctx context.Context, id int64) error
	SetRuleEnabled(ctx context.Context, id int64, enabled bool, actor string) error
	DeleteRule(ctx context.Context, id int64) error
	ListAlerts(ctx context.Context, state string, limit int) ([]operations.Alert, error)
	ListAlertEvents(ctx context.Context, alertID int64, limit int) ([]operations.AlertEvent, error)
	UpdateAlert(ctx context.Context, id int64, input operations.AlertUpdateInput) (operations.Alert, error)
	IngestMessages(ctx context.Context, messages []operations.MessageRecord) (int, []operations.Alert, error)
	MonitorSummary(ctx context.Context) (operations.MonitorSummary, error)
	ListCollectionTargets(ctx context.Context) ([]operations.CollectionTarget, error)
	SearchMessages(ctx context.Context, filter operations.MessageQuery) (operations.MessagePage, error)
	SearchIndexHealth(ctx context.Context) (operations.SearchIndexHealth, error)
	RebuildSearchIndex(ctx context.Context) (operations.SearchIndexHealth, error)
	AnalyzeCorrelations(
		ctx context.Context,
		query operations.CorrelationQuery,
	) (operations.CorrelationAnalysis, error)
	BuildReport(ctx context.Context, days int) (operations.Report, error)
	BuildEvidenceManifest(
		ctx context.Context,
		filter operations.EvidenceManifestFilter,
	) (operations.EvidenceManifest, error)
	Dashboard(ctx context.Context, telegramReady bool) (operations.Dashboard, error)
	CleanupMessages(ctx context.Context, input operations.RetentionCleanupInput) (operations.RetentionResult, error)
	GetRetentionPolicy(ctx context.Context) (operations.RetentionPolicy, error)
	SetRetentionPolicy(ctx context.Context, retentionDays int, actor string) (operations.RetentionPolicy, error)
	ListLegalHolds(ctx context.Context) ([]operations.LegalHold, error)
	CreateLegalHold(ctx context.Context, input operations.LegalHoldInput) (operations.LegalHold, error)
	ReleaseLegalHold(ctx context.Context, id int64, actor string) (operations.LegalHold, error)
	RecordAudit(ctx context.Context, input operations.AuditEventInput) error
	ListAuditEvents(ctx context.Context, filter operations.AuditFilter) (operations.AuditPage, error)
}

type MonitorRunner interface {
	RunOnce(ctx context.Context) error
	Status() monitorscheduler.Status
}

type API struct {
	accounts   AccountService
	telegram   TelegramAuthService
	chats      TelegramChatService
	proxies    AccountProxyStore
	proxyCheck ProxyChecker
	settings   SettingsService
	auth       *auth.Manager
	healthSvc  HealthService
	operations OperationsService
	monitor    MonitorRunner
	web        http.Handler
	logger     *slog.Logger
	loginMu    sync.Mutex
	attempts   map[string]loginAttempt
	authOpsMu  sync.Mutex
	authOps    map[string]loginAttempt
	authSlots  chan struct{}
}

type Option func(*API)

func WithAccountProxyStore(store AccountProxyStore) Option {
	return func(api *API) {
		api.proxies = store
	}
}

func WithProxyChecker(checker ProxyChecker) Option {
	return func(api *API) {
		if checker != nil {
			api.proxyCheck = checker
		}
	}
}

func New(
	accounts AccountService,
	telegram TelegramAuthService,
	chats TelegramChatService,
	settingsService SettingsService,
	authManager *auth.Manager,
	webHandler http.Handler,
	logger *slog.Logger,
	healthService HealthService,
	operationsService OperationsService,
	monitorRunner MonitorRunner,
	options ...Option,
) *API {
	api := &API{
		accounts:   accounts,
		telegram:   telegram,
		chats:      chats,
		settings:   settingsService,
		auth:       authManager,
		healthSvc:  healthService,
		operations: operationsService,
		monitor:    monitorRunner,
		web:        webHandler,
		logger:     logger,
		attempts:   make(map[string]loginAttempt),
		authOps:    make(map[string]loginAttempt),
		authSlots:  make(chan struct{}, telegramAuthConcurrency),
		proxyCheck: accountproxy.Check,
	}
	for _, option := range options {
		if option != nil {
			option(api)
		}
	}
	return api
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", a.web)
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("GET /login", a.web.ServeHTTP)
	mux.HandleFunc("GET /api/v1/auth/session", a.authSession)
	mux.HandleFunc("POST /api/v1/auth/setup", a.setupAdministrator)
	mux.HandleFunc("POST /api/v1/auth/login", a.loginAdministrator)
	mux.HandleFunc("POST /api/v1/auth/logout",
		a.requireAPIPermission(auth.PermissionSession, a.logoutAdministrator))
	mux.HandleFunc("GET /api/v1/health",
		a.requireAPIPermission(auth.PermissionHealthRead, a.healthDetails))
	mux.HandleFunc("GET /api/v1/audit/events",
		a.requireAPIPermission(auth.PermissionAuditRead, a.listAuditEvents))
	mux.HandleFunc("GET /api/v1/dashboard",
		a.requireAPIPermission(auth.PermissionDashboardRead, a.dashboard))
	mux.HandleFunc("GET /api/v1/groups",
		a.requireAPIPermission(auth.PermissionGroupRead, a.listGroups))
	mux.HandleFunc("POST /api/v1/groups",
		a.requireAPIPermission(auth.PermissionGroupWrite, a.createGroup))
	mux.HandleFunc("DELETE /api/v1/groups/{id}",
		a.requireAPIPermission(auth.PermissionGroupWrite, a.deleteGroup))
	mux.HandleFunc("PUT /api/v1/groups/{id}/accounts",
		a.requireAPIPermission(auth.PermissionGroupWrite, a.updateGroupAccounts))
	mux.HandleFunc("GET /api/v1/monitor/rules",
		a.requireAPIPermission(auth.PermissionMonitorRead, a.listMonitorRules))
	mux.HandleFunc("POST /api/v1/monitor/rules",
		a.requireAPIPermission(auth.PermissionMonitorWrite, a.createMonitorRule))
	mux.HandleFunc("POST /api/v1/monitor/rules/preview",
		a.requireAPIPermission(auth.PermissionMonitorRead, a.previewMonitorRule))
	mux.HandleFunc("GET /api/v1/monitor/rules/{id}/versions",
		a.requireAPIPermission(auth.PermissionMonitorRead, a.listMonitorRuleVersions))
	mux.HandleFunc("PUT /api/v1/monitor/rules/{id}",
		a.requireAPIPermission(auth.PermissionMonitorWrite, a.updateMonitorRule))
	mux.HandleFunc("DELETE /api/v1/monitor/rules/{id}",
		a.requireAPIPermission(auth.PermissionMonitorWrite, a.deleteMonitorRule))
	mux.HandleFunc("GET /api/v1/monitor/channels",
		a.requireAPIPermission(auth.PermissionMonitorRead, a.listNotificationChannels))
	mux.HandleFunc("POST /api/v1/monitor/channels",
		a.requireAPIPermission(auth.PermissionMonitorWrite, a.createNotificationChannel))
	mux.HandleFunc("DELETE /api/v1/monitor/channels/{id}",
		a.requireAPIPermission(auth.PermissionMonitorWrite, a.deleteNotificationChannel))
	mux.HandleFunc("POST /api/v1/monitor/run",
		a.requireAPIPermission(auth.PermissionMonitorOperate, a.runMonitor))
	mux.HandleFunc("GET /api/v1/monitor/status",
		a.requireAPIPermission(auth.PermissionMonitorRead, a.monitorStatus))
	mux.HandleFunc("GET /api/v1/monitor/targets",
		a.requireAPIPermission(auth.PermissionMonitorRead, a.listMonitorTargets))
	mux.HandleFunc("GET /api/v1/monitor/alerts",
		a.requireAPIPermission(auth.PermissionMonitorRead, a.listMonitorAlerts))
	mux.HandleFunc("GET /api/v1/monitor/alerts/{id}/events",
		a.requireAPIPermission(auth.PermissionMonitorRead, a.listMonitorAlertEvents))
	mux.HandleFunc(
		"POST /api/v1/monitor/alerts/{id}/resolve",
		a.requireAPIPermission(auth.PermissionMonitorOperate, a.resolveMonitorAlert),
	)
	mux.HandleFunc(
		"PATCH /api/v1/monitor/alerts/{id}",
		a.requireAPIPermission(auth.PermissionMonitorOperate, a.updateMonitorAlert),
	)
	mux.HandleFunc("GET /api/v1/messages",
		a.requireAPIPermission(auth.PermissionMessageRead, a.searchMessages))
	mux.HandleFunc("POST /api/v1/messages/cleanup",
		a.requireAPIPermission(auth.PermissionMessageWrite, a.cleanupMessages))
	mux.HandleFunc("GET /api/v1/search/health",
		a.requireAPIPermission(auth.PermissionMessageRead, a.searchIndexHealth))
	mux.HandleFunc("POST /api/v1/search/rebuild",
		a.requireAPIPermission(auth.PermissionMessageWrite, a.rebuildSearchIndex))
	mux.HandleFunc("GET /api/v1/retention",
		a.requireAPIPermission(auth.PermissionRetentionRead, a.getRetentionPolicy))
	mux.HandleFunc("PUT /api/v1/retention",
		a.requireAPIPermission(auth.PermissionRetentionWrite, a.updateRetentionPolicy))
	mux.HandleFunc("GET /api/v1/retention/legal-holds",
		a.requireAPIPermission(auth.PermissionRetentionRead, a.listLegalHolds))
	mux.HandleFunc("POST /api/v1/retention/legal-holds",
		a.requireAPIPermission(auth.PermissionRetentionWrite, a.createLegalHold))
	mux.HandleFunc(
		"DELETE /api/v1/retention/legal-holds/{id}",
		a.requireAPIPermission(auth.PermissionRetentionWrite, a.releaseLegalHold),
	)
	mux.HandleFunc("GET /api/v1/reports",
		a.requireAPIPermission(auth.PermissionReportRead, a.messageReport))
	mux.HandleFunc("GET /api/v1/correlations",
		a.requireAPIPermission(auth.PermissionMessageRead, a.correlationAnalysis))
	mux.HandleFunc("GET /api/v1/evidence/manifest",
		a.requireAPIPermission(auth.PermissionReportRead, a.evidenceManifest))
	mux.Handle("GET /", a.requirePage(a.web))

	mux.HandleFunc("GET /api/v1/accounts",
		a.requireAPIPermission(auth.PermissionAccountRead, a.listAccounts))
	mux.HandleFunc("POST /api/v1/accounts",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.createAccount))
	mux.HandleFunc("POST /api/v1/accounts/refresh",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.refreshAccounts))
	mux.HandleFunc("GET /api/v1/accounts/{id}",
		a.requireAPIPermission(auth.PermissionAccountRead, a.getAccount))
	mux.HandleFunc("PUT /api/v1/accounts/{id}",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.updateAccount))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.deleteAccount))
	mux.HandleFunc("POST /api/v1/accounts/{id}/restore",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.restoreAccount))
	mux.HandleFunc("POST /api/v1/accounts/{id}/refresh",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.refreshAccount))
	mux.HandleFunc("PUT /api/v1/accounts/{id}/proxy",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.setAccountProxy))
	mux.HandleFunc("DELETE /api/v1/accounts/{id}/proxy",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.clearAccountProxy))
	mux.HandleFunc("POST /api/v1/accounts/{id}/proxy/check",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.checkAccountProxy))
	mux.HandleFunc("GET /api/v1/accounts/{id}/chats",
		a.requireAPIPermission(auth.PermissionChatRead, a.listAccountChats))
	mux.HandleFunc("GET /api/v1/accounts/{id}/search",
		a.requireAPIPermission(auth.PermissionChatRead, a.searchAccountChats))
	mux.HandleFunc(
		"GET /api/v1/accounts/{id}/chats/{peerType}/{peerID}/messages",
		a.requireAPIPermission(auth.PermissionChatRead, a.listChatMessages),
	)
	mux.HandleFunc(
		"POST /api/v1/accounts/{id}/chats/{peerType}/{peerID}/messages/delete",
		a.requireAPIPermission(auth.PermissionChatOperate, a.deleteChatMessages),
	)
	mux.HandleFunc(
		"POST /api/v1/accounts/{id}/chats/{peerType}/{peerID}/messages/forward",
		a.requireAPIPermission(auth.PermissionChatOperate, a.forwardChatMessages),
	)
	mux.HandleFunc("GET /api/v1/accounts/{id}/profile",
		a.requireAPIPermission(auth.PermissionAccountRead, a.getCurrentProfile))
	mux.HandleFunc("PUT /api/v1/accounts/{id}/profile",
		a.requireAPIPermission(auth.PermissionAccountWrite, a.updateCurrentProfile))
	mux.HandleFunc(
		"GET /api/v1/accounts/{id}/chats/{peerType}/{peerID}/profile",
		a.requireAPIPermission(auth.PermissionChatRead, a.getChatProfile),
	)
	mux.HandleFunc(
		"GET /api/v1/accounts/{id}/chats/{peerType}/{peerID}/avatar",
		a.requireAPIPermission(auth.PermissionChatRead, a.getChatAvatar),
	)
	mux.HandleFunc("GET /api/v1/settings",
		a.requireAPIPermission(auth.PermissionSettingsRead, a.getSettings))
	mux.HandleFunc("PUT /api/v1/settings",
		a.requireAPIPermission(auth.PermissionSettingsWrite, a.updateSettings))
	mux.HandleFunc("GET /api/v1/telegram/auth/config",
		a.requireAPIPermission(auth.PermissionTelegramAuth, a.telegramAuthConfig))
	mux.HandleFunc("POST /api/v1/telegram/auth/send-code",
		a.requireAPIPermission(auth.PermissionTelegramAuth, a.sendTelegramCode))
	mux.HandleFunc("POST /api/v1/telegram/auth/qr",
		a.requireAPIPermission(auth.PermissionTelegramAuth, a.startTelegramQR))
	mux.HandleFunc("GET /api/v1/telegram/auth/qr/{loginID}",
		a.requireAPIPermission(auth.PermissionTelegramAuth, a.pollTelegramQR))
	mux.HandleFunc("DELETE /api/v1/telegram/auth/qr/{loginID}",
		a.requireAPIPermission(auth.PermissionTelegramAuth, a.cancelTelegramQR))
	mux.HandleFunc(
		"POST /api/v1/telegram/auth/{loginID}/code",
		a.requireAPIPermission(auth.PermissionTelegramAuth, a.submitTelegramCode),
	)
	mux.HandleFunc(
		"POST /api/v1/telegram/auth/{loginID}/password",
		a.requireAPIPermission(auth.PermissionTelegramAuth, a.submitTelegramPassword),
	)
	mux.HandleFunc(
		"DELETE /api/v1/telegram/auth/{loginID}",
		a.requireAPIPermission(auth.PermissionTelegramAuth, a.cancelTelegramLogin),
	)
	return a.logRequests(a.recoverPanic(mux))
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	a.writeHealth(w, r, false)
}

func (a *API) healthDetails(w http.ResponseWriter, r *http.Request) {
	a.writeHealth(w, r, true)
}

func (a *API) writeHealth(w http.ResponseWriter, r *http.Request, detailed bool) {
	if a.healthSvc == nil {
		writeJSON(w, http.StatusServiceUnavailable, healthcheck.Report{
			Status:    "error",
			CheckedAt: time.Now().UTC(),
			Checks: []healthcheck.Check{{
				Name:    "service",
				Status:  "error",
				Message: "健康检查尚未初始化",
			}},
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	report := a.healthSvc.Check(ctx, detailed)
	status := http.StatusOK
	if report.Status == "error" {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, report)
}

func (a *API) dashboard(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	result, err := a.operations.Dashboard(r.Context(), a.chats != nil)
	if err != nil {
		a.logger.Error("build dashboard", "error", err)
		writeError(w, http.StatusInternalServerError, "dashboard_failed", "无法读取工作台数据")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	filter := operations.AuditFilter{
		Actor: strings.TrimSpace(r.URL.Query().Get("actor")),
	}
	var err error
	if raw := r.URL.Query().Get("limit"); raw != "" {
		filter.Limit, err = strconv.Atoi(raw)
		if err != nil || filter.Limit < 1 || filter.Limit > 200 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 200")
			return
		}
	}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		filter.Offset, err = strconv.Atoi(raw)
		if err != nil || filter.Offset < 0 {
			writeError(w, http.StatusBadRequest, "invalid_offset", "offset must be a non-negative integer")
			return
		}
	}
	page, err := a.operations.ListAuditEvents(r.Context(), filter)
	if err != nil {
		a.logger.Error("list operation audit", "error", err)
		writeError(w, http.StatusInternalServerError, "audit_unavailable", "操作审计记录暂时不可用")
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) listGroups(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	items, err := a.operations.ListGroups(r.Context())
	if err != nil {
		a.logger.Error("list account groups", "error", err)
		writeError(w, http.StatusInternalServerError, "groups_failed", "无法读取账号分组")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) createGroup(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	var input struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	item, err := a.operations.CreateGroup(r.Context(), input.Name, input.Color)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_group", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.operations.DeleteGroup(r.Context(), id); err != nil {
		a.writeOperationsError(w, err, "account group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) updateGroupAccounts(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		AccountIDs []int64 `json:"account_ids"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if err := a.operations.SetGroupAccounts(r.Context(), id, input.AccountIDs); err != nil {
		a.writeOperationsError(w, err, "account group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listMonitorRules(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	enabledOnly := r.URL.Query().Get("enabled") == "true"
	items, err := a.operations.ListRules(r.Context(), enabledOnly)
	if err != nil {
		a.logger.Error("list monitor rules", "error", err)
		writeError(w, http.StatusInternalServerError, "monitor_rules_failed", "无法读取监控规则")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) listNotificationChannels(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	items, err := a.operations.ListNotificationChannels(r.Context())
	if err != nil {
		a.logger.Error("list notification channels", "error", err)
		writeError(w, http.StatusInternalServerError, "notification_channels_failed",
			"无法读取通知渠道")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) createNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	var input struct {
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if input.Kind != "" && input.Kind != operations.NotificationKindWebhook {
		writeError(w, http.StatusUnprocessableEntity, "invalid_notification_channel",
			"当前仅支持创建 Webhook 通知渠道")
		return
	}
	item, err := a.operations.CreateWebhookChannel(
		r.Context(),
		input.Name,
		input.URL,
		input.Secret,
	)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_notification_channel",
			err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) deleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.operations.DeleteNotificationChannel(r.Context(), id); err != nil {
		a.writeOperationsError(w, err, "notification channel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) createMonitorRule(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	var input struct {
		Name            string   `json:"name"`
		AccountID       int64    `json:"account_id"`
		PeerType        string   `json:"peer_type"`
		PeerID          int64    `json:"peer_id"`
		PeerTitle       string   `json:"peer_title"`
		ChannelID       int64    `json:"notification_channel_id"`
		Keywords        []string `json:"keywords"`
		ExcludeKeywords []string `json:"exclude_keywords"`
		Cooldown        int      `json:"cooldown_seconds"`
		Owner           string   `json:"owner"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	item, err := a.operations.CreateRuleWithInput(r.Context(), operations.RuleCreateInput{
		Name:                  input.Name,
		AccountID:             input.AccountID,
		PeerType:              input.PeerType,
		PeerID:                input.PeerID,
		PeerTitle:             input.PeerTitle,
		NotificationChannelID: input.ChannelID,
		Keywords:              input.Keywords,
		ExcludeKeywords:       input.ExcludeKeywords,
		CooldownSeconds:       input.Cooldown,
		Owner:                 input.Owner,
		Actor:                 currentActor(r),
	})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_monitor_rule", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updateMonitorRule(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Name            *string   `json:"name"`
		AccountID       *int64    `json:"account_id"`
		PeerType        *string   `json:"peer_type"`
		PeerID          *int64    `json:"peer_id"`
		PeerTitle       *string   `json:"peer_title"`
		ChannelID       *int64    `json:"notification_channel_id"`
		Keywords        *[]string `json:"keywords"`
		ExcludeKeywords *[]string `json:"exclude_keywords"`
		Cooldown        *int      `json:"cooldown_seconds"`
		Owner           *string   `json:"owner"`
		Enabled         *bool     `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if input.Name == nil && input.AccountID == nil && input.PeerType == nil &&
		input.PeerID == nil && input.PeerTitle == nil && input.ChannelID == nil && input.Keywords == nil &&
		input.ExcludeKeywords == nil && input.Cooldown == nil && input.Owner == nil &&
		input.Enabled == nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_monitor_rule",
			"至少需要修改一个规则字段")
		return
	}
	item, err := a.operations.UpdateRule(r.Context(), id, operations.RuleUpdateInput{
		Name:                  input.Name,
		AccountID:             input.AccountID,
		PeerType:              input.PeerType,
		PeerID:                input.PeerID,
		PeerTitle:             input.PeerTitle,
		NotificationChannelID: input.ChannelID,
		Keywords:              input.Keywords,
		ExcludeKeywords:       input.ExcludeKeywords,
		CooldownSeconds:       input.Cooldown,
		Owner:                 input.Owner,
		Enabled:               input.Enabled,
		Actor:                 currentActor(r),
	})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_monitor_rule", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) previewMonitorRule(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	var input struct {
		AccountID       int64    `json:"account_id"`
		PeerType        string   `json:"peer_type"`
		PeerID          int64    `json:"peer_id"`
		Keywords        []string `json:"keywords"`
		ExcludeKeywords []string `json:"exclude_keywords"`
		Limit           int      `json:"limit"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	preview, err := a.operations.PreviewRule(r.Context(), operations.RulePreviewInput{
		AccountID:       input.AccountID,
		PeerType:        input.PeerType,
		PeerID:          input.PeerID,
		Keywords:        input.Keywords,
		ExcludeKeywords: input.ExcludeKeywords,
		Limit:           input.Limit,
	})
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_monitor_rule", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (a *API) listMonitorRuleVersions(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	items, err := a.operations.ListRuleVersions(r.Context(), id)
	if err != nil {
		a.writeOperationsError(w, err, "monitor rule")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) deleteMonitorRule(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.operations.DeleteRule(r.Context(), id); err != nil {
		a.writeOperationsError(w, err, "monitor rule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) runMonitor(w http.ResponseWriter, _ *http.Request) {
	if a.monitor == nil {
		writeError(w, http.StatusServiceUnavailable, "monitor_unavailable", "监控调度器尚未初始化")
		return
	}
	if a.monitor.Status().Running {
		writeError(w, http.StatusConflict, "monitor_running", "监控任务正在运行，请稍后再试")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := a.monitor.RunOnce(ctx); err != nil {
			a.logger.Warn("manual monitor run failed", "error", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, a.monitor.Status())
}

func (a *API) monitorStatus(w http.ResponseWriter, r *http.Request) {
	if a.monitor == nil {
		writeError(w, http.StatusServiceUnavailable, "monitor_unavailable", "监控调度器尚未初始化")
		return
	}
	if !a.prepareOperations(w) {
		return
	}
	summary, err := a.operations.MonitorSummary(r.Context())
	if err != nil {
		a.logger.Error("read monitor collection health", "error", err)
		writeError(w, http.StatusInternalServerError, "monitor_status_failed",
			"无法读取监控采集状态")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		monitorscheduler.Status
		Collection operations.MonitorSummary `json:"collection"`
	}{
		Status:     a.monitor.Status(),
		Collection: summary,
	})
}

func (a *API) listMonitorTargets(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	items, err := a.operations.ListCollectionTargets(r.Context())
	if err != nil {
		a.logger.Error("list monitor collection targets", "error", err)
		writeError(w, http.StatusInternalServerError, "monitor_targets_failed",
			"无法读取采集目标状态")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) listMonitorAlerts(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	limit, ok := parseLimit(w, r, 50)
	if !ok {
		return
	}
	items, err := a.operations.ListAlerts(r.Context(), r.URL.Query().Get("state"), limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_alert_query", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) listMonitorAlertEvents(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, r, 100)
	if !ok {
		return
	}
	items, err := a.operations.ListAlertEvents(r.Context(), id, limit)
	if errors.Is(err, operations.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "告警记录不存在")
		return
	}
	if err != nil {
		a.logger.Error("list monitor alert events", "alert_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "alert_history_failed",
			"无法读取告警历史")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) resolveMonitorAlert(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, err := a.operations.UpdateAlert(r.Context(), id, operations.AlertUpdateInput{
		Action: "resolve",
		Actor:  currentActor(r),
	}); err != nil {
		a.writeAlertError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) updateMonitorAlert(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input operations.AlertUpdateInput
	if !decodeJSONBody(w, r, &input) {
		return
	}
	input.Actor = currentActor(r)
	item, err := a.operations.UpdateAlert(r.Context(), id, input)
	if err != nil {
		a.writeAlertError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) writeAlertError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, operations.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "告警记录不存在")
	case errors.Is(err, operations.ErrAlertConflict):
		writeError(w, http.StatusConflict, "alert_conflict", err.Error())
	case errors.Is(err, operations.ErrAlertInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_alert_update", err.Error())
	default:
		a.logger.Error("monitor alert update failed", "error", err)
		writeError(w, http.StatusInternalServerError, "alert_update_failed", "告警更新失败")
	}
}

func (a *API) searchMessages(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	filter, err := parseMessageQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_message_query", err.Error())
		return
	}
	page, err := a.operations.SearchMessages(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusBadRequest, "message_search_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (a *API) correlationAnalysis(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	query, err := parseCorrelationQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_correlation_query", err.Error())
		return
	}
	analysis, err := a.operations.AnalyzeCorrelations(r.Context(), query)
	if err != nil {
		if errors.Is(err, operations.ErrInvalidCorrelationQuery) {
			writeError(w, http.StatusBadRequest, "invalid_correlation_query", err.Error())
			return
		}
		a.logger.Error("analyze cross-account correlations", "error", err)
		writeError(w, http.StatusInternalServerError, "correlation_analysis_failed",
			"关联分析暂时不可用")
		return
	}
	writeJSON(w, http.StatusOK, analysis)
}

func (a *API) searchIndexHealth(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	health, err := a.operations.SearchIndexHealth(r.Context())
	if err != nil {
		a.logger.Error("inspect message search index", "error", err)
		writeError(w, http.StatusInternalServerError, "search_index_unavailable",
			"搜索索引状态暂时不可用")
		return
	}
	writeJSON(w, http.StatusOK, health)
}

func (a *API) rebuildSearchIndex(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	if !prepareJSONMutation(w, r) {
		return
	}
	health, err := a.operations.RebuildSearchIndex(r.Context())
	if err != nil {
		a.logger.Error("rebuild message search index", "error", err)
		writeError(w, http.StatusInternalServerError, "search_index_rebuild_failed",
			"搜索索引重建失败")
		return
	}
	writeJSON(w, http.StatusOK, health)
}

func (a *API) cleanupMessages(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	var input struct {
		RetentionDays int  `json:"retention_days"`
		DryRun        bool `json:"dry_run"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	result, err := a.operations.CleanupMessages(
		r.Context(),
		operations.RetentionCleanupInput{
			RetentionDays: input.RetentionDays,
			DryRun:        input.DryRun,
			Actor:         currentActor(r),
		},
	)
	if err != nil {
		a.writeRetentionError(w, err, "消息留存清理失败")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) getRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	policy, err := a.operations.GetRetentionPolicy(r.Context())
	if err != nil {
		a.writeRetentionError(w, err, "无法读取消息留存策略")
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (a *API) updateRetentionPolicy(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	var input struct {
		RetentionDays int `json:"retention_days"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	policy, err := a.operations.SetRetentionPolicy(
		r.Context(),
		input.RetentionDays,
		currentActor(r),
	)
	if err != nil {
		a.writeRetentionError(w, err, "消息留存策略更新失败")
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (a *API) listLegalHolds(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	items, err := a.operations.ListLegalHolds(r.Context())
	if err != nil {
		a.writeRetentionError(w, err, "无法读取法律保留记录")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) createLegalHold(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	var input struct {
		Scope     string `json:"scope"`
		AccountID int64  `json:"account_id"`
		PeerType  string `json:"peer_type"`
		PeerID    int64  `json:"peer_id"`
		MessageID int    `json:"message_id"`
		Reason    string `json:"reason"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	hold, err := a.operations.CreateLegalHold(r.Context(), operations.LegalHoldInput{
		Scope:     input.Scope,
		AccountID: input.AccountID,
		PeerType:  input.PeerType,
		PeerID:    input.PeerID,
		MessageID: input.MessageID,
		Reason:    input.Reason,
		CreatedBy: currentActor(r),
	})
	if err != nil {
		a.writeRetentionError(w, err, "法律保留创建失败")
		return
	}
	writeJSON(w, http.StatusCreated, hold)
}

func (a *API) releaseLegalHold(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	hold, err := a.operations.ReleaseLegalHold(r.Context(), id, currentActor(r))
	if err != nil {
		a.writeRetentionError(w, err, "法律保留解除失败")
		return
	}
	writeJSON(w, http.StatusOK, hold)
}

func (a *API) writeRetentionError(w http.ResponseWriter, err error, message string) {
	switch {
	case errors.Is(err, operations.ErrInvalidRetention):
		writeError(w, http.StatusUnprocessableEntity, "invalid_retention_request", err.Error())
	case errors.Is(err, operations.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "法律保留记录不存在")
	default:
		a.logger.Error("retention operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "retention_failed", message)
	}
}

func (a *API) messageReport(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	report, err := a.operations.BuildReport(r.Context(), days)
	if err != nil {
		a.logger.Error("build message report", "error", err)
		writeError(w, http.StatusInternalServerError, "report_failed", "无法生成消息报表")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (a *API) evidenceManifest(w http.ResponseWriter, r *http.Request) {
	if !a.prepareOperations(w) {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 {
		days = 7
	}
	if days > 3650 {
		writeError(w, http.StatusBadRequest, "invalid_manifest_range",
			"清单时间范围不能超过 3650 天")
		return
	}
	accountID := int64(0)
	if raw := r.URL.Query().Get("account_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_account_id",
				"account_id 必须是正整数")
			return
		}
		accountID = parsed
	}
	manifest, err := a.operations.BuildEvidenceManifest(
		r.Context(),
		operations.EvidenceManifestFilter{
			Days:        days,
			AccountID:   accountID,
			GeneratedBy: currentActor(r),
		},
	)
	if err != nil {
		a.logger.Error("build evidence manifest", "error", err)
		writeError(w, http.StatusInternalServerError, "manifest_failed",
			"无法生成证据完整性摘要")
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (a *API) prepareOperations(w http.ResponseWriter) bool {
	if a.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "operations_unavailable", "业务数据服务尚未初始化")
		return false
	}
	return true
}

func (a *API) writeOperationsError(w http.ResponseWriter, err error, subject string) {
	if errors.Is(err, operations.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", subject+" was not found")
		return
	}
	if errors.Is(err, operations.ErrProtectedChannel) {
		writeError(w, http.StatusUnprocessableEntity, "protected_channel",
			"内置通知渠道不可删除")
		return
	}
	if errors.Is(err, operations.ErrNotificationChannelUse) {
		writeError(w, http.StatusConflict, "notification_channel_in_use",
			"通知渠道仍被监控规则引用")
		return
	}
	a.logger.Error("operations request failed", "subject", subject, "error", err)
	writeError(w, http.StatusInternalServerError, "operations_failed", subject+" operation failed")
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "ID must be a positive integer")
		return 0, false
	}
	return id, true
}

func currentActor(r *http.Request) string {
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		return anonymousAuditActor
	}
	return principal.Actor
}

func parseMessageQuery(r *http.Request) (operations.MessageQuery, error) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil && r.URL.Query().Get("limit") != "" {
		return operations.MessageQuery{}, errors.New("limit must be an integer")
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil && r.URL.Query().Get("offset") != "" {
		return operations.MessageQuery{}, errors.New("offset must be an integer")
	}
	var accountID int64
	if value := r.URL.Query().Get("account_id"); value != "" {
		accountID, err = strconv.ParseInt(value, 10, 64)
		if err != nil || accountID <= 0 {
			return operations.MessageQuery{}, errors.New("account_id must be a positive integer")
		}
	}
	from, err := parseOptionalQueryTime(r.URL.Query().Get("from"))
	if err != nil {
		return operations.MessageQuery{}, errors.New("from must be an RFC3339 timestamp")
	}
	to, err := parseOptionalQueryTime(r.URL.Query().Get("to"))
	if err != nil {
		return operations.MessageQuery{}, errors.New("to must be an RFC3339 timestamp")
	}
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return operations.MessageQuery{}, errors.New("from must be before to")
	}
	return operations.MessageQuery{
		Query:     r.URL.Query().Get("q"),
		AccountID: accountID,
		PeerType:  r.URL.Query().Get("peer_type"),
		From:      from,
		To:        to,
		Limit:     limit,
		Offset:    offset,
	}, nil
}

func parseCorrelationQuery(r *http.Request) (operations.CorrelationQuery, error) {
	values := r.URL.Query()
	days, err := strconv.Atoi(values.Get("days"))
	if err != nil && values.Get("days") != "" {
		return operations.CorrelationQuery{}, errors.New("days must be an integer")
	}
	if days < 0 {
		return operations.CorrelationQuery{}, errors.New("days must not be negative")
	}
	limit, err := strconv.Atoi(values.Get("limit"))
	if err != nil && values.Get("limit") != "" {
		return operations.CorrelationQuery{}, errors.New("limit must be an integer")
	}
	if limit < 0 {
		return operations.CorrelationQuery{}, errors.New("limit must not be negative")
	}
	return operations.CorrelationQuery{
		Days:  days,
		Types: append([]string(nil), values["types"]...),
		Limit: limit,
	}, nil
}

func parseOptionalQueryTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}

type loginAttempt struct {
	windowStart time.Time
	count       int
}

type authSessionResponse struct {
	Configured    bool   `json:"configured"`
	Authenticated bool   `json:"authenticated"`
	Username      string `json:"username,omitempty"`
	CSRFToken     string `json:"csrf_token,omitempty"`
}

type accountRefreshResult struct {
	item account.Account
	err  error
}

type accountRefreshOutcome struct {
	AccountID int64  `json:"account_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	Deferred  bool   `json:"deferred,omitempty"`
}

type accountRefreshWarning struct {
	AccountID int64  `json:"account_id"`
	Message   string `json:"message"`
}

const (
	anonymousAuditActor = "anonymous"
	anonymousAuditRole  = "anonymous"
)

func (a *API) authSession(w http.ResponseWriter, r *http.Request) {
	if a.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "管理员鉴权尚未初始化")
		return
	}
	response := authSessionResponse{Configured: a.auth.Configured()}
	token, ok := sessionCookie(r)
	if ok {
		session, err := a.auth.Lookup(token)
		if err == nil {
			response.Authenticated = true
			response.Username = a.auth.Username()
			response.CSRFToken = session.CSRF
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) setupAdministrator(w http.ResponseWriter, r *http.Request) {
	w = responseWriter(w)
	principal := auth.Principal{Actor: anonymousAuditActor, Role: anonymousAuditRole}
	defer func() {
		a.recordAudit(r, responseWriter(w).StatusCode(), principal)
	}()
	defer a.recoverRequest(w, r)
	if a.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "管理员鉴权尚未初始化")
		return
	}
	if !isLoopbackRequest(r) {
		writeError(w, http.StatusForbidden, "setup_local_only", "管理员初始化仅允许从本机完成")
		return
	}
	if a.auth.Configured() {
		writeError(w, http.StatusConflict, "already_configured", "管理员账号已经初始化")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if err := a.auth.Setup(input.Username, input.Password); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_admin_credentials", err.Error())
		return
	}
	principal = auth.Principal{Actor: a.auth.Username(), Role: auth.RoleAdministrator}
	if err := a.startAdministratorSession(w, r); err != nil {
		a.logger.Error("create initial administrator session", "error", err)
		writeError(w, http.StatusInternalServerError, "session_failed", "管理员已创建，请重新登录")
	}
}

func (a *API) loginAdministrator(w http.ResponseWriter, r *http.Request) {
	w = responseWriter(w)
	principal := auth.Principal{Actor: anonymousAuditActor, Role: anonymousAuditRole}
	defer func() {
		a.recordAudit(r, responseWriter(w).StatusCode(), principal)
	}()
	defer a.recoverRequest(w, r)
	if a.auth == nil {
		writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "管理员鉴权尚未初始化")
		return
	}
	key := clientAddress(r)
	if retryAfter := a.reserveLoginAttempt(key); retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
		writeError(w, http.StatusTooManyRequests, "login_rate_limited", "登录尝试过于频繁，请稍后再试")
		return
	}
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if len(input.Username) > 64 || len(input.Password) > 128 {
		writeError(w, http.StatusUnauthorized, "invalid_login", "管理员账号或密码不正确")
		return
	}
	if err := a.auth.Login(input.Username, input.Password); err != nil {
		if errors.Is(err, auth.ErrNotConfigured) {
			writeError(w, http.StatusConflict, "setup_required", "请先初始化管理员账号")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_login", "管理员账号或密码不正确")
		return
	}
	principal = auth.Principal{Actor: a.auth.Username(), Role: auth.RoleAdministrator}
	a.clearLoginAttempts(key)
	if err := a.startAdministratorSession(w, r); err != nil {
		a.logger.Error("create administrator session", "error", err)
		writeError(w, http.StatusInternalServerError, "session_failed", "无法创建管理员会话")
	}
}

func (a *API) logoutAdministrator(w http.ResponseWriter, r *http.Request) {
	token, _ := sessionCookie(r)
	if a.auth != nil {
		a.auth.Delete(token)
	}
	a.clearAuthCookies(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) startAdministratorSession(w http.ResponseWriter, r *http.Request) error {
	token, session, err := a.auth.CreateSession()
	if err != nil {
		return err
	}
	secure := requestIsHTTPS(r)
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(time.Until(session.ExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CSRFCookie,
		Value:    session.CSRF,
		Path:     "/",
		MaxAge:   int(time.Until(session.ExpiresAt).Seconds()),
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, authSessionResponse{
		Configured:    true,
		Authenticated: true,
		Username:      a.auth.Username(),
		CSRFToken:     session.CSRF,
	})
	return nil
}

func (a *API) requireAPIPermission(
	permission string,
	next http.HandlerFunc,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w = responseWriter(w)
		principal := auth.Principal{Actor: anonymousAuditActor, Role: anonymousAuditRole}
		defer func() {
			a.recordAudit(r, responseWriter(w).StatusCode(), principal)
		}()
		defer a.recoverRequest(w, r)
		session, ok := a.authorizedSession(w, r, false)
		if !ok {
			return
		}
		if !auth.CanAccess(session.Role, permission) {
			writeError(w, http.StatusForbidden, "permission_denied", "当前操作者没有访问权限")
			return
		}
		principal = auth.Principal{Actor: session.Actor, Role: session.Role}
		request := r.WithContext(auth.WithPrincipal(r.Context(), principal))
		if isUnsafeMethod(r.Method) && r.Header.Get(auth.CSRFHeader) != session.CSRF {
			writeError(w, http.StatusForbidden, "invalid_csrf", "请求校验失败，请刷新页面后重试")
			return
		}
		next(w, request)
	}
}

func (a *API) requirePage(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := a.authorizedSession(w, r, true); !ok {
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) authorizedSession(
	w http.ResponseWriter,
	r *http.Request,
	redirect bool,
) (auth.Session, bool) {
	reject := func() bool {
		if redirect {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		} else {
			writeError(w, http.StatusUnauthorized, "authentication_required", "请先登录管理台")
		}
		return false
	}
	if a.auth == nil {
		if redirect {
			http.Error(w, "管理员鉴权尚未初始化", http.StatusServiceUnavailable)
		} else {
			writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "管理员鉴权尚未初始化")
		}
		return auth.Session{}, false
	}
	if !a.auth.Configured() {
		if redirect {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		} else {
			writeError(w, http.StatusServiceUnavailable, "setup_required", "请先初始化管理员账号")
		}
		return auth.Session{}, false
	}
	token, ok := sessionCookie(r)
	if !ok {
		reject()
		return auth.Session{}, false
	}
	session, err := a.auth.Lookup(token)
	if err != nil {
		a.clearAuthCookies(w, r)
		reject()
		return auth.Session{}, false
	}
	return session, true
}

func sessionCookie(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(auth.CookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return "", false
	}
	return cookie.Value, true
}

func (a *API) clearAuthCookies(w http.ResponseWriter, r *http.Request) {
	secure := requestIsHTTPS(r)
	for _, name := range []string{auth.CookieName, auth.CSRFCookie} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: name == auth.CookieName,
			Secure:   secure,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func isUnsafeMethod(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func isLoopbackRequest(r *http.Request) bool {
	host := clientAddress(r)
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a *API) reserveLoginAttempt(key string) time.Duration {
	const (
		window = time.Minute
		limit  = 5
	)
	now := time.Now()
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	if a.attempts == nil {
		a.attempts = make(map[string]loginAttempt)
	}
	attempt := a.attempts[key]
	if attempt.windowStart.IsZero() || now.Sub(attempt.windowStart) >= window {
		attempt = loginAttempt{windowStart: now}
	}
	attempt.count++
	a.attempts[key] = attempt
	if attempt.count <= limit {
		return 0
	}
	return window - now.Sub(attempt.windowStart)
}

func (a *API) clearLoginAttempts(key string) {
	a.loginMu.Lock()
	delete(a.attempts, key)
	a.loginMu.Unlock()
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

func (a *API) updateAccount(w http.ResponseWriter, r *http.Request) {
	if !prepareJSONMutation(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "account ID must be a positive integer")
		return
	}
	var input account.UpdateInput
	if !decodeJSONBody(w, r, &input) {
		return
	}
	item, err := a.accounts.Update(r.Context(), id, input)
	if errors.Is(err, account.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account was not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_account", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if !prepareJSONMutation(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "account ID must be a positive integer")
		return
	}
	item, err := a.accounts.ScheduleDeletion(r.Context(), id)
	if errors.Is(err, account.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account was not found")
		return
	}
	if errors.Is(err, account.ErrDeletionInProgress) {
		writeError(w, http.StatusConflict, "account_delete_in_progress",
			"账号已进入不可恢复的清理阶段")
		return
	}
	if err != nil {
		a.logger.Error("schedule account deletion", "account_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error",
			"could not schedule account deletion")
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (a *API) restoreAccount(w http.ResponseWriter, r *http.Request) {
	if !prepareJSONMutation(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "account ID must be a positive integer")
		return
	}
	item, err := a.accounts.Restore(r.Context(), id)
	if errors.Is(err, account.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account was not found")
		return
	}
	if errors.Is(err, account.ErrDeletionInProgress) {
		writeError(w, http.StatusConflict, "account_delete_in_progress",
			"账号已进入清理阶段，不能恢复；请稍后确认销毁结果")
		return
	}
	if err != nil {
		a.logger.Error("restore account", "account_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error",
			"could not restore account")
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
	semaphore := make(chan struct{}, telegramAuthConcurrency)
	results := make([]accountRefreshResult, len(items))
	for i := range items {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			results[index].item, results[index].err = a.refreshAccountStatus(r.Context(), items[index])
		}(i)
	}
	wait.Wait()
	warnings := make([]accountRefreshWarning, 0)
	outcomes := make([]accountRefreshOutcome, 0, len(items))
	for i, result := range results {
		items[i] = result.item
		if result.err == nil {
			outcomes = append(outcomes, accountRefreshOutcome{
				AccountID: items[i].ID,
				Status:    items[i].Status,
				Reason:    items[i].StatusReason,
			})
			continue
		}
		if errors.Is(result.err, errAccountValidationDeferred) {
			warnings = append(warnings, accountRefreshWarning{
				AccountID: items[i].ID,
				Message:   "Telegram 暂时不可达或请求受限，已保留原状态，请稍后重试",
			})
			outcomes = append(outcomes, accountRefreshOutcome{
				AccountID: items[i].ID,
				Status:    items[i].Status,
				Reason:    items[i].StatusReason,
				Deferred:  true,
			})
			continue
		}
		a.logger.Error(
			"persist refreshed Telegram account status",
			"account_id",
			items[i].ID,
			"error",
			result.err,
		)
		writeError(w, http.StatusInternalServerError, "account_refresh_incomplete",
			"账号状态校验已完成，但部分结果未能保存，请重试")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":       items,
		"outcomes":    outcomes,
		"warnings":    warnings,
		"verified_at": time.Now().UTC(),
		"limit":       len(items),
		"offset":      0,
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
	updated, err := a.refreshAccountStatus(r.Context(), item)
	if err != nil {
		if errors.Is(err, errAccountValidationDeferred) {
			writeJSON(w, http.StatusOK, map[string]any{
				"item": updated,
				"outcomes": []accountRefreshOutcome{{
					AccountID: updated.ID,
					Status:    updated.Status,
					Reason:    updated.StatusReason,
					Deferred:  true,
				}},
				"warnings": []accountRefreshWarning{{
					AccountID: updated.ID,
					Message:   "Telegram 暂时不可达或请求受限，已保留原状态，请稍后重试",
				}},
				"verified_at": time.Now().UTC(),
			})
			return
		}
		a.logger.Error("persist refreshed Telegram account status", "account_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "account_refresh_failed",
			"账号状态校验已完成，但结果未能保存，请重试")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

type accountProxyInput struct {
	ProxyURL string `json:"proxy_url"`
}

func (a *API) setAccountProxy(w http.ResponseWriter, r *http.Request) {
	if !prepareJSONMutation(w, r) {
		return
	}
	if a.proxies == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_store_unavailable",
			"账号代理存储不可用")
		return
	}
	id, ok := accountIDPath(w, r)
	if !ok {
		return
	}
	if _, err := a.accounts.Get(r.Context(), id); err != nil {
		a.writeAccountReadError(w, id, err)
		return
	}
	var input accountProxyInput
	if !decodeJSONBody(w, r, &input) {
		return
	}
	profile, err := accountproxy.Parse(input.ProxyURL)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_proxy", err.Error())
		return
	}
	previous, hadPrevious := a.proxies.Get(id)
	if err := a.proxies.Set(r.Context(), id, profile); err != nil {
		a.logger.Error("save account proxy credentials", "account_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "proxy_save_failed",
			"代理凭据保存失败")
		return
	}
	item, err := a.accounts.SetProxy(r.Context(), id, account.Proxy{
		Enabled:  true,
		Host:     profile.Host,
		Port:     profile.Port,
		Username: profile.Username,
	})
	if err != nil {
		a.rollbackProxy(r.Context(), id, previous, hadPrevious)
		a.writeAccountProxyError(w, id, err)
		return
	}
	a.resetAccountConnection(id)
	writeJSON(w, http.StatusOK, item)
}

func (a *API) clearAccountProxy(w http.ResponseWriter, r *http.Request) {
	if !prepareJSONMutation(w, r) {
		return
	}
	if a.proxies == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_store_unavailable",
			"账号代理存储不可用")
		return
	}
	id, ok := accountIDPath(w, r)
	if !ok {
		return
	}
	if _, err := a.accounts.Get(r.Context(), id); err != nil {
		a.writeAccountReadError(w, id, err)
		return
	}
	if _, err := a.proxies.Delete(r.Context(), id); err != nil {
		a.logger.Error("delete account proxy credentials", "account_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "proxy_delete_failed",
			"代理凭据删除失败，账号代理设置未变更")
		return
	}
	item, err := a.accounts.ClearProxy(r.Context(), id)
	if err != nil {
		a.writeAccountProxyError(w, id, err)
		return
	}
	a.resetAccountConnection(id)
	writeJSON(w, http.StatusOK, item)
}

func (a *API) checkAccountProxy(w http.ResponseWriter, r *http.Request) {
	if !prepareJSONMutation(w, r) {
		return
	}
	if a.proxies == nil {
		writeError(w, http.StatusServiceUnavailable, "proxy_store_unavailable",
			"账号代理存储不可用")
		return
	}
	id, ok := accountIDPath(w, r)
	if !ok {
		return
	}
	if _, err := a.accounts.Get(r.Context(), id); err != nil {
		a.writeAccountReadError(w, id, err)
		return
	}
	profile, found := a.proxies.Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "proxy_not_found",
			"该账号尚未配置代理")
		return
	}
	checkErr := a.proxyCheck(r.Context(), profile)
	now := time.Now().UTC()
	proxy := account.Proxy{
		Enabled:     true,
		Host:        profile.Host,
		Port:        profile.Port,
		Username:    profile.Username,
		LastChecked: &now,
	}
	if checkErr != nil {
		proxy.LastError = "代理连接失败"
		a.logger.Warn("account proxy connectivity check failed",
			"account_id", id, "error_type", fmt.Sprintf("%T", checkErr))
	}
	_, err := a.accounts.SetProxy(r.Context(), id, proxy)
	if err != nil {
		a.writeAccountProxyError(w, id, err)
		return
	}
	status := http.StatusOK
	if checkErr != nil {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, map[string]any{
		"ok": checkErr == nil,
		"message": map[bool]string{
			true:  "代理连接正常",
			false: "代理暂时无法连接 Telegram，账号在线状态未变更",
		}[checkErr == nil],
	})
}

func (a *API) rollbackProxy(
	ctx context.Context,
	id int64,
	previous accountproxy.Profile,
	hadPrevious bool,
) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	if hadPrevious {
		err = a.proxies.Set(rollbackCtx, id, previous)
	} else {
		_, err = a.proxies.Delete(rollbackCtx, id)
	}
	if err != nil {
		a.logger.Error("rollback account proxy credentials", "account_id", id, "error", err)
	}
}

func (a *API) resetAccountConnection(id int64) {
	if a.chats != nil {
		a.chats.ResetAccount(id)
	}
}

func (a *API) writeAccountProxyError(w http.ResponseWriter, id int64, err error) {
	if errors.Is(err, account.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account was not found")
		return
	}
	a.logger.Error("update account proxy metadata", "account_id", id, "error", err)
	writeError(w, http.StatusInternalServerError, "proxy_update_failed",
		"代理设置保存失败")
}

func (a *API) writeAccountReadError(w http.ResponseWriter, id int64, err error) {
	if errors.Is(err, account.ErrNotFound) {
		writeError(w, http.StatusNotFound, "account_not_found", "account was not found")
		return
	}
	a.logger.Error("read account for proxy update", "account_id", id, "error", err)
	writeError(w, http.StatusInternalServerError, "internal_error",
		"could not load account")
}

func accountIDPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id",
			"account ID must be a positive integer")
		return 0, false
	}
	return id, true
}

func (a *API) refreshAccountStatus(
	ctx context.Context,
	item account.Account,
) (account.Account, error) {
	if item.Status != account.StatusOnline && item.Status != account.StatusError {
		return item, nil
	}
	validateCtx, validateCancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		accountValidationTimeout,
	)
	profile, err := a.validateAccountProfile(validateCtx, item.ID)
	validateCancel()
	if err != nil {
		if !errors.Is(err, telegramchat.ErrSessionMissing) &&
			!errors.Is(err, telegramchat.ErrSessionInvalid) {
			a.logger.Warn("Telegram account validation deferred",
				"account_id", item.ID, "error", err)
			return item, fmt.Errorf("%w: %v", errAccountValidationDeferred, err)
		}
		a.logger.Warn("validate Telegram account session",
			"account_id", item.ID, "error", err)
		reason := "授权会话不存在，请重新登录"
		if errors.Is(err, telegramchat.ErrSessionInvalid) {
			reason = "Telegram 授权已失效，请重新登录"
		}
		updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		updated, updateErr := a.accounts.RecordVerification(
			updateCtx,
			item.ID,
			account.StatusError,
			reason,
		)
		if updateErr != nil {
			return item, fmt.Errorf("record account %d verification: %w", item.ID, updateErr)
		}
		return updated, nil
	}
	if profile.ID != item.ID {
		reason := "Telegram 授权账号与本地账号不一致，请重新登录"
		updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		updated, err := a.accounts.RecordVerification(
			updateCtx,
			item.ID,
			account.StatusError,
			reason,
		)
		if err != nil {
			return item, fmt.Errorf("record account %d mismatch: %w", item.ID, err)
		}
		return updated, nil
	}
	updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	updated, err := a.accounts.UpsertAuthorized(
		updateCtx,
		account.CreateInput{
			ID:          profile.ID,
			PhoneNumber: profile.PhoneNumber,
			Username:    profile.Username,
			DisplayName: profile.DisplayName,
		},
	)
	if err != nil {
		return item, fmt.Errorf("record account %d verification: %w", item.ID, err)
	}
	return updated, nil
}

func (a *API) validateAccountProfile(
	ctx context.Context,
	accountID int64,
) (telegramchat.Profile, error) {
	if a.chats == nil {
		return telegramchat.Profile{}, telegramchat.ErrNotConfigured
	}
	return a.chats.InspectSession(ctx, accountID)
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
	a.indexMessages(r.Context(), accountID, peerType, peerID, page.Items)
	writeJSON(w, http.StatusOK, page)
}

func (a *API) indexMessages(
	ctx context.Context,
	accountID int64,
	peerType string,
	peerID int64,
	items []telegramchat.Message,
) {
	if a.operations == nil || len(items) == 0 {
		return
	}
	indexCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
	defer cancel()
	records := make([]operations.MessageRecord, 0, len(items))
	for _, item := range items {
		media := operations.MediaRecord{}
		if item.Media != nil {
			media = operations.MediaRecord{
				Type:          item.Media.Type,
				Kind:          item.Media.Kind,
				ID:            item.Media.ID,
				DCID:          item.Media.DCID,
				MimeType:      item.Media.MimeType,
				FileName:      item.Media.FileName,
				Size:          item.Media.Size,
				Width:         item.Media.Width,
				Height:        item.Media.Height,
				Spoiler:       item.Media.Spoiler,
				Video:         item.Media.Video,
				Voice:         item.Media.Voice,
				Round:         item.Media.Round,
				AccessHash:    item.Media.AccessHash,
				FileReference: base64.StdEncoding.EncodeToString(item.Media.FileReference),
			}
		}
		records = append(records, operations.MessageRecord{
			AccountID: accountID,
			PeerType:  peerType,
			PeerID:    peerID,
			MessageID: item.ID,
			Sender:    item.Sender,
			Text:      item.Text,
			MediaType: item.MediaType,
			Media:     media,
			SentAt:    item.SentAt,
		})
	}
	if _, _, err := a.operations.IngestMessages(indexCtx, records); err != nil {
		a.logger.Warn("index Telegram messages", "account_id", accountID, "error", err)
	}
}

func (a *API) deleteChatMessages(w http.ResponseWriter, r *http.Request) {
	accountID, peerType, peerID, ok := a.authorizedChatPeer(w, r)
	if !ok {
		return
	}
	var input struct {
		MessageIDs []int  `json:"message_ids"`
		Scope      string `json:"scope"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if input.Scope != "me" && input.Scope != "all" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_delete_scope",
			"scope must be me or all")
		return
	}
	if err := a.chats.DeleteMessages(
		r.Context(),
		accountID,
		peerType,
		peerID,
		input.MessageIDs,
		input.Scope == "all",
	); err != nil {
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
		writeError(w, http.StatusConflict, "account_not_online", accountOfflineMessage(item))
		return 0, false
	}
	if !accountVerificationFresh(item) {
		item, err = a.refreshAccountStatus(r.Context(), item)
		if err != nil {
			if errors.Is(err, errAccountValidationDeferred) {
				writeError(w, http.StatusServiceUnavailable, "account_validation_deferred",
					"Telegram 暂时不可达或请求受限，请稍后重试")
				return 0, false
			}
			a.logger.Error("verify stale Telegram account status", "account_id", accountID, "error", err)
			writeError(w, http.StatusInternalServerError, "account_refresh_failed",
				"账号状态校验结果未能保存，请重试")
			return 0, false
		}
		if item.Status != account.StatusOnline {
			writeError(w, http.StatusConflict, "account_not_online", accountOfflineMessage(item))
			return 0, false
		}
	}
	return accountID, true
}

func accountVerificationFresh(item account.Account) bool {
	if item.LastVerifiedAt == nil {
		return false
	}
	return time.Since(item.LastVerifiedAt.UTC()) <= accountVerificationTTL
}

func accountOfflineMessage(item account.Account) string {
	if reason := strings.TrimSpace(item.StatusReason); reason != "" {
		return "该账号当前不可用：" + reason
	}
	return "该账号当前不可用，请重新校验状态或重新登录"
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
		if _, updateErr := a.accounts.RecordVerification(
			context.WithoutCancel(ctx),
			accountID,
			account.StatusError,
			"授权会话不存在，请重新登录",
		); updateErr != nil {
			a.logger.Error("persist missing Telegram session status",
				"account_id", accountID, "error", updateErr)
		}
		writeError(w, http.StatusNotFound, "session_not_found", "该账号的授权会话不存在，请重新登录")
	case errors.Is(err, telegramchat.ErrSessionInvalid):
		a.logger.Warn("Telegram session is no longer authorized", "account_id", accountID, "error", err)
		if _, updateErr := a.accounts.RecordVerification(
			context.WithoutCancel(ctx),
			accountID,
			account.StatusError,
			"Telegram 授权已失效，请重新登录",
		); updateErr != nil {
			a.logger.Error("persist invalid Telegram session status",
				"account_id", accountID, "error", updateErr)
		}
		writeError(w, http.StatusConflict, "session_invalid", "该账号的 Telegram 授权已失效，请重新登录后再查看会话")
	case errors.Is(err, telegramchat.ErrFloodWait):
		retryAfter := 30
		var floodWait *telegramchat.FloodWaitError
		if errors.As(err, &floodWait) && floodWait.Wait > 0 {
			retryAfter = max(int(floodWait.Wait.Seconds()), 1)
		}
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		writeError(w, http.StatusTooManyRequests, "telegram_flood_wait",
			"Telegram 请求过于频繁，请稍后再试")
	case errors.Is(err, telegramchat.ErrTimeout):
		writeError(w, http.StatusGatewayTimeout, "telegram_timeout",
			"Telegram 响应超时，请稍后重试")
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
		AccountID   int64  `json:"account_id"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if input.AccountID < 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_account_id",
			"account_id must be zero or a positive integer")
		return
	}
	release, ok := a.beginTelegramAuth(w, r)
	if !ok {
		return
	}
	defer release()
	challenge, err := a.telegram.SendCode(r.Context(), input.PhoneNumber, input.AccountID)
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
	if !prepareJSONMutation(w, r) {
		return
	}
	var input struct {
		AccountID int64 `json:"account_id"`
	}
	if !decodeJSONBody(w, r, &input) {
		return
	}
	if input.AccountID < 0 {
		writeError(w, http.StatusUnprocessableEntity, "invalid_account_id",
			"account_id must be zero or a positive integer")
		return
	}
	release, ok := a.beginTelegramAuth(w, r)
	if !ok {
		return
	}
	defer release()
	challenge, err := a.telegram.StartQR(r.Context(), input.AccountID)
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
	release, ok := a.beginTelegramAuth(w, r)
	if !ok {
		return
	}
	defer release()
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
	release, ok := a.beginTelegramAuth(w, r)
	if !ok {
		return
	}
	defer release()
	result, err := a.telegram.SubmitPassword(r.Context(), r.PathValue("loginID"), input.Password)
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
		if a.chats != nil {
			a.chats.ResetAccount(item.ID)
		}
		response.Account = &item
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *API) writeQRResult(w http.ResponseWriter, r *http.Request, result telegramauth.QRResult) {
	response := struct {
		NextStep  string           `json:"next_step"`
		Image     string           `json:"image,omitempty"`
		RefreshAt time.Time        `json:"refresh_at,omitempty"`
		Account   *account.Account `json:"account,omitempty"`
	}{
		NextStep:  result.NextStep,
		Image:     result.Image,
		RefreshAt: result.RefreshAt,
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
		if a.chats != nil {
			a.chats.ResetAccount(item.ID)
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
	case errors.Is(err, telegramauth.ErrAccountMismatch):
		writeError(w, http.StatusUnprocessableEntity, "telegram_account_mismatch",
			"登录的 Telegram 账号与正在恢复的账号不一致，请使用目标账号重新登录")
	case errors.Is(err, telegramauth.ErrLoginNotFound):
		writeError(w, http.StatusNotFound, "login_not_found", "登录会话不存在或已过期")
	case errors.Is(err, telegramauth.ErrLoginStage):
		writeError(w, http.StatusConflict, "invalid_login_step", "当前登录步骤不允许此操作")
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, "telegram_timeout",
			"Telegram 响应超时，请稍后重试")
	case errors.Is(err, telegramauth.ErrTelegram):
		a.logger.Warn("Telegram authentication failed", "error", err)
		writeError(w, http.StatusBadGateway, "telegram_error", "Telegram 登录失败，请稍后重试")
	default:
		a.logger.Error("telegram authentication error", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "登录服务暂时不可用")
	}
}

func (a *API) beginTelegramAuth(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if retryAfter := a.reserveTelegramAuthAttempt(clientAddress(r)); retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Seconds()))))
		writeError(w, http.StatusTooManyRequests, "telegram_auth_rate_limited",
			"Telegram 登录请求过于频繁，请稍后再试")
		return nil, false
	}
	select {
	case a.authSlots <- struct{}{}:
		return func() { <-a.authSlots }, true
	default:
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "telegram_auth_busy",
			"Telegram 登录请求较多，请稍后重试")
		return nil, false
	}
}

func (a *API) reserveTelegramAuthAttempt(key string) time.Duration {
	now := time.Now()
	a.authOpsMu.Lock()
	defer a.authOpsMu.Unlock()
	if a.authOps == nil {
		a.authOps = make(map[string]loginAttempt)
	}
	attempt := a.authOps[key]
	if attempt.windowStart.IsZero() || now.Sub(attempt.windowStart) >= telegramAuthWindow {
		attempt = loginAttempt{windowStart: now}
	}
	attempt.count++
	a.authOps[key] = attempt
	if attempt.count <= telegramAuthAttemptLimit {
		return 0
	}
	return telegramAuthWindow - now.Sub(attempt.windowStart)
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
	query := r.URL.Query()
	filter := account.Filter{
		Status:         query.Get("status"),
		Limit:          20,
		IncludeDeleted: query.Get("include_deleted") == "true",
	}
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
		requestID := newRequestID()
		w.Header().Set("X-Request-ID", requestID)
		r = r.WithContext(context.WithValue(r.Context(), requestIDContextKey{}, requestID))
		writer := responseWriter(w)
		started := time.Now()
		next.ServeHTTP(writer, r)
		a.logger.Info("http request",
			"request_id", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", writer.StatusCode(),
			"duration", time.Since(started),
		)
	})
}

type requestIDContextKey struct{}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
}

func responseWriter(w http.ResponseWriter) *statusResponseWriter {
	if writer, ok := w.(*statusResponseWriter); ok {
		return writer
	}
	return &statusResponseWriter{ResponseWriter: w}
}

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusResponseWriter) StatusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *statusResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func newRequestID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(value)
}

func requestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDContextKey{}).(string)
	return value
}

func (a *API) recordAudit(
	r *http.Request,
	statusCode int,
	principal auth.Principal,
) {
	if a.operations == nil || !shouldAuditRequest(r.Method, r.Pattern) {
		return
	}
	route := r.Pattern
	if index := strings.IndexByte(route, ' '); index >= 0 {
		route = strings.TrimSpace(route[index+1:])
	}
	if route == "" {
		route = r.URL.Path
	}
	resourceType, resourceID := auditResource(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Second)
	defer cancel()
	if err := a.operations.RecordAudit(ctx, operations.AuditEventInput{
		RequestID:    requestIDFromContext(r.Context()),
		Actor:        principal.Actor,
		Role:         principal.Role,
		Method:       r.Method,
		Route:        route,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		ClientIP:     clientAddress(r),
		StatusCode:   statusCode,
	}); err != nil {
		a.logger.Error("record operation audit",
			"request_id", requestIDFromContext(r.Context()),
			"actor", principal.Actor,
			"route", route,
			"error", err)
	}
}

func shouldAuditRequest(method, pattern string) bool {
	route := pattern
	if index := strings.IndexByte(route, ' '); index >= 0 {
		route = strings.TrimSpace(route[index+1:])
	}
	if route == "" || !strings.HasPrefix(route, "/api/v1/") {
		return false
	}
	switch route {
	case "/api/v1/health",
		"/api/v1/search/health",
		"/api/v1/telegram/auth/qr/{loginID}",
		"/api/v1/accounts/{id}/chats/{peerType}/{peerID}/avatar":
		return method != http.MethodGet
	default:
		return true
	}
}

func auditResource(r *http.Request) (string, string) {
	pattern := r.Pattern
	if index := strings.IndexByte(pattern, ' '); index >= 0 {
		pattern = strings.TrimSpace(pattern[index+1:])
	}
	if peerID := r.PathValue("peerID"); peerID != "" {
		return "chat", strings.Join([]string{
			r.PathValue("id"),
			r.PathValue("peerType"),
			peerID,
		}, "/")
	}
	if accountID := r.PathValue("id"); accountID != "" &&
		strings.HasPrefix(pattern, "/api/v1/accounts/") {
		return "account", accountID
	}
	if loginID := r.PathValue("loginID"); loginID != "" {
		return "telegram_login", loginID
	}
	if holdID := r.PathValue("id"); holdID != "" &&
		strings.Contains(pattern, "/retention/legal-holds/") {
		return "legal_hold", holdID
	}
	if ruleID := r.PathValue("id"); ruleID != "" &&
		strings.Contains(pattern, "/monitor/rules/") {
		return "monitor_rule", ruleID
	}
	if alertID := r.PathValue("id"); alertID != "" &&
		strings.Contains(pattern, "/monitor/alerts/") {
		return "monitor_alert", alertID
	}
	if groupID := r.PathValue("id"); groupID != "" &&
		strings.Contains(pattern, "/groups/") {
		return "account_group", groupID
	}
	return "api", ""
}

func (a *API) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer a.recoverRequest(w, r)
		next.ServeHTTP(w, r)
	})
}

func (a *API) recoverRequest(w http.ResponseWriter, r *http.Request) {
	if recovered := recover(); recovered != nil {
		a.logger.Error("panic serving request",
			"request_id", requestIDFromContext(r.Context()),
			"path", r.URL.Path,
			"panic", fmt.Sprint(recovered))
		writeError(w, http.StatusInternalServerError, "internal_error", "unexpected server error")
	}
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
