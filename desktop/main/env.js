/**
 * The environment child processes run with.
 *
 * An app opened from Finder inherits launchd's minimal PATH
 * (/usr/bin:/bin:/usr/sbin:/sbin). `monitor serve` shells out to git,
 * codemap, vecgrep and fcheap, and `monitor run` launches node, bun, python
 * or go — all of which usually live in Homebrew, asdf or ~/go/bin. So the
 * PATH is read once from the user's login shell, the same way a terminal
 * gets it, and common tool directories are appended as a fallback.
 */
const { execFile } = require("node:child_process");
const os = require("node:os");
const path = require("node:path");

const FALLBACK_DIRS = [
  "/opt/homebrew/bin",
  "/opt/homebrew/sbin",
  "/usr/local/bin",
  path.join(os.homedir(), "go", "bin"),
  path.join(os.homedir(), ".local", "bin"),
  path.join(os.homedir(), ".bun", "bin"),
  path.join(os.homedir(), ".asdf", "shims"),
];

/** @type {Promise<string> | null} */
let cachedPath = null;

/**
 * Merges PATH lists, keeping the first occurrence of each directory.
 * @param {...(string | undefined)} lists
 */
function mergePath(...lists) {
  const seen = new Set();
  const out = [];
  for (const list of lists) {
    for (const dir of (list ?? "").split(path.delimiter)) {
      if (!dir || seen.has(dir)) continue;
      seen.add(dir);
      out.push(dir);
    }
  }
  return out.join(path.delimiter);
}

/**
 * Reads PATH from an interactive login shell, bounded to 5 s. A marker
 * fences the value off from anything the shell's rc files print.
 * @returns {Promise<string>}
 */
function loginShellPath() {
  const shell = process.env.SHELL || "/bin/zsh";
  const marker = "__MONITOR_DESKTOP_PATH__";
  return new Promise((resolve) => {
    execFile(
      shell,
      ["-ilc", `printf '%s%s%s' '${marker}' "$PATH" '${marker}'`],
      { timeout: 5000, env: { ...process.env, TERM: "dumb" } },
      (error, stdout) => {
        if (error) return resolve("");
        const match = String(stdout).split(marker);
        resolve(match.length >= 3 ? match[1] : "");
      },
    );
  });
}

/** @returns {Promise<string>} the PATH children should run with */
function resolvedPath() {
  if (!cachedPath) {
    cachedPath = loginShellPath().then((shellPath) =>
      mergePath(shellPath, process.env.PATH, FALLBACK_DIRS.join(path.delimiter)),
    );
  }
  return cachedPath;
}

/** @returns {Promise<NodeJS.ProcessEnv>} process.env with the resolved PATH */
async function childEnv() {
  return { ...process.env, PATH: await resolvedPath() };
}

module.exports = { childEnv, resolvedPath, mergePath, FALLBACK_DIRS };
