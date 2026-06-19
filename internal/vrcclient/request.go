// Package vrcclient talks to a vrc-video-proxy server on behalf of the yt-dlp
// replacement wrapper: it infers a Request from the yt-dlp-style argv VRChat
// passes and resolves it against the server's /api/getvideo endpoint.
package vrcclient

import (
	"errors"
	"net/url"
	"strings"
)

const (
	envOptionPrefix   = "VRCVP_OPTION_"
	queryOptionPrefix = "vrcvp_"
)

// Request is a resolved wrapper invocation: which source URL to resolve and the
// player flags inferred from the yt-dlp argv.
type Request struct {
	URL     string
	AVPro   bool
	Source  string
	Options url.Values
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
			cleanURL, options := splitWrapperQuery(arg)
			req.URL = cleanURL
			req.Options = options
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

func splitWrapperQuery(rawURL string) (string, url.Values) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, nil
	}

	query := parsedURL.Query()
	options := make(url.Values)
	for key, values := range query {
		if !strings.HasPrefix(key, queryOptionPrefix) {
			continue
		}
		for _, value := range values {
			options.Add(key, value)
		}
		query.Del(key)
	}
	parsedURL.RawQuery = query.Encode()

	if len(options) == 0 {
		return rawURL, nil
	}
	return parsedURL.String(), options
}

// OptionsFromEnvironment converts wrapper option environment variables into
// vrcvp_* query parameters. For example, VRCVP_OPTION_TRANSCODE=true becomes
// vrcvp_transcode=true.
func OptionsFromEnvironment(environ []string) url.Values {
	options := make(url.Values)
	for _, item := range environ {
		name, value, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		suffix, ok := strings.CutPrefix(name, envOptionPrefix)
		if !ok || suffix == "" {
			continue
		}
		key := queryOptionPrefix + strings.ToLower(suffix)
		options.Set(key, value)
	}
	if len(options) == 0 {
		return nil
	}
	return options
}

// MergeOptions combines default and override option sets. Override values replace
// defaults for matching keys, which lets per-URL vrcvp_* parameters win over
// process-level environment defaults.
func MergeOptions(defaults, overrides url.Values) url.Values {
	if len(defaults) == 0 && len(overrides) == 0 {
		return nil
	}

	merged := make(url.Values, len(defaults)+len(overrides))
	for key, values := range defaults {
		merged[key] = append([]string(nil), values...)
	}
	for key, values := range overrides {
		merged[key] = append([]string(nil), values...)
	}
	return merged
}
