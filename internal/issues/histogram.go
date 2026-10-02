package issues

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/veclite"
)

// MaxHistogramBuckets bounds Histogram's output per series, so a caller
// asking for "every minute of the last week" gets an error instead of a
// 10,080-point series per issue.
const MaxHistogramBuckets = 240

// OccurrencePoint is the slice of an Occurrence a histogram needs: which
// issue, when, and how many raw events it subsumes.
type OccurrencePoint struct {
	IssueID    string    `json:"issue_id"`
	ObservedAt time.Time `json:"observed_at"`
	Count      int64     `json:"count"`
}

// OccurrencePoints returns one point per retained occurrence observed at or
// after since, restricted to ids when ids is non-empty. It is one scan of
// the occurrences collection, so a list of sparklines costs one read
// rather than one per issue.
//
// Points cover only RETAINED occurrences: the store keeps a bounded number
// of occurrence bodies (FIFO), so a long-lived noisy issue's oldest points
// are gone while Issue.OccurrenceCount still counts them. Callers present a
// histogram as recent activity, never as a lifetime total.
func (s *Store) OccurrencePoints(since time.Time, ids []string) ([]OccurrencePoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	if !s.db.HasCollection(occurrencesCollection) {
		return []OccurrencePoint{}, nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	var (
		points  []OccurrencePoint
		scanErr error
	)
	s.db.Collection(occurrencesCollection).ForEach(func(record *veclite.Record) bool {
		var occ struct {
			IssueID    string    `json:"issue_id"`
			ObservedAt time.Time `json:"observed_at"`
			Count      int64     `json:"count"`
		}
		if err := json.Unmarshal([]byte(record.Content), &occ); err != nil {
			scanErr = errors.Join(scanErr, err)
			return true
		}
		if len(want) > 0 && !want[occ.IssueID] {
			return true
		}
		if !since.IsZero() && occ.ObservedAt.Before(since) {
			return true
		}
		count := occ.Count
		if count <= 0 {
			count = 1
		}
		points = append(points, OccurrencePoint{IssueID: occ.IssueID, ObservedAt: occ.ObservedAt, Count: count})
		return true
	})
	if points == nil {
		points = []OccurrencePoint{}
	}
	return points, scanErr
}

// Histogram buckets points into n consecutive buckets of width bucket
// starting at start, one series per issue ID. A point outside
// [start, start+n*bucket) is dropped. Every series has exactly n entries.
func Histogram(points []OccurrencePoint, start time.Time, bucket time.Duration, n int) map[string][]int64 {
	series := map[string][]int64{}
	if bucket <= 0 || n <= 0 {
		return series
	}
	end := start.Add(time.Duration(n) * bucket)
	for _, p := range points {
		if p.ObservedAt.Before(start) || !p.ObservedAt.Before(end) {
			continue
		}
		idx := int(p.ObservedAt.Sub(start) / bucket)
		if idx < 0 || idx >= n {
			continue
		}
		s, ok := series[p.IssueID]
		if !ok {
			s = make([]int64, n)
			series[p.IssueID] = s
		}
		s[idx] += p.Count
	}
	return series
}
