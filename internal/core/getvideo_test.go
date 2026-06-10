package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"vrc-video-proxy/internal/extractor"
)

var streamURLPattern = regexp.MustCompile(`^http://127\.0\.0\.1:8080/stream/([0-9a-f]{32})\.mp4$`)

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

func TestGetVideoHandlerResolvesAndReturnsStreamURL(t *testing.T) {
	srv := newTestServer(t)

	const sourceURL = "https://example.com/watch?v=2"
	srv.extract = extractor.Func(func(_ context.Context, rawURL string) (extractor.Extraction, error) {
		if rawURL != sourceURL {
			t.Fatalf("extract rawURL = %q", rawURL)
		}
		return extractor.Extraction{
			Metadata:  extractor.Metadata{"id": "video-id", "title": "Video Title"},
			StreamURL: "https://cdn.example.com/stream.mp4",
			Headers:   http.Header{"User-Agent": {"vrc/1.0"}},
		}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/getvideo?url=https%3A%2F%2Fexample.com%2Fwatch%3Fv%3D2&avpro=true&source=vrchat", nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()

	srv.getVideoHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got, want := rec.Header().Get("Content-Type"), "text/plain; charset=utf-8"; got != want {
		t.Fatalf("Content-Type = %q, want %q (VRChat expects plain text)", got, want)
	}

	m := streamURLPattern.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("body = %q, want a /stream/<id>.mp4 URL", rec.Body.String())
	}

	// The minted handle must resolve to the upstream stream we returned.
	entry, ok := srv.store.Get(m[1])
	if !ok {
		t.Fatalf("no store entry for id %q", m[1])
	}
	if entry.upstreamURL != "https://cdn.example.com/stream.mp4" {
		t.Fatalf("stored upstreamURL = %q", entry.upstreamURL)
	}
	if entry.headers.Get("User-Agent") != "vrc/1.0" {
		t.Fatalf("stored headers not replayed: %v", entry.headers)
	}
}

func TestGetVideoHandlerResoniteReturnsJSON(t *testing.T) {
	srv := newTestServer(t)

	const sourceURL = "https://example.com/watch?v=1"
	srv.extract = extractor.Func(func(context.Context, string) (extractor.Extraction, error) {
		return extractor.Extraction{
			Metadata:  extractor.Metadata{"id": "video-id", "title": "Video Title"},
			StreamURL: "https://cdn.example.com/stream.mp4",
		}, nil
	})

	req := httptest.NewRequest(http.MethodGet, "/api/getvideo?url=https%3A%2F%2Fexample.com%2Fwatch%3Fv%3D1&avpro=false&source=resonite", nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()

	srv.getVideoHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	url, _ := got["url"].(string)
	if !streamURLPattern.MatchString(url) {
		t.Fatalf("url = %q, want a /stream/<id>.mp4 URL", url)
	}
	if got["original_url"] != sourceURL {
		t.Fatalf("original_url = %q, want %q", got["original_url"], sourceURL)
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
