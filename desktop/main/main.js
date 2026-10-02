/**
 * Monitor Desktop — Electron main process.
 *
 * Deliberately thin: it owns the window, the settings file, the
 * connections (one `monitor serve --stdio` each, local or over SSH) and the
 * launches, and it turns issue events into OS notifications. Every product
 * answer comes from monitor itself over monitor.app.v1.
 */
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { pathToFileURL } = require("node:url");
const { app, BrowserWindow, Menu, Notification, session, shell } = require("electron");

const { ConnectionManager } = require("./connections");
const { LaunchManager } = require("./launches");
const { SettingsStore } = require("./settings");
const { registerIpc } = require("./ipc");
const { resolveMonitorBinary } = require("./binary");
const { childEnv, resolvedPath } = require("./env");

const isMac = process.platform === "darwin";
const argv = process.argv.slice(1);
const smokeMode = argv.includes("--smoke");
// --capture <dir>: a development harness that walks every view and saves a
// PNG of each (docs screenshots, visual checks). Never in a packaged app.
const captureIndex = argv.indexOf("--capture");
const captureDir = captureIndex >= 0 && !app.isPackaged ? path.resolve(argv[captureIndex + 1] ?? "captures") : null;
const desktopRoot = path.resolve(__dirname, "..");
const repoRoot = path.resolve(desktopRoot, "..");
const rendererFile = path.join(desktopRoot, "dist", "renderer", "index.html");
const rendererURL = pathToFileURL(path.dirname(rendererFile)).href;

if (smokeMode || captureDir) {
  // A smoke or capture run must never read or write the user's real settings.
  app.setPath("userData", fs.mkdtempSync(path.join(os.tmpdir(), "monitor-desktop-smoke-")));
}

/** @type {BrowserWindow | null} */
let mainWindow = null;
/** @type {SettingsStore} */
let settings;
/** @type {ConnectionManager} */
let connections;
/** @type {LaunchManager} */
let launches;

function getWindow() {
  return mainWindow && !mainWindow.isDestroyed() ? mainWindow : null;
}

/**
 * @param {string} channel
 * @param {unknown} payload
 */
function send(channel, payload) {
  getWindow()?.webContents.send(channel, payload);
}

async function monitorBinary() {
  return resolveMonitorBinary({
    override: settings.get().monitorPath,
    resourcesPath: process.resourcesPath,
    packaged: app.isPackaged,
    repoRoot,
    pathList: await resolvedPath(),
  });
}

// OS notifications for new and regressed issues: one per issue per minute,
// and at most five in any ten seconds, so a crash loop is one alert, not a
// wall of them.
const notifiedAt = new Map();
/** @type {number[]} */
let recentNotifications = [];
/**
 * @param {string} connId
 * @param {any} event
 */
function maybeNotify(connId, event) {
  if (!settings.get().notifications || !Notification.isSupported()) return;
  if (event?.type !== "new" && event?.type !== "regressed") return;
  const issue = event.issue ?? {};
  const key = `${connId}:${issue.id}`;
  const now = Date.now();
  if (now - (notifiedAt.get(key) ?? 0) < 60_000) return;
  recentNotifications = recentNotifications.filter((t) => now - t < 10_000);
  if (recentNotifications.length >= 5) return;
  notifiedAt.set(key, now);
  recentNotifications.push(now);
  const conn = connections.list().find((c) => c.id === connId);
  const where = issue.culprit?.file ? `${issue.culprit.file}:${issue.culprit.line ?? ""}` : issue.project ?? "";
  const n = new Notification({
    title: `${event.type === "new" ? "New issue" : "Regressed"} · ${issue.project || conn?.name || ""}`.trim(),
    subtitle: conn && conn.kind === "ssh" ? conn.name : undefined,
    // Notification text is plain text: the untrusted title cannot inject markup.
    body: `${String(issue.title ?? "").slice(0, 180)}${where ? `\n${where}` : ""}`,
    silent: false,
  });
  n.on("click", () => {
    const win = getWindow();
    if (win) {
      if (win.isMinimized()) win.restore();
      win.show();
      win.focus();
    }
    send("app:open-issue", { connId, id: issue.id });
  });
  n.show();
}

