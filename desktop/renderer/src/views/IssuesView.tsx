// The inbox: grouped issues for the active connection, filtered and
// searchable, with 24 h sparklines, live updates from issue.event, and bulk
// resolve / ignore / reopen.

import { useEffect, useMemo, useRef, useState } from "react";
import { type Histogram, type IssuesList, type ProjectSummary } from "../api";
import { Sparkline } from "../components/charts";
import { Empty, ErrorNotice, Icon, rpc, useRpc } from "../components/ui";
import { count, issueBadge, location, timeAgo } from "../format";
import { navigate, toast, useStore } from "../store";

type StatusFilter = "open" | "resolved" | "ignored" | "all";
type KindFilter = "" | "exception" | "alert" | "investigation";

export function IssuesView() {
  const conn = useStore((s) => s.activeConn);
  const issuesVersion = useStore((s) => s.issuesVersion);
  const lastEvent = useStore((s) => s.lastIssueEvent);
  const searchFocus = useStore((s) => s.searchFocus);
  const readOnly = useStore((s) => s.connections.find((c) => c.id === s.activeConn)?.hello?.read_only ?? false);
  const [status, setStatus] = useState<StatusFilter>("open");
  const [project, setProject] = useState("");
  const [kind, setKind] = useState<KindFilter>("");
  const [query, setQuery] = useState("");
  const [debounced, setDebounced] = useState("");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const searchRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const t = setTimeout(() => setDebounced(query.trim()), 200);
    return () => clearTimeout(t);
  }, [query]);
  useEffect(() => {
    if (searchFocus) searchRef.current?.focus();
  }, [searchFocus]);
  useEffect(() => {
    setSelected(new Set());
    setProject("");
  }, [conn]);

  const params = useMemo(
    () => ({
      statuses: status === "all" ? [] : [status],
      project: project || undefined,
      kind: kind || undefined,
      query: debounced || undefined,
      limit: 300,
    }),
    [status, project, kind, debounced],
  );
  const list = useRpc<IssuesList>("issues.list", params, [JSON.stringify(params), issuesVersion]);
  const projects = useRpc<{ projects: ProjectSummary[] }>("projects.list", undefined, [issuesVersion]);
  const ids = useMemo(() => (list.data?.items ?? []).map((i) => i.id), [list.data]);
  const hist = useRpc<Histogram>("issues.histogram", { ids, since: "24h", buckets: 24 }, [ids.join(","), issuesVersion], {
    enabled: ids.length > 0,
  });

  const items = list.data?.items ?? [];
  const flashId = lastEvent && Date.now() - lastEvent.at < 2000 ? lastEvent.issue.id : null;

  const bulk = async (to: "resolved" | "ignored" | "open") => {
    const target = [...selected];
    if (!target.length) return;
    try {
      const res = await rpc<{ updated: unknown[]; failed?: { id: string; error: string }[] }>("issues.set_status", { ids: target, status: to });
      const failed = res.failed?.length ?? 0;
      toast(`${res.updated.length} issue${res.updated.length === 1 ? "" : "s"} ${to === "open" ? "reopened" : to}${failed ? `, ${failed} failed` : ""}`, failed ? "error" : "success");
      setSelected(new Set());
      list.reload();
    } catch (err) {
      toast(String((err as Error).message), "error");
    }
  };

  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const allSelected = items.length > 0 && items.every((i) => selected.has(i.id));

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Issues</h1>
        <span className="page-sub">
          {list.data ? `${list.data.total} ${status === "all" ? "" : status} · grouped by fingerprint` : ""}
        </span>
        <div className="spacer" />
        <button className="btn ghost" onClick={() => list.reload()} title="Refresh">
          <Icon name="refresh" />
        </button>
      </header>

      <div className="toolbar">
        <div className="segmented" role="tablist">
          {(["open", "resolved", "ignored", "all"] as StatusFilter[]).map((s) => (
            <button key={s} className={status === s ? "on" : ""} onClick={() => setStatus(s)}>
              {s[0].toUpperCase() + s.slice(1)}
            </button>
          ))}
        </div>
        <select className="select" value={project} onChange={(e) => setProject(e.target.value)}>
          <option value="">All projects</option>
          {(projects.data?.projects ?? []).map((p) => (
            <option key={p.project} value={p.project}>
              {p.project} ({p.open} open)
            </option>
          ))}
        </select>
        <select className="select" value={kind} onChange={(e) => setKind(e.target.value as KindFilter)}>
          <option value="">All kinds</option>
          <option value="exception">Exceptions</option>
          <option value="alert">Host alerts</option>
          <option value="investigation">Investigations</option>
        </select>
        <input
          ref={searchRef}
          className="input search"
          placeholder="Search title, message, file, function…  ⌘K"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <div className="spacer" />
        {selected.size > 0 && !readOnly ? (
          <>
            <span className="muted">{selected.size} selected</span>
            <button className="btn" onClick={() => bulk("resolved")}>
              <Icon name="check" /> Resolve
            </button>
            <button className="btn" onClick={() => bulk("ignored")}>
              Ignore
            </button>
            <button className="btn" onClick={() => bulk("open")}>
              Reopen
            </button>
          </>
        ) : null}
      </div>

      <ErrorNotice error={list.error} />

      {list.data && items.length === 0 ? (
        <Empty title={debounced || project || kind ? "No issues match these filters" : status === "open" ? "No open issues" : `No ${status} issues`}>
          {status === "open" && !debounced && !project ? (
            <>
              <span>Run something under monitor and its crashes land here, grouped, with the line that failed:</span>
              <code>monitor run -- node server.js</code>
              <button className="btn primary" onClick={() => navigate({ view: "launch" })}>
                <Icon name="launch" /> Launch a command
              </button>
            </>
          ) : null}
        </Empty>
      ) : null}

      {items.length > 0 ? (
        <table className="table">
          <thead>
            <tr>
              <th style={{ width: 28 }}>
                {!readOnly ? (
                  <input
                    type="checkbox"
                    checked={allSelected}
                    onChange={() => setSelected(allSelected ? new Set() : new Set(items.map((i) => i.id)))}
                  />
                ) : null}
              </th>
              <th>Issue</th>
              <th>Last 24 h</th>
              <th className="num">Events</th>
              <th>Project</th>
              <th>Last seen</th>
              <th>Age</th>
            </tr>
          </thead>
          <tbody>
            {items.map((issue) => {
              const badge = issueBadge(issue);
              const where = location(issue.culprit);
              return (
                <tr
                  key={issue.id}
                  className={`clickable ${selected.has(issue.id) ? "selected" : ""} ${flashId === issue.id ? "flash" : ""}`}
                  onClick={() => navigate({ view: "issue", issueId: issue.id })}
                >
                  <td onClick={(e) => e.stopPropagation()}>
                    {!readOnly ? <input type="checkbox" checked={selected.has(issue.id)} onChange={() => toggle(issue.id)} /> : null}
                  </td>
                  <td className="cell-title">
                    <div className="issue-title">
                      {badge ? <span className={`badge ${badge}`}>{badge}</span> : null}
                      {issue.level === "fatal" ? <span className="badge fatal">fatal</span> : null}
                      <span className="type">{issue.exception_type || issue.kind}</span>
                      <span className="msg untrusted">{issue.message || issue.title}</span>
                    </div>
                    <div className="issue-sub">
                      <span>{issue.id.slice(4, 8)}</span>
                      {where ? (
                        <span className="culprit">
                          {where}
                          {issue.culprit?.function ? ` · ${issue.culprit.function}()` : ""}
                        </span>
                      ) : (
                        <span className="faint">no in-app frame</span>
                      )}
                      {issue.service ? <span>{issue.service}</span> : null}
                    </div>
                  </td>
                  <td>
                    <Sparkline values={hist.data?.series[issue.id] ?? []} />
                  </td>
                  <td className="num">{count(issue.occurrence_count)}</td>
                  <td className="muted">{issue.project}</td>
                  <td className="muted" title={issue.last_seen}>
                    {timeAgo(issue.last_seen)}
                  </td>
                  <td className="faint" title={issue.first_seen}>
                    {timeAgo(issue.first_seen)}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      ) : null}
      {list.data?.truncated ? <div className="faint" style={{ padding: 12 }}>Showing the first {items.length} of {list.data.total}. Narrow the filters to see the rest.</div> : null}
      {!list.data && list.loading ? <div className="loading">Loading issues…</div> : null}
    </div>
  );
}
