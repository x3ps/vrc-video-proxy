package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"vrc-video-proxy/internal/extractor"
	"vrc-video-proxy/internal/logging"
)

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintln(w, "ok")
}

type videoRequest struct {
	URL    string
	AVPro  bool
	Source string
}

// getVideoHandler resolves a source URL with the extractor, stores the resolved
// upstream stream under a fresh handle, and returns a playback URL pointing at the
// proxy's /stream endpoint. The player then fetches the bytes through us.
func (s *Server) getVideoHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	videoReq, err := parseVideoRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ext, err := s.extract.Extract(r.Context(), videoReq.URL)
	if err != nil {
		s.logger.Error("extraction failed", "url", logging.RedactURL(videoReq.URL), "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	id := s.store.Put(ext.StreamURL, ext.Headers)

	metadata := ext.Metadata
	if metadata == nil {
		metadata = extractor.Metadata{}
	}
	extractor.ReplaceStreamURLs(metadata, s.streamURL(r, id))
	metadata["original_url"] = videoReq.URL

	s.logPlaybackResponse(videoReq.Source, metadata)
	writeVideoResponse(w, videoReq.Source, metadata)
}

func parseVideoRequest(r *http.Request) (videoRequest, error) {
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		return videoRequest{}, fmt.Errorf("missing url query parameter")
	}

	parsedURL, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return videoRequest{}, fmt.Errorf("url must be an absolute http(s) URL")
	}

	source := strings.TrimSpace(r.URL.Query().Get("source"))
	if source == "" {
		source = "vrchat"
	}

	return videoRequest{
		URL:    rawURL,
		AVPro:  strings.EqualFold(r.URL.Query().Get("avpro"), "true"),
		Source: source,
	}, nil
}

// streamURL builds an absolute URL back to this server's /stream endpoint for the
// given handle id, e.g. http://127.0.0.1:8080/stream/<id>.mp4.
func (s *Server) streamURL(r *http.Request, id string) string {
	u := url.URL{
		Scheme: requestScheme(r),
		Host:   r.Host,
		Path:   "/stream/" + id + ".mp4",
	}
	return u.String()
}

func (s *Server) logPlaybackResponse(source string, metadata extractor.Metadata) {
	playURL, _ := extractor.StringField(metadata, "url")
	originalURL, _ := extractor.StringField(metadata, "original_url")
	s.logger.Info("returning playback url",
		"source", source,
		"url", logging.RedactURL(playURL),
		"original_url", logging.RedactURL(originalURL),
	)
}

// writeVideoResponse returns the resolved playback location in the shape the
// caller's player expects. VRChat invokes yt-dlp expecting a single plain-text URL
// on stdout, so for that source (the default) we emit only the proxied url.
// Resonite invokes yt-dlp with -J and parses the full document, so resonite keeps
// the yt-dlp-like JSON metadata.
func writeVideoResponse(w http.ResponseWriter, source string, metadata extractor.Metadata) {
	if source == "resonite" {
		writeJSON(w, metadata)
		return
	}
	playURL, _ := extractor.StringField(metadata, "url")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprint(w, playURL)
}

func writeJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func requestScheme(r *http.Request) string {
	if r.URL.Scheme != "" {
		return r.URL.Scheme
	}
	if scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); scheme != "" {
		return scheme
	}
	return "http"
}
