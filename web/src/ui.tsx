import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type { Level } from "./api";

export const levelLabel: Record<Level, string> = {
  critical: "Critical", high: "High", medium: "Medium", low: "Low", unknown: "Unknown", ok: "OK",
};

export function LevelBadge({ level, compact = false }: { level: Level; compact?: boolean }) {
  return (
    <span className={`badge lvl-${level}`} title={levelLabel[level]}>
      <span className="dot" aria-hidden />
      {!compact && levelLabel[level]}
    </span>
  );
}

export function Tag({ children, tone = "neutral", title }: { children: ReactNode; tone?: string; title?: string }) {
  return <span className={`tag tone-${tone}`} title={title}>{children}</span>;
}

export function Card({ title, actions, children }: { title?: ReactNode; actions?: ReactNode; children: ReactNode }) {
  return (
    <section className="card">
      {(title || actions) && (
        <header className="card-head">
          {title && <h2>{title}</h2>}
          {actions && <div className="card-actions">{actions}</div>}
        </header>
      )}
      {children}
    </section>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="empty">{children}</p>;
}

export function ErrorBox({ error, retry }: { error: unknown; retry?: () => void }) {
  return (
    <div className="error" role="alert">
      <strong>Couldn’t load data.</strong> {error instanceof Error ? error.message : String(error)}
      {retry && <button className="btn small" onClick={retry}>Retry</button>}
    </div>
  );
}

export function Loading() {
  return <div className="loading" aria-busy="true">Loading…</div>;
}

/** Fetch on mount (and when deps change); returns data, error and a reload function. */
export function useApi<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(true);
  const latest = useRef(0); // only the newest request may update state (no stale overwrite)
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const load = useCallback(fn, deps);
  const reload = useCallback(() => {
    const id = ++latest.current;
    setLoading(true);
    load()
      .then((d) => { if (id === latest.current) { setData(d); setError(null); } })
      .catch((e) => { if (id === latest.current) setError(e); })
      .finally(() => { if (id === latest.current) setLoading(false); });
  }, [load]);
  useEffect(reload, [reload]);
  return { data, error, loading, reload };
}

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
export function ago(iso?: string | null): string {
  if (!iso) return "never";
  const s = (new Date(iso).getTime() - Date.now()) / 1000;
  const steps: [number, Intl.RelativeTimeFormatUnit][] = [[60, "second"], [3600, "minute"], [86400, "hour"], [2592000, "day"], [31536000, "month"]];
  for (let i = 0; i < steps.length; i++) {
    const [limit, unit] = steps[i];
    if (Math.abs(s) < limit) return rtf.format(Math.round(s / (i === 0 ? 1 : steps[i - 1][0])), unit);
  }
  return rtf.format(Math.round(s / 31536000), "year");
}

export function date(iso?: string | null): string {
  return iso ? new Date(iso).toISOString().slice(0, 10) : "—";
}

export const levelRank: Record<Level, number> = { critical: 5, high: 4, medium: 3, low: 2, unknown: 1, ok: 0 };

export function worstLevel(levels: Level[]): Level {
  return levels.reduce<Level>((a, b) => (levelRank[b] > levelRank[a] ? b : a), "ok");
}

/** Count per level, worst first, only levels that occur. */
export function levelCounts(levels: Level[]): { level: Level; n: number }[] {
  const n: Partial<Record<Level, number>> = {};
  for (const l of levels) n[l] = (n[l] ?? 0) + 1;
  return (["critical", "high", "medium", "low", "unknown", "ok"] as Level[]).filter((l) => n[l]).map((l) => ({ level: l, n: n[l]! }));
}

/** Stacked bar of level counts (segments sized by count). */
export function LevelBar({ counts, thin = false }: { counts: { level: Level; n: number }[]; thin?: boolean }) {
  const label = counts.map((c) => `${c.n} ${levelLabel[c.level]}`).join(", ") || "nothing scored";
  return (
    <div className={`lvbar${thin ? " thin" : ""}`} role="img" aria-label={`Components by level: ${label}`}>
      {counts.map((c) => <span key={c.level} className={`lvl-${c.level}`} style={{ flexGrow: c.n }} />)}
    </div>
  );
}

export function CountBadges({ counts }: { counts: { level: Level; n: number }[] }) {
  return (
    <div className="counts">
      {counts.map((c) => <span key={c.level} className={`badge lvl-${c.level}`}>{c.n} {levelLabel[c.level]}</span>)}
    </div>
  );
}

/** Format an API error for display under a form. */
export function Msg({ msg }: { msg: { ok: boolean; text: string } | null }) {
  if (!msg) return null;
  return <p className={msg.ok ? "ok-text" : "err-text"} role={msg.ok ? "status" : "alert"}>{msg.text}</p>;
}

/** "11–20 of 37", Previous/Next, page numbers (with gaps when there are many), rows-per-page. */
export function Pager({ page, pages, start, shown, total, size, onPage, onSize }: {
  page: number; pages: number; start: number; shown: number; total: number; size: number;
  onPage: (p: number) => void; onSize: (n: number) => void;
}) {
  if (total === 0) return null;
  const nums: (number | "…")[] = [];
  for (let p = 1; p <= pages; p++) {
    if (pages <= 7 || p === 1 || p === pages || Math.abs(p - page) <= 1) nums.push(p);
    else if (nums[nums.length - 1] !== "…") nums.push("…");
  }
  return (
    <nav className="pager" aria-label="Pages">
      <span className="subtle" style={{ margin: 0 }}>{start + 1}–{start + shown} of {total}</span>
      {pages > 1 && (
        <span className="row" style={{ gap: 4 }}>
          <button className="btn small" disabled={page <= 1} onClick={() => onPage(page - 1)}>Previous</button>
          {nums.map((n, i) => n === "…"
            ? <span key={`gap${i}`} className="subtle" style={{ margin: 0 }} aria-hidden>…</span>
            : <button key={n} className={`btn small${n === page ? " primary" : ""}`} aria-current={n === page ? "page" : undefined} onClick={() => onPage(n)}>{n}</button>)}
          <button className="btn small" disabled={page >= pages} onClick={() => onPage(page + 1)}>Next</button>
        </span>
      )}
      <label className="row small" style={{ gap: 6, marginLeft: "auto" }}>Rows
        <select className="select" value={size} onChange={(e) => onSize(Number(e.target.value))}>
          {[10, 25, 50].map((n) => <option key={n} value={n}>{n}</option>)}
          <option value={0}>All</option>
        </select>
      </label>
    </nav>
  );
}

/** The value of a form field by name ("" if missing). Password inputs are deliberately uncontrolled
 * (no `value=` prop): React copies a controlled input's value into the HTML `value` attribute, which puts
 * the password in the DOM where CSS attribute selectors and DOM inspectors can see it. Read them from
 * the form on input/submit instead, and keep only derived booleans (filled, long enough, matching) in state. */
export function formValue(form: HTMLFormElement, name: string): string {
  const el = form.elements.namedItem(name);
  return el instanceof HTMLInputElement ? el.value : "";
}

/** Password length the way the server counts it (code points, not UTF-16 units). */
export function pwLength(s: string): number {
  return [...s].length;
}
