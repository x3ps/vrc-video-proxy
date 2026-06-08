package main

import (
	"net/http"
	"testing"
	"time"
)

func TestHeaderDigestStability(t *testing.T) {
	h1 := http.Header{}
	h1.Set("User-Agent", "vrc/1.0")
	h1.Set("Cookie", "sid=abc")

	h2 := http.Header{}
	h2.Set("Cookie", "sid=abc")
	h2.Set("User-Agent", "vrc/1.0")

	if headerDigest(h1) != headerDigest(h2) {
		t.Fatal("digest should be order-independent")
	}

	h3 := http.Header{}
	h3.Set("User-Agent", "vrc/1.0")
	h3.Set("Cookie", "sid=different")
	if headerDigest(h1) == headerDigest(h3) {
		t.Fatal("different cookies should yield different digests")
	}

	if headerDigest(nil) != "noauth" {
		t.Fatal("nil headers should be noauth")
	}
}

func TestSegmentKeyDistinctByHeaders(t *testing.T) {
	a := http.Header{}
	a.Set("Cookie", "sid=1")
	b := http.Header{}
	b.Set("Cookie", "sid=2")
	if hlsSegmentKey("https://cdn/s.ts", a) == hlsSegmentKey("https://cdn/s.ts", b) {
		t.Fatal("same URL with different auth must not collide")
	}
	if hlsManifestKey("https://cdn/m.m3u8") == mpdManifestKey("https://cdn/m.m3u8") {
		t.Fatal("namespaces must not collide")
	}
}

func TestMemCacheSetGetTTL(t *testing.T) {
	c := NewMemCache(8, 0)
	c.Set("k", []byte("v"))
	got, ok := c.Get("k")
	if !ok || string(got) != "v" {
		t.Fatalf("get = %q, %v", got, ok)
	}
	if _, ok := c.Get("missing"); ok {
		t.Fatal("missing key should not be present")
	}

	ttlCache := NewMemCache(8, 20*time.Millisecond)
	ttlCache.Set("k", []byte("v"))
	time.Sleep(40 * time.Millisecond)
	if _, ok := ttlCache.Get("k"); ok {
		t.Fatal("entry should have expired")
	}
}
