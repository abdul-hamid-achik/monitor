'use strict';
// Monitor's Node SDK (also Bun, and Deno through npm compatibility).
//
// Two ways in:
//   - auto: `monitor run --probes -- <cmd>` preloads auto.cjs through
//     NODE_OPTIONS/BUN_OPTIONS. Nothing to install, no code to change.
//   - explicit: `require('@thelacanians/monitor')` (or import) and call init(),
//     captureException(), captureMessage(), addBreadcrumb(), setTag().
//
// Either way it only observes: it never prints, never changes the app's
// output or exit code, and never reads anything but the error itself
// (type, message, the stack string exactly as the runtime formatted it).
// Each event is one JSON file (monitor.event.v1) written atomically into
// $MONITOR_EVENTS_DIR when `monitor run` set it, else into the global
// inbox that `monitor events drain` / `monitor issues` read. Parsing,
// scrubbing and grouping happen in monitor, not here.
//
// One instance per realm: every copy (the preloaded one and an installed
// package) shares the instance registered on globalThis, so hooks are
// never installed twice.

const KEY = Symbol.for('monitorcli.sdk.node');

module.exports = globalThis[KEY] || (globalThis[KEY] = create());

function create() {
  const VERSION = '0.1.0';
  const SCHEMA = 'monitor.event.v1';
  const MAX_BREADCRUMBS = 30;
  const MAX_CAUSES = 8;
  const MAX_TEXT = 8 * 1024;
  const MAX_STACK = 64 * 1024;
  // At most RATE_MAX events per RATE_WINDOW_MS; the rest are dropped, so an
  // error loop can never turn into a disk-filling loop.
  const RATE_MAX = 50;
  const RATE_WINDOW_MS = 10000;

  const fs = safeRequire('fs');
  const path = safeRequire('path');
  const os = safeRequire('os');
  const crypto = safeRequire('crypto');
  const proc = typeof process !== 'undefined' ? process : undefined;

  const state = {
    mode: '',
    service: undefined,
    release: undefined,
    environment: undefined,
    tags: Object.create(null),
    breadcrumbs: [],
    beforeSend: undefined,
    dir: undefined,
    console: true,
    hooked: false,
    sending: false,
    windowStart: 0,
    windowCount: 0,
    dropped: 0,
    seq: 0,
    madeDirs: new Set(),
  };

  function safeRequire(name) {
    try {
      return require(name);
    } catch (_) {
      return undefined;
    }
  }

  function env(name) {
    try {
      const v = proc && proc.env ? proc.env[name] : undefined;
      return typeof v === 'string' && v.trim() !== '' ? v.trim() : undefined;
    } catch (_) {
      return undefined;
    }
  }

  function runtime() {
    if (typeof globalThis.Bun !== 'undefined') return 'bun';
    if (typeof globalThis.Deno !== 'undefined') return 'deno';
    return 'node';
  }

  function clip(s, n) {
    s = String(s);
    return s.length > n ? s.slice(0, n) : s;
  }

  function isError(x) {
    if (x instanceof Error) return true;
    try {
      return Object.prototype.toString.call(x) === '[object Error]';
    } catch (_) {
      return false;
    }
  }

  function describe(x) {
    try {
      if (typeof x === 'string') return x;
      if (x === null || x === undefined || typeof x !== 'object') return String(x);
      if (isError(x)) return String(x.name || 'Error') + ': ' + String(x.message);
      return Array.isArray(x) ? '[array]' : '[object]';
    } catch (_) {
      return '[unprintable]';
    }
  }

  // serializeError reads only what the runtime already formatted. Reading
  // err.stack (the string) is safe: V8 formats it once and caches it, so the
  // app later prints exactly the same text. Never install
  // Error.prepareStackTrace here: that changes what the app prints.
  function serializeError(err, depth, seen) {
    if (depth >= MAX_CAUSES || err === undefined || err === null) return undefined;
    if (!isError(err)) {
      return { value: clip(describe(err), MAX_TEXT) };
    }
    if (seen.has(err)) return undefined;
    seen.add(err);
    const out = {};
    try {
      out.type = clip(err.name || (err.constructor && err.constructor.name) || 'Error', 256);
    } catch (_) {
      out.type = 'Error';
    }
    try {
      out.value = clip(err.message === undefined ? '' : err.message, MAX_TEXT);
    } catch (_) {
      out.value = '';
    }
    try {
      const stack = err.stack;
      if (typeof stack === 'string') out.stack = clip(stack, MAX_STACK);
    } catch (_) {}
    let cause;
    try {
      cause = err.cause;
    } catch (_) {}
    const c = serializeError(cause, depth + 1, seen);
    if (c) out.cause = c;
    return out;
  }

  function inboxDir() {
    const xdg = env('XDG_STATE_HOME');
    let base = xdg && path.isAbsolute(xdg) ? xdg : undefined;
    if (!base) {
      const home = os && os.homedir ? os.homedir() : undefined;
      if (!home) return undefined;
      base = path.join(home, '.local', 'state');
    }
    return path.join(base, 'monitor', 'events', 'inbox');
  }

  function writeInto(dir, name, data) {
    if (!state.madeDirs.has(dir)) {
      fs.mkdirSync(dir, { recursive: true, mode: 0o700 });
      state.madeDirs.add(dir);
    }
    const tmp = path.join(dir, '.' + name + '.tmp');
    fs.writeFileSync(tmp, data, { mode: 0o600 });
    try {
      fs.renameSync(tmp, path.join(dir, name + '.json'));
    } catch (e) {
      try {
        fs.unlinkSync(tmp);
      } catch (_) {}
      throw e;
    }
  }

  // deliver writes synchronously on purpose: an uncaught exception is the
  // process's last moment, and there is no event loop left for async I/O.
  function deliver(ev) {
    if (!fs || !path) return false;
    const now = Date.now();
    if (now - state.windowStart > RATE_WINDOW_MS) {
      state.windowStart = now;
      state.windowCount = 0;
    }
    if (state.windowCount >= RATE_MAX) {
      state.dropped++;
      return false;
    }
    state.windowCount++;
    const nanos = (BigInt(now) * 1000000n + BigInt(state.seq++ % 1000000)).toString().padStart(19, '0');
    const name = nanos + '-' + (proc ? proc.pid : 0) + '-' + ev.event_id;
    const data = JSON.stringify(ev);
    const dirs = [state.dir, env('MONITOR_EVENTS_DIR'), inboxDir()].filter(Boolean);
    for (const dir of dirs) {
      try {
        writeInto(dir, name, data);
        return true;
      } catch (_) {
        state.madeDirs.delete(dir);
      }
    }
    return false;
  }

  function newID() {
    try {
      return crypto.randomBytes(16).toString('hex');
    } catch (_) {
      return Date.now().toString(16) + Math.random().toString(16).slice(2);
    }
  }

  function build(kind, fields) {
    const ev = {
      $schema: SCHEMA,
      event_id: newID(),
      timestamp: new Date().toISOString(),
      kind,
      runtime: runtime(),
      sdk: { name: 'monitor.node', version: VERSION, mode: state.mode || 'explicit' },
    };
    Object.assign(ev, fields);
    const tags = Object.assign(Object.create(null), state.tags, fields.tags || {});
    delete ev.tags;
    if (Object.keys(tags).length) ev.tags = Object.assign({}, tags);
    if (state.breadcrumbs.length) ev.breadcrumbs = state.breadcrumbs.slice();
    if (state.service) ev.service = state.service;
    if (state.release) ev.release = state.release;
    if (state.environment) ev.environment = state.environment;
    try {
      ev.cwd = proc.cwd();
    } catch (_) {}
    if (proc) ev.pid = proc.pid;
    const launch = env('MONITOR_LAUNCH_ID');
    if (launch) ev.launch_id = launch;
    return ev;
  }

  // send never throws and never recurses: a failure inside capture (or a
  // beforeSend that logs an error) is dropped, not re-captured.
  function send(ev) {
    if (state.sending) return undefined;
    state.sending = true;
    try {
      if (state.beforeSend) {
        ev = state.beforeSend(ev);
        if (!ev) return undefined;
      }
      return deliver(ev) ? ev.event_id : undefined;
    } catch (_) {
      return undefined;
    } finally {
      state.sending = false;
    }
  }

  function captureError(kind, err, opts) {
    opts = opts || {};
    const error = serializeError(err, 0, new Set());
    if (!error) return undefined;
    const fields = { error };
    if (opts.handled !== undefined) fields.handled = !!opts.handled;
    if (opts.level) fields.level = String(opts.level);
    if (opts.tags) fields.tags = opts.tags;
    return send(build(kind, fields));
  }

  function pushBreadcrumb(b) {
    const message = clip(b.message === undefined ? '' : b.message, 1024).trim();
    if (!message) return;
    const crumb = { timestamp: new Date().toISOString(), message };
    if (b.category) crumb.category = clip(b.category, 64);
    if (b.level) crumb.level = clip(b.level, 16);
    state.breadcrumbs.push(crumb);
    if (state.breadcrumbs.length > MAX_BREADCRUMBS) state.breadcrumbs.shift();
  }

  const CONSOLE_LEVELS = { debug: 'debug', log: 'info', info: 'info', warn: 'warning', error: 'error' };
  const PATCHED = Symbol.for('monitorcli.sdk.node.patched');

  function onConsole(method, args) {
    if (state.sending) return;
    if (method === 'error') {
      for (let i = 0; i < args.length; i++) {
        if (isError(args[i])) captureError('logged', args[i], { handled: true });
      }
    }
    const parts = [];
    for (let i = 0; i < args.length && i < 8; i++) parts.push(describe(args[i]));
    pushBreadcrumb({ category: 'console', level: CONSOLE_LEVELS[method], message: parts.join(' ') });
  }

  function patchConsole() {
    if (typeof console === 'undefined') return;
    for (const method of Object.keys(CONSOLE_LEVELS)) {
      const orig = console[method];
      if (typeof orig !== 'function' || orig[PATCHED]) continue;
      // The app's own call runs first, untouched: Bun prints a logged error
      // differently once its stack has been read, so the SDK only looks at
      // the arguments after they are printed.
      const wrapped = function () {
        try {
          return orig.apply(this, arguments);
        } finally {
          if (state.console) {
            try {
              onConsole(method, arguments);
            } catch (_) {}
          }
        }
      };
      try {
        Object.defineProperty(wrapped, 'name', { value: orig.name });
        wrapped[PATCHED] = true;
      } catch (_) {}
      console[method] = wrapped;
    }
  }

  function installHooks() {
    if (state.hooked || !proc || typeof proc.on !== 'function') return;
    state.hooked = true;
    // uncaughtExceptionMonitor observes without changing what happens next:
    // the process still prints the error and exits with the same code. A
    // rejection that crashes the process arrives here too, with its origin.
    try {
      proc.on('uncaughtExceptionMonitor', (err, origin) => {
        captureError(origin === 'unhandledRejection' ? 'rejection' : 'uncaught', err, { handled: false, level: 'fatal' });
      });
    } catch (_) {}
    // A listener on 'unhandledRejection' would stop the crash, so the
    // rejection is observed by wrapping emit instead. Only a rejection the
    // app itself handles (it has listeners) is captured here; one nobody
    // handles crashes and is captured by the monitor above.
    if (typeof proc.emit === 'function') {
      const emit = proc.emit;
      proc.emit = function (name, reason) {
        if (name === 'unhandledRejection') {
          try {
            if (proc.listenerCount('unhandledRejection') > 0) {
              captureError('rejection', reason, { handled: false, level: 'error' });
            }
          } catch (_) {}
        }
        return emit.apply(this, arguments);
      };
    }
    patchConsole();
  }

  const api = {
    version: VERSION,

    // init sets the context every later event carries and installs the
    // hooks (uncaught errors, rejections, console.error(err)). Safe to call
    // more than once; the latest values win.
    init(options) {
      options = options || {};
      state.mode = 'explicit';
      if (options.service !== undefined) state.service = options.service ? String(options.service) : undefined;
      if (options.release !== undefined) state.release = options.release ? String(options.release) : undefined;
      if (options.environment !== undefined) state.environment = options.environment ? String(options.environment) : undefined;
      if (options.dir !== undefined) state.dir = options.dir ? String(options.dir) : undefined;
      if (options.tags) api.setTags(options.tags);
      if (typeof options.beforeSend === 'function') state.beforeSend = options.beforeSend;
      if (options.console !== undefined) state.console = !!options.console;
      installHooks();
      return api;
    },

    captureException(err, options) {
      options = options || {};
      return captureError('captured', err, { handled: options.handled !== false, level: options.level, tags: options.tags });
    },

    captureMessage(message, level, options) {
      if (level && typeof level === 'object') {
        options = level;
        level = undefined;
      }
      options = options || {};
      const text = clip(message === undefined ? '' : message, MAX_TEXT);
      if (!text.trim()) return undefined;
      const fields = { message: text, level: String(level || options.level || 'info'), handled: true };
      if (options.tags) fields.tags = options.tags;
      return send(build('message', fields));
    },

    addBreadcrumb(crumb) {
      if (typeof crumb === 'string') crumb = { message: crumb };
      if (crumb && typeof crumb === 'object') pushBreadcrumb(crumb);
    },

    setTag(key, value) {
      if (key === undefined || key === null) return;
      if (value === undefined || value === null) delete state.tags[String(key)];
      else state.tags[String(key)] = clip(value, 256);
    },

    setTags(tags) {
      if (!tags || typeof tags !== 'object') return;
      for (const k of Object.keys(tags)) api.setTag(k, tags[k]);
    },

    // Events are written synchronously, so there is never anything to flush;
    // kept so code written for other SDKs keeps working.
    flush() {
      return Promise.resolve(true);
    },

    // _auto is what auto.cjs calls under `monitor run --probes`.
    _auto() {
      if (!state.mode) state.mode = 'auto';
      installHooks();
      return api;
    },

    _stats() {
      return { dropped: state.dropped, hooked: state.hooked, mode: state.mode };
    },
  };
  return api;
}
