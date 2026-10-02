# SDKs

Monitor reads crashes from what a process prints, with no SDK. That covers a
lot, but not everything: an error the app catches and writes to a log file,
a promise rejection its own listener swallows, or a worker thread that dies
quietly never reaches stderr.

Monitor's own SDKs cover that gap. They are small, have zero dependencies,
and never talk to a server: each error becomes one
[`monitor.event.v1`](/contracts/event-v1) file that monitor groups into the
same issues, with the same culprit lines, as everything else. There are two
ways in:

| | Auto (`--probes`) | Explicit (in code) |
|---|---|---|
| Install | nothing | the package |
| Code changes | none | `init()`, then capture calls |
| Runtimes | Node, Bun, Python | Node, Bun, Deno, Python, Go |
| Records | crashes, rejections, thread deaths, `console.error(err)`, `logger.exception(...)`, console/log breadcrumbs | all of that, plus your own errors, messages, tags and breadcrumbs |

## Auto: `monitor run --probes`

```sh
monitor run --probes -- node server.js
monitor run --probes -- npm run dev
monitor run --probes -- python manage.py runserver
```

`--probes` loads the SDK into the process at launch, by environment only:

- **Node:** `NODE_OPTIONS=--require <sdk>/auto.cjs`, appended to whatever
  `NODE_OPTIONS` already holds. Child processes (npm, workers) inherit it.
- **Bun:** `BUN_OPTIONS=--preload <sdk>/auto.cjs`, also appended.
- **Python:** `PYTHONPATH` gets one directory in front, holding only a
  bootstrap `sitecustomize.py`. It runs the `sitecustomize` it shadowed, if
  there is one, then loads the SDK, and removes itself from `sys.path`.

The SDK files ship inside the `monitor` binary; there is nothing to install.

