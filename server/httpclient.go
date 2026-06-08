package main

import (
	"net/http"
	"net/url"
	"time"
)

// sharedHTTPClient is the single tuned client used for every upstream fetch
// (probe, range download, and the HLS/DASH manifest/segment proxy).
//
// Go's default Transport allows only MaxIdleConnsPerHost=2 idle connections,
// which starves the sparse downloader: it opens several concurrent connections
// to the same host (sequential filler + seek-driven fetches), so with only two
// idle slots connections are torn down and re-dialed constantly. A larger idle
// pool removes that churn — the lesson behind MediaFlow's per-route client cache.
//
// Client.Timeout stays 0 so streaming bodies are never cut off mid-transfer;
// cancellation is driven by each request's context instead. ResponseHeaderTimeout
// bounds only the header phase (MediaFlow's "asymmetric timeouts"), so a slow or
// hung upstream fails fast without limiting long downloads.
var sharedHTTPClient = newUpstreamHTTPClient(nil)

// newUpstreamHTTPClient builds the tuned client. When proxyURL is non-nil every
// upstream request is routed through it (http, https or socks5 — http.Transport
// dials all three natively, and userinfo becomes a Proxy-Authorization header).
// When nil the cloned default's ProxyFromEnvironment is kept, so an ambient
// HTTP(S)_PROXY is still honored.
func newUpstreamHTTPClient(proxyURL *url.URL) *http.Client {
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
