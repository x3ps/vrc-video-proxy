package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestHealthHandlerReturnsOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()

	healthHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, want := rec.Body.String(), "ok\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if got, want := rec.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
}

func TestGetVideoHandlerCacheHitReturnsVideoURL(t *testing.T) {
	srv := newTestServer(t)

	const sourceURL = "https://example.com/watch?v=1"
	id := cacheID(sourceURL)
	path, err := srv.cache.Path(id)
	if err != nil {
		t.Fatalf("cache.Path returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte("cached-bytes"), 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	srv.extract = extractorFunc(func(context.Context, string) (Extraction, error) {
		t.Fatal("extract should not be called on cache hit")
		return Extraction{}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/getvideo?url=https%3A%2F%2Fexample.com%2Fwatch%3Fv%3D1&avpro=true&source=vrchat", nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()

	srv.getVideoHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// VRChat expects a single plain-text URL on stdout, not JSON.
	if got, want := rec.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q", got, want)
	}
	wantURL := "http://127.0.0.1:8080/video/" + id + ".mp4"
	if got := rec.Body.String(); got != wantURL {
		t.Fatalf("body = %q, want %q", got, wantURL)
	}
}

func TestGetVideoHandlerResoniteCacheHitReturnsJSON(t *testing.T) {
	srv := newTestServer(t)

	const sourceURL = "https://example.com/watch?v=1"
	id := cacheID(sourceURL)
	path, err := srv.cache.Path(id)
	if err != nil {
		t.Fatalf("cache.Path returned error: %v", err)
	}
	if err := os.WriteFile(path, []byte("cached-bytes"), 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/getvideo?url=https%3A%2F%2Fexample.com%2Fwatch%3Fv%3D1&avpro=false&source=resonite", nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()

	srv.getVideoHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// Resonite invokes yt-dlp with -J and parses the full JSON document.
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	wantURL := "http://127.0.0.1:8080/video/" + id + ".mp4"
	if got["url"] != wantURL {
		t.Fatalf("url = %q, want %q", got["url"], wantURL)
	}
	if got["original_url"] != sourceURL {
		t.Fatalf("original_url = %q, want %q", got["original_url"], sourceURL)
	}
}

func TestGetVideoHandlerCacheMissStartsJobAndReturnsLiveURL(t *testing.T) {
	srv := newTestServer(t)

	const sourceURL = "https://example.com/watch?v=2"
	id := cacheID(sourceURL)

	srv.extract = extractorFunc(func(_ context.Context, rawURL string) (Extraction, error) {
		if rawURL != sourceURL {
			t.Fatalf("extract rawURL = %q", rawURL)
		}
		return Extraction{
			Metadata: ytdlpMetadata{
				"id":    "video-id",
				"title": "Video Title",
				"url":   "https://cdn.example.com/stream.mp4",
			},
			StreamURL: "https://cdn.example.com/stream.mp4",
			Endpoint:  EndpointProgressive,
		}, nil
	})

	// Block the probe so the job stays active (and never touches the network).
	release := make(chan struct{})
	srv.jobs.probe = func(context.Context, string) (int64, bool, error) {
		<-release
		return 0, false, nil
	}
	t.Cleanup(func() { close(release) })

	req := httptest.NewRequest(http.MethodGet, "/api/getvideo?url=https%3A%2F%2Fexample.com%2Fwatch%3Fv%3D2&avpro=true&source=vrchat", nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()

	srv.getVideoHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	// VRChat expects a single plain-text URL on stdout, not JSON.
	wantURL := "http://127.0.0.1:8080/live/" + id + ".mp4"
	if got := rec.Body.String(); got != wantURL {
		t.Fatalf("body = %q, want %q", got, wantURL)
	}

	if _, ok := srv.jobs.getJob(id); !ok {
		t.Fatal("expected an active job for the cache miss")
	}
}

func TestParseVideoRequestReadsVRCParameters(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/getvideo?url=https%3A%2F%2Fexample.com%2Fvideo.mp4&avpro=True&source=resonite", nil)

	got, err := parseVideoRequest(req)
	if err != nil {
		t.Fatalf("parseVideoRequest returned error: %v", err)
	}

	if got.URL != "https://example.com/video.mp4" {
		t.Fatalf("URL = %q", got.URL)
	}
	if !got.AVPro {
		t.Fatal("AVPro = false, want true")
	}
	if got.Source != "resonite" {
		t.Fatalf("Source = %q, want resonite", got.Source)
	}
}

func TestGetVideoHandlerRejectsMissingURL(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/getvideo?avpro=true&source=vrchat", nil)
	rec := httptest.NewRecorder()

	srv.getVideoHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetVideoHandlerRejectsInvalidURL(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/getvideo?url=not-a-url&source=vrchat", nil)
	rec := httptest.NewRecorder()

	srv.getVideoHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestGetVideoHandlerRejectsNonGET(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/getvideo?url=https%3A%2F%2Fexample.com%2Fv.mp4", nil)
	rec := httptest.NewRecorder()

	srv.getVideoHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
