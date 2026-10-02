// One issue: monitor.issue_context.v1 at the full budget — the culprit line
// with its snippet, the cause chain, frames, codemap impact, the last
// commit that touched the line, honest degradations, and the next commands
// (copied, never run for you).

import { useState, type ReactNode } from "react";
import { bridge, Codes, errorCode, type Histogram, type IssueGetResult, type Occurrence } from "../api";
import { Bars } from "../components/charts";
import { SnippetFrame } from "../components/code";
import { CopyButton, Empty, ErrorNotice, Icon, Notice, rpc, useRpc } from "../components/ui";
import { count, dateTime, location, shellQuote, timeAgo } from "../format";
import { goBack, toast, useStore } from "../store";

export function IssueView({ id }: { id: string }) {
  const conn = useStore((s) => s.activeConn);
  const connInfo = useStore((s) => s.connections.find((c) => c.id === s.activeConn));
  const issuesVersion = useStore((s) => s.issuesVersion);
  const editor = useStore((s) => s.settings?.editor ?? "vscode");
  const readOnly = connInfo?.hello?.read_only ?? false;
  const got = useRpc<IssueGetResult>("issues.get", { id }, [id, issuesVersion]);
  const occ = useRpc<{ items: Occurrence[]; total: number; truncated: boolean }>("issues.occurrences", { id, limit: 50 }, [id, issuesVersion]);
  const hist = useRpc<Histogram>("issues.histogram", { ids: [id], since: "168h", buckets: 56 }, [id, issuesVersion]);
  const [busy, setBusy] = useState(false);

  if (got.error) {
    const notFound = errorCode(got.error) === Codes.NotFound;
    return (
      <div className="page">
        <Header onBack={goBack} title="Issue" />
        {notFound ? <Empty title="This issue no longer exists">It may have been evicted from the store's bounded history.</Empty> : <ErrorNotice error={got.error} />}
      </div>
    );
  }
  if (!got.data) {
    return (
      <div className="page">
        <Header onBack={goBack} title="Issue" />
        <div className="loading">Loading issue…</div>
      </div>
    );
  }
  if (got.data.not_found || !got.data.context) {
    return (
      <div className="page">
        <Header onBack={goBack} title="Issue" />
        <Empty title="Issue not found">{got.data.recovery}</Empty>
      </div>
    );
  }

  const ctx = got.data.context;
  const root = got.data.root;
  const culprit = ctx.culprit;
  const where = location(culprit);
  const status = ctx.issue.status;

  const setStatus = async (to: "resolved" | "ignored" | "open") => {
    setBusy(true);
    try {
      await rpc("issues.set_status", { ids: [ctx.issue.id], status: to });
      toast(to === "open" ? "Reopened" : to === "resolved" ? "Resolved — a new occurrence will reopen it" : "Ignored", "success");
      got.reload();
    } catch (err) {
      toast(String((err as Error).message), "error");
    } finally {
      setBusy(false);
    }
  };

  const openEditor = async (file?: string, line?: number) => {
    try {
      await bridge.openInEditor({ connId: conn, root, file, line });
    } catch (err) {
      toast(String((err as Error).message), "error");
    }
  };

  const copyForAgent = async () => {
    try {
      const brief = await rpc<IssueGetResult>("issues.get", { id: ctx.issue.id, budget: "brief", markdown: true });
      await bridge.copy(brief.markdown ?? "");
      toast("Copied the issue brief (≤4 KB) for an agent", "success");
    } catch (err) {
      toast(String((err as Error).message), "error");
    }
  };

  const bucketHours = hist.data ? hist.data.bucket_seconds / 3600 : 3;
  const histValues = hist.data?.series[ctx.issue.id] ?? [];

  return (
    <div className="page">
      <Header onBack={goBack} title={ctx.issue.exception_type || ctx.issue.kind}>
        <span className="mono faint">{ctx.issue.short_id}</span>
        <div className="spacer" />
        <button className="btn" onClick={copyForAgent} title="monitor issue <id> --md, brief budget">
          <Icon name="copy" /> Copy for agent
        </button>
        {culprit?.file ? (
          <button className="btn" onClick={() => openEditor(culprit.file, culprit.line)} disabled={editor === "none"}>
            <Icon name="open" /> Open in editor
          </button>
        ) : null}
        {!readOnly ? (
          status === "open" ? (
            <>
              <button className="btn" disabled={busy} onClick={() => setStatus("ignored")}>
                Ignore
              </button>
              <button className="btn primary" disabled={busy} onClick={() => setStatus("resolved")}>
                <Icon name="check" /> Resolve
              </button>
            </>
          ) : (
            <button className="btn" disabled={busy} onClick={() => setStatus("open")}>
              Reopen
            </button>
          )
        ) : null}
      </Header>

      <div className="stack">
        <div>
          <div className="row-flex wrap" style={{ marginBottom: 6 }}>
            <span className={`badge ${status}`}>{status}</span>
            {ctx.timeline.reopened > 0 ? <span className="badge regressed">regressed ×{ctx.timeline.reopened}</span> : null}
            {ctx.issue.level ? <span className={`badge ${ctx.issue.level}`}>{ctx.issue.level}</span> : null}
            {ctx.issue.handled !== undefined ? <span className="badge neutral">{ctx.issue.handled ? "handled" : "unhandled"}</span> : null}
            <span className="chip">{ctx.issue.project}</span>
            {ctx.issue.service ? <span className="chip">{ctx.issue.service}</span> : null}
            {connInfo?.kind === "ssh" ? <span className="chip">on {connInfo.name}</span> : null}
          </div>
          <div className="untrusted" style={{ fontSize: 16, fontWeight: 500 }}>
            {ctx.issue.title}
          </div>
        </div>

        <div className="issue-layout">
          <div className="stack">
            <section className="card">
              <div className="card-head">
                <h2 className="card-title">Culprit</h2>
                {where ? (
                  <button className="link mono" onClick={() => openEditor(culprit?.file, culprit?.line)}>
                    {where}
                  </button>
                ) : null}
                {culprit?.function ? <span className="mono muted">{culprit.function}()</span> : null}
                <div className="spacer" />
                {culprit ? <span className={`badge ${culprit.confidence === "high" ? "ok" : culprit.confidence === "low" ? "warn" : "neutral"}`}>{culprit.confidence} confidence</span> : null}
                {culprit?.mapping ? <span className="badge neutral">{culprit.mapping}</span> : null}
              </div>
              <div className="card-body">
                {!culprit ? (
                  <Notice title="No in-app frame to blame" recovery="Every frame in the chain is library or runtime code, and the message search found nothing." />
                ) : culprit.snippet ? (
                  <>
                    {culprit.snippet.stale ? (
                      <Notice title="The file changed since this was recorded" recovery="The highlighted line may be one edit out of date." />
                    ) : null}
                    <SnippetFrame snippet={culprit.snippet} />
                  </>
                ) : (
                  <div className="muted">No snippet: the source file is not readable from {connInfo?.name ?? "this host"}.</div>
                )}
                {culprit?.source === "message_search" ? (
                  <div className="faint" style={{ marginTop: 8 }}>
                    Inferred from the message text via {culprit.via ?? "search"} — the error printed no stack.
                  </div>
                ) : null}
              </div>
            </section>

            {ctx.causes.length > 0 ? (
              <section className="card">
                <div className="card-head">
                  <h2 className="card-title">Cause chain</h2>
                  <span className="faint">outer → innermost</span>
                </div>
                <div className="card-body">
                  <ul className="frame-list">
                    {ctx.causes.map((c, i) => (
                      <li key={i}>
                        <span className="fn">{c.type || "(untyped)"}</span>
                        {c.culprit?.file ? (
                          <button className="link mono" onClick={() => openEditor(c.culprit?.file, c.culprit?.line)}>
                            {location(c.culprit)}
                          </button>
                        ) : (
                          <span className="faint">no in-app frame</span>
                        )}
                        {c.culprit?.function ? <span className="muted">{c.culprit.function}()</span> : null}
                      </li>
                    ))}
                  </ul>
                </div>
              </section>
            ) : null}

            <section className="card">
              <div className="card-head">
                <h2 className="card-title">Stack</h2>
                <span className="faint">oldest → crash frame</span>
                {ctx.truncated.frames ? <span className="faint">{ctx.truncated.frames} more frames cut at ingest</span> : null}
              </div>
              <div className="card-body">
                {ctx.frames.length ? (
                  <ul className="frame-list">
                    {ctx.frames.map((f, i) => (
                      <li key={i} className={f.in_app ? "" : "lib"}>
                        <span className="fn untrusted">{f.function || "(anonymous)"}</span>
                        {f.in_app && f.file ? (
                          <button className="link mono" onClick={() => openEditor(f.file, f.line)}>
                            {location(f)}
                          </button>
                        ) : (
                          <span className="untrusted">{location(f)}</span>
                        )}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <div className="muted">No frames recorded.</div>
                )}
              </div>
            </section>

            {ctx.event ? (
              <section className="card">
                <div className="card-head">
                  <h2 className="card-title">Event</h2>
                  <span className="faint">
                    {[ctx.event.kind, ctx.event.sdk, ctx.event.mode, ctx.event.release ? `release ${ctx.event.release}` : ""].filter(Boolean).join(" · ")}
                  </span>
                  {ctx.truncated.breadcrumbs ? <span className="faint">{ctx.truncated.breadcrumbs} earlier steps cut</span> : null}
                </div>
                <div className="card-body">
                  {ctx.event.tags && Object.keys(ctx.event.tags).length ? (
                    <div className="mono untrusted" style={{ marginBottom: 8 }}>
                      {Object.entries(ctx.event.tags)
                        .sort(([a], [b]) => a.localeCompare(b))
                        .map(([k, v]) => `${k}=${v}`)
                        .join("  ")}
                    </div>
                  ) : null}
                  {ctx.event.breadcrumbs?.length ? (
                    <ul className="frame-list">
                      {ctx.event.breadcrumbs.map((b, i) => (
                        <li key={i}>
                          <span className="faint">{new Date(b.timestamp).toLocaleTimeString()}</span>
                          <span className="muted crumb-cat">{b.category || ""}</span>
                          <span className="untrusted crumb-msg">{b.message}</span>
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <div className="muted">No steps recorded before this event.</div>
                  )}
                </div>
              </section>
            ) : null}

            <section className="card">
              <div className="card-head">
                <h2 className="card-title">Activity</h2>
                <span className="faint">last 7 days · {bucketHours}h buckets</span>
              </div>
              <div className="card-body">
                <Bars values={histValues} label={(i) => (hist.data ? new Date(Date.parse(hist.data.start) + i * hist.data.bucket_seconds * 1000).toLocaleString() : String(i))} />
                <div className="faint" style={{ marginTop: 6 }}>
                  {hist.data?.note}
                </div>
              </div>
            </section>

            <section className="card">
              <div className="card-head">
                <h2 className="card-title">Occurrences</h2>
                <span className="faint">{occ.data ? `${occ.data.total} retained` : ""}</span>
              </div>
              {occ.data?.items.length ? (
                <table className="table">
                  <thead>
                    <tr>
                      <th>Observed</th>
                      <th className="num">Count</th>
                      <th>Service</th>
                      <th>Run</th>
                      <th>Release</th>
                      <th className="num">PID</th>
                    </tr>
                  </thead>
                  <tbody>
                    {occ.data.items.map((o) => (
                      <tr key={o.id}>
                        <td title={o.observed_at}>{dateTime(o.observed_at)}</td>
                        <td className="num">{o.count && o.count > 1 ? `×${o.count}` : "1"}</td>
                        <td className="muted">{o.service || "—"}</td>
                        <td className="mono muted">{o.run_id || o.run?.id || "—"}</td>
                        <td className="mono muted">{o.release || o.run?.release || "—"}</td>
                        <td className="num muted">{o.pid || "—"}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <div className="card-body muted">{occ.loading ? "Loading…" : "No retained occurrences."}</div>
              )}
            </section>
          </div>

          <aside className="stack">
            <section className="card">
              <div className="card-body">
                <dl className="kv">
                  <dt>Events</dt>
                  <dd className="mono">{count(ctx.timeline.occurrences)}</dd>
                  <dt>First seen</dt>
                  <dd title={ctx.timeline.first_seen}>{timeAgo(ctx.timeline.first_seen)}</dd>
                  <dt>Last seen</dt>
                  <dd title={ctx.timeline.last_seen}>{timeAgo(ctx.timeline.last_seen)}</dd>
                  <dt>Reopened</dt>
                  <dd>{ctx.timeline.reopened}×</dd>
                  {ctx.timeline.first_git_sha ? (
                    <>
                      <dt>First SHA</dt>
                      <dd className="mono">{ctx.timeline.first_git_sha.slice(0, 10)}</dd>
                    </>
                  ) : null}
                  {ctx.timeline.runs?.length ? (
                    <>
                      <dt>Runs</dt>
                      <dd className="mono">{ctx.timeline.runs.slice(-3).join(", ")}</dd>
                    </>
                  ) : null}
                  <dt>ID</dt>
                  <dd className="mono selectable">{ctx.issue.id}</dd>
                </dl>
              </div>
            </section>

            <section className="card">
              <div className="card-head">
                <h2 className="card-title">Impact</h2>
                <span className={`badge ${ctx.impact.status}`}>{ctx.impact.status}</span>
              </div>
              <div className="card-body">
                {ctx.impact.status === "ok" ? (
                  <dl className="kv">
                    <dt>Callers</dt>
                    <dd className="mono">{ctx.impact.callers ?? 0}</dd>
                    <dt>Blast radius</dt>
                    <dd className="mono">{ctx.impact.blast_radius ?? 0}</dd>
                    <dt>Tests</dt>
                    <dd className="mono">{ctx.impact.untested ? <span className="badge warn">untested</span> : ctx.impact.tests ?? 0}</dd>
                    {ctx.impact.test_files?.length ? (
                      <>
                        <dt>Test files</dt>
                        <dd className="mono">{ctx.impact.test_files.slice(0, 4).join(", ")}</dd>
                      </>
                    ) : null}
                  </dl>
                ) : (
                  <div className="muted">
                    {ctx.impact.detail}
                    {ctx.impact.recovery ? <div className="faint" style={{ marginTop: 4 }}>{ctx.impact.recovery}</div> : null}
                  </div>
                )}
              </div>
            </section>

            <section className="card">
              <div className="card-head">
                <h2 className="card-title">Last touched</h2>
                <span className={`badge ${ctx.last_touched.status}`}>{ctx.last_touched.status}</span>
              </div>
              <div className="card-body">
                {ctx.last_touched.status === "ok" ? (
                  <>
                    <div className="mono">{ctx.last_touched.sha?.slice(0, 10)}</div>
                    <div className="untrusted">{ctx.last_touched.subject}</div>
                    <div className="faint">{ctx.last_touched.author_time ? timeAgo(ctx.last_touched.author_time) : ""}</div>
                  </>
                ) : (
                  <div className="muted">
                    {ctx.last_touched.detail}
                    {ctx.last_touched.recovery ? <div className="faint" style={{ marginTop: 4 }}>{ctx.last_touched.recovery}</div> : null}
                  </div>
                )}
              </div>
            </section>

            {ctx.degraded.length ? (
              <section className="card">
                <div className="card-head">
                  <h2 className="card-title">Degraded</h2>
                </div>
                <div className="card-body stack" style={{ gap: 8 }}>
                  {ctx.degraded.map((d) => (
                    <Notice key={d.component} title={`${d.component}: ${d.state}${d.detail ? ` — ${d.detail}` : ""}`} recovery={d.recovery} />
                  ))}
                </div>
              </section>
            ) : null}

            <section className="card">
              <div className="card-head">
                <h2 className="card-title">Next</h2>
                <span className="faint">proposed, never run for you</span>
              </div>
              <div className="card-body">
                <ul className="cmd-list">
                  <li>
                    <code>monitor issue {ctx.issue.short_id}</code>
                    <CopyButton text={`monitor issue ${ctx.issue.short_id}`} label="" />
                  </li>
                  {culprit?.file && culprit.line ? (
                    <li>
                      <code>monitor issues --at {shellQuote(`${culprit.file}:${culprit.line}`)}</code>
                      <CopyButton text={`monitor issues --at ${shellQuote(`${culprit.file}:${culprit.line}`)}`} label="" />
                    </li>
                  ) : null}
                  {ctx.next
                    .filter((n) => n.cli)
                    .map((n) => (
                      <li key={n.cli} title={n.why}>
                        <code>{n.cli}</code>
                        <CopyButton text={n.cli!} label="" />
                      </li>
                    ))}
                </ul>
              </div>
            </section>
            {ctx.privacy.scrubbed > 0 ? <div className="faint">{ctx.privacy.scrubbed} secret-shaped value(s) redacted in this page.</div> : null}
          </aside>
        </div>
      </div>
    </div>
  );
}

function Header({ onBack, title, children }: { onBack: () => void; title: string; children?: ReactNode }) {
  return (
    <header className="page-header">
      <button className="btn ghost" onClick={onBack} title="Back">
        <Icon name="back" />
      </button>
      <h1 className="page-title">{title}</h1>
      {children}
    </header>
  );
}
