// Incidents: integrity-hashed monitor.incident bundles archived in
// file.cheap, and local bundles still waiting for archival.

import { CopyButton, Empty, ErrorNotice, Icon, Notice, useRpc } from "../components/ui";
import { bytes, timeAgo } from "../format";

interface IncidentsResult {
  stashes: { id: string; name?: string; created_at?: string; tags?: string[]; file_count?: number; total_size?: number }[];
  pending: { id: string; name?: string; created_at?: string; trigger?: string; attempts?: number; last_error?: string; size_bytes?: number }[];
  stash_error?: string;
  pending_error?: string;
}

export function IncidentsView() {
  const res = useRpc<IncidentsResult>("incidents.list", undefined, []);
  const d = res.data;
  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Incidents</h1>
        <span className="page-sub">monitor.incident bundles · evidence that survives the process</span>
        <div className="spacer" />
        <button className="btn ghost" onClick={() => res.reload()} title="Refresh">
          <Icon name="refresh" />
        </button>
      </header>
      <ErrorNotice error={res.error} />
      {d?.stash_error ? <Notice title="file.cheap could not list stashes" recovery={d.stash_error} /> : null}
      {d?.pending?.length ? (
        <section className="card" style={{ marginBottom: 14 }}>
          <div className="card-head">
            <h2 className="card-title">Waiting for archival</h2>
            <span className="faint">kept locally because fcheap failed</span>
          </div>
          <table className="table">
            <tbody>
              {d.pending.map((p) => (
                <tr key={p.id}>
                  <td className="mono">{p.id}</td>
                  <td className="muted">{p.trigger ?? ""}</td>
                  <td className="faint">{timeAgo(p.created_at)}</td>
                  <td className="muted untrusted">{p.last_error ?? ""}</td>
                  <td style={{ textAlign: "right" }}>
                    <CopyButton text={`monitor incidents resume-stash ${p.id}`} label="Copy retry" />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ) : null}
      {d && !d.stashes.length && !d.pending.length ? (
        <Empty title="No incident bundles yet">
          <span>An investigation or a watch alert with --stash bundles snapshot, profile and correlations as evidence:</span>
          <code>monitor investigate &lt;pid&gt;</code>
        </Empty>
      ) : null}
      {d?.stashes.length ? (
        <section className="card">
          <div className="card-head">
            <h2 className="card-title">Archived</h2>
          </div>
          <table className="table">
            <tbody>
              {d.stashes.map((s) => (
                <tr key={s.id}>
                  <td className="mono">{s.id}</td>
                  <td className="untrusted">{s.name ?? ""}</td>
                  <td className="mono muted">{s.total_size !== undefined ? bytes(s.total_size) : ""}</td>
                  <td className="faint">{timeAgo(s.created_at)}</td>
                  <td style={{ textAlign: "right" }}>
                    <CopyButton text={`fcheap restore ${s.id}`} label="Copy restore" />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      ) : null}
      {!d && res.loading ? <div className="loading">Loading incidents…</div> : null}
    </div>
  );
}
