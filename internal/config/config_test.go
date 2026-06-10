package config

import (
	"reflect"
	"testing"
	"time"
)

func TestLoadConfigUsesEnvironment(t *testing.T) {
	t.Setenv(envListen, "127.0.0.1:9090")
	t.Setenv(envShutdownTimeout, "7s")

	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.Listen != "127.0.0.1:9090" {
		t.Fatalf("Listen = %q, want %q", cfg.Listen, "127.0.0.1:9090")
	}
	if cfg.ShutdownTimeout != 7*time.Second {
		t.Fatalf("ShutdownTimeout = %s, want 7s", cfg.ShutdownTimeout)
	}
}

func TestLoadConfigFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv(envListen, "127.0.0.1:9090")
	t.Setenv(envShutdownTimeout, "7s")

	cfg, err := LoadConfig([]string{
		"--listen", "127.0.0.1:9091",
		"--shutdown-timeout", "2500ms",
	})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.Listen != "127.0.0.1:9091" {
		t.Fatalf("Listen = %q, want %q", cfg.Listen, "127.0.0.1:9091")
	}
	if cfg.ShutdownTimeout != 2500*time.Millisecond {
		t.Fatalf("ShutdownTimeout = %s, want 2500ms", cfg.ShutdownTimeout)
	}
}

func TestParseDurationAcceptsSecondsWithoutUnit(t *testing.T) {
	duration, err := parseDuration("1.5")
	if err != nil {
		t.Fatalf("parseDuration returned error: %v", err)
	}
	if duration != 1500*time.Millisecond {
		t.Fatalf("duration = %s, want 1500ms", duration)
	}
}

func TestLoadConfigReadsToolEnvironment(t *testing.T) {
	t.Setenv(envYtdlpPath, "/opt/yt-dlp")
	t.Setenv(envCookiesFile, "/secrets/cookies.txt")

	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.YtdlpPath != "/opt/yt-dlp" {
		t.Fatalf("YtdlpPath = %q", cfg.YtdlpPath)
	}
	if cfg.CookiesFile != "/secrets/cookies.txt" {
		t.Fatalf("CookiesFile = %q", cfg.CookiesFile)
	}
}

func TestLoadConfigDefaultsAreSane(t *testing.T) {
	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.YtdlpPath != "yt-dlp" {
		t.Fatalf("YtdlpPath = %q, want yt-dlp", cfg.YtdlpPath)
	}
	if cfg.StreamTTL != defaultStreamTTL {
		t.Fatalf("StreamTTL = %s, want %s", cfg.StreamTTL, defaultStreamTTL)
	}
}

func TestLoadConfigStreamTTL(t *testing.T) {
	t.Setenv(envStreamTTL, "90s")
	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.StreamTTL != 90*time.Second {
		t.Fatalf("StreamTTL from env = %s, want 90s", cfg.StreamTTL)
	}

	cfg, err = LoadConfig([]string{"--stream-ttl", "10m"})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.StreamTTL != 10*time.Minute {
		t.Fatalf("StreamTTL from flag = %s, want 10m", cfg.StreamTTL)
	}
}

func TestLoadConfigLogLevel(t *testing.T) {
	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("default LogLevel = %q, want info", cfg.LogLevel)
	}

	t.Setenv(envLogLevel, "warn")
	cfg, err = LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.LogLevel != "warn" {
		t.Fatalf("LogLevel from env = %q, want warn", cfg.LogLevel)
	}

	cfg, err = LoadConfig([]string{"--log-level", "debug"})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("LogLevel from flag = %q, want debug (flag overrides env)", cfg.LogLevel)
	}
}

func TestLoadConfigRejectsInvalidLogLevel(t *testing.T) {
	if _, err := LoadConfig([]string{"--log-level", "loud"}); err == nil {
		t.Fatal("LoadConfig accepted invalid log level, want error")
	}
}

