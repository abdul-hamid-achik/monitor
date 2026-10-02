// Logs: the log store `monitor logs capture` fills, searched by text, level
// and process. Every line is scrubbed by the server and shown as text.

import { useEffect, useMemo, useState } from "react";
import type { LogEntry } from "../api";
import { Empty, ErrorNotice, Icon, useRpc } from "../components/ui";
import { dateTime } from "../format";

const LEVELS = ["error", "fatal", "warn", "info", "debug"];
const WINDOWS: [string, number][] = [
  ["15 min", 900],
  ["1 hour", 3600],
  ["24 hours", 86400],
  ["7 days", 604800],
  ["All", 0],
];

export function LogsView() {
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [levels, setLevels] = useState<string[]>([]);
  const [process, setProcess] = useState("");
  const [since, setSince] = useState(3600);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(query.trim()), 250);
    return () => clearTimeout(t);
  }, [query]);
  const params = useMemo(
    () => ({ query: debounced || undefined, levels: levels.length ? levels : undefined, process: process || undefined, since_seconds: since || undefined, limit: 500 }),
    [debounced, levels, process, since],
  );
  const logs = useRpc<{ entries: LogEntry[] }>("logs.search", params, [JSON.stringify(params)]);
  const entries = logs.data?.entries ?? [];

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Logs</h1>
        <span className="page-sub">captured by monitor logs capture</span>
        <div className="spacer" />
        <button className="btn ghost" onClick={() => logs.reload()} title="Refresh">
          <Icon name="refresh" />
        </button>
      </header>
      <div className="toolbar">
        <input className="input search" placeholder="Search messages…" value={query} onChange={(e) => setQuery(e.target.value)} />
        <input className="input" placeholder="Process" value={process} onChange={(e) => setProcess(e.target.value)} />
        <div className="segmented">
          {LEVELS.map((l) => (
            <button
              key={l}
              className={levels.includes(l) ? "on" : ""}
              onClick={() => setLevels((prev) => (prev.includes(l) ? prev.filter((x) => x !== l) : [...prev, l]))}
            >
              {l}
            </button>
          ))}
        </div>
        <select className="select" value={since} onChange={(e) => setSince(Number(e.target.value))}>
          {WINDOWS.map(([label, s]) => (
            <option key={label} value={s}>
              {label}
            </option>
          ))}
        </select>
      </div>
      <ErrorNotice error={logs.error} />
      {logs.data && entries.length === 0 ? (
        <Empty title="No log lines match">
          <span>Capture a running service's output into the searchable log store:</span>
          <code>monitor logs capture --name api -- node server.js</code>
        </Empty>
      ) : null}
      {entries.length ? (
        <table className="table">
          <thead>
            <tr>
              <th style={{ width: 170 }}>Time</th>
              <th style={{ width: 70 }}>Level</th>
              <th style={{ width: 140 }}>Process</th>
              <th>Message</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e, i) => (
              <tr key={i}>
                <td className="mono muted">{dateTime(e.timestamp)}</td>
                <td>
                  <span className={`badge ${e.level === "error" || e.level === "fatal" ? "crit" : e.level === "warn" || e.level === "warning" ? "warn" : "neutral"}`}>{e.level}</span>
                </td>
                <td className="mono muted">
                  {e.process}
                  {e.pid ? <span className="faint"> {e.pid}</span> : null}
                </td>
                <td className="mono untrusted">{e.message}</td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}
    </div>
  );
}
