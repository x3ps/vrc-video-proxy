package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// downloadTimeout bounds how long a single background download may run.
const downloadTimeout = 30 * time.Minute

// downloadChunkSize is the buffer size used while teeing upstream bytes to disk.
const downloadChunkSize = 256 * 1024

// maxConcurrentFills caps simultaneous upstream connections per video. It bounds
// throttling pressure (e.g. googlevideo 403s) while still leaving room for a
// seek-driven fetch alongside the sequential filler.
const maxConcurrentFills = 3

// userAgent is sent on all upstream requests.
const userAgent = "Mozilla/5.0 (compatible; vrc-video-proxy)"

// probeFunc reports an upstream's total size and whether it supports Range.
type probeFunc func(ctx context.Context, rawURL string) (size int64, rangeable bool, err error)

// rangeFetchFunc opens the byte range [start, end] (inclusive). end < 0 means
// open-ended; with start == 0 it sends no Range header (a plain GET).
type rangeFetchFunc func(ctx context.Context, rawURL string, start, end int64) (io.ReadCloser, error)

// Job represents an in-progress download keyed by cache id. After the upstream is
// probed it runs in one of two modes: sparse (Range-capable, supports seeking) or
// sequential (fallback). Readers wait on ready before inspecting the mode.
type Job struct {
	id        string
	streamURL string
	tmpPath   string

	remux     bool              // ffmpeg-remux mode (HLS/DASH VOD into a single MP4)
	transcode bool              // remux mode must re-encode (incompatible codecs)
	headers   map[string]string // upstream request headers to replay (remux mode)

	ready     chan struct{} // closed once the probe completes
	size      int64         // total size, -1 when unknown
	rangeable bool          // sparse mode selected
	probeErr  error         // non-nil if the probe (or sparse-file setup) failed
	fillCtx   context.Context

	sf      *SparseFile   // sparse mode only
	seqFile *os.File      // sequential mode: writer fd, created before ready
	fillSem chan struct{} // per-video cap on concurrent upstream fetches

	// sequential mode (tailReader) state
	mu            sync.Mutex
	cond          *sync.Cond
	contentLength int64
	written       int64
	done          bool
	err           error
}

// JobManager starts and deduplicates download jobs keyed by cache id.
type JobManager struct {
	cache         *Cache
	logger        *slog.Logger
	probe         probeFunc
	fetchRange    rangeFetchFunc
	openRemux     remuxOpener
	openTranscode remuxOpener
	fillSem       chan struct{}
	fillChunk     int64

	mu   sync.Mutex
	jobs map[string]*Job
}

// NewJobManager creates a JobManager backed by the given cache.
func NewJobManager(cache *Cache, logger *slog.Logger) *JobManager {
	if logger == nil {
		logger = slog.Default()
	}
	ff := newFfmpegRunner(defaultFfmpegPath, logger)
	return &JobManager{
		cache:         cache,
		logger:        logger,
		probe:         httpProbe,
		fetchRange:    httpFetchRange,
		openRemux:     ff.openRemux,
		openTranscode: ff.openTranscode,
		fillSem:       make(chan struct{}, maxConcurrentFills),
		fillChunk:     maxFillChunk,
		jobs:          make(map[string]*Job),
	}
}

// getOrStart returns the active progressive-download job for id, starting a new
// one if none exists.
func (m *JobManager) getOrStart(id, streamURL string) *Job {
	return m.startJob(id, streamURL, false, false, nil)
}

// getOrStartRemux returns the active job for id, starting a new ffmpeg-remux job
// (HLS/DASH VOD into a single MP4) if none exists. headers are replayed upstream;
// transcode forces re-encoding instead of a stream copy.
func (m *JobManager) getOrStartRemux(id, streamURL string, transcode bool, headers map[string]string) *Job {
	return m.startJob(id, streamURL, true, transcode, headers)
}

// startJob deduplicates by id and launches the appropriate run loop.
func (m *JobManager) startJob(id, streamURL string, remux, transcode bool, headers map[string]string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()

	if job, ok := m.jobs[id]; ok {
		return job
	}

	job := &Job{
		id:            id,
		streamURL:     streamURL,
		tmpPath:       m.cache.tempPath(id, randomToken()),
		remux:         remux,
		transcode:     transcode,
		headers:       headers,
		ready:         make(chan struct{}),
		size:          -1,
		contentLength: -1,
	}
	job.cond = sync.NewCond(&job.mu)
	m.jobs[id] = job

	go m.run(job)
	return job
}

// getJob returns the active job for id, if one exists.
func (m *JobManager) getJob(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	return job, ok
}

