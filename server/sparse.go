package main

import (
	"context"
	"io"
	"os"
	"sync"
)

// maxFillChunk is the default bound on a single upstream range fetch so the fill
// semaphore cycles and seek-driven fetches are not starved behind one huge
// sequential fetch. The effective value is per-SparseFile (see chunk) so tests
// can shrink it.
const maxFillChunk int64 = 8 << 20

// interval is a half-open byte range [start, end).
type interval struct {
	start, end int64
}

// intervalSet is a sorted, merged, non-overlapping, non-adjacent set of ranges.
type intervalSet struct {
	items []interval
}

// add inserts [start, end), merging with any overlapping or adjacent ranges.
func (s *intervalSet) add(start, end int64) {
	if start >= end {
		return
	}
	out := make([]interval, 0, len(s.items)+1)
	inserted := false
	for _, it := range s.items {
		switch {
		case it.end < start: // strictly before, gap remains
			out = append(out, it)
		case it.start > end: // strictly after, gap remains
			if !inserted {
				out = append(out, interval{start, end})
				inserted = true
			}
			out = append(out, it)
		default: // overlapping or adjacent: absorb
			if it.start < start {
				start = it.start
			}
			if it.end > end {
				end = it.end
			}
		}
	}
	if !inserted {
		out = append(out, interval{start, end})
	}
	s.items = out
}

// remove subtracts [start, end) from the set.
func (s *intervalSet) remove(start, end int64) {
	if start >= end {
		return
	}
	out := make([]interval, 0, len(s.items)+1)
	for _, it := range s.items {
		if it.end <= start || it.start >= end {
			out = append(out, it)
			continue
		}
		if it.start < start {
			out = append(out, interval{it.start, start})
		}
		if it.end > end {
			out = append(out, interval{end, it.end})
		}
	}
	s.items = out
}

// covers reports whether [start, end) is fully present. Because the set is merged
// (no adjacency), this holds iff a single stored interval contains it.
func (s *intervalSet) covers(start, end int64) bool {
	if start >= end {
		return true
	}
	for _, it := range s.items {
		if it.start <= start && it.end >= end {
			return true
		}
	}
	return false
}

// missing returns the sub-ranges of [start, end) not present in the set.
func (s *intervalSet) missing(start, end int64) []interval {
	var gaps []interval
	pos := start
	for _, it := range s.items {
		if it.end <= pos {
			continue
		}
		if it.start >= end {
			break
		}
		if it.start > pos {
			gaps = append(gaps, interval{pos, min(it.start, end)})
		}
		if it.end > pos {
			pos = it.end
		}
		if pos >= end {
			break
		}
	}
	if pos < end {
		gaps = append(gaps, interval{pos, end})
	}
	return gaps
}

// runEnd returns the end of the present run covering off, or off if off is absent.
func (s *intervalSet) runEnd(off int64) int64 {
	for _, it := range s.items {
		if it.start <= off && off < it.end {
			return it.end
		}
	}
	return off
}

// SparseFile is a fixed-size on-disk file filled out of order, with an in-memory
// map of which byte ranges are present and which are being fetched.
type SparseFile struct {
	path  string
	size  int64
	chunk int64 // max bytes reserved per fill

	mu       sync.Mutex
	cond     *sync.Cond
	present  intervalSet
	inflight intervalSet
	f        *os.File
	closed   bool
	done     bool
	err      error
}

// newSparseFile creates a sparse file truncated to size.
func newSparseFile(path string, size, chunk int64) (*SparseFile, error) {
	if chunk <= 0 {
		chunk = maxFillChunk
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return nil, err
	}
	sf := &SparseFile{path: path, size: size, chunk: chunk, f: f}
	sf.cond = sync.NewCond(&sf.mu)
	return sf, nil
}

// writeAt writes p at off and marks the range present.
func (s *SparseFile) writeAt(p []byte, off int64) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return os.ErrClosed
	}
	s.mu.Unlock()

	if _, err := s.f.WriteAt(p, off); err != nil {
		return err
	}

	s.mu.Lock()
	s.present.add(off, off+int64(len(p)))
	s.cond.Broadcast()
	s.mu.Unlock()
	return nil
}

// reserve returns the sub-ranges of [start, end) that are neither present nor
// already in flight, each capped to chunk, marking them in flight.
func (s *SparseFile) reserve(start, end int64) []interval {
	if start < 0 {
		start = 0
	}
	if end > s.size {
		end = s.size
	}
	if start >= end {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []interval
	for _, gap := range s.present.missing(start, end) {
		for _, sub := range s.inflight.missing(gap.start, gap.end) {
			for p := sub.start; p < sub.end; p += s.chunk {
				e := min(p+s.chunk, sub.end)
				s.inflight.add(p, e)
				out = append(out, interval{p, e})
			}
		}
	}
	return out
}

// reserveLowest reserves a single chunk-capped range at the lowest offset that is
// neither present nor in flight, so the sequential filler advances one chunk at a
// time and leaves capacity for concurrent seek-driven fills. ok is false when
// nothing remains to reserve right now.
func (s *SparseFile) reserveLowest() (interval, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, gap := range s.present.missing(0, s.size) {
		for _, sub := range s.inflight.missing(gap.start, gap.end) {
			e := min(sub.start+s.chunk, sub.end)
			s.inflight.add(sub.start, e)
			return interval{sub.start, e}, true
		}
	}
	return interval{}, false
}

// releaseInflight removes a reserved range from the in-flight set. The written
// portion remains tracked by the present set, so released-but-unwritten bytes
// become eligible for a later reserve (retry).
func (s *SparseFile) releaseInflight(iv interval) {
	s.mu.Lock()
	s.inflight.remove(iv.start, iv.end)
	s.cond.Broadcast()
	s.mu.Unlock()
}

// availableEnd returns the end of the present run covering off.
func (s *SparseFile) availableEnd(off int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.present.runEnd(off)
}

// waitByte blocks until off is present, or the file is done / errored / ctx done.
func (s *SparseFile) waitByte(ctx context.Context, off int64) error {
	if off < 0 || off >= s.size {
		return io.EOF
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.present.covers(off, off+1) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.done {
			if s.err != nil {
				return s.err
			}
			return io.ErrUnexpectedEOF
		}
		s.cond.Wait()
	}
}

// complete reports whether every byte is present.
func (s *SparseFile) complete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.present.covers(0, s.size)
}

// waitProgress blocks until the next state change (a write, a release, done, or
// ctx cancellation), so a driver loop can re-evaluate without busy-spinning.
func (s *SparseFile) waitProgress(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil || s.done {
		return
	}
	s.cond.Wait()
}

// wake broadcasts to all waiters (used to unblock them on ctx cancellation).
func (s *SparseFile) wake() {
	s.mu.Lock()
	s.cond.Broadcast()
	s.mu.Unlock()
}

// markDone records terminal state and wakes all waiters.
func (s *SparseFile) markDone(err error) {
	s.mu.Lock()
	s.done = true
	if s.err == nil {
		s.err = err
	}
	s.cond.Broadcast()
	s.mu.Unlock()
}

// closeWriter closes the writer fd. Readers hold their own fds and are unaffected.
func (s *SparseFile) closeWriter() error {
	s.mu.Lock()
	s.closed = true
	s.cond.Broadcast()
	s.mu.Unlock()
	return s.f.Close()
}
