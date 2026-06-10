package core

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"vrc-video-proxy/internal/httpx"
)

// streamIDPattern matches the hex ids minted by streamStore. Validating the shape
// before a map lookup keeps untrusted path input from reaching anything else.
var streamIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// streamHeadersToMirror are the upstream response headers copied through to the
// player so Range/seek and content negotiation work transparently.
var streamHeadersToMirror = []string{
	"Content-Type",
	"Content-Length",
	"Content-Range",
	"Accept-Ranges",
	"Last-Modified",
	"ETag",
	"Cache-Control",
}

// streamRequestHeadersToForward are the client request headers passed upstream so
// Range and conditional requests reach the origin.
var streamRequestHeadersToForward = []string{
	"Range",
	"If-Range",
	"If-None-Match",
	"If-Modified-Since",
}

// streamHandler is the passthrough byte proxy: it looks up a resolved upstream
// stream by id and streams its bytes to the player, forwarding the client's Range
// and mirroring the upstream status (200/206/416) and headers. There is no cache
// and no transcode — the upstream response is relayed as-is.
func (s *Server) streamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, ok := streamIDFromPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	entry, ok := s.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}

	reqLog := s.logger.With("id", id, "range", r.Header.Get("Range"))

	// Defence in depth: the URL came from our own yt-dlp, but re-check before
	// dialing in case a resolved host points somewhere internal.
	if err := httpx.GuardUpstreamURL(r.Context(), entry.upstreamURL); err != nil {
		reqLog.Warn("upstream blocked", "error", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, entry.upstreamURL, nil)
	if err != nil {
		reqLog.Warn("build upstream request failed", "error", err)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
		return
	}
	for k := range entry.headers {
		req.Header.Set(k, entry.headers.Get(k))
	}
	for _, h := range streamRequestHeadersToForward {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}

	resp, err := s.client.Do(req)
	if err != nil {
		reqLog.Warn("upstream fetch failed", "error", err)
		http.Error(w, "upstream fetch failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for _, h := range streamHeadersToMirror {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	// Remove the server WriteTimeout for this request so long streams are not cut
	// off mid-transfer; cancellation is driven by the request context instead.
	clearWriteDeadline(w)
	w.WriteHeader(resp.StatusCode)

	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		// A client that pauses or seeks away cancels the request context; that is
		// expected, not an error.
		reqLog.Debug("stream copy ended early", "error", err)
	}
}

// streamIDFromPath extracts and validates the id from "/stream/{id}" or
// "/stream/{id}.mp4" (AVPro is friendlier to URLs that look like a file).
func streamIDFromPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "/stream/")
	if !ok {
		return "", false
	}
	id := strings.TrimSuffix(rest, ".mp4")
	if !streamIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

// clearWriteDeadline removes the server's WriteTimeout for the current request so
// long video streams are not cut off mid-transfer.
func clearWriteDeadline(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	// SetWriteDeadline(zero) disables the deadline; ignore unsupported errors.
	_ = rc.SetWriteDeadline(time.Time{})
}
