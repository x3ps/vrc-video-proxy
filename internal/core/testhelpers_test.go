package core

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"vrc-video-proxy/internal/config"
	"vrc-video-proxy/internal/httpx"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestServer builds a Server with a real handle store and upstream client but
// no extractor wired; tests that exercise getVideoHandler set srv.extract.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		cfg:    config.Config{},
		logger: discardLogger(),
		client: httpx.NewUpstreamClient(nil),
		store:  newStreamStore(30 * time.Minute),
	}
}
