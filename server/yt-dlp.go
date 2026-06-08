package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"time"
)

const ytdlpTimeout = 30 * time.Second

// progressiveFormat prefers a single file that already contains both audio and
// video (so no muxing/transcoding is required), favouring MP4, then falling back
// to any combined stream, then yt-dlp's default best.
const progressiveFormat = "best[protocol^=http][acodec!=none][vcodec!=none][ext=mp4]/best[acodec!=none][vcodec!=none]/best"

type ytdlpMetadata map[string]any

// ytdlpExtractor is the default Extractor: it shells out to the yt-dlp binary.
type ytdlpExtractor struct{}

func (ytdlpExtractor) Extract(ctx context.Context, cfg Config, rawURL string) (Extraction, error) {
	ctx, cancel := context.WithTimeout(ctx, ytdlpTimeout)
	defer cancel()

	ytdlpPath := cfg.YtdlpPath
	if ytdlpPath == "" {
		ytdlpPath = "yt-dlp"
	}

	// progressiveFormat prefers a combined file but ends with "/best", so when a
	// source only offers HLS/DASH (e.g. live streams) yt-dlp still selects it and
	// reports its protocol, which classifyEndpoint then routes appropriately.
	args := []string{"-J", "--no-playlist", "--no-warnings", "-f", progressiveFormat}
	if cfg.CookiesFile != "" {
		args = append(args, "--cookies", cfg.CookiesFile)
	}
	args = append(args, "--", rawURL)

	cmd := exec.CommandContext(ctx, ytdlpPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	output, err := cmd.Output()
	if ctx.Err() != nil {
		return Extraction{}, fmt.Errorf("yt-dlp timed out: %w", ctx.Err())
	}
	if err != nil {
		return Extraction{}, fmt.Errorf("yt-dlp failed: %s", stderr.String())
	}

	var metadata ytdlpMetadata
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
		Endpoint:  classifyEndpoint(metadata, streamURL),
		IsLive:    boolField(metadata, "is_live"),
		Transcode: needsTranscode(metadata),
		Headers:   extractHeaders(metadata),
	}, nil
}

func findStreamURL(metadata ytdlpMetadata) (string, error) {
	if streamURL, ok := stringField(metadata, "url"); ok && isHTTPURL(streamURL) {
		return streamURL, nil
	}

	if downloads, ok := metadata["requested_downloads"].([]any); ok {
		for _, download := range downloads {
			if item, ok := download.(map[string]any); ok {
				if streamURL, ok := stringField(item, "url"); ok && isHTTPURL(streamURL) {
					return streamURL, nil
				}
			}
		}
	}

	if formats, ok := metadata["formats"].([]any); ok {
		// Prefer a progressive format that carries both audio and video so the
		// cached file is playable without muxing. Iterate from the end because
		// yt-dlp lists formats roughly worst-to-best.
		var fallback string
		for i := len(formats) - 1; i >= 0; i-- {
			item, ok := formats[i].(map[string]any)
			if !ok {
				continue
			}
			streamURL, ok := stringField(item, "url")
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

// isProgressiveFormat reports whether a yt-dlp format entry contains both an
// audio and a video codec (i.e. it is directly playable without muxing).
func isProgressiveFormat(item map[string]any) bool {
	acodec, _ := item["acodec"].(string)
	vcodec, _ := item["vcodec"].(string)
	return acodec != "" && acodec != "none" && vcodec != "" && vcodec != "none"
}

func replaceStreamURLs(metadata ytdlpMetadata, proxyURL string) {
	metadata["url"] = proxyURL

	if downloads, ok := metadata["requested_downloads"].([]any); ok {
		for _, download := range downloads {
			if item, ok := download.(map[string]any); ok {
				item["url"] = proxyURL
			}
		}
	}
}

func stringField(metadata map[string]any, key string) (string, bool) {
	value, ok := metadata[key].(string)
	return value, ok && value != ""
}

func isHTTPURL(rawURL string) bool {
	parsedURL, err := url.Parse(rawURL)
	return err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && parsedURL.Host != ""
}
