// Main-process units: the RPC client, connection argv, settings
// validation, editor paths and launch parsing — the trust boundary where a
// mistake becomes a wedged child, an ssh option injection or a path escape.
const { describe, expect, test } = require("bun:test");
const { EventEmitter } = require("node:events");
const { PassThrough } = require("node:stream");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");

const { RpcClient, RpcError, lineSplitter, timeoutFor } = require("../main/rpc");
const { spawnArgs, explainExit, Connection, ALLOWED_METHODS } = require("../main/connections");
const { normalize, validateConnection, SettingsStore } = require("../main/settings");
const { culpritPath, editorURL } = require("../main/editor");
const { splitCommand, stripAnsi, validateLaunch, monitorRunArgs } = require("../main/launches");
const { resolveMonitorBinary } = require("../main/binary");
const { mergePath } = require("../main/env");

describe("RpcClient", () => {
  test("matches responses to calls by id, out of order", async () => {
    const sent = [];
    const rpc = new RpcClient({ write: (l) => sent.push(JSON.parse(l)) });
    const a = rpc.call("issues.list", { limit: 1 });
    const b = rpc.call("ping");
    expect(sent.map((m) => m.method)).toEqual(["issues.list", "ping"]);
    rpc.handleLine(JSON.stringify({ jsonrpc: "2.0", id: sent[1].id, result: { pong: true } }));
    rpc.handleLine(JSON.stringify({ jsonrpc: "2.0", id: sent[0].id, result: { items: [] } }));
    expect(await b).toEqual({ pong: true });
    expect(await a).toEqual({ items: [] });
  });

  test("keeps the protocol's error code and data", async () => {
    const sent = [];
    const rpc = new RpcClient({ write: (l) => sent.push(JSON.parse(l)) });
    const p = rpc.call("process.kill", { pid: 1 });
    rpc.handleLine(JSON.stringify({ jsonrpc: "2.0", id: sent[0].id, error: { code: -32006, message: "refused", data: { has_protected: true } } }));
    const err = await p.catch((e) => e);
    expect(err).toBeInstanceOf(RpcError);
    expect(err.code).toBe(-32006);
    expect(err.data).toEqual({ has_protected: true });
  });

  test("routes notifications and reports garbage instead of throwing", () => {
    const notes = [];
    const garbage = [];
    const rpc = new RpcClient({ write: () => {}, onNotification: (m, p) => notes.push([m, p]), onGarbage: (l) => garbage.push(l) });
    rpc.handleLine('{"jsonrpc":"2.0","method":"issue.event","params":{"type":"new"}}');
    rpc.handleLine("Debugger listening on ws://127.0.0.1:9229/x");
    rpc.handleLine("");
    expect(notes).toEqual([["issue.event", { type: "new" }]]);
    expect(garbage).toHaveLength(1);
  });

  test("close rejects pending calls and refuses new ones", async () => {
    const rpc = new RpcClient({ write: () => {} });
    const p = rpc.call("doctor");
    rpc.close(new Error("server exited"));
    expect((await p.catch((e) => e)).message).toBe("server exited");
    expect((await rpc.call("ping").catch((e) => e)).message).toBe("connection closed");
  });

  test("times out a call that is never answered", async () => {
    const rpc = new RpcClient({ write: () => {} });
    const err = await rpc.call("ping", undefined, 20).catch((e) => e);
    expect(err.message).toContain("timed out");
    expect(rpc.pending.size).toBe(0);
  });

  test("lineSplitter holds a partial line until its newline", () => {
    const lines = [];
    const feed = lineSplitter((l) => lines.push(l));
    feed('{"a":');
    feed('1}\n{"b"');
    feed(":2}\n");
    expect(lines).toEqual(['{"a":1}', '{"b":2}']);
  });

  test("profile captures get their duration plus margin", () => {
    expect(timeoutFor("profile.capture", { duration_seconds: 20 })).toBe(80_000);
    expect(timeoutFor("ping")).toBe(30_000);
  });
});