func (m *JobManager) run(job *Job) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()
	job.fillCtx = ctx

	m.logger.Info("download job started", "id", job.id, "mode", jobMode(job))

	// Remux jobs have no probeable size: ffmpeg produces a fragmented MP4 stream
	// that is tailed sequentially, then finalized like any other download.
	if job.remux {
		close(job.ready)
		err := m.runRemux(ctx, job)
		if err == nil {
			err = m.cache.finalize(job.tmpPath, job.id)
		}
		m.finishJob(job, err)
		return
	}

	size, rangeable, err := m.probe(ctx, job.streamURL)
	if err == nil {
		job.size = size
		job.rangeable = rangeable && size > 0
		if job.rangeable {
			sf, e := newSparseFile(job.tmpPath, size, m.fillChunk)
			if e != nil {
				err = e
			} else {
				job.sf = sf
			}
		}
	}
	job.probeErr = err
	close(job.ready)

	if err == nil {
		if job.rangeable {
			err = m.runSparse(ctx, job)
		} else {
			err = m.runSequential(ctx, job)
		}
		if err == nil {
			err = m.cache.finalize(job.tmpPath, job.id)
		}
	}

	m.finishJob(job, err)
}

// finishJob records terminal state, drops the job from the registry (so a later
// request can retry), and cleans up the temp file on failure.
func (m *JobManager) finishJob(job *Job, err error) {
	if job.sf != nil {
		job.sf.markDone(err)
	}
	job.mu.Lock()
	job.done = true
	job.err = err
	job.cond.Broadcast()
	job.mu.Unlock()

	m.mu.Lock()
	delete(m.jobs, job.id)
	m.mu.Unlock()

	if err != nil {
		_ = os.Remove(job.tmpPath)
		m.logger.Error("download job failed", "id", job.id, "error", err)
		return
	}
	m.logger.Info("download job finished", "id", job.id, "bytes", job.size, "mode", jobMode(job))
}

func jobMode(job *Job) string {
	switch {
	case job.remux:
		return "remux"
	case job.rangeable:
		return "sparse"
	default:
		return "sequential"
	}
}

// runRemux streams ffmpeg's remuxed MP4 output into the temp file, publishing
// progress so a tailReader can serve /live while the remux runs.
func (m *JobManager) runRemux(ctx context.Context, job *Job) error {
	open := m.openRemux
	if job.transcode {
		open = m.openTranscode
	}
	reader, wait, err := open(ctx, job.streamURL, job.headers)
	if err != nil {
		return fmt.Errorf("start remux: %w", err)
	}
	defer reader.Close()

	f, err := os.Create(job.tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	writeErr := copyTee(ctx, job, f, reader)
	closeErr := f.Close()
	waitErr := wait()

	if writeErr != nil {
		return writeErr
	}
	if waitErr != nil {
		return waitErr
	}
	return closeErr
}

// runSparse fills the sparse file sequentially from the lowest gap upward (one
// chunk at a time) until complete. Seek-driven fills (requestFillAt) run
// concurrently under the same per-video connection cap, so a forward seek gets
// its own upstream fetch instead of waiting for the sequential filler.
func (m *JobManager) runSparse(ctx context.Context, job *Job) error {
	sf := job.sf
	if sf.size == 0 {
		return sf.closeWriter()
	}

	fctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Wake any blocked readers/driver if the job's context is cancelled.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-fctx.Done():
			sf.wake()
		case <-stop:
		}
	}()

	var errMu sync.Mutex
	var firstErr error
	setErr := func(e error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
			cancel()
		}
		errMu.Unlock()
	}

	// Single sequential filler: one outstanding driver fetch at a time, leaving
	// the rest of the connection cap for seek-driven fills.
	for fctx.Err() == nil && !sf.complete() {
		iv, ok := sf.reserveLowest()
		if !ok {
			sf.waitProgress(fctx)
			continue
		}
		if e := m.fillRange(fctx, job, iv); e != nil && !errors.Is(e, context.Canceled) {
			setErr(e)
			break
		}
	}

	_ = sf.closeWriter()

	errMu.Lock()
	fe := firstErr
	errMu.Unlock()
	if fe != nil {
		return fe
	}
	if !sf.complete() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("sparse download incomplete")
	}
	return nil
}

// requestFillAt ensures a fetch is in flight for the gap at off, giving a seek a
// dedicated upstream connection instead of waiting for the sequential filler.
func (m *JobManager) requestFillAt(job *Job, off int64) {
	if job.sf == nil {
		return
	}
	for _, iv := range job.sf.reserve(off, off+job.sf.chunk) {
		go func(iv interval) {
			_ = m.fillRange(job.fillCtx, job, iv)
		}(iv)
	}
}

