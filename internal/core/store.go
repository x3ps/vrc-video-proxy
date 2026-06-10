package core

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// streamEntry holds a resolved upstream stream and the headers to replay when
// fetching it. expiresAt is refreshed on every access (sliding TTL).
type streamEntry struct {
	upstreamURL string
	headers     http.Header
	expiresAt   time.Time
}

// streamStore maps an opaque, unguessable id to a resolved upstream stream. It
// bridges /api/getvideo (which resolves the URL once) and /stream/{id} (which the
// player hits repeatedly, including many Range requests while seeking) without a
// disk cache or download job.
//
// The TTL slides on every Get, so an actively-playing client keeps its handle
// alive; an abandoned handle expires and is reaped lazily.
type streamStore struct {
	mu      sync.Mutex
	entries map[string]streamEntry
	ttl     time.Duration
	now     func() time.Time // injectable clock for tests
}

func newStreamStore(ttl time.Duration) *streamStore {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &streamStore{
		entries: make(map[string]streamEntry),
		ttl:     ttl,
		now:     time.Now,
	}
}

// Put stores a resolved stream under a fresh random id and returns the id. The id
// is unguessable (16 bytes from crypto/rand, hex-encoded), so it doubles as a
// capability token: the proxy only ever fetches URLs it resolved itself.
func (s *streamStore) Put(upstreamURL string, headers http.Header) string {
	id := newStreamID()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked()
	s.entries[id] = streamEntry{
		upstreamURL: upstreamURL,
		headers:     headers.Clone(),
		expiresAt:   s.now().Add(s.ttl),
	}
	return id
}

// Get returns the entry for id and refreshes its expiry. A missing or expired id
// returns ok=false.
func (s *streamStore) Get(id string) (streamEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok {
		return streamEntry{}, false
	}
	now := s.now()
	if now.After(entry.expiresAt) {
		delete(s.entries, id)
		return streamEntry{}, false
	}
	entry.expiresAt = now.Add(s.ttl)
	s.entries[id] = entry
	return entry, true
}

func (s *streamStore) reapLocked() {
	now := s.now()
	for id, entry := range s.entries {
		if now.After(entry.expiresAt) {
			delete(s.entries, id)
		}
	}
}

// newStreamID returns 32 hex chars of cryptographic randomness. crypto/rand.Read
// does not fail on supported platforms; a failure is treated as fatal because an
// unguessable id is a security requirement, not best-effort.
func newStreamID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
