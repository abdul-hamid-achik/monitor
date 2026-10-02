// Hot lines: which LINE inside a function burns CPU (or holds heap, or parks
// goroutines), from a live capture of a running process or a saved
// .cpuprofile / pprof file — with the issues raised on each line beside it.

import { useEffect, useMemo, useState } from "react";
import { bridge, Codes, errorCode, type Heatmap, type HeatFunction, type LaunchInfo, type ProfileResult } from "../api";
import { HeatFrame } from "../components/code";
import { confirmedRpc, ErrorNotice, Icon, Notice, rpc, useRpc } from "../components/ui";
import { location, timeAgo } from "../format";
import { onNotification, toast, useStore } from "../store";

type ProfileType = "cpu" | "heap" | "heap-alloc" | "goroutine";

export function HotView({ initialPid }: { initialPid?: number }) {
  const conn = useStore((s) => s.activeConn);
  const connInfo = useStore((s) => s.connections.find((c) => c.id === s.activeConn));
  const readOnly = connInfo?.hello?.read_only ?? false;
  const [pid, setPid] = useState(initialPid ? String(initialPid) : "");
  const [type, setType] = useState<ProfileType>("cpu");
  const [seconds, setSeconds] = useState(5);
  const [pprofAddr, setPprofAddr] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ heatmap: Heatmap; label: string } | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const launches = useRpc<{ launches: LaunchInfo[] }>("launches.list", undefined, []);

  useEffect(() => {
    if (initialPid) setPid(String(initialPid));
  }, [initialPid]);
  useEffect(() => {
    setResult(null);
    setError(null);
  }, [conn]);
  useEffect(() => {
    const off = bridge.on("menu:open-profile", () => void openFile());
    return off;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn]);
  // A new launch registers itself; refresh the list when issues move too.
  useEffect(() => onNotification(conn, (m) => m === "issue.event" && launches.reload()), [conn, launches.reload]);

  const show = (heatmap: Heatmap, label: string) => {
    setResult({ heatmap, label });
    setError(null);
    const hottest = [...heatmap.functions].sort((a, b) => b.self_pct - a.self_pct)[0];
    setSelected(hottest ? key(hottest) : null);
  };

  const capture = async (targetPid: number, label?: string) => {
    setBusy(true);
    setError(null);
    try {
      const res = await confirmedRpc<ProfileResult>(
        "profile.capture",
        { pid: targetPid, type, duration_seconds: type === "cpu" ? seconds : undefined, pprof_addr: pprofAddr || undefined },
        {
          title: `Profile pid ${targetPid}${label ? ` (${label})` : ""}?`,
          body: (
            <>
              Samples the process{type === "cpu" ? ` for ${seconds}s` : ""}
              {connInfo?.kind === "ssh" ? ` on ${connInfo.name}` : ""}. Node/Deno need to have been started with --inspect, Go needs
              net/http/pprof; otherwise macOS sample gives function-level results. Monitor never attaches to a process that was not
              started for it.
            </>
          ),
          confirmLabel: "Capture",
        },
      );
      if (res) show(res.heatmap, `${res.target.name ?? "pid"} ${res.target.pid} · ${res.target.runtime ?? res.heatmap.runtime} · ${res.method}`);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const openFile = async () => {
    if (connInfo?.kind !== "local") {
      toast("Opening a saved profile works on the local connection: the file is read where monitor runs.", "info");
      return;
    }
    const path = await bridge.dialogs.chooseProfile();
    if (!path) return;
    setBusy(true);
    try {
      const hm = await rpc<Heatmap>("heatmap.file", { path, type: type === "cpu" ? undefined : type });
      show(hm, path.split("/").pop() ?? path);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const hm = result?.heatmap;
  const fn: HeatFunction | undefined = useMemo(() => hm?.functions.find((f) => key(f) === selected), [hm, selected]);
  const unavailable = errorCode(error) === Codes.Unavailable;
  const ambiguous = errorCode(error) === Codes.Ambiguous;
  const candidates = (error as { data?: { candidates?: { pid: number; name?: string; runtime?: string; main_script?: string }[] } })?.data?.candidates;
  const alive = (launches.data?.launches ?? []).filter((l) => l.alive);

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Hot lines</h1>
        <span className="page-sub">which line inside the function — and which of those lines also throw</span>
      </header>

      <div className="toolbar">
        <input className="input" style={{ width: 120 }} placeholder="PID" value={pid} onChange={(e) => setPid(e.target.value.replace(/\D/g, ""))} />
        <select className="select" value={type} onChange={(e) => setType(e.target.value as ProfileType)}>
          <option value="cpu">CPU</option>
          <option value="heap">Heap in use (Go)</option>
          <option value="heap-alloc">Heap allocations (Go)</option>
          <option value="goroutine">Goroutines (Go)</option>
        </select>
        {type === "cpu" ? (
          <select className="select" value={seconds} onChange={(e) => setSeconds(Number(e.target.value))}>
            {[2, 5, 10, 20, 30].map((n) => (
              <option key={n} value={n}>
                {n}s
              </option>
            ))}
          </select>
        ) : null}
        <input className="input" style={{ width: 170 }} placeholder="pprof addr (Go, optional)" value={pprofAddr} onChange={(e) => setPprofAddr(e.target.value)} />
        <button className="btn primary" disabled={busy || readOnly || !pid} onClick={() => capture(Number(pid))}>
          <Icon name="hot" /> {busy ? "Capturing…" : "Capture"}
        </button>
        <button className="btn" disabled={busy} onClick={openFile}>
          Open profile file…
        </button>
      </div>
      {readOnly ? <Notice kind="info" title="This connection is read-only: live capture is disabled." /> : null}

      {alive.length ? (
        <section className="card" style={{ marginBottom: 14 }}>
          <div className="card-head">
            <h2 className="card-title">Running under monitor run</h2>
          </div>
          <table className="table">
            <tbody>
              {alive.map((l) => (
                <tr key={l.launch_id}>
                  <td className="mono">{l.name}</td>
                  <td className="muted">{l.project}</td>
                  <td className="num muted">pid {l.pid}</td>
                  <td className="muted">{l.inspector_ports?.length ? `inspector :${l.inspector_ports.join(", :")}` : "no inspector"}</td>
                  <td className="faint">{timeAgo(l.started_at)}</td>
                  <td style={{ textAlign: "right" }}>
                    <button className="btn small" disabled={busy || readOnly} onClick={() => capture(l.pid, l.name)}>
                      <Icon name="hot" /> Capture
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ) : null}

      {error ? (
        unavailable ? (
          <Notice
            kind="info"
            title={(error as Error).message}
            recovery={(error as { data?: { recovery?: string } }).data?.recovery ?? "Relaunch it under `monitor run --inspect -- <cmd>` so monitor can profile it."}
          />
        ) : ambiguous && candidates?.length ? (
          <Notice
            kind="info"
            title="That pid has several runtime processes under it — pick one:"
            recovery={
              <span className="row-flex wrap">
                {candidates.map((c) => (
                  <button key={c.pid} className="btn small" onClick={() => capture(c.pid, c.name)}>
                    {c.pid} {c.runtime} {c.main_script ?? c.name}
                  </button>
                ))}
              </span>
            }
          />
        ) : (
          <ErrorNotice error={error} />
        )
      ) : null}

      {hm ? (
        <div className="stack">
          <div className="row-flex wrap">
            <span className="chip">{result!.label}</span>
            <span className="chip">{hm.profile_type}</span>
            <span className="chip">{hm.samples} samples</span>
            {hm.idle_pct ? <span className="chip">{hm.idle_pct.toFixed(0)}% idle</span> : null}
            {hm.gc_pct ? <span className="chip">{hm.gc_pct.toFixed(0)}% GC</span> : null}
          </div>
          {(hm.warnings ?? []).map((w) => (
            <Notice key={w} title={w} />
          ))}
          {(hm.limitations ?? []).map((w) => (
            <Notice key={w} kind="info" title={w} />
          ))}
          <div className="grid-2" style={{ gridTemplateColumns: "minmax(0, 0.9fr) minmax(0, 1.1fr)" }}>
            <section className="card">
              <div className="card-head">
                <h2 className="card-title">Functions</h2>
              </div>
              <table className="table">
                <thead>
                  <tr>
                    <th className="num">Self</th>
                    <th className="num">Total</th>
                    <th>Function</th>
                  </tr>
                </thead>
                <tbody>
                  {hm.functions.map((f) => {
                    const issues = new Set((f.lines ?? []).flatMap((l) => (l.issues ?? []).map((i) => i.short_id)));
                    return (
                      <tr key={key(f)} className={`clickable ${selected === key(f) ? "selected" : ""}`} onClick={() => setSelected(key(f))}>
                        <td className="num">{f.self_pct.toFixed(1)}%</td>
                        <td className="num muted">{f.cum_pct.toFixed(1)}%</td>
                        <td>
                          <div className="mono untrusted">{f.name}</div>
                          <div className="faint mono" style={{ fontSize: 11 }}>
                            {location({ file: f.file, line: f.start_line })}
                            {issues.size ? <span className="heat-issue">● {[...issues].join(", ")}</span> : null}
                          </div>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </section>
            <section className="card">
              <div className="card-head">
                <h2 className="card-title">{fn ? fn.name : "Lines"}</h2>
                {fn ? <span className="mono faint">{location({ file: fn.file, line: fn.start_line })}</span> : null}
              </div>
              <div className="card-body">{fn ? <HeatFrame fn={fn} unit={hm.unit} /> : <div className="muted">Pick a function.</div>}</div>
            </section>
          </div>
        </div>
      ) : !error && !busy ? (
        <div className="empty">
          <h3>Pick a process to profile</h3>
          <span>From Host → Processes, a running launch above, or a pid. Node and Deno need --inspect; launch them with:</span>
          <code>monitor run --inspect --name api -- node server.js</code>
        </div>
      ) : null}
    </div>
  );
}

function key(f: HeatFunction) {
  return `${f.name}\u0000${f.file}`;
}
