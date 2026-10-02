# @monitorcli/sdk

Monitor's own error SDK for Node, Bun and Deno. It has zero dependencies, runs
locally, and never talks to a server. Every error becomes one
`monitor.event.v1` JSON file that the [monitor](https://monitorcli.dev) CLI
groups into issues and points at a line, the same way it handles crashes it
reads from a terminal.

## Without installing anything

```sh
monitor run --probes -- node server.js
```

`--probes` loads this SDK through `NODE_OPTIONS=--require` (Node) or
`BUN_OPTIONS=--preload` (Bun). It records:

- uncaught exceptions and the rejections that crash the process;
- rejections the app handles with its own `unhandledRejection` listener (Node);
- errors passed to `console.error(err)`, with their `cause` chain;
- the last 30 console calls, kept as breadcrumbs.

The app prints the same bytes and exits with the same code. The SDK only
observes: it never prints, never changes `Error.prepareStackTrace`, and never
reads arguments, environment variables, headers or bodies.

## In code, for more context

```js
const monitor = require('@monitorcli/sdk'); // or: import * as monitor from '@monitorcli/sdk'

monitor.init({ service: 'api', release: process.env.GIT_SHA, environment: 'dev' });
monitor.setTag('region', 'mx');
monitor.addBreadcrumb({ category: 'db', message: 'select users' });

try {
  await charge(order);
} catch (err) {
  monitor.captureException(err, { tags: { order: order.id } });
}

monitor.captureMessage('cache rebuilt', 'info');
```

`init` also installs the automatic hooks, so code that uses the SDK does not
need `--probes`. Pass `beforeSend(event)` to edit or drop an event (return
`null` to drop it). Under `monitor run`, events go to that launch. Anywhere
else, they go to `$XDG_STATE_HOME/monitor/events/inbox`, which `monitor
issues` and `monitor events drain` read.

Monitor redacts secrets, emails and card numbers from every event before it
stores one. At most 50 events are written per 10 seconds; the rest are dropped.

The event format is documented in
[docs/contracts/event-v1.md](https://monitorcli.dev/contracts/event-v1).
