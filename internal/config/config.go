// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

type Config struct {
	Environment     string
	HTTPAddr        string
	LogLevel        string
	DatabasePath    string
	ShutdownTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Environment:     envOrDefault("APP_ENV", "development"),
		HTTPAddr:        envOrDefault("HTTP_ADDR", ":8080"),
		LogLevel:        envOrDefault("LOG_LEVEL", "info"),
		DatabasePath:    envOrDefault("DATABASE_PATH", "./data/telenexus.db"),
		ShutdownTimeout: 10 * time.Second,
	}

	if raw := os.Getenv("SHUTDOWN_TIMEOUT"); raw != "" {
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
	if c.ShutdownTimeout <= 0 {
		return errors.New("SHUTDOWN_TIMEOUT must be positive")
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
