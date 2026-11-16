import { useEffect, useState, type ReactNode } from "react";
import { Link, NavLink, useParams } from "react-router-dom";
import { adminToken, agentSecretYAML, api, ApiError, download, writeErrorText, type Cluster, type ServerSettings } from "../api";
import { useAppData } from "../App";
import { Icon } from "../icons";
import { ago, Card, Empty, ErrorBox, Loading, Msg, Tag, useApi } from "../ui";
import { overrideText } from "./ClusterDetail";

type M = { ok: boolean; text: string } | null;

const TABS: [string, string][] = [
  ["clusters", "Clusters"], ["stages", "Stages"], ["tenants", "Tenants"], ["policy", "Version policy"],
  ["overrides", "Catalog overrides"], ["token", "Admin token"],
];

export default function Settings() {
  const { tab = "clusters" } = useParams();
  const hasToken = adminToken.get() !== "";
  return (
    <>
      <div className="page-head">
        <div>
          <h1>Settings</h1>
          <p className="muted">Changes need the admin token{hasToken ? " (set for this tab)" : ""}.</p>
        </div>
      </div>
      <nav className="tabs" aria-label="Settings sections">
        {TABS.map(([id, label]) => <NavLink key={id} to={`/settings/${id}`} className={({ isActive }) => `tab${isActive ? " active" : ""}`}>{label}</NavLink>)}
      </nav>
      {!hasToken && tab !== "token" && (
        <div className="callout info"><Icon.info /><span>Read-only: <Link to="/settings/token">set the admin token</Link> to make changes.</span></div>
      )}
      {tab === "clusters" && <ClustersTab />}
      {tab === "stages" && <StagesTab />}
      {tab === "tenants" && <TenantsTab />}
      {tab === "policy" && <PolicyTab />}
      {tab === "overrides" && <OverridesTab />}
      {tab === "token" && <TokenTab />}
      {!TABS.some(([id]) => id === tab) && <Empty>Unknown section.</Empty>}
    </>
  );
}

/** Runs a write, reports the result, and tells the caller whether it worked. */
function useWrite() {
  const [msg, setMsg] = useState<M>(null);
  const [busy, setBusy] = useState(false);
  async function run<T>(fn: () => Promise<T>, ok?: string): Promise<T | undefined> {
    setBusy(true); setMsg(null);
    try { const r = await fn(); if (ok) setMsg({ ok: true, text: ok }); return r; }
    catch (e) { setMsg({ ok: false, text: writeErrorText(e) }); return undefined; }
    finally { setBusy(false); }
  }
  return { msg, setMsg, busy, run };
}

