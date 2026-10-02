/**
 * Connections: one `monitor serve --stdio` per configured host.
 *
 * A local connection spawns the monitor binary directly; an SSH connection
 * spawns `ssh -T <host> <monitorPath> serve --stdio`. Both speak the same
 * protocol over the child's stdin/stdout, so everything above this file is
 * transport-blind. SSH runs with BatchMode=yes: it uses the user's agent and
 * keys and fails fast instead of hanging on a password prompt the app has no
 * way to show. The app never sees or stores a credential.
 */
const { spawn } = require("node:child_process");
const os = require("node:os");
const { RpcClient, RpcError, lineSplitter, timeoutFor } = require("./rpc");

const PROTOCOL = "monitor.app.v1";
const HELLO_TIMEOUT_MS = { local: 15_000, ssh: 30_000, chalupa: 45_000 };
const MAX_STDERR_LINES = 40;
const MAX_RETRIES_BEFORE_READY = 4;
const BACKOFF_MAX_MS = 30_000;

/** Methods the renderer may call. Anything else is refused in main. */
const ALLOWED_METHODS = new Set([
  "hello",
  "ping",
  "issues.list",
  "issues.get",
  "issues.occurrences",
  "issues.histogram",
  "issues.set_status",
  "projects.list",
  "host.snapshot",
  "processes.list",
  "process.kill",
  "profile.capture",
  "heatmap.file",
  "launches.list",
  "logs.search",
  "incidents.list",
  "doctor",
  "subscribe",
  "unsubscribe",
]);

/**
 * The argv a connection spawns.
 * @param {import("./settings").Connection} config
 * @param {string | null} localBinary
 * @returns {{command: string, args: string[]}}
 */
function spawnArgs(config, localBinary) {
  const serveArgs = ["serve", "--stdio", ...(config.readOnly ? ["--read-only"] : [])];
  if (config.kind === "chalupa") {
    // Chalupa owns the droplet's address, identity and pinned host key; its
    // stdio door (`chalupa monitor serve`) runs the droplet's monitor with
    // --read-only and writes nothing but protocol bytes to stdout.
    const target = config.config ? ["--config", String(config.config)] : ["--name", String(config.env)];
    return { command: "chalupa", args: ["monitor", "serve", ...target] };
  }
  if (config.kind === "ssh") {
    return {
      command: "ssh",
      args: [
        "-T",
        "-o", "BatchMode=yes",
        "-o", "ConnectTimeout=10",
        "-o", "ServerAliveInterval=15",
        "-o", "ServerAliveCountMax=3",
        "--",
        String(config.host),
        config.monitorPath || "monitor",
        ...serveArgs,
      ],
    };
  }
  if (!localBinary) throw new Error("monitor binary not found");
  return { command: localBinary, args: serveArgs };
}

/**
 * Turns a failed spawn into something a person can act on.
 * @param {import("./settings").Connection} config
 * @param {number | null} code
 * @param {string[]} stderr
 */
function explainExit(config, code, stderr) {
  const tail = stderr.slice(-6).join("\n").trim();
  if (config.kind === "chalupa") {
    if (/unknown command "serve"|unknown flag: --stdio/.test(tail)) {
      return `the monitor on Chalupa environment ${config.env} is too old to speak ${PROTOCOL}: it needs a monitor release with \`monitor serve\` and Chalupa's pinned version raised to it.`;
    }
    if (/Unknown command|unknown command|Usage: chalupa|usage: chalupa/.test(tail) && /monitor/.test(tail)) {
      return "this chalupa CLI has no `chalupa monitor serve` yet: update chalupa.";
    }
    if (code === 69) return `Chalupa could not load ${config.config ?? "the config"}${tail ? `: ${tail}` : ""}`;
    // 64 and anything else: Chalupa's own one-line reason (unknown env,
    // expired session, no compute address, monitor not provisioned).
    return tail || `chalupa monitor serve exited with code ${code}`;
  }
  if (config.kind === "ssh") {
    if (code === 127 || /command not found|No such file or directory/.test(tail)) {
      return `monitor was not found on ${config.host}. Install it there, or set this connection's remote monitor path (for example ~/go/bin/monitor).`;
    }
    if (/unknown command "serve"|unknown flag: --stdio/.test(tail)) {
      return `the monitor on ${config.host} is too old to speak ${PROTOCOL}: upgrade it to a version with \`monitor serve --stdio\`.`;
    }
    if (code === 255) {
      return `ssh could not connect to ${config.host}${tail ? `: ${tail}` : ""}. The app uses your ssh config, agent and keys with BatchMode=yes, so a host that needs a password cannot connect.`;
    }
  }
  if (/unknown command "serve"|unknown flag: --stdio/.test(tail)) {
    return `this monitor binary is too old to speak ${PROTOCOL}: point Settings → monitor binary at a newer one.`;
  }
  return tail || `monitor serve exited with code ${code}`;
}

