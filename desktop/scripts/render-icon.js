// Renders build/icon.svg to build/icon.png (1024², transparent corners) with
// Electron's offscreen renderer: `electron scripts/render-icon.js`.
const fs = require("node:fs");
const path = require("node:path");
const { app, BrowserWindow } = require("electron");

app.disableHardwareAcceleration();
app.whenReady().then(async () => {
  const svg = fs.readFileSync(path.join(__dirname, "..", "build", "icon.svg"), "utf8");
  const win = new BrowserWindow({
    width: 1024,
    height: 1024,
    show: false,
    transparent: true,
    frame: false,
    useContentSize: true,
    webPreferences: { offscreen: true },
  });
  win.webContents.setFrameRate(1);
  const html = `<html><body style="margin:0;background:transparent">${svg}</body></html>`;
  await win.loadURL(`data:text/html;charset=utf-8,${encodeURIComponent(html)}`);
  await new Promise((r) => setTimeout(r, 500));
  const image = await win.webContents.capturePage({ x: 0, y: 0, width: 1024, height: 1024 });
  const out = path.join(__dirname, "..", "build", "icon.png");
  fs.writeFileSync(out, image.resize({ width: 1024, height: 1024 }).toPNG());
  console.log(`wrote ${out} ${JSON.stringify(image.getSize())}`);
  app.exit(0);
});
