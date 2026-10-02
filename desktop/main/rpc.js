/**
 * A JSON-RPC 2.0 client over a line stream — the client half of
 * monitor.app.v1 (docs/contracts/app-protocol-v1.md).
 *
 * Pure: it never touches a process. The owner feeds it lines with
 * `handleLine` and gives it a `write` function; that keeps it testable with
 * plain arrays and lets the same client sit on a local `monitor serve
 * --stdio` or one spawned over SSH.
 */

/** Default per-call timeout. Slow methods pass their own. */
const DEFAULT_TIMEOUT_MS = 30_000;

/** A JSON-RPC error answer, carrying the protocol's code and data. */
class RpcError extends Error {
  /**
   * @param {number} code
   * @param {string} message
   * @param {unknown} [data]
   */
  constructor(code, message, data) {
    super(message);
    this.name = "RpcError";
    this.code = code;
    this.data = data;
  }
}

class RpcClient {
  /**
   * @param {object} opts
   * @param {(line: string) => void} opts.write  writes one line (no newline needed)
   * @param {(method: string, params: any) => void} [opts.onNotification]
   * @param {(line: string, error: Error) => void} [opts.onGarbage] a line that is not JSON-RPC
   */
  constructor({ write, onNotification, onGarbage }) {
    this.write = write;
    this.onNotification = onNotification ?? (() => {});
    this.onGarbage = onGarbage ?? (() => {});
    this.nextId = 1;
    /** @type {Map<number, {resolve: (v: any) => void, reject: (e: Error) => void, timer: ReturnType<typeof setTimeout>, method: string}>} */
    this.pending = new Map();
    this.closed = false;
  }

  /**
   * Sends a request and resolves with its result.
   * @param {string} method
   * @param {unknown} [params]
   * @param {number} [timeoutMs]
   * @returns {Promise<any>}
   */
  call(method, params, timeoutMs = DEFAULT_TIMEOUT_MS) {
    if (this.closed) return Promise.reject(new RpcError(-32000, "connection closed"));
    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new RpcError(-32000, `${method} timed out after ${Math.round(timeoutMs / 1000)}s`));
      }, timeoutMs);
      this.pending.set(id, { resolve, reject, timer, method });
      const message = { jsonrpc: "2.0", id, method };
      if (params !== undefined) message.params = params;
      try {
        this.write(JSON.stringify(message));
      } catch (error) {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(error instanceof Error ? error : new Error(String(error)));
      }
    });
  }

  /**
   * Feeds one line read from the server.
   * @param {string} line
   */
  handleLine(line) {
    const trimmed = line.trim();
    if (!trimmed) return;
    let msg;
    try {
      msg = JSON.parse(trimmed);
    } catch (error) {
      this.onGarbage(trimmed, /** @type {Error} */ (error));
      return;
    }
    if (!msg || msg.jsonrpc !== "2.0") {
      this.onGarbage(trimmed, new Error("not a JSON-RPC 2.0 message"));
      return;
    }
    if (typeof msg.method === "string" && msg.id === undefined) {
      this.onNotification(msg.method, msg.params);
      return;
    }
    if (typeof msg.id !== "number") {
      // A response to nothing we sent (a parse error answer has id null).
      if (msg.error) this.onGarbage(trimmed, new Error(msg.error.message ?? "server error"));
      return;
    }
    const entry = this.pending.get(msg.id);
    if (!entry) return;
    this.pending.delete(msg.id);
    clearTimeout(entry.timer);
    if (msg.error) {
      entry.reject(new RpcError(msg.error.code ?? -32603, msg.error.message ?? "server error", msg.error.data));
    } else {
      entry.resolve(msg.result);
    }
  }

  /**
   * Rejects every pending call and refuses new ones.
   * @param {Error} reason
   */
  close(reason) {
    this.closed = true;
    for (const [id, entry] of this.pending) {
      clearTimeout(entry.timer);
      entry.reject(reason instanceof RpcError ? reason : new RpcError(-32000, reason.message));
      this.pending.delete(id);
    }
  }
}

/**
 * Splits a byte stream into lines, holding a partial trailing line until
 * its newline arrives.
 * @param {(line: string) => void} onLine
 * @returns {(chunk: Buffer | string) => void}
 */
function lineSplitter(onLine) {
  let buffer = "";
  return (chunk) => {
    buffer += chunk.toString("utf8");
    let index = buffer.indexOf("\n");
    while (index !== -1) {
      onLine(buffer.slice(0, index));
      buffer = buffer.slice(index + 1);
      index = buffer.indexOf("\n");
    }
  };
}

/**
 * How long a call may take: a profile capture runs for its own duration
 * first, and doctor probes every ecosystem tool.
 * @param {string} method
 * @param {any} params
 */
function timeoutFor(method, params) {
  if (method === "profile.capture") {
    const seconds = Number(params?.duration_seconds) > 0 ? Number(params.duration_seconds) : 5;
    return seconds * 1000 + 60_000;
  }
  if (method === "doctor" || method === "incidents.list") return 60_000;
  return DEFAULT_TIMEOUT_MS;
}

module.exports = { RpcClient, RpcError, lineSplitter, timeoutFor, DEFAULT_TIMEOUT_MS };
