package extractor

import (
	"context"
	"io"
	"log/slog"
	"slices"
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

// argv builds the full process argument list go-ytdlp would execute, so tests
// can assert on the real command without spawning yt-dlp.
func argv(r *Runner, rawURL string) []string {
	return r.command().BuildCommand(context.Background(), r.runArgs(rawURL)...).Args
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

	got := argv(r, "https://example.com/watch?v=abc")

	// go-ytdlp does not guarantee the relative order of builder flags, so check
	// for presence of each flag/value rather than an exact slice.
	for _, want := range []string{
		"--dump-single-json", "--no-playlist", "--no-warnings",
		"--cookies", "/tmp/cookies.txt",
		"--proxy", "socks5://127.0.0.1:1080",
		"--add-headers", "User-Agent: Mozilla/5.0",
		"--extractor-args", "youtube:player_client=android",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("argv missing %q: %#v", want, got)
		}
	}
	assertFlagValue(t, got, "--format", "bestvideo+bestaudio/best")

	// The URL must remain the verbatim tail, guarded by our own "--".
	if got[len(got)-1] != "https://example.com/watch?v=abc" || got[len(got)-2] != "--" {
		t.Fatalf("argv = %#v, want it to end with -- <url>", got)
	}
}

func TestRunnerDefaultsFormat(t *testing.T) {
	r := New(config.Config{}, discardLogger())
	got := argv(r, "https://example.com/v")
	assertFlagValue(t, got, "--format", progressiveFormat)
}

// assertFlagValue checks that flag appears in argv immediately followed by value.
func assertFlagValue(t *testing.T, argv []string, flag, value string) {
	t.Helper()
	for i, a := range argv {
		if a == flag {
			if i+1 < len(argv) && argv[i+1] == value {
				return
			}
			t.Fatalf("%s = %q, want %q", flag, argvAt(argv, i+1), value)
		}
	}
	t.Fatalf("argv missing %s: %#v", flag, argv)
}

func argvAt(argv []string, i int) string {
	if i < len(argv) {
		return argv[i]
	}
	return ""
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
