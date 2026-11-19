import { useEffect, useMemo, useRef, useState } from "react";
import type { Cluster } from "../api";
import { Icon } from "../icons";
import { useClusterSelection } from "../model";

const SEARCH_FROM = 9; // clusters: show the filter box from this many

/** Which clusters Overview and Changes show: one button (right of the top bar) that sums up the selection
 *  and opens a popover — All / per stage / per tenant shortcuts and the clusters grouped by stage.
 *  `clusters` come in stage order. The selection lives in ?clusters=. */
export function ClusterPicker({ clusters }: { clusters: Cluster[] }) {
  const sel = useClusterSelection(clusters);
  const [open, setOpen] = useState(false);
  const [q, setQ] = useState("");
  const box = useRef<HTMLDivElement>(null);

  const stages = useMemo(() => {
    const out: { stage: string; names: string[] }[] = [];
    for (const c of clusters) {
      const g = out.find((s) => s.stage === c.stage);
      if (g) g.names.push(c.name); else out.push({ stage: c.stage, names: [c.name] });
    }
    return out;
  }, [clusters]);
  const tenants = useMemo(() => [...new Set(clusters.map((c) => c.tenant))].sort(), [clusters]);

  const chosen = new Set(sel.selected);
  const same = (names: string[]) => names.length === chosen.size && names.every((n) => chosen.has(n));
  const activeStage = sel.isAll ? null : stages.find((s) => same(s.names))?.stage ?? null;
  const activeTenant = sel.isAll || activeStage ? null : tenants.find((t) => same(clusters.filter((c) => c.tenant === t).map((c) => c.name))) ?? null;
  const custom = !sel.isAll && !activeStage;
  const n = sel.selected.length;
  const label = sel.isAll ? `${clusters.length} cluster${clusters.length === 1 ? "" : "s"}`
    : n === 1 ? sel.selected[0]
    : activeStage ? `${activeStage[0].toUpperCase()}${activeStage.slice(1)} · ${n} clusters`
    : activeTenant ? `Tenant ${activeTenant} · ${n}`
    : `${n} of ${clusters.length} clusters`;

  // Close on a click outside or Escape.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => { if (box.current && !box.current.contains(e.target as Node)) setOpen(false); };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("mousedown", onDown); document.removeEventListener("keydown", onKey); };
  }, [open]);

  const needle = q.trim().toLowerCase();
  const visible = (name: string) => !needle || name.toLowerCase().includes(needle);

  return (
    <div className="topbar picker" role="group" aria-label="Clusters shown">
      <div className="pick" ref={box}>
        <button className={`pick-btn${custom ? " custom" : ""}`} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen((o) => !o)}
          aria-label={`Clusters: ${label}`} title={sel.selected.join(", ")}>
          <Icon.clusters />
          <span className="pick-label">{label}</span>
          <Icon.chevron />
        </button>
        {open && (
          <div className="pick-pop" role="dialog" aria-label="Choose clusters">
            {clusters.length >= SEARCH_FROM && (
              <label className="pick-search">
                <Icon.search />
                <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter clusters…" aria-label="Filter clusters" autoFocus />
              </label>
            )}
            {stages.length > 1 && (
              <div className="pick-quick" role="group" aria-label="Shortcuts">
                <button className={`chip small${sel.isAll ? " on" : ""}`} aria-pressed={sel.isAll} onClick={() => sel.set([])}>All</button>
                {stages.map((s) => (
                  <button key={s.stage} className={`chip small cap${activeStage === s.stage ? " on" : ""}`} aria-pressed={activeStage === s.stage}
                    onClick={() => sel.set(s.names)}>{s.stage}</button>
                ))}
              </div>
            )}
            {tenants.length > 1 && (
              <div className="pick-tenants">
                <span className="pick-h">Tenant</span>
                {tenants.map((t) => (
                  <button key={t} className={`chip small${activeTenant === t ? " on" : ""}`} aria-pressed={activeTenant === t}
                    onClick={() => sel.set(clusters.filter((c) => c.tenant === t).map((c) => c.name))}>{t}</button>
                ))}
              </div>
            )}
            <div className="pick-list">
              {stages.map((s) => {
                const names = s.names.filter(visible);
                if (names.length === 0) return null;
                return (
                  <div key={s.stage} className="pick-group">
                    <button className="pick-h pick-stage" onClick={() => sel.set(s.names)} title={`Only ${s.stage}`}>{s.stage}</button>
                    {names.map((name) => {
                      const c = clusters.find((x) => x.name === name)!;
                      const on = chosen.has(name);
                      const last = on && n === 1;
                      return (
                        <label key={name} className={`pick-row${last ? " last" : ""}`} title={last ? "At least one cluster stays selected" : undefined}>
                          <input type="checkbox" checked={on} disabled={last} onChange={() => sel.toggle(name)} />
                          <span className="pick-name">{name}</span>
                          {tenants.length > 1 && <span className="pick-tenant">{c.tenant}</span>}
                        </label>
                      );
                    })}
                  </div>
                );
              })}
            </div>
            <div className="pick-foot">
              <span className="muted">{n} of {clusters.length} selected</span>
              <button className="link-btn" onClick={() => sel.set([])} disabled={sel.isAll}>Select all</button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
