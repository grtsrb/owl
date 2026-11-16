import { useEffect } from "react";
import { Link } from "react-router-dom";
import { api, type ReleaseLine } from "../api";
import { Icon } from "../icons";
import { Note } from "../model";
import type { Row } from "../pages/Overview";
import { ErrorBox, LevelBadge, Loading, useApi } from "../ui";

/** Quick look at a component from the Overview: target, release lines, reasons per selected cluster. */
export default function ComponentPanel({ row, stageNames, selected, onClose }: {
  row: Row; stageNames: string[]; selected: string[]; onClose: () => void;
}) {
  const detail = useApi(() => api.component(row.c.id), [row.c.id]);
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") onClose(); };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const { c, insts } = row;
  const sel = new Set(selected);
  const stagePos = (s: string) => stageNames.indexOf(s);
  const byCluster = [...insts].sort((a, b) => stagePos(a.stage) - stagePos(b.stage) || a.cluster.localeCompare(b.cluster));
  const drift = c.drift.filter((d) => d.clusters.every((x) => sel.has(x)) || d.kind === "only_in");

  return (
    <div className="overlay" onClick={onClose}>
      <aside className="panel" aria-label={`${c.name} details`} onClick={(e) => e.stopPropagation()}>
        <div className="row" style={{ justifyContent: "space-between", alignItems: "flex-start" }}>
          <div className="stack" style={{ gap: 4 }}>
            <div className="with-badge"><h1>{c.name}</h1><LevelBadge level={row.level} /></div>
            <span className="subtle" style={{ margin: 0 }}>Advisories: {c.upstream.advisorySource || "none published upstream — status unknown"}</span>
          </div>
          <button className="icon-btn" onClick={onClose} aria-label="Close"><Icon.close /></button>
        </div>

        <div className="kv-grid">
          <div><span>Target</span><span className="mono big">{c.target?.version ?? "—"}</span></div>
          <div><span>Policy</span><span className="mono">{c.target?.policy ?? "—"}</span></div>
          <div><span>Latest upstream</span><span className="mono big">{c.upstream.latestStable ?? "—"}</span></div>
        </div>
        {row.notes.length > 0 && <div className="notes">{row.notes.map((n, k) => <Note key={k} n={n} />)}</div>}

        <div className="stack">
          <h2>Release lines</h2>
          {detail.error ? <ErrorBox error={detail.error} retry={detail.reload} /> : !detail.data ? <Loading /> : (
            <Lines lines={detail.data.upstream?.lines ?? []} row={row} />
          )}
        </div>

        <div className="stack">
          <h2>Per cluster</h2>
          {byCluster.map((i, k) => (
            <div key={k} className="inst">
              <div className="row" style={{ justifyContent: "space-between" }}>
                <span className="row" style={{ gap: 8 }}><b>{i.cluster}</b><span className="stage-inline">{i.stage}</span></span>
                <span className={`vchip lvl-${i.score.level}`}><span className="dot" aria-hidden />{i.version || "?"}</span>
              </div>
              <div className="small"><b>{i.action.text}</b> <span className="muted">— {i.action.why}</span></div>
              {i.score.reasons.length > 0 && <ul className="bullets">{i.score.reasons.slice(0, 5).map((r, n) => <li key={n}>{r.text}</li>)}
                {i.score.reasons.length > 5 && <li className="muted">+{i.score.reasons.length - 5} more on the full page</li>}</ul>}
              <div className="subtle">{i.namespace ? `${i.namespace}/${i.workload} · ${i.kind}` : "cluster version"}</div>
            </div>
          ))}
        </div>

        {drift.length > 0 && (
          <div className="stack" style={{ gap: 6 }}>
            <h2>Promotion</h2>
            {drift.map((d, k) => (
              <div key={k} className="small">
                {d.kind === "anomaly" ? <Note n={{ kind: "drift", text: d.text }} /> : d.kind === "only_in" ? <span className="muted">ⓘ {d.tenant}: {d.text}</span> : d.text}
              </div>
            ))}
          </div>
        )}

        <div className="row">
          <Link to={`/components/${encodeURIComponent(c.id)}`} className="btn">Open full page (advisories, policy) →</Link>
        </div>
        <div className="callout info">Milestone 8: a Claude summary of the upgrade (breaking changes, steps) will appear here.</div>
      </aside>
    </div>
  );
}

/** The lines that matter here: every line running in the selected clusters, the target and the newest. */
function Lines({ lines, row }: { lines: ReleaseLine[]; row: Row }) {
  const running = new Map<string, string[]>();
  for (const i of row.insts) {
    if (!i.score.line) continue;
    running.set(i.score.line, [...new Set([...(running.get(i.score.line) ?? []), i.stage])]);
  }
  const target = row.c.target?.line;
  const newest = lines[0]?.name;
  const pick = lines.filter((l) => running.has(l.name) || l.name === target || l.name === newest).reverse(); // oldest first
  if (pick.length === 0) return <p className="subtle">No release-line data.</p>;
  return (
    <>
      <div className="lines">
        {pick.map((l) => {
          const cls = l.eol ? "eol" : l.name === target ? "target" : l.name === newest ? "latest" : "";
          const tags = [
            l.eol ? `EOL${l.eolDate ? " " + l.eolDate.slice(0, 7) : ""}` : "",
            l.name === target ? `target ${row.c.target?.version}` : "",
            l.name === newest && l.name !== target ? `latest ${l.latest}` : "",
            ...(running.get(l.name) ?? []),
          ].filter(Boolean);
          return <div key={l.name} className={`ln ${cls}`}><span className="ln-v">{l.name}</span><span className="small">{tags.join(" · ")}</span></div>;
        })}
      </div>
      <span className="subtle">Red = end of life · blue = policy target · grey = newest upstream.</span>
    </>
  );
}
