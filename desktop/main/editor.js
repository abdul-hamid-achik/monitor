/**
 * "Open in editor": the URL that opens root/file at a line in the user's
 * editor, locally or — for an issue recorded on an SSH host — through VS
 * Code / Cursor Remote-SSH on that host.
 *
 * Only these URL schemes are ever opened, and the path is validated first:
 * the file name comes from a stack trace, which is process output.
 */
const path = require("node:path");

/**
 * Joins the recorded root and a frame's file into an absolute path, refusing
 * anything that escapes the root or carries control characters.
 * @param {string} root
 * @param {string} file
 * @returns {string | null}
 */
function culpritPath(root, file) {
  if (typeof file !== "string" || !file || /[\u0000-\u001f]/.test(file)) return null;
  if (path.posix.isAbsolute(file)) return path.posix.normalize(file);
  if (typeof root !== "string" || !path.posix.isAbsolute(root)) return null;
  const joined = path.posix.normalize(path.posix.join(root, file));
  const rootNorm = path.posix.normalize(root).replace(/\/$/, "");
  if (joined !== rootNorm && !joined.startsWith(`${rootNorm}/`)) return null;
  return joined;
}

/** @param {string} p */
function encodePath(p) {
  return p.split("/").map(encodeURIComponent).join("/");
}

/**
 * @param {object} opts
 * @param {string} opts.editor        vscode | cursor | zed | idea
 * @param {string} opts.absPath
 * @param {number} [opts.line]
 * @param {string} [opts.sshHost]     set for a remote connection
 * @returns {string | null}
 */
function editorURL({ editor, absPath, line, sshHost }) {
  const ln = Number.isInteger(line) && line > 0 ? line : 1;
  const p = encodePath(absPath);
  if (sshHost) {
    if (editor === "vscode") return `vscode://vscode-remote/ssh-remote+${encodeURIComponent(sshHost)}${p}:${ln}`;
    if (editor === "cursor") return `cursor://vscode-remote/ssh-remote+${encodeURIComponent(sshHost)}${p}:${ln}`;
    return null;
  }
  switch (editor) {
    case "vscode":
      return `vscode://file${p}:${ln}`;
    case "cursor":
      return `cursor://file${p}:${ln}`;
    case "zed":
      return `zed://file${p}:${ln}`;
    case "idea":
      return `idea://open?file=${encodeURIComponent(absPath)}&line=${ln}`;
    default:
      return null;
  }
}

module.exports = { culpritPath, editorURL };
