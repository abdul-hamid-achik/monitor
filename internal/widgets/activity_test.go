package widgets

import (
	"strings"
	"testing"
	"time"
)

func TestRenderActivityLineDotsAndHashes(t *testing.T) {
	got := RenderActivityLine([]int64{0, 0, 0, 0, 0, 0, 1, 4, 3, 2})
	want := "......####"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderActivityLineEmptyInput(t *testing.T) {
	if got := RenderActivityLine(nil); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestRenderActivityLineNeverEmitsANSI(t *testing.T) {
	got := RenderActivityLine([]int64{1, 0, 1})
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("output contains an ANSI escape: %q", got)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want exactly len(buckets)", len(got))
	}
}

func TestBucketCountsDistributesIntoEqualWidthBuckets(t *testing.T) {
	since := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	until := since.Add(10 * time.Hour)
	times := []time.Time{
		since.Add(30 * time.Minute),             // bucket 0
		since.Add(30 * time.Minute),             // bucket 0
		since.Add(6*time.Hour + 30*time.Minute), // bucket 6
		since.Add(9*time.Hour + 59*time.Minute), // bucket 9
	}
	got := BucketCounts(times, since, until, 10)
	want := []int64{2, 0, 0, 0, 0, 0, 1, 0, 0, 1}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bucket %d = %d, want %d (got %v)", i, got[i], want[i], got)
		}
	}
}

func TestBucketCountsDropsOutOfWindowTimestamps(t *testing.T) {
	since := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	until := since.Add(time.Hour)
	times := []time.Time{since.Add(-time.Minute), until, until.Add(time.Minute)}
	got := BucketCounts(times, since, until, 4)
	for i, c := range got {
		if c != 0 {
			t.Fatalf("bucket %d = %d, want 0 (all timestamps are out of window)", i, c)
		}
	}
}

func TestBucketCountsInvalidInputsReturnEmptyNonNil(t *testing.T) {
	now := time.Now()
	if got := BucketCounts(nil, now, now.Add(time.Hour), 0); got == nil || len(got) != 0 {
		t.Fatalf("bucketCount<=0: got %v, want empty non-nil", got)
	}
	if got := BucketCounts(nil, now, now, 5); got == nil || len(got) != 0 {
		t.Fatalf("until==since: got %v, want empty non-nil", got)
	}
	if got := BucketCounts(nil, now, now.Add(-time.Hour), 5); got == nil || len(got) != 0 {
		t.Fatalf("until<since: got %v, want empty non-nil", got)
	}
}
