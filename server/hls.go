package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
)

// hlsTokenTTL bounds how long a signed manifest/segment URL stays valid. Live
// players refetch the manifest well within this window.
const hlsTokenTTL = 6 * time.Hour

// maxConcurrentSegFetch caps simultaneous upstream segment/manifest fetches for
// the live HLS proxy, independent of the progressive downloader's cap so one live
// stream cannot starve downloads (and vice versa).
const maxConcurrentSegFetch = 6

// maxManifestSize bounds a fetched manifest to guard against absurd inputs.
const maxManifestSize = 16 << 20

// hlsKind selects which proxy endpoint a rewritten URI points at.
type hlsKind int

const (
	kindManifest hlsKind = iota // a child playlist (variant / rendition)
	kindSegment                 // a media segment, init section, or key
)

// proxyPayload is what a signed token carries: the absolute upstream URL plus the
// request headers to replay. It is encrypted inside the token, so cookies and
// auth headers are not exposed.
type proxyPayload struct {
	URL     string            `json:"u"`
	Headers map[string]string `json:"h,omitempty"`
}

// hlsProxy rewrites live HLS manifests and proxies their segments, caching
// segments in memory and coalescing concurrent identical fetches.
type hlsProxy struct {
	signer *URLSigner
	cache  *MemCache
	client *http.Client
	logger *slog.Logger
	sem    chan struct{}
	group  singleflight.Group
}

func newHLSProxy(signer *URLSigner, cache *MemCache, logger *slog.Logger) *hlsProxy {
	if logger == nil {
		logger = slog.Default()
	}
	return &hlsProxy{
		signer: signer,
		cache:  cache,
		client: sharedHTTPClient,
		logger: logger,
		sem:    make(chan struct{}, maxConcurrentSegFetch),
	}
}

// encodeToken seals an upstream URL + headers into a signed token.
func (p *hlsProxy) encodeToken(rawURL string, headers map[string]string) string {
	payload, _ := json.Marshal(proxyPayload{URL: rawURL, Headers: headers})
	return p.signer.Sign(string(payload), hlsTokenTTL)
}

// decodeToken verifies a token and returns its payload.
func (p *hlsProxy) decodeToken(token string) (proxyPayload, error) {
	raw, err := p.signer.Verify(token)
	if err != nil {
		return proxyPayload{}, err
	}
	var pl proxyPayload
	if err := json.Unmarshal([]byte(raw), &pl); err != nil {
		return proxyPayload{}, fmt.Errorf("decode payload: %w", err)
	}
	return pl, nil
}

// manifestURL builds a proxied /hls/manifest URL for an upstream playlist.
func (p *hlsProxy) manifestURL(proxyBase, rawURL string, headers map[string]string) string {
	return proxyBase + "/hls/manifest?t=" + url.QueryEscape(p.encodeToken(rawURL, headers))
}

// segmentURL builds a proxied /hls/segment URL for an upstream segment/key/init.
func (p *hlsProxy) segmentURL(proxyBase, rawURL string, headers map[string]string) string {
	return proxyBase + "/hls/segment?t=" + url.QueryEscape(p.encodeToken(rawURL, headers))
}

// uriAttrRe matches a quoted URI="..." attribute on HLS tag lines.
var uriAttrRe = regexp.MustCompile(`URI="([^"]*)"`)

// rewriteHLSPlaylist rewrites every URI in an m3u8 so it points back at this
// proxy. It is line-based (lossless, tolerant of non-standard IPTV playlists like
// MediaFlow's fallback parser): bare URI lines become segment or child-manifest
// links depending on whether this is a master playlist, and URI="..." attributes
// on EXT-X-KEY/MEDIA/MAP/etc. are rewritten in place. build resolves each
// absolute upstream URL to a proxied URL of the given kind.
func rewriteHLSPlaylist(body []byte, baseURL string, build func(absURL string, kind hlsKind) string) ([]byte, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	master := bytes.Contains(body, []byte("#EXT-X-STREAM-INF"))

	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), maxManifestSize)
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			out.WriteString(line)
		case strings.HasPrefix(trimmed, "#"):
			out.WriteString(rewriteTagLine(line, base, build))
		default:
			kind := kindSegment
			if master {
				kind = kindManifest
			}
			out.WriteString(build(resolveRef(base, trimmed), kind))
		}
		out.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan playlist: %w", err)
	}
	return out.Bytes(), nil
}

