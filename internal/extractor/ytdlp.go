package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"time"

	"vrc-video-proxy/internal/config"
	"vrc-video-proxy/internal/logging"
)

const defaultYtdlpPath = "yt-dlp"

const ytdlpTimeout = 30 * time.Second

// progressiveFormat prefers a single file that already contains both audio and
// video, favouring MP4, then falling back to any combined stream, then yt-dlp's
// default best. A progressive file streams cleanly through the byte proxy without
// any muxing on our side.
const progressiveFormat = "best[protocol^=http][acodec!=none][vcodec!=none][ext=mp4]/best[acodec!=none][vcodec!=none]/best"

// Runner is the default Extractor: it shells out to the yt-dlp binary and returns
// the resolved stream URL plus the headers to replay. Configuration and the logger
// live on the struct, so Extract takes only the per-request URL.
type Runner struct {
	path        string
	logger      *slog.Logger
	format      string   // -f selector; defaults to progressiveFormat
	extraArgs   []string // raw passthrough flags
	cookiesFile string
	proxy       string
	timeout     time.Duration
}

// New builds a Runner from configuration, defaulting the binary path, format
// selector and timeout.
func New(cfg config.Config, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	binPath := cfg.YtdlpPath
	if binPath == "" {
		binPath = defaultYtdlpPath
	}
	format := cfg.YtdlpFormat
	if format == "" {
		format = progressiveFormat
	}
	return &Runner{
		path:        binPath,
		logger:      logger,
		format:      format,
		extraArgs:   cfg.YtdlpExtraArgs,
		cookiesFile: cfg.CookiesFile,
		proxy:       cfg.Proxy,
		timeout:     ytdlpTimeout,
	}
}

func (r *Runner) Extract(ctx context.Context, rawURL string) (Extraction, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	r.logger.Debug("running yt-dlp", "url", logging.RedactURL(rawURL))

	cmd := exec.CommandContext(ctx, r.path, r.commandArgs(rawURL)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if ctx.Err() != nil {
		return Extraction{}, fmt.Errorf("yt-dlp timed out: %w", ctx.Err())
	}
	if err != nil {
		return Extraction{}, fmt.Errorf("yt-dlp failed: %s", stderr.String())
	}

	var metadata Metadata
	if err := json.Unmarshal(output, &metadata); err != nil {
		return Extraction{}, fmt.Errorf("failed to parse yt-dlp JSON: %w", err)
	}

	streamURL, err := findStreamURL(metadata)
	if err != nil {
		return Extraction{}, err
	}

	return Extraction{
		Metadata:  metadata,
		StreamURL: streamURL,
		Headers:   extractHeaders(metadata),
	}, nil
}

func (r *Runner) commandArgs(rawURL string) []string {
	args := []string{"-J", "--no-playlist", "--no-warnings", "-f", r.format}
	args = append(args, r.extraArgs...)
	if r.cookiesFile != "" {
		args = append(args, "--cookies", r.cookiesFile)
	}
	if r.proxy != "" {
		args = append(args, "--proxy", r.proxy)
	}
	return append(args, "--", rawURL)
}

func findStreamURL(metadata Metadata) (string, error) {
	if streamURL, ok := StringField(metadata, "url"); ok && isHTTPURL(streamURL) {
		return streamURL, nil
	}

	if downloads, ok := metadata["requested_downloads"].([]any); ok {
		for _, download := range downloads {
			if item, ok := download.(map[string]any); ok {
				if streamURL, ok := StringField(item, "url"); ok && isHTTPURL(streamURL) {
					return streamURL, nil
				}
			}
		}
	}

	if formats, ok := metadata["formats"].([]any); ok {
		// Prefer a progressive format that carries both audio and video so the
		// stream is playable without muxing. Iterate from the end because yt-dlp
		// lists formats roughly worst-to-best.
		var fallback string
		for i := len(formats) - 1; i >= 0; i-- {
			item, ok := formats[i].(map[string]any)
			if !ok {
				continue
			}
			streamURL, ok := StringField(item, "url")
			if !ok || !isHTTPURL(streamURL) {
				continue
			}
			if fallback == "" {
				fallback = streamURL
			}
			if isProgressiveFormat(item) {
				return streamURL, nil
			}
		}
		if fallback != "" {
			return fallback, nil
		}
	}

	return "", fmt.Errorf("yt-dlp JSON does not contain a playable stream URL")
}

// isProgressiveFormat reports whether a yt-dlp format entry contains both an audio
// and a video codec (i.e. it is directly playable without muxing).
func isProgressiveFormat(item map[string]any) bool {
	acodec, _ := item["acodec"].(string)
	vcodec, _ := item["vcodec"].(string)
	return acodec != "" && acodec != "none" && vcodec != "" && vcodec != "none"
}

func isHTTPURL(rawURL string) bool {
	parsedURL, err := url.Parse(rawURL)
	return err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && parsedURL.Host != ""
}
