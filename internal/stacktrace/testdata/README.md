# Stack-trace fixtures

Only synthetic or dogfood data lives here: never commit real application logs
(customer data, employer code). Every absolute path was rewritten before
committing: the app to `/repo/...`, GOROOT to `/usr/local/go`, the Python and
Ruby installs to `/usr/local`.

| Dir | What | How it was made |
|---|---|---|
| `dogfood/` | stderr/stdout of the `examples/polyglot/*` workloads: periodic caught errors with full stacks, then an uncaught crash after ~40s | Recorded 2026-09-22 with Node 26.7, Deno 2.9.7, Bun 1.4.2, Python 3.14.2, Ruby 3.4.8, Go 1.26 (`go-crash` and `go-zap-stdout` run straight from their example dirs) |
| `real/{node,deno,bun}/` | One file per error shape: `[ERR_X]` codes, `file://` / `node:internal` code-frame intros, `console.error("msg:", err)`, nested `[cause]` chains, `Caused by:`, property blocks, ENOENT, rejections, `Promise.all`, thrown strings, multi-line messages, `[eval]`, process warnings | A scratch script (`scenarios.mjs`, `cjs.js`, `missing.mjs`) run under Node 26.7.0, Deno 2.9.7 (`NO_COLOR=1`) and Bun 1.4.2 on 2026-09-22, stdout+stderr captured |
| `real/go/` | Go 1.26.6 panics (nil-pointer SIGSEGV in a pointer-receiver method under `internal/`, recovered re-panic, `[recovered, repanicked]`, generics, a non-main goroutine, a net/http handler panic recovered and logged by the server, a bare `debug.PrintStack` dump) and go.uber.org/zap v1.28.0 + github.com/pkg/errors v0.9.1 output (development console, production console, production JSON with float and ISO `ts`, sugar `Errorf("%+v")`, an error entry followed by info entries and a panic) | A throwaway module outside this repo importing zap and pkg/errors only to generate the text; monitor itself depends on neither |
| `real/python/` | CPython 3.14.2: `python -m pkg` (`<frozen runpy>`), module-level `logging.exception` (`ERROR:root:`), asctime-format logger line before a traceback, a 4-segment cause/context chain, a message-less exception, a thread crash, `traceback.print_exc()`, notes, an ExceptionGroup, a SyntaxError | A scratch script run with `PYTHONDONTWRITEBYTECODE=1` |
| `real/ruby/` | Ruby 3.4.8: `NoMethodError` with the error_highlight snippet, a 3-level cause chain, a deep backtrace, stdlib Logger with and without a backtrace, a thread crash report, a top-level raise | A scratch script run from its own directory (Ruby prints relative paths) |
| `real/*/` (review captures) | Shapes found by the adversarial review, captured with the same real runtimes: long JSON / assertion-diff messages, AggregateError, a deprecation warning or a lone header before a prefixed error, `deno test` failures, a `${level}: ${stack}` logger line, a `go test` panic, a Python class defined in a function and an ExceptionGroup inside a chain, Ruby `Class: message` lines before a backtrace | Scratch scripts outside the repo, 2026-09-22 |
| `chained/` | One-level Python (`direct cause`, `During handling`) and Node `[cause]` chains, with the scripts that produced them | Node 26.7 / Python 3.14.2 |
| `synthetic/` | `python-logging` (real Python 3.14.2 output of the committed script) and `tslog.txt`, a hand-written typescript-logging / Temporal-style record (there is no single real format to capture) | Hand-written / generated |
| `clean/` | Logs that must produce no events: info-level records of every supported logger, JSON error lines from non-Go loggers (winston, pino, bunyan, structlog, ECS), "Error:" lines with no stack, Bun-like `N \|` gutters (rustc, psql, markdown), Python warnings, Go test failures, nginx/Laravel/vitest output, a prose "panic ...:" line right before a SIGQUIT-style goroutine dump | Hand-written |

The golden table in `golden_test.go` lists the exact events each capture must
produce; `clean_test.go` asserts the `clean/` files produce none, with LF and
CRLF line endings.
