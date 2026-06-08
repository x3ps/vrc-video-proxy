package main

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

func TestLoadConfigReadsCacheAndToolEnvironment(t *testing.T) {
	t.Setenv(envCacheDir, "/tmp/vrcvp-cache")
	t.Setenv(envCacheMaxSize, "2GB")
	t.Setenv(envYtdlpPath, "/opt/yt-dlp")
	t.Setenv(envFfmpegPath, "/opt/ffmpeg")
	t.Setenv(envCookiesFile, "/secrets/cookies.txt")

	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.CacheDir != "/tmp/vrcvp-cache" {
		t.Fatalf("CacheDir = %q", cfg.CacheDir)
	}
	if cfg.CacheMaxSize != 2<<30 {
		t.Fatalf("CacheMaxSize = %d, want %d", cfg.CacheMaxSize, 2<<30)
	}
	if cfg.YtdlpPath != "/opt/yt-dlp" {
		t.Fatalf("YtdlpPath = %q", cfg.YtdlpPath)
	}
	if cfg.FfmpegPath != "/opt/ffmpeg" {
		t.Fatalf("FfmpegPath = %q", cfg.FfmpegPath)
	}
	if cfg.CookiesFile != "/secrets/cookies.txt" {
		t.Fatalf("CookiesFile = %q", cfg.CookiesFile)
	}
}

func TestLoadConfigFlagsOverrideCacheEnvironment(t *testing.T) {
	t.Setenv(envCacheDir, "/tmp/env-cache")
	t.Setenv(envCacheMaxSize, "2GB")

	cfg, err := LoadConfig([]string{
		"--cache-dir", "/tmp/flag-cache",
		"--cache-max-size", "512MB",
	})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	if cfg.CacheDir != "/tmp/flag-cache" {
		t.Fatalf("CacheDir = %q", cfg.CacheDir)
	}
	if cfg.CacheMaxSize != 512<<20 {
		t.Fatalf("CacheMaxSize = %d, want %d", cfg.CacheMaxSize, 512<<20)
	}
}

