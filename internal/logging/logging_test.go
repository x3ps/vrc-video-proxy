package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"DEBUG":   slog.LevelDebug,
		" warn ":  slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for in, want := range cases {
		got, err := ParseLevel(in)
		if err != nil {
			t.Fatalf("ParseLevel(%q) returned error: %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}

	if _, err := ParseLevel("verbose"); err == nil {
		t.Fatal("ParseLevel(\"verbose\") returned nil error, want error")
	}
}

func TestNewRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelWarn)

	logger.Info("below the threshold")
	if buf.Len() != 0 {
		t.Fatalf("info logged at warn level: %q", buf.String())
	}

	logger.Error("above the threshold")
	if !strings.Contains(buf.String(), "above the threshold") {
		t.Fatalf("error not logged: %q", buf.String())
	}
}

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"":                                  "",
		"https://cdn.example.com/v.mp4?t=x": "https://cdn.example.com/v.mp4?<redacted>",
		"not a url":                         "<invalid-url>",
	}
	for in, want := range cases {
		if got := RedactURL(in); got != want {
			t.Fatalf("RedactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
