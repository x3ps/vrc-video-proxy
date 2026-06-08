package main

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// parseLogLevel maps a human-friendly level name to a slog.Level. An empty
// string defaults to info; anything unrecognised is an error so a typo in the
// configured level fails fast at startup rather than silently logging at info.
func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (want debug, info, warn or error)", s)
	}
}

// newLogger builds the process logger: a text handler at the given level.
func newLogger(w io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}
