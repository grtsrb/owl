import { useEffect, useState, type ReactNode } from "react";
import { Link, Navigate, NavLink, useLocation, useNavigate, useParams } from "react-router-dom";
import { agentSecretYAML, api, ApiError, download, useIsAdmin, writeErrorText, type Cluster, type OpsSettings, type ServerSettings } from "../api";
import { paginate, usePageSize } from "../model";
import { useAppData } from "../App";
import { Icon } from "../icons";
import { ago, Card, date, Empty, ErrorBox, Loading, Msg, Pager, Tag, useApi } from "../ui";
import { overrideText } from "./ClusterDetail";
import NotificationsTab from "./SettingsNotifications";
import SummariesTab from "./SettingsSummaries";
import CatalogComponents from "./SettingsCatalog";
import { UsersTab } from "./SettingsUsers";

type M = { ok: boolean; text: string } | null;

type Section = { id: string; label: string; admin?: boolean };
const GROUPS: { title: string; items: Section[] }[] = [
  { title: "Inventory", items: [{ id: "clusters", label: "Clusters" }, { id: "stages", label: "Stages & tenants" },
    { id: "catalog", label: "Catalog" }, { id: "policy", label: "Version policy" }] },
  { title: "Integrations", items: [{ id: "notifications", label: "Notifications", admin: true }, { id: "summaries", label: "Claude summaries", admin: true }] },
  { title: "System", items: [{ id: "users", label: "Users", admin: true }, { id: "backups", label: "Backups & restore", admin: true },
    { id: "logs", label: "Logs & audit", admin: true }] },
];
// Sections that moved (old links and bookmarks keep working).
const MOVED: Record<string, string> = { tenants: "/settings/stages", overrides: "/settings/catalog", account: "/account" };

export default function Settings() {
  const { tab = "clusters" } = useParams();
  const admin = useIsAdmin();
  const navigate = useNavigate();
  const { search } = useLocation();
  if (MOVED[tab]) return <Navigate to={MOVED[tab] + search} replace />;
  const groups = GROUPS.map((g) => ({ ...g, items: g.items.filter((i) => admin || !i.admin) })).filter((g) => g.items.length > 0);
  const all = GROUPS.flatMap((g) => g.items);
  const current = all.find((i) => i.id === tab);
  return (
    <>
      <div className="page-head">
        <div>
          <h1>Settings</h1>
          <p className="muted">{admin ? "You're an admin: you can change everything here." : "You're a viewer: settings are read-only. Ask an admin to make changes."}</p>
        </div>
      </div>
      <div className="settings">
        <nav className="settings-nav" aria-label="Settings sections">
          {groups.map((g) => (
            <div key={g.title} role="group" aria-label={g.title}>
              <div className="settings-group">{g.title}</div>
              <div className="settings-items">
                {g.items.map((i) => <NavLink key={i.id} to={`/settings/${i.id}`} className={({ isActive }) => `settings-link${isActive ? " active" : ""}`}>{i.label}</NavLink>)}
              </div>
            </div>
          ))}
        </nav>
        <select className="select settings-select" aria-label="Settings section" value={current?.id ?? ""} onChange={(e) => navigate(`/settings/${e.target.value}`)}>
          {groups.map((g) => <optgroup key={g.title} label={g.title}>{g.items.map((i) => <option key={i.id} value={i.id}>{i.label}</option>)}</optgroup>)}
        </select>
        <div className="settings-body">
          {!current ? <Empty>Unknown section.</Empty>
            : current.admin && !admin ? <Empty>Only admins can see this section.</Empty>
            : <>
              {tab === "clusters" && <ClustersTab />}
              {tab === "stages" && <><StagesTab /><TenantsTab /></>}
              {tab === "catalog" && <><CatalogComponents /><OverridesTab /></>}
              {tab === "policy" && <PolicyTab />}
              {tab === "summaries" && <SummariesTab />}
              {tab === "notifications" && <NotificationsTab />}
              {tab === "backups" && <BackupsTab />}
              {tab === "logs" && <LogsTab />}
              {tab === "users" && <UsersTab />}
            </>}
        </div>
      </div>
    </>
  );
}

