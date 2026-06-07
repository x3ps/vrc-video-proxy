package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// clearWriteDeadline removes the server's WriteTimeout for the current request
// so long video streams are not cut off mid-transfer.
func clearWriteDeadline(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	// SetWriteDeadline(zero) disables the deadline; ignore unsupported errors.
	_ = rc.SetWriteDeadline(time.Time{})
}

// serveCachedFile serves a finished cache entry with full HEAD/Range support via
// http.ServeContent.
func serveCachedFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	clearWriteDeadline(w)
	w.Header().Set("Content-Type", "video/mp4")
	// ServeContent handles HEAD, Range, 206, Content-Range, Content-Length and
	// Accept-Ranges. The name is only used for content-type sniffing, which we
	// have already set explicitly.
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// serveLive streams a download-in-progress to the client as it is written to
// disk. It serves sequentially from offset 0 and does not support Range.
func serveLive(w http.ResponseWriter, r *http.Request, job *Job) {
	reader, err := newTailReader(r.Context(), job)
	if err != nil {
		// The temp file may have been finalized (renamed) between the cache miss
		// check and now; let the caller fall back to the cached file.
		http.Error(w, "live stream unavailable", http.StatusServiceUnavailable)
		return
	}
	defer reader.Close()

	clearWriteDeadline(w)
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "none")

	job.mu.Lock()
	contentLength := job.contentLength
	job.mu.Unlock()
	if contentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	_, err = io.Copy(w, reader)
	if err != nil && !errors.Is(err, r.Context().Err()) {
		// Client disconnects and context cancellation are expected; nothing to do
		// but stop. The background job keeps running and still fills the cache.
		return
	}
}

// parseSingleRange parses a single HTTP byte range against a known size.
//   - ok=false, satisfiable=true  → no usable single range; serve the full body.
//   - ok=true,  satisfiable=true  → serve [start, end].
//   - satisfiable=false           → reply 416.
func parseSingleRange(header string, size int64) (start, end int64, ok, satisfiable bool) {
	const prefix = "bytes="
	afterPrefix, ok := strings.CutPrefix(header, prefix)
	if !ok {
		return 0, 0, false, true
	}
	spec := strings.TrimSpace(afterPrefix)
	if spec == "" || strings.Contains(spec, ",") {
		return 0, 0, false, true // multi-range: fall back to full body
	}
	before, after, ok0 := strings.Cut(spec, "-")
	if !ok0 {
		return 0, 0, false, true
	}
	startStr := strings.TrimSpace(before)
	endStr := strings.TrimSpace(after)

	if startStr == "" { // suffix range: last N bytes
		if endStr == "" {
			return 0, 0, false, true
		}
		n, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true, true
	}

	s, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || s < 0 {
		return 0, 0, false, false
	}
	if s >= size {
		return 0, 0, false, false
	}
	e := size - 1
	if endStr != "" {
		e, err = strconv.ParseInt(endStr, 10, 64)
		if err != nil || e < s {
			return 0, 0, false, false
		}
		if e >= size {
			e = size - 1
		}
	}
	return s, e, true, true
}

// serveSparse serves a sparse (Range-capable) download in progress. It honors
// HTTP Range with 206/Content-Range and triggers on-demand back-fill so a seek
// ahead of the sequential filler is served from its own upstream fetch.
func serveSparse(w http.ResponseWriter, r *http.Request, job *Job, m *JobManager) {
	sf := job.sf
	size := job.size

	clearWriteDeadline(w)
	h := w.Header()
	h.Set("Content-Type", "video/mp4")
	h.Set("Accept-Ranges", "bytes")

	start, end := int64(0), size-1
	status := http.StatusOK
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		s, e, ok, satisfiable := parseSingleRange(rangeHeader, size)
		if !satisfiable {
			h.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			http.Error(w, "requested range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if ok {
			start, end = s, e
			status = http.StatusPartialContent
			h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		}
	}

	h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}

	rf, err := os.Open(job.tmpPath)
	if err != nil {
		return // file already finalized; the player will re-request and hit cache
	}
	defer rf.Close()

	ctx := r.Context()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			sf.wake()
		case <-stop:
		}
	}()

	buf := make([]byte, downloadChunkSize)
	pos := start
	for pos <= end {
		m.requestFillAt(job, pos)
		if err := sf.waitByte(ctx, pos); err != nil {
			return
		}
		limit := min(sf.availableEnd(pos), end+1)
		for pos < limit {
			n := min(limit-pos, int64(len(buf)))
			rn, rerr := rf.ReadAt(buf[:n], pos)
			if rn > 0 {
				if _, werr := w.Write(buf[:rn]); werr != nil {
					return
				}
				pos += int64(rn)
			}
			if rerr != nil {
				break
			}
		}
	}
}
