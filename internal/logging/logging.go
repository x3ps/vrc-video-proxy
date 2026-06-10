// Package logging holds the process logger setup and the log helpers shared
// across the server's packages.
//
// Logging conventions: all log lines use lowercase snake_case attribute keys and
// pick a level by intent, not by code location:
//
//   - Error — an operation failed and the request cannot proceed
//     (e.g. "extraction failed").
//   - Warn  — degraded or anomalous but still continuing (e.g. a blocked or
//     failing upstream fetch).
//   - Info  — lifecycle and user-facing milestones: server start/stop,
//     "returning playback url".
//   - Debug — high-volume routine detail gated by VRCVP_LOG_LEVEL=debug: the
//     access log, "running yt-dlp", expected client disconnects.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
)

// ParseLevel maps a human-friendly level name to a slog.Level. An empty string
// defaults to info; anything unrecognised is an error so a typo in the configured
// level fails fast at startup rather than silently logging at info.
func ParseLevel(s string) (slog.Level, error) {
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

// New builds the process logger: a text handler at the given level.
func New(w io.Writer, level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

// RedactURL returns rawURL with its query string and fragment masked, so a log
// line records which endpoint was used without leaking signed tokens or
// credentials carried in the query. Unparseable input becomes "<invalid-url>".
func RedactURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		return "<invalid-url>"
	}
	if parsedURL.RawQuery != "" {
		parsedURL.RawQuery = "<redacted>"
	}
	if parsedURL.Fragment != "" {
		parsedURL.Fragment = "<redacted>"
	}
	return parsedURL.String()
}
