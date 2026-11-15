import { useMemo, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { api, LEVELS, stageOrder, type ComponentReport, type Level, type ScoredInstance } from "../api";
import { ago, Card, Empty, ErrorBox, LevelBadge, levelLabel, Loading, Tag, useApi } from "../ui";

const rank: Record<Level, number> = { critical: 5, high: 4, medium: 3, low: 2, unknown: 1, ok: 0 };

export default function Overview() {
  const report = useApi(api.report);
  const clusters = useApi(api.clusters);
  const [filter, setFilter] = useState<Level | "all">("all");
  const [query, setQuery] = useState("");
  const navigate = useNavigate();

  const columns = useMemo(
    () => [...(clusters.data ?? [])].sort((a, b) => (stageOrder[a.stage] ?? 9) - (stageOrder[b.stage] ?? 9) || a.name.localeCompare(b.name)),
    [clusters.data],
  );

  if (report.error) return <ErrorBox error={report.error} retry={report.reload} />;
  if (!report.data || !clusters.data) return <Loading />;
  const r = report.data;
  const rows = r.components.filter(
    (c) => (filter === "all" || c.level === filter) && c.name.toLowerCase().includes(query.toLowerCase()),
  );

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Overview</h1>
          <p className="muted">
            {r.components.length} components across {columns.length} clusters · generated {ago(r.generatedAt)}
          </p>
        </div>
        <button className="btn" onClick={() => { report.reload(); clusters.reload(); }}>Refresh</button>
      </div>

      <div className="tiles">
        {LEVELS.map((l) => (
          <button
            key={l}
            className={`tile lvl-${l} ${filter === l ? "active" : ""}`}
            onClick={() => setFilter(filter === l ? "all" : l)}
            aria-pressed={filter === l}
          >
            <span className="tile-n">{r.summary[l] ?? 0}</span>
            <span className="tile-l"><span className="dot" aria-hidden />{levelLabel[l]}</span>
          </button>
        ))}
        <Link to="/images" className="tile tile-plain">
          <span className="tile-n">{r.unpinned + r.unrecognized}</span>
          <span className="tile-l">Unpinned / unrecognized</span>
        </Link>
      </div>

      <Card
        title="Components"
        actions={
          <input className="input" placeholder="Filter components…" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Filter components" />
        }
      >
        {rows.length === 0 ? (
          <Empty>No components match.</Empty>
        ) : (
          <div className="table-wrap">
            <table className="matrix">
              <thead>
                <tr>
                  <th>Component</th>
                  {columns.map((c) => (
                    <th key={c.name} className="cluster-col">
                      <span className="stage">{c.stage}</span>
                      {c.name}
                    </th>
                  ))}
                  <th>Target</th>
                  <th>Latest</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {rows.map((c) => (
                  <tr key={c.id} className="clickable" onClick={() => navigate(`/components/${c.id}`)}>
                    <td>
                      <div className="comp-cell">
                        <LevelBadge level={c.level} compact />
                        <Link to={`/components/${c.id}`} onClick={(e) => e.stopPropagation()}>{c.name}</Link>
                      </div>
                      <div className="subtle">{topReason(c)}</div>
                    </td>
                    {columns.map((cl) => <ClusterCell key={cl.name} instances={c.instances.filter((i) => i.cluster === cl.name)} />)}
                    <td className="mono">{c.target?.version ?? "—"}<div className="subtle">{c.target?.policy}</div></td>
                    <td className="mono">{c.upstream.latestStable ?? "—"}</td>
                    <td className="flags">
                      {c.upstream.retired && <Tag tone="critical" title="Upstream project retired">retired</Tag>}
                      {!c.upstream.retired && c.upstream.archived && <Tag tone="warn" title="Repository archived, still releasing">archived</Tag>}
                      {c.drift.some((d) => d.kind === "anomaly") && <Tag tone="warn" title={c.drift.filter((d) => d.kind === "anomaly").map((d) => d.text).join("\n")}>drift</Tag>}
                      {!c.upstream.advisorySource && <Tag title="No advisory data upstream — vulnerability status unknown">no CVE data</Tag>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}

function ClusterCell({ instances }: { instances: ScoredInstance[] }) {
  if (instances.length === 0) return <td className="cell-empty">—</td>;
  const worst = instances.reduce((a, b) => (rank[b.score.level] > rank[a.score.level] ? b : a));
  const versions = [...new Set(instances.map((i) => i.version || i.image.split(":").pop() || "?"))];
  return (
    <td className={`cell lvl-${worst.score.level}`} title={worst.score.reasons.map((x) => x.text).join("\n") || "OK"}>
      <span className="dot" aria-hidden />
      <span className="mono">{versions.join(", ")}</span>
    </td>
  );
}

function topReason(c: ComponentReport): string {
  let best: { level: Level; text: string } | null = null;
  for (const i of c.instances) for (const r of i.score.reasons) if (!best || rank[r.level] > rank[best.level]) best = r;
  return best?.text ?? "Up to date";
}
