// The SSH path end to end, without an sshd: a stand-in `ssh` consumes the
// options and the host exactly like OpenSSH, then hands the remaining words
// to a shell the way sshd runs a remote command. The real monitor binary
// (../../bin/monitor) answers on the other side, so this proves the argv the
// app builds reaches `monitor serve --stdio` intact — `~` expansion of the
// remote path included — and that --read-only travels with it.
const { afterAll, describe, expect, test } = require("bun:test");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { Connection } = require("../main/connections");

const monitor = path.resolve(__dirname, "..", "..", "bin", "monitor");
const haveMonitor = fs.existsSync(monitor);
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "monitor-desktop-remote-"));
const shim = path.join(tmp, "fake-ssh");
fs.writeFileSync(
  shim,
  `#!/bin/sh
# Options until "--", then the host, then the remote command words.
while [ "$#" -gt 0 ]; do
  case "$1" in
    --) shift; break ;;
    -o) shift 2 ;;
    -*) shift ;;
    *) break ;;
  esac
done
host="$1"; shift
[ "$host" = "fakehost" ] || { echo "ssh: Could not resolve hostname $host" >&2; exit 255; }
exec /bin/sh -c "$*"
`,
  { mode: 0o755 },
);
// A home with the binary at ~/bin/monitor, like ~/go/bin/monitor on a box.
const home = path.join(tmp, "home");
fs.mkdirSync(path.join(home, "bin"), { recursive: true });
if (haveMonitor) fs.symlinkSync(monitor, path.join(home, "bin", "monitor"));

afterAll(() => fs.rmSync(tmp, { recursive: true, force: true }));

/** @param {import("../main/settings").Connection} config */
function connect(config) {
  const states = [];
  const conn = new Connection(config, {
    localBinary: async () => null,
    env: async () => ({ ...process.env, HOME: home, MONITOR_ISSUES_STORE: path.join(tmp, "issues.veclite") }),
    onState: (c) => states.push(c.state),
    onNotification: () => {},
    spawn: (command, args, opts) => {
      expect(command).toBe("ssh");
      return spawn(shim, args, opts);
    },
  });
  return { conn, states };
}

async function until(fn, ms = 15_000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (fn()) return;
    await new Promise((r) => setTimeout(r, 25));
  }
  throw new Error("timed out");
}

describe.skipIf(!haveMonitor)("ssh connection (stand-in ssh, real monitor)", () => {
  test("reaches monitor serve through ~ and answers calls", async () => {
    const { conn } = connect({ id: "box", name: "box", kind: "ssh", host: "fakehost", monitorPath: "~/bin/monitor" });
    await conn.connect();
    await until(() => conn.state === "ready" || conn.state === "error");
    expect(conn.error).toBeNull();
    expect(conn.hello.protocol).toBe("monitor.app.v1");
    expect(await conn.call("ping")).toEqual({ pong: true });
    const list = await conn.call("issues.list", {});
    expect(list.items).toEqual([]);
    conn.close();
    await until(() => conn.state === "closed");
  });

  test("--read-only travels over ssh", async () => {
    const { conn } = connect({ id: "box", name: "box", kind: "ssh", host: "fakehost", monitorPath: "~/bin/monitor", readOnly: true });
    await conn.connect();
    await until(() => conn.state === "ready" || conn.state === "error");
    expect(conn.hello.read_only).toBe(true);
    const err = await conn.call("issues.set_status", { ids: ["ISS-1"], status: "resolved" }).catch((e) => e);
    expect(err.code).toBe(-32002);
    conn.close();
  });

  test("a missing remote binary explains itself", async () => {
    const { conn } = connect({ id: "box", name: "box", kind: "ssh", host: "fakehost", monitorPath: "~/nope/monitor" });
    await conn.connect();
    await until(() => conn.state === "error");
    expect(conn.error).toContain("not found on fakehost");
    conn.close();
  });

  test("an unreachable host explains itself", async () => {
    const { conn } = connect({ id: "box", name: "box", kind: "ssh", host: "nohost", monitorPath: "monitor" });
    await conn.connect();
    await until(() => conn.state === "error");
    expect(conn.error).toContain("ssh could not connect to nohost");
    conn.close();
  });
});