// fillRange fetches one reserved range and writes it into the sparse file.
func (m *JobManager) fillRange(ctx context.Context, job *Job, iv interval) error {
	defer job.sf.releaseInflight(iv)

	select {
	case m.fillSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.fillSem }()

	reader, err := m.fetchRange(ctx, job.streamURL, iv.start, iv.end-1)
	if err != nil {
		return err
	}
	defer reader.Close()

	pos := iv.start
	buf := make([]byte, downloadChunkSize)
	for pos < iv.end {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := reader.Read(buf)
		if n > 0 {
			w := int64(n)
			if pos+w > iv.end {
				w = iv.end - pos
			}
			if err := job.sf.writeAt(buf[:w], pos); err != nil {
				return err
			}
			pos += w
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	return nil
}

// runSequential is the fallback path for upstreams without Range / known size:
// it streams the whole body to a temp file that tailReader serves in order.
func (m *JobManager) runSequential(ctx context.Context, job *Job) error {
	reader, err := m.fetchRange(ctx, job.streamURL, 0, -1)
	if err != nil {
		return fmt.Errorf("open upstream: %w", err)
	}
	defer reader.Close()

	job.mu.Lock()
	job.contentLength = job.size
	job.cond.Broadcast()
	job.mu.Unlock()

	f, err := os.Create(job.tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	writeErr := copyTee(ctx, job, f, reader)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

// copyTee streams reader into f in chunks, publishing progress on the job so
// tailing readers can be woken. Nothing is buffered beyond one chunk.
func copyTee(ctx context.Context, job *Job, f *os.File, reader io.Reader) error {
	buf := make([]byte, downloadChunkSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := reader.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			job.mu.Lock()
			job.written += int64(n)
			job.cond.Broadcast()
			job.mu.Unlock()
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

// tailReader reads a sequential job's temp file, blocking until more bytes are
// available or the job finishes.
type tailReader struct {
	ctx context.Context
	job *Job
	f   *os.File
	pos int64
}

// newTailReader opens the job's temp file for tailing. The caller must Close it.
func newTailReader(ctx context.Context, job *Job) (*tailReader, error) {
	f, err := os.Open(job.tmpPath)
	if err != nil {
		return nil, err
	}
	return &tailReader{ctx: ctx, job: job, f: f}, nil
}

func (t *tailReader) Read(p []byte) (int, error) {
	for {
		n, err := t.f.Read(p)
		if n > 0 {
			t.pos += int64(n)
			return n, nil
		}
		if err != nil && err != io.EOF {
			return 0, err
		}

		t.job.mu.Lock()
		for {
			if ctxErr := t.ctx.Err(); ctxErr != nil {
				t.job.mu.Unlock()
				return 0, ctxErr
			}
			if t.pos < t.job.written {
				break
			}
			if t.job.done {
				jobErr := t.job.err
				t.job.mu.Unlock()
				if jobErr != nil {
					return 0, jobErr
				}
				return 0, io.EOF
			}
			t.job.cond.Wait()
		}
		t.job.mu.Unlock()
	}
}

func (t *tailReader) Close() error {
	return t.f.Close()
}

// randomToken returns a short random hex string for unique temp filenames.
func randomToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// httpProbe probes an upstream with a 1-byte range request.
func httpProbe(ctx context.Context, rawURL string) (int64, bool, error) {
	if err := guardUpstreamURL(ctx, rawURL); err != nil {
		return 0, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Range", "bytes=0-0")

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))

	switch resp.StatusCode {
	case http.StatusPartialContent:
		if total := parseContentRangeTotal(resp.Header.Get("Content-Range")); total > 0 {
			return total, true, nil
		}
		return -1, false, nil
	case http.StatusOK:
		return resp.ContentLength, false, nil
	default:
		return 0, false, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}
}

// httpFetchRange is the default rangeFetchFunc.
func httpFetchRange(ctx context.Context, rawURL string, start, end int64) (io.ReadCloser, error) {
	if err := guardUpstreamURL(ctx, rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Encoding", "identity")
	if !(start == 0 && end < 0) {
		if end < 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
		} else {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		}
	}

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, fmt.Errorf("upstream returned status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// parseContentRangeTotal extracts the total from "bytes start-end/total".
func parseContentRangeTotal(header string) int64 {
	i := strings.LastIndexByte(header, '/')
	if i < 0 {
		return -1
	}
	total, err := strconv.ParseInt(strings.TrimSpace(header[i+1:]), 10, 64)
	if err != nil {
		return -1
	}
	return total
}

// guardUpstreamURL provides a basic SSRF guard: only http(s) URLs are allowed and
// the host must not resolve to a loopback/private/link-local address.
func guardUpstreamURL(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid upstream URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("upstream URL must be http(s)")
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("upstream URL missing host")
	}

	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip.Unmap()}
	} else {
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return fmt.Errorf("resolve upstream host: %w", err)
		}
		ips = make([]netip.Addr, 0, len(resolved))
		for _, ip := range resolved {
			ips = append(ips, ip.Unmap())
		}
	}

	if slices.ContainsFunc(ips, isDisallowedIP) {
		return fmt.Errorf("upstream host resolves to a disallowed address")
	}
	return nil
}

// isDisallowedIP reports whether an upstream IP is in a forbidden range. It is a
// package var so tests can relax it to reach a loopback httptest server.
var isDisallowedIP = func(ip netip.Addr) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
