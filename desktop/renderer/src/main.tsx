import "@fontsource-variable/geist";
import "@fontsource-variable/geist-mono";
import "./styles.css";

import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { bridge, type IssuesList } from "./api";
import { App } from "./App";
import { getState, initStore } from "./store";

async function boot() {
  await initStore();
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  );
  if (bridge.smokeMode) void smoke();
}

/**
 * `electron . --smoke`: wait for the local connection, make real calls
 * through the whole stack (renderer → preload → main → monitor serve), and
 * report to the main process, which prints one JSON line and exits.
 */
async function smoke() {
  const checks: Record<string, unknown> = {};
  try {
    const deadline = Date.now() + 30_000;
    while (Date.now() < deadline) {
      const local = getState().connections.find((c) => c.id === "local");
      if (local?.state === "ready") break;
      if (local?.state === "error") throw new Error(`local connection failed: ${local.error}`);
      await new Promise((r) => setTimeout(r, 100));
    }
    const local = getState().connections.find((c) => c.id === "local");
    if (local?.state !== "ready") throw new Error("local connection never became ready");
    checks.protocol = local.hello?.protocol;
    checks.monitor_version = local.hello?.monitor_version;
    checks.ping = await bridge.call("local", "ping");
    const list = await bridge.call<IssuesList>("local", "issues.list", { limit: 5 });
    checks.issues_total = list.total;
    checks.rendered = Boolean(document.querySelector(".sidebar"));
    bridge.smokeReady({ ok: checks.protocol === "monitor.app.v1" && Boolean(checks.rendered), checks });
  } catch (error) {
    bridge.smokeReady({ ok: false, reason: String((error as Error).message ?? error), checks });
  }
}

void boot();
