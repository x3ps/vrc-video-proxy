// Package httpx provides the tuned upstream HTTP client and the SSRF guard used
// when the proxy fetches a resolved stream on a player's behalf.
package httpx

import (
	"net/http"
	"net/url"
	"time"
)

// NewUpstreamClient builds the tuned client used for every upstream fetch.
//
// Go's default Transport allows only MaxIdleConnsPerHost=2 idle connections; a
// larger idle pool removes connection churn when a player issues many sequential
// Range requests to the same host while seeking.
//
// Client.Timeout stays 0 so streaming bodies are never cut off mid-transfer;
// cancellation is driven by each request's context instead. ResponseHeaderTimeout
// bounds only the header phase, so a slow or hung upstream fails fast without
// limiting long downloads.
//
// When proxyURL is non-nil every upstream request is routed through it (http,
// https or socks5 — http.Transport dials all three natively). When nil the cloned
// default's ProxyFromEnvironment is kept, so an ambient HTTP(S)_PROXY is honored.
func NewUpstreamClient(proxyURL *url.URL) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 16
	transport.IdleConnTimeout = 90 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return &http.Client{Transport: transport}
}
