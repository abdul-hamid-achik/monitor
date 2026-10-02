// Pure formatting helpers (covered by test/format.test.ts).

export function timeAgo(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 45) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 30) return `${d}d ago`;
  const mo = Math.round(d / 30);
  if (mo < 12) return `${mo}mo ago`;
  return `${Math.round(mo / 12)}y ago`;
}

export function dateTime(iso: string | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

export function bytes(n: number | undefined, digits = 1): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return "—";
  let v = Math.abs(n);
  let i = 0;
  while (v >= 1024 && i < UNITS.length - 1) {
    v /= 1024;
    i++;
  }
  const out = i === 0 ? String(Math.round(v)) : v.toFixed(v >= 100 ? 0 : digits);
  return `${n < 0 ? "-" : ""}${out} ${UNITS[i]}`;
}

export function rate(n: number | undefined): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return "—";
  return `${bytes(n)}/s`;
}

export function percent(n: number | undefined, digits = 0): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return "—";
  return `${n.toFixed(digits)}%`;
}

export function count(n: number | undefined): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return "—";
  if (n < 1000) return String(n);
  if (n < 10_000) return `${(n / 1000).toFixed(1)}k`;
  if (n < 1_000_000) return `${Math.round(n / 1000)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

export function duration(seconds: number | undefined): string {
  if (seconds === undefined || !Number.isFinite(seconds)) return "—";
  const s = Math.floor(seconds);
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${s}s`;
}

/** "src/app.js:42" for a culprit-shaped object, or "" without a file. */
export function location(c: { file?: string; line?: number } | undefined): string {
  if (!c?.file) return "";
  return c.line ? `${c.file}:${c.line}` : c.file;
}

/** The short id the CLI prints ("9FBE") for a full ISS-… id. */
export function shortID(id: string): string {
  const suffix = id.startsWith("ISS-") ? id.slice(4) : id;
  return suffix.slice(0, 4);
}

/**
 * A single shell word, quoted only when needed — for the "copy CLI" buttons.
 * Mirrors the CLI's own quoting so a copied command pastes cleanly.
 */
export function shellQuote(s: string): string {
  if (/^[A-Za-z0-9_./:@%+=,-]+$/.test(s)) return s;
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

/** An issue's age class for the inbox badge. */
export function issueBadge(issue: { status: string; first_seen: string; reopened_count: number }, now: number = Date.now()):
  | "new"
  | "regressed"
  | "resolved"
  | "ignored"
  | null {
  if (issue.status === "resolved") return "resolved";
  if (issue.status === "ignored") return "ignored";
  if (issue.reopened_count > 0) return "regressed";
  const first = Date.parse(issue.first_seen);
  if (!Number.isNaN(first) && now - first < 24 * 3600 * 1000) return "new";
  return null;
}