func TestLoadConfigDefaultsAreSane(t *testing.T) {
	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.CacheMaxSize != defaultCacheMaxSize {
		t.Fatalf("CacheMaxSize = %d, want %d", cfg.CacheMaxSize, defaultCacheMaxSize)
	}
	if cfg.YtdlpPath != "yt-dlp" {
		t.Fatalf("YtdlpPath = %q, want yt-dlp", cfg.YtdlpPath)
	}
	if cfg.CacheDir == "" {
		t.Fatal("CacheDir is empty")
	}
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"10GB", 10 << 30},
		{"500MB", 500 << 20},
		{"1.5G", int64(1.5 * float64(1<<30))},
		{"256KiB", 256 << 10},
		{"1073741824", 1073741824},
		{"42", 42},
		{"2 gb", 2 << 30},
	}
	for _, tc := range cases {
		got, err := parseSize(tc.in)
		if err != nil {
			t.Fatalf("parseSize(%q) returned error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseSizeRejectsInvalid(t *testing.T) {
	for _, in := range []string{"", "abc", "-5", "GB", "10XB"} {
		if _, err := parseSize(in); err == nil {
			t.Fatalf("parseSize(%q) = nil error, want error", in)
		}
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
	if u, err := parseProxyURL(""); err != nil || u != nil {
		t.Fatalf("parseProxyURL(\"\") = %v, %v; want nil, nil", u, err)
	}
	for _, in := range []string{"http://host:8080", "https://host:8080", "socks5://host:1080", "socks5h://host:1080"} {
		if _, err := parseProxyURL(in); err != nil {
			t.Fatalf("parseProxyURL(%q) returned error: %v", in, err)
		}
	}
	for _, in := range []string{"ftp://host:21", "socks4://host:1080", "http://", "not a url"} {
		if _, err := parseProxyURL(in); err == nil {
			t.Fatalf("parseProxyURL(%q) = nil error, want error", in)
		}
	}
}

func TestLoadConfigHWAccelDefaultsToSoftware(t *testing.T) {
	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.FfmpegBackend != hwSoftware {
		t.Fatalf("default FfmpegBackend = %q, want software", cfg.FfmpegBackend)
	}
}

func TestLoadConfigHWAccelEnvAndFlag(t *testing.T) {
	t.Setenv(envFfmpegHWAccel, "nvenc")
	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.FfmpegBackend != hwNVENC {
		t.Fatalf("FfmpegBackend from env = %q, want nvenc", cfg.FfmpegBackend)
	}

	// Flag overrides env; "libx264" and mixed case both normalise to software.
	cfg, err = LoadConfig([]string{"--ffmpeg-hwaccel", "LibX264"})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.FfmpegBackend != hwSoftware {
		t.Fatalf("FfmpegBackend from flag = %q, want software", cfg.FfmpegBackend)
	}
}

func TestLoadConfigRejectsUnknownHWAccel(t *testing.T) {
	if _, err := LoadConfig([]string{"--ffmpeg-hwaccel", "bogus"}); err == nil {
		t.Fatal("LoadConfig accepted unknown hwaccel, want error")
	}
}

func TestLoadConfigVAAPIRequiresDevice(t *testing.T) {
	if _, err := LoadConfig([]string{"--ffmpeg-hwaccel", "vaapi"}); err == nil {
		t.Fatal("LoadConfig accepted vaapi without a device, want error")
	}
	cfg, err := LoadConfig([]string{"--ffmpeg-hwaccel", "vaapi", "--ffmpeg-hw-device", "/dev/dri/renderD128"})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.FfmpegBackend != hwVAAPI || cfg.FfmpegHWDevice != "/dev/dri/renderD128" {
		t.Fatalf("vaapi config = %q/%q, want vaapi /dev/dri/renderD128", cfg.FfmpegBackend, cfg.FfmpegHWDevice)
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

func TestLoadConfigTranscodeOptionsEnvAndFlag(t *testing.T) {
	t.Setenv(envTranscodePreset, "slow")
	t.Setenv(envTranscodeCRF, "20")
	t.Setenv(envTranscodeVideoBitrate, "4M")
	t.Setenv(envTranscodeMaxrate, "5M")
	t.Setenv(envTranscodeBufsize, "10M")
	t.Setenv(envTranscodeAudioBitrate, "160k")

	cfg, err := LoadConfig(nil)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	assertTranscodeConfig(t, cfg, "slow", "20", "4M", "5M", "10M", "160k")

	cfg, err = LoadConfig([]string{
		"--transcode-preset", "medium",
		"--transcode-crf", "21",
		"--transcode-video-bitrate", "6M",
		"--transcode-maxrate", "7M",
		"--transcode-bufsize", "14M",
		"--transcode-audio-bitrate", "128k",
	})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	assertTranscodeConfig(t, cfg, "medium", "21", "6M", "7M", "14M", "128k")
}

func assertTranscodeConfig(t *testing.T, cfg Config, preset, crf, videoBitrate, maxrate, bufsize, audioBitrate string) {
	t.Helper()
	if cfg.TranscodePreset != preset {
		t.Fatalf("TranscodePreset = %q, want %q", cfg.TranscodePreset, preset)
	}
	if cfg.TranscodeCRF != crf {
		t.Fatalf("TranscodeCRF = %q, want %q", cfg.TranscodeCRF, crf)
	}
	if cfg.TranscodeVideoBitrate != videoBitrate {
		t.Fatalf("TranscodeVideoBitrate = %q, want %q", cfg.TranscodeVideoBitrate, videoBitrate)
	}
	if cfg.TranscodeMaxrate != maxrate {
		t.Fatalf("TranscodeMaxrate = %q, want %q", cfg.TranscodeMaxrate, maxrate)
	}
	if cfg.TranscodeBufsize != bufsize {
		t.Fatalf("TranscodeBufsize = %q, want %q", cfg.TranscodeBufsize, bufsize)
	}
	if cfg.TranscodeAudioBitrate != audioBitrate {
		t.Fatalf("TranscodeAudioBitrate = %q, want %q", cfg.TranscodeAudioBitrate, audioBitrate)
	}
}

func TestLoadConfigKeepsGameCommandAfterSeparator(t *testing.T) {
	cfg, err := LoadConfig([]string{
		"--listen", "127.0.0.1:9090",
		"--",
		"gamemoderun",
		"vrchat",
		"--some-game-arg",
	})
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}

	want := []string{"gamemoderun", "vrchat", "--some-game-arg"}
	if !reflect.DeepEqual(cfg.GameCommand, want) {
		t.Fatalf("GameCommand = %#v, want %#v", cfg.GameCommand, want)
	}
}
