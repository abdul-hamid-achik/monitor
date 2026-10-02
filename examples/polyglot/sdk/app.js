// A small Node app with three errors, used by specs/run_probes_node.yml and
// the SDK docs. Only one of them reaches stderr in a form a stderr parser
// can see on its own:
//   - a config error the app catches and prints with console.error;
//   - a promise rejection the app's own listener writes to a log FILE,
//     invisible on stderr: only `monitor run --probes` (or the SDK) sees it;
//   - an uncaught crash with a cause.
const fs = require('fs');
const path = require('path');

const logFile = path.join(process.env.APP_LOG_DIR || require('os').tmpdir(), 'sdk-example-app.log');
process.on('unhandledRejection', (reason) => {
  fs.appendFileSync(logFile, `rejected: ${reason && reason.message}\n`);
});

console.log('booting');

function loadConfig(text) {
  return JSON.parse(text);
}

try {
  loadConfig('{bad');
} catch (err) {
  console.error('config failed:', err);
}

function chargeCard(order) {
  return Promise.reject(new TypeError(`payment gateway timeout for order ${order}`));
}

chargeCard(42);

function drainQueue() {
  throw new Error('queue worker crashed', { cause: new RangeError('queue overflow') });
}

setTimeout(drainQueue, 50);
