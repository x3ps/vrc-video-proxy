package main

import "testing"

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
