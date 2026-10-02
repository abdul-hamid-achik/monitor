// Dependency-free SVG charts: a sparkline for issue rows, a time-series
// line chart for host metrics, and a bar histogram for issue activity.

import { useMemo } from "react";

export function Sparkline({ values, width = 96, height = 22 }: { values: number[]; width?: number; height?: number }) {
  const { line, area } = useMemo(() => {
    if (!values.length) return { line: "", area: "" };
    const max = Math.max(1, ...values);
    const step = values.length > 1 ? width / (values.length - 1) : width;
    const pts = values.map((v, i) => [i * step, height - 1 - (v / max) * (height - 3)] as const);
    const line = pts.map(([x, y], i) => `${i ? "L" : "M"}${x.toFixed(1)},${y.toFixed(1)}`).join("");
    return { line, area: `${line}L${width},${height}L0,${height}Z` };
  }, [values, width, height]);
  if (!values.length) return <svg className="spark" width={width} height={height} />;
  return (
    <svg className="spark" width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden>
      <path className="area" d={area} />
      <path className="line" d={line} />
    </svg>
  );
}

export interface Series {
  values: number[];
  label: string;
}

/**
 * A line chart over evenly spaced samples. max fixes the y range (100 for
 * percentages); otherwise it follows the data, shared by every series so a
 * read/write pair stays comparable.
 */
export function LineChart({
  series,
  height = 120,
  max,
  format = (v: number) => v.toFixed(0),
}: {
  series: Series[];
  height?: number;
  max?: number;
  format?: (v: number) => string;
}) {
  const width = 600;
  const pad = { l: 44, r: 6, t: 8, b: 14 };
  const yMax = useMemo(() => {
    if (max !== undefined) return max;
    const m = Math.max(0, ...series.flatMap((s) => s.values));
    return m > 0 ? m * 1.15 : 1;
  }, [series, max]);
  const n = Math.max(2, ...series.map((s) => s.values.length));
  const x = (i: number) => pad.l + (i / (n - 1)) * (width - pad.l - pad.r);
  const y = (v: number) => pad.t + (1 - Math.min(v, yMax) / yMax) * (height - pad.t - pad.b);
  const grid = [0, 0.5, 1].map((f) => yMax * f);
  return (
    <svg className="chart" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" style={{ height }}>
      {grid.map((g) => (
        <g key={g}>
          <line className="grid" x1={pad.l} x2={width - pad.r} y1={y(g)} y2={y(g)} />
          <text className="axis" x={pad.l - 6} y={y(g) + 3} textAnchor="end">
            {format(g)}
          </text>
        </g>
      ))}
      {series.map((s, si) => {
        if (s.values.length < 2) return null;
        const offset = n - s.values.length;
        const d = s.values.map((v, i) => `${i ? "L" : "M"}${x(i + offset).toFixed(1)},${y(v).toFixed(1)}`).join("");
        const area = `${d}L${x(n - 1)},${y(0)}L${x(offset)},${y(0)}Z`;
        return (
          <g key={s.label}>
            <path className={`series-${si}-area`} d={area} />
            <path className={`series-${si}`} d={d} vectorEffect="non-scaling-stroke" />
          </g>
        );
      })}
    </svg>
  );
}

/** Bars for an issue's histogram, oldest bucket first. */
export function Bars({ values, height = 70, label }: { values: number[]; height?: number; label?: (i: number) => string }) {
  const width = 600;
  const max = Math.max(1, ...values);
  const bw = width / Math.max(1, values.length);
  return (
    <svg className="chart bars" viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" style={{ height }}>
      {values.map((v, i) => {
        const h = v === 0 ? 0 : Math.max(2, (v / max) * (height - 4));
        return (
          <rect key={i} x={i * bw + 1} y={height - h} width={Math.max(1, bw - 2)} height={h}>
            <title>{`${label ? label(i) : i}: ${v}`}</title>
          </rect>
        );
      })}
    </svg>
  );
}

/** A horizontal usage meter coloured by threshold. */
export function Meter({ pct }: { pct: number | undefined }) {
  const v = Math.max(0, Math.min(100, pct ?? 0));
  const cls = v >= 90 ? "crit" : v >= 75 ? "warn" : "good";
  return (
    <div className="meter">
      <span className={cls} style={{ width: `${v}%` }} />
    </div>
  );
}
