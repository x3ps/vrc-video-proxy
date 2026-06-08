package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"time"
)

const defaultYtdlpPath = "yt-dlp"

const ytdlpTimeout = 30 * time.Second

// ytdlpProbeTimeout bounds the ffprobe codec check run after yt-dlp resolves a
// stream URL, so probing cannot dominate the request even on slow CDNs.
const ytdlpProbeTimeout = 10 * time.Second

// progressiveFormat prefers a single file that already contains both audio and
// video (so no muxing/transcoding is required), favouring MP4, then falling back
// to any combined stream, then yt-dlp's default best.
const progressiveFormat = "best[protocol^=http][acodec!=none][vcodec!=none][ext=mp4]/best[acodec!=none][vcodec!=none]/best"

type ytdlpMetadata map[string]any

// ytdlpRunner is the default Extractor: it shells out to the yt-dlp binary and,
// when yt-dlp does not report the resolved stream's codecs, falls back to ffprobe
// to detect them. It mirrors the ffprobe/ffmpeg runners: configuration and the
// logger live on the struct, so Extract takes only the per-request URL.
type ytdlpRunner struct {
	path        string
	logger      *slog.Logger
	format      string   // -f selector; defaults to progressiveFormat
	extraArgs   []string // raw passthrough flags
	cookiesFile string
	proxy       string
	timeout     time.Duration
	ffprobe     *ffprobeRunner
}

// newYtdlpRunner builds a ytdlpRunner from configuration, defaulting the binary
// path, format selector and timeout, and wiring an ffprobeRunner (sharing the
// configured ffprobe path) for codec detection.
func newYtdlpRunner(cfg Config, logger *slog.Logger) *ytdlpRunner {
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
	return &ytdlpRunner{
		path:        binPath,
		logger:      logger,
		format:      format,
		extraArgs:   cfg.YtdlpExtraArgs,
		cookiesFile: cfg.CookiesFile,
		proxy:       cfg.Proxy,
		timeout:     ytdlpTimeout,
		ffprobe:     newFfprobeRunner(cfg.FfprobePath, logger),
	}
}

func (y *ytdlpRunner) Extract(ctx context.Context, rawURL string) (Extraction, error) {
	// reqCtx is the unbounded request context; the ffprobe codec check derives its
	// own timeout from it rather than from yt-dlp's already-consumed one.
	reqCtx := ctx

	ctx, cancel := context.WithTimeout(ctx, y.timeout)
	defer cancel()

	y.logger.Debug("running yt-dlp", "url", redactURLForLog(rawURL))

	args := y.commandArgs(rawURL)

	cmd := exec.CommandContext(ctx, y.path, args...)
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

	endpoint := classifyEndpoint(metadata, streamURL)
	isLive := boolField(metadata, "is_live")
	headers := extractHeaders(metadata)

	return Extraction{
		Metadata:  metadata,
		StreamURL: streamURL,
		Endpoint:  endpoint,
		IsLive:    isLive,
		Transcode: y.decideTranscode(reqCtx, metadata, streamURL, endpoint, isLive, headers),
		Headers:   headers,
	}, nil
}

func (y *ytdlpRunner) commandArgs(rawURL string) []string {
	// progressiveFormat prefers a combined file but ends with "/best", so when a
	// source only offers HLS/DASH (e.g. live streams) yt-dlp still selects it and
	// reports its protocol, which classifyEndpoint then routes appropriately.
	args := []string{"-J", "--no-playlist", "--no-warnings", "-f", y.format}
	args = append(args, y.extraArgs...)
	if y.cookiesFile != "" {
		args = append(args, "--cookies", y.cookiesFile)
	}
	if y.proxy != "" {
		args = append(args, "--proxy", y.proxy)
	}
	return append(args, "--", rawURL)
}

// decideTranscode reports whether the resolved stream must be re-encoded. yt-dlp's
// reported codecs are authoritative when present; for any codec yt-dlp leaves
// unknown it probes the resolved URL with ffprobe to learn the real codec.
// Probing is best-effort and skipped where it is unreliable: any failure, or a
// live/HLS/DASH endpoint, falls back to whatever yt-dlp reported (unknown codecs
// are assumed compatible, so we never re-encode on pure guesswork).
func (y *ytdlpRunner) decideTranscode(ctx context.Context, metadata ytdlpMetadata, streamURL string, endpoint Endpoint, isLive bool, headers http.Header) bool {
	vcodec, _ := stringField(metadata, "vcodec")
	acodec, _ := stringField(metadata, "acodec")

	if (vcodec == "" || acodec == "") && endpoint == EndpointProgressive && !isLive {
		probeCtx, cancel := context.WithTimeout(ctx, ytdlpProbeTimeout)
		defer cancel()
		if res, err := y.ffprobe.probe(probeCtx, streamURL, headerMap(headers)); err == nil && res != nil {
			if vcodec == "" {
				if v := res.videoStream(); v != nil {
					vcodec = v.CodecName
				}
			}
			if acodec == "" {
				if a := res.audioStream(); a != nil {
					acodec = a.CodecName
				}
			}
		} else {
			y.logger.Debug("codec probe failed; using yt-dlp metadata",
				"url", redactURLForLog(streamURL), "error", err)
		}
	}

	return transcodeFromCodecs(vcodec, acodec)
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