function createMainWindow() {
  const win = new BrowserWindow({
    width: 1440,
    height: 920,
    minWidth: 1024,
    minHeight: 640,
    show: false,
    backgroundColor: "#11161d",
    title: "Monitor",
    ...(isMac ? { titleBarStyle: "hiddenInset", trafficLightPosition: { x: 16, y: 18 } } : {}),
    webPreferences: {
      preload: path.join(desktopRoot, "preload", "preload.js"),
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webSecurity: true,
      spellcheck: false,
      additionalArguments: smokeMode ? ["--monitor-smoke"] : [],
    },
  });
  win.once("ready-to-show", () => {
    if (captureDir) win.showInactive();
    else if (!smokeMode) win.show();
  });
  win.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https:\/\//i.test(url)) void shell.openExternal(url);
    return { action: "deny" };
  });
  win.webContents.on("will-navigate", (event, url) => {
    if (!url.startsWith(rendererURL)) event.preventDefault();
  });
  win.on("closed", () => {
    if (mainWindow === win) mainWindow = null;
  });
  void win.loadFile(rendererFile);
  return win;
}

function buildMenu() {
  /** @param {string} view */
  const nav = (view) => () => send("menu:navigate", { view });
  /** @type {Electron.MenuItemConstructorOptions[]} */
  const template = [
    ...(isMac ? [{ role: /** @type {const} */ ("appMenu") }] : []),
    {
      label: "File",
      submenu: [
        { label: "New Launch…", accelerator: "CmdOrCtrl+N", click: nav("launch") },
        { label: "Open Profile…", accelerator: "CmdOrCtrl+O", click: () => send("menu:open-profile", {}) },
        { type: "separator" },
        isMac ? { role: "close" } : { role: "quit" },
      ],
    },
    { role: "editMenu" },
    {
      label: "View",
      submenu: [
        { label: "Issues", accelerator: "CmdOrCtrl+1", click: nav("issues") },
        { label: "Hot Lines", accelerator: "CmdOrCtrl+2", click: nav("hot") },
        { label: "Host", accelerator: "CmdOrCtrl+3", click: nav("host") },
        { label: "Launch", accelerator: "CmdOrCtrl+4", click: nav("launch") },
        { label: "Logs", accelerator: "CmdOrCtrl+5", click: nav("logs") },
        { label: "Incidents", accelerator: "CmdOrCtrl+6", click: nav("incidents") },
        { label: "Doctor", accelerator: "CmdOrCtrl+7", click: nav("doctor") },
        { label: "Connections", accelerator: "CmdOrCtrl+,", click: nav("settings") },
        { type: "separator" },
        { label: "Search Issues", accelerator: "CmdOrCtrl+K", click: () => send("menu:search", {}) },
        { type: "separator" },
        { role: "reload" },
        { role: "toggleDevTools" },
        { type: "separator" },
        { role: "resetZoom" },
        { role: "zoomIn" },
        { role: "zoomOut" },
        { type: "separator" },
        { role: "togglefullscreen" },
      ],
    },
    { role: "windowMenu" },
    {
      label: "Help",
      submenu: [
        { label: "Monitor documentation", click: () => void shell.openExternal("https://monitorcli.dev/") },
        { label: "App protocol (monitor.app.v1)", click: () => void shell.openExternal("https://monitorcli.dev/contracts/app-protocol-v1") },
        { label: "Repository", click: () => void shell.openExternal("https://github.com/abdul-hamid-achik/monitor") },
      ],
    },
  ];
  Menu.setApplicationMenu(Menu.buildFromTemplate(template));
}

/**
 * Smoke mode boots the real window hidden, lets the renderer connect to the
 * local monitor and call it, prints one JSON line, and exits — the CI proof
 * that main, preload, renderer and `monitor serve --stdio` are wired.
 */
