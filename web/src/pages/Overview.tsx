import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api, LEVELS, type Cluster, type ComponentReport, type Level, type ScoredInstance } from "../api";
import { useAppData, useReporting } from "../App";
import ComponentPanel from "../components/ComponentPanel";
import LevelHelp from "../components/LevelHelp";
import { SummaryHeadline } from "../components/Summaries";
import { LEGEND, needsByCluster, Note, notesFor, paginate, useClusterSelection, usePageSize, useQueryState, worstInstance, type NoteItem } from "../model";
import { ago, Card, CountBadges, Empty, ErrorBox, LevelBadge, LevelBar, levelCounts, levelLabel, levelRank, Loading, Pager, useApi, worstLevel } from "../ui";

/** A component as seen through the cluster selection. */
export interface Row {
  c: ComponentReport;
  insts: ScoredInstance[]; // in selected clusters only
  level: Level;
  worst: ScoredInstance;
  notes: NoteItem[];
  clusters: string[];
  inLive: boolean;
}

export function rowsFor(components: ComponentReport[], selected: string[], lastStage: string): Row[] {
  const sel = new Set(selected);
  const out: Row[] = [];
  for (const c of components) {
    const insts = c.instances.filter((i) => sel.has(i.cluster));
    const worst = worstInstance(insts);
    if (!worst) continue;
    out.push({
      c, insts, worst, level: worstLevel(insts.map((i) => i.score.level)), notes: notesFor(c, insts, selected),
      clusters: [...new Set(insts.map((i) => i.cluster))], inLive: insts.some((i) => i.stage === lastStage),
    });
  }
  return out.sort((a, b) => levelRank[b.level] - levelRank[a.level] || a.c.name.localeCompare(b.c.name));
}

