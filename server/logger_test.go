package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"DEBUG":   slog.LevelDebug,
		" warn ":  slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, want := range cases {
		got, err := parseLogLevel(in)
		if err != nil {
			t.Fatalf("parseLogLevel(%q) returned error: %v", in, err)
		}
		if got != want {
			t.Fatalf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}

	if _, err := parseLogLevel("verbose"); err == nil {
		t.Fatal("parseLogLevel(\"verbose\") returned nil error, want error")
	}
}

func TestNewLoggerRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := newLogger(&buf, slog.LevelWarn)

	logger.Info("below the threshold")
	if buf.Len() != 0 {
		t.Fatalf("info logged at warn level: %q", buf.String())
	}

	logger.Error("above the threshold")
	if !strings.Contains(buf.String(), "above the threshold") {
		t.Fatalf("error not logged: %q", buf.String())
	}
}
