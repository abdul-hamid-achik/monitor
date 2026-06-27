package history

import (
	"sort"
	"time"
)

// Summary aggregates a metric series over its time window.
type Summary struct {
	Count int       `json:"count"`
	Min   float64   `json:"min"`
	Max   float64   `json:"max"`
	Avg   float64   `json:"avg"`
	P95   float64   `json:"p95"`
	First float64   `json:"first"`
	Last  float64   `json:"last"`
	Trend float64   `json:"trend"` // last - first
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
}

// Summarize computes summary stats over points (oldest-first). The zero value
// is returned for an empty series.
func Summarize(pts []Point) Summary {
	s := Summary{Count: len(pts)}
	if len(pts) == 0 {
		return s
	}
	vals := make([]float64, len(pts))
	sum := 0.0
	s.Min, s.Max = pts[0].Value, pts[0].Value
	for i, p := range pts {
		vals[i] = p.Value
		sum += p.Value
		if p.Value < s.Min {
			s.Min = p.Value
		}
		if p.Value > s.Max {
			s.Max = p.Value
		}
	}
	s.Avg = sum / float64(len(pts))
	s.First = pts[0].Value
	s.Last = pts[len(pts)-1].Value
	s.Trend = s.Last - s.First
	s.From = pts[0].Timestamp
	s.To = pts[len(pts)-1].Timestamp

	sort.Float64s(vals)
	s.P95 = vals[int(0.95*float64(len(vals)-1))]
	return s
}
