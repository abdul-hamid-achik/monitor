package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"
)

func heavyStringify(items []int) []int {
	out := make([]int, 0, len(items))
	for _, item := range items {
		doubled := item * 2
		var b strings.Builder
		for i := 0; i < 2500; i++ {
			data, _ := json.Marshal(map[string]interface{}{"i": i, "item": item, "doubled": doubled, "pad": strings.Repeat("x", 64)})
			b.Write(data) // HOT LINE (line 17)
		}
		out = append(out, b.Len())
	}
	return out
}

func processBatch(n int) []int {
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	return heavyStringify(items)
}

func flakyParse(raw string) (map[string]interface{}, error) {
	if strings.Contains(raw, "bad") {
		return nil, fmt.Errorf("flakyParse: malformed payload near token %q", raw[:8])
	}
	var m map[string]interface{}
	err := json.Unmarshal([]byte(raw), &m)
	return m, err
}

func tickErrors() {
	for _, p := range []string{`{"ok":true}`, "bad-payload-123", `{"ok":true}`} {
		if _, err := flakyParse(p); err != nil {
			fmt.Fprintf(os.Stderr, "[go-pprof] caught in tickErrors: %v\n", err)
		}
	}
}

// Row is one retained record in the in-memory index buildIndex grows below
// -- a deliberately BOUNDED (see maxIndexBuilds), not actually unbounded,
// allocation, so a `monitor hot <pid> --type heap` capture has real,
// sustained inuse_space bytes to attribute to a specific line, unlike
// heavyStringify's transient per-call garbage above, which a GC cycle may
// already have reclaimed by the time a snapshot is taken.
type Row struct {
	ID  int
	Key string
	Pad string
}

// idx is written ONLY by buildIndex, which is itself called only from the
// one dedicated ticker goroutine main() starts below -- no other goroutine
// reads or writes it, so it deliberately carries no mutex/sync import: a
// second writer would need one, but adding one preemptively here would
// shift every line number below it, which every OTHER spec/fixture in this
// repo that names a specific main.go line (json.Marshal at line 20 above,
// specs/profile_go_pprof.yml and specs/hot_file.yml) depends on staying
// put.
var idx = map[string][]Row{}

// maxIndexBuilds / indexBuildBatch bound buildIndex's total retained
// growth (roughly maxIndexBuilds*indexBuildBatch rows, a few dozen MB) so
// the workload's heap stays bounded for its whole run instead of growing
// without limit; once the cap is reached, idx stays alive at that size
// (still real inuse_space to profile) rather than continuing to grow.
const (
	maxIndexBuilds  = 200
	indexBuildBatch = 500
)

// buildIndex is go-pprof's planted HEAP allocation (see the roadmap's own
// "main.buildIndex" mockup, "7. Go: CPU y heap desde el proto de pprof"):
// every Row is retained in idx for the life of the process (until the cap
// above), so its inuse_space keeps accumulating on the append call below —
// the exact line `monitor hot --type heap` should name.
func buildIndex(n int) {
	rows := make([]Row, 0, n)
	for i := 0; i < n; i++ {
		// HOT LINE (heap): Key/Pad's fmt.Sprintf/strings.Repeat calls are
		// where the retained bytes actually get allocated (verified live:
		// `monitor hot <pid> --type heap --func main.buildIndex` attributes
		// ~91% cum inuse_space here, not to the map-insertion line below).
		rows = append(rows, Row{ID: i, Key: fmt.Sprintf("k%d", i%37), Pad: strings.Repeat("y", 512)})
	}
	for _, r := range rows {
		idx[r.Key] = append(idx[r.Key], r)
	}
}

func main() {
	pid := os.Getpid()
	fmt.Fprintf(os.Stderr, "[go-pprof] pid=%d workload starting\n", pid)

	go func() {
		_ = http.ListenAndServe("127.0.0.1:6069", nil)
	}()

	start := time.Now()
	stop := make(chan struct{})

	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				processBatch(40)
				time.Sleep(40 * time.Millisecond)
			}
		}
	}()

	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				tickErrors()
			}
		}
	}()

	go func() {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		builds := 0
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if builds >= maxIndexBuilds {
					continue // steady-state: idx stays retained, stops growing
				}
				buildIndex(indexBuildBatch)
				builds++
			}
		}
	}()

	time.Sleep(40 * time.Second)
	close(stop)
	panic(fmt.Sprintf("go-pprof workload: intentional uncaught failure at t=%.1fs", time.Since(start).Seconds()))
}
