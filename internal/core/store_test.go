package core

import (
	"net/http"
	"testing"
	"time"
)

func TestStreamStorePutGet(t *testing.T) {
	s := newStreamStore(time.Minute)
	h := http.Header{"User-Agent": {"vrc/1.0"}}

	id := s.Put("https://cdn.example.com/v.mp4", h)
	if !streamIDPattern.MatchString(id) {
		t.Fatalf("id %q does not match the expected pattern", id)
	}

	entry, ok := s.Get(id)
	if !ok {
		t.Fatal("Get returned ok=false for a freshly stored id")
	}
	if entry.upstreamURL != "https://cdn.example.com/v.mp4" {
		t.Fatalf("upstreamURL = %q", entry.upstreamURL)
	}
	if entry.headers.Get("User-Agent") != "vrc/1.0" {
		t.Fatalf("headers not preserved: %v", entry.headers)
	}

	if _, ok := s.Get("ffffffffffffffffffffffffffffffff"); ok {
		t.Fatal("Get returned ok=true for an unknown id")
	}
}

func TestStreamStoreClonesHeaders(t *testing.T) {
	s := newStreamStore(time.Minute)
	h := http.Header{"Cookie": {"a=1"}}
	id := s.Put("https://cdn.example.com/v.mp4", h)

	// Mutating the caller's header must not affect the stored entry.
	h.Set("Cookie", "tampered")

	entry, _ := s.Get(id)
	if entry.headers.Get("Cookie") != "a=1" {
		t.Fatalf("stored Cookie = %q, want a=1 (headers must be cloned)", entry.headers.Get("Cookie"))
	}
}

func TestStreamStoreExpiry(t *testing.T) {
	now := time.Unix(0, 0)
	s := newStreamStore(time.Minute)
	s.now = func() time.Time { return now }

	id := s.Put("https://cdn.example.com/v.mp4", nil)

	// Just before expiry: still present.
	now = now.Add(59 * time.Second)
	if _, ok := s.Get(id); !ok {
		t.Fatal("entry expired early")
	}

	// The successful Get above slid the TTL forward; advancing past the new expiry
	// drops it.
	now = now.Add(2 * time.Minute)
	if _, ok := s.Get(id); ok {
		t.Fatal("entry should have expired")
	}
}

func TestStreamStoreSlidingTTL(t *testing.T) {
	now := time.Unix(0, 0)
	s := newStreamStore(time.Minute)
	s.now = func() time.Time { return now }

	id := s.Put("https://cdn.example.com/v.mp4", nil)

	// Access every 30s for 5 minutes: the sliding TTL keeps it alive throughout.
	for i := 0; i < 10; i++ {
		now = now.Add(30 * time.Second)
		if _, ok := s.Get(id); !ok {
			t.Fatalf("entry expired at iteration %d despite continuous access", i)
		}
	}
}
