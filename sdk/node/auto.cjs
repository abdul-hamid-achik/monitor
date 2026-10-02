'use strict';
// Loaded by `monitor run --probes` through NODE_OPTIONS=--require (Node) or
// BUN_OPTIONS=--preload (Bun): installs the SDK's hooks in auto mode. It
// never prints and never throws into the app.
try {
  require('./index.cjs')._auto();
} catch (_) {}
