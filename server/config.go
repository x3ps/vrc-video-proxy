package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	envListen           = "VRCVP_LISTEN"
	envShutdownTimeout  = "VRCVP_SHUTDOWN_TIMEOUT"
	envCacheDir         = "VRCVP_CACHE_DIR"
	envCacheMaxSize     = "VRCVP_CACHE_MAX_SIZE"
	envYtdlpPath        = "VRCVP_YTDLP_PATH"
	envFfmpegPath       = "VRCVP_FFMPEG_PATH"
	envFfprobePath      = "VRCVP_FFPROBE_PATH"
	envCookiesFile      = "VRCVP_COOKIES_FILE"
	envSecret           = "VRCVP_SECRET"
	envSegmentCacheTTL  = "VRCVP_SEGMENT_CACHE_TTL"
	envSegmentCacheSize = "VRCVP_SEGMENT_CACHE_SIZE"
	envLogLevel         = "VRCVP_LOG_LEVEL"
	envProxy            = "VRCVP_PROXY"

	envYtdlpFormat        = "VRCVP_YTDLP_FORMAT"
	envYtdlpExtraArgsJSON = "VRCVP_YTDLP_EXTRA_ARGS_JSON"

	envFfmpegHWAccel = "VRCVP_FFMPEG_HWACCEL"
	envFfmpegHWDevice = "VRCVP_FFMPEG_HW_DEVICE"

	envTranscodePreset       = "VRCVP_TRANSCODE_PRESET"
	envTranscodeCRF          = "VRCVP_TRANSCODE_CRF"
	envTranscodeVideoBitrate = "VRCVP_TRANSCODE_VIDEO_BITRATE"
	envTranscodeMaxrate      = "VRCVP_TRANSCODE_MAXRATE"
	envTranscodeBufsize      = "VRCVP_TRANSCODE_BUFSIZE"
	envTranscodeAudioBitrate = "VRCVP_TRANSCODE_AUDIO_BITRATE"
)

// defaultCacheMaxSize is the default on-disk cache budget (~10 GiB).
const defaultCacheMaxSize int64 = 10 << 30

// Defaults for the in-memory segment/manifest cache used by the HLS/DASH paths.
const (
	defaultSegmentCacheTTL  = 5 * time.Minute
	defaultSegmentCacheSize = 512
)

type Config struct {
	Listen           string
	ShutdownTimeout  time.Duration
	CacheDir         string
	CacheMaxSize     int64
	YtdlpPath        string
	FfmpegPath       string
	FfprobePath      string
	CookiesFile      string
	Secret           string
	SegmentCacheTTL  time.Duration
	SegmentCacheSize int
	LogLevel         string
	Proxy            string
	GameCommand      []string

	// yt-dlp tuning.
	YtdlpFormat    string   // -f selector override; defaults to progressiveFormat
	YtdlpExtraArgs []string // passthrough yt-dlp flags (JSON array, spaces preserved)

	// FFmpeg hardware acceleration for the transcode path. The default is software
	// libx264; a hardware backend is opt-in. FfmpegBackend is the normalised form of
	// FfmpegHWAccel, resolved by LoadConfig. FfmpegHWDevice is required by VAAPI.
	FfmpegHWAccel  string    // raw VRCVP_FFMPEG_HWACCEL value (for diagnostics/usage)
	FfmpegHWDevice string    // hardware device, e.g. /dev/dri/renderD128 (VAAPI)
	FfmpegBackend  hwBackend // normalised backend consumed by the ffmpeg runner

	// Transcoding parameters. These configure the re-encode path
	// (openTranscode); the stream-copy remux path is unaffected. Empty values
	// fall back to encoder defaults / the in-code best-practice defaults.
	TranscodePreset       string // libx264 -preset (ignored by hardware encoders)
	TranscodeCRF          string // libx264 -crf (ignored by hardware encoders)
	TranscodeVideoBitrate string // -b:v target (hardware encoders default to 8M)
	TranscodeMaxrate      string // -maxrate cap
	TranscodeBufsize      string // -bufsize for the rate cap
	TranscodeAudioBitrate string // -b:a target
}

