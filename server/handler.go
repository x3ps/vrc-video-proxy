package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// extractVideo extracts metadata and a playable stream URL for a source URL. It
// is a package var so tests can substitute a fake extractor.
var extractVideo = extractWithYTDLP

// Server holds the shared state for the HTTP handlers.
type Server struct {
	cfg     Config
	cache   *Cache
	jobs    *JobManager
	logger  *slog.Logger
	extract func(cfg Config, rawURL string) (ytdlpMetadata, string, error)
}

// NewServer wires up the cache and job manager for the given config.
func NewServer(cfg Config, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	cache, err := NewCache(cfg.CacheDir, cfg.CacheMaxSize)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:     cfg,
		cache:   cache,
		jobs:    NewJobManager(cache, logger),
		logger:  logger,
		extract: extractVideo,
	}, nil
}

// routes builds the HTTP handler. A failure to initialise the cache is fatal, so
// callers that need to handle it should use NewServer directly.
func routes(cfg Config) http.Handler {
	srv, err := NewServer(cfg, slog.Default())
	if err != nil {
		panic(err)
	}
	return srv.Handler()
}

// Handler returns the configured mux for the server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/api/getvideo", s.getVideoHandler)
	mux.HandleFunc("/video/", s.videoFileHandler)
	mux.HandleFunc("/live/", s.liveHandler)
	return mux
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintln(w, "ok")
}

type videoRequest struct {
	URL    string
	AVPro  bool
	Source string
}

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

	id := cacheID(videoReq.URL)

	// Cache hit: return a yt-dlp-like document pointing at the Range-capable
	// cached file without re-invoking yt-dlp.
	if s.cache.Has(id) {
		_ = s.cache.Touch(id)
		metadata := ytdlpMetadata{
			"id":           id,
			"ext":          "mp4",
			"url":          s.playableURL(r, "/video/", id),
			"original_url": videoReq.URL,
			"_cache":       "hit",
		}
		writeJSON(w, metadata)
		return
	}

	// Cache miss: extract metadata + a progressive stream URL, kick off a
	// background download, and return the live streaming URL.
	metadata, streamURL, err := s.extract(s.cfg, videoReq.URL)
	if err != nil {
		s.logger.Error("extraction failed", "url", videoReq.URL, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.jobs.getOrStart(id, streamURL)

	replaceStreamURLs(metadata, s.playableURL(r, "/live/", id))
	metadata["original_url"] = videoReq.URL
	metadata["_cache"] = "miss"
	writeJSON(w, metadata)
}

func (s *Server) videoFileHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, ok := cacheIDFromPath(r.URL.Path, "/video/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	path, err := s.cache.Path(id)
	if err != nil || !s.cache.Has(id) {
		http.NotFound(w, r)
		return
	}

	serveCachedFile(w, r, path)
}

func (s *Server) liveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id, ok := cacheIDFromPath(r.URL.Path, "/live/")
	if !ok {
		http.NotFound(w, r)
		return
	}

	// If the download already completed, serve the finished file with Range
	// support instead of tailing.
	if s.serveIfCached(w, r, id) {
		return
	}

	job, ok := s.jobs.getJob(id)
	if !ok {
		// No active job and not cached: maybe it finished after the Has() check.
		if !s.serveIfCached(w, r, id) {
			http.NotFound(w, r)
		}
		return
	}

	// Wait for the upstream probe to determine the playback mode.
	select {
	case <-job.ready:
	case <-r.Context().Done():
		return
	}

	if job.probeErr != nil {
		if !s.serveIfCached(w, r, id) {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		}
		return
	}

	// The download may have finished while we waited.
	if s.serveIfCached(w, r, id) {
		return
	}

	if job.rangeable && job.sf != nil {
		serveSparse(w, r, job, s.jobs)
		return
	}
	serveLive(w, r, job)
}

// serveIfCached serves the finished cache entry for id if present, returning true
// when it handled the request.
func (s *Server) serveIfCached(w http.ResponseWriter, r *http.Request, id string) bool {
	if !s.cache.Has(id) {
		return false
	}
	path, err := s.cache.Path(id)
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	serveCachedFile(w, r, path)
	return true
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

// cacheIDFromPath extracts and validates the "<id>.mp4" component after prefix.
func cacheIDFromPath(path, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok {
		return "", false
	}
	id, ok := strings.CutSuffix(rest, ".mp4")
	if !ok {
		return "", false
	}
	if !validateID(id) {
		return "", false
	}
	return id, true
}

// playableURL builds an absolute URL back to this server for the given path
// prefix and cache id, e.g. http://127.0.0.1:8080/video/<id>.mp4.
func (s *Server) playableURL(r *http.Request, prefix, id string) string {
	u := url.URL{
		Scheme: requestScheme(r),
		Host:   r.Host,
		Path:   prefix + id + ".mp4",
	}
	return u.String()
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
