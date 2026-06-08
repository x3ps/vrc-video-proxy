package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

const defaultFfprobePath = "ffprobe"

// ffprobeResult is the subset of ffprobe's `-print_format json -show_format
// -show_streams` output that we care about. ffprobe omits fields with invalid or
// non-applicable values, so all fields are optional and parsed defensively.
type ffprobeResult struct {
	Format  ffprobeFormat   `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

// ffprobeFormat describes the container as a whole.
type ffprobeFormat struct {
	Filename       string `json:"filename"`
	FormatName     string `json:"format_name"`
	FormatLongName string `json:"format_long_name"`
	Duration       string `json:"duration"` // seconds as a string, e.g. "634.512"
	Size           string `json:"size"`     // bytes as a string
	BitRate        string `json:"bit_rate"` // bits/sec as a string
	ProbeScore     int    `json:"probe_score"`
}

// ffprobeStream describes a single media stream (video, audio, subtitle, ...).
type ffprobeStream struct {
	Index     int    `json:"index"`
	CodecName string `json:"codec_name"`
	CodecType string `json:"codec_type"` // "video", "audio", ...
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	BitRate   string `json:"bit_rate"`
	Duration  string `json:"duration"`
}

// ffprobeRunner inspects media by shelling out to the ffprobe binary, mirroring
// the ffmpeg wrapper: ffprobe is killed when the context is cancelled (no
// orphaned processes) and stderr is captured and logged instead of discarded.
// Like ffmpeg, it inherits the process-wide proxy env vars set by configureProxy.
type ffprobeRunner struct {
	path   string
	logger *slog.Logger
}

func newFfprobeRunner(path string, logger *slog.Logger) *ffprobeRunner {
	if path == "" {
		path = defaultFfprobePath
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ffprobeRunner{path: path, logger: logger}
}

// probe runs ffprobe against srcURL and returns the parsed container/stream
// metadata. The upstream request headers are replayed (User-Agent via its
// dedicated option, the rest via -headers) using the same input args as ffmpeg,
// since ffprobe shares libavformat's input options. Cancelling ctx kills ffprobe.
func (f *ffprobeRunner) probe(ctx context.Context, srcURL string, headers map[string]string) (*ffprobeResult, error) {
	args := []string{
		"-hide_banner", "-loglevel", "error",
		"-print_format", "json", "-show_format", "-show_streams",
	}
	args = append(args, inputArgs(srcURL, headers)...)

	cmd := exec.CommandContext(ctx, f.path, args...)
	stderr := &tailBuffer{max: 8 << 10}
	cmd.Stderr = stderr

	f.logger.Debug("ffprobe starting", "args", strings.Join(args, " "))
	stdout, err := cmd.Output()
	if err != nil {
		// Best-effort probe: the caller (decideTranscode) treats any failure as an
		// expected fallback and logs it at Debug, so we must not emit an Error line
		// here. The stderr tail is preserved in the returned error for diagnostics.
		return nil, fmt.Errorf("ffprobe: %w: %s", err, stderr.String())
	}

	var result ffprobeResult
	if err := json.Unmarshal(stdout, &result); err != nil {
		return nil, fmt.Errorf("ffprobe: parse json: %w", err)
	}
	f.logger.Debug("ffprobe completed", "format", result.Format.FormatName, "streams", len(result.Streams))
	return &result, nil
}

// videoStream returns the first video stream, or nil if there is none.
func (r *ffprobeResult) videoStream() *ffprobeStream { return r.firstStream("video") }

// audioStream returns the first audio stream, or nil if there is none.
func (r *ffprobeResult) audioStream() *ffprobeStream { return r.firstStream("audio") }

func (r *ffprobeResult) firstStream(codecType string) *ffprobeStream {
	for i := range r.Streams {
		if r.Streams[i].CodecType == codecType {
			return &r.Streams[i]
		}
	}
	return nil
}

// durationSeconds returns the container duration in seconds, falling back to the
// video then audio stream duration. It returns 0 when no usable value is present.
func (r *ffprobeResult) durationSeconds() float64 {
	if d := parseSeconds(r.Format.Duration); d > 0 {
		return d
	}
	if v := r.videoStream(); v != nil {
		if d := parseSeconds(v.Duration); d > 0 {
			return d
		}
	}
	if a := r.audioStream(); a != nil {
		if d := parseSeconds(a.Duration); d > 0 {
			return d
		}
	}
	return 0
}

// parseSeconds parses ffprobe's string-encoded seconds, treating empty or
// malformed values as 0 rather than an error.
func parseSeconds(value string) float64 {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return seconds
}
