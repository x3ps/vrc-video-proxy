package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheIDIsStableAndHex(t *testing.T) {
	a := cacheID("https://example.com/watch?v=abc")
	b := cacheID("  https://example.com/watch?v=abc#fragment ")
	if a != b {
		t.Fatalf("cacheID not stable across whitespace/fragment: %q vs %q", a, b)
	}
	if !validateID(a) {
		t.Fatalf("cacheID %q is not a valid id", a)
	}
	if c := cacheID("https://example.com/other"); c == a {
		t.Fatal("different URLs produced the same cache id")
	}
}

func TestValidateIDRejectsUnsafeInput(t *testing.T) {
	const hex64 = "0123456789012345678901234567890123456789012345678901234567890123"
	bad := []string{
		"",
		"../../etc/passwd",
		"abc",                // too short
		hex64 + "0",          // too long (65)
		"g" + hex64[1:],      // non-hex char
		"ABCDEF" + hex64[6:], // uppercase
		"/" + hex64[1:],      // path separator
		hex64[:32] + "/" + hex64[33:],
	}
	for _, id := range bad {
		if validateID(id) {
			t.Fatalf("validateID(%q) = true, want false", id)
		}
	}

	good := cacheID("https://example.com/v")
	if !validateID(good) {
		t.Fatalf("validateID(%q) = false, want true", good)
	}
}

func TestCachePathRejectsInvalidID(t *testing.T) {
	cache, err := NewCache(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatalf("NewCache returned error: %v", err)
	}
	if _, err := cache.Path("../escape"); err == nil {
		t.Fatal("Path accepted a traversal id")
	}

	id := cacheID("https://example.com/v")
	path, err := cache.Path(id)
	if err != nil {
		t.Fatalf("Path returned error: %v", err)
	}
	if filepath.Dir(path) != cache.dir {
		t.Fatalf("path %q is outside cache dir %q", path, cache.dir)
	}
}

func TestCacheFinalizeIsAtomicRename(t *testing.T) {
	dir := t.TempDir()
	cache, err := NewCache(dir, 1<<30)
	if err != nil {
		t.Fatalf("NewCache returned error: %v", err)
	}

	id := cacheID("https://example.com/v")
	tmp := cache.tempPath(id, "token")
	if err := os.WriteFile(tmp, []byte("payload"), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}

	if err := cache.finalize(tmp, id); err != nil {
		t.Fatalf("finalize returned error: %v", err)
	}
	if !cache.Has(id) {
		t.Fatal("cache entry missing after finalize")
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("temp file still present after finalize")
	}
}

func TestCacheEvictsLeastRecentlyModified(t *testing.T) {
	dir := t.TempDir()
	// Budget fits two 10-byte files but not three.
	cache, err := NewCache(dir, 25)
	if err != nil {
		t.Fatalf("NewCache returned error: %v", err)
	}

	ids := []string{
		cacheID("https://example.com/a"),
		cacheID("https://example.com/b"),
		cacheID("https://example.com/c"),
	}
	base := time.Now().Add(-time.Hour)
	for i, id := range ids {
		path := filepath.Join(dir, id+".mp4")
		if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
			t.Fatalf("write %s: %v", id, err)
		}
		// a is oldest, c is newest.
		mod := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	cache.mu.Lock()
	cache.evictLocked()
	cache.mu.Unlock()

	if cache.Has(ids[0]) {
		t.Fatal("oldest entry should have been evicted")
	}
	if !cache.Has(ids[1]) || !cache.Has(ids[2]) {
		t.Fatal("newer entries should have been kept")
	}
}
