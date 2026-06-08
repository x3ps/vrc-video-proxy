package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sort"
	"strings"
)

const defaultFfmpegPath = "ffmpeg"

// hwBackend names the FFmpeg hardware-acceleration backend for the H.264 transcode
// path. Hardware acceleration is an explicit configuration choice (cf.
// VRCVP_FFMPEG_HWACCEL); the default is software libx264. We never enable a
// hardware encoder just because the ffmpeg build happens to expose it, since some
// backends (notably VAAPI) need extra device/filter setup to produce a valid command.
type hwBackend string

const (
	hwSoftware     hwBackend = "software"
	hwNVENC        hwBackend = "nvenc"
	hwVAAPI        hwBackend = "vaapi"
	hwQSV          hwBackend = "qsv"
	hwVideoToolbox hwBackend = "videotoolbox"
	hwAMF          hwBackend = "amf"
)

// h264Backend describes how to build the H.264 transcode command for one backend.
// Software (libx264) uses preset/CRF rate control; hardware backends use a target
// bitrate. VAAPI is the only backend that needs a device plus a GPU upload filter
// chain, so its command is structurally different (verified against the FFmpeg
// Hardware/VAAPI wiki): -vaapi_device DEV ... -vf format=nv12,hwupload -c:v h264_vaapi.
type h264Backend struct {
	encoder  string // -c:v value
	software bool   // preset/CRF rate control instead of a target bitrate
	pixFmt   string // -pix_fmt value; "" when a filter chain sets the format (VAAPI)
	vaapi    bool   // needs -vaapi_device DEV + -vf format=nv12,hwupload
}

// h264Backends maps each accepted backend to its descriptor. The keys double as the
// set of valid VRCVP_FFMPEG_HWACCEL values (plus the "libx264" alias for software).
var h264Backends = map[hwBackend]h264Backend{
	hwSoftware:     {encoder: "libx264", software: true, pixFmt: "yuv420p"},
	hwNVENC:        {encoder: "h264_nvenc", pixFmt: "yuv420p"},
	hwVAAPI:        {encoder: "h264_vaapi", vaapi: true},
	hwQSV:          {encoder: "h264_qsv", pixFmt: "nv12"},
	hwVideoToolbox: {encoder: "h264_videotoolbox", pixFmt: "yuv420p"},
	hwAMF:          {encoder: "h264_amf", pixFmt: "yuv420p"},
}

// ffmpegRunner builds and runs ffmpeg commands by shelling out to the binary
// (the same approach as the yt-dlp wrapper). Mirrors MediaFlow's transcode
// pipeline but fixes two of its shortcomings: ffmpeg is killed when the context
// is cancelled (no orphaned processes), and stderr is captured and logged
// instead of discarded.
type ffmpegRunner struct {
	path   string
	logger *slog.Logger
	opts   transcodeOptions
}

// transcodeOptions holds the configurable re-encode parameters consumed by
// openTranscode. Empty fields fall back to encoder defaults (or, for hardware
// encoders, a target bitrate), so a zero value still yields a sensible transcode.
type transcodeOptions struct {
	backend      hwBackend // H.264 acceleration backend; empty means software
	hwDevice     string    // hardware device, e.g. /dev/dri/renderD128 (VAAPI)
	preset       string    // libx264 -preset; ignored by hardware encoders
	crf          string    // libx264 -crf; ignored by hardware encoders
	videoBitrate string    // -b:v target
	maxrate      string    // -maxrate cap
	bufsize      string    // -bufsize for the rate cap
	audioBitrate string    // -b:a target
}

func newFfmpegRunner(path string, logger *slog.Logger, opts transcodeOptions) *ffmpegRunner {
	if path == "" {
		path = defaultFfmpegPath
	}
	if logger == nil {
		logger = slog.Default()
	}
	if opts.backend == "" {
		opts.backend = hwSoftware
	}
	return &ffmpegRunner{path: path, logger: logger, opts: opts}
}

// backend resolves the configured backend descriptor, falling back to software.
func (f *ffmpegRunner) backend() h264Backend {
	if b, ok := h264Backends[f.opts.backend]; ok {
		return b
	}
	return h264Backends[hwSoftware]
}

// validateBackend confirms the configured hardware encoder is present in this
// ffmpeg build, by probing `ffmpeg -encoders`. Software (libx264) is always
// available and needs no probe. Used as a fatal startup check so a misconfigured
// hardware backend fails fast instead of breaking the first transcode.
func (f *ffmpegRunner) validateBackend() error {
	b := f.backend()
	if b.software {
		return nil
	}
	out, err := exec.Command(f.path, "-hide_banner", "-encoders").Output()
	if err != nil {
		return fmt.Errorf("ffmpeg -encoders probe failed: %w", err)
	}
	if !strings.Contains(string(out), b.encoder) {
		return fmt.Errorf("ffmpeg hwaccel %q requires encoder %q, which this ffmpeg build does not provide", f.opts.backend, b.encoder)
	}
	return nil
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
	return f.start(ctx, f.transcodeArgs(srcURL, headers))
}

// transcodeArgs builds the full ffmpeg argument vector for a re-encode to H.264 +
// AAC fragmented MP4 on stdout. Kept as a pure function (no process launch) so the
// per-backend command shape is unit-testable.
func (f *ffmpegRunner) transcodeArgs(srcURL string, headers map[string]string) []string {
	b := f.backend()
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	// VAAPI needs its device initialised before the input is opened.
	if b.vaapi {
		args = append(args, "-vaapi_device", f.opts.hwDevice)
	}
	args = append(args, inputArgs(srcURL, headers)...)
	args = append(args, f.videoEncodeArgs(b)...)
	args = append(args, "-c:a", "aac")
	if f.opts.audioBitrate != "" {
		args = append(args, "-b:a", f.opts.audioBitrate)
	}
	args = append(args, "-movflags", "frag_keyframe+empty_moov", "-f", "mp4", "pipe:1")
	return args
}

// videoEncodeArgs builds the video-codec portion of a transcode command for the
// given backend. Software libx264 uses preset + capped CRF (the recommended
// streaming rate control); hardware encoders, whose preset/CRF semantics differ,
// use a target bitrate instead (defaulting to 8M when none is configured). The
// optional -maxrate/-bufsize cap applies to both. The output pixel format is then
// pinned: most backends emit a software -pix_fmt (yuv420p restricts output to the
// chroma subsampling AVPro and most players can decode; nv12 for QSV), while VAAPI
// instead converts and uploads to a GPU surface via a filter chain, as its encoder
// only accepts VAAPI surfaces.
func (f *ffmpegRunner) videoEncodeArgs(b h264Backend) []string {
	args := []string{"-c:v", b.encoder}
	if b.software {
		if f.opts.preset != "" {
			args = append(args, "-preset", f.opts.preset)
		}
		switch {
		case f.opts.videoBitrate != "":
			args = append(args, "-b:v", f.opts.videoBitrate)
		case f.opts.crf != "":
			args = append(args, "-crf", f.opts.crf)
		}
	} else {
		vb := f.opts.videoBitrate
		if vb == "" {
			vb = "8M"
		}
		args = append(args, "-b:v", vb)
	}
	if f.opts.maxrate != "" {
		args = append(args, "-maxrate", f.opts.maxrate)
	}
	if f.opts.bufsize != "" {
		args = append(args, "-bufsize", f.opts.bufsize)
	}
	if b.vaapi {
		args = append(args, "-vf", "format=nv12,hwupload")
	} else if b.pixFmt != "" {
		args = append(args, "-pix_fmt", b.pixFmt)
	}
	return args
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
