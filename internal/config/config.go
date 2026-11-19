// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ThanatosXingYu/TeleNexux/internal/settings"
)

type Config struct {
	Environment        string
	HTTPAddr           string
	LogLevel           string
	DatabasePath       string
	SettingsPath       string
	AuthPath           string
	ShutdownTimeout    time.Duration
	TelegramAPIID      int
	TelegramAPIHash    string
	TelegramSessionDir string
}

func Load() (Config, error) {
	envFile := valueOrDefault(os.Getenv("ENV_FILE"), ".env")
	fileValues, err := readEnvFile(envFile)
	if err != nil {
		return Config{}, err
	}
	settingsPath := valueOrDefault(
		valueOrDefault(os.Getenv("SETTINGS_PATH"), fileValues["SETTINGS_PATH"]),
		"./data/settings.json",
	)
	storedSettings, err := settings.Load(settingsPath)
	if err != nil {
		return Config{}, err
	}
	lookup := func(key string) string {
		if value, ok := os.LookupEnv(key); ok {
			return value
		}
		if value, ok := storedSettings.Lookup(key); ok {
			return value
		}
		return fileValues[key]
	}

	cfg := Config{
		Environment:        valueOrDefault(lookup("APP_ENV"), "development"),
		HTTPAddr:           valueOrDefault(lookup("HTTP_ADDR"), "127.0.0.1:9487"),
		LogLevel:           valueOrDefault(lookup("LOG_LEVEL"), "info"),
		DatabasePath:       valueOrDefault(lookup("DATABASE_PATH"), "./data/telenexus.db"),
		SettingsPath:       settingsPath,
		AuthPath:           valueOrDefault(lookup("AUTH_PATH"), "./data/auth.json"),
		ShutdownTimeout:    10 * time.Second,
		TelegramAPIHash:    strings.TrimSpace(lookup("TELEGRAM_API_HASH")),
		TelegramSessionDir: valueOrDefault(lookup("TELEGRAM_SESSION_DIR"), "./data/telegram-sessions"),
	}

	if raw := strings.TrimSpace(lookup("TELEGRAM_API_ID")); raw != "" {
		apiID, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse TELEGRAM_API_ID: %w", err)
		}
		cfg.TelegramAPIID = apiID
	}
	if raw := lookup("SHUTDOWN_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parse SHUTDOWN_TIMEOUT: %w", err)
		}
		cfg.ShutdownTimeout = timeout
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if c.Environment == "" {
		return errors.New("APP_ENV must not be empty")
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return fmt.Errorf("invalid HTTP_ADDR %q: %w", c.HTTPAddr, err)
	}
	if c.DatabasePath == "" {
		return errors.New("DATABASE_PATH must not be empty")
	}
	if c.AuthPath == "" {
		return errors.New("AUTH_PATH must not be empty")
	}
	if c.ShutdownTimeout <= 0 {
		return errors.New("SHUTDOWN_TIMEOUT must be positive")
	}
	if c.TelegramAPIID < 0 {
		return errors.New("TELEGRAM_API_ID must not be negative")
	}
	if (c.TelegramAPIID == 0) != (c.TelegramAPIHash == "") {
		return errors.New("TELEGRAM_API_ID and TELEGRAM_API_HASH must be configured together")
	}
	if c.TelegramSessionDir == "" {
		return errors.New("TELEGRAM_SESSION_DIR must not be empty")
	}
	return nil
}

func valueOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
