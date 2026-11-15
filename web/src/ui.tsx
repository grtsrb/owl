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