function Dialog({ title, onClose, children }: { title: string; onClose?: () => void; children: ReactNode }) {
  useEffect(() => {
    if (!onClose) return;
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="modal-wrap">
      <div className="modal" role="dialog" aria-modal="true" aria-label={title}>
        <div className="row" style={{ justifyContent: "space-between" }}>
          <h1>{title}</h1>
          {onClose && <button className="icon-btn" onClick={onClose} aria-label="Close"><Icon.close /></button>}
        </div>
        {children}
      </div>
    </div>
  );
}

// ---------------- clusters ----------------

type DialogState =
  | { kind: "add" }
  | { kind: "edit"; c: Cluster }
  | { kind: "delete"; c: Cluster }
  | { kind: "token"; cluster: string; token: string; rotated: boolean };

function ClustersTab() {
  const { clusters, reload } = useAppData();
  const settings = useApi(api.settings);
  const w = useWrite();
  const [dlg, setDlg] = useState<DialogState | null>(null);
  const canWrite = adminToken.get() !== "";

  if (!clusters) return <Loading />;
  const close = () => { setDlg(null); reload(); };

  async function rotate(c: Cluster) {
    if (!confirm(`Rotate the token for ${c.name}? The current agent is rejected until its Secret is updated and the pod restarted.`)) return;
    const r = await w.run(() => api.rotateToken(c.name));
    if (r) setDlg({ kind: "token", cluster: c.name, token: r.token, rotated: true });
  }

  return (
    <>
      <Card title="Clusters" actions={<>
        <a className="btn" href={api.agentManifestURL} download="agent.yaml">Download agent.yaml</a>
        <button className="btn primary" disabled={!canWrite} onClick={() => setDlg({ kind: "add" })}>+ Add cluster</button>
      </>}>
        {clusters.length === 0 ? <Empty>No clusters yet. Add one, then apply its agent manifest.</Empty> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Cluster</th><th>Stage</th><th>Tenant</th><th>Kubernetes</th><th>Agent</th><th>Status</th><th></th></tr></thead>
              <tbody>
                {clusters.map((c) => (
                  <tr key={c.name}>
                    <td><Link to={`/clusters/${encodeURIComponent(c.name)}`}><b>{c.name}</b></Link></td>
                    <td>{c.stage}</td><td>{c.tenant}</td>
                    <td className="mono">{c.kubernetesVersion || "—"}</td><td className="mono">{c.agentVersion || "—"}</td>
                    <td><Status c={c} /></td>
                    <td>
                      <div className="row" style={{ justifyContent: "flex-end", flexWrap: "nowrap" }}>
                        <button className="btn small" disabled={!canWrite || w.busy} onClick={() => setDlg({ kind: "edit", c })}>Edit</button>
                        <button className="btn small" disabled={!canWrite || w.busy} onClick={() => rotate(c)}>Rotate token</button>
                        <button className="btn small danger" disabled={!canWrite || w.busy} onClick={() => setDlg({ kind: "delete", c })}>Delete</button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <Msg msg={w.msg} />
      </Card>

      {settings.error ? <ErrorBox error={settings.error} retry={settings.reload} /> : !settings.data ? <Loading /> :
        <AgentSettings initial={settings.data} onSaved={settings.reload} canWrite={canWrite} />}

      {dlg?.kind === "add" && <AddDialog onClose={close} onCreated={(cluster, token) => setDlg({ kind: "token", cluster, token, rotated: false })} />}
      {dlg?.kind === "edit" && <EditDialog c={dlg.c} onClose={close} />}
      {dlg?.kind === "delete" && <DeleteDialog c={dlg.c} onClose={close} />}
      {dlg?.kind === "token" && <TokenDialog {...dlg} serverURL={settings.data?.serverURL ?? ""} onClose={close} />}
    </>
  );
}

function Status({ c }: { c: Cluster }) {
  if (!c.lastSeenAt) return <span className="row" style={{ gap: 6 }}><span className="dot lvl-unknown" aria-hidden />Waiting for first report</span>;
  const stale = Date.now() - new Date(c.lastSeenAt).getTime() > 45 * 60 * 1000;
  return (
    <span className="row" style={{ gap: 6 }}>
      <span className={`dot ${stale ? "lvl-high" : "lvl-ok"}`} aria-hidden />Reported {ago(c.lastSeenAt)}
      {stale && <Tag tone="warn" title="No report for 45+ minutes (agents push every 15)">stale</Tag>}
    </span>
  );
}

function AgentSettings({ initial, onSaved, canWrite }: { initial: ServerSettings; onSaved: () => void; canWrite: boolean }) {
  const [v, setV] = useState(initial);
  const w = useWrite();
  const dirty = v.serverURL !== initial.serverURL || v.agentImage !== initial.agentImage;
  return (
    <Card title="Agent install settings">
      <p className="subtle" style={{ margin: 0 }}>Used in the files Add cluster and Rotate token give you.</p>
      {(!initial.serverURL || !initial.agentImage) && (
        <div className="callout warn"><Icon.warn /><span>
          {!initial.serverURL && "No server URL yet: the token Secret will contain a placeholder. "}
          {!initial.agentImage && "No agent image yet: agent.yaml uses service-inventory:dev, which only exists on dev clusters."}
        </span></div>
      )}
      <form className="grid-2" style={{ gap: 16 }} onSubmit={async (e) => {
        e.preventDefault();
        if (await w.run(() => api.saveSettings(v), "Saved.")) onSaved();
      }}>
        <label className="field">Server URL agents use
          <input className="input mono" placeholder="https://inventory.example.internal" value={v.serverURL} onChange={(e) => setV({ ...v, serverURL: e.target.value })} />
        </label>
        <label className="field">Agent image
          <input className="input mono" placeholder="registry.example.com/devops/service-inventory:0.7.0" value={v.agentImage} onChange={(e) => setV({ ...v, agentImage: e.target.value })} />
        </label>
        <div className="row"><button className="btn primary" disabled={!canWrite || !dirty || w.busy}>Save</button><Msg msg={w.msg} /></div>
      </form>
    </Card>
  );
}

const NEW = "__new";

function AddDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (cluster: string, token: string) => void }) {
  const { stages } = useAppData();
  const tenants = useApi(api.tenants);
  const [name, setName] = useState("");
  const [stage, setStage] = useState(stages?.[0]?.name ?? "");
  const [newStage, setNewStage] = useState("");
  const [tenant, setTenant] = useState("");
  const [newTenant, setNewTenant] = useState("");
  const w = useWrite();
  const tenantList = tenants.data ?? [];
  const pickedTenant = tenant || tenantList[0]?.name || NEW;
  const finalStage = stage === NEW ? newStage.trim() : stage;
  const finalTenant = pickedTenant === NEW ? newTenant.trim() : pickedTenant;
  const valid = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name) && finalStage !== "" && finalTenant !== "";

  async function create() {
    // Retrying after a failed create must not trip over the stage the first attempt already added.
    if (stage === NEW) {
      try { await api.addStage(finalStage); }
      catch (e) { if (!(e instanceof ApiError && e.status === 409)) { w.setMsg({ ok: false, text: writeErrorText(e) }); return; } }
    }
    const r = await w.run(() => api.addCluster({ name, stage: finalStage, tenant: finalTenant }));
    if (r) onCreated(r.cluster.name, r.token);
  }

  return (
    <Dialog title="Add cluster" onClose={onClose}>
      <label className="field">Name
        <input className="input mono" placeholder="e.g. ds-prod" value={name} onChange={(e) => setName(e.target.value.toLowerCase())} autoFocus />
        <span className="subtle" style={{ fontWeight: 400 }}>Lowercase letters, digits and “-”. Can't be changed later (history refers to it).</span>
      </label>
      <label className="field">Stage
        <select className="select" value={stage} onChange={(e) => setStage(e.target.value)}>
          {stages?.map((s) => <option key={s.name} value={s.name}>{s.name}</option>)}
          <option value={NEW}>+ New stage…</option>
        </select>
      </label>
      {stage === NEW && (
        <label className="field">New stage name (added last — reorder in Settings → Stages)
          <input className="input" placeholder="e.g. dev" value={newStage} onChange={(e) => setNewStage(e.target.value.toLowerCase())} />
        </label>
      )}
      <label className="field">Tenant
        <select className="select" value={pickedTenant} onChange={(e) => setTenant(e.target.value)}>
          {tenantList.map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
          <option value={NEW}>+ New tenant…</option>
        </select>
      </label>
      {pickedTenant === NEW && (
        <label className="field">New tenant name
          <input className="input" placeholder="e.g. DS" value={newTenant} onChange={(e) => setNewTenant(e.target.value)} />
        </label>
      )}
      <Msg msg={w.msg} />
      <div className="row" style={{ justifyContent: "flex-end" }}>
        <button className="btn" onClick={onClose}>Cancel</button>
        <button className="btn primary" disabled={!valid || w.busy} onClick={create}>Create and show token</button>
      </div>
    </Dialog>
  );
}

function EditDialog({ c, onClose }: { c: Cluster; onClose: () => void }) {
  const { stages } = useAppData();
  const tenants = useApi(api.tenants);
  const [stage, setStage] = useState(c.stage);
  const [tenant, setTenant] = useState(c.tenant);
  const w = useWrite();
  return (
    <Dialog title={`Edit ${c.name}`} onClose={onClose}>
      <label className="field">Stage
        <select className="select" value={stage} onChange={(e) => setStage(e.target.value)}>
          {stages?.map((s) => <option key={s.name} value={s.name}>{s.name}</option>)}
        </select>
      </label>
      <label className="field">Tenant
        <select className="select" value={tenant} onChange={(e) => setTenant(e.target.value)}>
          {(tenants.data ?? [{ name: c.tenant, clusters: 1 }]).map((t) => <option key={t.name} value={t.name}>{t.name}</option>)}
        </select>
      </label>
      <p className="subtle" style={{ margin: 0 }}>The name can't be changed: history and exports refer to it. The agent's token stays valid. New tenants are added in Settings → Tenants.</p>
      <Msg msg={w.msg} />
      <div className="row" style={{ justifyContent: "flex-end" }}>
        <button className="btn" onClick={onClose}>Cancel</button>
        <button className="btn primary" disabled={w.busy || (stage === c.stage && tenant === c.tenant)}
          onClick={async () => { if (await w.run(() => api.editCluster(c.name, { stage, tenant }))) onClose(); }}>Save</button>
      </div>
    </Dialog>
  );
}

function DeleteDialog({ c, onClose }: { c: Cluster; onClose: () => void }) {
  const [typed, setTyped] = useState("");
  const w = useWrite();
  async function exportFirst() {
    const data = await w.run(() => api.exportCluster(c.name), "Exported.");
    if (data) download(`cluster-${c.name}-${new Date().toISOString().slice(0, 10)}.json`, JSON.stringify(data, null, 2), "application/json");
  }
  return (
    <Dialog title={`Delete ${c.name}?`} onClose={onClose}>
      <p style={{ margin: 0 }}>This permanently removes the cluster's token, current state and <b>full change history</b>. Its agent is rejected right away.</p>
      <div className="callout info"><Icon.info /><span>To cut off an agent but keep the data, use <b>Rotate token</b> instead.</span></div>
      <div className="row">
        <button className="btn" disabled={w.busy} onClick={exportFirst}>Export cluster data (JSON) first</button>
        <span className="subtle" style={{ margin: 0 }}>Settings, workloads, change history.</span>
      </div>
      <label className="field">
        <span>Type <b><i>{c.name}</i></b> to confirm</span>
        <input className="input mono" value={typed} onChange={(e) => setTyped(e.target.value)} aria-label={`Type ${c.name} to confirm`} />
      </label>
      <Msg msg={w.msg} />
      <div className="row" style={{ justifyContent: "flex-end" }}>
        <button className="btn" onClick={onClose}>Cancel</button>
        <button className="btn danger solid" disabled={typed !== c.name || w.busy}
          onClick={async () => { if (await w.run(() => api.deleteCluster(c.name))) onClose(); }}>Delete cluster</button>
      </div>
    </Dialog>
  );
}

/** Shown once after Add cluster / Rotate token. The token exists only in this component's memory. */
function TokenDialog({ cluster, token, rotated, serverURL, onClose }: {
  cluster: string; token: string; rotated: boolean; serverURL: string; onClose: () => void;
}) {
  const [copied, setCopied] = useState("");
  const ctx = `--context ${cluster}`;
  const secretFile = `agent-secret-${cluster}.yaml`;
  const commands = rotated
    ? `kubectl ${ctx} apply -f ${secretFile}\nkubectl ${ctx} -n service-inventory rollout restart deploy/inventory-agent\nrm ${secretFile}`
    : `kubectl ${ctx} apply -f agent.yaml\nkubectl ${ctx} apply -f ${secretFile}\nrm ${secretFile}`;
  async function copy(what: string, text: string) {
    try { await navigator.clipboard.writeText(text); setCopied(what); } catch { setCopied("failed"); }
  }
  return (
    <Dialog title={rotated ? `New token for ${cluster}` : `Install the agent on ${cluster}`}>
      <div className="callout warn"><Icon.warn /><span><b>Shown only once.</b> After you close this, the token can't be seen again. If you lose it, rotate the token.</span></div>
      {!serverURL && <div className="callout warn"><Icon.warn /><span>No server URL is set (Settings → Clusters), so the Secret contains a placeholder. Edit it before applying.</span></div>}
      <div className="stack">
        <h2>1 · The token Secret</h2>
        <div className="row">
          <button className="btn primary" onClick={() => download(secretFile, agentSecretYAML(serverURL, token))}>Download {secretFile}</button>
          <button className="btn" onClick={() => copy("token", token)}>{copied === "token" ? "Token copied" : "Copy raw token"}</button>
        </div>
        <p className="subtle" style={{ margin: 0 }}>The file is the safe default: typing <code>--from-literal=INV_TOKEN=…</code> leaves the token in your shell history. Use the raw token if you manage Secrets another way (Sealed Secrets, Vault).</p>
      </div>
      <div className="stack">
        <h2>2 · Apply</h2>
        <pre className="code">{commands}</pre>
        <div className="row">
          <button className="btn small" onClick={() => copy("cmds", commands)}>{copied === "cmds" ? "Commands copied" : "Copy commands"}</button>
          {!rotated && <a className="btn small" href={api.agentManifestURL} download="agent.yaml">Download agent.yaml</a>}
        </div>
        {copied === "failed" && <p className="err-text">Copy failed (the clipboard needs HTTPS or localhost). Select the text instead.</p>}
      </div>
      <div className="callout info"><Icon.info /><span>The cluster shows <b>waiting for first report</b> until the agent checks in (usually under a minute).</span></div>
      <div className="row" style={{ justifyContent: "flex-end" }}>
        <button className="btn primary" onClick={() => { if (confirm("Close? The token won't be shown again.")) onClose(); }}>I saved the token — close</button>
      </div>
    </Dialog>
  );
}

// ---------------- stages & tenants ----------------

function StagesTab() {
  const { stages, reload } = useAppData();
  const [name, setName] = useState("");
  const w = useWrite();
  const canWrite = adminToken.get() !== "";
  if (!stages) return <Loading />;
  const names = stages.map((s) => s.name);
  const move = (i: number, d: number) => {
    const next = [...names];
    [next[i], next[i + d]] = [next[i + d], next[i]];
    w.run(() => api.reorderStages(next)).then((r) => r && reload());
  };
  return (
    <Card title="Stages" actions={<span className="subtle">Order = promotion path. Drift compares clusters of the same tenant along this order.</span>}>
      <div className="stack narrow">
        {stages.map((s, i) => (
          <div key={s.name} className="list-row">
            <span className="num">{i + 1}</span>
            <span className="grow" style={{ fontWeight: 600 }}>{s.name}</span>
            <span className="subtle" style={{ margin: 0 }}>{s.clusters ? `${s.clusters} ${s.clusters === 1 ? "cluster" : "clusters"}` : "unused"}</span>
            <button className="btn small" disabled={!canWrite || w.busy || i === 0} onClick={() => move(i, -1)} aria-label={`Move ${s.name} up`}>↑</button>
            <button className="btn small" disabled={!canWrite || w.busy || i === stages.length - 1} onClick={() => move(i, 1)} aria-label={`Move ${s.name} down`}>↓</button>
            <button className="btn small danger" disabled={!canWrite || w.busy || s.clusters > 0} title={s.clusters ? "In use — move its clusters first" : undefined}
              onClick={() => w.run(() => api.deleteStage(s.name), `Deleted ${s.name}.`).then((r) => r && reload())}>Delete</button>
          </div>
        ))}
        <form className="row" onSubmit={async (e) => {
          e.preventDefault();
          if (await w.run(() => api.addStage(name.trim()), `Added ${name.trim()}.`)) { setName(""); reload(); }
        }}>
          <input className="input" placeholder="New stage, e.g. dev" aria-label="New stage name" value={name} onChange={(e) => setName(e.target.value.toLowerCase())} />
          <button className="btn" disabled={!canWrite || !name.trim() || w.busy}>Add stage</button>
        </form>
        <Msg msg={w.msg} />
      </div>
    </Card>
  );
}

function TenantsTab() {
  const tenants = useApi(api.tenants);
  const { reload } = useAppData();
  const [name, setName] = useState("");
  const [editing, setEditing] = useState<string | null>(null);
  const [newName, setNewName] = useState("");
  const w = useWrite();
  const canWrite = adminToken.get() !== "";
  const done = () => { tenants.reload(); reload(); };
  if (tenants.error) return <ErrorBox error={tenants.error} retry={tenants.reload} />;
  if (!tenants.data) return <Loading />;
  return (
    <Card title="Tenants" actions={<span className="subtle">Teams using the clusters. A filter, and drift only compares clusters of the same tenant.</span>}>
      <div className="stack narrow">
        {tenants.data.length === 0 && <Empty>No tenants yet.</Empty>}
        {tenants.data.map((t) => (
          <div key={t.name} className="list-row">
            {editing === t.name ? (
              <form className="row grow" onSubmit={async (e) => {
                e.preventDefault();
                if (await w.run(() => api.renameTenant(t.name, newName.trim()), `Renamed ${t.name} to ${newName.trim()}.`)) { setEditing(null); done(); }
              }}>
                <input className="input grow" value={newName} onChange={(e) => setNewName(e.target.value)} aria-label={`New name for ${t.name}`} autoFocus />
                <button className="btn small primary" disabled={!newName.trim() || w.busy}>Save</button>
                <button type="button" className="btn small" onClick={() => setEditing(null)}>Cancel</button>
              </form>
            ) : (
              <>
                <span className="grow" style={{ fontWeight: 600 }}>{t.name}</span>
                <span className="subtle" style={{ margin: 0 }}>{t.clusters ? `${t.clusters} ${t.clusters === 1 ? "cluster" : "clusters"}` : "unused"}</span>
                <button className="btn small" disabled={!canWrite || w.busy} onClick={() => { setEditing(t.name); setNewName(t.name); }}>Rename</button>
                <button className="btn small danger" disabled={!canWrite || w.busy || t.clusters > 0} title={t.clusters ? "In use — move its clusters first" : undefined}
                  onClick={() => w.run(() => api.deleteTenant(t.name), `Deleted ${t.name}.`).then((r) => r && done())}>Delete</button>
              </>
            )}
          </div>
        ))}
        <form className="row" onSubmit={async (e) => {
          e.preventDefault();
          if (await w.run(() => api.addTenant(name.trim()), `Added ${name.trim()}.`)) { setName(""); done(); }
        }}>
          <input className="input" placeholder="New tenant, e.g. Data" aria-label="New tenant name" value={name} onChange={(e) => setName(e.target.value)} />
          <button className="btn" disabled={!canWrite || !name.trim() || w.busy}>Add tenant</button>
        </form>
        <p className="subtle" style={{ margin: 0 }}>Renaming updates every cluster of that tenant. A tenant with clusters can't be deleted — move them first (Clusters → Edit).</p>
        <Msg msg={w.msg} />
      </div>
    </Card>
  );
}

// ---------------- policy, overrides, token ----------------

function PolicyTab() {
  const policies = useApi(api.policies);
  const [value, setValue] = useState<string | null>(null);
  const w = useWrite();
  if (policies.error) return <ErrorBox error={policies.error} retry={policies.reload} />;
  if (!policies.data) return <Loading />;
  const current = policies.data.global;
  const v = value ?? current;
  return (
    <Card title="Global version policy">
      <form className="row" onSubmit={async (e) => {
        e.preventDefault();
        if (await w.run(() => api.setPolicy("global", v), "Saved.")) { setValue(null); policies.reload(); }
      }}>
        <input className="input mono" list="gpresets" value={v} onChange={(e) => setValue(e.target.value)} aria-label="Global policy" />
        <datalist id="gpresets">{policies.data.presets.filter((p) => !p.includes("<")).map((p) => <option key={p} value={p} />)}</datalist>
        <button className="btn primary" disabled={!v || v === current || w.busy || !adminToken.get()}>Save</button>
      </form>
      <p className="subtle" style={{ margin: 0 }}><code>lts-or-n-1</code> (default): newest LTS line if the product has one, otherwise the previous release line. Per-component policies are set on the component page.</p>
      {Object.keys(policies.data.overrides).length > 0 && (
        <div className="row">Overrides: {Object.entries(policies.data.overrides).map(([k, p]) => <Link key={k} to={`/components/${encodeURIComponent(k)}`}><Tag>{k}: {p}</Tag></Link>)}</div>
      )}
      <Msg msg={w.msg} />
    </Card>
  );
}

function OverridesTab() {
  const overrides = useApi(api.overrides);
  const catalog = useApi(api.catalog);
  const w = useWrite();
  if (overrides.error) return <ErrorBox error={overrides.error} retry={overrides.reload} />;
  if (!overrides.data) return <Loading />;
  return (
    <Card title="Catalog overrides" actions={<span className="subtle">Your decisions about unrecognized images. They apply on top of the built-in catalog.</span>}>
      {overrides.data.length === 0 ? <Empty>No overrides yet. Open a cluster and use Map / In-house / Ignore on an unrecognized image.</Empty> : (
        <div className="stack">
          {overrides.data.map((o) => (
            <div key={o.repository} className="list-row">
              <span className="mono grow" style={{ wordBreak: "break-all" }}>{o.repository}</span>
              <span className="tag tone-ok">{overrideText(o, catalog.data?.components ?? [])}</span>
              <span className="subtle" style={{ margin: 0 }}>{ago(o.updatedAt)}</span>
              <button className="btn small" disabled={w.busy || !adminToken.get()}
                onClick={() => w.run(() => api.deleteOverride(o.repository), `Undone: ${o.repository}.`).then((r) => r && overrides.reload())}>Undo</button>
            </div>
          ))}
        </div>
      )}
      <Msg msg={w.msg} />
    </Card>
  );
}

function TokenTab() {
  const [value, setValue] = useState("");
  const [has, setHas] = useState(adminToken.get() !== "");
  return (
    <Card title="Admin token">
      <p className="subtle" style={{ margin: 0 }}>Needed for changes. Kept only in this browser tab (session storage) and sent only with write requests.</p>
      {has ? (
        <div className="row"><Tag tone="ok">token set for this tab</Tag><button className="btn" onClick={() => { adminToken.set(""); setHas(false); }}>Forget</button></div>
      ) : (
        <form className="row" onSubmit={(e) => { e.preventDefault(); adminToken.set(value.trim()); setValue(""); setHas(true); }}>
          <input className="input mono wide" type="password" autoComplete="off" placeholder="INV_ADMIN_TOKEN" value={value} onChange={(e) => setValue(e.target.value)} aria-label="Admin token" />
          <button className="btn primary" disabled={!value.trim()}>Use</button>
        </form>
      )}
    </Card>
  );
}
