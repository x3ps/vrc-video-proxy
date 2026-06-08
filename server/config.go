package main

import (
	"flag"
	"fmt"
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
	envCookiesFile      = "VRCVP_COOKIES_FILE"
	envSecret           = "VRCVP_SECRET"
	envSegmentCacheTTL  = "VRCVP_SEGMENT_CACHE_TTL"
	envSegmentCacheSize = "VRCVP_SEGMENT_CACHE_SIZE"
	envLogLevel         = "VRCVP_LOG_LEVEL"
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
	CookiesFile      string
	Secret           string
	SegmentCacheTTL  time.Duration
	SegmentCacheSize int
	LogLevel         string
	GameCommand      []string
}

func LoadConfig(args []string) (Config, error) {
	cfg := Config{
		Listen:           "127.0.0.1:8080",
		ShutdownTimeout:  5 * time.Second,
		CacheDir:         defaultCacheDir(),
		CacheMaxSize:     defaultCacheMaxSize,
		YtdlpPath:        "yt-dlp",
		FfmpegPath:       "ffmpeg",
		SegmentCacheTTL:  defaultSegmentCacheTTL,
		SegmentCacheSize: defaultSegmentCacheSize,
		LogLevel:         "info",
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

	var cacheMaxSize string
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address")
	flags.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "graceful shutdown timeout")
	flags.StringVar(&cfg.CacheDir, "cache-dir", cfg.CacheDir, "directory for cached videos")
	flags.StringVar(&cacheMaxSize, "cache-max-size", "", "cache size budget (e.g. 10GB, 500MB, or bytes)")
	flags.StringVar(&cfg.YtdlpPath, "ytdlp-path", cfg.YtdlpPath, "path to the yt-dlp executable")
	flags.StringVar(&cfg.FfmpegPath, "ffmpeg-path", cfg.FfmpegPath, "path to the ffmpeg executable (reserved)")
	flags.StringVar(&cfg.CookiesFile, "cookies-file", cfg.CookiesFile, "optional yt-dlp cookies file")
	flags.StringVar(&cfg.Secret, "secret", cfg.Secret, "secret for signing segment URLs (random per-process if empty)")
	flags.DurationVar(&cfg.SegmentCacheTTL, "segment-cache-ttl", cfg.SegmentCacheTTL, "in-memory HLS/DASH segment cache TTL")
	flags.IntVar(&cfg.SegmentCacheSize, "segment-cache-size", cfg.SegmentCacheSize, "in-memory HLS/DASH segment cache entry count")
	flags.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn or error")
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [options] [-- <game command> [args...]]\n", os.Args[0])
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Options:")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Environment:")
		for _, name := range []string{envListen, envShutdownTimeout, envCacheDir, envCacheMaxSize, envYtdlpPath, envFfmpegPath, envCookiesFile, envSecret, envSegmentCacheTTL, envSegmentCacheSize, envLogLevel} {
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

	return cfg, nil
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