class Connection {
  /**
   * @param {import("./settings").Connection} config
   * @param {object} deps
   * @param {() => Promise<string | null>} deps.localBinary
   * @param {() => Promise<NodeJS.ProcessEnv>} deps.env
   * @param {(conn: Connection) => void} deps.onState
   * @param {(conn: Connection, method: string, params: any) => void} deps.onNotification
   * @param {typeof spawn} [deps.spawn]
   */
  constructor(config, deps) {
    this.config = config;
    this.deps = deps;
    /** @type {"idle" | "connecting" | "ready" | "error" | "closed"} */
    this.state = "idle";
    /** @type {any} */
    this.hello = null;
    /** @type {string | null} */
    this.error = null;
    /** @type {string[]} */
    this.stderr = [];
    /** @type {import("node:child_process").ChildProcess | null} */
    this.child = null;
    /** @type {RpcClient | null} */
    this.rpc = null;
    this.intentionalClose = false;
    this.attempt = 0;
    this.everReady = false;
    /** @type {ReturnType<typeof setTimeout> | null} */
    this.retryTimer = null;
  }

  get id() {
    return this.config.id;
  }

  snapshot() {
    return {
      id: this.config.id,
      name: this.config.name,
      kind: this.config.kind,
      host: this.config.host ?? (this.config.kind === "chalupa" ? `chalupa:${this.config.env ?? this.config.config}` : null),
      readOnly: Boolean(this.config.readOnly),
      state: this.state,
      error: this.error,
      hello: this.hello,
    };
  }

  /** @param {Connection["state"]} state @param {string | null} [error] */
  setState(state, error = null) {
    this.state = state;
    this.error = error;
    this.deps.onState(this);
  }

  async connect() {
    if (this.state === "connecting" || this.state === "ready") return;
    this.intentionalClose = false;
    if (this.retryTimer) clearTimeout(this.retryTimer);
    this.retryTimer = null;
    this.hello = null;
    this.stderr = [];
    this.setState("connecting");

    let command;
    let args;
    try {
      const binary = this.config.kind === "local" ? await this.deps.localBinary() : null;
      ({ command, args } = spawnArgs(this.config, binary));
    } catch (error) {
      this.setState("error", `${/** @type {Error} */ (error).message}. Install monitor (brew install --cask abdul-hamid-achik/tap/monitor) or set its path in Settings.`);
      return;
    }

    const child = (this.deps.spawn ?? spawn)(command, args, {
      // The app's own cwd ("/" when opened from Finder) means nothing to
      // monitor; home is where a terminal would start it.
      cwd: os.homedir(),
      env: await this.deps.env(),
      stdio: ["pipe", "pipe", "pipe"],
    });
    this.child = child;

    let helloTimer = null;
    const rpc = new RpcClient({
      write: (line) => {
        if (child.stdin && !child.stdin.destroyed) child.stdin.write(`${line}\n`);
      },
      onNotification: (method, params) => {
        if (method === "hello") {
          clearTimeout(helloTimer);
          if (params?.protocol !== PROTOCOL) {
            this.fail(`expected protocol ${PROTOCOL}, the server speaks ${params?.protocol ?? "nothing"}`, false);
            return;
          }
          this.hello = params;
          this.attempt = 0;
          this.everReady = true;
          this.setState("ready");
          if (Array.isArray(params.topics) && params.topics.includes("issues")) {
            rpc.call("subscribe", { topics: ["issues"] }).catch(() => {});
          }
          return;
        }
        this.deps.onNotification(this, method, params);
      },
      onGarbage: (line) => this.pushStderr(`[stdout] ${line.slice(0, 300)}`),
    });
    this.rpc = rpc;

    child.stdout?.on("data", lineSplitter((line) => rpc.handleLine(line)));
    child.stderr?.on("data", lineSplitter((line) => this.pushStderr(line)));
    child.stdin?.on("error", () => {});
    child.on("error", (error) => {
      const missing = /** @type {NodeJS.ErrnoException} */ (error).code === "ENOENT";
      if (missing && command === "chalupa") return this.fail("the chalupa CLI is not on PATH: install it from https://chalupa.run", false);
      if (missing && command === "ssh") return this.fail("ssh is not on PATH", false);
      this.fail(`could not start ${command}: ${error.message}`, true);
    });
    child.on("exit", (code) => {
      clearTimeout(helloTimer);
      if (this.child !== child) return;
      this.child = null;
      if (this.intentionalClose) {
        rpc.close(new RpcError(-32000, "connection closed"));
        this.setState("closed");
        return;
      }
      this.fail(explainExit(this.config, code, this.stderr), true);
    });
    helloTimer = setTimeout(() => {
      if (this.state === "connecting") this.fail(`no hello from monitor serve within ${HELLO_TIMEOUT_MS[this.config.kind] / 1000}s`, true);
    }, HELLO_TIMEOUT_MS[this.config.kind]);
  }

