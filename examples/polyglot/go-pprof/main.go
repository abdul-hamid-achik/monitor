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

	time.Sleep(40 * time.Second)
	close(stop)
	panic(fmt.Sprintf("go-pprof workload: intentional uncaught failure at t=%.1fs", time.Since(start).Seconds()))
}
