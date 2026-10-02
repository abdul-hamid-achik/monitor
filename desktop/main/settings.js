/**
 * App settings: one JSON file in the app's userData directory, written
 * atomically (temp file + rename) so a crash mid-save never leaves half a
 * file. Unknown keys are dropped and every value is validated on load, so a
 * hand-edited file can never smuggle in an unvalidated connection.
 */
const fs = require("node:fs");
const path = require("node:path");

const EDITORS = ["vscode", "cursor", "zed", "idea", "none"];

/**
 * @typedef {object} Connection
 * @property {string} id
 * @property {string} name
 * @property {"local" | "ssh" | "chalupa"} kind
 * @property {string} [host]         ssh destination: an alias or user@host
 * @property {string} [env]          chalupa managed environment name
 * @property {string} [config]       chalupa.yml of a BYOC stack (absolute)
 * @property {string} [monitorPath]  remote binary, default "monitor"
 * @property {boolean} [readOnly]    spawn with --read-only
 */

/**
 * @typedef {object} Settings
 * @property {Connection[]} connections
 * @property {string} monitorPath    local binary override ("" = auto)
 * @property {string} editor
 * @property {boolean} notifications
 * @property {{cwd: string, command: string, name: string, inspect: boolean, scanStdout: boolean}[]} recentLaunches
 */

/** @returns {Settings} */
function defaults() {
  return {
    connections: [{ id: "local", name: "This Mac", kind: "local" }],
    monitorPath: "",
    editor: "vscode",
    notifications: true,
    recentLaunches: [],
  };
}

/** An ssh destination: alias, host or user@host[:port]-free. Never an option. */
const SSH_HOST_RE = /^[A-Za-z0-9_][A-Za-z0-9._@\-]{0,252}$/;
/** A remote binary path: absolute or a bare command, no shell metacharacters. */
const REMOTE_PATH_RE = /^[A-Za-z0-9_./~+\-]{1,512}$/;
const CONNECTION_ID_RE = /^[A-Za-z0-9_-]{1,64}$/;
/** A Chalupa environment name (Chalupa's own StackNameSchema, plus an instance suffix). */
const CHALUPA_ENV_RE = /^[a-z0-9](?:[a-z0-9-]{0,126}[a-z0-9])?$/;

/**
 * Validates one connection; returns a clean copy or throws.
 * @param {any} raw
 * @returns {Connection}
 */
function validateConnection(raw) {
  if (!raw || typeof raw !== "object") throw new Error("connection must be an object");
  const id = String(raw.id ?? "");
  if (!CONNECTION_ID_RE.test(id)) throw new Error(`invalid connection id ${JSON.stringify(id)}`);
  const name = String(raw.name ?? "").trim().slice(0, 80) || id;
  if (raw.kind === "local") {
    return { id, name, kind: "local", readOnly: Boolean(raw.readOnly) };
  }
  if (raw.kind === "chalupa") {
    // Exactly one target: a managed environment by name, or a BYOC stack by
    // its chalupa.yml. Chalupa's door is always read-only; mirror it here.
    const env = String(raw.env ?? "").trim();
    const config = String(raw.config ?? "").trim();
    if (env && config) throw new Error(`connection ${id}: set either a Chalupa environment or a chalupa.yml, not both`);
    if (config) {
      if (!path.isAbsolute(config) || /[\u0000-\u001f]/.test(config) || !/\.ya?ml$/.test(config)) {
        throw new Error(`connection ${id}: the Chalupa config must be an absolute path to a .yml file`);
      }
      return { id, name, kind: "chalupa", config, readOnly: true };
    }
    if (!CHALUPA_ENV_RE.test(env)) throw new Error(`connection ${id}: ${JSON.stringify(env)} is not a Chalupa environment name`);
    return { id, name, kind: "chalupa", env, readOnly: true };
  }
  if (raw.kind !== "ssh") throw new Error(`connection ${id}: kind must be local, ssh or chalupa`);
  const host = String(raw.host ?? "").trim();
  if (!SSH_HOST_RE.test(host)) {
    throw new Error(`connection ${id}: host must be an ssh alias or user@host (letters, digits, . _ - @), got ${JSON.stringify(host)}`);
  }
  const monitorPath = String(raw.monitorPath ?? "").trim() || "monitor";
  if (!REMOTE_PATH_RE.test(monitorPath)) {
    throw new Error(`connection ${id}: remote monitor path has characters that are not allowed`);
  }
  return { id, name, kind: "ssh", host, monitorPath, readOnly: Boolean(raw.readOnly) };
}

/**
 * Normalizes a whole settings object, dropping invalid entries.
 * @param {any} raw
 * @returns {Settings}
 */
function normalize(raw) {
  const base = defaults();
  if (!raw || typeof raw !== "object") return base;
  const connections = [];
  const seen = new Set();
  for (const entry of Array.isArray(raw.connections) ? raw.connections : []) {
    try {
      const conn = validateConnection(entry);
      if (seen.has(conn.id)) continue;
      seen.add(conn.id);
      connections.push(conn);
    } catch {
      // Dropped: an invalid hand edit must not break startup.
    }
  }
  if (!connections.some((c) => c.kind === "local")) connections.unshift(base.connections[0]);
  return {
    connections,
    monitorPath: typeof raw.monitorPath === "string" ? raw.monitorPath.trim() : "",
    editor: EDITORS.includes(raw.editor) ? raw.editor : base.editor,
    notifications: typeof raw.notifications === "boolean" ? raw.notifications : base.notifications,
    recentLaunches: (Array.isArray(raw.recentLaunches) ? raw.recentLaunches : [])
      .filter((l) => l && typeof l.cwd === "string" && typeof l.command === "string" && typeof l.name === "string")
      .slice(0, 10)
      .map((l) => ({ cwd: l.cwd, command: l.command, name: l.name, inspect: Boolean(l.inspect), scanStdout: Boolean(l.scanStdout) })),
  };
}

class SettingsStore {
  /** @param {string} file */
  constructor(file) {
    this.file = file;
    /** @type {Settings} */
    this.value = this.load();
  }

  /** @returns {Settings} */
  load() {
    try {
      return normalize(JSON.parse(fs.readFileSync(this.file, "utf8")));
    } catch {
      return defaults();
    }
  }

  /** @returns {Settings} */
  get() {
    return structuredClone(this.value);
  }

  /**
   * Applies a partial update after validation and saves it.
   * @param {Partial<Settings>} patch
   * @returns {Settings}
   */
  update(patch) {
    const next = normalize({ ...this.value, ...patch });
    if (Array.isArray(patch.connections)) {
      // Validate strictly on explicit edits: report the problem instead of
      // silently dropping the user's new connection.
      patch.connections.forEach(validateConnection);
    }
    this.value = next;
    this.save();
    return this.get();
  }

  save() {
    fs.mkdirSync(path.dirname(this.file), { recursive: true });
    const tmp = `${this.file}.${process.pid}.tmp`;
    fs.writeFileSync(tmp, JSON.stringify(this.value, null, 2), { mode: 0o600 });
    fs.renameSync(tmp, this.file);
  }
}

module.exports = { SettingsStore, normalize, validateConnection, defaults, EDITORS };
