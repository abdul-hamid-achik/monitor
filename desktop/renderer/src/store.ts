// App state: connections, the active one, the current route, and toasts.
// A tiny external store read with useSyncExternalStore — the app has one
// window and a handful of global facts, so it needs no state library.

import { useSyncExternalStore } from "react";
import { bridge, type AppInfo, type ConnSnapshot, type IssueEvent, type Settings } from "./api";

export type View = "issues" | "issue" | "hot" | "host" | "launch" | "logs" | "incidents" | "doctor" | "settings";

export interface Route {
  view: View;
  issueId?: string;
  pid?: number;
}

export interface Toast {
  id: number;
  kind: "info" | "success" | "error";
  text: string;
}

export interface State {
  connections: ConnSnapshot[];
  activeConn: string;
  route: Route;
  history: Route[];
  toasts: Toast[];
  settings: Settings | null;
  appInfo: AppInfo | null;
  /** Bumped on every issue.event for the active connection. */
  issuesVersion: number;
  lastIssueEvent: (IssueEvent & { connId: string; at: number }) | null;
  searchFocus: number;
}

let state: State = {
  connections: [],
  activeConn: "local",
  route: { view: "issues" },
  history: [],
  toasts: [],
  settings: null,
  appInfo: null,
  issuesVersion: 0,
  lastIssueEvent: null,
  searchFocus: 0,
};

const listeners = new Set<() => void>();

export function getState(): State {
  return state;
}

export function setState(patch: Partial<State> | ((s: State) => Partial<State>)) {
  const next = typeof patch === "function" ? patch(state) : patch;
  state = { ...state, ...next };
  for (const l of listeners) l();
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function useStore<T>(select: (s: State) => T): T {
  return useSyncExternalStore(subscribe, () => select(state));
}

export function navigate(route: Route) {
  setState((s) => ({ route, history: [...s.history.slice(-30), s.route] }));
}

export function goBack() {
  setState((s) => {
    const prev = s.history[s.history.length - 1];
    return prev ? { route: prev, history: s.history.slice(0, -1) } : {};
  });
}

let toastSeq = 0;
export function toast(text: string, kind: Toast["kind"] = "info") {
  const id = ++toastSeq;
  setState((s) => ({ toasts: [...s.toasts.slice(-3), { id, kind, text }] }));
  setTimeout(() => setState((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })), kind === "error" ? 8000 : 3500);
}

export function activeConnection(s: State = state): ConnSnapshot | undefined {
  return s.connections.find((c) => c.id === s.activeConn);
}

/** True when the active connection's server offers method. */
export function can(method: string, s: State = state): boolean {
  return Boolean(activeConnection(s)?.hello?.methods.includes(method));
}

// Notification fan-out for components that want raw protocol events
// (host ticks), keyed by connection.
type NotificationListener = (method: string, params: any) => void;
const notificationListeners = new Map<string, Set<NotificationListener>>();

export function onNotification(connId: string, listener: NotificationListener) {
  let set = notificationListeners.get(connId);
  if (!set) {
    set = new Set();
    notificationListeners.set(connId, set);
  }
  set.add(listener);
  return () => {
    set!.delete(listener);
  };
}

/** Wires the bridge's push events into the store. Called once at boot. */
export async function initStore() {
  bridge.on("conn:state", (snap: ConnSnapshot) => {
    setState((s) => {
      const others = s.connections.filter((c) => c.id !== snap.id);
      const index = s.connections.findIndex((c) => c.id === snap.id);
      const connections = [...others];
      connections.splice(index === -1 ? connections.length : index, 0, snap);
      return { connections };
    });
  });
  bridge.on("conn:notification", ({ connId, method, params }) => {
    for (const l of notificationListeners.get(connId) ?? []) l(method, params);
    if (method === "issue.event" && connId === state.activeConn) {
      setState((s) => ({ issuesVersion: s.issuesVersion + 1, lastIssueEvent: { ...params, connId, at: Date.now() } }));
    }
  });
  bridge.on("menu:navigate", ({ view }) => navigate({ view }));
  bridge.on("menu:search", () => {
    navigate({ view: "issues" });
    setState((s) => ({ searchFocus: s.searchFocus + 1 }));
  });
  bridge.on("menu:open-profile", () => navigate({ view: "hot" }));
  bridge.on("app:open-issue", ({ connId, id }) => {
    setState({ activeConn: connId });
    navigate({ view: "issue", issueId: id });
  });
  bridge.on("app:error", ({ message }) => toast(message, "error"));

  const [connections, settings, appInfo] = await Promise.all([
    bridge.connections.list(),
    bridge.settings.get(),
    bridge.appInfo(),
  ]);
  setState({ connections, settings, appInfo });
}