describe("connections", () => {
  test("local spawns the binary with serve --stdio", () => {
    expect(spawnArgs({ id: "local", name: "x", kind: "local" }, "/bin/monitor")).toEqual({ command: "/bin/monitor", args: ["serve", "--stdio"] });
    expect(spawnArgs({ id: "local", name: "x", kind: "local", readOnly: true }, "/bin/monitor").args).toEqual(["serve", "--stdio", "--read-only"]);
  });

  test("ssh ends option parsing before the host and never prompts", () => {
    const { command, args } = spawnArgs({ id: "s", name: "s", kind: "ssh", host: "dev-box", monitorPath: "~/go/bin/monitor", readOnly: true }, null);
    expect(command).toBe("ssh");
    expect(args).toContain("BatchMode=yes");
    const dd = args.indexOf("--");
    expect(dd).toBeGreaterThan(0);
    expect(args.slice(dd + 1)).toEqual(["dev-box", "~/go/bin/monitor", "serve", "--stdio", "--read-only"]);
  });

  test("explains the failures people actually hit", () => {
    const ssh = { id: "s", name: "s", kind: "ssh", host: "box" };
    expect(explainExit(ssh, 127, ["bash: monitor: command not found"])).toContain("not found on box");
    expect(explainExit(ssh, 255, ["Permission denied (publickey)."])).toContain("BatchMode");
    expect(explainExit(ssh, 1, ['Error: unknown command "serve" for "monitor"'])).toContain("too old");
  });

  test("only protocol methods pass the main-process allowlist", async () => {
    expect(ALLOWED_METHODS.has("issues.list")).toBe(true);
    const conn = new Connection({ id: "local", name: "x", kind: "local" }, { localBinary: async () => null, env: async () => ({}), onState: () => {}, onNotification: () => {} });
    expect((await conn.call("shell.exec", {}).catch((e) => e)).code).toBe(-32601);
  });

  test("a fake server: hello makes the connection ready and subscribes to issues", async () => {
    const stdin = new PassThrough();
    const stdout = new PassThrough();
    const stderr = new PassThrough();
    const child = Object.assign(new EventEmitter(), { stdin, stdout, stderr, exitCode: null, kill() {} });
    const states = [];
    const written = [];
    stdin.on("data", (d) => written.push(...String(d).trim().split("\n").map((l) => JSON.parse(l))));
    const conn = new Connection(
      { id: "local", name: "This Mac", kind: "local" },
      {
        localBinary: async () => "/bin/monitor",
        env: async () => ({}),
        onState: (c) => states.push(c.state),
        onNotification: () => {},
        spawn: () => child,
      },
    );
    await conn.connect();
    stdout.write('{"jsonrpc":"2.0","method":"hello","params":{"protocol":"monitor.app.v1","monitor_version":"t","topics":["issues","host"],"methods":[]}}\n');
    await new Promise((r) => setTimeout(r, 10));
    expect(states).toEqual(["connecting", "ready"]);
    expect(written[0]).toMatchObject({ method: "subscribe", params: { topics: ["issues"] } });
    conn.close();
    child.emit("exit", 0);
    expect(conn.state).toBe("closed");
  });

  test("a wrong protocol is an error, not a retry loop", async () => {
    const stdout = new PassThrough();
    const child = Object.assign(new EventEmitter(), { stdin: new PassThrough(), stdout, stderr: new PassThrough(), exitCode: null, kill() {} });
    const conn = new Connection(
      { id: "local", name: "x", kind: "local" },
      { localBinary: async () => "/m", env: async () => ({}), onState: () => {}, onNotification: () => {}, spawn: () => child },
    );
    await conn.connect();
    stdout.write('{"jsonrpc":"2.0","method":"hello","params":{"protocol":"other.v9"}}\n');
    await new Promise((r) => setTimeout(r, 10));
    expect(conn.state).toBe("error");
    expect(conn.error).toContain("monitor.app.v1");
    expect(conn.retryTimer).toBeNull();
  });
});

