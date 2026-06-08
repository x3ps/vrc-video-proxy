package main

import (
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"
)

// MemCache is a bounded, TTL-based in-memory byte cache for small hot objects
// such as HLS/DASH manifests and segments. It complements (does not replace) the
// on-disk Cache, which holds large progressive video bodies. The underlying
// expirable.LRU is safe for concurrent use.
type MemCache struct {
	lru *expirable.LRU[string, []byte]
}

// NewMemCache creates a cache holding at most size entries, each living for ttl.
// A size of 0 means unbounded by count; a ttl of 0 disables expiry.
func NewMemCache(size int, ttl time.Duration) *MemCache {
	return &MemCache{lru: expirable.NewLRU[string, []byte](size, nil, ttl)}
}

// Get returns the bytes stored under key and whether they were present.
func (c *MemCache) Get(key string) ([]byte, bool) {
	return c.lru.Get(key)
}

// Set stores value under key, evicting the least-recently-used entry if needed.
func (c *MemCache) Set(key string, value []byte) {
	c.lru.Add(key, value)
}

// Len reports the number of live entries (used in tests).
func (c *MemCache) Len() int {
	return c.lru.Len()
}
