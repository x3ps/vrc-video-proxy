package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
)

func TestJobManagerDeduplicatesActiveJobs(t *testing.T) {
	mgr, _ := newTestJobManager(t)

	// Block the probe so the job stays active for the assertion.
	release := make(chan struct{})
	mgr.probe = func(context.Context, string) (int64, bool, error) {
		<-release
		return 0, false, nil
	}
	t.Cleanup(func() { close(release) })

	id := cacheID("https://example.com/dedup")
	a := mgr.getOrStart(id, "https://cdn.example.com/s.mp4")
	b := mgr.getOrStart(id, "https://cdn.example.com/s.mp4")
	if a != b {
		t.Fatal("getOrStart returned different jobs for the same id")
	}
}

func TestJobManagerSequentialFinalizesToCache(t *testing.T) {
	mgr, cache := newTestJobManager(t)

	payload := bytes.Repeat([]byte("seq"), 1000)
	(&fakeUpstream{data: payload, rangeable: false}).install(mgr)

	id := cacheID("https://example.com/seq")
	job := mgr.getOrStart(id, "https://cdn.example.com/s.mp4")
	waitForJob(t, job)

	if job.err != nil {
		t.Fatalf("job.err = %v, want nil", job.err)
	}
	if job.rangeable {
		t.Fatal("expected sequential mode for a non-rangeable upstream")
	}
	assertCached(t, cache, id, payload)
	if _, ok := mgr.getJob(id); ok {
		t.Fatal("finished job should be removed from the manager")
	}
}

func TestJobManagerSparseFinalizesToCache(t *testing.T) {
	mgr, cache := newTestJobManager(t)

	payload := bytes.Repeat([]byte("sparse-"), 5000) // ~35 KB, spans multiple chunks
	(&fakeUpstream{data: payload, rangeable: true, chunk: 1000}).install(mgr)

	id := cacheID("https://example.com/sparse")
	job := mgr.getOrStart(id, "https://cdn.example.com/s.mp4")
	waitForJob(t, job)

	if job.err != nil {
		t.Fatalf("job.err = %v, want nil", job.err)
	}
	if !job.rangeable {
		t.Fatal("expected sparse mode for a rangeable upstream")
	}
	assertCached(t, cache, id, payload)
	if _, ok := mgr.getJob(id); ok {
		t.Fatal("finished job should be removed from the manager")
	}
}

func TestJobManagerProbeFailureCleansUp(t *testing.T) {
	mgr, cache := newTestJobManager(t)
	(&fakeUpstream{probeErr: errors.New("probe boom")}).install(mgr)

	id := cacheID("https://example.com/probefail")
	job := mgr.getOrStart(id, "https://cdn.example.com/s.mp4")
	waitForJob(t, job)

	if job.err == nil {
		t.Fatal("job.err = nil, want error")
	}
	if cache.Has(id) {
		t.Fatal("failed job should not produce a cache entry")
	}
	if _, ok := mgr.getJob(id); ok {
		t.Fatal("failed job should be removed from the manager")
	}
}

func TestJobManagerSparseFetchFailureCleansUp(t *testing.T) {
	mgr, cache := newTestJobManager(t)
	(&fakeUpstream{data: make([]byte, 4096), rangeable: true, fetchErr: errors.New("fetch boom")}).install(mgr)

	id := cacheID("https://example.com/fetchfail")
	job := mgr.getOrStart(id, "https://cdn.example.com/s.mp4")
	waitForJob(t, job)

	if job.err == nil {
		t.Fatal("job.err = nil, want error")
	}
	if cache.Has(id) {
		t.Fatal("failed job should not produce a cache entry")
	}
	if _, err := os.Stat(job.tmpPath); !os.IsNotExist(err) {
		t.Fatal("temp file should be removed after failure")
	}
	if _, ok := mgr.getJob(id); ok {
		t.Fatal("failed job should be removed from the manager")
	}
}

func assertCached(t *testing.T, cache *Cache, id string, want []byte) {
	t.Helper()
	if !cache.Has(id) {
		t.Fatal("cache entry missing after successful job")
	}
	path, _ := cache.Path(id)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("cached %d bytes, want %d", len(got), len(want))
	}
}
