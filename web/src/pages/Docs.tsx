import { useCallback, useEffect, useMemo, useRef, useState, type MouseEvent, type ReactNode } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router-dom";
import { api, ApiError, type DocsIndex, type DocSection } from "../api";
import { Icon } from "../icons";
import { Empty, ErrorBox, Loading, useApi } from "../ui";

// The built-in docs: the page in the main area, a topics drawer (search + pages) on its right that the
// arrow collapses. The HTML comes from Owl's own build (docs/*.md rendered by docsgen, raw HTML
// refused there), so inserting it is safe; links inside it are routed here, not reloaded.

const LAST_KEY = "owl-docs-last";
const TOPICS_KEY = "owl-docs-topics";

function load(key: string): string | null {
  try { return localStorage.getItem(key); } catch { return null; }
}
function save(key: string, v: string) {
  try { localStorage.setItem(key, v); } catch { /* storage blocked */ }
}

/** Where the sidebar's "Docs" goes: the last page read in this browser, else the docs start. */
export function docsHome(): string {
  const last = load(LAST_KEY);
  return last && /^\/docs\/[a-z0-9-]+(#[\w-]*)?$/.test(last) ? last : "/docs";
}

/** A small "?" that opens the docs section explaining a whole feature (page headers, level legend). */
export function DocHelp({ to, label }: { to: string; label: string }) {
  return (
    <Link className="doc-help" to={`/docs/${to}`} title={`Docs: ${label}`} aria-label={`Docs: ${label}`}>?</Link>
  );
}

const narrow = () => typeof window !== "undefined" && window.matchMedia("(max-width: 900px)").matches;

export default function Docs() {
  const { page } = useParams();
  const index = useApi(api.docs);
  const [topics, setTopicsState] = useState(() => load(TOPICS_KEY) !== "closed" && !narrow());
  // The arrow is a choice to remember; closing after picking a topic on a phone is not (it would
  // collapse the drawer on the desktop too).
  const setTopics = (open: boolean, remember = true) => { setTopicsState(open); if (remember) save(TOPICS_KEY, open ? "open" : "closed"); };

  if (index.error) {
    if (index.error instanceof ApiError && index.error.status === 404) {
      return <Empty>The docs aren’t built into this server. Build it with <code>make build</code> (or the image).</Empty>;
    }
    return <ErrorBox error={index.error} retry={index.reload} />;
  }
  if (!index.data) return <Loading />;
  const ix = index.data;

  return (
    <div className={`docs${topics ? " topics-open" : ""}`}>
      <div className="docs-main">
        {page ? <DocView ix={ix} id={page} /> : <DocsStart ix={ix} />}
      </div>
      <Topics ix={ix} current={page} open={topics} setOpen={setTopics} />
    </div>
  );
}

function DocsStart({ ix }: { ix: DocsIndex }) {
  return (
    <>
      <div className="page-head">
        <div>
          <h1>Docs</h1>
          <p className="muted">How to host, run and use Owl {ix.version} — what each feature does, how it works and why.</p>
        </div>
      </div>
      <div className="doc-cards">
        {ix.pages.map((p) => (
          <Link key={p.id} to={`/docs/${p.id}`} className="doc-card">
            <span className="doc-card-t">{p.title}</span>
            <span className="doc-card-d">{p.description}</span>
          </Link>
        ))}
      </div>
    </>
  );
}

function DocView({ ix, id }: { ix: DocsIndex; id: string }) {
  const meta = ix.pages.find((p) => p.id === id);
  const doc = useApi(() => (meta ? api.docPage(id) : Promise.reject(new ApiError(404, "no such docs page"))), [id, meta]);
  const body = useRef<HTMLDivElement>(null);
  const { hash, key } = useLocation();
  const navigate = useNavigate();
  const [copied, setCopied] = useState<string | null>(null);

  useEffect(() => { if (meta) save(LAST_KEY, `/docs/${id}${hash}`); }, [id, hash, meta]);

  // After the HTML is in: copy buttons on code blocks.
  const html = doc.data?.html;
  useEffect(() => {
    const el = body.current;
    if (!el || !html) return;
    for (const pre of el.querySelectorAll("pre")) {
      if (pre.parentElement?.classList.contains("doc-pre")) continue;
      const wrap = document.createElement("div");
      wrap.className = "doc-pre";
      pre.replaceWith(wrap);
      wrap.appendChild(pre);
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn small doc-copy";
      btn.textContent = "Copy";
      btn.dataset.copy = "code";
      wrap.appendChild(btn);
    }
  }, [html]);

  // Scroll to the section in the URL (or the top for a new page).
  useEffect(() => {
    if (!html) return;
    const target = hash ? document.getElementById(decodeURIComponent(hash.slice(1))) : null;
    if (target) target.scrollIntoView({ block: "start" });
    else window.scrollTo({ top: 0 });
  }, [html, hash, key]); // key: clicking the section already in the URL scrolls back to it

  const flash = (what: string) => { setCopied(what); window.setTimeout(() => setCopied((c) => (c === what ? null : c)), 1500); };
  /** Resolves true once the text is on the clipboard; false where it can't be (plain http://, denied). */
  const copy = (text: string, what: string): Promise<boolean> => {
    if (!navigator.clipboard) { flash("failed"); return Promise.resolve(false); }
    return navigator.clipboard.writeText(text).then(() => { flash(what); return true; }, () => { flash("failed"); return false; });
  };

  // One click handler for the inserted HTML: section links, links between pages, copy buttons.
  const onClick = useCallback((e: MouseEvent<HTMLDivElement>) => {
    const t = e.target as HTMLElement;
    const btn = t.closest<HTMLButtonElement>("button[data-copy]");
    if (btn) {
      void copy(btn.parentElement?.querySelector("pre")?.textContent ?? "", "code").then((ok) => {
        btn.textContent = ok ? "Copied" : "Couldn’t copy";
        window.setTimeout(() => { btn.textContent = "Copy"; }, 1500);
      });
      return;
    }
    const a = t.closest<HTMLAnchorElement>("a");
    if (!a || e.metaKey || e.ctrlKey || e.shiftKey || e.button !== 0) return;
    const href = a.getAttribute("href") ?? "";
    if (a.classList.contains("anchor")) {
      e.preventDefault();
      navigate({ hash: href }, { replace: true });
      void copy(`${window.location.origin}/docs/${id}${href}`, "link");
    } else if (href.startsWith("#")) {
      e.preventDefault();
      navigate({ hash: href });
    } else if (href.startsWith("/docs/")) {
      e.preventDefault();
      navigate(href);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, navigate]);

  if (!meta) return <Empty>No docs page “{id}”. <Link to="/docs">All docs</Link></Empty>;
  if (doc.error) return <ErrorBox error={doc.error} retry={doc.reload} />;
  return (
    <article className="doc">
      <p className="crumbs"><Link to="/docs">Docs</Link> · Owl {ix.version}</p>
      {!doc.data ? <Loading /> : (
        // eslint-disable-next-line react/no-danger -- Owl's own build output, raw HTML refused by docsgen
        <div ref={body} className="doc-body" onClick={onClick} dangerouslySetInnerHTML={{ __html: doc.data.html }} />
      )}
      {copied && <div className="doc-toast" role="status">{copied === "failed" ? "Couldn’t copy — the browser allows it only over HTTPS" : copied === "link" ? "Link copied" : "Copied"}</div>}
    </article>
  );
}

function Topics({ ix, current, open, setOpen }: { ix: DocsIndex; current?: string; open: boolean; setOpen: (o: boolean, remember?: boolean) => void }) {
  const [q, setQ] = useState("");
  const hits = useMemo(() => search(ix, q), [ix, q]);
  const sectionsOf = (id: string) => ix.sections.filter((s) => s.page === id && s.level === 2);
  const title = (id: string) => ix.pages.find((p) => p.id === id)?.title ?? id;
  const picked = () => { if (narrow()) setOpen(false, false); };

  if (!open) {
    return (
      <button className="topics-tab" onClick={() => setOpen(true)} aria-expanded="false" aria-controls="doc-topics" title="Show topics">
        <span className="topics-arrow" aria-hidden>‹</span><span className="topics-tab-l">Topics</span>
      </button>
    );
  }
  return (
    <aside className="topics" id="doc-topics" aria-label="Docs topics">
      <div className="topics-head">
        <button className="icon-btn topics-collapse" onClick={() => setOpen(false)} aria-expanded="true" aria-controls="doc-topics" title="Hide topics">
          <span className="topics-arrow" aria-hidden>›</span>
        </button>
        <span className="topics-title">Topics</span>
      </div>
      <label className="pick-search">
        <Icon.search />
        <input type="search" placeholder="Search the docs…" aria-label="Search the docs" value={q} onChange={(e) => setQ(e.target.value)} />
      </label>
      {q.trim() ? (
        hits.length === 0 ? <p className="subtle">Nothing found.</p> : (
          <ul className="topics-hits">
            {hits.map((h) => (
              <li key={`${h.s.page}#${h.s.anchor}`}>
                <Link to={`/docs/${h.s.page}${h.s.anchor ? `#${h.s.anchor}` : ""}`} onClick={picked}>
                  <span className="hit-h">{h.s.heading}</span>
                  <span className="hit-p">{title(h.s.page)}</span>
                  {h.snippet && <span className="hit-s">{h.snippet}</span>}
                </Link>
              </li>
            ))}
          </ul>
        )
      ) : (
        <ul className="topics-pages">
          {ix.pages.map((p) => (
            <li key={p.id}>
              <Link to={`/docs/${p.id}`} className={p.id === current ? "active" : ""} aria-current={p.id === current ? "page" : undefined} onClick={picked}>{p.title}</Link>
              {p.id === current && (
                <ul className="topics-sections">
                  {sectionsOf(p.id).map((s) => (
                    <li key={s.anchor}><Link to={`/docs/${p.id}#${s.anchor}`} onClick={picked}>{s.heading}</Link></li>
                  ))}
                </ul>
              )}
            </li>
          ))}
        </ul>
      )}
    </aside>
  );
}

interface Hit { s: DocSection; score: number; snippet: ReactNode }

/** Every word must appear in the section (heading or text); heading matches rank first. */
export function search(ix: DocsIndex, query: string, limit = 30): Hit[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return [];
  const out: Hit[] = [];
  for (const s of ix.sections) {
    const head = s.heading.toLowerCase();
    const text = s.text.toLowerCase();
    if (!words.every((w) => head.includes(w) || text.includes(w))) continue;
    const score = words.reduce((n, w) => n + (head.includes(w) ? 10 : 0) + (head === w ? 5 : 0), 0) + (s.level === 1 ? 1 : 0);
    out.push({ s, score, snippet: snippet(s.text, words) });
  }
  return out.sort((a, b) => b.score - a.score).slice(0, limit);
}

/** ~140 characters of text around the first match, the matched words marked. */
function snippet(text: string, words: string[]): ReactNode {
  const lower = text.toLowerCase();
  const at = Math.min(...words.map((w) => lower.indexOf(w)).filter((i) => i >= 0));
  if (!Number.isFinite(at)) return null;
  const start = Math.max(0, at - 50);
  const part = (start > 0 ? "…" : "") + text.slice(start, start + 140) + (start + 140 < text.length ? "…" : "");
  const re = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})`, "gi");
  return part.split(re).map((p, i) => (i % 2 === 1 ? <mark key={i}>{p}</mark> : p));
}
