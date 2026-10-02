# monitorcli

Monitor's own error SDK for Python. It has zero dependencies, runs locally,
and never talks to a server. Every exception becomes one `monitor.event.v1`
JSON file that the [monitor](https://monitorcli.dev) CLI groups into issues
and points at a line, the same way it handles tracebacks it reads from a
terminal.

## Without installing anything

```sh
monitor run --probes -- python app.py
```

`--probes` puts a directory holding only a bootstrap `sitecustomize.py` first
on `PYTHONPATH`. That file runs any `sitecustomize` it shadowed, then loads
this SDK. It records:

- uncaught exceptions, with their full `__cause__`/`__context__` chain;
- exceptions that end a thread (`threading.excepthook`);
- `logger.exception(...)` and any `logger.error(..., exc_info=True)`;
- the last 30 log records at INFO and above, kept as breadcrumbs.

The app prints the same bytes and exits with the same status. The SDK only
observes: it never adds a logging handler, never prints, and never reads
locals, arguments, environment variables or request data.

## In code, for more context

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

`init` also installs the automatic hooks. Pass `before_send=fn` to edit or
drop an event (return `None` to drop it). Under `monitor run`, events go to
that launch. Anywhere else, they go to
`$XDG_STATE_HOME/monitor/events/inbox`, which `monitor issues` and `monitor
events drain` read.

Monitor redacts secrets, emails and card numbers from every event before it
stores one. At most 50 events are written per 10 seconds; the rest are
dropped. Python 3.8 or newer.
