package main

import (
	"net/url"
	"time"
)

// Logging conventions for the server. All log lines use lowercase snake_case
// attribute keys and pick a level by intent, not by code location:
//
//   - Error — an operation failed and the request cannot proceed
//     (e.g. "extraction failed", "download job failed", "ffmpeg exited with error").
//   - Warn  — degraded or anomalous but still continuing: the stall/slow
//     detectors below, "fill range failed/slow", "range not satisfiable".
//   - Info  — lifecycle and user-facing milestones: server start/stop,
//     "download job started/finished", "returning playback url", encoder choice.
//   - Debug — high-volume routine detail gated by VRCVP_LOG_LEVEL=debug: the
//     access log, periodic throughput progress, routing/"extracted stream",
//     the direct-media short-circuit, per-range fill timing, expected client
//     disconnects.
//
// Common attribute vocabulary: id, mode, bytes, content_length, file_pct,
// range_pct, bytes_per_sec, duration_ms, http_range, start, end, pos, error,
// source, cache, url, original_url, remux.

const (
	// progressLogInterval is how often a download emits a debug throughput line.
	progressLogInterval = 30 * time.Second
	// slowFirstByteThreshold warns when no byte has been written/served yet.
	slowFirstByteThreshold = 5 * time.Second
	// slowClientWaitThreshold warns when a client seek waits this long for bytes.
	slowClientWaitThreshold = 5 * time.Second
	// slowFillThreshold warns when a single upstream range fetch is this slow.
	slowFillThreshold = 15 * time.Second
)

// bytesPerSecond is a defensive throughput helper: it returns 0 rather than
// dividing by a zero/negative duration or counting negative byte deltas.
func bytesPerSecond(bytes int64, duration time.Duration) int64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return int64(float64(bytes) / duration.Seconds())
}

// percentOfFile reports pos as a percentage of size, guarding against unknown
// (<= 0) sizes so a log line never shows a bogus or NaN percentage.
func percentOfFile(pos, size int64) float64 {
	if pos <= 0 || size <= 0 {
		return 0
	}
	return float64(pos) * 100 / float64(size)
}

// rateTracker computes byte throughput between successive samples. Each call to
// rate advances the tracker and returns the bytes/sec observed since the previous
// call, so the periodic progress loggers share one rate calculation instead of
// each re-deriving delta/elapsed/clamp.
type rateTracker struct {
	last  time.Time
	bytes int64
}

func (r *rateTracker) rate(now time.Time, total int64) int64 {
	delta := total - r.bytes
	if delta < 0 {
		delta = 0
	}
	bps := bytesPerSecond(delta, now.Sub(r.last))
	r.last = now
	r.bytes = total
	return bps
}

// redactURLForLog returns rawURL with its query string and fragment masked, so a
// log line records which endpoint was used without leaking signed tokens or
// credentials carried in the query. Unparseable input becomes "<invalid-url>".
// Intentionally duplicated in the server and wrapper binaries (they share no
// package); keep the two copies identical.
func redactURLForLog(rawURL string) string {
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
