import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { useIsAdmin, api, LEVELS, writeErrorText, type CatalogOverride, type Inventory, type Report } from "../api";
import { useAppData } from "../App";
import { paginate, usePageSize, useQueryState } from "../model";
import { FLAG, unrecognizedIn, workloadCount, workloadsFor, type WorkloadRow } from "../clusterview";
import LevelHelp from "../components/LevelHelp";
import { ago, Card, Empty, ErrorBox, LevelBadge, LevelBar, levelLabel, Loading, Msg, Pager, useApi } from "../ui";
import { clusterCounts, k8sStatus } from "./Overview";

type Tab = "all" | "third" | "inhouse" | "unrecognized";
const TABS: [Tab, string][] = [["all", "All"], ["third", "Third-party"], ["inhouse", "In-house"], ["unrecognized", "Unrecognized"]];

export default function ClusterDetail() {
  const { name = "" } = useParams();
  const { clusters, error } = useAppData();
  const report = useApi(api.report);
  const inv = useApi(api.inventory);
  // Which workloads are shown and the page live in the URL (Back from a component page restores them).
  const qs = useQueryState();
  const tab: Tab = (TABS.map(([t]) => t) as string[]).includes(qs.get("show")) ? (qs.get("show") as Tab) : "all";
  const setTab = (t: Tab) => qs.set({ show: t === "all" ? null : t });
  const [pageSize, setPageSize] = usePageSize();

  if (error || report.error || inv.error) return <ErrorBox error={error ?? report.error ?? inv.error} retry={() => { report.reload(); inv.reload(); }} />;
  if (!clusters || !report.data || !inv.data) return <Loading />;
  const cl = clusters.find((c) => c.name === name);
  if (!cl) return <Empty>Cluster “{name}” not found. <Link to="/clusters">All clusters</Link></Empty>;

  const reload = () => { report.reload(); inv.reload(); };
  const rows = workloadsFor(cl.name, report.data, inv.data);
  const shown = rows.filter((r) => tab === "all" || r.group === tab || (tab === "unrecognized" && r.group === "ignored"));
  const pg = paginate(shown, Number(qs.get("page")), pageSize);
  const counts = clusterCounts(report.data.components, cl.name);
  const total = counts.reduce((a, c) => a + c.n, 0);
  const k8s = k8sStatus(report.data.components, cl.name);

  return (
    <>
      <p className="crumbs"><Link to="/clusters">Clusters</Link> / {cl.name}</p>
      <div className="page-head">
        <div className="stack" style={{ gap: 4 }}>
          <div className="with-badge"><h1>{cl.name}</h1><span className="tag tone-info">{cl.stage}</span><span className="tag">Tenant {cl.tenant}</span></div>
          <p className="muted">{cl.lastSeenAt ? `Agent ${cl.agentVersion || "?"} · last report ${ago(cl.lastSeenAt)} · agents report every ~15 min` : "Waiting for the first report from its agent."}</p>
        </div>
        <Link to="/settings/clusters" className="btn">Manage cluster</Link>
      </div>

      <Card>
        <div className="kv-grid">
          <div><span>Kubernetes</span><span className="mono big">{cl.kubernetesVersion || "—"}</span></div>
          <div><span>Kubernetes status</span>{k8s ? <span className={`badge lvl-${k8s.level}`}>{k8s.text}</span> : "—"}</div>
          <div><span>Workloads</span><span className="big">{workloadCount(rows)}</span></div>
        </div>
        <div className="stack" style={{ gap: 10, paddingTop: 14, borderTop: "1px solid var(--border-2)" }}>
          <div className="row" style={{ justifyContent: "space-between" }}>
            <span className="subtle" style={{ margin: 0 }}>Components by level</span>
            <span className="subtle" style={{ margin: 0 }}>{total} components</span>
          </div>
          <LevelBar counts={counts} />
          <div className="lvtiles">
            {LEVELS.map((l) => {
              const n = counts.find((c) => c.level === l)?.n ?? 0;
              return (
                <div key={l} className={`lvtile lvl-${l}${n ? "" : " zero"}`}>
                  <span className="n">{n}</span>
                  <span className="l"><span className="dot" aria-hidden />{levelLabel[l]}</span>
                </div>
              );
            })}
          </div>
        </div>
      </Card>

      <Card title="Workloads" actions={
        <div className="row" role="group" aria-label="Show">
          {TABS.map(([t, label]) => (
            <button key={t} className={`chip${tab === t ? " on" : ""}`} aria-pressed={tab === t} onClick={() => setTab(t)}>{label}</button>
          ))}
        </div>
      }>
        {shown.length === 0 ? <Empty>Nothing in this view.</Empty> : <WorkloadTable rows={pg.items} />}
        <FlagLegend rows={rows} />
        <LevelHelp />
        <Pager page={pg.page} pages={pg.pages} start={pg.start} shown={pg.items.length} total={pg.total} size={pageSize}
          onPage={(p) => qs.set({ page: p })} onSize={(n) => { setPageSize(n); qs.set({ page: null }); }} />
      </Card>

      <UnrecognizedCard cluster={cl.name} inv={inv.data} report={report.data} onChanged={reload} />
    </>
  );
}

