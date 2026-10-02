// Code frames: the culprit snippet of an issue, and a function's lines
// with a heat channel (line_heatmap.v1). Source text is rendered as text —
// it is the monitored project's code, never markup.

import type { ReactElement } from "react";
import type { HeatFunction, HeatLine, Snippet } from "../api";

export function SnippetFrame({ snippet }: { snippet: Snippet }) {
  return (
    <pre className="code">
      {snippet.lines.map((text, i) => {
        const ln = snippet.start + i;
        return (
          <div key={ln} className={`code-line ${ln === snippet.highlight ? "hl" : ""}`}>
            <span className="ln">{ln}</span>
            <span className="src">{text}</span>
          </div>
        );
      })}
    </pre>
  );
}

/**
 * A function's sampled lines in line order, a heat bar per line (its share
 * of the function), and the issues raised on that line. Gaps between
 * sampled lines are shown as "…" rather than invented source.
 */
export function HeatFrame({ fn, unit }: { fn: HeatFunction; unit: string }) {
  const lines: HeatLine[] = [...(fn.lines ?? [])].sort((a, b) => a.line - b.line);
  if (!lines.length) return <div className="muted">No line-level samples for this function.</div>;
  const out: ReactElement[] = [];
  let prev = -1;
  for (const l of lines) {
    if (prev !== -1 && l.line > prev + 1) {
      out.push(
        <div key={`gap-${l.line}`} className="code-gap">
          ⋯
        </div>,
      );
    }
    prev = l.line;
    const pct = Math.max(0, Math.min(100, l.pct_of_function));
    out.push(
      <div key={l.line} className={`code-line ${pct >= 50 ? "hl" : ""}`} title={`self ${l.self} · cum ${l.cum} ${unit}`}>
        <span className="ln">{l.line}</span>
        <span className="heat-cell">
          <span className="heat-bar">
            <span style={{ width: `${pct}%` }} />
          </span>
          <span className="mono">{pct.toFixed(pct >= 10 ? 0 : 1)}%</span>
        </span>
        <span className="src">
          {l.code ?? ""}
          {l.issues?.length ? (
            <span className="heat-issue">
              {l.issues.map((iss) => `● ${iss.short_id}×${iss.count}`).join("  ")}
            </span>
          ) : null}
          {l.stale ? <span className="heat-issue faint"> (source changed since capture)</span> : null}
        </span>
      </div>,
    );
  }
  return <pre className="code heat-frame">{out}</pre>;
}
