// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
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
	AccountProxiesPath string
	ShutdownTimeout    time.Duration
	TelegramAPIID      int
	TelegramAPIHash    string
	TelegramSessionDir string
	TrustedProxyCIDRs  []netip.Prefix
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
		Environment:  valueOrDefault(lookup("APP_ENV"), "development"),
		HTTPAddr:     valueOrDefault(lookup("HTTP_ADDR"), "127.0.0.1:9487"),
		LogLevel:     valueOrDefault(lookup("LOG_LEVEL"), "info"),
		DatabasePath: valueOrDefault(lookup("DATABASE_PATH"), "./data/telenexus.db"),
		SettingsPath: settingsPath,
		AuthPath:     valueOrDefault(lookup("AUTH_PATH"), "./data/auth.json"),
		AccountProxiesPath: valueOrDefault(
			lookup("ACCOUNT_PROXIES_PATH"),
			"./data/account-proxies.json",
		),
		ShutdownTimeout:    10 * time.Second,
		TelegramAPIHash:    strings.TrimSpace(lookup("TELEGRAM_API_HASH")),
		TelegramSessionDir: valueOrDefault(lookup("TELEGRAM_SESSION_DIR"), "./data/telegram-sessions"),
	}
	cfg.TrustedProxyCIDRs, err = parseTrustedProxyCIDRs(lookup("TRUSTED_PROXY_CIDRS"))
	if err != nil {
		return Config{}, err
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
	if c.AccountProxiesPath == "" {
		return errors.New("ACCOUNT_PROXIES_PATH must not be empty")
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

func parseTrustedProxyCIDRs(raw string) ([]netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	prefixes := make([]netip.Prefix, 0)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if address, err := netip.ParseAddr(item); err == nil {
			address = address.Unmap()
			prefixes = append(prefixes, netip.PrefixFrom(address, address.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("invalid TRUSTED_PROXY_CIDRS entry %q: %w", item, err)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}