// rewriteTagLine rewrites a URI="..." attribute on an HLS tag line, if present.
// EXT-X-MEDIA and I-frame stream tags reference child playlists; everything else
// carrying a URI (KEY, MAP, PART, PRELOAD-HINT) references a segment-like object.
func rewriteTagLine(line string, base *url.URL, build func(string, hlsKind) string) string {
	upper := strings.ToUpper(strings.TrimSpace(line))
	kind := kindSegment
	if strings.HasPrefix(upper, "#EXT-X-MEDIA") || strings.HasPrefix(upper, "#EXT-X-I-FRAME-STREAM-INF") {
		kind = kindManifest
	}
	return uriAttrRe.ReplaceAllStringFunc(line, func(m string) string {
		ref := uriAttrRe.FindStringSubmatch(m)[1]
		return `URI="` + build(resolveRef(base, ref), kind) + `"`
	})
}

// resolveRef resolves a possibly-relative URI against the manifest's base URL.
func resolveRef(base *url.URL, ref string) string {
	if base == nil {
		return ref
	}
	u, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}

// ServeManifest handles GET /hls/manifest: fetch the upstream playlist, rewrite
// its URIs to proxied URLs, and return it. Live manifests are never cached so the
// player always sees the latest sliding window.
func (p *hlsProxy) ServeManifest(w http.ResponseWriter, r *http.Request, proxyBase string) {
	pl, ok := p.payloadFromRequest(w, r)
	if !ok {
		return
	}

	body, _, err := p.fetch(r.Context(), pl)
	if err != nil {
		p.logger.Error("hls manifest fetch failed", "url", pl.URL, "error", err)
		http.Error(w, "upstream manifest unavailable", http.StatusBadGateway)
		return
	}
	if int64(len(body)) > maxManifestSize {
		http.Error(w, "manifest too large", http.StatusBadGateway)
		return
	}

	// A DASH MPD is converted to an HLS media playlist on the fly; an HLS m3u8 is
	// rewritten URI-by-URI. Both end up as HLS the player can consume directly.
	var rewritten []byte
	if isMPD(body) {
		seg := func(absURL string) string { return p.segmentURL(proxyBase, absURL, pl.Headers) }
		hls, convErr := dashToHLS(body, pl.URL, seg)
		if convErr != nil {
			p.logger.Error("dash to hls conversion failed", "url", pl.URL, "error", convErr)
			http.Error(w, "unsupported DASH manifest", http.StatusNotImplemented)
			return
		}
		rewritten = []byte(hls)
	} else {
		build := func(absURL string, kind hlsKind) string {
			if kind == kindManifest {
				return p.manifestURL(proxyBase, absURL, pl.Headers)
			}
			return p.segmentURL(proxyBase, absURL, pl.Headers)
		}
		rewritten, err = rewriteHLSPlaylist(body, pl.URL, build)
		if err != nil {
			p.logger.Error("hls manifest rewrite failed", "url", pl.URL, "error", err)
			http.Error(w, "manifest rewrite failed", http.StatusBadGateway)
			return
		}
	}

	clearWriteDeadline(w)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(rewritten)
}

