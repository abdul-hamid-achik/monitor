'use strict';
// polyglot-compatible CPU + error workload (node/bun/deno)

const START = Date.now();
const RUNTIME = (() => {
  if (typeof Bun !== 'undefined') return 'bun';
  if (typeof Deno !== 'undefined') return 'deno';
  return 'node';
})();

function heavyStringify(items) {
  let out = [];
  for (const item of items) {
    const doubled = item * 2;
    let s = '';
    for (let i = 0; i < 2500; i++) {
      s += JSON.stringify({ i, item, doubled, pad: 'x'.repeat(64) }); // HOT LINE (line 16)
    }
    out.push(s.length);
  }
  return out;
}

function processBatch(n) {
  const items = Array.from({ length: n }, (_, i) => i);
  return heavyStringify(items);
}

function flakyParse(raw) {
  if (raw.includes('bad')) {
    throw new Error(`flakyParse: malformed payload near token "${raw.slice(0, 8)}"`);
  }
  return JSON.parse(raw);
}

function tickErrors() {
  const payloads = ['{"ok":true}', 'bad-payload-123', '{"ok":true}'];
  for (const p of payloads) {
    try {
      flakyParse(p);
    } catch (err) {
      console.error(`[${RUNTIME}] caught in tickErrors: ${err.message}`);
      console.error(err.stack);
    }
  }
}

function detonate() {
  throw new Error(`${RUNTIME} workload: intentional uncaught failure at t=${Date.now() - START}ms`);
}

const pid = typeof process !== 'undefined' ? process.pid : (typeof Deno !== 'undefined' ? Deno.pid : 'unknown');
console.error(`[${RUNTIME}] pid=${pid} workload starting`);

// WORKLOAD_SECONDS shortens the run for CI/spec use (monitor run --'s
// mvp_errors_* specs): default 40s is unchanged when unset. Deno.env.get
// requires --allow-env; wrapped in try/catch so an unpermissioned `deno
// run` (no -A/--allow-env) still falls back to the default instead of
// crashing before it can print anything.
function envSeconds() {
  let raw;
  if (typeof process !== 'undefined' && process.env) {
    raw = process.env.WORKLOAD_SECONDS;
  } else if (typeof Deno !== 'undefined' && Deno.env) {
    try {
      raw = Deno.env.get('WORKLOAD_SECONDS');
    } catch {
      raw = undefined;
    }
  }
  const n = Number(raw);
  return Number.isFinite(n) && n > 0 ? n : 40;
}
const SECONDS = envSeconds();
// Scales with SECONDS so a shortened run still produces several handled
// errors before the fatal one, not just the fatal one alone: at the
// default 40s this is exactly the original fixed 4000ms interval.
const ERR_INTERVAL_MS = Math.max(50, Math.floor((SECONDS * 1000) / 10));

const hotTimer = setInterval(() => {
  processBatch(40);
}, 40);

const errTimer = setInterval(tickErrors, ERR_INTERVAL_MS);

setTimeout(() => {
  clearInterval(hotTimer);
  clearInterval(errTimer);
  detonate();
}, SECONDS * 1000);

// hard safety stop in case detonate() doesn't actually end the process
setTimeout(() => {
  console.error(`[${RUNTIME}] safety exit`);
  if (typeof process !== 'undefined') process.exit(0);
  else if (typeof Deno !== 'undefined') Deno.exit(0);
}, SECONDS * 1000 + 35000);
