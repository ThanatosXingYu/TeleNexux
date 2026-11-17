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
	"github.com/ThanatosXingYu/TeleNexux/internal/config"
	"github.com/ThanatosXingYu/TeleNexux/internal/httpapi"
	"github.com/ThanatosXingYu/TeleNexux/internal/telegramauth"
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

	webRenderer, err := web.New()
	if err != nil {
		return fmt.Errorf("initialize web interface: %w", err)
	}

	codeFetcher, err := telegramauth.NewHTTPCodeFetcher(
		cfg.TelegramCodeHosts,
		cfg.TelegramDNSServer,
		10*time.Second,
	)
	if err != nil {
		return fmt.Errorf("initialize Telegram code fetcher: %w", err)
	}
	var telegramDriver telegramauth.Driver
	if cfg.TelegramAPIID > 0 {
		telegramDriver = telegramauth.NewGotdDriver(cfg.TelegramAPIID, cfg.TelegramAPIHash)
	}
	telegramAuth, err := telegramauth.NewService(telegramDriver, codeFetcher, telegramauth.ServiceConfig{
		SessionDir:       cfg.TelegramSessionDir,
		LoginTTL:         10 * time.Minute,
		OperationTimeout: 45 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("initialize Telegram authentication: %w", err)
	}

	server := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: httpapi.New(
			account.NewService(repository),
			telegramAuth,
			webRenderer.Handler(),
			logger,
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
