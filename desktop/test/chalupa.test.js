// The Chalupa door end to end, without a droplet: a stand-in `chalupa`
// implements the agreed contract of `chalupa monitor serve (--name ENV |
// --config PATH)` — stdin piped through, only protocol bytes on stdout, the
// remote side always --read-only, exit 64 with one stderr line for an
// unknown environment — over the real monitor binary.
const { afterAll, describe, expect, test } = require("bun:test");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { Connection } = require("../main/connections");

const monitor = path.resolve(__dirname, "..", "..", "bin", "monitor");
const haveMonitor = fs.existsSync(monitor);
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "monitor-desktop-chalupa-"));
const fake = path.join(tmp, "chalupa");
fs.writeFileSync(
  fake,
  `#!/bin/sh
[ "$1" = monitor ] && [ "$2" = serve ] || { echo "Unknown command: $*" >&2; exit 64; }
case "$3" in
  --name) [ "$4" = gpu-dev ] || { echo "No managed host for $4. Run: chalupa up --name $4" >&2; exit 64; } ;;
  --config) [ -f "$4" ] || { echo "config not found: $4" >&2; exit 69; } ;;
  *) echo "usage: chalupa monitor serve (--config PATH | --name ENV)" >&2; exit 64 ;;
esac
exec "${monitor}" serve --stdio --read-only
`,
  { mode: 0o755 },
);
afterAll(() => fs.rmSync(tmp, { recursive: true, force: true }));

/** @param {import("../main/settings").Connection} config */
function connect(config) {
  return new Connection(config, {
    localBinary: async () => null,
    env: async () => ({ ...process.env, MONITOR_ISSUES_STORE: path.join(tmp, "issues.veclite") }),
    onState: () => {},
    onNotification: () => {},
    spawn: (command, args, opts) => {
      expect(command).toBe("chalupa");
      return spawn(fake, args, opts);
    },
  });
}

async function until(fn, ms = 15_000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    if (fn()) return;
    await new Promise((r) => setTimeout(r, 25));
  }
  throw new Error("timed out");
}

describe.skipIf(!haveMonitor)("chalupa connection (stand-in chalupa, real monitor)", () => {
  test("a managed environment connects read-only and answers", async () => {
    const conn = connect({ id: "c", name: "gpu-dev", kind: "chalupa", env: "gpu-dev", readOnly: true });
    await conn.connect();
    await until(() => conn.state === "ready" || conn.state === "error");
    expect(conn.error).toBeNull();
    expect(conn.hello.read_only).toBe(true);
    expect(await conn.call("ping")).toEqual({ pong: true });
    const err = await conn.call("process.kill", { pid: 1, confirm: true }).catch((e) => e);
    expect(err.code).toBe(-32002);
    conn.close();
  });

  test("an unknown environment shows Chalupa's own reason", async () => {
    const conn = connect({ id: "c", name: "x", kind: "chalupa", env: "nope", readOnly: true });
    await conn.connect();
    await until(() => conn.state === "error");
    expect(conn.error).toContain("No managed host for nope");
    conn.close();
  });

  test("a BYOC stack connects through its chalupa.yml", async () => {
    const yml = path.join(tmp, "chalupa.yml");
    fs.writeFileSync(yml, "name: shop\n");
    const conn = connect({ id: "c", name: "shop", kind: "chalupa", config: yml, readOnly: true });
    await conn.connect();
    await until(() => conn.state === "ready" || conn.state === "error");
    expect(conn.error).toBeNull();
    conn.close();
  });
});
