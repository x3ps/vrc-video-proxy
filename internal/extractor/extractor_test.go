package extractor

import (
	"io"
	"log/slog"
	"reflect"
	"testing"

	"vrc-video-proxy/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestExtractHeaders(t *testing.T) {
	h := extractHeaders(Metadata{
		"http_headers": map[string]any{
			"User-Agent": "vrc/1.0",
			"Referer":    "https://example.com",
			"Empty":      "",
			"NotString":  42,
		},
	})
	if h.Get("User-Agent") != "vrc/1.0" {
		t.Fatalf("User-Agent = %q", h.Get("User-Agent"))
	}
	if h.Get("Referer") != "https://example.com" {
		t.Fatalf("Referer = %q", h.Get("Referer"))
	}
	if _, ok := h["Empty"]; ok {
		t.Fatal("empty header should be dropped")
	}

	if extractHeaders(Metadata{}) != nil {
		t.Fatal("missing http_headers should yield nil")
	}
}

func TestRunnerCommandArgsIncludeExtraArgs(t *testing.T) {
	r := New(config.Config{
		YtdlpFormat: "bestvideo+bestaudio/best",
		YtdlpExtraArgs: []string{
			"--add-headers", "User-Agent: Mozilla/5.0",
			"--extractor-args", "youtube:player_client=android",
		},
		CookiesFile: "/tmp/cookies.txt",
		Proxy:       "socks5://127.0.0.1:1080",
	}, discardLogger())

	got := r.commandArgs("https://example.com/watch?v=abc")
	want := []string{
		"-J", "--no-playlist", "--no-warnings", "-f", "bestvideo+bestaudio/best",
		"--add-headers", "User-Agent: Mozilla/5.0",
		"--extractor-args", "youtube:player_client=android",
		"--cookies", "/tmp/cookies.txt",
		"--proxy", "socks5://127.0.0.1:1080",
		"--", "https://example.com/watch?v=abc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commandArgs = %#v, want %#v", got, want)
	}
}

func TestRunnerDefaultsFormat(t *testing.T) {
	r := New(config.Config{}, discardLogger())
	got := r.commandArgs("https://example.com/v")
	// The format selector is the 5th element (after -J, --no-playlist, --no-warnings, -f).
	if got[4] != progressiveFormat {
		t.Fatalf("default format = %q, want progressiveFormat", got[4])
	}
}

func TestFindStreamURLUsesTopLevelURL(t *testing.T) {
	got, err := findStreamURL(Metadata{"url": "https://cdn.example.com/top.mp4"})
	if err != nil {
		t.Fatalf("findStreamURL returned error: %v", err)
	}
	if got != "https://cdn.example.com/top.mp4" {
		t.Fatalf("url = %q", got)
	}
}

func TestFindStreamURLFallsBackToRequestedDownloads(t *testing.T) {
	got, err := findStreamURL(Metadata{
		"requested_downloads": []any{
			map[string]any{"url": "https://cdn.example.com/download.mp4"},
		},
	})
	if err != nil {
		t.Fatalf("findStreamURL returned error: %v", err)
	}
	if got != "https://cdn.example.com/download.mp4" {
		t.Fatalf("url = %q", got)
	}
}

func TestFindStreamURLPrefersProgressiveFormat(t *testing.T) {
	got, err := findStreamURL(Metadata{
		"formats": []any{
			map[string]any{"url": "https://cdn.example.com/video-only.mp4", "vcodec": "avc1", "acodec": "none"},
			map[string]any{"url": "https://cdn.example.com/progressive.mp4", "vcodec": "avc1", "acodec": "mp4a"},
			map[string]any{"url": "https://cdn.example.com/audio-only.m4a", "vcodec": "none", "acodec": "mp4a"},
		},
	})
	if err != nil {
		t.Fatalf("findStreamURL returned error: %v", err)
	}
	if got != "https://cdn.example.com/progressive.mp4" {
		t.Fatalf("url = %q, want progressive.mp4", got)
	}
}

func TestReplaceStreamURLsUpdatesTopLevelAndRequestedDownloads(t *testing.T) {
	metadata := Metadata{
		"url": "https://cdn.example.com/top.mp4",
		"requested_downloads": []any{
			map[string]any{"url": "https://cdn.example.com/download.mp4"},
		},
	}

	ReplaceStreamURLs(metadata, "http://127.0.0.1:8080/stream/abc.mp4")

	if metadata["url"] != "http://127.0.0.1:8080/stream/abc.mp4" {
		t.Fatalf("url = %q", metadata["url"])
	}
	downloads := metadata["requested_downloads"].([]any)
	download := downloads[0].(map[string]any)
	if download["url"] != "http://127.0.0.1:8080/stream/abc.mp4" {
		t.Fatalf("requested_downloads url = %q", download["url"])
	}
}
