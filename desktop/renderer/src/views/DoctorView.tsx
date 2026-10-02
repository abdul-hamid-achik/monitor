// Doctor: `monitor doctor --json` (docs/contracts/doctor-v1.md) for the
// active host — which ecosystem tools are present, whether codemap and
// vecgrep are actually usable, and which monitor binaries are on PATH.

import { useEffect, useState } from "react";
import type { ProjectSummary } from "../api";
import { ErrorNotice, Icon, Notice, useRpc } from "../components/ui";
import { useStore } from "../store";

interface ToolStatus {
  available: boolean;
  version?: string;
  path?: string;
  note?: string;
}
interface Health {
  state: string;
  detail?: string;
  recovery?: string;
  version?: string;
  path?: string;
}
interface BinaryInfo {
  name: string;
  available: boolean;
  path?: string;
  version?: string;
  all_paths?: string[];
  shadowed?: boolean;
  warning?: string;
}
type DoctorReport = Record<string, ToolStatus> & {
  code_intel: { codemap: Health; vecgrep: Health };
  binaries: { self: { version: string; path?: string }; other_binaries: BinaryInfo[] };
};

const TOOLS = ["codemap", "vecgrep", "fcheap", "glyphrun", "cairntrace", "tinyvault", "vidtrace", "veclite", "tmux"];
const WHY: Record<string, string> = {
  codemap: "function ranges, blast radius and tests on the issue page",
  vecgrep: "culprit inferred from the message when there is no stack (keyword mode only)",
  fcheap: "archives incident bundles",
  glyphrun: "behavioral specs",
  cairntrace: "browser specs that call monitor",
  tinyvault: "monitor vault (secrets for runs)",
  vidtrace: "screen recordings for investigations",
  veclite: "embedded store (built in)",
  tmux: "service windows",
};

export function DoctorView() {
  const conn = useStore((s) => s.connections.find((c) => c.id === s.activeConn));
  const app = useStore((s) => s.appInfo);
  const projects = useRpc<{ projects: ProjectSummary[] }>("projects.list", undefined, []);
  const withRoots = (projects.data?.projects ?? []).filter((p) => p.root);
  const [dir, setDir] = useState("");
  useEffect(() => {
    if (!dir && withRoots.length) setDir(withRoots[0].root!);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [withRoots.length]);
  const doc = useRpc<DoctorReport>("doctor", dir ? { dir } : undefined, [dir], { enabled: projects.data !== null || Boolean(projects.error) });
  const d = doc.data;
  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Doctor</h1>
        <span className="page-sub">{conn?.hello ? `${conn.hello.hostname ?? conn.name} · monitor ${conn.hello.monitor_version}` : ""}</span>
        <div className="spacer" />
        {withRoots.length ? (
          <select className="select" value={dir} onChange={(e) => setDir(e.target.value)} title="codemap and vecgrep are checked against this checkout">
            {withRoots.map((p) => (
              <option key={p.project} value={p.root}>
                {p.project} — {p.root}
              </option>
            ))}
          </select>
        ) : null}
        <button className="btn ghost" onClick={() => doc.reload()} title="Run again">
          <Icon name="refresh" />
        </button>
      </header>
      <ErrorNotice error={doc.error} />
      {!d && doc.loading ? <div className="loading">Probing the ecosystem…</div> : null}
      {d ? (
        <div className="stack">
          <div className="grid-2">
            {(["codemap", "vecgrep"] as const).map((name) => {
              const h = d.code_intel?.[name];
              if (!h) return null;
              return (
                <section className="card" key={name}>
                  <div className="card-head">
                    <h2 className="card-title">{name}</h2>
                    <span className={`badge ${h.state === "ok" ? "ok" : h.state === "unavailable" ? "neutral" : "warn"}`}>{h.state}</span>
                    <div className="spacer" />
                    <span className="mono faint">{h.version}</span>
                  </div>
                  <div className="card-body">
                    <div className="muted">
                      {WHY[name]}
                      {dir ? <span className="faint mono"> · {dir}</span> : null}
                    </div>
                    {h.detail ? <div style={{ marginTop: 6 }}>{h.detail}</div> : null}
                    {h.recovery ? <div className="faint" style={{ marginTop: 4 }}>{h.recovery}</div> : null}
                  </div>
                </section>
              );
            })}
          </div>

          <section className="card">
            <div className="card-head">
              <h2 className="card-title">Ecosystem tools</h2>
            </div>
            <table className="table">
              <tbody>
                {TOOLS.filter((t) => d[t]).map((t) => {
                  const s = d[t] as ToolStatus;
                  return (
                    <tr key={t}>
                      <td className="nowrap">
                        <span className={`dot ${s.available ? "ready" : ""}`} style={{ marginRight: 8 }} />
                        <span className="mono">{t}</span>
                      </td>
                      <td className="muted">{WHY[t]}</td>
                      <td className="mono muted">{s.version ?? ""}</td>
                      <td className="mono faint">{s.available ? s.path : s.note ?? "not on PATH"}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </section>

          <section className="card">
            <div className="card-head">
              <h2 className="card-title">Binaries</h2>
            </div>
            <div className="card-body stack" style={{ gap: 8 }}>
              <div>
                <span className="mono">monitor {d.binaries?.self.version}</span> <span className="faint mono">{d.binaries?.self.path}</span>
                <span className="faint"> — the binary serving this connection</span>
              </div>
              {(d.binaries?.other_binaries ?? [])
                .filter((b) => b.shadowed || b.warning)
                .map((b) => (
                  <Notice key={`${b.name}${b.path}`} title={`${b.name}: ${b.warning ?? "shadowed on PATH"}`} recovery={(b.all_paths ?? [b.path]).join("  ·  ")} />
                ))}
            </div>
          </section>

          {app ? (
            <section className="card">
              <div className="card-head">
                <h2 className="card-title">This app</h2>
              </div>
              <div className="card-body">
                <dl className="kv">
                  <dt>Monitor Desktop</dt>
                  <dd className="mono">{app.version}</dd>
                  <dt>Electron</dt>
                  <dd className="mono">{app.electron}</dd>
                  <dt>Local monitor</dt>
                  <dd className="mono">{app.monitor ? `${app.monitor.path} (${app.monitor.source})` : "not found"}</dd>
                </dl>
              </div>
            </section>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
