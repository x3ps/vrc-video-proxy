package httpx

import (
	"context"
	"testing"
)

func TestGuardUpstreamURLRejectsNonHTTP(t *testing.T) {
	for _, raw := range []string{"ftp://example.com/x", "file:///etc/passwd", "://nohost"} {
		if err := GuardUpstreamURL(context.Background(), raw); err == nil {
			t.Fatalf("GuardUpstreamURL(%q) = nil, want error", raw)
		}
	}
}

func TestGuardUpstreamURLRejectsLoopback(t *testing.T) {
	// 127.0.0.1 is a literal loopback address, so no DNS lookup is needed.
	if err := GuardUpstreamURL(context.Background(), "http://127.0.0.1:9/x"); err == nil {
		t.Fatal("GuardUpstreamURL(loopback) = nil, want error")
	}
}

func TestGuardUpstreamURLAllowsPublicLiteral(t *testing.T) {
	if err := GuardUpstreamURL(context.Background(), "https://93.184.216.34/x"); err != nil {
		t.Fatalf("GuardUpstreamURL(public literal) = %v, want nil", err)
	}
}
