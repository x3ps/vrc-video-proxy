// Package core is the HTTP API and the passthrough byte proxy. It resolves a
// source URL with the extractor, stashes the resolved upstream stream in an
// in-memory handle store, and streams the bytes back to the player on demand —
// no caching, no transcoding.
package core

import (
	"log/slog"
	"net/http"
	"time"

	"vrc-video-proxy/internal/config"
	"vrc-video-proxy/internal/extractor"
	"vrc-video-proxy/internal/httpx"
)

// Server holds the shared state for the HTTP handlers.
type Server struct {
	cfg     config.Config
	logger  *slog.Logger
	extract extractor.Extractor
	client  *http.Client
	store   *streamStore
}

// NewServer wires the extractor, the upstream client and the stream handle store
// for the given config. A nil client falls back to a default upstream client.
func NewServer(cfg config.Config, logger *slog.Logger, client *http.Client) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if client == nil {
		client = httpx.NewUpstreamClient(nil)
	}
	return &Server{
		cfg:     cfg,
		logger:  logger,
		extract: extractor.New(cfg, logger),
		client:  client,
		store:   newStreamStore(cfg.StreamTTL),
	}
}

// Handler returns the configured mux for the server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/api/getvideo", s.getVideoHandler)
	mux.HandleFunc("/stream/", s.streamHandler)
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
// duration. At info level it is silent.
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