func LoadConfig(args []string) (Config, error) {
	cfg := Config{
		Listen:           "127.0.0.1:8080",
		ShutdownTimeout:  5 * time.Second,
		CacheDir:         defaultCacheDir(),
		CacheMaxSize:     defaultCacheMaxSize,
		YtdlpPath:        defaultYtdlpPath,
		FfmpegPath:       defaultFfmpegPath,
		FfprobePath:      defaultFfprobePath,
		SegmentCacheTTL:  defaultSegmentCacheTTL,
		SegmentCacheSize: defaultSegmentCacheSize,
		LogLevel:         "info",

		YtdlpFormat:           progressiveFormat,
		FfmpegHWAccel:         string(hwSoftware),
		TranscodePreset:       "veryfast",
		TranscodeCRF:          "23",
		TranscodeMaxrate:      "8M",
		TranscodeBufsize:      "16M",
		TranscodeAudioBitrate: "192k",
	}

	if value := strings.TrimSpace(os.Getenv(envListen)); value != "" {
		cfg.Listen = value
	}
	if value := strings.TrimSpace(os.Getenv(envShutdownTimeout)); value != "" {
		timeout, err := parseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envShutdownTimeout, err)
		}
		cfg.ShutdownTimeout = timeout
	}
	if value := strings.TrimSpace(os.Getenv(envCacheDir)); value != "" {
		cfg.CacheDir = value
	}
	if value := strings.TrimSpace(os.Getenv(envCacheMaxSize)); value != "" {
		size, err := parseSize(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envCacheMaxSize, err)
		}
		cfg.CacheMaxSize = size
	}
	if value := strings.TrimSpace(os.Getenv(envYtdlpPath)); value != "" {
		cfg.YtdlpPath = value
	}
	if value := strings.TrimSpace(os.Getenv(envFfmpegPath)); value != "" {
		cfg.FfmpegPath = value
	}
	if value := strings.TrimSpace(os.Getenv(envFfprobePath)); value != "" {
		cfg.FfprobePath = value
	}
	if value := strings.TrimSpace(os.Getenv(envCookiesFile)); value != "" {
		cfg.CookiesFile = value
	}
	if value := strings.TrimSpace(os.Getenv(envSecret)); value != "" {
		cfg.Secret = value
	}
	if value := strings.TrimSpace(os.Getenv(envSegmentCacheTTL)); value != "" {
		ttl, err := parseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envSegmentCacheTTL, err)
		}
		cfg.SegmentCacheTTL = ttl
	}
	if value := strings.TrimSpace(os.Getenv(envSegmentCacheSize)); value != "" {
		size, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envSegmentCacheSize, err)
		}
		cfg.SegmentCacheSize = size
	}
	if value := strings.TrimSpace(os.Getenv(envLogLevel)); value != "" {
		cfg.LogLevel = value
	}
	if value := strings.TrimSpace(os.Getenv(envProxy)); value != "" {
		cfg.Proxy = value
	}
	if value := strings.TrimSpace(os.Getenv(envYtdlpFormat)); value != "" {
		cfg.YtdlpFormat = value
	}
	if value := strings.TrimSpace(os.Getenv(envYtdlpExtraArgsJSON)); value != "" {
		extra, err := parseExtraArgsJSON(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envYtdlpExtraArgsJSON, err)
		}
		cfg.YtdlpExtraArgs = extra
	}
	if value := strings.TrimSpace(os.Getenv(envFfmpegHWAccel)); value != "" {
		cfg.FfmpegHWAccel = value
	}
	if value := strings.TrimSpace(os.Getenv(envFfmpegHWDevice)); value != "" {
		cfg.FfmpegHWDevice = value
	}
	if value := strings.TrimSpace(os.Getenv(envTranscodePreset)); value != "" {
		cfg.TranscodePreset = value
	}
	if value := strings.TrimSpace(os.Getenv(envTranscodeCRF)); value != "" {
		cfg.TranscodeCRF = value
	}
	if value := strings.TrimSpace(os.Getenv(envTranscodeVideoBitrate)); value != "" {
		cfg.TranscodeVideoBitrate = value
	}
	if value := strings.TrimSpace(os.Getenv(envTranscodeMaxrate)); value != "" {
		cfg.TranscodeMaxrate = value
	}
	if value := strings.TrimSpace(os.Getenv(envTranscodeBufsize)); value != "" {
		cfg.TranscodeBufsize = value
	}
	if value := strings.TrimSpace(os.Getenv(envTranscodeAudioBitrate)); value != "" {
		cfg.TranscodeAudioBitrate = value
	}

	var cacheMaxSize string
	var ytdlpExtraArgsJSON string
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address")
	flags.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "graceful shutdown timeout")
	flags.StringVar(&cfg.CacheDir, "cache-dir", cfg.CacheDir, "directory for cached videos")
	flags.StringVar(&cacheMaxSize, "cache-max-size", "", "cache size budget (e.g. 10GB, 500MB, or bytes)")
	flags.StringVar(&cfg.YtdlpPath, "ytdlp-path", cfg.YtdlpPath, "path to the yt-dlp executable")
	flags.StringVar(&cfg.FfmpegPath, "ffmpeg-path", cfg.FfmpegPath, "path to the ffmpeg executable")
	flags.StringVar(&cfg.FfprobePath, "ffprobe-path", cfg.FfprobePath, "path to the ffprobe executable")
	flags.StringVar(&cfg.CookiesFile, "cookies-file", cfg.CookiesFile, "optional yt-dlp cookies file")
	flags.StringVar(&cfg.Secret, "secret", cfg.Secret, "secret for signing segment URLs (random per-process if empty)")
	flags.DurationVar(&cfg.SegmentCacheTTL, "segment-cache-ttl", cfg.SegmentCacheTTL, "in-memory HLS/DASH segment cache TTL")
	flags.IntVar(&cfg.SegmentCacheSize, "segment-cache-size", cfg.SegmentCacheSize, "in-memory HLS/DASH segment cache entry count")
	flags.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn or error")
	flags.StringVar(&cfg.Proxy, "proxy", cfg.Proxy, "proxy for all upstream traffic and tools, e.g. http://host:port or socks5://host:port")
	flags.StringVar(&cfg.YtdlpFormat, "ytdlp-format", cfg.YtdlpFormat, "yt-dlp -f format selector")
	flags.StringVar(&ytdlpExtraArgsJSON, "ytdlp-extra-args-json", "", `extra yt-dlp flags as a JSON array, e.g. ["--add-headers","User-Agent: Mozilla/5.0"]`)
	flags.StringVar(&cfg.FfmpegHWAccel, "ffmpeg-hwaccel", cfg.FfmpegHWAccel, "ffmpeg H.264 hardware acceleration: software (default), nvenc, vaapi, qsv, videotoolbox or amf")
	flags.StringVar(&cfg.FfmpegHWDevice, "ffmpeg-hw-device", cfg.FfmpegHWDevice, "hardware device for ffmpeg acceleration, required by vaapi, e.g. /dev/dri/renderD128")
	flags.StringVar(&cfg.TranscodePreset, "transcode-preset", cfg.TranscodePreset, "libx264 preset for transcoding (ignored by hardware encoders)")
	flags.StringVar(&cfg.TranscodeCRF, "transcode-crf", cfg.TranscodeCRF, "libx264 CRF quality for transcoding (ignored by hardware encoders)")
	flags.StringVar(&cfg.TranscodeVideoBitrate, "transcode-video-bitrate", cfg.TranscodeVideoBitrate, "target video bitrate for transcoding, e.g. 8M (hardware encoders default to 8M)")
	flags.StringVar(&cfg.TranscodeMaxrate, "transcode-maxrate", cfg.TranscodeMaxrate, "video rate cap for transcoding, e.g. 8M")
	flags.StringVar(&cfg.TranscodeBufsize, "transcode-bufsize", cfg.TranscodeBufsize, "rate-control buffer size for transcoding, e.g. 16M")
	flags.StringVar(&cfg.TranscodeAudioBitrate, "transcode-audio-bitrate", cfg.TranscodeAudioBitrate, "target audio bitrate for transcoding, e.g. 192k")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [options] [-- <game command> [args...]]\n", os.Args[0])
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Options:")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Environment:")
		for _, name := range []string{envListen, envShutdownTimeout, envCacheDir, envCacheMaxSize, envYtdlpPath, envFfmpegPath, envFfprobePath, envCookiesFile, envSecret, envSegmentCacheTTL, envSegmentCacheSize, envLogLevel, envProxy, envYtdlpFormat, envYtdlpExtraArgsJSON, envFfmpegHWAccel, envFfmpegHWDevice, envTranscodePreset, envTranscodeCRF, envTranscodeVideoBitrate, envTranscodeMaxrate, envTranscodeBufsize, envTranscodeAudioBitrate} {
			fmt.Fprintf(flags.Output(), "  %s\n", name)
		}
	}

	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if value := strings.TrimSpace(cacheMaxSize); value != "" {
		size, err := parseSize(value)
		if err != nil {
			return Config{}, fmt.Errorf("cache-max-size: %w", err)
		}
		cfg.CacheMaxSize = size
	}
	if value := strings.TrimSpace(ytdlpExtraArgsJSON); value != "" {
		extra, err := parseExtraArgsJSON(value)
		if err != nil {
			return Config{}, fmt.Errorf("ytdlp-extra-args-json: %w", err)
		}
		cfg.YtdlpExtraArgs = extra
	}
	cfg.GameCommand = flags.Args()

	if strings.TrimSpace(cfg.Listen) == "" {
		return Config{}, fmt.Errorf("listen address must not be empty")
	}
	if cfg.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("shutdown timeout must be greater than zero")
	}
	if strings.TrimSpace(cfg.CacheDir) == "" {
		return Config{}, fmt.Errorf("cache directory must not be empty")
	}
	if cfg.CacheMaxSize <= 0 {
		return Config{}, fmt.Errorf("cache max size must be greater than zero")
	}
	if cfg.SegmentCacheSize < 0 {
		return Config{}, fmt.Errorf("segment cache size must not be negative")
	}
	if cfg.SegmentCacheTTL < 0 {
		return Config{}, fmt.Errorf("segment cache ttl must not be negative")
	}
	if _, err := parseLogLevel(cfg.LogLevel); err != nil {
		return Config{}, err
	}
	if _, err := parseProxyURL(cfg.Proxy); err != nil {
		return Config{}, err
	}
	backend, err := normalizeHWBackend(cfg.FfmpegHWAccel)
	if err != nil {
		return Config{}, err
	}
	if backend == hwVAAPI && strings.TrimSpace(cfg.FfmpegHWDevice) == "" {
		return Config{}, fmt.Errorf("%s=vaapi requires a device (set %s, e.g. /dev/dri/renderD128)", envFfmpegHWAccel, envFfmpegHWDevice)
	}
	cfg.FfmpegBackend = backend

	return cfg, nil
}

