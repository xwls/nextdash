package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argon2DefaultMemory      = 64 * 1024
	argon2DefaultIterations  = 3
	argon2DefaultParallelism = 2
	argon2DefaultSaltLength  = 16
	argon2DefaultKeyLength   = 32

	argon2MaxMemory      = 128 * 1024
	argon2MaxIterations  = 10
	argon2MaxParallelism = 8
	argon2MaxSaltLength  = 64
	argon2MaxKeyLength   = 64
)

type argon2PasswordHash struct {
	memory      uint32
	iterations  uint32
	parallelism uint8
	salt        []byte
	hash        []byte
}

func parseArgon2idPHC(encoded string) (argon2PasswordHash, error) {
	var parsed argon2PasswordHash
	parts := strings.Split(strings.TrimSpace(encoded), "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return parsed, errors.New("must be an Argon2id PHC string using version 19")
	}

	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return parsed, errors.New("invalid Argon2id parameter list")
	}
	values := make(map[string]uint64, 3)
	for _, param := range params {
		pair := strings.SplitN(param, "=", 2)
		if len(pair) != 2 || (pair[0] != "m" && pair[0] != "t" && pair[0] != "p") {
			return parsed, errors.New("invalid Argon2id parameter")
		}
		if _, exists := values[pair[0]]; exists {
			return parsed, errors.New("duplicate Argon2id parameter")
		}
		value, err := strconv.ParseUint(pair[1], 10, 32)
		if err != nil || value == 0 {
			return parsed, errors.New("invalid Argon2id parameter value")
		}
		values[pair[0]] = value
	}
	if len(values) != 3 {
		return parsed, errors.New("missing Argon2id parameter")
	}
	if values["m"] > argon2MaxMemory || values["t"] > argon2MaxIterations || values["p"] > argon2MaxParallelism {
		return parsed, errors.New("Argon2id parameters exceed safe server limits")
	}

	decode := func(value string) ([]byte, error) {
		decoded, err := base64.RawStdEncoding.Strict().DecodeString(value)
		if err != nil {
			return nil, errors.New("invalid Argon2id base64 data")
		}
		return decoded, nil
	}
	salt, err := decode(parts[4])
	if err != nil {
		return parsed, err
	}
	hash, err := decode(parts[5])
	if err != nil {
		return parsed, err
	}
	if len(salt) < 8 || len(salt) > argon2MaxSaltLength {
		return parsed, errors.New("Argon2id salt length is outside safe limits")
	}
	if len(hash) < 16 || len(hash) > argon2MaxKeyLength {
		return parsed, errors.New("Argon2id hash length is outside safe limits")
	}

	parsed = argon2PasswordHash{
		memory:      uint32(values["m"]),
		iterations:  uint32(values["t"]),
		parallelism: uint8(values["p"]),
		salt:        salt,
		hash:        hash,
	}
	return parsed, nil
}

func hashAdminPassword(password []byte) (string, error) {
	if len(password) == 0 {
		return "", errors.New("password must not be empty")
	}
	salt := make([]byte, argon2DefaultSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey(password, salt, argon2DefaultIterations, argon2DefaultMemory, argon2DefaultParallelism, argon2DefaultKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argon2DefaultMemory,
		argon2DefaultIterations,
		argon2DefaultParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func verifyAdminPassword(parsed argon2PasswordHash, password []byte) bool {
	candidate := argon2.IDKey(password, parsed.salt, parsed.iterations, parsed.memory, parsed.parallelism, uint32(len(parsed.hash)))
	return subtle.ConstantTimeCompare(candidate, parsed.hash) == 1
}
