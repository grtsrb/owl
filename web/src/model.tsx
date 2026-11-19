// View logic shared by pages: which clusters are selected (kept in the URL), and the notes shown
// under a component. Levels and actions come from the server; nothing here decides a level.
import { useCallback, useMemo, useState, type ReactElement } from "react";
import { useSearchParams } from "react-router-dom";
import type { Cluster, ComponentReport, ScoredInstance } from "./api";
import { Icon } from "./icons";
import { levelRank } from "./ui";

/** Clusters ordered by the user's stage order, then name. */
export function orderClusters(clusters: Cluster[], stages: string[]): Cluster[] {
  const pos = (s: string) => { const i = stages.indexOf(s); return i < 0 ? 999 : i; };
  return [...clusters].sort((a, b) => pos(a.stage) - pos(b.stage) || a.name.localeCompare(b.name));
}

/**
 * The cluster picker's selection lives in `?clusters=a,b` so a view can be bookmarked and shared.
 * No parameter = all clusters (also when every listed cluster is gone, e.g. after a delete).
 */
export function useClusterSelection(all: Cluster[]) {
  const [params, setParams] = useSearchParams();
  const raw = params.get("clusters");
  const names = useMemo(() => all.map((c) => c.name), [all]);
  const selected = useMemo(() => {
    if (!raw) return names;
    const want = new Set(raw.split(",").filter(Boolean));
    const hit = names.filter((n) => want.has(n));
    return hit.length ? hit : names;
  }, [raw, names]);

  const set = useCallback((next: string[]) => {
    setParams((p) => {
      const q = new URLSearchParams(p);
      if (next.length === 0 || next.length === names.length) q.delete("clusters");
      else q.set("clusters", next.join(","));
      q.delete("page"); // a different selection is a different list: start at page 1
      return q;
    }, { replace: true });
  }, [names, setParams]);

  const toggle = useCallback((name: string) => {
    const on = selected.includes(name);
    const next = on ? selected.filter((n) => n !== name) : [...selected, name];
    if (next.length) set(next); // never select nothing
  }, [selected, set]);

  return { selected, set, toggle, isAll: selected.length === names.length };
}

export type NoteKind = "retired" | "archived" | "drift" | "unpinned" | "review" | "nocve" | "stale";
export interface NoteItem { kind: NoteKind; text: string }

const noteTone: Record<NoteKind, string> = {
  retired: "critical", archived: "warn", drift: "warn", unpinned: "warn", review: "info", nocve: "", stale: "",
};
const noteIcon: Record<NoteKind, () => ReactElement> = {
  retired: Icon.retired, archived: Icon.archived, drift: Icon.drift, unpinned: Icon.pin, review: Icon.review,
  nocve: Icon.nocve, stale: Icon.stale,
};

export function Note({ n }: { n: NoteItem }) {
  const I = noteIcon[n.kind];
  return <span className={`note ${noteTone[n.kind]}`}><I />{n.text}</span>;
}

export const LEGEND: NoteItem[] = [
  { kind: "retired", text: "Retired upstream" },
  { kind: "archived", text: "Archived, still releasing" },
  { kind: "drift", text: "Drift anomaly" },
  { kind: "unpinned", text: "Unpinned tag" },
  { kind: "review", text: "Advisories need review" },
  { kind: "nocve", text: "No advisory data" },
  { kind: "stale", text: "Upstream data stale" },
];

/** Notes for a component, limited to the selected clusters (drift only if all its clusters are selected). */
export function notesFor(c: ComponentReport, insts: ScoredInstance[], selected: string[]): NoteItem[] {
  const out: NoteItem[] = [];
  const sel = new Set(selected);
  if (c.upstream.retired) out.push({ kind: "retired", text: `Retired upstream${c.upstream.lastReleaseAt ? ` (last release ${c.upstream.lastReleaseAt.slice(0, 10)})` : ""}` });
  else if (c.upstream.archived) out.push({ kind: "archived", text: "Repo archived but still releasing" });
  for (const d of c.drift) {
    if (d.kind === "anomaly" && d.clusters.every((x) => sel.has(x))) out.push({ kind: "drift", text: d.text });
  }
  const unpinned = insts.filter((i) => i.status === "unpinned");
  if (unpinned.length) out.push({ kind: "unpinned", text: `${unpinned.map((i) => i.workload ?? i.image).join(", ")} unpinned` });
  const review = Math.max(0, ...insts.map((i) => i.score.review ?? 0));
  if (review > 0) out.push({ kind: "review", text: `${review} ${review === 1 ? "advisory needs" : "advisories need"} review` });
  if (!c.upstream.advisorySource) out.push({ kind: "nocve", text: "No advisory data upstream" });
  if (c.upstream.stale) out.push({ kind: "stale", text: "Upstream data stale" });
  return out;
}

