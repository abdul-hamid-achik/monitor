// Host: live CPU, memory, network and disk from the host topic (the same
// collector and rule engine as `monitor watch`), per-core load, filesystems,
// the process table with a confirmed kill, and the alerts the rules raised.

import { useEffect, useMemo, useState } from "react";
import { bridge, type Alert, type HostTick, type ProcessList } from "../api";
import { LineChart, Meter } from "../components/charts";
import { confirmedRpc, ErrorNotice, Icon, useInterval, useRpc } from "../components/ui";
import { bytes, duration, percent, rate, timeAgo } from "../format";
import { navigate, onNotification, toast, useStore } from "../store";

const WINDOW = 120;

interface History {
  cpu: number[];
  mem: number[];
  rx: number[];
  tx: number[];
  rd: number[];
  wr: number[];
}

const emptyHistory = (): History => ({ cpu: [], mem: [], rx: [], tx: [], rd: [], wr: [] });

function push(arr: number[], v: number | undefined) {
  const next = [...arr, v ?? 0];
  return next.length > WINDOW ? next.slice(next.length - WINDOW) : next;
}

export function HostView() {
  const conn = useStore((s) => s.activeConn);
  const connInfo = useStore((s) => s.connections.find((c) => c.id === s.activeConn));
  const ready = connInfo?.state === "ready";
  const readOnly = connInfo?.hello?.read_only ?? false;
  const [tick, setTick] = useState<HostTick | null>(null);
  const [history, setHistory] = useState<History>(emptyHistory);
  const [alerts, setAlerts] = useState<(Alert & { at: number })[]>([]);
  const [streamError, setStreamError] = useState<string | null>(null);
  const [sort, setSort] = useState<"cpu" | "memory">("cpu");
  const [filter, setFilter] = useState("");
  const [showSystem, setShowSystem] = useState(false);
  const [allVolumes, setAllVolumes] = useState(false);

  useEffect(() => {
    setTick(null);
    setHistory(emptyHistory());
    setAlerts([]);
    if (!ready) return;
    const off = onNotification(conn, (method, params) => {
      if (method === "topic.error" && params?.topic === "host") setStreamError(params.error);
      if (method !== "host.tick") return;
      const t = params as HostTick;
      setTick(t);
      setStreamError(null);
      setHistory((h) => ({
        cpu: push(h.cpu, t.snapshot.cpu.usage_percent),
        mem: push(h.mem, t.snapshot.memory.usage_percent),
        rx: push(h.rx, t.snapshot.network.bytes_recv_per_sec),
        tx: push(h.tx, t.snapshot.network.bytes_sent_per_sec),
        rd: push(h.rd, t.snapshot.disk_io.read_per_sec),
        wr: push(h.wr, t.snapshot.disk_io.write_per_sec),
      }));
      if (t.alerts?.length) {
        setAlerts((a) => [...t.alerts!.map((x) => ({ ...x, at: Date.now() })), ...a].slice(0, 50));
      }
    });
    bridge.call(conn, "subscribe", { topics: ["host"], host: { interval_ms: 1000, process_limit: 5 } }).catch((err) => setStreamError(String(err.message)));
    return () => {
      off();
      bridge.call(conn, "unsubscribe", { topics: ["host"] }).catch(() => {});
    };
  }, [conn, ready]);

  const procParams = useMemo(() => ({ sort, limit: 60, filter: filter || undefined, include_system: showSystem }), [sort, filter, showSystem]);
  const procs = useRpc<ProcessList>("processes.list", procParams, [JSON.stringify(procParams)]);
  useInterval(() => procs.reload(), ready ? 3000 : null);

  const kill = async (pid: number, name: string) => {
    try {
      const res = await confirmedRpc<{ killed: boolean; outcome: string; next_action?: string }>(
        "process.kill",
        { pid },
        {
          title: `Terminate ${name} (${pid})?`,
          body: (
            <>
              Sends SIGTERM to pid {pid}
              {connInfo?.kind === "ssh" ? (
                <>
                  {" "}
                  on <b>{connInfo.name}</b>
                </>
              ) : null}{" "}
              and waits to confirm it exited. Protected and system processes are always refused.
            </>
          ),
          confirmLabel: "Terminate",
          danger: true,
        },
      );
      if (!res) return;
      toast(res.killed ? `${name} terminated` : `${name}: ${res.outcome}${res.next_action ? ` — ${res.next_action}` : ""}`, res.killed ? "success" : "error");
      procs.reload();
    } catch (err) {
      toast(String((err as Error).message), "error");
    }
  };

  const s = tick?.snapshot;
  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Host</h1>
        <span className="page-sub">
          {s ? `${s.host.hostname} · ${s.host.platform || s.host.os} · up ${duration(s.host.uptime_seconds)}` : "waiting for the first sample…"}
        </span>
      </header>
      {streamError ? <ErrorNotice error={new Error(streamError)} /> : null}

      <div className="grid-4">
        <Stat label="CPU" value={percent(s?.cpu.usage_percent)} hint={s ? `${s.cpu.core_count} cores · load ${tick?.load_avg[0].toFixed(2)}` : ""} />
        <Stat label="Memory" value={percent(s?.memory.usage_percent)} hint={s ? `${bytes(s.memory.used_bytes)} of ${bytes(s.memory.total_bytes)}` : ""} />
        <Stat label="Network" value={rate(s?.network.bytes_recv_per_sec)} hint={s ? `↑ ${rate(s.network.bytes_sent_per_sec)}` : ""} />
        <Stat label="Disk I/O" value={rate(s?.disk_io.read_per_sec)} hint={s ? `write ${rate(s.disk_io.write_per_sec)}` : ""} />
      </div>

      <div className="grid-2" style={{ marginTop: 14 }}>
        <section className="card">
          <div className="card-head">
            <h2 className="card-title">CPU & memory</h2>
            <span className="faint">last {WINDOW}s</span>
            <div className="spacer" />
            <span className="legend">
              <i /> cpu
            </span>
            <span className="legend">
              <i className="s1" /> memory
            </span>
          </div>
          <div className="card-body">
            <LineChart series={[{ label: "cpu", values: history.cpu }, { label: "mem", values: history.mem }]} max={100} format={(v) => `${v.toFixed(0)}%`} />
          </div>
        </section>
        <section className="card">
          <div className="card-head">
            <h2 className="card-title">Network & disk</h2>
            <span className="faint">per second</span>
            <div className="spacer" />
            <span className="legend">
              <i /> network in
            </span>
            <span className="legend">
              <i className="s1" /> disk read
            </span>
          </div>
          <div className="card-body">
            <LineChart series={[{ label: "rx", values: history.rx }, { label: "rd", values: history.rd }]} format={(v) => bytes(v, 0)} />
          </div>
        </section>
      </div>

      {tick?.per_core_usage.length ? (
        <section className="card" style={{ marginTop: 14 }}>
          <div className="card-head">
            <h2 className="card-title">Cores</h2>
          </div>
          <div className="card-body cores">
            {tick.per_core_usage.map((v, i) => (
              <div className="core" key={i} title={`core ${i}: ${v.toFixed(0)}%`}>
                <div className="bar">
                  <span style={{ height: `${Math.min(100, v)}%` }} />
                </div>
                {v.toFixed(0)}%
              </div>
            ))}
          </div>
        </section>
      ) : null}

      <div className="grid-2" style={{ marginTop: 14 }}>
        <section className="card">
          <div className="card-head">
            <h2 className="card-title">Filesystems</h2>
            <div className="spacer" />
            <label className="check faint">
              <input type="checkbox" checked={allVolumes} onChange={(e) => setAllVolumes(e.target.checked)} /> system volumes
            </label>
          </div>
          <div className="card-body stack" style={{ gap: 10 }}>
            {(s?.filesystems ?? []).filter((f) => allVolumes || isUserVolume(f.mount_point, f.total_bytes)).map((f) => (
              <div key={f.mount_point}>
                <div className="row-flex">
                  <span className="mono">{f.mount_point}</span>
                  <span className="faint">{f.filesystem}</span>
                  <div className="spacer" />
                  <span className="mono muted">
                    {bytes(f.used_bytes)} / {bytes(f.total_bytes)}
                  </span>
                </div>
                <Meter pct={f.usage_percent} />
              </div>
            ))}
            {!s?.filesystems?.length ? <div className="muted">—</div> : null}
          </div>
        </section>
        <section className="card">
          <div className="card-head">
            <h2 className="card-title">Alerts</h2>
            <span className="faint">rules from monitor watch · 1 min cooldown</span>
          </div>
          <div className="card-body">
            {alerts.length ? (
              <ul className="frame-list">
                {alerts.map((a, i) => (
                  <li key={i}>
                    <span className={`badge ${a.severity}`}>{a.severity}</span>
                    <span className="fn">{a.rule}</span>
                    <span className="untrusted">
                      {a.process ? `${a.process} (${a.pid}) · ` : ""}
                      {a.diagnosis?.summary ?? a.detail}
                    </span>
                    <span className="faint">{timeAgo(new Date(a.at).toISOString())}</span>
                  </li>
                ))}
              </ul>
            ) : (
              <div className="muted">No alerts while this view was open.</div>
            )}
          </div>
        </section>
      </div>

      <section className="card" style={{ marginTop: 14 }}>
        <div className="card-head">
          <h2 className="card-title">Processes</h2>
          <span className="faint">{procs.data ? `${procs.data.matched} of ${procs.data.total}` : ""}</span>
          <div className="spacer" />
          <div className="segmented">
            <button className={sort === "cpu" ? "on" : ""} onClick={() => setSort("cpu")}>
              CPU
            </button>
            <button className={sort === "memory" ? "on" : ""} onClick={() => setSort("memory")}>
              Memory
            </button>
          </div>
          <input className="input" placeholder="Filter name, pid, user" value={filter} onChange={(e) => setFilter(e.target.value)} />
          <label className="check">
            <input type="checkbox" checked={showSystem} onChange={(e) => setShowSystem(e.target.checked)} /> system
          </label>
        </div>
        <ErrorNotice error={procs.error} />
        <table className="table">
          <thead>
            <tr>
              <th className="num">PID</th>
              <th>Name</th>
              <th>User</th>
              <th className="num">CPU</th>
              <th className="num">Memory</th>
              <th className="num">Threads</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {(procs.data?.processes ?? []).map((p) => (
              <tr key={p.pid}>
                <td className="num muted">{p.pid}</td>
                <td>
                  <span className="untrusted">{p.name}</span> {p.is_protected ? <span className="badge neutral">protected</span> : null}
                </td>
                <td className="muted">{p.user}</td>
                <td className="num">{percent(p.cpu_percent, 1)}</td>
                <td className="num">{bytes(p.memory)}</td>
                <td className="num muted">{p.threads}</td>
                <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                  <button className="btn small ghost" title="Line heatmap for this process" onClick={() => navigate({ view: "hot", pid: p.pid })}>
                    <Icon name="hot" /> Hot lines
                  </button>
                  {!readOnly && !p.is_protected && !p.is_system ? (
                    <button className="btn small ghost danger" onClick={() => kill(p.pid, p.name)}>
                      Kill
                    </button>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>
    </div>
  );
}

/**
 * macOS mounts a dozen system volumes (Preboot, VM, Update, simulator
 * runtimes) that share the root container; they repeat "/" and only add
 * noise. Linux pseudo filesystems report a zero size.
 */
function isUserVolume(mount: string, total: number) {
  if (!total) return false;
  if (mount.startsWith("/System/Volumes/")) return false;
  if (mount.startsWith("/Library/Developer/CoreSimulator/")) return false;
  if (mount.startsWith("/private/var/vm")) return false;
  return true;
}

function Stat({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="card stat">
      <div className="label">{label}</div>
      <div className="value">{value}</div>
      <div className="hint">{hint}</div>
    </div>
  );
}
