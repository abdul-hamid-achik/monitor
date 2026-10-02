/**
 * Launch: run a command under `monitor run --name <name> -- <argv>` from the
 * app, on this machine.
 *
 * This is the zero-SDK path into the app: the command's crashes become
 * issues (and issue.event notifications) the moment they print, and the
 * launch registry entry lets Hot lines profile it by name. The command line
 * is split into argv here — quotes and backslashes are honored, but nothing
 * is expanded and no shell runs it, so a pasted command cannot smuggle in a
 * second one.
 */
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const { lineSplitter } = require("./rpc");

const MAX_LINES = 2000;
const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;
// eslint-disable-next-line no-control-regex
const ANSI_RE = /\u001b\[[0-9;?]*[ -/]*[@-~]|\u001b\][^\u0007]*(\u0007|\u001b\\)/g;

/**
 * Splits a command line into argv: whitespace separates words; single
 * quotes are literal; double quotes allow \" and \\; a backslash outside
 * quotes escapes the next character. No globbing, no $VAR expansion.
 * @param {string} line
 * @returns {string[]}
 */
function splitCommand(line) {
  const out = [];
  let word = "";
  let inWord = false;
  /** @type {null | "'" | '"'} */
  let quote = null;
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (quote === "'") {
      if (ch === "'") quote = null;
      else word += ch;
      continue;
    }
    if (quote === '"') {
      if (ch === '"') quote = null;
      else if (ch === "\\" && (line[i + 1] === '"' || line[i + 1] === "\\")) word += line[++i];
      else word += ch;
      continue;
    }
    if (ch === "'" || ch === '"') {
      quote = ch;
      inWord = true;
      continue;
    }
    if (ch === "\\" && i + 1 < line.length) {
      word += line[++i];
      inWord = true;
      continue;
    }
    if (/\s/.test(ch)) {
      if (inWord) out.push(word);
      word = "";
      inWord = false;
      continue;
    }
    word += ch;
    inWord = true;
  }
  if (quote) throw new Error(`unterminated ${quote === "'" ? "single" : "double"} quote`);
  if (inWord) out.push(word);
  return out;
}

/** @param {string} s */
function stripAnsi(s) {
  return s.replace(ANSI_RE, "");
}

/**
 * Validates a launch request; returns the argv monitor run gets.
 * @param {{cwd: string, command: string, name: string, inspect?: boolean, scanStdout?: boolean}} req
 */
function validateLaunch(req) {
  const name = String(req?.name ?? "").trim();
  if (!NAME_RE.test(name)) throw new Error("name must be letters, digits, . _ - (up to 64), starting with a letter or digit");
  const cwd = String(req?.cwd ?? "");
  if (!path.isAbsolute(cwd) || !fs.existsSync(cwd) || !fs.statSync(cwd).isDirectory()) {
    throw new Error("choose an existing project directory to run in");
  }
  const argv = splitCommand(String(req?.command ?? ""));
  if (argv.length === 0) throw new Error("enter a command to run");
  return { name, cwd, argv, inspect: Boolean(req?.inspect), scanStdout: Boolean(req?.scanStdout) };
}

/**
 * The full `monitor` argv for a validated launch. --inspect is monitor
 * run's own flag (it opens the Node/Deno inspector at launch, the only time
 * monitor ever injects anything), so it goes before the `--`.
 * --scan both also reads stdout, for loggers that print errors there (zap,
 * pino, Rails); the default stays stderr so the child keeps a TTY stdout.
 * @param {{name: string, argv: string[], inspect: boolean, scanStdout?: boolean}} launch
 */
function monitorRunArgs({ name, argv, inspect, scanStdout }) {
  return [
    "run",
    "--name", name,
    ...(scanStdout ? ["--scan", "both"] : []),
    ...(inspect ? ["--inspect"] : []),
    "--",
    ...argv,
  ];
}