  /** @param {string} line */
  pushStderr(line) {
    this.stderr.push(line);
    if (this.stderr.length > MAX_STDERR_LINES) this.stderr.shift();
  }

  /**
   * Ends this attempt with an error and, when it may help, schedules a
   * retry with exponential backoff.
   * @param {string} message
   * @param {boolean} retry
   */
  fail(message, retry) {
    this.rpc?.close(new RpcError(-32000, message));
    const child = this.child;
    this.child = null;
    if (child && child.exitCode === null) child.kill("SIGTERM");
    this.setState("error", message);
    if (!retry || this.intentionalClose) return;
    if (!this.everReady && this.attempt >= MAX_RETRIES_BEFORE_READY) return;
    const delay = Math.min(1000 * 2 ** this.attempt, BACKOFF_MAX_MS);
    this.attempt += 1;
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      void this.connect();
    }, delay);
  }

  /**
   * @param {string} method
   * @param {unknown} params
   */
  async call(method, params) {
    if (!ALLOWED_METHODS.has(method)) throw new RpcError(-32601, `method not allowed: ${method}`);
    if (this.state !== "ready" || !this.rpc) {
      throw new RpcError(-32000, `${this.config.name} is not connected${this.error ? `: ${this.error}` : ""}`);
    }
    return this.rpc.call(method, params, timeoutFor(method, params));
  }

  close() {
    this.intentionalClose = true;
    if (this.retryTimer) clearTimeout(this.retryTimer);
    this.retryTimer = null;
    const child = this.child;
    if (!child) {
      this.setState("closed");
      return;
    }
    child.stdin?.end();
    const killTimer = setTimeout(() => {
      if (child.exitCode === null) child.kill("SIGTERM");
    }, 2000);
    child.once("exit", () => clearTimeout(killTimer));
  }
}

class ConnectionManager {
  /**
   * @param {object} deps
   * @param {() => Promise<string | null>} deps.localBinary
   * @param {() => Promise<NodeJS.ProcessEnv>} deps.env
   * @param {(snapshot: ReturnType<Connection["snapshot"]>) => void} deps.onState
   * @param {(connId: string, method: string, params: any) => void} deps.onNotification
   * @param {typeof spawn} [deps.spawn]
   */
  constructor(deps) {
    this.deps = deps;
    /** @type {Map<string, Connection>} */
    this.connections = new Map();
  }

  /** @param {import("./settings").Connection[]} configs */
  sync(configs) {
    const wanted = new Map(configs.map((c) => [c.id, c]));
    for (const [id, conn] of this.connections) {
      const next = wanted.get(id);
      if (!next || JSON.stringify(next) !== JSON.stringify(conn.config)) {
        conn.close();
        this.connections.delete(id);
      }
    }
    for (const config of configs) {
      if (this.connections.has(config.id)) continue;
      this.connections.set(
        config.id,
        new Connection(config, {
          localBinary: this.deps.localBinary,
          env: this.deps.env,
          spawn: this.deps.spawn,
          onState: (conn) => this.deps.onState(conn.snapshot()),
          onNotification: (conn, method, params) => this.deps.onNotification(conn.id, method, params),
        }),
      );
    }
  }

  list() {
    return [...this.connections.values()].map((c) => c.snapshot());
  }

  /** @param {string} id */
  get(id) {
    const conn = this.connections.get(id);
    if (!conn) throw new RpcError(-32000, `unknown connection ${id}`);
    return conn;
  }

  closeAll() {
    for (const conn of this.connections.values()) conn.close();
  }
}

module.exports = { ConnectionManager, Connection, spawnArgs, explainExit, ALLOWED_METHODS, PROTOCOL };
