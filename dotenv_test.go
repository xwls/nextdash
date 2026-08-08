package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	input := "\uFEFF# comment\r\n" +
		"export NEXTDASH_ADMIN_USERNAME=admin\r\n" +
		"NEXTDASH_ADMIN_PASSWORD_HASH='$argon2id$v=19$m=65536,t=3,p=2$salt$hash'\r\n" +
		"DOUBLE=\"line\\nvalue\\t\\\"quoted\\\"\\$literal\" # comment\r\n" +
		"UNQUOTED=value with spaces # comment\r\n" +
		"HASH=value#part\r\n" +
		"EMPTY=\r\n" +
		"DUPLICATE=first\r\n" +
		"DUPLICATE=second\r\n"

	values, err := parseDotEnv(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"NEXTDASH_ADMIN_USERNAME":      "admin",
		"NEXTDASH_ADMIN_PASSWORD_HASH": "$argon2id$v=19$m=65536,t=3,p=2$salt$hash",
		"DOUBLE":                       "line\nvalue\t\"quoted\"$literal",
		"UNQUOTED":                     "value with spaces",
		"HASH":                         "value#part",
		"EMPTY":                        "",
		"DUPLICATE":                    "second",
	}
	if len(values) != len(expected) {
		t.Fatalf("parsed %d values, want %d: %#v", len(values), len(expected), values)
	}
	for key, want := range expected {
		if got := values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestParseDotEnvRejectsMalformedInputWithoutLeakingValue(t *testing.T) {
	cases := []string{
		"NO_SEPARATOR",
		"1INVALID=value",
		"KEY='top-secret",
		"KEY=\"top-secret",
		"KEY='value' trailing",
	}
	for _, input := range cases {
		_, err := parseDotEnv(strings.NewReader(input))
		if err == nil {
			t.Errorf("expected error for %q", input)
			continue
		}
		if strings.Contains(err.Error(), "top-secret") {
			t.Errorf("error leaked value for %q: %v", input, err)
		}
	}
}

func TestLoadDotEnvFileDoesNotOverrideProcessEnvironment(t *testing.T) {
	const existingKey = "NEXTDASH_DOTENV_TEST_EXISTING"
	const newKey = "NEXTDASH_DOTENV_TEST_NEW"
	t.Setenv(existingKey, "from-process")
	previous, existed := os.LookupEnv(newKey)
	if err := os.Unsetenv(newKey); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(newKey, previous)
		} else {
			_ = os.Unsetenv(newKey)
		}
	})

	path := filepath.Join(t.TempDir(), ".env")
	contents := existingKey + "=from-file\n" + newKey + "='$literal$value'\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadDotEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 1 {
		t.Fatalf("loaded %d variables, want 1", loaded)
	}
	if got := os.Getenv(existingKey); got != "from-process" {
		t.Fatalf("existing value overridden: %q", got)
	}
	if got := os.Getenv(newKey); got != "$literal$value" {
		t.Fatalf("new value = %q", got)
	}
}

func TestLoadDotEnvFileMissingIsOptional(t *testing.T) {
	loaded, err := loadDotEnvFile(filepath.Join(t.TempDir(), "missing.env"))
	if err != nil || loaded != 0 {
		t.Fatalf("loaded=%d err=%v", loaded, err)
	}
}
