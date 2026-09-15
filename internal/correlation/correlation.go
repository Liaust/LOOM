package correlation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

const Header = "X-Loom-Correlation-ID"

type contextKey struct{}

func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "corr_unavailable"
	}
	return "corr_" + hex.EncodeToString(b[:])
}

func Normalize(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return New()
	}
	return value
}

func WithContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, Normalize(id))
}

func FromContext(ctx context.Context) string {
	value, _ := ctx.Value(contextKey{}).(string)
	return Normalize(value)
}
