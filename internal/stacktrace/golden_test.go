package stacktrace

import "testing"

// goldenCases is the golden table: every committed capture (real runtime
// output under testdata/real, the polyglot dogfood captures, the chained and
// synthetic fixtures) and the exact events it must produce, rendered by
// summarize as
//
//	parser/runtime level handled Type: Value @crashFunc@file:line(frames) <= cause ... [observed_at]
//
// Each row was checked against the raw capture: the crash frame is the one
// the runtime printed first (innermost), causes run outer -> innermost, and
// zone-less timestamps are read in testZone (UTC-6, where they were
// recorded).
var goldenCases = []struct {
	fixture string
	want    []string
}{
	{"chained/node-cause.stderr.txt", []string{
		`js/node fatal unhandled Error: work failed @wrapIt@node-cause.js:9(3) <= Error: root cause boom @rootCause@node-cause.js:3(10)`,
	}},
	{"chained/py-direct-cause.stderr.txt", []string{
		`python/python fatal unhandled RuntimeError: work failed @wrap_it@py-direct-cause.py:9(2) <= ValueError: root cause boom @root_cause@py-direct-cause.py:2(2)`,
	}},
	{"chained/py-during-handling.stderr.txt", []string{
		`python/python fatal unhandled RuntimeError: work failed while handling @wrap_it@py-during-handling.py:9(2) <= ValueError: root cause boom @root_cause@py-during-handling.py:2(2)`,
	}},
	{"dogfood/bun-inspect.stderr.txt", []string{
		`js/ error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(2)`,
		`js/ error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(2)`,
		`js/ error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(2)`,
		`js/ error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(2)`,
		`js/ error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(2)`,
		`js/ error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(2)`,
		`js/ error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(2)`,
		`js/ error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(2)`,
		`js/ error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(2)`,
		`js/bun fatal unhandled Error: bun workload: intentional uncaught failure at t=40001ms @detonate@workload.js:49(2)`,
	}},
	{"dogfood/deno-inspect.stderr.txt", []string{
		`js/deno error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(7)`,
		`js/deno error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(7)`,
		`js/deno error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(7)`,
		`js/deno error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(7)`,
		`js/deno error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(7)`,
		`js/deno error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(7)`,
		`js/deno error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(7)`,
		`js/deno error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(7)`,
		`js/deno error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(7)`,
		`js/deno fatal unhandled Error: deno workload: intentional uncaught failure at t=40042ms @detonate@workload.js:49(7)`,
	}},
	{"dogfood/go-crash.stderr.txt", []string{
		`gopanic/go fatal unhandled panic: go-crash workload: intentional uncaught failure @main.main@main.go:11(1)`,
	}},
	{"dogfood/go-plain.stderr.txt", []string{}},
	{"dogfood/go-pprof.stderr.txt", []string{
		`gopanic/go fatal unhandled panic: go-pprof workload: intentional uncaught failure at t=40.0s @main.main@main.go:90(1)`,
	}},
	{"dogfood/go-zap-stdout.stdout.txt", []string{
		`zap/go error handled "request failed" @main.(*worker).doWork@main.go:67(4) [2026-09-23T04:56:02.367Z]`,
	}},
	{"dogfood/node-inspect.stderr.txt", []string{
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node fatal unhandled Error: node workload: intentional uncaught failure at t=40020ms @detonate@workload.js:49(4)`,
	}},
	{"dogfood/node-plain.stderr.txt", []string{
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
		`js/node error handled Error: flakyParse: malformed payload near token "bad-payl" @flakyParse@workload.js:31(4)`,
	}},
	{"dogfood/python.stderr.txt", []string{
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@workload.py:31(2)`,
		`python/python fatal unhandled RuntimeError: python workload: intentional uncaught failure at t=40.0s @detonate@workload.py:45(2)`,
	}},
	{"dogfood/ruby.stderr.txt", []string{
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby error handled "" @Object#flaky_parse@workload.rb:26(7)`,
		`ruby/ruby fatal unhandled RuntimeError: ruby workload: intentional uncaught failure at t=40.1s @Object#detonate@workload.rb:42(2)`,
	}},
	{"synthetic/python-logging.stderr.txt", []string{
		`python/python error handled ValueError: flaky_parse: malformed payload near token 'bad-payl' @flaky_parse@python-logging.py:9(2)`,
		`message/python error handled "connection pool exhausted: 0 of 10 connections available" @-(0)`,
	}},
	{"synthetic/tslog.txt", []string{
		`tslog/node error handled ApplicationFailure: activity task failed @runActivity@workload.js:80(2) <= ApplicationFailure: root cause: boom @doWork@workload.js:90(1) [2026-09-22T10:05:00.000Z]`,
	}},
	{"real/bun/assert.txt", []string{
		`js/bun fatal unhandled AssertionError: Expected values to be strictly equal: @@scenarios.mjs:45(1)`,
	}},
	{"real/bun/cjs-handled.txt", []string{
		`js/bun error handled Error: outer failure @outer@cjs.js:17(2)`,
		`js/bun error handled Error: middle failure @middle@cjs.js:10(3)`,
		`js/bun error handled Error: root failure @root@cjs.js:4(4)`,
	}},
	{"real/bun/cjs-missing.txt", []string{
		`js/bun fatal unhandled Error: Cannot find module './no-such-module' from '/repo/app/src/cjs.js' @-(0)`,
	}},
	{"real/bun/cjs-uncaught.txt", []string{
		`js/bun fatal unhandled Error: outer failure @outer@cjs.js:17(2) <= Error: middle failure @middle@cjs.js:10(3) <= Error: root failure @root@cjs.js:4(4)`,
	}},
	{"real/bun/console-prefix.txt", []string{
		`js/bun error handled SyntaxError: JSON Parse error: Expected '}' @@scenarios.mjs:60(1)`,
	}},
	{"real/bun/enoent-handled.txt", []string{
		`js/bun error handled ENOENT: no such file or directory, open 'does-not-exist.json' @@scenarios.mjs:68(1)`,
	}},
	{"real/bun/enoent-uncaught.txt", []string{
		`js/bun fatal unhandled ENOENT: no such file or directory, open 'does-not-exist.json' @@scenarios.mjs:75(1)`,
	}},
	{"real/bun/err-code.txt", []string{
		`js/bun fatal unhandled TypeError: path must be a string or a file descriptor @@scenarios.mjs:42(1)`,
	}},
	{"real/bun/esm-missing.txt", []string{
		`js/bun fatal unhandled Error: Cannot find package 'no-such-pkg-xyz' from '/repo/app/src/missing.mjs' @-(0)`,
	}},
	{"real/bun/handled-cause.txt", []string{
		`js/bun error handled Error: outer failure @outer@scenarios.mjs:19(2)`,
		`js/bun error handled Error: middle failure @middle@scenarios.mjs:12(3)`,
		`js/bun error handled Error: root failure @root@scenarios.mjs:6(4)`,
	}},
	{"real/bun/multiline.txt", []string{
		`js/bun fatal unhandled Error: first line of message @@scenarios.mjs:95(1)`,
	}},
	{"real/bun/promise-all.txt", []string{
		`js/bun fatal unhandled TypeError: user payload is not an object @fetchUser@scenarios.mjs:29(1)`,
	}},
	{"real/bun/props-cause.txt", []string{
		`js/bun fatal unhandled Error: upstream request failed @@scenarios.mjs:88(1) <= Error: socket hang up @@scenarios.mjs:87(1)`,
	}},
	{"real/bun/reject-object.txt", []string{
		`js/bun fatal unhandled "{ code: 42, }" @-(0)`,
	}},
	{"real/bun/reject-string.txt", []string{
		`js/bun fatal unhandled Error: boom @-(0)`,
	}},
	{"real/bun/rejection.txt", []string{
		`js/bun fatal unhandled TypeError: user payload is not an object @fetchUser@scenarios.mjs:29(1)`,
	}},
	{"real/bun/throw-string.txt", []string{
		`js/bun fatal unhandled Error: plain string failure @-(0)`,
	}},
	// Two unrelated console.error(err) calls print exactly like the
	// handled cause chain above (code frame, "error: ...", frames, blank,
	// next code frame): without the "Bun vX" footer of an uncaught crash
	// there is no way to tell a cause from the next error, so handled Bun
	// blocks are never linked.
	{"real/bun/two-errors.txt", []string{
		`js/bun error handled Error: first unrelated failure @@two-errors.mjs:8(1)`,
		`js/bun error handled Error: second unrelated failure @@two-errors.mjs:9(1)`,
	}},
	{"real/bun/typeerror.txt", []string{
		`js/bun fatal unhandled TypeError: undefined is not an object (evaluating 'req.body.length') @handle@scenarios.mjs:34(2)`,
	}},
	{"real/bun/uncaught.txt", []string{
		`js/bun fatal unhandled Error: outer failure @outer@scenarios.mjs:19(2) <= Error: middle failure @middle@scenarios.mjs:12(3) <= Error: root failure @root@scenarios.mjs:6(4)`,
	}},
	{"real/deno/assert.txt", []string{
		`js/deno fatal unhandled AssertionError: Expected values to be strictly equal: @@scenarios.mjs:45(1)`,
	}},
	{"real/deno/console-prefix.txt", []string{
		`js/ error handled SyntaxError: Expected property name or '}' in JSON at position 1 (line 1 column 2) @JSON.parse@<anonymous>:0(2)`,
	}},
	{"real/deno/enoent-handled.txt", []string{
		`js/deno error handled Error: ENOENT: no such file or directory, open 'does-not-exist.json' @Object.readFileSync@fs.ts:423(3)`,
	}},
	{"real/deno/enoent-uncaught.txt", []string{
		`js/deno fatal unhandled Error: ENOENT: no such file or directory, open 'does-not-exist.json' @Object.readFileSync@fs.ts:423(3)`,
	}},
	{"real/deno/err-code.txt", []string{
		`js/deno fatal unhandled TypeError: The "path" argument must be of type string or an instance of Buffer or URL. Received undefined @getValidatedPathToString@utils.mjs:917(3)`,
	}},
	{"real/deno/esm-missing.txt", []string{
		`js/ fatal unhandled Error: Import "no-such-pkg-xyz" not a dependency @@missing.mjs:1(1)`,
	}},
	{"real/deno/handled-cause.txt", []string{
		`js/ error handled Error: outer failure @outer@scenarios.mjs:19(2) <= Error: middle failure @middle@scenarios.mjs:12(3) <= Error: root failure @root@scenarios.mjs:6(4)`,
	}},
	{"real/deno/multiline.txt", []string{
		`js/deno fatal unhandled Error: first line of message @@scenarios.mjs:95(1)`,
	}},
	{"real/deno/promise-all.txt", []string{
		`js/deno fatal unhandled TypeError: user payload is not an object @fetchUser@scenarios.mjs:29(3)`,
	}},
	{"real/deno/props-cause.txt", []string{
		`js/deno fatal unhandled Error: upstream request failed @@scenarios.mjs:88(1) <= Error: socket hang up @@scenarios.mjs:87(1)`,
	}},
	{"real/deno/reject-object.txt", []string{
		`js/deno fatal unhandled "{ code: 42 }" @-(0)`,
	}},
	{"real/deno/reject-string.txt", []string{
		`js/deno fatal unhandled "boom" @-(0)`,
	}},
	{"real/deno/rejection.txt", []string{
		`js/deno fatal unhandled TypeError: user payload is not an object @fetchUser@scenarios.mjs:29(1)`,
	}},
	{"real/deno/throw-string.txt", []string{
		`js/deno fatal unhandled "plain string failure" @-(0)`,
	}},
	{"real/deno/typeerror.txt", []string{
		`js/deno fatal unhandled TypeError: Cannot read properties of undefined (reading 'length') @Server.handle@scenarios.mjs:34(2)`,
	}},
	{"real/deno/uncaught.txt", []string{
		`js/deno fatal unhandled Error: outer failure @outer@scenarios.mjs:19(2) <= Error: middle failure @middle@scenarios.mjs:12(3) <= Error: root failure @root@scenarios.mjs:6(4)`,
	}},
	// A bare goroutine dump (debug.PrintStack) is not an event...
	{"real/go/debug-stack.txt", []string{}},
	// ...but net/http's recovered handler panic is: the log line before
	// the stanza carries the value, and the recovery frames are dropped
	// so the crash frame is where the panic was raised.
	{"real/go/http-panic.txt", []string{
		`gopanic/go error handled panic: runtime error: invalid memory address or nil pointer dereference @example.com/app/internal/svc.(*Server).Handle@svc.go:13(5) [2026-09-23T05:16:04.000Z]`,
	}},
	{"real/go/errorf.txt", []string{
		`gopanic/go fatal unhandled panic: bad input 7 @main.main@main.go:74(1)`,
	}},
	{"real/go/generic.txt", []string{
		`gopanic/go fatal unhandled panic: assignment to entry in nil map @main.main.generic.func1@main.go:35(4)`,
	}},
	{"real/go/goroutine.txt", []string{
		`gopanic/go fatal unhandled panic: runtime error: index out of range [3] with length 0 @main.worker@main.go:52(2)`,
	}},
	{"real/go/nil.txt", []string{
		`gopanic/go fatal unhandled panic: runtime error: invalid memory address or nil pointer dereference @example.com/app/internal/svc.(*Server).Handle@svc.go:13(2)`,
	}},
	{"real/go/recovered.txt", []string{
		`gopanic/go fatal unhandled panic: re-panic after: first failure @main.recovered.func1@main.go:18(4) <= panic: first failure @-(0)`,
	}},
	{"real/go/repanic.txt", []string{
		`gopanic/go fatal unhandled panic: same value @main.repanicSame.func1@main.go:27(4)`,
	}},
	{"real/go/zap-dev-sugar.txt", []string{
		`zap/go error handled "load: db timeout" @example.com/app/internal/svc.(*Store).Load@svc.go:20(5) <= "load: db timeout" @example.com/app/internal/svc.fetch@svc.go:24(6) [2026-09-23T04:30:26.319Z]`,
	}},
	{"real/go/zap-dev-then-panic.txt", []string{
		`zap/go error handled "request failed" @example.com/app/internal/svc.(*Store).Load@svc.go:20(5) <= "db timeout" @example.com/app/internal/svc.fetch@svc.go:24(6) [2026-09-23T04:30:26.346Z]`,
		`gopanic/go fatal unhandled panic: runtime error: invalid memory address or nil pointer dereference @example.com/app/internal/svc.(*Server).Handle@svc.go:13(2) [2026-09-23T04:30:26.346Z]`,
	}},
	{"real/go/zap-dev.txt", []string{
		`zap/go error handled "request failed" @example.com/app/internal/svc.(*Store).Load@svc.go:20(5) <= "db timeout" @example.com/app/internal/svc.fetch@svc.go:24(6) [2026-09-23T04:30:26.309Z]`,
	}},
	{"real/go/zap-prod-console.txt", []string{
		`zap/go error handled "request failed" @example.com/app/internal/svc.(*Store).Load@svc.go:20(5) <= "db timeout" @example.com/app/internal/svc.fetch@svc.go:24(6) [2026-09-23T04:30:26.326Z]`,
	}},
	{"real/go/zap-prod-json-iso.txt", []string{
		`zap/go error handled "request failed" @example.com/app/internal/svc.(*Store).Load@svc.go:20(5) <= "db timeout" @example.com/app/internal/svc.fetch@svc.go:24(6) [2026-09-23T04:30:26.339Z]`,
	}},
	{"real/go/zap-prod-json.txt", []string{
		`zap/go error handled "request failed" @example.com/app/internal/svc.(*Store).Load@svc.go:20(5) <= "db timeout" @example.com/app/internal/svc.fetch@svc.go:24(6) [2026-09-23T04:30:26.333Z]`,
		`message/go warning handled "slow query" @-(0) [2026-09-23T04:30:26.333Z]`,
	}},
	{"real/node/assert.txt", []string{
		`js/node fatal unhandled AssertionError: Expected values to be strictly equal: @@scenarios.mjs:45(4)`,
	}},
	{"real/node/cjs-handled.txt", []string{
		`js/node error handled Error: outer failure @outer@cjs.js:17(3) <= Error: middle failure @middle@cjs.js:10(4) <= Error: root failure @root@cjs.js:4(10)`,
	}},
	{"real/node/cjs-missing.txt", []string{
		`js/node fatal unhandled Error: Cannot find module './no-such-module' @Module._resolveFilename@loader:1569(10)`,
	}},
	{"real/node/cjs-uncaught.txt", []string{
		`js/node fatal unhandled Error: outer failure @outer@cjs.js:17(3) <= Error: middle failure @middle@cjs.js:10(4) <= Error: root failure @root@cjs.js:4(10)`,
	}},
	{"real/node/console-prefix.txt", []string{
		`js/node error handled SyntaxError: Expected property name or '}' in JSON at position 1 (line 1 column 2) @JSON.parse@<anonymous>:0(5)`,
	}},
	{"real/node/enoent-handled.txt", []string{
		`js/node error handled Error: ENOENT: no such file or directory, open 'does-not-exist.json' @Object.readFileSync@node:fs:539(6)`,
	}},
	{"real/node/enoent-uncaught.txt", []string{
		`js/node fatal unhandled Error: ENOENT: no such file or directory, open 'does-not-exist.json' @Object.readFileSync@node:fs:539(6)`,
	}},
	{"real/node/err-code.txt", []string{
		`js/node fatal unhandled TypeError: The "path" argument must be of type string or an instance of Buffer or URL. Received undefined @Object.openSync@node:fs:698(6)`,
	}},
	{"real/node/esm-missing.txt", []string{
		`js/node fatal unhandled Error: Cannot find package 'no-such-pkg-xyz' imported from /repo/app/src/missing.mjs @Object.getPackageJSONURL@package_json_reader:301(10)`,
	}},
	{"real/node/eval.txt", []string{
		`js/node fatal unhandled Error: from eval @f@[eval]:1(9)`,
	}},
	{"real/node/handled-cause.txt", []string{
		`js/node error handled Error: outer failure @outer@scenarios.mjs:19(3) <= Error: middle failure @middle@scenarios.mjs:12(3) <= Error: root failure @root@scenarios.mjs:6(7)`,
	}},
	{"real/node/multiline.txt", []string{
		`js/node fatal unhandled Error: first line of message @@scenarios.mjs:95(4)`,
	}},
	{"real/node/promise-all.txt", []string{
		`js/node fatal unhandled TypeError: user payload is not an object @fetchUser@scenarios.mjs:29(3)`,
	}},
	{"real/node/props-cause.txt", []string{
		`js/node fatal unhandled Error: upstream request failed @@scenarios.mjs:88(4) <= Error: socket hang up @@scenarios.mjs:87(4)`,
	}},
	{"real/node/reject-object.txt", []string{
		`js/node fatal unhandled UnhandledPromiseRejection: This error originated either by throwing inside of an async function without a catch block, or by rejecting a promise which was not handled with .catch(). The promise rejected with the reason "#<Object>". @throwUnhandledRejectionsMode@promises:322(3)`,
	}},
	{"real/node/reject-string.txt", []string{
		`js/node fatal unhandled UnhandledPromiseRejection: This error originated either by throwing inside of an async function without a catch block, or by rejecting a promise which was not handled with .catch(). The promise rejected with the reason "boom". @throwUnhandledRejectionsMode@promises:322(3)`,
	}},
	{"real/node/rejection.txt", []string{
		`js/node fatal unhandled TypeError: user payload is not an object @fetchUser@scenarios.mjs:29(1)`,
	}},
	{"real/node/throw-string.txt", []string{
		`js/node fatal unhandled "plain string failure" @-(0)`,
	}},
	{"real/node/typeerror.txt", []string{
		`js/node fatal unhandled TypeError: Cannot read properties of undefined (reading 'length') @Server.handle@scenarios.mjs:34(5)`,
	}},
	{"real/node/uncaught.txt", []string{
		`js/node fatal unhandled Error: outer failure @outer@scenarios.mjs:19(3) <= Error: middle failure @middle@scenarios.mjs:12(3) <= Error: root failure @root@scenarios.mjs:6(7)`,
	}},
	{"real/node/warning.txt", []string{
		`js/node warning handled DeprecationWarning: the widget API is deprecated @@[eval]:1(8)`,
	}},
	{"real/python/asctime.txt", []string{
		`python/python error handled ValueError: invalid literal for int() with base 10: 'x2' @parse@scenarios.py:8(2) [2026-09-23T04:35:39.909Z]`,
	}},
	{"real/python/bare.txt", []string{
		`python/python fatal unhandled PaymentDeclined @charge@scenarios.py:37(2)`,
	}},
	{"real/python/chain3.txt", []string{
		`python/python fatal unhandled SystemError: service unavailable @chain3@scenarios.py:29(2) <= RuntimeError: resolve failed @resolve@scenarios.py:22(2) <= LookupError: missing key 7 @lookup@scenarios.py:15(2) <= KeyError: 7 @lookup@scenarios.py:13(1)`,
	}},
	{"real/python/dash-m.txt", []string{
		`python/python fatal unhandled RuntimeError: cannot load settings.toml @load@worker.py:2(5)`,
	}},
	{"real/python/group.txt", []string{
		`python/python fatal unhandled ExceptionGroup: batch failed (2 sub-exceptions) @<module>@scenarios.py:90(1)`,
	}},
	{"real/python/module-logging.txt", []string{
		`python/python error handled ValueError: invalid literal for int() with base 10: 'x1' @parse@scenarios.py:8(2)`,
	}},
	{"real/python/notes.txt", []string{
		`python/python fatal unhandled ValueError: bad config @<module>@scenarios.py:88(1)`,
	}},
	{"real/python/print-exc.txt", []string{
		`python/python error handled ValueError: invalid literal for int() with base 10: 'x3' @parse@scenarios.py:8(2)`,
	}},
	{"real/python/syntax.txt", []string{
		`python/python fatal unhandled SyntaxError: invalid syntax @@syntax_bad.py:1(1)`,
	}},
	{"real/python/thread.txt", []string{
		`python/python fatal unhandled ValueError: invalid literal for int() with base 10: 'not-a-number' @parse@scenarios.py:8(4)`,
	}},
	{"real/ruby/cause3.txt", []string{
		`ruby/ruby fatal unhandled RuntimeError: outer failure @Object#outer@scenarios.rb:26(2) <= ArgumentError: middle failure @Object#middle@scenarios.rb:20(3) <= KeyError: key not found: :a @Hash#fetch@scenarios.rb:14(5)`,
	}},
	{"real/ruby/deep.txt", []string{
		`ruby/ruby fatal unhandled RuntimeError: bottom reached @Object#deep@scenarios.rb:30(42)`,
	}},
	{"real/ruby/logger.txt", []string{
		`ruby/ruby error handled NoMethodError: undefined method 'baz' for nil @Foo#bar@scenarios.rb:5(3) [2026-09-23T04:35:40.285Z]`,
		`message/ruby error handled "plain error without backtrace" @-(0) [2026-09-23T04:35:40.285Z]`,
		`message/ruby warning handled "disk almost full" @-(0) [2026-09-23T04:35:40.285Z]`,
	}},
	{"real/ruby/nomethod.txt", []string{
		`ruby/ruby fatal unhandled NoMethodError: undefined method 'baz' for nil @Foo#bar@scenarios.rb:5(3)`,
	}},
	{"real/ruby/thread.txt", []string{
		`ruby/ruby fatal unhandled KeyError: key not found: :a @Hash#fetch@scenarios.rb:14(3)`,
	}},
	{"real/ruby/toplevel.txt", []string{
		`ruby/ruby fatal unhandled RuntimeError: top level failure @<main>@scenarios.rb:58(1)`,
	}},
}

func TestGolden(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(tc.fixture, func(t *testing.T) {
			assertSummaries(t, detectIn(readFixture(t, tc.fixture), testZone), tc.want)
		})
	}
}
