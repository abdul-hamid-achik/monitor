/**
 * Preload bridge: the only door from the sandboxed renderer into main.
 *
 * It exposes named functions, not ipcRenderer: a compromised renderer can
 * ask for exactly what the app already offers and nothing else. Errors keep
 * the protocol's code and data, so the UI can tell "confirm required" from
 * "unavailable" from "not found".
 */
const { contextBridge, ipcRenderer } = require("electron");

/** Push channels the renderer may subscribe to. */
const EVENTS = new Set([
  "conn:state",
  "conn:notification",
  "launch:started",
  "launch:output",
  "launch:exit",
  "menu:navigate",
  "menu:open-profile",
  "menu:search",
  "app:open-issue",
  "app:error",
]);

/**
 * @param {string} channel
 * @param {...any} args
 */
async function invoke(channel, ...args) {
  const result = await ipcRenderer.invoke(channel, ...args);
  if (result && result.ok === false) {
    const error = new Error(String(result.error ?? "ipc failure"));
    /** @type {any} */ (error).code = result.code ?? null;
    /** @type {any} */ (error).data = result.data ?? null;
    throw error;
  }
  return result?.data;
}

contextBridge.exposeInMainWorld("monitor", {
  appInfo: () => invoke("app:info"),
  settings: {
    get: () => invoke("settings:get"),
    update: (/** @type {any} */ patch) => invoke("settings:update", patch),
  },
  connections: {
    list: () => invoke("conn:list"),
    connect: (/** @type {string} */ id) => invoke("conn:connect", id),
    disconnect: (/** @type {string} */ id) => invoke("conn:disconnect", id),
  },
  /**
   * One monitor.app.v1 call on a connection.
   * @param {string} connId
   * @param {string} method
   * @param {unknown} [params]
   */
  call: (connId, method, params) => invoke("conn:call", connId, method, params),
  launches: {
    start: (/** @type {any} */ req) => invoke("launch:start", req),
    stop: (/** @type {string} */ id) => invoke("launch:stop", id),
    remove: (/** @type {string} */ id) => invoke("launch:remove", id),
    list: () => invoke("launch:list"),
  },
  chalupa: {
    list: () => invoke("chalupa:list"),
  },
  openInEditor: (/** @type {any} */ target) => invoke("editor:open", target),
  copy: (/** @type {string} */ text) => invoke("clipboard:write", text),
  openExternal: (/** @type {string} */ url) => invoke("shell:open-external", url),
  dialogs: {
    chooseDirectory: (/** @type {string | undefined} */ defaultPath) => invoke("dialog:choose-directory", defaultPath),
    chooseProfile: () => invoke("dialog:choose-profile"),
    chooseBinary: () => invoke("dialog:choose-binary"),
    chooseChalupaConfig: () => invoke("dialog:choose-chalupa-config"),
  },
  /**
   * @param {string} channel
   * @param {(payload: any) => void} listener
   * @returns {() => void}
   */
  on(channel, listener) {
    if (!EVENTS.has(channel)) throw new Error(`event not allowed: ${channel}`);
    if (typeof listener !== "function") throw new Error("listener required");
    const wrapped = (/** @type {any} */ _event, /** @type {any} */ payload) => listener(payload);
    ipcRenderer.on(channel, wrapped);
    return () => ipcRenderer.removeListener(channel, wrapped);
  },
  smokeMode: process.argv.includes("--monitor-smoke"),
  smokeReady: (/** @type {any} */ payload) => ipcRenderer.send("smoke:ready", payload),
});
