package logging

import (
	"io"
	"log/slog"
	"strings"
)

func New(level string, out io.Writer) *slog.Logger {
	var slogLevel slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level: slogLevel,
	}))
}