describe("settings", () => {
  test("rejects an ssh host that would be read as an option", () => {
    expect(() => validateConnection({ id: "x", kind: "ssh", host: "-oProxyCommand=touch /tmp/pwned" })).toThrow();
    expect(() => validateConnection({ id: "x", kind: "ssh", host: "box; rm -rf ~" })).toThrow();
    expect(validateConnection({ id: "x", kind: "ssh", host: "me@dev-box.local" }).host).toBe("me@dev-box.local");
  });

  test("rejects a remote path with shell metacharacters", () => {
    expect(() => validateConnection({ id: "x", kind: "ssh", host: "box", monitorPath: "monitor; curl evil" })).toThrow();
    expect(validateConnection({ id: "x", kind: "ssh", host: "box", monitorPath: "~/go/bin/monitor" }).monitorPath).toBe("~/go/bin/monitor");
  });

  test("a hand-edited file keeps a local connection and drops bad entries", () => {
    const s = normalize({ connections: [{ id: "bad id!", kind: "ssh", host: "x" }, { id: "ok", kind: "ssh", host: "box" }], editor: "emacs" });
    expect(s.connections.map((c) => c.id)).toEqual(["local", "ok"]);
    expect(s.editor).toBe("vscode");
  });

  test("saves atomically with private permissions", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "monitor-desktop-test-"));
    const store = new SettingsStore(path.join(dir, "settings.json"));
    store.update({ notifications: false });
    const file = path.join(dir, "settings.json");
    expect(JSON.parse(fs.readFileSync(file, "utf8")).notifications).toBe(false);
    expect(fs.statSync(file).mode & 0o777).toBe(0o600);
    expect(new SettingsStore(file).get().notifications).toBe(false);
  });
});

describe("editor", () => {
  test("joins the recorded root and the culprit file", () => {
    expect(culpritPath("/repo", "src/app.js")).toBe("/repo/src/app.js");
    expect(culpritPath("/repo", "/abs/elsewhere.js")).toBe("/abs/elsewhere.js");
  });

  test("refuses paths that escape the root or carry control characters", () => {
    expect(culpritPath("/repo", "../../etc/passwd")).toBeNull();
    expect(culpritPath("/repo", "src/a.js\nrm")).toBeNull();
    expect(culpritPath("", "src/a.js")).toBeNull();
  });

  test("builds editor URLs, remote ones through Remote-SSH", () => {
    expect(editorURL({ editor: "vscode", absPath: "/repo/src/a b.js", line: 42 })).toBe("vscode://file/repo/src/a%20b.js:42");
    expect(editorURL({ editor: "vscode", absPath: "/srv/app.js", line: 7, sshHost: "box" })).toBe("vscode://vscode-remote/ssh-remote+box/srv/app.js:7");
    expect(editorURL({ editor: "zed", absPath: "/srv/app.js", line: 7, sshHost: "box" })).toBeNull();
    expect(editorURL({ editor: "idea", absPath: "/r/a.go", line: 3 })).toBe("idea://open?file=%2Fr%2Fa.go&line=3");
  });
});

describe("launches", () => {
  test("splits a command line without a shell", () => {
    expect(splitCommand(`node server.js --port 3000`)).toEqual(["node", "server.js", "--port", "3000"]);
    expect(splitCommand(`sh -c 'npm run build && node dist/server.js'`)).toEqual(["sh", "-c", "npm run build && node dist/server.js"]);
    expect(splitCommand(`echo "a \\"b\\"" c\\ d`)).toEqual(["echo", 'a "b"', "c d"]);
    expect(splitCommand(`node $HOME/x.js; rm -rf /`)).toEqual(["node", "$HOME/x.js;", "rm", "-rf", "/"]);
    expect(() => splitCommand(`node 'unterminated`)).toThrow("unterminated");
  });

  test("monitor run flags go before the --", () => {
    expect(monitorRunArgs({ name: "api", argv: ["node", "s.js"], inspect: true, scanStdout: true })).toEqual([
      "run", "--name", "api", "--scan", "both", "--inspect", "--", "node", "s.js",
    ]);
    expect(monitorRunArgs({ name: "api", argv: ["--inspect"], inspect: false })).toEqual(["run", "--name", "api", "--", "--inspect"]);
  });

  test("validates name and directory", () => {
    expect(() => validateLaunch({ name: "../x", cwd: os.tmpdir(), command: "node a.js" })).toThrow();
    expect(() => validateLaunch({ name: "api", cwd: "relative", command: "node a.js" })).toThrow();
    expect(validateLaunch({ name: "api", cwd: os.tmpdir(), command: "node a.js" }).argv).toEqual(["node", "a.js"]);
  });

  test("strips ANSI colour from captured output", () => {
    expect(stripAnsi("\u001b[31mError\u001b[0m: boom")).toBe("Error: boom");
  });
});

