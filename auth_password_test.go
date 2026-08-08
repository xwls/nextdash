package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func testPHC(memory, iterations, parallelism uint32) string {
	salt := base64.RawStdEncoding.EncodeToString([]byte("12345678abcdefgh"))
	hash := base64.RawStdEncoding.EncodeToString([]byte("1234567890abcdef1234567890abcdef"))
	return "$argon2id$v=19$m=" + uintString(memory) + ",t=" + uintString(iterations) + ",p=" + uintString(parallelism) + "$" + salt + "$" + hash
}

func uintString(value uint32) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var out [10]byte
	i := len(out)
	for value > 0 {
		i--
		out[i] = digits[value%10]
		value /= 10
	}
	return string(out[i:])
}

func TestHashAdminPasswordRoundTrip(t *testing.T) {
	hash, err := hashAdminPassword([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseArgon2idPHC(hash)
	if err != nil {
		t.Fatalf("parse generated hash: %v", err)
	}
	if !verifyAdminPassword(parsed, []byte("correct horse battery staple")) {
		t.Fatal("generated hash did not verify")
	}
	if verifyAdminPassword(parsed, []byte("wrong password")) {
		t.Fatal("wrong password verified")
	}
}

func TestParseArgon2idPHCRejectsInvalidAndExcessiveParameters(t *testing.T) {
	cases := []string{
		"",
		"$argon2i$v=19$m=65536,t=3,p=2$bad$bad",
		"$argon2id$v=16$m=65536,t=3,p=2$bad$bad",
		testPHC(argon2MaxMemory+1, 1, 1),
		testPHC(8, argon2MaxIterations+1, 1),
		testPHC(8, 1, argon2MaxParallelism+1),
		strings.Replace(testPHC(8, 1, 1), "m=8", "m=0", 1),
	}
	for _, input := range cases {
		if _, err := parseArgon2idPHC(input); err == nil {
			t.Errorf("expected parse failure for %q", input)
		}
	}
}

func TestLoadAuthConfigIsMandatoryAndSecureByDefault(t *testing.T) {
	t.Setenv("NEXTDASH_ADMIN_PASSWORD_HASH", "")
	if _, err := loadAuthConfigFromEnv(); err == nil {
		t.Fatal("missing password hash did not fail")
	}

	t.Setenv("NEXTDASH_ADMIN_PASSWORD_HASH", testPHC(8, 1, 1))
	t.Setenv("NEXTDASH_ADMIN_USERNAME", "")
	t.Setenv("NEXTDASH_AUTH_COOKIE_SECURE", "")
	config, err := loadAuthConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.username != "admin" || !config.cookieSecure {
		t.Fatalf("unexpected defaults: %+v", config)
	}

	t.Setenv("NEXTDASH_AUTH_COOKIE_SECURE", "0")
	config, err = loadAuthConfigFromEnv()
	if err != nil || config.cookieSecure {
		t.Fatalf("development cookie setting not accepted: %+v, %v", config, err)
	}

	t.Setenv("NEXTDASH_AUTH_COOKIE_SECURE", "yes")
	if _, err := loadAuthConfigFromEnv(); err == nil {
		t.Fatal("invalid secure-cookie setting did not fail")
	}
}