What the app sees does not change. Its stdout, stderr and exit code are
byte-identical with and without `--probes` (the specs check this on every
change). The SDK never prints, never adds a logging handler (that would switch
off Python's last-resort printer), never installs `Error.prepareStackTrace`
(that would change what Node prints), and never listens for
`unhandledRejection` in a way that stops a crash. It only reads the error
itself.

The same crash seen by the SDK and printed on stderr is recorded **once**,
with the SDK's richer data: the exact handled flag, the full stack, and the
breadcrumbs before it.

```text
$ monitor run --probes -- python3 app.py
...
monitor > NEW 0821 error ZeroDivisionError: division by zero app.py:31 <lambda>()
monitor > NEW E065 fatal RuntimeError: settlement failed app.py:23 parse_invoice()
monitor > NEW 1F23 error json.decoder.JSONDecodeError: Expecting property name enclosed in double quotes: line 1 column 2 (char 1) app.py:23 parse_invoice()

$ monitor issue 1f23
1F23  json.decoder.JSONDecodeError: Expecting property name enclosed in do...  new · error · handled
billing / python3 · first seen just now · last seen just now · 1 event(s)

CULPRIT  app.py:23 in parse_invoice()
>   23 |     return json.loads(text)
STACK    in-app 2
EVENT    logged · monitor.python/0.1.0 (auto)
TRAIL    04:06:15 [billing] loaded 3 plans
```

`1F23` was logged with `logger.exception` into a log file and never reached
the terminal. Without `--probes`, monitor could not have seen it. The app is
[`examples/polyglot/sdk/app.py`](https://github.com/abdul-hamid-achik/monitor/blob/main/examples/polyglot/sdk/app.py).

Auto mode does not cover:

- **Deno:** Deno has no environment variable that preloads a module. Import
  the SDK explicitly, or run Deno's own `OTEL_DENO=true`.
- **Ruby:** planned.
- **Go:** Go cannot be instrumented from outside. Use the Go SDK. Without
  it, `monitor run` still records panics from the trace Go prints.
- **Bun:** a rejection that Bun's own listener handles is not seen.
- **Python:** `python -S` (no `site`) and `python -I` (isolated mode, which
  ignores `PYTHONPATH`) do not load it.

## Explicit: the SDK in your code

Use the SDK directly to add context, to capture errors you handle yourself,
or for apps that do not run under `monitor run`. Under `monitor run`, events
go to that launch. Anywhere else, they go to the inbox
(`~/.local/state/monitor/events/inbox`), which `monitor issues` and
`monitor issue` drain before they read.

The packages are not on npm or PyPI yet. Install them from the copies that
ship in the binary:

```sh
npm install "$(monitor sdk path node)"      # @thelacanians/monitor
pip install "$(monitor sdk path python)"    # monitorcli
go get github.com/abdul-hamid-achik/monitor/sdk/go
```

### Node, Bun, Deno

```js
const monitor = require('@thelacanians/monitor'); // or: import * as monitor from '@thelacanians/monitor'

monitor.init({ service: 'api', release: process.env.GIT_SHA, environment: 'dev' });
monitor.setTag('region', 'mx');
monitor.addBreadcrumb({ category: 'db', message: 'select users' });

try {
  await charge(order);
} catch (err) {
  monitor.captureException(err, { tags: { order: String(order.id) } });
}

monitor.captureMessage('cache rebuilt', 'info');
```

`init` also installs the automatic hooks, so an app that uses the SDK does not
need `--probes`. The preloaded copy and an installed copy share one instance,
so hooks are never installed twice.

### Python

```python
import monitorcli

monitorcli.init(service="api", release=GIT_SHA, environment="dev")
monitorcli.set_tag("region", "mx")
monitorcli.add_breadcrumb("select users", category="db")

try:
    charge(order)
except PaymentError:
    monitorcli.capture_exception(tags={"order": order.id})

monitorcli.capture_message("cache rebuilt")
```

Python 3.9 or newer.

### Go

```go
import monitor "github.com/abdul-hamid-achik/monitor/sdk/go"

func main() {
	monitor.Init(monitor.Options{Service: "api", Release: version})
	defer monitor.Recover() // records a panic, then panics again

	if err := charge(order); err != nil {
		monitor.CaptureError(err, monitor.WithTags(map[string]string{"order": order.ID}))
	}
}

func worker() {
	defer monitor.RecoverAndContinue() // records a panic and keeps going
	// ...
}
```

`CaptureError` records the error's type, message and `Unwrap` chain, and the
stack where it was captured. A panic recorded by `Recover` groups with the
trace Go prints for the same panic.

### Editing or dropping events

Every SDK takes a hook that sees each event before it is written: `beforeSend`
in Node, `before_send` in Python, `Options.BeforeSend` in Go. Return the event,
possibly edited, or `null`/`None`/`nil` to drop it.

## Privacy and limits

- Events stay on your machine, as files in `0700` directories, until monitor
  records and deletes them.
- The SDKs never record local variables, arguments, `argv`, environment
  variables, headers, request bodies or cookies.
- Monitor scrubs every stored text field: the error's message and function
  names, breadcrumb messages, and tag keys and values (secret shapes, emails,
  card numbers, secret environment values).
- At most 50 events per 10 seconds per process are written; the rest are
  dropped. An error loop can never fill the disk.
- An occurrence keeps the newest 30 breadcrumbs and at most 24 tags.

## Commands

| Command | Does |
|---|---|
| `monitor run --probes -- <cmd>` | loads the SDK into `cmd` at launch |
| `monitor events` | shows the inbox and how many events wait in it |
| `monitor events drain` | records the inbox into the issue store (`--store`, `--dir`, `--json`) |
| `monitor sdk` | shows where the bundled SDKs are |
| `monitor sdk path <node\|python>` | prints one SDK's directory, for `npm install` / `pip install` |

The event format is documented in
[`monitor.event.v1`](/contracts/event-v1).
