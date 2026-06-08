package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultServerURL = "http://127.0.0.1:8080"
	defaultTimeout   = 30 * time.Second

	envServerURL = "VRCVP_SERVER_URL"
)

type wrapperRequest struct {
	URL    string
	AVPro  bool
	Source string
}

func main() {
	exitCode := run(os.Args[1:], os.Stdout, os.Stderr, http.DefaultClient)
	os.Exit(exitCode)
}

func run(args []string, stdout, stderr io.Writer, client *http.Client) int {
	serverURL := strings.TrimSpace(os.Getenv(envServerURL))
	if serverURL == "" {
		serverURL = defaultServerURL
	}

	logger, flush := newLogger(stderr, parseLogLevel(os.Getenv(envLogLevel)))
	defer flush()

	req, err := parseWrapperRequest(args)
	if err != nil {
		logger.Error("failed to parse arguments", "error", err, "args", args)
		return 1
	}
	logger.Debug("parsed request", "url", req.URL, "avpro", req.AVPro, "source", req.Source)

	body, err := fetchVideo(context.Background(), client, serverURL, req)
	if err != nil {
		logger.Error("failed to fetch video", "url", req.URL, "server", serverURL, "error", err)
		return 1
	}
	logger.Info("fetched video", "url", req.URL, "bytes", len(body))

	if _, err := stdout.Write(body); err != nil {
		logger.Error("failed to write response", "error", err)
		return 1
	}
	if len(body) == 0 || body[len(body)-1] != '\n' {
		_, _ = fmt.Fprintln(stdout)
	}
	return 0
}

func parseWrapperRequest(args []string) (wrapperRequest, error) {
	req := wrapperRequest{
		AVPro:  true,
		Source: "vrchat",
	}

	for _, arg := range args {
		if strings.Contains(arg, "[protocol^=http]") {
			req.AVPro = false
			continue
		}
		if strings.Contains(arg, "--flat-playlist") {
			req.Source = "resonite"
			continue
		}
		if isAbsoluteHTTPURL(arg) && req.URL == "" {
			req.URL = arg
		}
	}

	if req.URL == "" {
		return wrapperRequest{}, errors.New("no http(s) URL found in arguments")
	}
	return req, nil
}

func fetchVideo(ctx context.Context, client *http.Client, serverURL string, req wrapperRequest) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}

	endpoint, err := getVideoEndpoint(serverURL, req)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to contact server at %s: %w", serverURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read server response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return nil, fmt.Errorf("server returned %s: %s", resp.Status, message)
	}
	return body, nil
}

func getVideoEndpoint(serverURL string, req wrapperRequest) (string, error) {
	base, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid %s %q", envServerURL, serverURL)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", fmt.Errorf("%s must use http or https", envServerURL)
	}

	base.Path = strings.TrimRight(base.Path, "/") + "/api/getvideo"
	query := base.Query()
	query.Set("url", req.URL)
	query.Set("avpro", fmt.Sprintf("%t", req.AVPro))
	query.Set("source", req.Source)
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func isAbsoluteHTTPURL(rawURL string) bool {
	parsedURL, err := url.Parse(rawURL)
	return err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && parsedURL.Host != ""
}
