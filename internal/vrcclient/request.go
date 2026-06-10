// Package vrcclient talks to a vrc-video-proxy server on behalf of the yt-dlp
// replacement wrapper: it infers a Request from the yt-dlp-style argv VRChat
// passes and resolves it against the server's /api/getvideo endpoint.
package vrcclient

import (
	"errors"
	"net/url"
	"strings"
)

// Request is a resolved wrapper invocation: which source URL to resolve and the
// player flags inferred from the yt-dlp argv.
type Request struct {
	URL    string
	AVPro  bool
	Source string
}

// ParseRequest infers a Request from the yt-dlp-style arguments VRChat (or
// Resonite) passes. By convention: "[protocol^=http]" in the format selector means
// a Unity/non-AVPro player (avpro=false); "--flat-playlist" marks a Resonite call
// (source=resonite); the first absolute http(s) argument is the source URL.
func ParseRequest(args []string) (Request, error) {
	req := Request{
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
		return Request{}, errors.New("no http(s) URL found in arguments")
	}
	return req, nil
}

func isAbsoluteHTTPURL(rawURL string) bool {
	parsedURL, err := url.Parse(rawURL)
	return err == nil && (parsedURL.Scheme == "http" || parsedURL.Scheme == "https") && parsedURL.Host != ""
}
