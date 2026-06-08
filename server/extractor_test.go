package main

import "testing"

func TestClassifyEndpoint(t *testing.T) {
	tests := []struct {
		name     string
		metadata ytdlpMetadata
		stream   string
		want     Endpoint
	}{
		{"progressive https protocol", ytdlpMetadata{"protocol": "https"}, "https://cdn/v.mp4", EndpointProgressive},
		{"hls protocol", ytdlpMetadata{"protocol": "m3u8_native"}, "https://cdn/v.m3u8", EndpointHLS},
		{"hls protocol plain", ytdlpMetadata{"protocol": "m3u8"}, "https://cdn/v", EndpointHLS},
		{"dash protocol", ytdlpMetadata{"protocol": "http_dash_segments"}, "https://cdn/v.mpd", EndpointDASH},
		{"no protocol, m3u8 url", ytdlpMetadata{}, "https://cdn/live/v.m3u8?token=x", EndpointHLS},
		{"no protocol, mpd url", ytdlpMetadata{}, "https://cdn/v.mpd", EndpointDASH},
		{"no protocol, mp4 url", ytdlpMetadata{}, "https://cdn/v.mp4", EndpointProgressive},
		{"unknown protocol falls back to url", ytdlpMetadata{"protocol": "weird"}, "https://cdn/v.m3u8", EndpointHLS},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyEndpoint(tt.metadata, tt.stream); got != tt.want {
				t.Fatalf("classifyEndpoint = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBoolField(t *testing.T) {
	if !boolField(ytdlpMetadata{"is_live": true}, "is_live") {
		t.Fatal("bool true not read")
	}
	if !boolField(ytdlpMetadata{"is_live": "True"}, "is_live") {
		t.Fatal("string True not read")
	}
	if boolField(ytdlpMetadata{"is_live": nil}, "is_live") {
		t.Fatal("nil should be false")
	}
	if boolField(ytdlpMetadata{}, "is_live") {
		t.Fatal("missing should be false")
	}
}

func TestNeedsTranscode(t *testing.T) {
	tests := []struct {
		name   string
		vcodec string
		acodec string
		want   bool
	}{
		{"h264+aac", "avc1.64001f", "mp4a.40.2", false},
		{"h264+opus", "avc1.64001f", "opus", true},
		{"vp9+aac", "vp09.00.10.08", "mp4a.40.2", true},
		{"unknown codecs assumed compatible", "", "", false},
		{"video only h264", "h264", "none", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := ytdlpMetadata{"vcodec": tt.vcodec, "acodec": tt.acodec}
			if got := needsTranscode(m); got != tt.want {
				t.Fatalf("needsTranscode = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExtractHeaders(t *testing.T) {
	h := extractHeaders(ytdlpMetadata{
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

	if extractHeaders(ytdlpMetadata{}) != nil {
		t.Fatal("missing http_headers should yield nil")
	}
}
