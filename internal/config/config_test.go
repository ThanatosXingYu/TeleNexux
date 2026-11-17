package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("DATABASE_PATH", "")
	t.Setenv("SHUTDOWN_TIMEOUT", "")
	t.Setenv("TELEGRAM_API_ID", "")
	t.Setenv("TELEGRAM_API_HASH", "")
	t.Setenv("TELEGRAM_SESSION_DIR", "")
	t.Setenv("TELEGRAM_CODE_FETCH_HOSTS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9487" {
		t.Errorf("HTTPAddr = %q, want loopback address", cfg.HTTPAddr)
	}
	if cfg.ShutdownTimeout != 10*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 10s", cfg.ShutdownTimeout)
	}
	if cfg.TelegramSessionDir != "./data/telegram-sessions" {
		t.Errorf("TelegramSessionDir = %q, want default session directory", cfg.TelegramSessionDir)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{name: "invalid address", key: "HTTP_ADDR", value: "localhost", wantErr: "HTTP_ADDR"},
		{name: "invalid timeout", key: "SHUTDOWN_TIMEOUT", value: "soon", wantErr: "SHUTDOWN_TIMEOUT"},
		{name: "zero timeout", key: "SHUTDOWN_TIMEOUT", value: "0s", wantErr: "SHUTDOWN_TIMEOUT"},
		{name: "invalid API ID", key: "TELEGRAM_API_ID", value: "abc", wantErr: "TELEGRAM_API_ID"},
		{name: "missing API hash", key: "TELEGRAM_API_ID", value: "123", wantErr: "TELEGRAM_API_HASH"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadTelegramConfiguration(t *testing.T) {
	t.Setenv("TELEGRAM_API_ID", "12345")
	t.Setenv("TELEGRAM_API_HASH", "test-hash")
	t.Setenv("TELEGRAM_CODE_FETCH_HOSTS", "tgok.cc, API.didiapi.uk")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TelegramAPIID != 12345 || cfg.TelegramAPIHash != "test-hash" {
		t.Fatalf("Telegram credentials = %d/%q, want configured values", cfg.TelegramAPIID, cfg.TelegramAPIHash)
	}
	if len(cfg.TelegramCodeHosts) != 2 || cfg.TelegramCodeHosts[1] != "api.didiapi.uk" {
		t.Fatalf("TelegramCodeHosts = %#v, want normalized hosts", cfg.TelegramCodeHosts)
	}
}

func TestLoadReadsDotEnvWithoutOverridingProcessEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "APP_ENV=file\nHTTP_ADDR=:9000\nTELEGRAM_API_ID=123\nTELEGRAM_API_HASH=\"from-file\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("ENV_FILE", path)
	t.Setenv("APP_ENV", "process")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Environment != "process" {
		t.Errorf("Environment = %q, want process environment to win", cfg.Environment)
	}
	if cfg.HTTPAddr != ":9000" || cfg.TelegramAPIHash != "from-file" {
		t.Errorf("dotenv values were not loaded: %+v", cfg)
	}
}
