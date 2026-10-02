'use strict';
// Run with: node --test sdk/node/test/
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const SDK = path.join(__dirname, '..', 'index.cjs');
const AUTO = path.join(__dirname, '..', 'auto.cjs');

function tmpdir() {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'monitor-sdk-node-'));
}

function events(dir) {
  if (!fs.existsSync(dir)) return [];
  return fs
    .readdirSync(dir)
    .filter((f) => f.endsWith('.json') && !f.startsWith('.'))
    .sort()
    .map((f) => JSON.parse(fs.readFileSync(path.join(dir, f), 'utf8')));
}

// run executes a script in a fresh node process, optionally preloading the
// auto SDK, with its own events directory.
function run(script, { auto = false, env = {} } = {}) {
  const dir = tmpdir();
  const file = path.join(dir, 'app.cjs');
  fs.writeFileSync(file, script.replaceAll('__SDK__', JSON.stringify(SDK)));
  const evdir = path.join(dir, 'events');
  const childEnv = { ...process.env, MONITOR_EVENTS_DIR: evdir, ...env };
  delete childEnv.NODE_OPTIONS;
  if (auto) childEnv.NODE_OPTIONS = `--require ${JSON.stringify(AUTO)}`;
  const res = spawnSync(process.execPath, [file], { env: childEnv, encoding: 'utf8', cwd: dir });
  return { status: res.status, stdout: res.stdout, stderr: res.stderr, events: events(evdir), dir };
}

const APP = `
console.log('starting', { port: 3000 });
function parse(s) { return JSON.parse(s); }
try { parse('{bad'); } catch (e) { console.error('config failed:', e); }
process.on('unhandledRejection', (r) => console.warn('app saw', r.message));
Promise.reject(new TypeError('late reject'));
setTimeout(() => { throw new Error('worker crashed', { cause: new RangeError('queue overflow') }); }, 10);
`;

test('auto mode leaves output and exit code byte-identical', () => {
  const plain = run(APP);
  const probed = run(APP, { auto: true });
  assert.strictEqual(probed.status, plain.status);
  assert.strictEqual(probed.stdout, plain.stdout);
  assert.strictEqual(probed.stderr.replaceAll(probed.dir, '<dir>'), plain.stderr.replaceAll(plain.dir, '<dir>'));
  assert.strictEqual(plain.events.length, 0);

  const kinds = probed.events.map((e) => e.kind);
  assert.deepStrictEqual(kinds, ['logged', 'rejection', 'uncaught']);
  const [logged, rejection, uncaught] = probed.events;
  assert.strictEqual(logged.handled, true);
  assert.strictEqual(logged.error.type, 'SyntaxError');
  assert.match(logged.error.stack, /at parse \(/);
  assert.strictEqual(rejection.handled, false);
  assert.strictEqual(rejection.level, 'error');
  assert.strictEqual(uncaught.level, 'fatal');
  assert.strictEqual(uncaught.error.cause.type, 'RangeError');
  assert.strictEqual(uncaught.sdk.mode, 'auto');
  // Breadcrumbs: the console calls before the crash, not the event's own.
  assert.deepStrictEqual(
    uncaught.breadcrumbs.map((b) => b.message),
    ['starting [object]', 'config failed: SyntaxError: ' + logged.error.value, 'app saw late reject'],
  );
  for (const ev of probed.events) {
    assert.strictEqual(ev.$schema, 'monitor.event.v1');
    assert.match(ev.event_id, /^[0-9a-f]{32}$/);
    assert.strictEqual(ev.runtime, 'node');
  }
});

test('explicit API: context, tags, messages, beforeSend', () => {
  const res = run(`
    const monitor = require(__SDK__);
    monitor.init({ service: 'api', release: '1.2.3', environment: 'dev', tags: { region: 'mx' },
      beforeSend: (ev) => (ev.message === 'drop me' ? null : ev) });
    monitor.addBreadcrumb({ category: 'db', message: 'select users' });
    const id = monitor.captureException(new Error('charge failed'), { tags: { order: '42' } });
    process.stdout.write(String(typeof id) + '\\n');
    monitor.captureException('a plain string');
    monitor.captureMessage('cache rebuilt');
    monitor.captureMessage('drop me', 'warning');
    monitor.setTag('region', null);
    monitor.captureMessage('after untag', { level: 'warning' });
  `);
  assert.strictEqual(res.status, 0, res.stderr);
  assert.strictEqual(res.stdout, 'string\n');
  const [err, plain, msg, untagged] = res.events;
  assert.strictEqual(res.events.length, 4);
  assert.strictEqual(err.kind, 'captured');
  assert.strictEqual(err.handled, true);
  assert.strictEqual(err.service, 'api');
  assert.strictEqual(err.release, '1.2.3');
  assert.strictEqual(err.environment, 'dev');
  assert.deepStrictEqual(err.tags, { region: 'mx', order: '42' });
  assert.deepStrictEqual(err.breadcrumbs.map((b) => b.message), ['select users']);
  assert.strictEqual(err.sdk.mode, 'explicit');
  assert.strictEqual(plain.error.value, 'a plain string');
  assert.strictEqual(msg.kind, 'message');
  assert.strictEqual(msg.level, 'info');
  assert.strictEqual(untagged.level, 'warning');
  assert.strictEqual(untagged.tags, undefined);
});

test('one instance per realm: the preloaded copy and a required copy share hooks', () => {
  const res = run(
    `
    const a = require(__SDK__);
    a.init({ service: 'api' });
    console.error(new Error('logged once'));
  `,
    { auto: true },
  );
  assert.strictEqual(res.status, 0, res.stderr);
  assert.strictEqual(res.events.length, 1);
  assert.strictEqual(res.events[0].service, 'api');
  assert.strictEqual(res.events[0].sdk.mode, 'explicit');
});

test('an error loop is rate limited', () => {
  const res = run(`
    const monitor = require(__SDK__);
    for (let i = 0; i < 80; i++) monitor.captureException(new Error('loop ' + i));
  `);
  assert.strictEqual(res.status, 0, res.stderr);
  assert.strictEqual(res.events.length, 50);
});

test('falls back to the inbox when the launch directory cannot be written', () => {
  const state = tmpdir();
  const blocker = path.join(state, 'file');
  fs.writeFileSync(blocker, '');
  const res = run(
    `require(__SDK__).captureMessage('after the launch ended');`,
    { env: { MONITOR_EVENTS_DIR: path.join(blocker, 'launch'), XDG_STATE_HOME: state } },
  );
  assert.strictEqual(res.status, 0, res.stderr);
  const inbox = path.join(state, 'monitor', 'events', 'inbox');
  assert.strictEqual(events(inbox).length, 1);
  assert.strictEqual(fs.statSync(inbox).mode & 0o777, 0o700);
});

test('the ESM entry exposes the same instance', async () => {
  const esm = await import(path.join(__dirname, '..', 'index.mjs'));
  assert.strictEqual(esm.default, require(SDK));
  assert.strictEqual(typeof esm.captureException, 'function');
});
