package history

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.veclite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	base := time.Now().Add(-10 * time.Minute)
	for i := 0; i < 6; i++ {
		if err := s.Append(Sample{Timestamp: base.Add(time.Duration(i) * time.Minute), Metric: "cpu.usage", Value: float64(i * 10)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// A different metric that must not leak into a cpu query.
	_ = s.Append(Sample{Timestamp: base, Metric: "mem.usage", Value: 99})

	pts, err := s.Query("cpu.usage", base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// minutes 2,3,4,5 => 4 points, oldest-first.
	if len(pts) != 4 {
		t.Fatalf("got %d points, want 4", len(pts))
	}
	if pts[0].Value != 20 || pts[len(pts)-1].Value != 50 {
		t.Errorf("boundary values = %v..%v, want 20..50", pts[0].Value, pts[len(pts)-1].Value)
	}
	for i := 1; i < len(pts); i++ {
		if pts[i].Timestamp.Before(pts[i-1].Timestamp) {
			t.Errorf("points not oldest-first at %d", i)
		}
	}
}

// TestPersistsAcrossReopen verifies the recorder/query split: data written by
// one Store survives a Close + reopen (the Unix-nano timestamp round-trips).
func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.veclite")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ts := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := w.Append(Sample{Timestamp: ts, Metric: "cpu.usage", Value: 42}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer r.Close()
	pts, err := r.Query("cpu.usage", ts.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(pts) != 1 || pts[0].Value != 42 {
		t.Fatalf("reloaded points = %+v, want one value 42", pts)
	}
	if !pts[0].Timestamp.Equal(ts) {
		t.Errorf("reloaded timestamp = %v, want %v (round-trip)", pts[0].Timestamp, ts)
	}
}

func TestSummarize(t *testing.T) {
	if s := Summarize(nil); s.Count != 0 {
		t.Errorf("empty summarize count = %d, want 0", s.Count)
	}
	now := time.Now()
	pts := []Point{
		{now, 10}, {now.Add(time.Second), 30}, {now.Add(2 * time.Second), 20}, {now.Add(3 * time.Second), 40},
	}
	s := Summarize(pts)
	if s.Count != 4 || s.Min != 10 || s.Max != 40 || s.First != 10 || s.Last != 40 || s.Trend != 30 {
		t.Errorf("summary = %+v", s)
	}
	if s.Avg != 25 {
		t.Errorf("avg = %v, want 25", s.Avg)
	}
}
