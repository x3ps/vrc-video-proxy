// Command wrapper is a yt-dlp-style stub that VRChat invokes instead of yt-dlp.
// It infers the source URL and player flags from the argv, asks a vrc-video-proxy
// server to resolve it, and writes the server's response to stdout.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"vrc-video-proxy/internal/logging"
	"vrc-video-proxy/internal/vrcclient"
)

const (
	defaultServerURL = "http://127.0.0.1:8080"

	envServerURL = "VRCVP_SERVER_URL"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, http.DefaultClient))
}

func run(args []string, stdout, stderr io.Writer, httpClient *http.Client) int {
	serverURL := strings.TrimSpace(os.Getenv(envServerURL))
	if serverURL == "" {
		serverURL = defaultServerURL
	}

	logger, flush := newLogger(stderr, parseLogLevel(os.Getenv(envLogLevel)))
	defer flush()

	req, err := vrcclient.ParseRequest(args)
	if err != nil {
		logger.Error("failed to parse arguments", "error", err, "args", args)
		return 1
	}
	logger.Debug("parsed request", "url", req.URL, "avpro", req.AVPro, "source", req.Source)

	body, err := vrcclient.New(serverURL, httpClient).Resolve(context.Background(), req)
	if err != nil {
		logger.Error("failed to fetch video", "url", req.URL, "server", serverURL, "error", err)
		return 1
	}
	logger.Info("fetched video",
		"url", req.URL,
		"bytes", len(body),
		"playback_url", logging.RedactURL(strings.TrimSpace(string(body))),
	)

	if _, err := stdout.Write(body); err != nil {
		logger.Error("failed to write response", "error", err)
		return 1
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		_, _ = fmt.Fprintln(stdout)
	}
	return 0
}
