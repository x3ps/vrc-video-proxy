package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// writeCacheEntry writes a finished cache file for src and returns its id.
func writeCacheEntry(t *testing.T, srv *Server, src string, data []byte) string {
	t.Helper()
	id := cacheID(src)
	path, err := srv.cache.Path(id)
	if err != nil {
		t.Fatalf("cache.Path returned error: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write cache file: %v", err)
	}
	return id
}

func waitCached(t *testing.T, cache *Cache, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cache.Has(id) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("download did not populate the cache")
}

func seqBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// --- finished cache file (v1 behaviour, must still hold) ---

func TestVideoFileHandlerSupportsHEAD(t *testing.T) {
	srv := newTestServer(t)
	data := bytes.Repeat([]byte("a"), 1000)
	id := writeCacheEntry(t, srv, "https://example.com/head", data)

	req := httptest.NewRequest(http.MethodHead, "/video/"+id+".mp4", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "1000" {
		t.Fatalf("Content-Length = %q, want 1000", got)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD response body length = %d, want 0", rec.Body.Len())
	}
}

func TestVideoFileHandlerSupportsRange(t *testing.T) {
	srv := newTestServer(t)
	data := []byte("0123456789abcdef")
	id := writeCacheEntry(t, srv, "https://example.com/range", data)

	req := httptest.NewRequest(http.MethodGet, "/video/"+id+".mp4", nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusPartialContent)
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-3/16" {
		t.Fatalf("Content-Range = %q, want bytes 0-3/16", got)
	}
	if got := rec.Header().Get("Content-Length"); got != "4" {
		t.Fatalf("Content-Length = %q, want 4", got)
	}
	if got := rec.Body.String(); got != "0123" {
		t.Fatalf("body = %q, want 0123", got)
	}
}

func TestVideoFileHandlerRejectsUnknownAndUnsafeIDs(t *testing.T) {
	srv := newTestServer(t)

	cases := []string{
		"/video/abc.mp4",
		"/video/" + cacheID("x") + ".mp4",
		"/video/" + cacheID("x"),
	}
	for _, target := range cases {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want %d", target, rec.Code, http.StatusNotFound)
		}
	}
}

func TestCacheIDFromPathRejectsTraversal(t *testing.T) {
	if _, ok := cacheIDFromPath("/video/../secret.mp4", "/video/"); ok {
		t.Fatal("cacheIDFromPath accepted a traversal path")
	}
	if _, ok := cacheIDFromPath("/video/"+cacheID("ok")+".mp4", "/video/"); !ok {
		t.Fatal("cacheIDFromPath rejected a valid path")
	}
}

// --- sparse live streaming (v2) ---

func TestLiveSparseFullStreamThenCached(t *testing.T) {
	srv := newTestServer(t)
	data := seqBytes(10000)
	up := &fakeUpstream{data: data, rangeable: true, chunk: 1000, delay: time.Millisecond}
	up.install(srv.jobs)

	const src = "https://example.com/sparse-full"
	id := cacheID(src)
	srv.jobs.getOrStart(id, "https://cdn.example.com/x.mp4")

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/live/" + id + ".mp4")
	if err != nil {
		t.Fatalf("GET /live: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !bytes.Equal(body, data) {
		t.Fatalf("streamed %d bytes, want %d", len(body), len(data))
	}

	waitCached(t, srv.cache, id)

	// Once cached, the finished file is served with full Range support.
	rr := httptest.NewRequest(http.MethodGet, "/video/"+id+".mp4", nil)
	rr.Header.Set("Range", "bytes=5-9")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, rr)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("cached range status = %d, want 206", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), data[5:10]) {
		t.Fatalf("cached range body mismatch")
	}
}

func TestLiveSparseRangeReturns206(t *testing.T) {
	srv := newTestServer(t)
	data := seqBytes(10000)
	up := &fakeUpstream{data: data, rangeable: true, chunk: 1000, delay: time.Millisecond}
	up.install(srv.jobs)

	const src = "https://example.com/sparse-range"
	id := cacheID(src)
	srv.jobs.getOrStart(id, "https://cdn.example.com/x.mp4")

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/live/"+id+".mp4", nil)
	req.Header.Set("Range", "bytes=2000-2999")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /live range: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Range"), "bytes 2000-2999/10000"; got != want {
		t.Fatalf("Content-Range = %q, want %q", got, want)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, data[2000:3000]) {
		t.Fatalf("range body mismatch (%d bytes)", len(body))
	}
}