/** Explains the flags that occur in this cluster. */
function FlagLegend({ rows }: { rows: WorkloadRow[] }) {
  const used = Object.values(FLAG).filter((f) => rows.some((r) => r.flags.some((x) => x.text === f.text)));
  if (used.length === 0) return null;
  return (
    <dl className="flag-legend">
      {used.map((f) => (
        <div key={f.text}><dt><span className={`tag ${f.tone ? `tone-${f.tone}` : ""}`}>{f.text}</span></dt><dd>{f.title}</dd></div>
      ))}
    </dl>
  );
}

function WorkloadTable({ rows }: { rows: WorkloadRow[] }) {
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th>Namespace / workload</th><th>Kind</th><th>Image</th><th>Component</th><th>Version</th><th>Level and why</th></tr></thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key}>
              <td><span className="muted">{r.namespace} /</span> <b>{r.workload}</b>{r.container && r.container !== r.workload && <div className="subtle">container {r.container}</div>}</td>
              <td className="small">{r.kind}</td>
              <td className="mono" style={{ wordBreak: "break-all" }}>{r.image}</td>
              <td>{r.componentId ? <Link to={`/components/${encodeURIComponent(r.componentId)}`}>{r.componentName}</Link> : <span className="muted">—</span>}</td>
              <td className="mono">{r.version || "—"}</td>
              <td className="why-cell">
                <div className="row" style={{ gap: 6 }}>
                  {r.level && <LevelBadge level={r.level} />}
                  {r.flags.map((f, k) => <span key={k} className={`tag ${f.tone ? `tone-${f.tone}` : ""}`} title={f.title}>{f.text}</span>)}
                </div>
                {r.level && r.level !== "ok" && r.action && (
                  <div className="subtle" title={r.reasons?.join("\n")}>
                    <b>{r.action}</b>{r.why && ` — ${r.why}`}
                    {(r.reasons?.length ?? 0) > 0 && r.componentId && <> · <Link to={`/components/${encodeURIComponent(r.componentId)}`}>{r.reasons!.length} {r.reasons!.length === 1 ? "reason" : "reasons"}</Link></>}
                  </div>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Unrecognized images in this cluster, with map / in-house / ignore. Decisions apply to the repository in every cluster. */
function UnrecognizedCard({ cluster, inv, report, onChanged }: { cluster: string; inv: Inventory; report: Report; onChanged: () => void }) {
  const catalog = useApi(api.catalog);
  const overrides = useApi(api.overrides);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const canWrite = useIsAdmin();
  const open = unrecognizedIn(cluster, inv);
  // Repositories in this cluster that an override decided (mapped ones are scored, so they're in the report).
  const decidedRepos = new Set([...inv.inHouse, ...inv.ignored, ...report.components.flatMap((c) => c.instances)]
    .filter((i) => i.cluster === cluster && i.override && i.repository).map((i) => i.repository!));
  const decided = (overrides.data ?? []).filter((o) => decidedRepos.has(o.repository));

  async function run(fn: () => Promise<unknown>, ok: string) {
    setBusy(true); setMsg(null);
    try { await fn(); setMsg({ ok: true, text: ok }); overrides.reload(); onChanged(); }
    catch (e) { setMsg({ ok: false, text: writeErrorText(e) }); }
    finally { setBusy(false); }
  }
  const components = (catalog.data?.components ?? []).filter((c) => c.source !== "cluster");

  return (
    <Card title={`Unrecognized images (${open.length})`} actions={<span className="subtle">A choice applies to the image in every cluster. Undo it here or in Settings → Catalog.</span>}>
      {!canWrite && <div className="callout info">Only admins can map, mark or ignore images.</div>}
      {open.length === 0 && <Empty>Every image in this cluster is recognized.</Empty>}
      {open.map((u) => {
        const here = u.instances.filter((i) => i.cluster === cluster);
        const elsewhere = new Set(u.instances.filter((i) => i.cluster !== cluster).map((i) => i.cluster)).size;
        return (
          <div key={u.repository} className="inst" style={{ flexDirection: "row", flexWrap: "wrap", alignItems: "center", justifyContent: "space-between", gap: 12 }}>
            <div style={{ minWidth: 0 }}>
              <div className="mono" style={{ fontWeight: 500 }}>{u.repository}</div>
              <div className="subtle">
                {here.map((i) => `${i.namespace}/${i.workload} (${i.image.split(":").pop()})`).join(", ")}
                {elsewhere > 0 && ` · also in ${elsewhere} other ${elsewhere === 1 ? "cluster" : "clusters"}`}
              </div>
            </div>
            <div className="row">
              {canWrite && <Link className="btn small primary" to={`/settings/catalog?new=${encodeURIComponent(u.repository)}`}
                title="Add it to Owl's catalog: GitHub repo, release data, version rules">New component</Link>}
              <label className="row small" style={{ gap: 6 }}>Map to
                <select className="select" disabled={busy || !canWrite} value="" aria-label={`Map ${u.repository} to a component`}
                  onChange={(e) => e.target.value && run(() => api.setOverride({ repository: u.repository, kind: "map", component: e.target.value }), `Mapped ${u.repository}.`)}>
                  <option value="">component…</option>
                  {components.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
                </select>
              </label>
              <button className="btn small" disabled={busy || !canWrite} onClick={() => run(() => api.setOverride({ repository: u.repository, kind: "inhouse" }), `Marked ${u.repository} in-house.`)}>In-house</button>
              <button className="btn small" disabled={busy || !canWrite} onClick={() => run(() => api.setOverride({ repository: u.repository, kind: "ignore" }), `Ignoring ${u.repository}.`)}>Ignore</button>
            </div>
          </div>
        );
      })}
      {decided.length > 0 && (
        <div className="stack">
          <span className="subtle" style={{ margin: 0 }}>Decided for images in this cluster</span>
          {decided.map((o) => (
            <div key={o.repository} className="list-row">
              <span className="mono grow" style={{ wordBreak: "break-all" }}>{o.repository}</span>
              <span className="tag tone-ok">{overrideText(o, components)}</span>
              <button className="btn small" disabled={busy || !canWrite} onClick={() => run(() => api.deleteOverride(o.repository), `Undone: ${o.repository} is unrecognized again.`)}>Undo</button>
            </div>
          ))}
        </div>
      )}
      <Msg msg={msg} />
    </Card>
  );
}

export function overrideText(o: CatalogOverride, components: { id: string; name: string }[]) {
  if (o.kind === "map") return `Mapped to ${components.find((c) => c.id === o.component)?.name ?? o.component}`;
  return o.kind === "inhouse" ? "In-house" : "Ignored";
}
