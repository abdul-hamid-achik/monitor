// Command go-crash is a minimal workload for internal/stacktrace's Go
// golden test: it panics in main.main at a known, fixed line so a parser
// test can assert the exact crash frame without depending on timing (unlike
// go-plain/go-pprof, which panic after a 40s timer).
package main

import "fmt"

func main() {
	fmt.Println("[go-crash] starting")
	panic("go-crash workload: intentional uncaught failure")
}