func TestLiveSparseForwardSeekTriggersDedicatedFetch(t *testing.T) {
	srv := newTestServer(t)
	srv.jobs.fillChunk = 2048 // small chunks so the sequential filler stays low
	data := seqBytes(20000)
	up := &fakeUpstream{data: data, rangeable: true, chunk: 512, delay: 3 * time.Millisecond}
	up.install(srv.jobs)

	const src = "https://example.com/sparse-seek"
	id := cacheID(src)
	srv.jobs.getOrStart(id, "https://cdn.example.com/x.mp4")

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/live/"+id+".mp4", nil)
	req.Header.Set("Range", "bytes=18000-18099")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /live seek: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, data[18000:18100]) {
		t.Fatalf("seek body mismatch (%d bytes)", len(body))
	}
	// The seek must have been served by a dedicated fetch starting at the seek
	// offset, not by waiting for the sequential filler (whose chunk boundaries
	// are multiples of 2048 and never start at 18000).
	if !up.fetchedFrom(18000) {
		t.Fatal("expected an on-demand upstream fetch starting at the seek offset")
	}
}

func TestLiveSparseUnsatisfiableRangeReturns416(t *testing.T) {
	srv := newTestServer(t)
	data := seqBytes(5000)
	up := &fakeUpstream{data: data, rangeable: true, chunk: 1000}
	up.install(srv.jobs)

	const src = "https://example.com/sparse-416"
	id := cacheID(src)
	srv.jobs.getOrStart(id, "https://cdn.example.com/x.mp4")

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/live/"+id+".mp4", nil)
	req.Header.Set("Range", "bytes=999999-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /live 416: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want 416", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Range"), "bytes */5000"; got != want {
		t.Fatalf("Content-Range = %q, want %q", got, want)
	}
}

func TestLiveSequentialFallback(t *testing.T) {
	srv := newTestServer(t)
	data := seqBytes(6000)
	up := &fakeUpstream{data: data, rangeable: false, chunk: 1000, delay: time.Millisecond}
	up.install(srv.jobs)

	const src = "https://example.com/seq-live"
	id := cacheID(src)
	srv.jobs.getOrStart(id, "https://cdn.example.com/x.mp4")

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/live/" + id + ".mp4")
	if err != nil {
		t.Fatalf("GET /live: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "none" {
		t.Fatalf("Accept-Ranges = %q, want none", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, data) {
		t.Fatalf("streamed %d bytes, want %d", len(body), len(data))
	}
}

func TestParseSingleRange(t *testing.T) {
	const size = 1000
	cases := []struct {
		header          string
		start, end      int64
		ok, satisfiable bool
	}{
		{"bytes=0-99", 0, 99, true, true},
		{"bytes=100-", 100, 999, true, true},
		{"bytes=-100", 900, 999, true, true},
		{"bytes=0-99999", 0, 999, true, true},     // end clamped
		{"bytes=1000-", 0, 0, false, false},       // start at/after size
		{"bytes=5-1", 0, 0, false, false},         // end < start
		{"bytes=abc", 0, 0, false, true},          // malformed (no dash) -> serve full
		{"bytes=x-y", 0, 0, false, false},         // dash present but non-numeric
		{"bytes=0-99,200-299", 0, 0, false, true}, // multi-range -> full
		{"items=0-99", 0, 0, false, true},         // not a byte range -> full
	}
	for _, tc := range cases {
		start, end, ok, sat := parseSingleRange(tc.header, size)
		if ok != tc.ok || sat != tc.satisfiable {
			t.Fatalf("%q: ok=%v sat=%v, want ok=%v sat=%v", tc.header, ok, sat, tc.ok, tc.satisfiable)
		}
		if ok && (start != tc.start || end != tc.end) {
			t.Fatalf("%q: range=%d-%d, want %d-%d", tc.header, start, end, tc.start, tc.end)
		}
	}
}