func TestLoadConfigProxy(t *testing.T) {
	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Proxy != "" {
		t.Fatalf("default Proxy = %q, want empty", cfg.Proxy)
	}

	t.Setenv(envProxy, "http://127.0.0.1:8888")
	cfg, err = LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Proxy != "http://127.0.0.1:8888" {
		t.Fatalf("Proxy from env = %q, want http://127.0.0.1:8888", cfg.Proxy)
	}

	cfg, err = LoadConfig([]string{"--proxy", "socks5://127.0.0.1:1080"})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.Proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("Proxy from flag = %q, want socks5://127.0.0.1:1080 (flag overrides env)", cfg.Proxy)
	}
}

func TestLoadConfigRejectsInvalidProxy(t *testing.T) {
	for _, proxy := range []string{"ftp://127.0.0.1:21", "socks4://127.0.0.1:1080", "http://", "://nohost"} {
		if _, err := LoadConfig([]string{"--proxy", proxy}); err == nil {
			t.Fatalf("LoadConfig accepted invalid proxy %q, want error", proxy)
		}
	}
}

func TestParseProxyURL(t *testing.T) {
	if u, err := ParseProxyURL(""); err != nil || u != nil {
		t.Fatalf("ParseProxyURL(\"\") = %v, %v; want nil, nil", u, err)
	}
	for _, in := range []string{"http://host:8080", "https://host:8080", "socks5://host:1080", "socks5h://host:1080"} {
		if _, err := ParseProxyURL(in); err != nil {
			t.Fatalf("ParseProxyURL(%q) returned error: %v", in, err)
		}
	}
	for _, in := range []string{"ftp://host:21", "socks4://host:1080", "http://", "not a url"} {
		if _, err := ParseProxyURL(in); err == nil {
			t.Fatalf("ParseProxyURL(%q) = nil error, want error", in)
		}
	}
}

func TestLoadConfigYtdlpFormatEnvAndFlag(t *testing.T) {
	t.Setenv(envYtdlpFormat, "best[height<=720]")
	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.YtdlpFormat != "best[height<=720]" {
		t.Fatalf("YtdlpFormat from env = %q, want best[height<=720]", cfg.YtdlpFormat)
	}

	cfg, err = LoadConfig([]string{"--ytdlp-format", "best[height<=480]"})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.YtdlpFormat != "best[height<=480]" {
		t.Fatalf("YtdlpFormat from flag = %q, want best[height<=480]", cfg.YtdlpFormat)
	}
}

func TestLoadConfigYtdlpExtraArgsJSON(t *testing.T) {
	t.Setenv(envYtdlpExtraArgsJSON, `["--add-headers","User-Agent: Mozilla/5.0","--extractor-args","youtube:player_client=android"]`)
	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	want := []string{"--add-headers", "User-Agent: Mozilla/5.0", "--extractor-args", "youtube:player_client=android"}
	if !reflect.DeepEqual(cfg.YtdlpExtraArgs, want) {
		t.Fatalf("YtdlpExtraArgs = %#v, want %#v (spaces preserved as one element)", cfg.YtdlpExtraArgs, want)
	}

	// Flag overrides env, including a header value containing spaces.
	cfg, err = LoadConfig([]string{"--ytdlp-extra-args-json", `["--add-header","Authorization: Bearer token"]`})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	want = []string{"--add-header", "Authorization: Bearer token"}
	if !reflect.DeepEqual(cfg.YtdlpExtraArgs, want) {
		t.Fatalf("YtdlpExtraArgs from flag = %#v, want %#v", cfg.YtdlpExtraArgs, want)
	}
}

func TestLoadConfigRejectsBadYtdlpExtraArgs(t *testing.T) {
	cases := []string{
		`not json`,
		`{"k":"v"}`,       // object, not an array of strings
		`["--ok", ""]`,    // empty element
		`["--ok", "   "]`, // blank element
	}
	for _, in := range cases {
		if _, err := LoadConfig([]string{"--ytdlp-extra-args-json", in}); err == nil {
			t.Fatalf("LoadConfig accepted bad extra args %q, want error", in)
		}
	}
}