// normalizeHWBackend maps a user-supplied VRCVP_FFMPEG_HWACCEL value to a known
// backend. Empty and "libx264" are accepted aliases for software; matching is
// case-insensitive. Unknown values are rejected with the list of valid options.
func normalizeHWBackend(value string) (hwBackend, error) {
	switch v := strings.ToLower(strings.TrimSpace(value)); v {
	case "", "software", "libx264":
		return hwSoftware, nil
	default:
		if _, ok := h264Backends[hwBackend(v)]; ok {
			return hwBackend(v), nil
		}
		return "", fmt.Errorf("unsupported %s %q (want software, nvenc, vaapi, qsv, videotoolbox or amf)", envFfmpegHWAccel, value)
	}
}

// parseExtraArgsJSON parses a JSON array of yt-dlp passthrough flags. JSON is used
// (rather than whitespace splitting) so values containing spaces, such as
// "User-Agent: Mozilla/5.0", survive as a single argv element. Element values are
// preserved verbatim; only empty elements are rejected.
func parseExtraArgsJSON(value string) ([]string, error) {
	var args []string
	if err := json.Unmarshal([]byte(value), &args); err != nil {
		return nil, fmt.Errorf("invalid JSON array: %w", err)
	}
	for i, a := range args {
		if strings.TrimSpace(a) == "" {
			return nil, fmt.Errorf("extra arg %d must not be empty", i)
		}
	}
	return args, nil
}

