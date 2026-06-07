package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := Config{
		CacheDir:     t.TempDir(),
		CacheMaxSize: 1 << 30,
		YtdlpPath:    "yt-dlp",
	}
	srv, err := NewServer(cfg, discardLogger())
	if err != nil {
		t.Fatalf("NewServer returned error: %v", err)
	}
	return srv
}

func newTestJobManager(t *testing.T) (*JobManager, *Cache) {
	t.Helper()
	cache, err := NewCache(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatalf("NewCache returned error: %v", err)
	}
	return NewJobManager(cache, discardLogger()), cache
}

// waitForJob blocks until the job's run() goroutine has finished.
func waitForJob(t *testing.T, job *Job) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job.mu.Lock()
		done := job.done
		job.mu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job did not finish within timeout")
}

// fakeUpstream serves probe/fetchRange from an in-memory byte slice so tests
// never touch the network. It records the ranges fetched so tests can assert that
// a seek triggered a dedicated upstream fetch.
type fakeUpstream struct {
	data      []byte
	rangeable bool
	delay     time.Duration // per-chunk delay to simulate a slow stream
	chunk     int           // bytes per read (0 → 4096)
	probeErr  error
	fetchErr  error

	mu      sync.Mutex
	fetched []interval
}

func (f *fakeUpstream) install(m *JobManager) {
	m.probe = func(context.Context, string) (int64, bool, error) {
		if f.probeErr != nil {
			return 0, false, f.probeErr
		}
		return int64(len(f.data)), f.rangeable, nil
	}
	m.fetchRange = func(_ context.Context, _ string, start, end int64) (io.ReadCloser, error) {
		if f.fetchErr != nil {
			return nil, f.fetchErr
		}
		f.mu.Lock()
		f.fetched = append(f.fetched, interval{start, end})
		f.mu.Unlock()

		s := clampInt64(start, 0, int64(len(f.data)))
		var e int64
		if end < 0 {
			e = int64(len(f.data))
		} else {
			e = end + 1
		}
		e = clampInt64(e, 0, int64(len(f.data)))
		c := f.chunk
		if c <= 0 {
			c = 4096
		}
		return io.NopCloser(&chunkReader{data: f.data[s:e], chunk: c, delay: f.delay}), nil
	}
}

// fetchedFrom reports whether any upstream fetch started at offset start.
func (f *fakeUpstream) fetchedFrom(start int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, iv := range f.fetched {
		if iv.start == start {
			return true
		}
	}
	return false
}

func clampInt64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// chunkReader yields its data in fixed-size pieces, optionally with a delay, so a
// download can be observed mid-flight.
type chunkReader struct {
	data  []byte
	pos   int
	chunk int
	delay time.Duration
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	end := min(c.pos+c.chunk, len(c.data))
	n := copy(p, c.data[c.pos:end])
	c.pos += n
	return n, nil
}
