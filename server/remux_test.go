package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRemuxJobFinalizesCache(t *testing.T) {
	jm, cache := newTestJobManager(t)
	jm.openRemux = func(context.Context, string, map[string]string) (io.ReadCloser, func() error, error) {
		return io.NopCloser(strings.NewReader("MP4DATA")), func() error { return nil }, nil
	}

	id := strings.Repeat("a", 64)
	job := jm.getOrStartRemux(id, "https://h/v.m3u8", false, nil)
	waitForJob(t, job)

	if !cache.Has(id) {
		t.Fatal("expected a cached entry after a successful remux")
	}
	path, _ := cache.Path(id)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cached file: %v", err)
	}
	if string(b) != "MP4DATA" {
		t.Fatalf("cached content = %q, want MP4DATA", b)
	}
}

func TestRemuxJobUsesTranscodeOpener(t *testing.T) {
	jm, cache := newTestJobManager(t)
	jm.openRemux = func(context.Context, string, map[string]string) (io.ReadCloser, func() error, error) {
		t.Fatal("copy opener must not be used when transcode is requested")
		return nil, nil, nil
	}
	var transcoded bool
	jm.openTranscode = func(context.Context, string, map[string]string) (io.ReadCloser, func() error, error) {
		transcoded = true
		return io.NopCloser(strings.NewReader("H264DATA")), func() error { return nil }, nil
	}

	id := strings.Repeat("c", 64)
	job := jm.getOrStartRemux(id, "https://h/v.m3u8", true /*transcode*/, nil)
	waitForJob(t, job)

	if !transcoded {
		t.Fatal("transcode opener was not used")
	}
	if !cache.Has(id) {
		t.Fatal("expected cached entry after transcode")
	}
}

func TestRemuxJobFailureCleansUp(t *testing.T) {
	jm, cache := newTestJobManager(t)
	jm.openRemux = func(context.Context, string, map[string]string) (io.ReadCloser, func() error, error) {
		// Body streams fine, but ffmpeg reports a non-zero exit.
		return io.NopCloser(strings.NewReader("partial")), func() error { return errors.New("exit 1") }, nil
	}

	id := strings.Repeat("b", 64)
	job := jm.getOrStartRemux(id, "https://h/v.m3u8", false, nil)
	waitForJob(t, job)

	if cache.Has(id) {
		t.Fatal("a failed remux must not produce a cache entry")
	}
	if _, err := os.Stat(job.tmpPath); !os.IsNotExist(err) {
		t.Fatalf("temp file should be removed, stat err = %v", err)
	}
}
