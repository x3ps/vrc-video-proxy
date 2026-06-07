package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestIntervalSetAddMergesOverlappingAndAdjacent(t *testing.T) {
	var s intervalSet
	s.add(0, 10)
	s.add(20, 30)
	s.add(10, 20) // bridges the two -> single [0,30)
	if !reflect.DeepEqual(s.items, []interval{{0, 30}}) {
		t.Fatalf("items = %v, want [{0 30}]", s.items)
	}

	var s2 intervalSet
	s2.add(0, 5)
	s2.add(10, 15)
	s2.add(3, 12) // overlaps both -> [0,15)
	if !reflect.DeepEqual(s2.items, []interval{{0, 15}}) {
		t.Fatalf("items = %v, want [{0 15}]", s2.items)
	}
}

func TestIntervalSetCovers(t *testing.T) {
	var s intervalSet
	s.add(0, 10)
	s.add(20, 30)
	if !s.covers(2, 8) {
		t.Fatal("covers(2,8) = false, want true")
	}
	if s.covers(5, 25) {
		t.Fatal("covers(5,25) = true, want false (gap at 10-20)")
	}
	if !s.covers(5, 5) {
		t.Fatal("empty range should be covered")
	}
}

func TestIntervalSetMissing(t *testing.T) {
	var s intervalSet
	s.add(10, 20)
	s.add(30, 40)
	got := s.missing(0, 50)
	want := []interval{{0, 10}, {20, 30}, {40, 50}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missing = %v, want %v", got, want)
	}
	if g := s.missing(12, 18); g != nil {
		t.Fatalf("missing within a present run = %v, want nil", g)
	}
}

func TestIntervalSetRunEnd(t *testing.T) {
	var s intervalSet
	s.add(10, 25)
	if got := s.runEnd(15); got != 25 {
		t.Fatalf("runEnd(15) = %d, want 25", got)
	}
	if got := s.runEnd(30); got != 30 {
		t.Fatalf("runEnd(30) = %d, want 30 (absent)", got)
	}
}

func TestIntervalSetRemove(t *testing.T) {
	var s intervalSet
	s.add(0, 100)
	s.remove(20, 40)
	want := []interval{{0, 20}, {40, 100}}
	if !reflect.DeepEqual(s.items, want) {
		t.Fatalf("items = %v, want %v", s.items, want)
	}
}

func newTestSparse(t *testing.T, size, chunk int64) *SparseFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sparse.part")
	sf, err := newSparseFile(path, size, chunk)
	if err != nil {
		t.Fatalf("newSparseFile returned error: %v", err)
	}
	t.Cleanup(func() { _ = sf.closeWriter() })
	return sf
}

func TestSparseFileOutOfOrderWriteAndComplete(t *testing.T) {
	sf := newTestSparse(t, 30, 100)

	if sf.complete() {
		t.Fatal("empty sparse file reported complete")
	}
	// Write the tail first, then the head.
	if err := sf.writeAt([]byte("3456789012"), 20); err != nil {
		t.Fatalf("writeAt tail: %v", err)
	}
	if sf.availableEnd(0) != 0 {
		t.Fatal("offset 0 should be absent")
	}
	if err := sf.writeAt(make([]byte, 20), 0); err != nil {
		t.Fatalf("writeAt head: %v", err)
	}
	if !sf.complete() {
		t.Fatal("file should be complete after covering [0,30)")
	}
	if got := sf.availableEnd(0); got != 30 {
		t.Fatalf("availableEnd(0) = %d, want 30", got)
	}
}

func TestSparseFileReserveDedupsAndChunks(t *testing.T) {
	sf := newTestSparse(t, 100, 16)

	got := sf.reserve(0, 100)
	// 100 bytes / 16-byte chunks -> 7 ranges (last is 96..100).
	want := []interval{{0, 16}, {16, 32}, {32, 48}, {48, 64}, {64, 80}, {80, 96}, {96, 100}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reserve = %v, want %v", got, want)
	}
	// Everything is now in flight: a second reserve yields nothing.
	if again := sf.reserve(0, 100); again != nil {
		t.Fatalf("second reserve = %v, want nil (all in flight)", again)
	}
	// Releasing a range makes it reservable again.
	sf.releaseInflight(interval{0, 16})
	if again := sf.reserve(0, 100); !reflect.DeepEqual(again, []interval{{0, 16}}) {
		t.Fatalf("reserve after release = %v, want [{0 16}]", again)
	}
}

func TestSparseFileReserveLowestSkipsPresentAndInflight(t *testing.T) {
	sf := newTestSparse(t, 100, 16)

	// Mark [0,16) present and reserve [16,32) in flight; the lowest free gap is 32.
	if err := sf.writeAt(make([]byte, 16), 0); err != nil {
		t.Fatalf("writeAt: %v", err)
	}
	sf.reserve(16, 32)

	iv, ok := sf.reserveLowest()
	if !ok {
		t.Fatal("reserveLowest returned ok=false")
	}
	if iv != (interval{32, 48}) {
		t.Fatalf("reserveLowest = %v, want {32 48}", iv)
	}
}
