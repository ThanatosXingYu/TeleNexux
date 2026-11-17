package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const maxEnvFileSize = 64 << 10

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func readEnvFile(path string) (_ map[string]string, err error) {
	values := make(map[string]string)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open environment file %q: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			if err == nil {
				err = fmt.Errorf("close environment file %q: %w", path, closeErr)
			}
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat environment file %q: %w", path, err)
	}
	if info.Size() > maxEnvFileSize {
		return nil, fmt.Errorf("environment file %q exceeds %d bytes", path, maxEnvFileSize)
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxEnvFileSize)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, rawValue, ok := strings.Cut(line, "=")
		if !ok || !envKeyPattern.MatchString(strings.TrimSpace(key)) {
			return nil, fmt.Errorf("parse environment file %q line %d: expected KEY=VALUE", path, lineNumber)
		}
		value, err := parseEnvValue(strings.TrimSpace(rawValue))
		if err != nil {
			return nil, fmt.Errorf("parse environment file %q line %d: %w", path, lineNumber, err)
		}
		values[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read environment file %q: %w", path, err)
	}
	return values, nil
}

func parseEnvValue(value string) (string, error) {
	if value == "" {
		return value, nil
	}
	if value[0] == '"' {
		if len(value) < 2 {
			return "", errors.New("unterminated double-quoted value")
		}
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value: %w", err)
		}
		return decoded, nil
	}
	if value[0] == '\'' {
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", errors.New("unterminated single-quoted value")
		}
		return value[1 : len(value)-1], nil
	}
	return value, nil
}
