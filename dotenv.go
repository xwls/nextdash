package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxDotEnvLineSize = 1024 * 1024

// loadDotEnvFile loads variables that are not already present in the process
// environment. Existing environment variables always take precedence.
func loadDotEnvFile(path string) (int, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	values, err := parseDotEnv(file)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}

	loaded := 0
	for key, value := range values {
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return loaded, fmt.Errorf("set environment variable %s: %w", key, err)
		}
		loaded++
	}
	return loaded, nil
}

func parseDotEnv(reader io.Reader) (map[string]string, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxDotEnvLineSize)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		if lineNumber == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") || strings.HasPrefix(line, "export\t") {
			line = strings.TrimSpace(line[len("export"):])
		}

		separator := strings.IndexByte(line, '=')
		if separator < 1 {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNumber)
		}
		key := strings.TrimSpace(line[:separator])
		if !validEnvKey(key) {
			return nil, fmt.Errorf("line %d: invalid environment variable name", lineNumber)
		}
		value, err := parseDotEnvValue(strings.TrimSpace(line[separator+1:]))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read environment file: %w", err)
	}
	return values, nil
}

func validEnvKey(key string) bool {
	if key == "" || !isEnvKeyStart(key[0]) {
		return false
	}
	for i := 1; i < len(key); i++ {
		if !isEnvKeyStart(key[i]) && (key[i] < '0' || key[i] > '9') {
			return false
		}
	}
	return true
}

func isEnvKeyStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func parseDotEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	switch raw[0] {
	case '\'':
		end := strings.IndexByte(raw[1:], '\'')
		if end < 0 {
			return "", errors.New("unterminated single-quoted value")
		}
		end++
		if !validDotEnvRemainder(raw[end+1:]) {
			return "", errors.New("unexpected content after quoted value")
		}
		return raw[1:end], nil
	case '"':
		return parseDoubleQuotedDotEnvValue(raw)
	default:
		for i := 0; i < len(raw); i++ {
			if raw[i] == '#' && (i == 0 || raw[i-1] == ' ' || raw[i-1] == '\t') {
				raw = raw[:i]
				break
			}
		}
		return strings.TrimSpace(raw), nil
	}
}

func parseDoubleQuotedDotEnvValue(raw string) (string, error) {
	var value strings.Builder
	escaped := false
	for i := 1; i < len(raw); i++ {
		current := raw[i]
		if escaped {
			switch current {
			case 'n':
				value.WriteByte('\n')
			case 'r':
				value.WriteByte('\r')
			case 't':
				value.WriteByte('\t')
			case '"', '\\', '$':
				value.WriteByte(current)
			default:
				value.WriteByte('\\')
				value.WriteByte(current)
			}
			escaped = false
			continue
		}
		if current == '\\' {
			escaped = true
			continue
		}
		if current == '"' {
			if !validDotEnvRemainder(raw[i+1:]) {
				return "", errors.New("unexpected content after quoted value")
			}
			return value.String(), nil
		}
		value.WriteByte(current)
	}
	return "", errors.New("unterminated double-quoted value")
}

func validDotEnvRemainder(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw == "" || strings.HasPrefix(raw, "#")
}
