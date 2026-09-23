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

const hotTimer = setInterval(() => {
  processBatch(40);
}, 40);

const errTimer = setInterval(tickErrors, 4000);

setTimeout(() => {
  clearInterval(hotTimer);
  clearInterval(errTimer);
  detonate();
}, 40000);

// hard safety stop in case detonate() doesn't actually end the process
setTimeout(() => {
  console.error(`[${RUNTIME}] safety exit`);
  if (typeof process !== 'undefined') process.exit(0);
  else if (typeof Deno !== 'undefined') Deno.exit(0);
}, 75000);
