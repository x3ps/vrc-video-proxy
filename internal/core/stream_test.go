package core

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"vrc-video-proxy/internal/httpx"
)

// allowLoopback relaxes the SSRF guard so the loopback httptest upstream is
// reachable, restoring it when the test ends.
func allowLoopback(t *testing.T) {
	t.Helper()
	orig := httpx.DisallowedIP
	httpx.DisallowedIP = func(netip.Addr) bool { return false }
	t.Cleanup(func() { httpx.DisallowedIP = orig })
}

func newStreamTestServer(t *testing.T, upstreamURL string, headers http.Header) (*Server, string) {
	t.Helper()
	srv := &Server{
		logger: discardLogger(),
		client: httpx.NewUpstreamClient(nil),
		store:  newStreamStore(time.Minute),
	}
	id := srv.store.Put(upstreamURL, headers)
	return srv, id
}

func TestStreamHandlerPassthrough(t *testing.T) {
	allowLoopback(t)

	body := bytes.Repeat([]byte("vrc"), 1000) // 3000 bytes
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ServeContent gives us Range/206 support for free.
		http.ServeContent(w, r, "v.mp4", time.Unix(0, 0), bytes.NewReader(body))
	}))
	defer upstream.Close()

	srv, id := newStreamTestServer(t, upstream.URL, nil)

	req := httptest.NewRequest(http.MethodGet, "/stream/"+id+".mp4", nil)
	rec := httptest.NewRecorder()
	srv.streamHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), body) {
		t.Fatalf("body length = %d, want %d", rec.Body.Len(), len(body))
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes (mirrored from upstream)", rec.Header().Get("Accept-Ranges"))
	}
}

func TestStreamHandlerForwardsRange(t *testing.T) {
	allowLoopback(t)

	body := bytes.Repeat([]byte("0123456789"), 100) // 1000 bytes
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "v.mp4", time.Unix(0, 0), bytes.NewReader(body))
	}))
	defer upstream.Close()

	srv, id := newStreamTestServer(t, upstream.URL, nil)

	req := httptest.NewRequest(http.MethodGet, "/stream/"+id+".mp4", nil)
	req.Header.Set("Range", "bytes=0-99")
	rec := httptest.NewRecorder()
	srv.streamHandler(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206 (Range forwarded)", rec.Code)
	}
	if got := rec.Body.Len(); got != 100 {
		t.Fatalf("body length = %d, want 100", got)
	}
	if cr := rec.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes 0-99/1000") {
		t.Fatalf("Content-Range = %q, want bytes 0-99/1000", cr)
	}
}

func TestStreamHandlerReplaysHeaders(t *testing.T) {
	allowLoopback(t)

	gotUA := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA <- r.Header.Get("User-Agent")
		_, _ = io.WriteString(w, "ok")
	}))
	defer upstream.Close()

	srv, id := newStreamTestServer(t, upstream.URL, http.Header{"User-Agent": {"vrc/1.0"}})

	req := httptest.NewRequest(http.MethodGet, "/stream/"+id+".mp4", nil)
	rec := httptest.NewRecorder()
	srv.streamHandler(rec, req)

	if ua := <-gotUA; ua != "vrc/1.0" {
		t.Fatalf("upstream saw User-Agent = %q, want vrc/1.0 (replayed)", ua)
	}
}

func TestStreamHandlerUnknownID(t *testing.T) {
	srv := &Server{logger: discardLogger(), client: httpx.NewUpstreamClient(nil), store: newStreamStore(time.Minute)}

	req := httptest.NewRequest(http.MethodGet, "/stream/ffffffffffffffffffffffffffffffff.mp4", nil)
	rec := httptest.NewRecorder()
	srv.streamHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for unknown id", rec.Code)
	}
}

func TestStreamHandlerRejectsBadPath(t *testing.T) {
	srv := &Server{logger: discardLogger(), client: httpx.NewUpstreamClient(nil), store: newStreamStore(time.Minute)}

	for _, path := range []string{"/stream/not-hex.mp4", "/stream/", "/stream/../etc"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.streamHandler(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("path %q: status = %d, want 404", path, rec.Code)
		}
	}
}

func TestStreamHandlerRejectsNonGET(t *testing.T) {
	srv := &Server{logger: discardLogger(), client: httpx.NewUpstreamClient(nil), store: newStreamStore(time.Minute)}

	req := httptest.NewRequest(http.MethodPost, "/stream/ffffffffffffffffffffffffffffffff.mp4", nil)
	rec := httptest.NewRecorder()
	srv.streamHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
