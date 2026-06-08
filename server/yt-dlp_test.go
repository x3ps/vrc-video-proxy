package main

import (
	"context"
	"reflect"
	"testing"
)

// newProbelessYtdlpRunner returns a runner whose ffprobe path does not exist, so
// any codec probe fails instantly (no network) and decideTranscode falls back to
// the yt-dlp metadata. This keeps the codec-decision tests hermetic and fast.
func newProbelessYtdlpRunner() *ytdlpRunner {
	return newYtdlpRunner(Config{YtdlpPath: "/does/not/exist", FfprobePath: "/does/not/exist"}, discardLogger())
}

func TestYtdlpRunnerCommandArgsIncludeExtraArgs(t *testing.T) {
	y := newYtdlpRunner(Config{
		YtdlpFormat: "bestvideo+bestaudio/best",
		YtdlpExtraArgs: []string{
			"--add-headers", "User-Agent: Mozilla/5.0",
			"--extractor-args", "youtube:player_client=android",
		},
		CookiesFile: "/tmp/cookies.txt",
		Proxy:       "socks5://127.0.0.1:1080",
	}, discardLogger())

	got := y.commandArgs("https://example.com/watch?v=abc")
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

func TestDecideTranscodeUsesYtdlpCodecs(t *testing.T) {
	y := newProbelessYtdlpRunner()

	// Both codecs known and MP4-friendly: no probe, no transcode.
	md := ytdlpMetadata{"vcodec": "avc1.640028", "acodec": "mp4a.40.2"}
	if y.decideTranscode(context.Background(), md, "https://cdn.example.com/v.mp4", EndpointProgressive, false, nil) {
		t.Fatal("transcode = true, want false for h264/aac")
	}

	// Incompatible codecs reported by yt-dlp: transcode without probing.
	md = ytdlpMetadata{"vcodec": "vp9", "acodec": "opus"}
	if !y.decideTranscode(context.Background(), md, "https://cdn.example.com/v.webm", EndpointProgressive, false, nil) {
		t.Fatal("transcode = false, want true for vp9/opus")
	}
}

func TestDecideTranscodeSkipsProbeForLiveAndManifests(t *testing.T) {
	y := newProbelessYtdlpRunner()
	md := ytdlpMetadata{} // no codecs reported; a progressive VOD would probe here

	cases := []struct {
		name     string
		endpoint Endpoint
		isLive   bool
	}{
		{"hls", EndpointHLS, false},
		{"dash", EndpointDASH, false},
		{"live progressive", EndpointProgressive, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// No probe is attempted; unknown codecs fall back to "compatible".
			if y.decideTranscode(context.Background(), md, "https://cdn.example.com/x", c.endpoint, c.isLive, nil) {
				t.Fatal("transcode = true, want false (no probe, unknown codecs assumed compatible)")
			}
		})
	}
}

func TestDecideTranscodeFallsBackWhenProbeFails(t *testing.T) {
	y := newProbelessYtdlpRunner()
	// Progressive + unknown codecs would trigger a probe, but the bogus ffprobe
	// path makes it fail instantly, so we fall back to metadata (assume compatible).
	md := ytdlpMetadata{}
	if y.decideTranscode(context.Background(), md, "https://cdn.example.com/x.bin", EndpointProgressive, false, nil) {
		t.Fatal("transcode = true, want false when probe fails and codecs are unknown")
	}
}

func TestFindStreamURLUsesTopLevelURL(t *testing.T) {
	got, err := findStreamURL(ytdlpMetadata{
		"url": "https://cdn.example.com/top.mp4",
	})
	if err != nil {
		t.Fatalf("findStreamURL returned error: %v", err)
	}
	if got != "https://cdn.example.com/top.mp4" {
		t.Fatalf("url = %q", got)
	}
}

func TestFindStreamURLFallsBackToRequestedDownloads(t *testing.T) {
	got, err := findStreamURL(ytdlpMetadata{
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

func TestFindStreamURLFallsBackToFormats(t *testing.T) {
	got, err := findStreamURL(ytdlpMetadata{
		"formats": []any{
			map[string]any{"url": "https://cdn.example.com/low.mp4"},
			map[string]any{"url": "https://cdn.example.com/high.mp4"},
		},
	})
	if err != nil {
		t.Fatalf("findStreamURL returned error: %v", err)
	}
	if got != "https://cdn.example.com/high.mp4" {
		t.Fatalf("url = %q", got)
	}
}

func TestFindStreamURLPrefersProgressiveFormat(t *testing.T) {
	got, err := findStreamURL(ytdlpMetadata{
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
	metadata := ytdlpMetadata{
		"url": "https://cdn.example.com/top.mp4",
		"requested_downloads": []any{
			map[string]any{"url": "https://cdn.example.com/download.mp4"},
		},
	}

	replaceStreamURLs(metadata, "http://127.0.0.1:8080/video?url=stream")

	if metadata["url"] != "http://127.0.0.1:8080/video?url=stream" {
		t.Fatalf("url = %q", metadata["url"])
	}

	downloads := metadata["requested_downloads"].([]any)
	download := downloads[0].(map[string]any)
	if download["url"] != "http://127.0.0.1:8080/video?url=stream" {
		t.Fatalf("requested_downloads url = %q", download["url"])
	}
}
