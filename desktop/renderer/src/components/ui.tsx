// Small shared pieces: icons, confirm dialog, toasts, empty states, and the
// RPC hooks every view uses.

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { bridge, errorCode, errorMessage, Codes } from "../api";
import { getState, setState, toast, useStore } from "../store";

// ---------- icons (inline, stroke = currentColor) ----------

const paths: Record<string, ReactNode> = {
  issues: <path d="M8 1.5 14.5 13H1.5L8 1.5Zm0 4.2v3.6m0 1.9v.1" />,
  hot: <path d="M8.5 1.5c.4 2.6 3.5 3.9 3.5 7.4A4 4 0 0 1 4 9c0-1.5.7-2.6 1.6-3.4.1 1.4.8 2.3 1.6 2.6-.4-2.8.4-5.2 1.3-6.7Z" />,
  host: <path d="M2 3h12v8H2zM5.5 14h5M8 11v3" />,
  launch: <path d="M4 3l9 5-9 5V3Z" />,
  logs: <path d="M3 3.5h10M3 6.5h10M3 9.5h7M3 12.5h5" />,
  incidents: <path d="M2.5 4.5 8 2l5.5 2.5v4C13.5 11.5 11 13.5 8 14.5 5 13.5 2.5 11.5 2.5 8.5v-4Z" />,
  doctor: <path d="M8 14s-5.5-3.2-5.5-7.3A3 3 0 0 1 8 4.5a3 3 0 0 1 5.5 2.2C13.5 10.8 8 14 8 14Z" />,
  settings: <path d="M8 5.5a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5Zm0-4v2m0 9v2m6.5-6.5h-2m-9 0h-2m11.1-4.6-1.4 1.4m-6.4 6.4-1.4 1.4m9.2 0-1.4-1.4M4.4 4.4 3 3" />,
  back: <path d="M10 3 5 8l5 5" />,
  copy: <path d="M5 5h8v8H5zM3 11V3h8" />,
  open: <path d="M9 2.5h4.5V7M13.5 2.5 7 9M12 10v3.5H2.5V4H6" />,
  refresh: <path d="M13 3v3.5H9.5M3 13V9.5h3.5M12.6 6.5A5 5 0 0 0 3.6 5M3.4 9.5a5 5 0 0 0 9 1.5" />,
  stop: <path d="M4 4h8v8H4z" />,
  plus: <path d="M8 3v10M3 8h10" />,
  check: <path d="m3 8.5 3 3 7-7" />,
  bell: <path d="M4 11V7a4 4 0 1 1 8 0v4l1.5 1.5h-11L4 11Zm2.5 2.5a1.5 1.5 0 0 0 3 0" />,
};

export function Icon({ name, className = "nav-icon" }: { name: keyof typeof paths | string; className?: string }) {
  return (
    <svg className={className} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth={1.4} strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      {paths[name] ?? null}
    </svg>
  );
}

// ---------- confirm dialog (promise-based) ----------

interface ConfirmRequest {
  title: string;
  body: ReactNode;
  confirmLabel: string;
  danger?: boolean;
  resolve: (ok: boolean) => void;
}

let pushConfirm: ((req: ConfirmRequest) => void) | null = null;

/** Asks the user; resolves true only on an explicit click. */
export function confirm(opts: Omit<ConfirmRequest, "resolve">): Promise<boolean> {
  return new Promise((resolve) => {
    if (!pushConfirm) return resolve(false);
    pushConfirm({ ...opts, resolve });
  });
}

export function ConfirmHost() {
  const [req, setReq] = useState<ConfirmRequest | null>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    pushConfirm = setReq;
    return () => {
      pushConfirm = null;
    };
  }, []);
  useEffect(() => {
    if (req) confirmRef.current?.focus();
  }, [req]);
  if (!req) return null;
  const close = (ok: boolean) => {
    req.resolve(ok);
    setReq(null);
  };
  return (
    <div className="modal-backdrop" onMouseDown={() => close(false)} onKeyDown={(e) => e.key === "Escape" && close(false)}>
      <div className="modal" role="dialog" aria-modal onMouseDown={(e) => e.stopPropagation()}>
        <h3>{req.title}</h3>
        <div className="muted">{req.body}</div>
        <div className="actions">
          <button className="btn" onClick={() => close(false)}>
            Cancel
          </button>
          <button ref={confirmRef} className={`btn ${req.danger ? "danger solid" : "primary"}`} onClick={() => close(true)}>
            {req.confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}

export function Toasts() {
  const toasts = useStore((s) => s.toasts);
  return (
    <div className="toast-stack" aria-live="polite">
      {toasts.map((t) => (
        <div key={t.id} className={`toast ${t.kind}`}>
          {t.text}
        </div>
      ))}
    </div>
  );
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <h3>{title}</h3>
      {children}
    </div>
  );
}

