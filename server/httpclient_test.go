package main

import (
	"net/http"
	"net/url"
	"testing"
)

func TestUpstreamHTTPClientTuning(t *testing.T) {
	c := newUpstreamHTTPClient(nil)

	// No overall timeout: streaming bodies must not be cut off.
	if c.Timeout != 0 {
		t.Fatalf("Client.Timeout = %v, want 0 (streaming safe)", c.Timeout)
	}

	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", c.Transport)
	}
	if tr.MaxIdleConnsPerHost <= 2 {
		t.Fatalf("MaxIdleConnsPerHost = %d, want > 2 (avoid sparse-fill churn)", tr.MaxIdleConnsPerHost)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatal("ResponseHeaderTimeout must be set (asymmetric timeout)")
	}
}

func TestUpstreamHTTPClientProxy(t *testing.T) {
	proxyURL, _ := url.Parse("socks5://127.0.0.1:1080")
	tr := newUpstreamHTTPClient(proxyURL).Transport.(*http.Transport)
	if tr.Proxy == nil {
		t.Fatal("Transport.Proxy is nil, want the configured proxy")
	}
	got, err := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}})
	if err != nil {
		t.Fatalf("Proxy func returned error: %v", err)
	}
	if got == nil || got.String() != proxyURL.String() {
		t.Fatalf("Proxy = %v, want %v", got, proxyURL)
	}

	// A nil proxy keeps the cloned default (ProxyFromEnvironment), not no-proxy.
	def := newUpstreamHTTPClient(nil).Transport.(*http.Transport)
	if def.Proxy == nil {
		t.Fatal("nil proxy must keep ProxyFromEnvironment, got nil Proxy")
	}
}
