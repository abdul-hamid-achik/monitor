/**
 * The IPC surface: every request the renderer can make, handled here.
 *
 * The renderer is sandboxed with context isolation and reaches main only
 * through preload's named functions, which map 1:1 onto the channels below.
 * Every handler checks that the sender is this app's own main frame, and
 * wraps its answer in {ok, data} / {ok:false, error, code, data} so an
 * RpcError's code survives the hop.
 */
const { app, clipboard, dialog, ipcMain, shell } = require("electron");
const path = require("node:path");
const { culpritPath, editorURL } = require("./editor");

/**
 * @param {object} deps
 * @param {import("./settings").SettingsStore} deps.settings
 * @param {import("./connections").ConnectionManager} deps.connections
 * @param {import("./launches").LaunchManager} deps.launches
 * @param {() => Electron.BrowserWindow | null} deps.getWindow
 * @param {string} deps.rendererURL   the only page allowed to call in
 * @param {() => Promise<{path: string, source: string} | null>} deps.binaryInfo
 */
function registerIpc(deps) {
  const { settings, connections, launches, getWindow, rendererURL } = deps;

  /**
   * @param {string} channel
   * @param {(...args: any[]) => any} fn
   */
  const handle = (channel, fn) => {
    ipcMain.handle(channel, async (event, ...args) => {
      const frameURL = event.senderFrame?.url ?? "";
      if (!frameURL.startsWith(rendererURL)) {
        return { ok: false, error: `ipc from an unexpected frame refused: ${channel}` };
      }
      try {
        return { ok: true, data: await fn(...args) };
      } catch (error) {
        const err = /** @type {any} */ (error);
        return { ok: false, error: String(err?.message ?? err), code: err?.code ?? null, data: err?.data ?? null };
      }
    });
  };

  handle("app:info", async () => ({
    version: app.getVersion(),
    electron: process.versions.electron,
    platform: process.platform,
    arch: process.arch,
    packaged: app.isPackaged,
    monitor: await deps.binaryInfo(),
  }));

  handle("settings:get", () => settings.get());
  handle("settings:update", (patch) => {
    const next = settings.update(patch ?? {});
    connections.sync(next.connections);
    for (const c of connections.list()) {
      if (c.state === "idle") void connections.get(c.id).connect();
    }
    return next;
  });

  handle("conn:list", () => connections.list());
  handle("conn:connect", (id) => connections.get(String(id)).connect());
  handle("conn:disconnect", (id) => connections.get(String(id)).close());
  handle("conn:call", (id, method, params) => connections.get(String(id)).call(String(method), params));

  handle("launch:start", async (req) => {
    const launch = await launches.start(req);
    const recent = [{ cwd: launch.cwd, command: launch.command, name: launch.name, inspect: launch.inspect, scanStdout: launch.scanStdout }]
      .concat(settings.get().recentLaunches.filter((l) => !(l.cwd === launch.cwd && l.command === launch.command)))
      .slice(0, 10);
    settings.update({ recentLaunches: recent });
    return launch;
  });
  handle("launch:stop", (id) => launches.stop(String(id)));
  handle("launch:remove", (id) => launches.remove(String(id)));
  handle("launch:list", () => launches.list());

  handle("editor:open", async ({ connId, root, file, line }) => {
    const editor = settings.get().editor;
    const conn = connections.get(String(connId)).config;
    const abs = culpritPath(String(root ?? ""), String(file ?? ""));
    if (!abs) throw new Error("this issue has no absolute path to open (it was recorded without one)");
    const url = editorURL({ editor, absPath: abs, line: Number(line), sshHost: conn.kind === "ssh" ? conn.host : undefined });
    if (!url) {
      throw new Error(conn.kind === "ssh"
        ? "opening a remote file needs VS Code or Cursor (Remote-SSH): choose one in Settings"
        : "choose an editor in Settings");
    }
    await shell.openExternal(url);
    return { url };
  });

  handle("clipboard:write", (text) => {
    clipboard.writeText(String(text ?? ""));
    return true;
  });

  handle("shell:open-external", async (url) => {
    const target = String(url ?? "");
    if (!/^https:\/\//i.test(target)) throw new Error("only https links open externally");
    await shell.openExternal(target);
    return true;
  });

  handle("dialog:choose-directory", async (defaultPath) => {
    const win = getWindow();
    /** @type {Electron.OpenDialogOptions} */
    const opts = { properties: ["openDirectory", "createDirectory"], defaultPath: defaultPath || undefined };
    const result = win ? await dialog.showOpenDialog(win, opts) : await dialog.showOpenDialog(opts);
    return result.canceled ? null : result.filePaths[0];
  });

  handle("dialog:choose-profile", async () => {
    const win = getWindow();
    /** @type {Electron.OpenDialogOptions} */
    const opts = {
      properties: ["openFile"],
      filters: [{ name: "CPU / heap profiles", extensions: ["cpuprofile", "pb", "gz"] }],
    };
    const result = win ? await dialog.showOpenDialog(win, opts) : await dialog.showOpenDialog(opts);
    return result.canceled ? null : result.filePaths[0];
  });

  handle("dialog:choose-binary", async () => {
    const win = getWindow();
    /** @type {Electron.OpenDialogOptions} */
    const opts = { properties: ["openFile"], defaultPath: "/opt/homebrew/bin" };
    const result = win ? await dialog.showOpenDialog(win, opts) : await dialog.showOpenDialog(opts);
    return result.canceled ? null : path.resolve(result.filePaths[0]);
  });
}

module.exports = { registerIpc };
