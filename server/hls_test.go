package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

// recordingBuild returns a build func that renders kind+abs as a marker so tests
// can assert rewriting without involving tokens.
func recordingBuild() func(string, hlsKind) string {
	return func(abs string, kind hlsKind) string {
		k := "SEG"
		if kind == kindManifest {
			k = "MAN"
		}
		return fmt.Sprintf("%s|%s", k, abs)
	}
}

func TestRewriteMasterPlaylist(t *testing.T) {
	input := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-STREAM-INF:BANDWIDTH=800000",
		"low/index.m3u8",
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"audio/eng.m3u8\"",
		"",
	}, "\n")

	out, err := rewriteHLSPlaylist([]byte(input), "https://h.com/live/master.m3u8", recordingBuild())
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "MAN|https://h.com/live/low/index.m3u8") {
		t.Fatalf("variant URI not rewritten to manifest:\n%s", got)
	}
	if !strings.Contains(got, "URI=\"MAN|https://h.com/live/audio/eng.m3u8\"") {
		t.Fatalf("EXT-X-MEDIA URI not rewritten to manifest:\n%s", got)
	}
}

func TestRewriteMediaPlaylist(t *testing.T) {
	input := strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-MAP:URI=\"init.mp4\"",
		"#EXT-X-KEY:METHOD=AES-128,URI=\"https://k.com/key.bin\"",
		"#EXTINF:4.0,",
		"seg0.ts",
		"#EXTINF:4.0,",
		"https://cdn.com/seg1.ts",
		"#EXT-X-ENDLIST",
		"",
	}, "\n")

	out, err := rewriteHLSPlaylist([]byte(input), "https://h.com/live/media.m3u8", recordingBuild())
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	got := string(out)
	checks := []string{
		"URI=\"SEG|https://h.com/live/init.mp4\"", // EXT-X-MAP resolved + segment kind
		"URI=\"SEG|https://k.com/key.bin\"",       // EXT-X-KEY absolute kept
		"SEG|https://h.com/live/seg0.ts",          // relative segment resolved
		"SEG|https://cdn.com/seg1.ts",             // absolute segment
	}
	for _, c := range checks {
		if !strings.Contains(got, c) {
			t.Fatalf("missing %q in:\n%s", c, got)
		}
	}
	if !strings.Contains(got, "#EXT-X-ENDLIST") {
		t.Fatalf("non-URI tags must be preserved:\n%s", got)
	}
}

func TestHLSProxyEndToEnd(t *testing.T) {
	var segHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/media.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:4.0,\nseg0.ts\n#EXT-X-ENDLIST\n")
		case "/seg0.ts":
			atomic.AddInt32(&segHits, 1)
			w.Header().Set("Content-Type", "video/mp2t")
			fmt.Fprint(w, "TSDATA")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	signer, _ := NewURLSigner("secret")
	p := newHLSProxy(signer, NewMemCache(16, 0), discardLogger())
	// Allow fetching the httptest loopback upstream despite the SSRF guard.
	p.client = upstream.Client()
	const proxyBase = "http://proxy.local"

	// Fetch the rewritten manifest.
	manifestURL := p.manifestURL(proxyBase, upstream.URL+"/media.m3u8", nil)
	req := httptest.NewRequest(http.MethodGet, manifestURL, nil)
	rec := httptest.NewRecorder()
	withoutSSRFGuard(t)
	p.ServeManifest(rec, req, proxyBase)

	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if rec.Header().Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("manifest content-type = %q", rec.Header().Get("Content-Type"))
	}

	// The single segment line should now point at our /hls/segment endpoint.
	var segURL string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, proxyBase+"/hls/segment") {
			segURL = line
		}
	}
	if segURL == "" {
		t.Fatalf("no rewritten segment URL in manifest:\n%s", body)
	}

	// Fetch the segment twice; the second must be served from cache (one upstream hit).
	for i := 0; i < 2; i++ {
		segReq := httptest.NewRequest(http.MethodGet, segURL, nil)
		segRec := httptest.NewRecorder()
		p.ServeSegment(segRec, segReq, proxyBase)
		if segRec.Code != http.StatusOK {
			t.Fatalf("segment status = %d, body=%s", segRec.Code, segRec.Body.String())
		}
		if segRec.Body.String() != "TSDATA" {
			t.Fatalf("segment body = %q", segRec.Body.String())
		}
		if ct := segRec.Header().Get("Content-Type"); ct != "video/mp2t" {
			t.Fatalf("segment content-type = %q", ct)
		}
	}
	if got := atomic.LoadInt32(&segHits); got != 1 {
		t.Fatalf("upstream segment hits = %d, want 1 (cache miss then hit)", got)
	}
}

// withoutSSRFGuard relaxes the upstream guard for the duration of a test so the
// httptest loopback server can be reached.
func withoutSSRFGuard(t *testing.T) {
	t.Helper()
	orig := isDisallowedIP
	isDisallowedIP = func(netip.Addr) bool { return false }
	t.Cleanup(func() { isDisallowedIP = orig })
}
