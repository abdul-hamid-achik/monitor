# polyglot examples

Small workloads used by monitor's specs, docs, and demos. Each one has a
CPU-hot line inside a multi-line function, prints caught errors with stack
traces to stderr every few seconds, and crashes with an uncaught error after
~40 seconds.

| Dir | Runtime | Hot line | Error sites |
|---|---|---|---|
| `js/` | Node, Bun, Deno (`workload.js`) | `heavyStringify` line 17 | `flakyParse` line 31 (caught), `detonate` line 49 (uncaught) |
| `python/` | Python 3.x | `heavy_stringify` | `flaky_parse` (caught), uncaught `RuntimeError` |
| `ruby/` | Ruby 3.x | `heavy_stringify` | `flaky_parse` (caught), uncaught `RuntimeError` |
| `go-pprof/` | Go, exposes `net/http/pprof` on 127.0.0.1:6069 | `main.heavyStringify` (`json.Marshal`) | caught errors, `panic` |
| `go-plain/` | Go, no pprof | `main.heavyStringify` | caught errors, `panic` |

The Go examples are separate modules (own `go.mod`) so they stay out of
monitor's own build.
