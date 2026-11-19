package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/accountpurge"
	"github.com/ThanatosXingYu/TeleNexux/internal/auth"
	"github.com/ThanatosXingYu/TeleNexux/internal/config"
	"github.com/ThanatosXingYu/TeleNexux/internal/health"
	"github.com/ThanatosXingYu/TeleNexux/internal/httpapi"
	"github.com/ThanatosXingYu/TeleNexux/internal/monitorscheduler"
	"github.com/ThanatosXingYu/TeleNexux/internal/notifications"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/settings"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramauth"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
	"github.com/ThanatosXingYu/TeleNexux/internal/web"
)

const (
	httpReadHeaderTimeout = 5 * time.Second
	httpReadTimeout       = 30 * time.Second
	httpWriteTimeout      = 45 * time.Second
	httpIdleTimeout       = 2 * time.Minute
	httpMaxHeaderBytes    = 1 << 20
)

func main() {
	if err := run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLogLevel(cfg.LogLevel),
	}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0o750); err != nil {
		return fmt.Errorf("create database directory: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	repository, err := account.NewSQLiteRepository(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := repository.Close(); err != nil {
			logger.Error("close account repository", "error", err)
		}
	}()

	authManager, err := auth.NewManager(cfg.AuthPath)
	if err != nil {
		return fmt.Errorf("initialize administrator authentication: %w", err)
	}
	if err := health.EnsureSessionDirectory(cfg.TelegramSessionDir); err != nil {
		return err
	}
	operationsStore, err := operations.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("initialize operations storage: %w", err)
	}
	defer func() {
		if err := operationsStore.Close(); err != nil {
			logger.Error("close operations storage", "error", err)
		}
	}()
	notificationSender := notifications.New(
		operationsStore,
		notifications.Options{
			Interval:     5 * time.Second,
			AllowPrivate: !strings.EqualFold(cfg.Environment, "production"),
		},
		logger,
	)
	notificationSender.Start(ctx)
	webRenderer, err := web.New()
	if err != nil {
		return fmt.Errorf("initialize web interface: %w", err)
	}
	settingsStore, err := settings.NewStore(cfg.SettingsPath)
	if err != nil {
		return fmt.Errorf("initialize system settings: %w", err)
	}
	settingsStore.UseDefaults(settings.Values{
		AppEnvironment:  cfg.Environment,
		LogLevel:        cfg.LogLevel,
		HTTPAddr:        cfg.HTTPAddr,
		TelegramAPIID:   cfg.TelegramAPIID,
		TelegramAPIHash: cfg.TelegramAPIHash,
	})

	var telegramDriver telegramauth.Driver
	if cfg.TelegramAPIID > 0 {
		telegramDriver = telegramauth.NewGotdDriver(cfg.TelegramAPIID, cfg.TelegramAPIHash)
	}
	telegramAuth, err := telegramauth.NewService(telegramDriver, telegramauth.ServiceConfig{
		SessionDir:       cfg.TelegramSessionDir,
		LoginTTL:         10 * time.Minute,
		OperationTimeout: 45 * time.Second,
		AppID:            cfg.TelegramAPIID,
		AppHash:          cfg.TelegramAPIHash,
	})
	if err != nil {
		return fmt.Errorf("initialize Telegram authentication: %w", err)
	}
	var telegramChats *telegramchat.Service
	if cfg.TelegramAPIID > 0 {
		telegramChats, err = telegramchat.NewService(
			cfg.TelegramAPIID,
			cfg.TelegramAPIHash,
			cfg.TelegramSessionDir,
		)
		if err != nil {
			return fmt.Errorf("initialize Telegram chat service: %w", err)
		}
	}
	accountService := account.NewService(repository)
	var monitorRunner httpapi.MonitorRunner
	if telegramChats != nil {
		scheduler := monitorscheduler.New(
			operationsStore,
			accountService,
			telegramChats,
			time.Minute,
			logger,
		)
		scheduler.Start(ctx)
		monitorRunner = scheduler
	}
	go cleanupMessageIndex(ctx, operationsStore, logger)
	var purgeSessionCleaner accountpurge.SessionCleaner
	if telegramChats != nil {
		purgeSessionCleaner = telegramChats
	}
	purgeWorker, err := accountpurge.New(
		accountService,
		purgeSessionCleaner,
		operationsStore,
	)
	if err != nil {
		return fmt.Errorf("initialize account purge worker: %w", err)
	}
	go runAccountPurgeLoop(ctx, purgeWorker, logger)

	server := newHTTPServer(
		cfg.HTTPAddr,
		httpapi.New(
			accountService,
			telegramAuth,
			telegramChats,
			settingsStore,
			authManager,
			webRenderer.Handler(),
			logger,
			health.NewChecker(health.Options{
				Database:           repository,
				SessionDirectory:   cfg.TelegramSessionDir,
				AuthPath:           cfg.AuthPath,
				TelegramConfigured: telegramChats != nil,
				AdminConfigured:    authManager.Configured,
			}),
			operationsStore,
			monitorRunner,
		).Handler(),
	)
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("HTTP server started", "address", cfg.HTTPAddr, "environment", cfg.Environment)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown HTTP server: %w", err)
	}
	return nil
}

func newHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}

func runAccountPurgeLoop(
	ctx context.Context,
	worker *accountpurge.Service,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := worker.RunOnce(ctx); err != nil {
			logger.Error("account purge cycle failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func cleanupMessageIndex(
	ctx context.Context,
	store *operations.Service,
	logger *slog.Logger,
) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			result, err := store.CleanupMessages(
				cleanupCtx,
				operations.RetentionCleanupInput{Actor: "retention_scheduler"},
			)
			cancel()
			if err != nil {
				logger.Warn("cleanup message index failed", "error", err)
				continue
			}
			if result.DeletedMessages > 0 {
				logger.Info("message retention cleanup completed",
					"deleted_messages", result.DeletedMessages,
					"destroyed_evidence_events", result.DestroyedEvidenceEvents,
					"held_messages", result.HeldMessages,
					"has_more", result.HasMore)
			}
		}
	}
}

func parseLogLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