// parseProxyURL parses and validates an upstream proxy URL. An empty value means
// "no proxy" and returns (nil, nil). Only the schemes honored by all three egress
// paths (Go's http.Transport, yt-dlp and ffmpeg) are accepted.
func parseProxyURL(value string) (*url.URL, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL %q: %w", value, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (want http, https, socks5 or socks5h)", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("proxy URL %q must include a host", value)
	}
	return u, nil
}

// defaultCacheDir returns the default cache location, preferring the user cache
// directory and falling back to a temp-dir path when it is unavailable.
func defaultCacheDir() string {
	if dir, err := os.UserCacheDir(); err == nil {
		return filepath.Join(dir, "vrc-video-proxy")
	}
	return filepath.Join(os.TempDir(), "vrc-video-proxy")
}

func parseDuration(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err == nil {
		return duration, nil
	}

	seconds, secondsErr := strconv.ParseFloat(value, 64)
	if secondsErr != nil {
		return 0, err
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// sizeUnits maps a (case-insensitive) suffix to its multiplier. Both decimal
// (KB/MB/GB) and binary (KiB/MiB/GiB) suffixes are accepted and treated as
// powers of 1024, which is the conventional meaning for disk cache budgets.
var sizeUnits = []struct {
	suffix string
	mult   int64
}{
	{"GIB", 1 << 30},
	{"GB", 1 << 30},
	{"G", 1 << 30},
	{"MIB", 1 << 20},
	{"MB", 1 << 20},
	{"M", 1 << 20},
	{"KIB", 1 << 10},
	{"KB", 1 << 10},
	{"K", 1 << 10},
	{"B", 1},
}

// parseSize parses a human-friendly byte size such as "10GB", "500MB", "1.5G"
// or a plain byte count like "1073741824".
func parseSize(value string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("empty size")
	}

	upper := strings.ToUpper(trimmed)
	for _, unit := range sizeUnits {
		if !strings.HasSuffix(upper, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(upper[:len(upper)-len(unit.suffix)])
		if number == "" {
			continue
		}
		amount, err := strconv.ParseFloat(number, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid size %q: %w", value, err)
		}
		if amount < 0 {
			return 0, fmt.Errorf("size must not be negative: %q", value)
		}
		return int64(amount * float64(unit.mult)), nil
	}

	bytes, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %w", value, err)
	}
	if bytes < 0 {
		return 0, fmt.Errorf("size must not be negative: %q", value)
	}
	return bytes, nil
}
