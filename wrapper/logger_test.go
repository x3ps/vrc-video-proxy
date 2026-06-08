package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":         slog.LevelInfo,
		"info":     slog.LevelInfo,
		"DEBUG":    slog.LevelDebug,
		"warn":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"nonsense": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLogLevel(in); got != want {
			t.Fatalf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// withDebugLogPath redirects the debug log to a file inside a temp dir for the
// duration of the test and returns its path.
func withDebugLogPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), wrapperLogName)
	orig := debugLogPath
	debugLogPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { debugLogPath = orig })
	return path
}

func TestNewLoggerDebugWritesFileAndStderr(t *testing.T) {
	path := withDebugLogPath(t)

	var stderr bytes.Buffer
	logger, flush := newLogger(&stderr, slog.LevelDebug)

	logger.Debug("parsed request", "url", "https://example.com/v")
	flush()

	if !bytes.Contains(stderr.Bytes(), []byte("parsed request")) {
		t.Fatalf("stderr missing log line: %q", stderr.String())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading debug log: %v", err)
	}
	if !bytes.Contains(data, []byte("parsed request")) {
		t.Fatalf("debug file missing log line: %q", string(data))
	}
	if !bytes.Contains(data, []byte("url=https://example.com/v")) {
		t.Fatalf("debug file missing attribute: %q", string(data))
	}
}

func TestNewLoggerInfoCreatesNoFile(t *testing.T) {
	path := withDebugLogPath(t)

	var stderr bytes.Buffer
	logger, flush := newLogger(&stderr, slog.LevelInfo)

	logger.Info("fetched video", "bytes", 42)
	logger.Debug("ignored at info level")
	flush()

	if !bytes.Contains(stderr.Bytes(), []byte("fetched video")) {
		t.Fatalf("stderr missing log line: %q", stderr.String())
	}
	if bytes.Contains(stderr.Bytes(), []byte("ignored at info level")) {
		t.Fatalf("debug line logged at info level: %q", stderr.String())
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("debug file created at info level: stat err = %v", err)
	}
}
