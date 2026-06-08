package main

import (
	"net/http"
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
var sharedHTTPClient = newUpstreamHTTPClient()

func newUpstreamHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 16
	transport.IdleConnTimeout = 90 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: transport}
}
