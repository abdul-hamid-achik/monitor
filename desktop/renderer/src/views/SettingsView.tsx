// Connections and preferences. A remote host is an ssh alias or user@host:
// the app runs `ssh -T <host> monitor serve --stdio` with your ssh config,
// agent and keys (BatchMode, so it never prompts), and the issue data
// streams straight from that host — it is never uploaded anywhere.

import { useState } from "react";
import { bridge, type ConnectionConfig, type Settings } from "../api";
import { Icon, Notice } from "../components/ui";
import { setState, toast, useStore } from "../store";

export function SettingsView() {
  const settings = useStore((s) => s.settings);
  const connections = useStore((s) => s.connections);
  const app = useStore((s) => s.appInfo);
  const [host, setHost] = useState("");
  const [name, setName] = useState("");
  const [remotePath, setRemotePath] = useState("");
  const [readOnly, setReadOnly] = useState(false);

  if (!settings) return null;

  const save = async (patch: Partial<Settings>) => {
    try {
      const next = await bridge.settings.update(patch);
      setState({ settings: next });
      return true;
    } catch (err) {
      toast(String((err as Error).message), "error");
      return false;
    }
  };

  const addHost = async () => {
    const h = host.trim();
    if (!h) return;
    const id = `ssh-${h.replace(/[^A-Za-z0-9_-]/g, "-").slice(0, 50)}`;
    if (settings.connections.some((c) => c.id === id)) {
      toast(`${h} is already a connection`, "error");
      return;
    }
    const conn: ConnectionConfig = { id, name: name.trim() || h, kind: "ssh", host: h, monitorPath: remotePath.trim() || undefined, readOnly };
    if (await save({ connections: [...settings.connections, conn] })) {
      setHost("");
      setName("");
      setRemotePath("");
      setReadOnly(false);
      await bridge.connections.connect(id).catch(() => {});
      toast(`Connecting to ${conn.name}…`);
    }
  };

  const remove = async (id: string) => {
    await save({ connections: settings.connections.filter((c) => c.id !== id) });
    setState((s) => (s.activeConn === id ? { activeConn: "local" } : {}));
  };

  const update = async (id: string, patch: Partial<ConnectionConfig>) => {
    await save({ connections: settings.connections.map((c) => (c.id === id ? { ...c, ...patch } : c)) });
  };

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Connections & settings</h1>
      </header>

      <div className="stack">
        <section className="card">
          <div className="card-head">
            <h2 className="card-title">Connections</h2>
            <span className="faint">each one is a monitor serve --stdio session</span>
          </div>
          <table className="table">
            <tbody>
              {settings.connections.map((c) => {
                const live = connections.find((x) => x.id === c.id);
                return (
                  <tr key={c.id}>
                    <td style={{ width: 24 }}>
                      <span className={`dot ${live?.state ?? ""}`} />
                    </td>
                    <td>
                      <div>{c.name}</div>
                      <div className="faint mono" style={{ fontSize: 11 }}>
                        {c.kind === "local" ? "this machine" : `ssh ${c.host} · ${c.monitorPath || "monitor"}`}
                        {live?.hello ? ` · monitor ${live.hello.monitor_version} · ${live.hello.os}/${live.hello.arch}` : ""}
                      </div>
                      {live?.state === "error" && live.error ? <div className="untrusted" style={{ color: "var(--crit)", fontSize: 12, marginTop: 3 }}>{live.error}</div> : null}
                    </td>
                    <td>
                      <label className="check" title="Start the server with --read-only: no resolve, kill or capture">
                        <input type="checkbox" checked={Boolean(c.readOnly)} onChange={(e) => update(c.id, { readOnly: e.target.checked })} />
                        read-only
                      </label>
                    </td>
                    <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                      {live?.state === "ready" ? (
                        <button className="btn small" onClick={() => bridge.connections.disconnect(c.id)}>
                          Disconnect
                        </button>
                      ) : (
                        <button className="btn small" onClick={() => bridge.connections.connect(c.id)}>
                          <Icon name="refresh" /> Connect
                        </button>
                      )}
                      {c.kind === "ssh" ? (
                        <button className="btn small ghost danger" onClick={() => remove(c.id)}>
                          Remove
                        </button>
                      ) : null}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
          <div className="card-body stack" style={{ gap: 10 }}>
            <div className="row-flex" style={{ alignItems: "flex-end" }}>
              <label className="field" style={{ flex: 1 }}>
                SSH host
                <input className="input mono" placeholder="dev-box   or   me@10.0.0.12   (from ~/.ssh/config)" value={host} onChange={(e) => setHost(e.target.value)} />
              </label>
              <label className="field" style={{ width: 160 }}>
                Name
                <input className="input" placeholder="optional" value={name} onChange={(e) => setName(e.target.value)} />
              </label>
              <label className="field" style={{ width: 220 }}>
                Remote monitor path
                <input className="input mono" placeholder="monitor" value={remotePath} onChange={(e) => setRemotePath(e.target.value)} />
              </label>
              <label className="check" style={{ height: 28 }}>
                <input type="checkbox" checked={readOnly} onChange={(e) => setReadOnly(e.target.checked)} /> read-only
              </label>
              <button className="btn primary" disabled={!host.trim()} onClick={addHost}>
                <Icon name="plus" /> Add host
              </button>
            </div>
            <Notice
              kind="info"
              title="Remote hosts connect over your own ssh: keys, agent, ProxyJump and Tailscale all work, and nothing is uploaded to any service."
              recovery="The host needs monitor with `monitor serve --stdio`. Non-interactive ssh shells often lack Homebrew or ~/go/bin on PATH — set the remote path (for example ~/go/bin/monitor or /usr/local/lib/chalupa/monitor on a Chalupa droplet)."
            />
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2 className="card-title">Preferences</h2>
          </div>
          <div className="card-body stack" style={{ gap: 12 }}>
            <label className="field" style={{ maxWidth: 320 }}>
              Open files in
              <select className="select" value={settings.editor} onChange={(e) => save({ editor: e.target.value as Settings["editor"] })}>
                <option value="vscode">VS Code (also Remote-SSH)</option>
                <option value="cursor">Cursor (also Remote-SSH)</option>
                <option value="zed">Zed</option>
                <option value="idea">JetBrains IDEs</option>
                <option value="none">Don't open files</option>
              </select>
            </label>
            <label className="check">
              <input type="checkbox" checked={settings.notifications} onChange={(e) => save({ notifications: e.target.checked })} />
              Notify me about new and regressed issues
            </label>
            <label className="field">
              Local monitor binary
              <div className="row-flex">
                <input
                  className="input wide mono"
                  placeholder={app?.monitor ? `${app.monitor.path} (${app.monitor.source})` : "auto"}
                  value={settings.monitorPath}
                  onChange={(e) => setState({ settings: { ...settings, monitorPath: e.target.value } })}
                  onBlur={(e) => save({ monitorPath: e.target.value })}
                />
                <button
                  className="btn"
                  onClick={async () => {
                    const p = await bridge.dialogs.chooseBinary();
                    if (p) await save({ monitorPath: p });
                  }}
                >
                  Choose…
                </button>
                {settings.monitorPath ? (
                  <button className="btn ghost" onClick={() => save({ monitorPath: "" })}>
                    Use auto
                  </button>
                ) : null}
              </div>
            </label>
            <div className="faint">Changing the binary takes effect on the next connect.</div>
          </div>
        </section>
      </div>
    </div>
  );
}