// ServeSegment handles GET /hls/segment: fetch (or serve from the in-memory
// cache) a media segment, init section, or key, coalescing concurrent identical
// fetches via singleflight.
func (p *hlsProxy) ServeSegment(w http.ResponseWriter, r *http.Request, proxyBase string) {
	pl, ok := p.payloadFromRequest(w, r)
	if !ok {
		return
	}

	header := headersToHTTP(pl.Headers)
	key := hlsSegmentKey(pl.URL, header)

	if cached, ok := p.cache.Get(key); ok {
		writeSegment(w, r, cached, segmentContentType(pl.URL))
		return
	}

	type result struct {
		body []byte
		ct   string
	}
	v, err, _ := p.group.Do(key, func() (any, error) {
		body, ct, err := p.fetch(r.Context(), pl)
		if err != nil {
			return nil, err
		}
		p.cache.Set(key, body)
		return result{body: body, ct: ct}, nil
	})
	if err != nil {
		p.logger.Error("hls segment fetch failed", "url", pl.URL, "error", err)
		http.Error(w, "upstream segment unavailable", http.StatusBadGateway)
		return
	}

	res := v.(result)
	ct := res.ct
	if ct == "" {
		ct = segmentContentType(pl.URL)
	}
	writeSegment(w, r, res.body, ct)
}

// payloadFromRequest extracts, verifies, and SSRF-guards the token on a request.
func (p *hlsProxy) payloadFromRequest(w http.ResponseWriter, r *http.Request) (proxyPayload, bool) {
	token := r.URL.Query().Get("t")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return proxyPayload{}, false
	}
	pl, err := p.decodeToken(token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusForbidden)
		return proxyPayload{}, false
	}
	if err := guardUpstreamURL(r.Context(), pl.URL); err != nil {
		http.Error(w, "disallowed upstream", http.StatusForbidden)
		return proxyPayload{}, false
	}
	return pl, true
}

// fetch retrieves an upstream URL with the replayed headers, under the proxy's
// concurrency cap. It returns the body and the upstream Content-Type.
func (p *hlsProxy) fetch(ctx context.Context, pl proxyPayload) ([]byte, string, error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, "", ctx.Err()
	}
	defer func() { <-p.sem }()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pl.URL, nil)
	if err != nil {
		return nil, "", err
	}
	applyHeaders(req, pl.Headers)
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", userAgent)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, "", fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize+1))
	if err != nil {
		return nil, "", err
	}
	return body, resp.Header.Get("Content-Type"), nil
}

// applyHeaders copies replayed headers onto an outgoing request.
func applyHeaders(req *http.Request, headers map[string]string) {
	for k, v := range headers {
		req.Header.Set(k, v)
	}
}

// headersToHTTP converts the payload's header map to an http.Header for keying.
func headersToHTTP(headers map[string]string) http.Header {
	if len(headers) == 0 {
		return nil
	}
	h := make(http.Header, len(headers))
	for k, v := range headers {
		h.Set(k, v)
	}
	return h
}

// headerMap flattens an http.Header to the single-value map carried in tokens.
func headerMap(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	m := make(map[string]string, len(h))
	for k := range h {
		m[k] = h.Get(k)
	}
	return m
}

// writeSegment writes a cached/just-fetched segment body with Range support so
// players can byte-range into a segment if they choose.
func writeSegment(w http.ResponseWriter, r *http.Request, body []byte, contentType string) {
	clearWriteDeadline(w)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(body))
}

// segmentContentType guesses a content type from the segment URL extension.
func segmentContentType(rawURL string) string {
	ext := strings.ToLower(path.Ext(pathOf(rawURL)))
	switch ext {
	case ".ts":
		return "video/mp2t"
	case ".m4s", ".mp4", ".m4a", ".m4v", ".cmf", ".cmfv", ".cmfa":
		return "video/mp4"
	case ".aac":
		return "audio/aac"
	case ".vtt":
		return "text/vtt"
	case ".m3u8", ".m3u":
		return "application/vnd.apple.mpegurl"
	case ".key", ".bin":
		return "application/octet-stream"
	default:
		return "application/octet-stream"
	}
}

// pathOf returns the path component of a URL, ignoring the query string.
func pathOf(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil {
		return u.Path
	}
	return rawURL
}