/** A degraded/unavailable explanation with its recovery. */
export function Notice({ kind = "warn", title, recovery }: { kind?: "warn" | "error" | "info"; title: ReactNode; recovery?: ReactNode }) {
  return (
    <div className={`notice ${kind === "warn" ? "" : kind}`}>
      <div>
        <div>{title}</div>
        {recovery ? <div className="recovery">{recovery}</div> : null}
      </div>
    </div>
  );
}

export function CopyButton({ text, label = "Copy", small = true }: { text: string; label?: string; small?: boolean }) {
  return (
    <button
      className={`btn ghost ${small ? "small" : ""}`}
      title={text}
      onClick={async (e) => {
        e.stopPropagation();
        await bridge.copy(text);
        toast("Copied", "success");
      }}
    >
      <Icon name="copy" className="nav-icon" />
      {label}
    </button>
  );
}

// ---------- RPC hooks ----------

export interface RpcState<T> {
  data: T | null;
  error: unknown;
  loading: boolean;
  reload: () => void;
}

/**
 * Calls method on the active connection whenever deps change (and once the
 * connection is ready). The previous data stays visible while reloading.
 */
export function useRpc<T>(method: string, params: unknown, deps: unknown[] = [], opts: { enabled?: boolean } = {}): RpcState<T> {
  const conn = useStore((s) => s.activeConn);
  const ready = useStore((s) => s.connections.find((c) => c.id === s.activeConn)?.state === "ready");
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [tick, setTick] = useState(0);
  const enabled = opts.enabled ?? true;
  const seq = useRef(0);

  useEffect(() => {
    setData(null);
  }, [conn]);

  useEffect(() => {
    if (!ready || !enabled) return;
    const id = ++seq.current;
    setLoading(true);
    bridge
      .call<T>(conn, method, params)
      .then((result) => {
        if (id !== seq.current) return;
        setData(result);
        setError(null);
      })
      .catch((err) => {
        if (id !== seq.current) return;
        setError(err);
      })
      .finally(() => {
        if (id === seq.current) setLoading(false);
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn, ready, enabled, method, tick, ...deps]);

  const reload = useCallback(() => setTick((t) => t + 1), []);
  return { data, error, loading, reload };
}

/** One call on the active connection, with errors turned into toasts. */
export async function rpc<T>(method: string, params?: unknown): Promise<T> {
  return bridge.call<T>(getState().activeConn, method, params);
}

/**
 * Runs a destructive method: the server asks for confirm, the user gives
 * it. The request is sent with confirm:true only after the dialog.
 */
export async function confirmedRpc<T>(method: string, params: Record<string, unknown>, ask: Omit<ConfirmRequest, "resolve">): Promise<T | null> {
  if (!(await confirm(ask))) return null;
  try {
    return await rpc<T>(method, { ...params, confirm: true });
  } catch (err) {
    if (errorCode(err) === Codes.Refused) toast(errorMessage(err), "error");
    throw err;
  }
}

export function ErrorNotice({ error }: { error: unknown }) {
  if (!error) return null;
  const data = (error as { data?: { limitation?: string; recovery?: string } }).data;
  return <Notice kind="error" title={errorMessage(error)} recovery={data?.recovery} />;
}

export function useInterval(fn: () => void, ms: number | null) {
  const saved = useRef(fn);
  saved.current = fn;
  useEffect(() => {
    if (ms === null) return;
    const id = setInterval(() => saved.current(), ms);
    return () => clearInterval(id);
  }, [ms]);
}

export function setActiveConnection(id: string) {
  setState({ activeConn: id });
}