describe("binary + env", () => {
  test("prefers the settings override, then the repo build, then PATH", () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "monitor-bin-"));
    const repo = path.join(dir, "repo");
    const pathDir = path.join(dir, "path");
    fs.mkdirSync(path.join(repo, "bin"), { recursive: true });
    fs.mkdirSync(pathDir);
    for (const f of [path.join(repo, "bin", "monitor"), path.join(pathDir, "monitor")]) fs.writeFileSync(f, "#!/bin/sh\n", { mode: 0o755 });
    expect(resolveMonitorBinary({ repoRoot: repo, pathList: pathDir, packaged: false })?.source).toBe("repo");
    expect(resolveMonitorBinary({ repoRoot: path.join(dir, "none"), pathList: pathDir, packaged: false })?.source).toBe("path");
    expect(resolveMonitorBinary({ override: path.join(dir, "missing"), repoRoot: repo, pathList: pathDir })).toBeNull();
  });

  test("mergePath keeps the first occurrence of each directory", () => {
    expect(mergePath("/a:/b", "/b:/c", "", "/a:/d")).toBe("/a:/b:/c:/d");
  });
});

describe("chalupa connections", () => {
  test("validates a managed env name or an absolute chalupa.yml, never both", () => {
    expect(validateConnection({ id: "c", kind: "chalupa", env: "gpu-dev" })).toEqual({ id: "c", name: "c", kind: "chalupa", env: "gpu-dev", readOnly: true });
    expect(validateConnection({ id: "c", kind: "chalupa", config: "/Users/me/app/chalupa.yml" }).config).toBe("/Users/me/app/chalupa.yml");
    expect(() => validateConnection({ id: "c", kind: "chalupa", env: "--name=x" })).toThrow();
    expect(() => validateConnection({ id: "c", kind: "chalupa", env: "Bad_Name" })).toThrow();
    expect(() => validateConnection({ id: "c", kind: "chalupa", config: "relative/chalupa.yml" })).toThrow();
    expect(() => validateConnection({ id: "c", kind: "chalupa", config: "/etc/passwd" })).toThrow();
    expect(() => validateConnection({ id: "c", kind: "chalupa", env: "a", config: "/x/chalupa.yml" })).toThrow();
  });

  test("spawns Chalupa's stdio door, read-only by construction", () => {
    expect(spawnArgs({ id: "c", name: "c", kind: "chalupa", env: "gpu-dev", readOnly: true }, null)).toEqual({
      command: "chalupa",
      args: ["monitor", "serve", "--name", "gpu-dev"],
    });
    expect(spawnArgs({ id: "c", name: "c", kind: "chalupa", config: "/w/chalupa.yml" }, null).args).toEqual(["monitor", "serve", "--config", "/w/chalupa.yml"]);
  });

  test("explains Chalupa's exits", () => {
    const c = { id: "c", name: "c", kind: "chalupa", env: "gpu-dev" };
    expect(explainExit(c, 64, ["No managed host for gpu-dev. Run: chalupa up --name gpu-dev"])).toContain("No managed host");
    expect(explainExit(c, 1, ['Error: unknown command "serve" for "monitor"'])).toContain("pinned version");
    expect(explainExit({ ...c, env: undefined, config: "/w/chalupa.yml" }, 69, ["bad yaml"])).toContain("could not load /w/chalupa.yml");
  });
});
