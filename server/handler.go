package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Server holds the shared state for the HTTP handlers.
type Server struct {
	cfg      Config
	cache    *Cache
	jobs     *JobManager
	logger   *slog.Logger
	extract  Extractor
	signer   *URLSigner
	segCache *MemCache
	hls      *hlsProxy
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
	signer, err := NewURLSigner(cfg.Secret)
	if err != nil {
		return nil, err
	}
	segCache := NewMemCache(cfg.SegmentCacheSize, cfg.SegmentCacheTTL)
	jobs := NewJobManager(cache, logger)
	// One configured ffmpeg runner feeds both the remux (stream-copy) and
	// transcode (re-encode) paths, so cfg.FfmpegPath and the transcode options
	// reach both — previously openTranscode silently used the default binary.
	ff := newFfmpegRunner(cfg.FfmpegPath, logger, transcodeOptions{
		backend:      cfg.FfmpegBackend,
		hwDevice:     cfg.FfmpegHWDevice,
		preset:       cfg.TranscodePreset,
		crf:          cfg.TranscodeCRF,
		videoBitrate: cfg.TranscodeVideoBitrate,
		maxrate:      cfg.TranscodeMaxrate,
		bufsize:      cfg.TranscodeBufsize,
		audioBitrate: cfg.TranscodeAudioBitrate,
	})
	jobs.openRemux = ff.openRemux
	jobs.openTranscode = ff.openTranscode
	return &Server{
		cfg:      cfg,
		cache:    cache,
		jobs:     jobs,
		logger:   logger,
		extract:  newYtdlpRunner(cfg, logger),
		signer:   signer,
		segCache: segCache,
		hls:      newHLSProxy(signer, segCache, logger),
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
	mux.HandleFunc("/hls/manifest", s.hlsManifestHandler)
	mux.HandleFunc("/hls/segment", s.hlsSegmentHandler)
	return s.logRequests(mux)
}

// loggingResponseWriter records the response status and byte count for the access
// log. It implements Unwrap so http.NewResponseController (used to clear the write
// deadline for long streams) and optional interfaces like http.Flusher still reach
// the underlying writer.
type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *loggingResponseWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *loggingResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests wraps the mux with a debug-level access log: one correlated line per
// request once it completes, capturing the Range header, status, bytes and
// duration. At info level it is silent. A hung seek shows as a long duration_ms
// with few bytes; a paused stream shows as a short-lived request that ended early.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lw := &loggingResponseWriter{ResponseWriter: w}
		next.ServeHTTP(lw, r)
		s.logger.Debug("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"range", r.Header.Get("Range"),
			"status", lw.status,
			"bytes", lw.bytes,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

func (s *Server) hlsManifestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.hls.ServeManifest(w, r, s.proxyBase(r))
}

func (s *Server) hlsSegmentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.hls.ServeSegment(w, r, s.proxyBase(r))
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
		s.logPlaybackResponse(videoReq.Source, metadata)
		writeVideoResponse(w, videoReq.Source, metadata)
		return
	}

	// Cache miss: extract metadata + a stream URL, then route by the kind of
	// stream. Progressive files download to the disk cache; HLS/DASH take their
	// own manifest-aware paths.
	ext, err := s.extract.Extract(r.Context(), videoReq.URL)
	if err != nil {
		s.logger.Error("extraction failed", "url", videoReq.URL, "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Debug("extracted stream", "url", videoReq.URL, "endpoint", ext.Endpoint, "is_live", ext.IsLive)

	switch {
	case ext.Endpoint == EndpointProgressive:
		s.serveProgressiveMiss(w, r, id, videoReq, ext)
	case (ext.Endpoint == EndpointHLS || ext.Endpoint == EndpointDASH) && ext.IsLive:
		// Live HLS is rewritten and live DASH is converted to HLS, both behind the
		// /hls/manifest endpoint which sniffs the manifest type.
		s.serveLiveManifestMiss(w, r, videoReq, ext)
	case ext.Endpoint == EndpointHLS || ext.Endpoint == EndpointDASH:
		// VOD HLS/DASH: remux into the disk cache and serve like a progressive file.
		s.serveRemuxMiss(w, r, id, videoReq, ext)
	default:
		http.Error(w, "unsupported stream type", http.StatusNotImplemented)
	}
}

// serveLiveManifestMiss returns a yt-dlp-like document whose url points at a
// proxied manifest. The player (AVPro) plays the HLS directly; /hls/manifest
// rewrites HLS or converts DASH and proxies every child manifest and segment back
// through this server.
func (s *Server) serveLiveManifestMiss(w http.ResponseWriter, r *http.Request, videoReq videoRequest, ext Extraction) {
	manifestURL := s.hls.manifestURL(s.proxyBase(r), ext.StreamURL, headerMap(ext.Headers))

	metadata := ext.Metadata
	replaceStreamURLs(metadata, manifestURL)
	metadata["original_url"] = videoReq.URL
	metadata["_cache"] = "live"
	s.logPlaybackResponse(videoReq.Source, metadata)
	writeVideoResponse(w, videoReq.Source, metadata)
}

// serveRemuxMiss starts an ffmpeg remux of a VOD HLS/DASH manifest into a single
// MP4 on disk and returns the live streaming URL, exactly like a progressive
// miss. Once the remux finishes, subsequent requests are cache hits with full
// Range/seek support.
func (s *Server) serveRemuxMiss(w http.ResponseWriter, r *http.Request, id string, videoReq videoRequest, ext Extraction) {
	s.jobs.getOrStartRemux(id, ext.StreamURL, ext.Transcode, headerMap(ext.Headers))

	metadata := ext.Metadata
	replaceStreamURLs(metadata, s.playableURL(r, "/live/", id))
	metadata["ext"] = "mp4"
	metadata["original_url"] = videoReq.URL
	metadata["_cache"] = "miss"
	s.logPlaybackResponse(videoReq.Source, metadata)
	writeVideoResponse(w, videoReq.Source, metadata)
}

// serveProgressiveMiss handles a cache miss for a single progressive file: it
// starts a background download and returns the live streaming URL.
func (s *Server) serveProgressiveMiss(w http.ResponseWriter, r *http.Request, id string, videoReq videoRequest, ext Extraction) {
	s.jobs.getOrStart(id, ext.StreamURL)

	metadata := ext.Metadata
	replaceStreamURLs(metadata, s.playableURL(r, "/live/", id))
	metadata["original_url"] = videoReq.URL
	metadata["_cache"] = "miss"
	s.logPlaybackResponse(videoReq.Source, metadata)
	writeVideoResponse(w, videoReq.Source, metadata)
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

	serveCachedFile(w, r, path, s.logger.With("id", id))
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

	reqLog := s.logger.With("id", id, "range", r.Header.Get("Range"))

	// If the download already completed, serve the finished file with Range
	// support instead of tailing.
	if s.serveIfCached(w, r, id) {
		reqLog.Debug("live request served from cache", "mode", "cached")
		return
	}

	job, ok := s.jobs.getJob(id)
	if !ok {
		// No active job and not cached: maybe it finished after the Has() check.
		if !s.serveIfCached(w, r, id) {
			reqLog.Debug("live request: no job and not cached", "status", http.StatusNotFound)
			http.NotFound(w, r)
		}
		return
	}

	// Wait for the upstream probe to determine the playback mode.
	select {
	case <-job.ready:
	case <-r.Context().Done():
		reqLog.Debug("client gone while awaiting probe")
		return
	}

	if job.probeErr != nil {
		reqLog.Warn("upstream probe failed", "error", job.probeErr)
		if !s.serveIfCached(w, r, id) {
			http.Error(w, "upstream unavailable", http.StatusBadGateway)
		}
		return
	}

	// The download may have finished while we waited.
	if s.serveIfCached(w, r, id) {
		reqLog.Debug("live request served from cache (finished during probe)", "mode", "cached")
		return
	}

	if job.rangeable && job.sf != nil {
		reqLog.Debug("routing live request", "mode", "sparse")
		serveSparse(w, r, job, s.jobs, reqLog)
		return
	}
	reqLog.Debug("routing live request", "mode", "sequential")
	serveLive(w, r, job, reqLog)
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
	serveCachedFile(w, r, path, s.logger.With("id", id))
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

// proxyBase returns the scheme://host prefix of this server as seen by the
// client, used to build absolute proxied manifest/segment URLs.
func (s *Server) proxyBase(r *http.Request) string {
	u := url.URL{Scheme: requestScheme(r), Host: r.Host}
	return u.String()
}

func (s *Server) logPlaybackResponse(source string, metadata ytdlpMetadata) {
	playURL, _ := stringField(metadata, "url")
	cacheState, _ := stringField(metadata, "_cache")
	originalURL, _ := stringField(metadata, "original_url")
	s.logger.Info("returning playback url",
		"source", source,
		"cache", cacheState,
		"url", redactURLForLog(playURL),
		"original_url", redactURLForLog(originalURL),
	)
}

// writeVideoResponse returns the resolved playback location to the wrapper in the
// shape the caller's player expects. VRChat invokes yt-dlp expecting a single
// plain-text URL on stdout, so for that source (the default) we emit only the
// proxied url. Resonite invokes yt-dlp with -J and parses the full document, so
// resonite keeps the yt-dlp-like JSON metadata.
func writeVideoResponse(w http.ResponseWriter, source string, metadata ytdlpMetadata) {
	if source == "resonite" {
		writeJSON(w, metadata)
		return
	}
	playURL, _ := stringField(metadata, "url")
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
