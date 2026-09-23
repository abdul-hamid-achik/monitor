'use strict';
// Synthetic fixture source for internal/stacktrace's chained-exception
// golden test: Node's `{ cause }` option nests the cause's own
// util.inspect output as "[cause]: ..." after the outer error's stack.
function rootCause() {
  throw new Error('root cause boom');
}
function wrapIt() {
  try {
    rootCause();
  } catch (err) {
    throw new Error('work failed', { cause: err });
  }
}
wrapIt();