export default function Overview() {
  const report = useApi(api.report);
  const { error: dataError, stageNames } = useAppData();
  const reporting = useReporting();
  const sel = useClusterSelection(reporting);
  // Level filter, search and page live in the URL (Back / bookmarks restore the view).
  const qs = useQueryState();
  const level = qs.get("level");
  const filter: Level | "all" = (LEVELS as string[]).includes(level) ? (level as Level) : "all";
  const query = qs.get("q");
  const setFilter = (l: Level | "all") => qs.set({ level: l === "all" ? null : l });
  const [pageSize, setPageSize] = usePageSize();
  const [panel, setPanel] = useState<string | null>(null);
  // Summaries are extra: if they fail to load, the Overview works as before.
  const summaries = useApi(api.summaries);
  const jumpsOf = (id: string) => summaries.data?.jumps.filter((j) => j.component === id) ?? [];

  const lastStage = stageNames[stageNames.length - 1] ?? "live";
  const rows = useMemo(() => (report.data ? rowsFor(report.data.components, sel.selected, lastStage) : []),
    [report.data, sel.selected, lastStage]);

  if (report.error || dataError) return <ErrorBox error={report.error ?? dataError} retry={report.reload} />;
  if (!report.data) return <Loading />;
  if (reporting.length === 0) {
    return (
      <>
        <div className="page-head"><h1>Overview</h1></div>
        <Empty>No cluster has reported yet. <Link to="/settings/clusters">Add a cluster</Link> and install its agent.</Empty>
      </>
    );
  }

  const cols = reporting.filter((c) => sel.selected.includes(c.name));
  const counts: Partial<Record<Level, number>> = {};
  for (const r of rows) counts[r.level] = (counts[r.level] ?? 0) + 1;
  const shown = rows.filter((r) => (filter === "all" || r.level === filter) && r.c.name.toLowerCase().includes(query.toLowerCase()));
  const pg = paginate(shown, Number(qs.get("page")), pageSize);
  const top = [...rows]
    .filter((r) => levelRank[r.level] >= levelRank.medium)
    .sort((a, b) => levelRank[b.level] - levelRank[a.level] || Number(b.inLive) - Number(a.inLive) || b.clusters.length - a.clusters.length)
    .slice(0, 5);
  const single = cols.length === 1;
  const open = rows.find((r) => r.c.id === panel);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Overview</h1>
          <p className="muted">{rows.length} components in {cols.length} {cols.length === 1 ? "cluster" : "clusters"} · generated {ago(report.data.generatedAt)}</p>
        </div>
        <button className="btn" onClick={report.reload}>Refresh</button>
      </div>

      <div className="tiles">
        {LEVELS.map((l) => (
          <button key={l} className={`tile lvl-${l}${filter === l ? " active" : ""}`} aria-pressed={filter === l}
            onClick={() => setFilter(filter === l ? "all" : l)}>
            <span className="tile-n">{counts[l] ?? 0}</span>
            <span className="tile-l"><span className="dot" aria-hidden />{levelLabel[l]}</span>
          </button>
        ))}
      </div>

      <div className="grid-top">
        <Card title="Top actions" actions={<span className="subtle">Worst level → runs in {lastStage} → clusters affected</span>}>
          {top.length === 0 ? <Empty>Nothing above Low in the selected clusters.</Empty> : (
            <div className="actions-list">
              {top.map((r, n) => (
                <button key={r.c.id} className="act" onClick={() => setPanel(r.c.id)}>
                  <span className="act-n">{n + 1}</span>
                  <span className="stack grow" style={{ gap: 2 }}>
                    <span className="row" style={{ gap: 8 }}><span className="act-name">{r.c.name}</span><LevelBadge level={r.level} /></span>
                    <span>{r.c.action.text} <span className="muted">— {r.c.action.why}</span></span>
                    <SummaryHeadline jumps={jumpsOf(r.c.id)} />
                    <span className="subtle">Runs in {r.clusters.length} {r.clusters.length === 1 ? "cluster" : "clusters"}{r.inLive ? ` · incl. ${lastStage}` : ""}</span>
                  </span>
                </button>
              ))}
            </div>
          )}
        </Card>
        <Card title="Clusters" actions={<Link to="/clusters" className="link-btn">All clusters →</Link>}>
          <div className="ccards">
            {cols.map((cl) => <MiniCard key={cl.name} cl={cl} components={report.data!.components} />)}
          </div>
        </Card>
      </div>

      <Card
        title={<>Components <span className="subtle" style={{ fontWeight: 400 }}>{single ? "One cluster selected: detailed list" : "Side by side. Click a name for details."}</span></>}
        actions={<>
          {filter !== "all" && <button className="btn small" onClick={() => setFilter("all")}>Level: {levelLabel[filter]} ✕</button>}
          <input className="input wide" placeholder="Filter components…" aria-label="Filter components" value={query} onChange={(e) => qs.set({ q: e.target.value })} />
        </>}
      >
        {shown.length === 0 ? <Empty>No components match in the selected clusters.</Empty> : single ? (
          <SingleTable rows={pg.items} onOpen={setPanel} />
        ) : (
          <MatrixTable rows={pg.items} cols={cols} onOpen={setPanel} />
        )}
        <Pager page={pg.page} pages={pg.pages} start={pg.start} shown={pg.items.length} total={pg.total} size={pageSize}
          onPage={(p) => qs.set({ page: p })} onSize={(n) => { setPageSize(n); qs.set({ page: null }); }} />
        <div className="legend" aria-label="Legend">{LEGEND.map((n) => <Note key={n.kind} n={n} />)}</div>
        <LevelHelp />
      </Card>

      {open && <ComponentPanel row={open} stageNames={stageNames} selected={sel.selected} jumps={jumpsOf(open.c.id)} onClose={() => setPanel(null)} />}
    </>
  );
}