/** What one instance needs to reach the component's target, in a couple of words. */
export function need(i: ScoredInstance): string {
  switch (i.action.kind) {
    case "none": return "up to date";
    case "patch": return "patch";
    case "plan": return "plan the move";
    case "upgrade":
      if (i.score.eol) return "EOL line";
      return i.score.linesBehind > 0 ? `${i.score.linesBehind} ${i.score.linesBehind === 1 ? "line" : "lines"} behind` : "upgrade";
    case "replace": return "replace";
    case "pin": return "unpinned";
    default: return "check";
  }
}

/** Instances grouped by (version, need): "it-aws-test, it-aws-prod 1.13.1 → 1 line behind". */
export function needsByCluster(insts: ScoredInstance[]): string[] {
  const groups = new Map<string, { clusters: string[]; version: string; need: string }>();
  for (const i of insts) {
    const version = i.version || i.image.split(":").pop() || "?";
    const n = need(i);
    const key = `${version}|${n}`;
    const g = groups.get(key) ?? { clusters: [], version, need: n };
    if (!g.clusters.includes(i.cluster)) g.clusters.push(i.cluster);
    groups.set(key, g);
  }
  return [...groups.values()].map((g) => `${g.clusters.join(", ")} ${g.version} → ${g.need}`);
}

/** The instance that decides a component's level among the given ones (worst, then most reasons). */
export function worstInstance(insts: ScoredInstance[]): ScoredInstance | undefined {
  return insts.reduce<ScoredInstance | undefined>((a, b) => {
    if (!a) return b;
    const d = levelRank[b.score.level] - levelRank[a.score.level];
    return d > 0 || (d === 0 && b.score.reasons.length > a.score.reasons.length) ? b : a;
  }, undefined);
}

/**
 * List view state in the URL (filters, search, page), so Back and bookmarks restore the view. Updates
 * replace the history entry (typing in a search box doesn't create Back steps), and any change other
 * than the page itself goes back to page 1.
 */
export function useQueryState() {
  const [params, setParams] = useSearchParams();
  const get = useCallback((k: string) => params.get(k) ?? "", [params]);
  const set = useCallback((patch: Record<string, string | number | null>) => {
    setParams((p) => {
      const q = new URLSearchParams(p);
      for (const [k, v] of Object.entries(patch)) {
        if (v === null || v === "" || (k === "page" && Number(v) <= 1)) q.delete(k);
        else q.set(k, String(v));
      }
      if (!("page" in patch)) q.delete("page");
      return q;
    }, { replace: true });
  }, [setParams]);
  return { get, set };
}

const PAGE_SIZE_KEY = "owl-page-size";
export const PAGE_SIZES = [10, 25, 50, 0] as const; // 0 = all

/** Rows per page: a per-browser preference (not in the URL). Storage may be blocked: default 10. */
export function usePageSize(): [number, (n: number) => void] {
  const [size, setSize] = useState<number>(() => {
    try { const v = Number(localStorage.getItem(PAGE_SIZE_KEY)); return (PAGE_SIZES as readonly number[]).includes(v) && localStorage.getItem(PAGE_SIZE_KEY) !== null ? v : 10; }
    catch { return 10; }
  });
  const update = useCallback((n: number) => {
    setSize(n);
    try { localStorage.setItem(PAGE_SIZE_KEY, String(n)); } catch { /* storage blocked */ }
  }, []);
  return [size, update];
}

/** The rows of the current page, with the page clamped to what exists (e.g. after a filter shrinks the list). */
export function paginate<T>(rows: T[], page: number, size: number) {
  const pages = size === 0 ? 1 : Math.max(1, Math.ceil(rows.length / size));
  const current = Math.min(Math.max(1, page || 1), pages);
  const start = size === 0 ? 0 : (current - 1) * size;
  return { items: size === 0 ? rows : rows.slice(start, start + size), page: current, pages, start, total: rows.length };
}