export function useWrite() {
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

export function Dialog({ title, onClose, children }: { title: string; onClose?: () => void; children: ReactNode }) {
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
  const canWrite = useIsAdmin();

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
          {!initial.agentImage && "No agent image yet: agent.yaml uses owl:dev, which only exists on dev clusters."}
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
          <input className="input mono" placeholder="registry.example.com/devops/owl:0.7.0" value={v.agentImage} onChange={(e) => setV({ ...v, agentImage: e.target.value })} />
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
        <label className="field">New stage name (added last — reorder in Settings → Stages & tenants)
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
      <p className="subtle" style={{ margin: 0 }}>The name can't be changed: history and exports refer to it. The agent's token stays valid. New tenants are added in Settings → Stages & tenants.</p>
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
    ? `kubectl ${ctx} apply -f ${secretFile}\nkubectl ${ctx} -n owl rollout restart deploy/owl-agent\nrm ${secretFile}`
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
        <p className="subtle" style={{ margin: 0 }}>The file is the safe default: typing <code>--from-literal=OWL_TOKEN=…</code> leaves the token in your shell history. Use the raw token if you manage Secrets another way (Sealed Secrets, Vault).</p>
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
  const canWrite = useIsAdmin();
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
  const canWrite = useIsAdmin();
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
  const hasToken = useIsAdmin();
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
        <button className="btn primary" disabled={!v || v === current || w.busy || !hasToken}>Save</button>
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
  const hasToken = useIsAdmin();
  if (overrides.error) return <ErrorBox error={overrides.error} retry={overrides.reload} />;
  if (!overrides.data) return <Loading />;
  const components = catalog.data?.components ?? [];
  const nameOf = (id: string) => components.find((c) => c.id === id)?.name ?? id;
  return (
    <Card title="Catalog overrides" actions={<span className="subtle">Your decisions about unrecognized images. They apply on top of the built-in catalog.</span>}>
      {overrides.data.length === 0 ? <Empty>No overrides yet. Open a cluster and use Map / In-house / Ignore on an unrecognized image.</Empty> : (
        <div className="stack">
          {overrides.data.map((o) => (
            <div key={o.repository} className="list-row" style={{ flexWrap: "wrap" }}>
              <span className="mono grow" style={{ wordBreak: "break-all" }}>{o.repository}</span>
              {o.kind === "map" && o.recognizedAs
                ? <Tag tone="warn" title="Owl's catalog (built-in or one of your components) now recognizes this image, so this mapping no longer applies">
                    Now covered by {nameOf(o.recognizedAs)} — remove?</Tag>
                : <span className="tag tone-ok">{overrideText(o, components)}</span>}
              <span className="subtle" style={{ margin: 0 }}>{ago(o.updatedAt)}</span>
              <button className="btn small" disabled={w.busy || !hasToken}
                onClick={() => w.run(() => api.deleteOverride(o.repository), `Removed: ${o.repository}.`).then((r) => r && overrides.reload())}>
                {o.kind === "map" && o.recognizedAs ? "Remove" : "Undo"}</button>
              {o.kind !== "map" && o.recognizedAs && (
                <div className="subtle" style={{ margin: 0, flexBasis: "100%" }}>
                  Owl now recognizes this as {nameOf(o.recognizedAs)}; your choice still applies. Undo it to track it as {nameOf(o.recognizedAs)}.
                </div>
              )}
            </div>
          ))}
        </div>
      )}
      <Msg msg={w.msg} />
    </Card>
  );
}

// ---------------- backups & restore ----------------

function size(n: number) {
  return n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`;
}

function BackupsTab() {
  const canWrite = useIsAdmin();
  if (!canWrite) return <Card title="Backups"><div className="callout info"><Icon.info /><span>Only admins can see and manage backups.</span></div></Card>;
  return <BackupsTabInner />;
}

function BackupsTabInner() {
  const ops = useApi(api.ops);
  const list = useApi(api.backups);
  const { reload: reloadData } = useAppData();
  const w = useWrite();
  const [confirm, setConfirm] = useState<{ what: string; run: () => Promise<{ safetyBackup: string }> } | null>(null);
  const [token, setToken] = useState<{ cluster: string; token: string } | null>(null);
  const settings = useApi(api.settings);

  if (ops.error) return <ErrorBox error={ops.error} retry={ops.reload} />;
  if (!ops.data) return <Loading />;
  if (!ops.data.backupsEnabled) return <Card title="Backups"><Empty>Backups are disabled on this server.</Empty></Card>;
  const st = ops.data.backup!;
  const refresh = () => { ops.reload(); list.reload(); };

  async function importFile(f: File) {
    const r = await w.run(async () => api.importCluster(await f.text()));
    if (r) { setToken({ cluster: r.cluster.name, token: r.token }); reloadData(); }
  }

  return (
    <>
      <Card title="Backups" actions={<button className="btn primary" disabled={w.busy}
        onClick={async () => { if (await w.run(() => api.backupNow(), "Backup taken.")) refresh(); }}>{w.busy ? "Working…" : "Back up now"}</button>}>
        {st.warnings.map((x) => <div key={x} className="callout warn"><Icon.warn /><span>{x}</span></div>)}
        <div className="kv-grid">
          <div><span>Copies go to</span>{st.target ? <span className="mono">{st.target}</span> : <b>this server only</b>}</div>
          <div><span>Last backup</span>{st.lastOK && !st.lastOK.startsWith("0001") ? `${ago(st.lastOK)} (${date(st.lastOK)})` : "never"}</div>
          <div><span>Next scheduled</span>{ago(st.nextRun)} <span className="subtle">({new Date(st.nextRun).toLocaleString()})</span></div>
        </div>
        <Msg msg={w.msg} />
      </Card>

      <BackupSettings ops={ops.data.settings} s3={!!st.target} onSaved={ops.reload} />

      <Card title="Stored backups" actions={<span className="subtle">{st.target ? "Local copies and the bucket" : `Local folder ${st.dir}`}</span>}>
        {list.error ? <ErrorBox error={list.error} retry={list.reload} /> : !list.data ? <Loading /> : (
          <>
            {list.data.remoteError && <div className="callout warn"><Icon.warn /><span>Bucket listing failed: {list.data.remoteError}</span></div>}
            {list.data.backups.length === 0 ? <Empty>No backups yet. “Back up now” takes the first one.</Empty> : (
              <div className="table-wrap">
                <table>
                  <thead><tr><th>Taken</th><th>Size</th><th>Where</th><th></th></tr></thead>
                  <tbody>
                    {list.data.backups.map((b) => (
                      <tr key={b.name}>
                        <td>{new Date(b.at).toLocaleString()}<div className="subtle mono">{b.name}</div></td>
                        <td>{size(b.size)}</td>
                        <td><div className="row" style={{ gap: 4 }}>
                          {b.local && <Tag>this server</Tag>}
                          {b.remote && <Tag tone="ok">bucket</Tag>}
                          {b.local && st.target && !b.uploaded && <Tag tone="warn">not uploaded yet</Tag>}
                        </div></td>
                        <td><div className="row" style={{ justifyContent: "flex-end", flexWrap: "nowrap" }}>
                          <button className="btn small" disabled={w.busy} onClick={() => w.run(() => api.downloadBackup(b.name))}>Download</button>
                          <button className="btn small danger" disabled={w.busy} onClick={() => setConfirm({ what: `backup ${b.name}`, run: () => api.restoreBackup(b.name) })}>Restore</button>
                        </div></td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </>
        )}
        <div className="row">
          <FileButton label="Restore from file…" accept=".gz,.db" disabled={w.busy}
            onFile={(f) => setConfirm({ what: `file ${f.name}`, run: () => api.restoreUpload(f) })} />
          <FileButton label="Import cluster export…" accept=".json,application/json" disabled={w.busy} onFile={importFile} />
        </div>
        <p className="subtle" style={{ margin: 0 }}>Import brings back one deleted cluster from its export (Delete → “Export cluster data”), with a new agent token.</p>
      </Card>

      {confirm && <RestoreDialog what={confirm.what} onClose={() => setConfirm(null)} onConfirm={async () => {
        const r = await w.run(confirm.run);
        setConfirm(null);
        if (r) { w.setMsg({ ok: true, text: `Restored from ${confirm.what}. Safety backup of the previous state: ${r.safetyBackup}.` }); refresh(); reloadData(); }
      }} />}
      {token && <TokenDialog cluster={token.cluster} token={token.token} rotated={false} serverURL={settings.data?.serverURL ?? ""} onClose={() => { setToken(null); reloadData(); }} />}
    </>
  );
}

function FileButton({ label, accept, disabled, onFile }: { label: string; accept: string; disabled: boolean; onFile: (f: File) => void }) {
  return (
    <label className={`btn${disabled ? " disabled" : ""}`} aria-disabled={disabled}>
      {label}
      <input type="file" accept={accept} disabled={disabled} className="visually-hidden"
        onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ""; if (f) onFile(f); }} />
    </label>
  );
}

function RestoreDialog({ what, onClose, onConfirm }: { what: string; onClose: () => void; onConfirm: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <Dialog title="Restore the database?" onClose={busy ? undefined : onClose}>
      <p style={{ margin: 0 }}>Clusters, tokens, state, history, stages, tenants, policies, overrides and settings are replaced with the contents of <b>{what}</b>. Before that, the server:</p>
      <ul className="bullets"><li>checks it's a valid backup it can read (older backups are upgraded to the current format)</li><li>takes a safety backup of the current state</li></ul>
      <div className="callout warn"><Icon.warn /><span>Clusters added after this backup won't exist afterwards — add them again. Agents keep working if their cluster was in the backup, except tokens rotated or clusters deleted since: those stay revoked (a restore never brings an old token back), so use Rotate token for them. The audit log is kept as it is.</span></div>
      <div className="row" style={{ justifyContent: "flex-end" }}>
        <button className="btn" disabled={busy} onClick={onClose}>Cancel</button>
        <button className="btn danger solid" disabled={busy} onClick={() => { setBusy(true); onConfirm(); }}>{busy ? "Restoring…" : "Restore"}</button>
      </div>
    </Dialog>
  );
}

function BackupSettings({ ops, s3, onSaved }: { ops: OpsSettings; s3: boolean; onSaved: () => void }) {
  const [v, setV] = useState(ops);
  const w = useWrite();
  const dirty = JSON.stringify(v) !== JSON.stringify(ops);
  return (
    <Card title="Schedule & retention">
      <form className="stack" onSubmit={async (e) => { e.preventDefault(); if (await w.run(() => api.saveOps(v), "Saved.")) onSaved(); }}>
        <div className="kv-grid">
          <label className="field">Daily backup at (server time)
            <input className="input mono" type="time" value={v.backupTime} onChange={(e) => setV({ ...v, backupTime: e.target.value })} />
          </label>
          <label className="field">{s3 ? "Local copies to keep" : "Keep local backups (days)"}
            {s3
              ? <input className="input" type="number" min={1} max={1000} value={v.backupLocalCopies} onChange={(e) => setV({ ...v, backupLocalCopies: Number(e.target.value) })} />
              : <input className="input" type="number" min={1} max={3650} value={v.backupLocalDays} onChange={(e) => setV({ ...v, backupLocalDays: Number(e.target.value) })} />}
          </label>
          <div><span className="subtle" style={{ display: "block", margin: 0 }}>Bucket retention</span>{s3 ? "Set by a lifecycle rule on the bucket. The server never deletes there." : "No bucket configured."}</div>
        </div>
        <p className="subtle" style={{ margin: 0 }}>{s3 ? "A local copy is never deleted before it's in the bucket." : "The newest backup is always kept."}</p>
        <div className="row"><button className="btn primary" disabled={!dirty || w.busy}>Save</button><Msg msg={w.msg} /></div>
      </form>
    </Card>
  );
}

// ---------------- logs & audit ----------------

function LogsTab() {
  if (!useIsAdmin()) return <Card title="Logs & audit"><div className="callout info"><Icon.info /><span>Only admins can see log settings and the audit log.</span></div></Card>;
  return <LogsTabInner />;
}

function LogsTabInner() {
  const ops = useApi(api.ops);
  const audit = useApi(() => api.audit());
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = usePageSize();
  if (ops.error) return <ErrorBox error={ops.error} retry={ops.reload} />;
  if (!ops.data) return <Loading />;
  const pg = paginate(audit.data ?? [], page, pageSize);
  return (
    <>
      <LogSettings ops={ops.data.settings} logFile={ops.data.logFile} onSaved={ops.reload} />
      <Card title="Audit log" actions={<span className="subtle">Every admin action: what, when, from where. Kept {ops.data.settings.auditRetentionDays} days.</span>}>
        {audit.error ? <ErrorBox error={audit.error} retry={audit.reload} /> : !audit.data ? <Loading /> :
          audit.data.length === 0 ? <Empty>No admin actions recorded yet.</Empty> : (
          <>
            <div className="table-wrap">
              <table>
                <thead><tr><th>When</th><th>Action</th><th>Details</th><th>From</th></tr></thead>
                <tbody>{pg.items.map((e) => (
                  <tr key={e.id}>
                    <td title={e.at}>{ago(e.at)}<div className="subtle">{new Date(e.at).toLocaleString()}</div></td>
                    <td><b>{e.action}</b>{e.actor && <div className="subtle">{e.actor}</div>}</td>
                    <td className="mono small" style={{ wordBreak: "break-word" }}>{e.details || "—"}</td>
                    <td className="mono">{e.remote}</td>
                  </tr>
                ))}</tbody>
              </table>
            </div>
            <Pager page={pg.page} pages={pg.pages} start={pg.start} shown={pg.items.length} total={pg.total} size={pageSize}
              onPage={setPage} onSize={(n) => { setPageSize(n); setPage(1); }} />
          </>
        )}
      </Card>
    </>
  );
}

function LogSettings({ ops, logFile, onSaved }: { ops: OpsSettings; logFile: boolean; onSaved: () => void }) {
  const [v, setV] = useState(ops);
  const w = useWrite();
  const dirty = JSON.stringify(v) !== JSON.stringify(ops);
  return (
    <Card title="Application logs">
      <form className="stack" onSubmit={async (e) => { e.preventDefault(); if (await w.run(() => api.saveOps(v), "Saved.")) onSaved(); }}>
        <div className="kv-grid">
          <label className="field">Log level
            <select className="select" value={v.logLevel} onChange={(e) => setV({ ...v, logLevel: e.target.value as OpsSettings["logLevel"] })}>
              <option value="debug">debug (every request, incl. static files)</option><option value="info">info</option><option value="warn">warn</option><option value="error">error</option>
            </select>
          </label>
          <label className="field">Keep log files (days)
            <input className="input" type="number" min={1} max={365} value={v.logRetentionDays} disabled={!logFile} onChange={(e) => setV({ ...v, logRetentionDays: Number(e.target.value) })} />
          </label>
          <label className="field">Keep audit entries (days)
            <input className="input" type="number" min={1} max={3650} value={v.auditRetentionDays} onChange={(e) => setV({ ...v, auditRetentionDays: Number(e.target.value) })} />
          </label>
        </div>
        <p className="subtle" style={{ margin: 0 }}>{logFile ? "JSON logs go to stderr and to logs/server.log next to the database: a new file every day (or at 50 MB), older files gzipped." : "Logs go to stderr only (no log directory configured)."} Tokens and Authorization headers are never logged.</p>
        <div className="row"><button className="btn primary" disabled={!dirty || w.busy}>Save</button><Msg msg={w.msg} /></div>
      </form>
    </Card>
  );
}
