package main

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// cacheIDPattern matches a valid cache id: the lowercase hex encoding of a
// SHA-256 digest. Restricting ids to this charset makes path traversal
// impossible because the id can never contain a path separator or "..".
var cacheIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Cache stores finished videos as "<dir>/<id>.mp4" and uses "<dir>/tmp" for
// in-progress downloads. It evicts least-recently-modified files when the total
// size exceeds maxSize.
type Cache struct {
	dir     string
	tmpDir  string
	maxSize int64

	mu sync.Mutex // serialises finalize/evict so size accounting stays consistent
}

// NewCache prepares the cache and temp directories on disk. Leftover partial
// downloads in tmp/ are discarded: in-progress state lives only in memory, so it
// cannot be resumed across restarts.
func NewCache(dir string, maxSize int64) (*Cache, error) {
	tmpDir := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}
	if entries, err := os.ReadDir(tmpDir); err == nil {
		for _, e := range entries {
			_ = os.Remove(filepath.Join(tmpDir, e.Name()))
		}
	}
	return &Cache{dir: dir, tmpDir: tmpDir, maxSize: maxSize}, nil
}

// cacheID derives a stable, filename-safe id from a source URL.
func cacheID(rawURL string) string {
	normalized := normalizeURL(rawURL)
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

// normalizeURL applies light normalisation so trivially-different URLs (e.g.
// with a trailing fragment or surrounding whitespace) map to the same cache id.
func normalizeURL(rawURL string) string {
	trimmed := strings.TrimSpace(rawURL)
	if i := strings.IndexByte(trimmed, '#'); i >= 0 {
		trimmed = trimmed[:i]
	}
	return trimmed
}

// validateID reports whether id is a well-formed cache id.
func validateID(id string) bool {
	return cacheIDPattern.MatchString(id)
}

// Path returns the on-disk path of a finished cache entry. It returns an error
// for malformed ids so callers never build a path from untrusted input.
func (c *Cache) Path(id string) (string, error) {
	if !validateID(id) {
		return "", fmt.Errorf("invalid cache id")
	}
	return filepath.Join(c.dir, id+".mp4"), nil
}

// Has reports whether a finished cache entry exists for id.
func (c *Cache) Has(id string) bool {
	path, err := c.Path(id)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Touch updates the modification time of a cache entry so LRU eviction treats it
// as recently used. Failures are non-fatal and ignored by callers.
func (c *Cache) Touch(id string) error {
	path, err := c.Path(id)
	if err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(path, now, now)
}

// tempPath returns a unique temp path for an in-progress download of id.
func (c *Cache) tempPath(id, token string) string {
	return filepath.Join(c.tmpDir, id+"."+token+".part")
}

// finalize atomically moves a completed temp file into the cache and triggers
// eviction if the cache is now over budget.
func (c *Cache) finalize(tmpPath, id string) error {
	final, err := c.Path(id)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := os.Rename(tmpPath, final); err != nil {
		return fmt.Errorf("finalize cache entry: %w", err)
	}
	c.evictLocked()
	return nil
}

// Size returns the total bytes used by finished cache entries.
func (c *Cache) Size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	total, _ := c.scanLocked()
	return total
}

type cacheEntry struct {
	path    string
	size    int64
	modUnix int64
}

// scanLocked lists finished cache entries (.mp4 files directly in dir) and their
// total size.
func (c *Cache) scanLocked() (int64, []cacheEntry) {
	dirEntries, err := os.ReadDir(c.dir)
	if err != nil {
		return 0, nil
	}

	var total int64
	entries := make([]cacheEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".mp4") {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		entries = append(entries, cacheEntry{
			path:    filepath.Join(c.dir, de.Name()),
			size:    info.Size(),
			modUnix: info.ModTime().UnixNano(),
		})
	}
	return total, entries
}

// evictLocked deletes least-recently-modified entries until the cache is within
// budget. The caller must hold c.mu.
func (c *Cache) evictLocked() {
	total, entries := c.scanLocked()
	if total <= c.maxSize {
		return
	}

	slices.SortFunc(entries, func(a, b cacheEntry) int {
		return cmp.Compare(a.modUnix, b.modUnix)
	})

	for _, entry := range entries {
		if total <= c.maxSize {
			break
		}
		if err := os.Remove(entry.path); err != nil {
			continue
		}
		total -= entry.size
	}
}