class LaunchManager {
  /**
   * @param {object} deps
   * @param {() => Promise<string | null>} deps.monitorBinary
   * @param {() => Promise<NodeJS.ProcessEnv>} deps.env
   * @param {(event: string, payload: any) => void} deps.emit
   */
  constructor(deps) {
    this.deps = deps;
    /** @type {Map<string, {id: string, name: string, cwd: string, command: string, inspect: boolean, scanStdout: boolean, startedAt: string, child: import("node:child_process").ChildProcess | null, lines: {stream: string, text: string}[], exitCode: number | null, signal: string | null, running: boolean}>} */
    this.launches = new Map();
    this.seq = 0;
  }

  /** @param {{cwd: string, command: string, name: string, inspect?: boolean, scanStdout?: boolean}} req */
  async start(req) {
    const valid = validateLaunch(req);
    const { name, cwd } = valid;
    for (const l of this.launches.values()) {
      if (l.running && l.name === name) throw new Error(`a launch named ${name} is already running`);
    }
    const binary = await this.deps.monitorBinary();
    if (!binary) throw new Error("monitor binary not found: set its path in Settings");
    const id = `launch-${Date.now()}-${++this.seq}`;
    const child = spawn(binary, monitorRunArgs(valid), {
      cwd,
      env: await this.deps.env(),
      stdio: ["ignore", "pipe", "pipe"],
    });
    const launch = {
      id, name, cwd, command: String(req.command), inspect: valid.inspect, scanStdout: valid.scanStdout, startedAt: new Date().toISOString(),
      child, lines: [], exitCode: null, signal: null, running: true,
    };
    this.launches.set(id, launch);
    const push = (stream) => lineSplitter((raw) => {
      const text = stripAnsi(raw.replace(/\r$/, ""));
      launch.lines.push({ stream, text });
      if (launch.lines.length > MAX_LINES) launch.lines.shift();
      this.deps.emit("launch:output", { id, stream, text });
    });
    child.stdout?.on("data", push("stdout"));
    child.stderr?.on("data", push("stderr"));
    child.on("error", (error) => {
      launch.running = false;
      launch.lines.push({ stream: "stderr", text: `could not start monitor: ${error.message}` });
      this.deps.emit("launch:exit", this.describe(launch));
    });
    child.on("exit", (code, signal) => {
      launch.running = false;
      launch.exitCode = code;
      launch.signal = signal;
      launch.child = null;
      this.deps.emit("launch:exit", this.describe(launch));
    });
    this.deps.emit("launch:started", this.describe(launch));
    return this.describe(launch);
  }

  /**
   * Stops a launch: SIGINT (monitor run forwards it), then SIGTERM, then SIGKILL.
   * @param {string} id
   */
  stop(id) {
    const launch = this.launches.get(id);
    if (!launch?.child || !launch.running) return false;
    const child = launch.child;
    child.kill("SIGINT");
    const term = setTimeout(() => child.exitCode === null && child.kill("SIGTERM"), 5000);
    const hard = setTimeout(() => child.exitCode === null && child.kill("SIGKILL"), 10_000);
    child.once("exit", () => {
      clearTimeout(term);
      clearTimeout(hard);
    });
    return true;
  }

  /** @param {string} id */
  remove(id) {
    const launch = this.launches.get(id);
    if (launch && !launch.running) this.launches.delete(id);
  }

  /** @param {any} launch */
  describe(launch) {
    return {
      id: launch.id, name: launch.name, cwd: launch.cwd, command: launch.command, inspect: launch.inspect, scanStdout: launch.scanStdout, startedAt: launch.startedAt,
      running: launch.running, exitCode: launch.exitCode, signal: launch.signal,
    };
  }

  list() {
    return [...this.launches.values()].map((l) => ({ ...this.describe(l), lines: l.lines.slice(-500) }));
  }

  stopAll() {
    for (const l of this.launches.values()) if (l.running) this.stop(l.id);
  }
}

module.exports = { LaunchManager, splitCommand, stripAnsi, validateLaunch, monitorRunArgs };
