// Package settings manages local runtime settings stored outside version control.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxSettingsFileSize = 64 << 10

type Values struct {
	HTTPAddr              string   `json:"http_addr,omitempty"`
	TelegramAPIID         int      `json:"telegram_api_id,omitempty"`
	TelegramAPIHash       string   `json:"telegram_api_hash,omitempty"`
	TelegramCodeHosts     []string `json:"telegram_code_fetch_hosts,omitempty"`
	TelegramCodeDNSServer string   `json:"telegram_code_dns_server,omitempty"`
}

type Store struct {
	path            string
	mu              sync.RWMutex
	values          Values
	restartRequired bool
}

func Load(path string) (Values, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Values{}, nil
	}
	if err != nil {
		return Values{}, fmt.Errorf("read settings file %q: %w", path, err)
	}
	if len(data) > maxSettingsFileSize {
		return Values{}, fmt.Errorf("settings file %q exceeds %d bytes", path, maxSettingsFileSize)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var values Values
	if err := decoder.Decode(&values); err != nil {
		return Values{}, fmt.Errorf("decode settings file %q: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Values{}, fmt.Errorf("settings file %q must contain one JSON object", path)
	}
	values, err = Normalize(values)
	if err != nil {
		return Values{}, fmt.Errorf("validate settings file %q: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return Values{}, fmt.Errorf("secure settings file %q: %w", path, err)
	}
	return values, nil
}

func NewStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("settings path must not be empty")
	}
	values, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, values: values}, nil
}

func (s *Store) UseDefaults(defaults Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values.HTTPAddr == "" {
		s.values.HTTPAddr = defaults.HTTPAddr
	}
	if s.values.TelegramAPIID == 0 {
		s.values.TelegramAPIID = defaults.TelegramAPIID
	}
	if s.values.TelegramAPIHash == "" {
		s.values.TelegramAPIHash = defaults.TelegramAPIHash
	}
	if len(s.values.TelegramCodeHosts) == 0 {
		s.values.TelegramCodeHosts = append([]string(nil), defaults.TelegramCodeHosts...)
	}
	if s.values.TelegramCodeDNSServer == "" {
		s.values.TelegramCodeDNSServer = defaults.TelegramCodeDNSServer
	}
}

func (s *Store) Snapshot() Values {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return clone(s.values)
}

func (s *Store) Save(values Values) error {
	normalized, err := Normalize(values)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	encoded = append(encoded, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writePrivateFile(s.path, encoded); err != nil {
		return err
	}
	s.values = normalized
	s.restartRequired = true
	return nil
}

func (s *Store) RestartRequired() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.restartRequired
}

func (v Values) Lookup(key string) (string, bool) {
	switch key {
	case "HTTP_ADDR":
		return v.HTTPAddr, v.HTTPAddr != ""
	case "TELEGRAM_API_ID":
		if v.TelegramAPIID <= 0 {
			return "", false
		}
		return fmt.Sprintf("%d", v.TelegramAPIID), true
	case "TELEGRAM_API_HASH":
		return v.TelegramAPIHash, v.TelegramAPIHash != ""
	case "TELEGRAM_CODE_FETCH_HOSTS":
		return strings.Join(v.TelegramCodeHosts, ","), len(v.TelegramCodeHosts) > 0
	case "TELEGRAM_CODE_DNS_SERVER":
		return v.TelegramCodeDNSServer, v.TelegramCodeDNSServer != ""
	default:
		return "", false
	}
}

func Normalize(values Values) (Values, error) {
	values.HTTPAddr = strings.TrimSpace(values.HTTPAddr)
	values.TelegramAPIHash = strings.TrimSpace(values.TelegramAPIHash)
	values.TelegramCodeDNSServer = strings.TrimSpace(values.TelegramCodeDNSServer)

	hosts := make([]string, 0, len(values.TelegramCodeHosts))
	seen := make(map[string]struct{}, len(values.TelegramCodeHosts))
	for _, host := range values.TelegramCodeHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	values.TelegramCodeHosts = hosts
	if err := Validate(values); err != nil {
		return Values{}, err
	}
	return values, nil
}

func Validate(values Values) error {
	if values.HTTPAddr != "" {
		if _, _, err := net.SplitHostPort(values.HTTPAddr); err != nil {
			return fmt.Errorf("HTTP_ADDR is invalid: %w", err)
		}
	}
	if values.TelegramAPIID < 0 {
		return errors.New("Telegram API ID must not be negative")
	}
	if (values.TelegramAPIID == 0) != (values.TelegramAPIHash == "") {
		return errors.New("Telegram API ID and API hash must be configured together")
	}
	for _, host := range values.TelegramCodeHosts {
		if strings.ContainsAny(host, `/\:`) || net.ParseIP(host) != nil || host == "localhost" {
			return fmt.Errorf("code fetch host %q is invalid", host)
		}
	}
	if values.TelegramCodeDNSServer != "" {
		host, port, err := net.SplitHostPort(values.TelegramCodeDNSServer)
		if err != nil {
			return fmt.Errorf("code fetch DNS server is invalid: %w", err)
		}
		if _, err := netip.ParseAddr(host); err != nil {
			return errors.New("code fetch DNS server must be an IP address")
		}
		if port != "53" {
			return errors.New("code fetch DNS server must use port 53")
		}
	}
	return nil
}

func clone(values Values) Values {
	values.TelegramCodeHosts = append([]string(nil), values.TelegramCodeHosts...)
	return values
}

func writePrivateFile(path string, data []byte) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary settings file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("remove temporary settings file: %w", removeErr)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary settings file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary settings file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary settings file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary settings file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace settings file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure settings file: %w", err)
	}
	return nil
}
