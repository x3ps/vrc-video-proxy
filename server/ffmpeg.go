package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

const defaultFfmpegPath = "ffmpeg"

// h264HardwareEncoders lists hardware H.264 encoders in preference order; the
// first one ffmpeg reports as available is used, falling back to software
// libx264. Probed once and cached (cf. MediaFlow's hw_detect.rs).
var h264HardwareEncoders = []string{
	"h264_nvenc",        // NVIDIA
	"h264_videotoolbox", // Apple
	"h264_vaapi",        // Intel/AMD on Linux (VA-API)
	"h264_qsv",          // Intel QuickSync
	"h264_amf",          // AMD on Windows
}

// ffmpegRunner builds and runs ffmpeg commands by shelling out to the binary
// (the same approach as the yt-dlp wrapper). Mirrors MediaFlow's transcode
// pipeline but fixes two of its shortcomings: ffmpeg is killed when the context
// is cancelled (no orphaned processes), and stderr is captured and logged
// instead of discarded.
type ffmpegRunner struct {
	path   string
	logger *slog.Logger

	encoderOnce sync.Once
	encoder     string
}

func newFfmpegRunner(path string, logger *slog.Logger) *ffmpegRunner {
	if path == "" {
		path = defaultFfmpegPath
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ffmpegRunner{path: path, logger: logger}
}

// h264Encoder returns the preferred H.264 encoder, probing ffmpeg once and
// caching the result for the process lifetime.
func (f *ffmpegRunner) h264Encoder() string {
	f.encoderOnce.Do(func() {
		out, err := exec.Command(f.path, "-hide_banner", "-encoders").Output()
		if err != nil {
			f.encoder = "libx264"
			f.logger.Warn("ffmpeg -encoders probe failed; using libx264", "error", err)
			return
		}
		f.encoder = pickH264Encoder(string(out))
		f.logger.Info("selected H.264 encoder", "encoder", f.encoder)
	})
	return f.encoder
}

// pickH264Encoder scans `ffmpeg -encoders` output for the first available
// hardware encoder, falling back to libx264.
func pickH264Encoder(encodersOutput string) string {
	for _, enc := range h264HardwareEncoders {
		if strings.Contains(encodersOutput, enc) {
			return enc
		}
	}
	return "libx264"
}

// remuxOpener starts a remux/transcode and returns a reader over the MP4 output
// stream plus a wait func that reports the process exit status. It is a function
// type so the job manager can fake ffmpeg in tests.
type remuxOpener func(ctx context.Context, srcURL string, headers map[string]string) (io.ReadCloser, func() error, error)

// openRemux starts ffmpeg remuxing srcURL (an HLS/DASH manifest, or any input
// ffmpeg understands) into a fragmented MP4 stream on stdout, copying codecs
// without re-encoding. Fragmented output (frag_keyframe+empty_moov) is
// streamable as it is produced, so the job can tee it to disk and tail it to the
// player at the same time. Cancelling ctx kills ffmpeg.
func (f *ffmpegRunner) openRemux(ctx context.Context, srcURL string, headers map[string]string) (io.ReadCloser, func() error, error) {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, inputArgs(srcURL, headers)...)
	args = append(args, "-c", "copy", "-movflags", "frag_keyframe+empty_moov", "-f", "mp4", "pipe:1")
	return f.start(ctx, args)
}

// openTranscode is like openRemux but re-encodes video to H.264 (using the
// detected hardware/software encoder) and audio to AAC. Used when the source
// codecs are not MP4-compatible, so the cached file always plays in AVPro.
func (f *ffmpegRunner) openTranscode(ctx context.Context, srcURL string, headers map[string]string) (io.ReadCloser, func() error, error) {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, inputArgs(srcURL, headers)...)
	args = append(args,
		"-c:v", f.h264Encoder(), "-c:a", "aac",
		"-movflags", "frag_keyframe+empty_moov", "-f", "mp4", "pipe:1")
	return f.start(ctx, args)
}

// start launches ffmpeg with args, wiring stdout to the returned reader and
// capturing a bounded tail of stderr for diagnostics.
func (f *ffmpegRunner) start(ctx context.Context, args []string) (io.ReadCloser, func() error, error) {
	cmd := exec.CommandContext(ctx, f.path, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr := &tailBuffer{max: 8 << 10}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	f.logger.Debug("ffmpeg started", "pid", cmd.Process.Pid, "args", strings.Join(args, " "))

	wait := func() error {
		if err := cmd.Wait(); err != nil {
			f.logger.Error("ffmpeg exited with error", "error", err, "stderr", stderr.String())
			return fmt.Errorf("ffmpeg: %w: %s", err, stderr.String())
		}
		return nil
	}
	return stdout, wait, nil
}

// inputArgs builds the input portion of an ffmpeg command, replaying the upstream
// request headers (User-Agent via its dedicated option, the rest via -headers).
func inputArgs(srcURL string, headers map[string]string) []string {
	var args []string
	if ua := headerValue(headers, "User-Agent"); ua != "" {
		args = append(args, "-user_agent", ua)
	}
	if h := formatFFmpegHeaders(headers); h != "" {
		args = append(args, "-headers", h)
	}
	return append(args, "-i", srcURL)
}

// headerValue does a case-insensitive lookup in a single-value header map.
func headerValue(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// formatFFmpegHeaders renders headers (except User-Agent, passed separately) as
// the CRLF-separated string ffmpeg's -headers option expects. Keys are sorted so
// the command is deterministic.
func formatFFmpegHeaders(headers map[string]string) string {
	var keys []string
	for k := range headers {
		if strings.EqualFold(k, "User-Agent") {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\r\n", k, headers[k])
	}
	return b.String()
}

// tailBuffer is an io.Writer that retains only the last max bytes written, giving
// bounded stderr capture for ffmpeg diagnostics.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	return strings.TrimSpace(string(t.buf))
}
