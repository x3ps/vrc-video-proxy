package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
)

// Cache keys are namespaced "category:material" strings, built only through the
// helpers below so the format never drifts between call sites (mirrors the
// centralized key builder in MediaFlow's src/cache/keys.rs). They key the shared
// in-memory MemCache, so distinct categories never collide.

// hlsManifestKey keys a rewritten HLS manifest by its upstream URL.
func hlsManifestKey(url string) string {
	return "hls_manifest:" + url
}

// hlsSegmentKey keys an HLS segment (or key/init) by its upstream URL plus a
// digest of the auth-relevant request headers, so the same URL fetched under
// different credentials does not collide.
func hlsSegmentKey(url string, headers http.Header) string {
	return "hls_segment:" + url + ":" + headerDigest(headers)
}

// mpdManifestKey keys a parsed/converted MPD by its upstream URL.
func mpdManifestKey(url string) string {
	return "mpd_manifest:" + url
}

// headerDigest returns a short stable digest of the auth-relevant request
// headers. Only headers that change the upstream response identity are included;
// order is normalized so equivalent header sets map to the same digest.
func headerDigest(headers http.Header) string {
	if len(headers) == 0 {
		return "noauth"
	}
	const sep = "\x00"
	var parts []string
	for _, name := range []string{"Authorization", "Cookie", "Referer", "User-Agent"} {
		if v := headers.Get(name); v != "" {
			parts = append(parts, strings.ToLower(name)+"="+v)
		}
	}
	if len(parts) == 0 {
		return "noauth"
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, sep)))
	return hex.EncodeToString(sum[:8])
}
