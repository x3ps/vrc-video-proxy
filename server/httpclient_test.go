package main

import (
	"net/http"
	"testing"
)

func TestUpstreamHTTPClientTuning(t *testing.T) {
	c := newUpstreamHTTPClient()

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
