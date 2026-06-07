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
