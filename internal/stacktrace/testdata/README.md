# Stack-trace fixtures

`dogfood/*.stderr.log` are real stderr captures of `examples/polyglot/*`
workloads (Node 26.7, Deno 2.9.7, Bun 1.4.2, Python 3.14.2, Ruby 3.4.8,
Go 1.26) recorded on 2026-09-22. Absolute paths were rewritten to
`/repo/examples/polyglot/...`. Each workload prints periodic caught errors with
full stacks to stderr and crashes with an uncaught error after ~40s.

Only synthetic or dogfood data lives here: never commit real application logs
(customer data, employer code).
