package automation

import (
	"strings"
	"testing"
)

func TestIntegrationTokenHash(t *testing.T) {
	token := "loom_test_token"
	hash, err := hashIntegrationToken(token)
	if err != nil {
		t.Fatalf("hashIntegrationToken returned error: %v", err)
	}
	if !strings.HasPrefix(hash, "sha256:") {
		t.Fatalf("hash = %q, want sha256 prefix", hash)
	}
	if len(hash) != len("sha256:")+64 {
		t.Fatalf("hash length = %d", len(hash))
	}
	hashAgain, err := hashIntegrationToken(token)
	if err != nil {
		t.Fatalf("hashIntegrationToken returned error: %v", err)
	}
	if hash != hashAgain {
		t.Fatal("hashIntegrationToken should be deterministic")
	}
	if !verifyIntegrationToken(token, hash) {
		t.Fatal("verifyIntegrationToken rejected matching token")
	}
	if verifyIntegrationToken("wrong", hash) {
		t.Fatal("verifyIntegrationToken accepted wrong token")
	}
}

func TestNewIntegrationTokenIsOpaque(t *testing.T) {
	token, err := generateIntegrationToken()
	if err != nil {
		t.Fatalf("newIntegrationToken returned error: %v", err)
	}
	if !strings.HasPrefix(token, "loom_") {
		t.Fatalf("token = %q, want loom_ prefix", token)
	}
	if tokenHint(token) == "" {
		t.Fatal("tokenHint returned empty suffix")
	}
}
