package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// Endpoint classifies how an extracted stream should be served. It is the
// discriminator that lets getVideoHandler route a result down the right path:
// progressive files keep the existing disk-cache download, while HLS/DASH take
// the manifest-aware paths.
type Endpoint int

const (
	// EndpointProgressive is a single combined file served via the disk cache.
	EndpointProgressive Endpoint = iota
	// EndpointHLS is an HLS (m3u8) manifest.
	EndpointHLS
	// EndpointDASH is an MPEG-DASH (mpd) manifest.
	EndpointDASH
)

func (e Endpoint) String() string {
	switch e {
	case EndpointHLS:
		return "hls"
	case EndpointDASH:
		return "dash"
	default:
		return "progressive"
	}
}

// Extraction is the result of resolving a source URL to a playable stream. The
// Endpoint and IsLive fields decide the serving strategy; Headers carries the
// upstream request headers (User-Agent, Referer, Cookie) that must be replayed
// when fetching the stream or its segments.
type Extraction struct {
	Metadata  ytdlpMetadata
	StreamURL string
	Endpoint  Endpoint
	IsLive    bool
	Transcode bool // remux path must re-encode (codecs not MP4-compatible)
	Headers   http.Header
}

// Extractor resolves a source URL to an Extraction. yt-dlp is the only
// implementation today; the interface keeps room for native per-host extractors
// behind the same shape (each can report a different Endpoint).
type Extractor interface {
	Extract(ctx context.Context, cfg Config, rawURL string, logger *slog.Logger) (Extraction, error)
}

// extractorFunc adapts a plain function to the Extractor interface, mirroring
// http.HandlerFunc. Tests use it to inject fakes.
type extractorFunc func(ctx context.Context, cfg Config, rawURL string, logger *slog.Logger) (Extraction, error)

func (f extractorFunc) Extract(ctx context.Context, cfg Config, rawURL string, logger *slog.Logger) (Extraction, error) {
	return f(ctx, cfg, rawURL, logger)
}

// classifyEndpoint determines the Endpoint for a chosen stream from the yt-dlp
// metadata protocol field and the URL itself. The protocol field is the most
// reliable signal; the URL suffix is a fallback for extractors that omit it.
func classifyEndpoint(metadata ytdlpMetadata, streamURL string) Endpoint {
	if protocol, ok := stringField(metadata, "protocol"); ok {
		switch e, ok := endpointFromProtocol(protocol); {
		case ok:
			return e
		}
	}
	return endpointFromURL(streamURL)
}

// endpointFromProtocol maps a yt-dlp "protocol" value to an Endpoint. yt-dlp uses
// values like "https", "m3u8_native", "m3u8", "http_dash_segments", "dash".
func endpointFromProtocol(protocol string) (Endpoint, bool) {
	p := strings.ToLower(protocol)
	switch {
	case strings.Contains(p, "m3u8"):
		return EndpointHLS, true
	case strings.Contains(p, "dash"):
		return EndpointDASH, true
	case strings.HasPrefix(p, "http"):
		return EndpointProgressive, true
	default:
		return EndpointProgressive, false
	}
}

// endpointFromURL classifies by the manifest file extension when the protocol is
// unknown. Query strings are ignored.
func endpointFromURL(streamURL string) Endpoint {
	path := streamURL
	if u, err := url.Parse(streamURL); err == nil {
		path = u.Path
	}
	switch {
	case strings.HasSuffix(path, ".m3u8"), strings.HasSuffix(path, ".m3u"):
		return EndpointHLS
	case strings.HasSuffix(path, ".mpd"):
		return EndpointDASH
	default:
		return EndpointProgressive
	}
}

// extractHeaders pulls the upstream request headers yt-dlp reports for a stream
// (under "http_headers"), so downstream fetches replay the same UA/Referer/etc.
func extractHeaders(metadata ytdlpMetadata) http.Header {
	raw, ok := metadata["http_headers"].(map[string]any)
	if !ok {
		return nil
	}
	h := make(http.Header, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			h.Set(k, s)
		}
	}
	if len(h) == 0 {
		return nil
	}
	return h
}

// needsTranscode reports whether the selected stream's codecs are not directly
// MP4-compatible, so the remux path must re-encode rather than copy. Unknown
// codecs are assumed compatible to avoid needless re-encoding.
func needsTranscode(metadata ytdlpMetadata) bool {
	vcodec, _ := stringField(metadata, "vcodec")
	acodec, _ := stringField(metadata, "acodec")
	return !mp4FriendlyVideo(vcodec) || !mp4FriendlyAudio(acodec)
}

func mp4FriendlyVideo(vcodec string) bool {
	c := strings.ToLower(vcodec)
	if c == "" || c == "none" {
		return true
	}
	return strings.HasPrefix(c, "avc") || strings.HasPrefix(c, "h264") || strings.HasPrefix(c, "mp4v")
}

func mp4FriendlyAudio(acodec string) bool {
	c := strings.ToLower(acodec)
	if c == "" || c == "none" {
		return true
	}
	return strings.HasPrefix(c, "mp4a") || strings.HasPrefix(c, "aac")
}

// boolField reads a boolean metadata value, tolerating yt-dlp's occasional use of
// other JSON types for the same key.
func boolField(metadata ytdlpMetadata, key string) bool {
	switch v := metadata[key].(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(v, "true")
	default:
		return false
	}
}
