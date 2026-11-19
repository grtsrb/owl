import { useEffect, useMemo, useRef, useState } from "react";
import type { Cluster } from "../api";
import { Icon } from "../icons";
import { useClusterSelection } from "../model";

const SEARCH_FROM = 9; // clusters: show the filter box from this many

/** Which clusters Overview and Changes show: stage tabs for the common case, a popover for single
 *  clusters and tenants. `clusters` come in stage order. The selection lives in ?clusters=. */
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
    : activeTenant ? `Tenant ${activeTenant} · ${n}`
    : custom ? `${n} of ${clusters.length} clusters`
    : `${n} clusters`;

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
      {stages.length > 1 && (
        <div className="seg" role="tablist" aria-label="Stage">
          <button role="tab" aria-selected={sel.isAll} className={sel.isAll ? "on" : ""} onClick={() => sel.set([])}>All</button>
          {stages.map((s) => (
            <button key={s.stage} role="tab" aria-selected={activeStage === s.stage} className={activeStage === s.stage ? "on" : ""}
              onClick={() => sel.set(s.names)} title={s.names.join(", ")}>
              {s.stage}<span className="seg-n">{s.names.length}</span>
            </button>
          ))}
        </div>
      )}
      <div className="pick" ref={box}>
        <button className={`pick-btn${custom ? " custom" : ""}`} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen((o) => !o)}
          aria-label={`Clusters: ${label}`} title={sel.selected.join(", ")}>
          <Icon.clusters />
          <span className="pick-label">{label}</span>
          <span className="pick-n" aria-hidden>{n}</span>
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
