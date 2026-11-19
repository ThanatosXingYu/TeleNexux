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
	"syscall"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/account"
	"github.com/ThanatosXingYu/TeleNexux/internal/auth"
	"github.com/ThanatosXingYu/TeleNexux/internal/config"
	"github.com/ThanatosXingYu/TeleNexux/internal/health"
	"github.com/ThanatosXingYu/TeleNexux/internal/httpapi"
	"github.com/ThanatosXingYu/TeleNexux/internal/monitorscheduler"
	"github.com/ThanatosXingYu/TeleNexux/internal/operations"
	"github.com/ThanatosXingYu/TeleNexux/internal/settings"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramauth"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramchat"
	"github.com/ThanatosXingYu/TeleNexux/internal/web"
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
	var monitorRunner httpapi.MonitorRunner
	if telegramChats != nil {
		scheduler := monitorscheduler.New(operationsStore, telegramChats, time.Minute, logger)
		scheduler.Start(ctx)
		monitorRunner = scheduler
	}
	go cleanupMessageIndex(ctx, operationsStore, logger)

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.New(
			account.NewService(repository),
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
		ReadHeaderTimeout: cfg.ShutdownTimeout,
	}
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
			deleted, err := store.CleanupMessages(cleanupCtx, 30)
			cancel()
			if err != nil {
				logger.Warn("cleanup message index failed", "error", err)
				continue
			}
			if deleted > 0 {
				logger.Info("message index retention cleanup completed", "deleted", deleted)
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
