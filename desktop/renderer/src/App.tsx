import { useEffect } from "react";
import { bridge, type IssuesList } from "./api";
import { ConfirmHost, Icon, setActiveConnection, Toasts, useRpc } from "./components/ui";
import { activeConnection, navigate, useStore, type View } from "./store";
import { DoctorView } from "./views/DoctorView";
import { HostView } from "./views/HostView";
import { HotView } from "./views/HotView";
import { IncidentsView } from "./views/IncidentsView";
import { IssuesView } from "./views/IssuesView";
import { IssueView } from "./views/IssueView";
import { LaunchView } from "./views/LaunchView";
import { LogsView } from "./views/LogsView";
import { SettingsView } from "./views/SettingsView";

const NAV: { view: View; label: string; icon: string }[] = [
  { view: "issues", label: "Issues", icon: "issues" },
  { view: "hot", label: "Hot lines", icon: "hot" },
  { view: "host", label: "Host", icon: "host" },
  { view: "launch", label: "Launch", icon: "launch" },
  { view: "logs", label: "Logs", icon: "logs" },
  { view: "incidents", label: "Incidents", icon: "incidents" },
  { view: "doctor", label: "Doctor", icon: "doctor" },
];

export function App() {
  const route = useStore((s) => s.route);
  const connections = useStore((s) => s.connections);
  const activeConn = useStore((s) => s.activeConn);
  const conn = useStore((s) => activeConnection(s));
  const issuesVersion = useStore((s) => s.issuesVersion);
  const openCount = useRpc<IssuesList>("issues.list", { statuses: ["open"], limit: 1 }, [issuesVersion]);

  useEffect(() => {
    // Picking a remote host connects it on demand.
    if (conn && (conn.state === "idle" || conn.state === "closed")) void bridge.connections.connect(conn.id).catch(() => {});
  }, [conn?.id, conn?.state]);

  return (
    <div className="app">
      <div className="titlebar-drag" />
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark">m</span> Monitor
        </div>

        <div className="conn-switch">
          <div className="section-label">Hosts</div>
          {connections.map((c) => (
            <button key={c.id} className={`conn-item ${c.id === activeConn ? "active" : ""}`} onClick={() => setActiveConnection(c.id)} title={c.error ?? c.host ?? c.name}>
              <span className={`dot ${c.state}`} />
              <span className="conn-name">{c.name}</span>
              {c.readOnly ? <span className="faint mono" style={{ fontSize: 10 }}>RO</span> : null}
            </button>
          ))}
          <button className="conn-item" onClick={() => navigate({ view: "settings" })}>
            <Icon name="plus" />
            <span className="conn-name faint">Add a host…</span>
          </button>
        </div>

        <nav className="nav">
          <div className="section-label">{conn?.name ?? "—"}</div>
          {NAV.map((n) => (
            <button
              key={n.view}
              className={`nav-item ${route.view === n.view || (n.view === "issues" && route.view === "issue") ? "active" : ""}`}
              onClick={() => navigate({ view: n.view })}
            >
              <Icon name={n.icon} />
              {n.label}
              {n.view === "issues" && openCount.data ? <span className={`count ${openCount.data.total ? "hot" : ""}`}>{openCount.data.total}</span> : null}
            </button>
          ))}
          <button className={`nav-item ${route.view === "settings" ? "active" : ""}`} onClick={() => navigate({ view: "settings" })}>
            <Icon name="settings" />
            Connections
          </button>
        </nav>

        <div className="sidebar-foot">
          {conn?.hello ? (
            <>
              monitor {conn.hello.monitor_version}
              <br />
              {conn.hello.hostname} · {conn.hello.os}/{conn.hello.arch}
              {conn.hello.read_only ? " · read-only" : ""}
            </>
          ) : conn?.state === "connecting" ? (
            "connecting…"
          ) : (
            conn?.error ?? ""
          )}
        </div>
      </aside>

      <main className="main">
        {conn && conn.state !== "ready" && route.view !== "settings" ? <ConnectionBanner /> : <Route />}
      </main>
      <ConfirmHost />
      <Toasts />
    </div>
  );
}

function Route() {
  const route = useStore((s) => s.route);
  switch (route.view) {
    case "issue":
      return <IssueView id={route.issueId!} />;
    case "hot":
      return <HotView initialPid={route.pid} />;
    case "host":
      return <HostView />;
    case "launch":
      return <LaunchView />;
    case "logs":
      return <LogsView />;
    case "incidents":
      return <IncidentsView />;
    case "doctor":
      return <DoctorView />;
    case "settings":
      return <SettingsView />;
    default:
      return <IssuesView />;
  }
}

function ConnectionBanner() {
  const conn = useStore((s) => activeConnection(s));
  const route = useStore((s) => s.route);
  if (!conn) return null;
  if (route.view === "launch" && conn.kind === "local") return <LaunchView />;
  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">{conn.name}</h1>
      </header>
      <div className="empty">
        {conn.state === "connecting" ? (
          <>
            <h3>Connecting…</h3>
            <span className="mono faint">{conn.kind === "ssh" ? `ssh ${conn.host} monitor serve --stdio` : "monitor serve --stdio"}</span>
          </>
        ) : (
          <>
            <h3>{conn.state === "error" ? "Could not connect" : "Disconnected"}</h3>
            {conn.error ? <div className="untrusted" style={{ maxWidth: 640 }}>{conn.error}</div> : null}
            <div className="row-flex">
              <button className="btn primary" onClick={() => bridge.connections.connect(conn.id)}>
                <Icon name="refresh" /> Connect
              </button>
              <button className="btn" onClick={() => navigate({ view: "settings" })}>
                Connection settings
              </button>
            </div>
          </>
        )}
      </div>
    </div>
  );
}
