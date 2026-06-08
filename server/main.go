package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// Bootstrap logger at info until the configured level is known; config
	// errors are reported through it before the real logger exists.
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := LoadConfig(os.Args[1:])
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(2)
	}

	// LoadConfig has validated the level, so this cannot fail.
	level, _ := parseLogLevel(cfg.LogLevel)
	logger = newLogger(os.Stdout, level)
	slog.SetDefault(logger)
	logger.Debug("logging configured", "level", level.String())

	configureProxy(logger, cfg)

	if err := checkRequiredExecutables(exec.LookPath, cfg.YtdlpPath); err != nil {
		logger.Error("failed startup dependency check", "error", err)
		os.Exit(1)
	}
	if _, err := exec.LookPath(cfg.FfmpegPath); err != nil {
		logger.Warn("ffmpeg not found; HLS/DASH remux and transcoding will be unavailable, continuing", "ffmpeg", cfg.FfmpegPath)
	} else {
		// Probe and cache the preferred H.264 encoder once at startup.
		newFfmpegRunner(cfg.FfmpegPath, logger).h264Encoder()
	}
	if _, err := exec.LookPath(cfg.FfprobePath); err != nil {
		logger.Warn("ffprobe not found; media probing will be unavailable, continuing", "ffprobe", cfg.FfprobePath)
	}

	srv, err := NewServer(cfg, logger)
	if err != nil {
		logger.Error("failed to initialise server", "error", err)
		os.Exit(1)
	}
	logger.Info("cache ready", "dir", cfg.CacheDir, "max_size_bytes", cfg.CacheMaxSize)

	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	exitCode := runServer(logger, server, cfg)
	os.Exit(exitCode)
}

// configureProxy routes all upstream traffic and tools through cfg.Proxy when set.
// It reassigns the shared Go client (done before the server goroutine starts, so it
// is single-goroutine and race-free) and exports the proxy environment variables the
// yt-dlp and ffmpeg subprocesses inherit. cfg.Proxy was already validated by
// LoadConfig, so parseProxyURL cannot fail here.
func configureProxy(logger *slog.Logger, cfg Config) {
	proxyURL, _ := parseProxyURL(cfg.Proxy)
	if proxyURL == nil {
		return
	}

	sharedHTTPClient = newUpstreamHTTPClient(proxyURL)

	switch proxyURL.Scheme {
	case "socks5", "socks5h":
		// ffmpeg reads socks_proxy for SOCKS5 input URLs.
		os.Setenv("socks_proxy", cfg.Proxy)
	default:
		// ffmpeg (and other CONNECT-based tooling) reads the lowercase env vars.
		os.Setenv("http_proxy", cfg.Proxy)
		os.Setenv("https_proxy", cfg.Proxy)
	}

	logger.Info("upstream proxy enabled", "scheme", proxyURL.Scheme, "host", proxyURL.Host)
}

func runServer(logger *slog.Logger, server *http.Server, cfg Config) int {
	serverDone := make(chan error, 1)
	go func() {
		logger.Info("starting web server", "listen", cfg.Listen)
		err := server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serverDone <- err
	}()

	if len(cfg.GameCommand) > 0 {
		return runWithGameCommand(logger, server, cfg, serverDone)
	}

	return runStandalone(logger, server, cfg, serverDone)
}

func runStandalone(logger *slog.Logger, server *http.Server, cfg Config, serverDone <-chan error) int {
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

func runWithGameCommand(logger *slog.Logger, server *http.Server, cfg Config, serverDone <-chan error) int {
	if err := waitForServer(cfg.Listen, cfg.ShutdownTimeout); err != nil {
		logger.Error("web server did not become ready", "listen", cfg.Listen, "error", err)
		shutdownServer(logger, server, cfg.ShutdownTimeout)
		_ = <-serverDone
		return 1
	}

	cmd := exec.Command(cfg.GameCommand[0], cfg.GameCommand[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.Env = os.Environ()
	configureCommand(cmd)

	if err := cmd.Start(); err != nil {
		logger.Error("failed to start game command", "error", err)
		shutdownServer(logger, server, cfg.ShutdownTimeout)
		_ = <-serverDone
		return 1
	}

	logger.Info("started game command", "pid", cmd.Process.Pid, "command", cfg.GameCommand[0])

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	gameDone := make(chan error, 1)
	go func() {
		gameDone <- cmd.Wait()
	}()

	exitCode := 0
	select {
	case sig := <-signals:
		logger.Info("received signal, stopping game", "signal", sig.String())
		stopProcess(cmd.Process, logger)
		if err := <-gameDone; err != nil {
			exitCode = processExitCode(err)
		}
	case err := <-gameDone:
		if err != nil {
			exitCode = processExitCode(err)
			logger.Info("game command exited with error", "error", err, "exit_code", exitCode)
		} else {
			logger.Info("game command exited")
		}
	case err := <-serverDone:
		logger.Error("web server exited before game finished", "error", err)
		stopProcess(cmd.Process, logger)
		if gameErr := <-gameDone; gameErr != nil {
			exitCode = processExitCode(gameErr)
		}
		if exitCode == 0 {
			exitCode = 1
		}
		return exitCode
	}

	shutdownServer(logger, server, cfg.ShutdownTimeout)
	if err := <-serverDone; err != nil && exitCode == 0 {
		logger.Error("web server failed", "error", err)
		exitCode = 1
	}

	return exitCode
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

func waitForServer(listen string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", listen, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}

	if lastErr == nil {
		return errors.New("timeout")
	}
	return lastErr
}

func processExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}
