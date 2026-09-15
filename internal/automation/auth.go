package automation

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

func newIntegrationToken() (string, error) {
	return generateIntegrationToken()
}

func generateIntegrationToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate integration token: %w", err)
	}
	return "loom_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func hashIntegrationToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("token is required")
	}
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func verifyIntegrationToken(token, storedHash string) bool {
	hash, err := hashIntegrationToken(token)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hash), []byte(strings.TrimSpace(storedHash))) == 1
}

func tokenLastFour(token string) string {
	return tokenHint(token)
}

func tokenHint(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 4 {
		return token
	}
	return token[len(token)-4:]
}
