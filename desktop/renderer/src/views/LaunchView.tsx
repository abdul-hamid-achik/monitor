// Launch: run a command under `monitor run --name <name> -- <cmd>` from the
// app. Its output streams here; its crashes become issues the moment they
// print, with no SDK; and Hot lines can profile it by name.

import { useEffect, useRef, useState } from "react";
import { bridge, type LocalLaunch } from "../api";
import { Empty, Icon, Notice } from "../components/ui";
import { timeAgo } from "../format";
import { navigate, toast, useStore } from "../store";

interface Line {
  stream: string;
  text: string;
}

export function LaunchView() {
  const settings = useStore((s) => s.settings);
  const connInfo = useStore((s) => s.connections.find((c) => c.id === s.activeConn));
  const [launches, setLaunches] = useState<LocalLaunch[]>([]);
  const [output, setOutput] = useState<Record<string, Line[]>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const [name, setName] = useState("app");
  const [cwd, setCwd] = useState("");
  const [command, setCommand] = useState("");
  const [inspect, setInspect] = useState(false);
  const [scanStdout, setScanStdout] = useState(false);
  const termRef = useRef<HTMLPreElement>(null);

  const refresh = async () => {
    const list = await bridge.launches.list();
    setLaunches(list);
    setOutput((prev) => {
      const next = { ...prev };
      for (const l of list) if (!next[l.id]) next[l.id] = l.lines ?? [];
      return next;
    });
    return list;
  };

  useEffect(() => {
    void refresh().then((list) => {
      if (list.length && !selected) setSelected(list[list.length - 1].id);
    });
    const offs = [
      bridge.on("launch:output", ({ id, stream, text }) => {
        setOutput((prev) => {
          const lines = [...(prev[id] ?? []), { stream, text }];
          return { ...prev, [id]: lines.length > 2000 ? lines.slice(-2000) : lines };
        });
      }),
      bridge.on("launch:started", (l: LocalLaunch) => {
        void refresh();
        setSelected(l.id);
      }),
      bridge.on("launch:exit", () => void refresh()),
    ];
    return () => offs.forEach((off) => off());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const el = termRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [output, selected]);

  const start = async () => {
    try {
      const launch = await bridge.launches.start({ cwd, command, name, inspect, scanStdout });
      setSelected(launch.id);
    } catch (err) {
      toast(String((err as Error).message), "error");
    }
  };

  const chooseDir = async () => {
    const dir = await bridge.dialogs.chooseDirectory(cwd || undefined);
    if (dir) {
      setCwd(dir);
      if (name === "app") setName(dir.split("/").pop()?.replace(/[^A-Za-z0-9._-]/g, "-").slice(0, 64) || "app");
    }
  };

  const current = launches.find((l) => l.id === selected);
  const lines = (selected && output[selected]) || [];

  if (connInfo && connInfo.kind !== "local") {
    return (
      <div className="page">
        <header className="page-header">
          <h1 className="page-title">Launch</h1>
        </header>
        <Empty title="Launch runs commands on this Mac">
          On {connInfo.name}, start it there with <code>monitor run --name api -- &lt;cmd&gt;</code> — its issues show up here live.
        </Empty>
      </div>
    );
  }

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Launch</h1>
        <span className="page-sub">monitor run -- &lt;cmd&gt;: crashes become issues as they print, no SDK</span>
      </header>

      <section className="card">
        <div className="card-body stack" style={{ gap: 10 }}>
          <div className="row-flex">
            <label className="field" style={{ width: 160 }}>
              Name
              <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            <label className="field" style={{ flex: 1 }}>
              Directory
              <div className="row-flex">
                <input className="input wide mono" value={cwd} placeholder="/path/to/project" onChange={(e) => setCwd(e.target.value)} />
                <button className="btn" onClick={chooseDir}>
                  Choose…
                </button>
              </div>
            </label>
          </div>
          <label className="field">
            Command
            <input
              className="input wide mono"
              value={command}
              placeholder="node server.js   ·   bun run dev   ·   python app.py   ·   go run ."
              onChange={(e) => setCommand(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void start()}
            />
          </label>
          <div className="row-flex">
            <label className="check" title="monitor run --inspect: lets Hot lines profile Node/Deno live">
              <input type="checkbox" checked={inspect} onChange={(e) => setInspect(e.target.checked)} />
              Enable live hot lines (Node/Deno <span className="mono">--inspect</span>)
            </label>
            <label className="check" title="monitor run --scan both: for loggers that print errors to stdout">
              <input type="checkbox" checked={scanStdout} onChange={(e) => setScanStdout(e.target.checked)} />
              Also scan stdout (zap, pino, Rails)
            </label>
            <div className="spacer" />
            <button className="btn primary" disabled={!cwd || !command.trim()} onClick={start}>
              <Icon name="launch" /> Run
            </button>
          </div>
          {settings?.recentLaunches.length ? (
            <div className="row-flex wrap">
              <span className="faint">Recent:</span>
              {settings.recentLaunches.map((r) => (
                <button
                  key={`${r.cwd}|${r.command}`}
                  className="chip"
                  style={{ cursor: "pointer", background: "transparent" }}
                  onClick={() => {
                    setCwd(r.cwd);
                    setName(r.name);
                    setCommand(r.command);
                    setInspect(r.inspect);
                    setScanStdout(r.scanStdout);
                  }}
                >
                  {r.name}: {r.command}
                  {r.inspect ? " · inspect" : ""}
                </button>
              ))}
            </div>
          ) : null}
        </div>
      </section>

      {launches.length ? (
        <div className="row-flex wrap" style={{ margin: "14px 0 8px" }}>
          {launches.map((l) => (
            <button key={l.id} className={`nav-item ${selected === l.id ? "active" : ""}`} style={{ width: "auto" }} onClick={() => setSelected(l.id)}>
              <span className={`dot ${l.running ? "ready" : l.exitCode === 0 ? "" : "error"}`} />
              {l.name}
              <span className="count">{l.running ? timeAgo(l.startedAt) : l.signal ?? `exit ${l.exitCode}`}</span>
            </button>
          ))}
        </div>
      ) : null}

      {current ? (
        <section className="card">
          <div className="card-head">
            <span className="mono untrusted">{current.command}</span>
            <span className="faint mono">{current.cwd}</span>
            <div className="spacer" />
            <button className="btn small" onClick={() => navigate({ view: "issues" })}>
              <Icon name="issues" /> Issues
            </button>
            {current.running ? (
              <button className="btn small danger" onClick={() => bridge.launches.stop(current.id)}>
                <Icon name="stop" /> Stop
              </button>
            ) : (
              <button
                className="btn small ghost"
                onClick={async () => {
                  await bridge.launches.remove(current.id);
                  setSelected(null);
                  void refresh();
                }}
              >
                Clear
              </button>
            )}
          </div>
          <div className="card-body">
            <pre ref={termRef} className="terminal">
              {lines.map((l, i) => (
                <div key={i} className={l.text.startsWith("monitor > ") ? "monitor-line" : l.stream === "stderr" ? "stderr" : ""}>
                  {l.text || " "}
                </div>
              ))}
            </pre>
          </div>
        </section>
      ) : (
        <div style={{ marginTop: 14 }}>
          <Notice
            kind="info"
            title="The command runs exactly as typed — no shell: quotes group words, nothing is expanded."
            recovery="To use shell features, run them through one: sh -c 'npm run build && node dist/server.js'"
          />
        </div>
      )}
    </div>
  );
}
