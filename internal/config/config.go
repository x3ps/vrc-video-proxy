// Package config parses the server's configuration from environment variables
// and command-line flags (flags override env, which override defaults).
package config

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"vrc-video-proxy/internal/logging"
)

const (
	envListen          = "VRCVP_LISTEN"
	envShutdownTimeout = "VRCVP_SHUTDOWN_TIMEOUT"
	envYtdlpPath       = "VRCVP_YTDLP_PATH"
	envCookiesFile     = "VRCVP_COOKIES_FILE"
	envLogLevel        = "VRCVP_LOG_LEVEL"
	envProxy           = "VRCVP_PROXY"
	envStreamTTL       = "VRCVP_STREAM_TTL"

	envYtdlpFormat        = "VRCVP_YTDLP_FORMAT"
	envYtdlpExtraArgsJSON = "VRCVP_YTDLP_EXTRA_ARGS_JSON"
)

// defaultYtdlpPath is the binary name looked up on PATH when none is configured.
const defaultYtdlpPath = "yt-dlp"

// defaultStreamTTL bounds how long a resolved upstream URL stays addressable
// through the proxy after /api/getvideo; the TTL slides on every access so an
// actively-playing/seeking client keeps it alive.
const defaultStreamTTL = 30 * time.Minute

// Config is the resolved server configuration. The proxy resolves a source URL
// with yt-dlp and streams the resulting bytes back to the player; there is no
// caching or transcoding, so the knobs here are limited to the listener, the
// yt-dlp invocation, the upstream proxy and the stream-handle lifetime.
type Config struct {
	Listen          string
	ShutdownTimeout time.Duration
	YtdlpPath       string
	CookiesFile     string
	LogLevel        string
	Proxy           string
	StreamTTL       time.Duration

	// yt-dlp tuning.
	YtdlpFormat    string   // -f selector override; empty falls back to the extractor default
	YtdlpExtraArgs []string // passthrough yt-dlp flags (JSON array, spaces preserved)
}

func LoadConfig(args []string) (Config, error) {
	cfg := Config{
		Listen:          "127.0.0.1:8080",
		ShutdownTimeout: 5 * time.Second,
		YtdlpPath:       defaultYtdlpPath,
		LogLevel:        "info",
		StreamTTL:       defaultStreamTTL,
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
	if value := strings.TrimSpace(os.Getenv(envYtdlpPath)); value != "" {
		cfg.YtdlpPath = value
	}
	if value := strings.TrimSpace(os.Getenv(envCookiesFile)); value != "" {
		cfg.CookiesFile = value
	}
	if value := strings.TrimSpace(os.Getenv(envLogLevel)); value != "" {
		cfg.LogLevel = value
	}
	if value := strings.TrimSpace(os.Getenv(envProxy)); value != "" {
		cfg.Proxy = value
	}
	if value := strings.TrimSpace(os.Getenv(envStreamTTL)); value != "" {
		ttl, err := parseDuration(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", envStreamTTL, err)
		}
		cfg.StreamTTL = ttl
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

	var ytdlpExtraArgsJSON string
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.StringVar(&cfg.Listen, "listen", cfg.Listen, "HTTP listen address")
	flags.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "graceful shutdown timeout")
	flags.StringVar(&cfg.YtdlpPath, "ytdlp-path", cfg.YtdlpPath, "path to the yt-dlp executable")
	flags.StringVar(&cfg.CookiesFile, "cookies-file", cfg.CookiesFile, "optional yt-dlp cookies file")
	flags.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn or error")
	flags.StringVar(&cfg.Proxy, "proxy", cfg.Proxy, "proxy for upstream traffic and yt-dlp, e.g. http://host:port or socks5://host:port")
	flags.DurationVar(&cfg.StreamTTL, "stream-ttl", cfg.StreamTTL, "how long a resolved stream handle stays valid (sliding)")
	flags.StringVar(&cfg.YtdlpFormat, "ytdlp-format", cfg.YtdlpFormat, "yt-dlp -f format selector")
	flags.StringVar(&ytdlpExtraArgsJSON, "ytdlp-extra-args-json", "", `extra yt-dlp flags as a JSON array, e.g. ["--add-headers","User-Agent: Mozilla/5.0"]`)
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: %s [options]\n", os.Args[0])
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Options:")
		flags.PrintDefaults()
		fmt.Fprintln(flags.Output())
		fmt.Fprintln(flags.Output(), "Environment:")
		for _, name := range []string{envListen, envShutdownTimeout, envYtdlpPath, envCookiesFile, envLogLevel, envProxy, envStreamTTL, envYtdlpFormat, envYtdlpExtraArgsJSON} {
			fmt.Fprintf(flags.Output(), "  %s\n", name)
		}
	}

	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	if value := strings.TrimSpace(ytdlpExtraArgsJSON); value != "" {
		extra, err := parseExtraArgsJSON(value)
		if err != nil {
			return Config{}, fmt.Errorf("ytdlp-extra-args-json: %w", err)
		}
		cfg.YtdlpExtraArgs = extra
	}

	if strings.TrimSpace(cfg.Listen) == "" {
		return Config{}, fmt.Errorf("listen address must not be empty")
	}
	if cfg.ShutdownTimeout <= 0 {
		return Config{}, fmt.Errorf("shutdown timeout must be greater than zero")
	}
	if cfg.StreamTTL <= 0 {
		return Config{}, fmt.Errorf("stream ttl must be greater than zero")
	}
	if _, err := logging.ParseLevel(cfg.LogLevel); err != nil {
		return Config{}, err
	}
	if _, err := ParseProxyURL(cfg.Proxy); err != nil {
		return Config{}, err
	}

	return cfg, nil
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

// ParseProxyURL parses and validates an upstream proxy URL. An empty value means
// "no proxy" and returns (nil, nil). Only the schemes honored by both egress
// paths (Go's http.Transport and yt-dlp) are accepted.
func ParseProxyURL(value string) (*url.URL, error) {
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
