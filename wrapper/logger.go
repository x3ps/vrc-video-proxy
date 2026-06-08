package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	// envLogLevel selects the wrapper's log verbosity (debug, info, warn, error).
	envLogLevel = "VRCVP_LOG_LEVEL"

	// wrapperLogName is the debug log file dropped next to the wrapper executable
	// when the log level is debug.
	wrapperLogName = "wrapper.log"
)

// debugLogPath returns the path of the debug log file: "wrapper.log" next to the
// wrapper executable. It is a variable so tests can redirect it to a temp file.
var debugLogPath = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exe), wrapperLogName), nil
}

// parseLogLevel maps a level name to a slog.Level, defaulting to info. Unlike the
// server, the wrapper never fails on a bad value: it is launched by VRChat with
// no console to read an error, so an unknown level silently falls back to info.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// newLogger builds the wrapper logger. It always writes human-readable lines to
// stderr at the given level. When the level is debug it additionally appends to
// wrapper.log next to the executable, so a debugging user gets a persistent
// record even though VRChat gives the wrapper no console. The returned flush
// func closes the file (if any) and must be called (e.g. deferred) before exit.
func newLogger(stderr io.Writer, level slog.Level) (*slog.Logger, func()) {
	handlers := []slog.Handler{
		slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}),
	}

	flush := func() {}
	if level == slog.LevelDebug {
		if file := openDebugLog(); file != nil {
			handlers = append(handlers, slog.NewTextHandler(file, &slog.HandlerOptions{Level: slog.LevelDebug}))
			flush = func() { _ = file.Close() }
		}
	}

	return slog.New(multiHandler(handlers)), flush
}

// openDebugLog opens (creating/appending) the debug log file, or returns nil if
// the path cannot be resolved or the file cannot be opened. Failures are silent:
// a logging problem must never change the wrapper's behaviour, so it simply falls
// back to stderr-only.
func openDebugLog() *os.File {
	path, err := debugLogPath()
	if err != nil {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil
	}
	return file
}

// multiHandler fans each record out to every embedded handler, so the wrapper
// can log to stderr and the debug file at once. Modelled on the standard slog
// fan-out pattern.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	var firstErr error
	for _, h := range m {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithAttrs(attrs)
	}
	return out
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	out := make(multiHandler, len(m))
	for i, h := range m {
		out[i] = h.WithGroup(name)
	}
	return out
}
