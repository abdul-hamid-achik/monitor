/**
 * Finding the `monitor` binary the local connection spawns.
 *
 * Order: an explicit path from settings, the binary bundled into the app
 * (Contents/Resources/bin/monitor), the repo's own bin/monitor in
 * development, then the first `monitor` on the resolved PATH. The bundled
 * binary wins over PATH so the app always talks to the protocol version it
 * was built against; `monitor doctor` reports a different `monitor` on PATH
 * (binaries.other_binaries).
 */
const fs = require("node:fs");
const path = require("node:path");

/**
 * @param {string} file
 * @returns {boolean}
 */
function isExecutable(file) {
  try {
    fs.accessSync(file, fs.constants.X_OK);
    return fs.statSync(file).isFile();
  } catch {
    return false;
  }
}

/**
 * @param {object} opts
 * @param {string} [opts.override]       settings.monitorPath
 * @param {string} [opts.resourcesPath]  process.resourcesPath when packaged
 * @param {boolean} [opts.packaged]
 * @param {string} opts.repoRoot         the desktop/ directory's parent in development
 * @param {string} opts.pathList         the resolved PATH
 * @returns {{path: string, source: "settings" | "bundled" | "repo" | "path"} | null}
 */
function resolveMonitorBinary({ override, resourcesPath, packaged, repoRoot, pathList }) {
  if (override && override.trim()) {
    const candidate = override.trim();
    return isExecutable(candidate) ? { path: candidate, source: "settings" } : null;
  }
  if (packaged && resourcesPath) {
    const bundled = path.join(resourcesPath, "bin", "monitor");
    if (isExecutable(bundled)) return { path: bundled, source: "bundled" };
  }
  const repoBinary = path.join(repoRoot, "bin", "monitor");
  if (!packaged && isExecutable(repoBinary)) return { path: repoBinary, source: "repo" };
  for (const dir of pathList.split(path.delimiter)) {
    if (!dir) continue;
    const candidate = path.join(dir, "monitor");
    if (isExecutable(candidate)) return { path: candidate, source: "path" };
  }
  return null;
}

module.exports = { resolveMonitorBinary, isExecutable };
