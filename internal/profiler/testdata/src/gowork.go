package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"
	"time"
)

func heavyStringify(n int) int {
	var b strings.Builder
	for i := 0; i < n; i++ {
		o := map[string]any{"a": i, "b": strings.Repeat("x", 10)}
		j, _ := json.Marshal(o) // line 18 hot
		b.Write(j)
		if b.Len() > 100000 {
			b.Reset()
		}
	}
	return b.Len()
}

func main() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	fmt.Println("pprof on", ln.Addr().String())
	go http.Serve(ln, nil)
	go func() {
		for {
			heavyStringify(2500)
		}
	}()
	d, _ := time.ParseDuration(os.Args[1])
	time.Sleep(d)
}
