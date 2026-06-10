// Package extractor resolves a source URL to a playable stream URL plus the
// upstream request headers to replay, using yt-dlp.
package extractor

import (
	"context"
	"net/http"
)

// Metadata is the parsed yt-dlp JSON document. It is a plain map alias so callers
// can read and rewrite fields (e.g. the playback "url") directly.
type Metadata = map[string]any

// Extraction is the result of resolving a source URL. StreamURL is the resolved
// upstream URL to fetch; Headers carries the upstream request headers
// (User-Agent, Referer, Cookie) that must be replayed when fetching it; Metadata
// is the yt-dlp document returned verbatim to JSON-consuming clients.
type Extraction struct {
	Metadata  Metadata
	StreamURL string
	Headers   http.Header
}

// Extractor resolves a source URL to an Extraction. yt-dlp is the only
// implementation today; the interface keeps room for native per-host extractors
// behind the same shape.
type Extractor interface {
	Extract(ctx context.Context, rawURL string) (Extraction, error)
}

// Func adapts a plain function to the Extractor interface, mirroring
// http.HandlerFunc. Tests use it to inject fakes.
type Func func(ctx context.Context, rawURL string) (Extraction, error)

func (f Func) Extract(ctx context.Context, rawURL string) (Extraction, error) {
	return f(ctx, rawURL)
}

// extractHeaders pulls the upstream request headers yt-dlp reports for a stream
// (under "http_headers"), so downstream fetches replay the same UA/Referer/etc.
func extractHeaders(metadata Metadata) http.Header {
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

// ReplaceStreamURLs rewrites the playback URL fields in a yt-dlp document to point
// at proxyURL, so a JSON-consuming client (Resonite) plays through the proxy.
func ReplaceStreamURLs(metadata Metadata, proxyURL string) {
	metadata["url"] = proxyURL

	if downloads, ok := metadata["requested_downloads"].([]any); ok {
		for _, download := range downloads {
			if item, ok := download.(map[string]any); ok {
				item["url"] = proxyURL
			}
		}
	}
}

// StringField reads a non-empty string metadata value.
func StringField(metadata map[string]any, key string) (string, bool) {
	value, ok := metadata[key].(string)
	return value, ok && value != ""
}