function installSmokeHarness() {
  const { ipcMain } = require("electron");
  /** @param {number} code @param {Record<string, unknown>} payload */
  const finish = (code, payload) => {
    process.stdout.write(`${JSON.stringify(payload)}\n`);
    connections?.closeAll();
    setTimeout(() => app.exit(code), 200);
  };
  const timer = setTimeout(() => finish(1, { ok: false, reason: "renderer did not report ready within 40s" }), 40_000);
  ipcMain.on("smoke:ready", (_event, payload) => {
    clearTimeout(timer);
    const ok = Boolean(payload?.ok);
    finish(ok ? 0 : 1, {
      ok,
      reason: payload?.reason ?? null,
      checks: payload?.checks ?? null,
      appVersion: app.getVersion(),
      electron: process.versions.electron,
    });
  });
}

/** Walks the views and writes one PNG per view into captureDir. */
async function runCapture() {
  const sleep = (/** @type {number} */ ms) => new Promise((r) => setTimeout(r, ms));
  fs.mkdirSync(/** @type {string} */ (captureDir), { recursive: true });
  for (let i = 0; i < 300 && connections.list().find((c) => c.id === "local")?.state !== "ready"; i++) await sleep(100);
  await sleep(1500);
  const views = ["issues", "issue", "host", "hot", "launch", "logs", "incidents", "doctor", "settings"];
  for (const [i, view] of views.entries()) {
    if (view === "issue") {
      const res = await connections.get("local").call("issues.list", { statuses: ["open"], limit: 1 }).catch(() => null);
      const id = res?.items?.[0]?.id;
      if (!id) continue;
      send("app:open-issue", { connId: "local", id });
    } else {
      send("menu:navigate", { view });
    }
    await sleep(view === "host" ? 6000 : view === "doctor" ? 6000 : 2500);
    const win = getWindow();
    if (!win) break;
    const image = await win.webContents.capturePage();
    fs.writeFileSync(path.join(/** @type {string} */ (captureDir), `${String(i + 1).padStart(2, "0")}-${view}.png`), image.toPNG());
  }
  launches?.stopAll();
  connections?.closeAll();
  setTimeout(() => app.exit(0), 300);
}

if (!app.requestSingleInstanceLock() && !smokeMode && !captureDir) {
  app.quit();
} else {
  app.on("second-instance", () => {
    const win = getWindow();
    if (!win) return;
    if (win.isMinimized()) win.restore();
    win.focus();
  });

  void app.whenReady().then(() => {
    app.setName("Monitor");
    app.setAboutPanelOptions({
      applicationName: "Monitor Desktop",
      applicationVersion: app.getVersion(),
      copyright: "MIT © Abdul Hamid Achik",
      website: "https://monitorcli.dev",
    });
    // The renderer needs no browser permission (camera, geolocation, ...).
    session.defaultSession.setPermissionRequestHandler((_wc, _permission, callback) => callback(false));

    settings = new SettingsStore(path.join(app.getPath("userData"), "settings.json"));
    connections = new ConnectionManager({
      localBinary: async () => (await monitorBinary())?.path ?? null,
      env: childEnv,
      onState: (snapshot) => send("conn:state", snapshot),
      onNotification: (connId, method, params) => {
        send("conn:notification", { connId, method, params });
        if (method === "issue.event") maybeNotify(connId, params);
      },
    });
    launches = new LaunchManager({
      monitorBinary: async () => (await monitorBinary())?.path ?? null,
      env: childEnv,
      emit: send,
    });
    registerIpc({ settings, connections, launches, getWindow, rendererURL, binaryInfo: monitorBinary, env: childEnv });

    connections.sync(settings.get().connections);
    for (const c of connections.list()) {
      // Local connects at once; remote hosts connect when the user picks them.
      if (c.kind === "local") void connections.get(c.id).connect();
    }

    buildMenu();
    if (smokeMode) installSmokeHarness();
    mainWindow = createMainWindow();
    if (captureDir) mainWindow.webContents.once("did-finish-load", () => void runCapture());
    app.on("activate", () => {
      if (!getWindow()) mainWindow = createMainWindow();
    });
  });

  app.on("window-all-closed", () => {
    if (!isMac) app.quit();
  });
  app.on("before-quit", () => {
    launches?.stopAll();
    connections?.closeAll();
  });
}

process.on("uncaughtException", (error) => {
  send("app:error", { message: String(error?.message ?? error) });
});
