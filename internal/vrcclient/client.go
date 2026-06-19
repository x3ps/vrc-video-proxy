package vrcclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

// Client resolves a Request against a vrc-video-proxy server's /api/getvideo
// endpoint and returns the raw response body verbatim (a plain-text playback URL
// for vrchat, or yt-dlp-like JSON for resonite).
type Client struct {
	serverURL string
	http      *http.Client
	timeout   time.Duration
}

// New builds a Client for serverURL. A nil httpClient falls back to
// http.DefaultClient. serverURL is validated lazily on Resolve.
func New(serverURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		serverURL: serverURL,
		http:      httpClient,
		timeout:   defaultTimeout,
	}
}

// Resolve calls /api/getvideo for req and returns the response body. A non-2xx
// status is reported as an error carrying the server's message.
func (c *Client) Resolve(ctx context.Context, req Request) ([]byte, error) {
	endpoint, err := c.endpoint(req)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to contact server at %s: %w", c.serverURL, err)
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

// endpoint builds the /api/getvideo URL for req against the configured server.
func (c *Client) endpoint(req Request) (string, error) {
	base, err := url.Parse(strings.TrimSpace(c.serverURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid server URL %q", c.serverURL)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", fmt.Errorf("server URL must use http or https")
	}

	base.Path = strings.TrimRight(base.Path, "/") + "/api/getvideo"
	query := base.Query()
	query.Set("url", req.URL)
	query.Set("avpro", fmt.Sprintf("%t", req.AVPro))
	query.Set("source", req.Source)
	for key, values := range req.Options {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	base.RawQuery = query.Encode()
	return base.String(), nil
}
