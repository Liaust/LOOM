package nodes

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"crypto/pbkdf2"
)

const (
	tokenHashScheme     = "pbkdf2-sha256"
	tokenHashIterations = 120000
	tokenHashBytes      = 32
	tokenSaltBytes      = 16
)

func newSecretToken(prefix string) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return strings.TrimSpace(prefix) + "_" + base64.RawURLEncoding.EncodeToString(random), nil
}

func hashSecretToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("token is required")
	}
	salt := make([]byte, tokenSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	derived, err := pbkdf2.Key(sha256.New, token, salt, tokenHashIterations, tokenHashBytes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s",
		tokenHashScheme,
		tokenHashIterations,
		base64.RawURLEncoding.EncodeToString(salt),
		base64.RawURLEncoding.EncodeToString(derived),
	), nil
}

func verifySecretToken(token, encoded string) bool {
	token = strings.TrimSpace(token)
	encoded = strings.TrimSpace(encoded)
	if token == "" || encoded == "" {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != tokenHashScheme {
		return false
	}
	var iterations int
	if _, err := fmt.Sscanf(parts[1], "%d", &iterations); err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || len(expected) == 0 {
		return false
	}
	actual, err := pbkdf2.Key(sha256.New, token, salt, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func tokenHint(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 8 {
		return token
	}
	return token[len(token)-8:]
}