/** The component's goal (same for every selection), then what the shown clusters each need. */
function ActionCell({ r, perCluster }: { r: Row; perCluster: boolean }) {
  const needs = r.c.action.kind === "none" ? [] : needsByCluster(r.insts);
  return (
    <td className="act-cell">
      <div className="act-text">{r.c.action.text}</div>
      <div className="subtle">{r.c.action.why}</div>
      {perCluster && needs.length > 0 && <ul className="needs">{needs.map((n) => <li key={n}>{n}</li>)}</ul>}
      {r.notes.length > 0 && <div className="notes">{r.notes.map((n, k) => <Note key={k} n={n} />)}</div>}
    </td>
  );
}

function MatrixTable({ rows, cols, onOpen }: { rows: Row[]; cols: Cluster[]; onOpen: (id: string) => void }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Component</th>
            {cols.map((c) => <th key={c.name}><span className="stage">{c.stage}</span>{c.name}</th>)}
            <th>Recommended action</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.c.id}>
              <td><button className="rowbtn" onClick={() => onOpen(r.c.id)}><span className={`dot lvl-${r.level}`} aria-hidden />{r.c.name}</button></td>
              {cols.map((cl) => {
                const here = r.insts.filter((i) => i.cluster === cl.name);
                if (here.length === 0) return <td key={cl.name} className="cell-empty">not running</td>;
                const w = worstInstance(here)!;
                const versions = [...new Set(here.map((i) => i.version || "?"))];
                return (
                  <td key={cl.name} className="cell" title={`${levelLabel[w.score.level]}: ${w.action.text} — ${w.action.why}`}>
                    <span className={`vchip lvl-${w.score.level}`}><span className="dot" aria-hidden />{versions.join(" · ")}</span>
                  </td>
                );
              })}
              <ActionCell r={r} perCluster />
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SingleTable({ rows, onOpen }: { rows: Row[]; onOpen: (id: string) => void }) {
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th>Component</th><th>Level</th><th>Running</th><th>Workloads</th><th>Recommended action</th></tr></thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.c.id}>
              <td><button className="rowbtn" onClick={() => onOpen(r.c.id)}>{r.c.name}</button></td>
              <td><LevelBadge level={r.level} /></td>
              <td className="mono">{[...new Set(r.insts.map((i) => i.version || "?"))].join(" · ")}</td>
              <td className="small">{r.insts.map((i) => (i.namespace ? `${i.namespace}/${i.workload}` : "cluster")).join(", ")}</td>
              <ActionCell r={r} perCluster />
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Per-cluster level counts: each component counted once, at its worst level in that cluster. */
export function clusterCounts(components: ComponentReport[], cluster: string) {
  const levels: Level[] = [];
  for (const c of components) {
    const here = c.instances.filter((i) => i.cluster === cluster);
    if (here.length) levels.push(worstLevel(here.map((i) => i.score.level)));
  }
  return levelCounts(levels);
}

/** The cluster's Kubernetes status, from the scored "kubernetes" component. */
export function k8sStatus(components: ComponentReport[], cluster: string) {
  const k = components.find((c) => c.id === "kubernetes")?.instances.find((i) => i.cluster === cluster);
  if (!k) return null;
  const eol = k.action.why.split("; ").find((p) => p.includes(" is EOL"));
  return { level: k.score.level, text: k.action.kind === "none" ? "up to date" : eol ?? k.action.text };
}

function MiniCard({ cl, components }: { cl: Cluster; components: ComponentReport[] }) {
  const counts = clusterCounts(components, cl.name);
  const k8s = k8sStatus(components, cl.name);
  return (
    <Link to={`/clusters/${encodeURIComponent(cl.name)}`} className="cc">
      <span className="cc-top"><b>{cl.name}</b><span className="stage-inline">{cl.stage}</span></span>
      <span className="subtle" style={{ margin: 0 }}>{cl.tenant} · reported {ago(cl.lastSeenAt)}</span>
      <span className="row small" style={{ gap: 6 }}>
        <span className="mono">{cl.kubernetesVersion || "—"}</span>
        {k8s && <span className={`badge lvl-${k8s.level}`}>{k8s.text}</span>}
      </span>
      <LevelBar counts={counts} thin />
      <CountBadges counts={counts} />
    </Link>
  );
}
