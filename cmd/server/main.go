// Command server is the vrc-video-proxy HTTP server: it resolves a source URL
// with yt-dlp and streams the resulting bytes back to the player, without caching
// or transcoding.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"vrc-video-proxy/internal/config"
	"vrc-video-proxy/internal/core"
	"vrc-video-proxy/internal/httpx"
	"vrc-video-proxy/internal/logging"
)

func main() {
	// Bootstrap logger at info until the configured level is known; config errors
	// are reported through it before the real logger exists.
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.LoadConfig(os.Args[1:])
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(2)
	}

	// LoadConfig has validated the level, so this cannot fail.
	level, _ := logging.ParseLevel(cfg.LogLevel)
	logger = logging.New(os.Stdout, level)
	slog.SetDefault(logger)
	logger.Debug("logging configured", "level", level.String())

	client := configureProxy(logger, cfg)

	if err := checkYtdlp(exec.LookPath, cfg.YtdlpPath); err != nil {
		logger.Error("failed startup dependency check", "error", err)
		os.Exit(1)
	}

	srv := core.NewServer(cfg, logger, client)

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// The stream handler clears its per-request write deadline, so long video
		// streams survive this timeout; it still bounds short request handling.
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	os.Exit(runServer(logger, server, cfg))
}

// configureProxy builds the upstream HTTP client, routing it through cfg.Proxy when
// set and exporting the proxy environment variables the yt-dlp subprocess inherits.
// cfg.Proxy was already validated by LoadConfig, so ParseProxyURL cannot fail here.
func configureProxy(logger *slog.Logger, cfg config.Config) *http.Client {
	proxyURL, _ := config.ParseProxyURL(cfg.Proxy)
	if proxyURL == nil {
		return httpx.NewUpstreamClient(nil)
	}

	switch proxyURL.Scheme {
	case "socks5", "socks5h":
		os.Setenv("socks_proxy", cfg.Proxy)
	default:
		os.Setenv("http_proxy", cfg.Proxy)
		os.Setenv("https_proxy", cfg.Proxy)
	}

	logger.Info("upstream proxy enabled", "scheme", proxyURL.Scheme, "host", proxyURL.Host)
	return httpx.NewUpstreamClient(proxyURL)
}

// checkYtdlp verifies the only executable the server cannot work without is on PATH.
func checkYtdlp(lookup func(string) (string, error), ytdlpPath string) error {
	path := ytdlpPath
	if path == "" {
		path = "yt-dlp"
	}
	if _, err := lookup(path); err != nil {
		return fmt.Errorf("missing required executable: %s", path)
	}
	return nil
}

func runServer(logger *slog.Logger, server *http.Server, cfg config.Config) int {
	serverDone := make(chan error, 1)
	go func() {
		logger.Info("starting web server", "listen", cfg.Listen)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverDone <- err
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case sig := <-signals:
		logger.Info("received signal", "signal", sig.String())
	case err := <-serverDone:
		if err != nil {
			logger.Error("web server failed", "error", err)
			return 1
		}
		return 0
	}

	shutdownServer(logger, server, cfg.ShutdownTimeout)
	if err := <-serverDone; err != nil {
		logger.Error("web server failed", "error", err)
		return 1
	}

	return 0
}

func shutdownServer(logger *slog.Logger, server *http.Server, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	logger.Info("stopping web server")
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("graceful shutdown failed, closing server", "error", err)
		_ = server.Close()
	}
}
