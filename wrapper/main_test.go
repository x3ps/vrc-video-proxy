package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseWrapperRequestDetectsVRChatAVPro(t *testing.T) {
	req, err := parseWrapperRequest([]string{
		"-J",
		"--no-playlist",
		"https://example.com/watch?v=123",
	})
	if err != nil {
		t.Fatalf("parseWrapperRequest returned error: %v", err)
	}
	if req.URL != "https://example.com/watch?v=123" {
		t.Fatalf("URL = %q", req.URL)
	}
	if !req.AVPro {
		t.Fatal("AVPro = false, want true")
	}
	if req.Source != "vrchat" {
		t.Fatalf("Source = %q, want vrchat", req.Source)
	}
}

func TestParseWrapperRequestDetectsUnityAndResonite(t *testing.T) {
	req, err := parseWrapperRequest([]string{
		"-f",
		"best[protocol^=http]",
		"--flat-playlist",
		"https://example.com/video",
	})
	if err != nil {
		t.Fatalf("parseWrapperRequest returned error: %v", err)
	}
	if req.AVPro {
		t.Fatal("AVPro = true, want false")
	}
	if req.Source != "resonite" {
		t.Fatalf("Source = %q, want resonite", req.Source)
	}
}

func TestParseWrapperRequestRequiresURL(t *testing.T) {
	if _, err := parseWrapperRequest([]string{"-J", "--no-playlist"}); err == nil {
		t.Fatal("parseWrapperRequest returned nil error, want error")
	}
}

func TestGetVideoEndpoint(t *testing.T) {
	endpoint, err := getVideoEndpoint("http://127.0.0.1:8080/base/", wrapperRequest{
		URL:    "https://example.com/watch?v=1&x=2",
		AVPro:  false,
		Source: "vrchat",
	})
	if err != nil {
		t.Fatalf("getVideoEndpoint returned error: %v", err)
	}

	if !strings.HasPrefix(endpoint, "http://127.0.0.1:8080/base/api/getvideo?") {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if !strings.Contains(endpoint, "avpro=false") {
		t.Fatalf("endpoint = %q, missing avpro=false", endpoint)
	}
	if !strings.Contains(endpoint, "source=vrchat") {
		t.Fatalf("endpoint = %q, missing source=vrchat", endpoint)
	}
	if !strings.Contains(endpoint, "url=https%3A%2F%2Fexample.com%2Fwatch%3Fv%3D1%26x%3D2") {
		t.Fatalf("endpoint = %q, missing encoded url", endpoint)
	}
}

func TestFetchVideoWritesServerResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/getvideo" {
			t.Fatalf("path = %q, want /api/getvideo", r.URL.Path)
		}
		if got := r.URL.Query().Get("url"); got != "https://example.com/video" {
			t.Fatalf("url query = %q", got)
		}
		if got := r.URL.Query().Get("avpro"); got != "true" {
			t.Fatalf("avpro query = %q", got)
		}
		if got := r.URL.Query().Get("source"); got != "vrchat" {
			t.Fatalf("source query = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"url": "http://127.0.0.1/video.mp4"})
	}))
	defer ts.Close()

	body, err := fetchVideo(context.Background(), ts.Client(), ts.URL, wrapperRequest{
		URL:    "https://example.com/video",
		AVPro:  true,
		Source: "vrchat",
	})
	if err != nil {
		t.Fatalf("fetchVideo returned error: %v", err)
	}
	if !bytes.Contains(body, []byte(`"url"`)) {
		t.Fatalf("body = %q, want JSON response", body)
	}
}

func TestFetchVideoReportsServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad url", http.StatusBadRequest)
	}))
	defer ts.Close()

	_, err := fetchVideo(context.Background(), ts.Client(), ts.URL, wrapperRequest{
		URL:    "https://example.com/video",
		AVPro:  true,
		Source: "vrchat",
	})
	if err == nil {
		t.Fatal("fetchVideo returned nil error, want error")
	}
	if !strings.Contains(err.Error(), "bad url") {
		t.Fatalf("error = %q, want body included", err.Error())
	}
}
