import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { api, useIsAdmin, writeErrorText, type ScannerStatus, type Severity, type Vuln, type VulnImage, type VulnPlace } from "../api";
import { Dialog } from "./Settings";
import { DocHelp } from "./Docs";
import { DOC_HELP } from "../dochelp";
import { paginate, usePageSize, useQueryState } from "../model";
import { ago, Card, date, Empty, ErrorBox, Loading, Msg, Pager, Tag, useApi } from "../ui";

// Image vulnerabilities from Trivy (milestone 15). A separate signal from the priority levels: nothing on
// these pages changes a component's level.

const sevLabel: Record<Severity, string> = { critical: "Critical", high: "High", medium: "Medium", low: "Low", unknown: "Unrated" };
// Findings without a severity: mostly distribution update notices (Debian DLA/DSA — routine package updates such
// as tzdata or ca-certificates, no CVE). They never count in the Critical/High numbers.
const UNRATED_HINT = "No severity from the source. Mostly Debian update notices (DLA/DSA): routine package updates such as tzdata or ca-certificates, not a CVE. They go away once the base image is rebuilt upstream and your image is rebuilt on it.";
const isUpdateNotice = (v: Vuln) => v.severity === "unknown" && /^(DLA|DSA)-/.test(v.id);
export const sevTone = (s: string) => (s === "critical" ? "critical" : s === "high" ? "warn" : "neutral");

/** "3C · 12H" for an image's fixable Critical/High findings; a state when there's no result. */
export function VulnCounts({ img, link = true }: { img: VulnImage; link?: boolean }) {
  const scanned = img.scannedAt != null;
  let body;
  if (!scanned) {
    body = <span className="subtle" style={{ margin: 0 }} title={img.error}>{img.state === "not scannable" ? "not scannable" : img.state}</span>;
  } else if (img.fixable.critical + img.fixable.high === 0) {
    const other = Object.values(img.total).reduce((a, n) => a + n, 0);
    body = other === 0
      ? <span className="tag tone-ok" title="No findings at all">✓ clean</span>
      : <span className="tag tone-ok" title={`No fixable Critical or High findings (${other} other — lower severity, no fix yet, or update notices)`}>✓ no C/H</span>;
  } else {
    body = (
      <span className="vcounts" title={`Fixable, not accepted: ${img.fixable.critical} Critical, ${img.fixable.high} High`}>
        {img.fixable.critical > 0 && <span className="tag tone-critical">{img.fixable.critical}C</span>}
        {img.fixable.high > 0 && <span className="tag tone-warn">{img.fixable.high}H</span>}
      </span>
    );
  }
  if (link && img.digest) return <Link to={`/vulns/${encodeURIComponent(img.digest)}`} className="vlink">{body}{img.state === "failed" && scanned && <span title={img.error}> ⚠</span>}</Link>;
  return body;
}

const placeKey = (p: { cluster: string; namespace?: string; kind?: string; workload?: string; container?: string }) =>
  `${p.cluster}/${p.namespace ?? ""}/${p.kind ?? ""}/${p.workload ?? ""}/${p.container ?? ""}`;

/** Images by where they run, for the Vulns columns on the cluster and component pages. */
export function useVulnsByPlace() {
  const v = useApi(api.vulns);
  return useMemo(() => {
    const m = new Map<string, VulnImage[]>();
    if (!v.data?.enabled) return null;
    for (const img of v.data.images) for (const p of img.places) {
      const k = placeKey(p);
      m.set(k, [...(m.get(k) ?? []), img]);
    }
    return (p: Parameters<typeof placeKey>[0]) => m.get(placeKey(p)) ?? [];
  }, [v.data]);
}

export function VulnCell({ images }: { images: VulnImage[] }) {
  if (images.length === 0) return <span className="muted" title="No running pods: nothing to scan">—</span>;
  return <div className="stack" style={{ gap: 2 }}>{images.map((img) => <VulnCounts key={img.key} img={img} />)}</div>;
}

type Show = "all" | "inhouse" | "third";

export default function VulnsPage() {
  const list = useApi(api.vulns);
  const qs = useQueryState();
  const show: Show = (["all", "inhouse", "third"] as string[]).includes(qs.get("show")) ? (qs.get("show") as Show) : "all";
  const affectedOnly = qs.get("affected") === "1";
  const [pageSize, setPageSize] = usePageSize();
  const [q, setQ] = useState("");

  if (list.error) return <ErrorBox error={list.error} retry={list.reload} />;
  if (!list.data) return <Loading />;
  const imgs = list.data.images.filter((i) =>
    (show === "all" || (show === "inhouse") === i.inHouse) &&
    (!affectedOnly || i.fixable.critical + i.fixable.high > 0) &&
    (!q || i.ref.toLowerCase().includes(q.toLowerCase()) || i.places.some((p) => p.workload.toLowerCase().includes(q.toLowerCase()))));
  const pg = paginate(imgs, Number(qs.get("page")), pageSize);
  const inHouse = list.data.images.filter((i) => i.inHouse);
  const sum = (xs: VulnImage[], k: "critical" | "high") => xs.reduce((a, i) => a + i.fixable[k], 0);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Vulnerabilities<DocHelp to={DOC_HELP.vulns} label="image scanning" /></h1>
          <p className="muted">What Trivy finds inside the running images. Separate from the priority levels: nothing here changes a component's level.</p>
        </div>
      </div>
      {!list.data.enabled && <div className="callout warn">Image scanning is off on this server (see Scanner below).</div>}

      <Card>
        <div className="kv-grid">
          <div><span>In-house images</span><span className="big">{inHouse.length}</span></div>
          <div><span>Fixable Critical (in-house)</span><span className="big">{sum(inHouse, "critical")}</span></div>
          <div><span>Fixable High (in-house)</span><span className="big">{sum(inHouse, "high")}</span></div>
        </div>
      </Card>

      <Card title={`Images (${imgs.length})`} actions={
        <div className="row" style={{ gap: 8 }}>
          <div className="row" role="group" aria-label="Show">
            {([["all", "All"], ["inhouse", "In-house"], ["third", "Third-party"]] as [Show, string][]).map(([t, l]) => (
              <button key={t} className={`chip${show === t ? " on" : ""}`} aria-pressed={show === t} onClick={() => qs.set({ show: t === "all" ? null : t, page: null })}>{l}</button>
            ))}
          </div>
          <button className={`chip${affectedOnly ? " on" : ""}`} aria-pressed={affectedOnly} onClick={() => qs.set({ affected: affectedOnly ? null : "1", page: null })}>With fixable Critical/High</button>
          <input className="input" type="search" placeholder="Search image or workload" value={q} onChange={(e) => { setQ(e.target.value); qs.set({ page: null }); }} aria-label="Search images" />
        </div>
      }>
        {imgs.length === 0 ? <Empty>No images in this view.</Empty> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Fixable</th><th>Image</th><th>Runs in</th><th>All findings</th><th>Scanned</th></tr></thead>
              <tbody>
                {pg.items.map((i) => (
                  <tr key={i.key}>
                    <td><VulnCounts img={i} /></td>
                    <td className="mono" style={{ wordBreak: "break-all" }}>
                      {i.digest ? <Link to={`/vulns/${encodeURIComponent(i.digest)}`}>{shortRef(i.ref)}</Link> : shortRef(i.ref)}
                      <div className="row" style={{ gap: 4, marginTop: 2 }}>
                        {i.inHouse && <Tag tone="info">in-house</Tag>}
                        {i.components.map((c) => <Link key={c} to={`/components/${encodeURIComponent(c)}`} className="tag">{c}</Link>)}
                        {i.os && <span className="subtle" style={{ margin: 0 }}>{i.os}</span>}
                      </div>
                    </td>
                    <td className="small"><Places places={i.places} /></td>
                    <td className="small">{i.scannedAt ? <SevSummary c={i.total} accepted={i.accepted} /> : "—"}</td>
                    <td className="small">{i.scannedAt ? ago(i.scannedAt) : "—"}{i.queue && <div><Tag>{i.queue}</Tag></div>}
                      {i.state === "failed" && <div className="err-text" style={{ margin: 0 }} title={i.error}>last attempt failed</div>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <Pager page={pg.page} pages={pg.pages} start={pg.start} shown={pg.items.length} total={pg.total} size={pageSize}
          onPage={(p) => qs.set({ page: p })} onSize={(n) => { setPageSize(n); qs.set({ page: null }); }} />
      </Card>

      <ScannerPanel onFinished={list.reload} />
    </>
  );
}

/** "registry/team/app@sha256:1234abcd…" */
export function shortRef(ref: string) {
  const [repo, digest] = ref.split("@");
  return digest ? `${repo}@${digest.slice(0, 19)}…` : ref;
}

function Places({ places }: { places: VulnPlace[] }) {
  const shown = places.slice(0, 3);
  return (
    <>
      {shown.map((p, k) => <div key={k}>{p.cluster} <span className="muted">· {p.namespace}/{p.workload}</span></div>)}
      {places.length > 3 && <div className="subtle" style={{ margin: 0 }}>+{places.length - 3} more</div>}
    </>
  );
}

function SevSummary({ c, accepted }: { c: Record<Severity, number>; accepted: number }) {
  const sevs = (["critical", "high", "medium", "low", "unknown"] as Severity[]).filter((s) => c[s] > 0);
  // Only "N Unrated" explains what unrated means; the rest keeps the general hint.
  return (
    <span title="Every finding, with or without a fix (accepted ones not counted)">
      {sevs.length === 0 ? "none" : sevs.map((s, k) => (
        <span key={s}>{k > 0 && " · "}<span title={s === "unknown" ? UNRATED_HINT : undefined}>{c[s]} {sevLabel[s]}</span></span>
      ))}
      {accepted > 0 && <span className="muted"> · {accepted} accepted</span>}
    </span>
  );
}

/** What the scanner is doing right now, its queue, recent scans and health. Polls while open. */
function ScannerPanel({ onFinished }: { onFinished: () => void }) {
  const st = useApi(api.scanner);
  const isAdmin = useIsAdmin();
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const reload = st.reload;
  useEffect(() => {
    const t = window.setInterval(reload, 3000);
    return () => window.clearInterval(t);
  }, [reload]);
  // When a scan finishes, the image list has new numbers.
  const newest = st.data?.status?.recent[0];
  const newestKey = newest ? `${newest.digest}/${newest.startedAt}` : "";
  const seen = useRef<string | null>(null);
  useEffect(() => {
    if (seen.current !== null && newestKey !== seen.current) onFinished();
    seen.current = newestKey;
  }, [newestKey, onFinished]);

  if (st.error) return <ErrorBox error={st.error} retry={st.reload} />;
  if (!st.data) return <Loading />;
  if (!st.data.enabled) {
    return <Card title="Scanner"><Empty>Scanning is off: {st.data.reason}. The server image runs Trivy; set OWL_SCAN=off only to switch it off on purpose.</Empty></Card>;
  }
  const s = st.data.status!;
  async function cancel(digest: string, what: string) {
    setMsg(null);
    try { await api.cancelScan(digest); setMsg({ ok: true, text: what }); st.reload(); }
    catch (e) { setMsg({ ok: false, text: writeErrorText(e) }); }
  }
  return (
    <Card title="Scanner" actions={<span className="subtle">One image at a time · refreshes every 3 s</span>}>
      <ScannerHealth s={s} />
      <div className="stack" style={{ gap: 6 }}>
        <span className="subtle" style={{ margin: 0 }}>Now scanning</span>
        {s.current ? (
          <div className="list-row">
            <span className="mono grow" style={{ wordBreak: "break-all" }}>{shortRef(s.current.ref)}</span>
            <Tag>{s.current.reason}</Tag>
            <Elapsed since={s.current.startedAt} limit={s.current.timeoutSeconds} />
            {isAdmin && <button className="btn small" onClick={() => void cancel(s.current!.digest, "Cancelled — the next daily rescan picks it up again.")}>Cancel</button>}
          </div>
        ) : <span className="muted">Nothing — {s.queueLength ? "waiting for the vulnerability DB" : "the queue is empty"}.</span>}
      </div>
      <div className="stack" style={{ gap: 6 }}>
        <span className="subtle" style={{ margin: 0 }}>Queue ({s.queueLength})</span>
        {s.queue.length === 0 ? <span className="muted">Empty.</span> : s.queue.map((j) => (
          <div key={j.digest} className="list-row">
            <span className="mono grow" style={{ wordBreak: "break-all" }}>{shortRef(j.ref)}</span>
            <Tag>{j.reason}</Tag>
            {isAdmin && <button className="btn small" onClick={() => void cancel(j.digest, "Removed from the queue.")}>Remove</button>}
          </div>
        ))}
        {s.queueLength > s.queue.length && <span className="subtle" style={{ margin: 0 }}>…and {s.queueLength - s.queue.length} more</span>}
      </div>
      <div className="stack" style={{ gap: 6 }}>
        <span className="subtle" style={{ margin: 0 }}>Recent</span>
        {s.recent.length === 0 ? <span className="muted">No scans since the server started.</span> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Image</th><th>Why</th><th>Result</th><th>Took</th><th>When</th></tr></thead>
              <tbody>
                {s.recent.map((f, k) => (
                  <tr key={k}>
                    <td className="mono" style={{ wordBreak: "break-all" }}><Link to={`/vulns/${encodeURIComponent(f.digest)}`}>{shortRef(f.ref)}</Link></td>
                    <td className="small">{f.reason}</td>
                    <td className="small"><Tag tone={f.outcome === "ok" ? "ok" : f.outcome === "failed" || f.outcome === "timeout" ? "critical" : "neutral"}>{f.outcome}</Tag>
                      {f.error && <div className="subtle" style={{ margin: 0 }}>{f.error}</div>}</td>
                    <td className="small">{f.outcome === "removed from queue" ? "—" : seconds(f.tookMs)}</td>
                    <td className="small">{ago(f.startedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <Msg msg={msg} />
    </Card>
  );
}

function ScannerHealth({ s }: { s: ScannerStatus }) {
  return (
    <div className="stack" style={{ gap: 8 }}>
      <div className="kv-grid">
        <div><span>Vulnerability DB</span><span>{s.db.updatedAt ? <>Trivy {s.db.version} · updated {ago(s.db.updatedAt)}</> : s.db.updating ? "downloading…" : "not downloaded yet"}</span></div>
        <div><span>Last daily rescan</span><span>{s.lastDaily ? ago(s.lastDaily) : "not yet"}</span></div>
        <div><span>Registry logins</span><span className="mono small">{s.credentialPrefixes.length ? s.credentialPrefixes.join(", ") : "none (public images only)"}</span></div>
      </div>
      {s.db.updating && s.db.updatedAt && <div className="callout info">Updating the vulnerability DB…</div>}
      {s.db.error && <div className="callout warn"><span>DB update failed: {s.db.error}{s.db.updatedAt ? " — scans use the copy from " + date(s.db.updatedAt) : ""}</span></div>}
      {s.authFailures.map((a) => (
        <div key={a.prefix} className="callout critical"><span><b>Auth failed for {a.prefix}</b> ({ago(a.at)}, {shortRef(a.ref)}): check the login in <Link to="/settings/registries">Settings → Registries</Link> (or in OWL_REGISTRY_AUTH_FILE, for a login from the file).</span></div>
      ))}
      {(s.credentialsExpiring ?? []).map((e) => (
        <div key={e.prefix} className={`callout ${e.expired ? "critical" : "warn"}`}><span><b>Registry login {e.prefix} {e.expired ? "expired" : "expires"} {date(e.expiresAt)}</b>
          {e.expired ? " — scans with it fail" : ""}: renew the token and replace it in <Link to="/settings/registries">Settings → Registries</Link> (or in OWL_REGISTRY_AUTH_FILE).</span></div>
      ))}
    </div>
  );
}

function Elapsed({ since, limit }: { since: string; limit: number }) {
  const [, tick] = useState(0);
  useEffect(() => { const t = window.setInterval(() => tick((n) => n + 1), 1000); return () => window.clearInterval(t); }, []);
  const s = Math.max(0, Math.round((Date.now() - new Date(since).getTime()) / 1000));
  return <span className="small mono" title="Trivy reports no progress: elapsed time against the time limit">{fmtDur(s)} / {fmtDur(limit)}</span>;
}

const fmtDur = (s: number) => (s >= 60 ? `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s` : `${s}s`);
const seconds = (ms: number) => fmtDur(Math.round(ms / 1000));

// ---------------- one image ----------------

export function VulnImagePage() {
  const { digest = "" } = useParams();
  const d = useApi(() => api.vulnImage(digest), [digest]);
  const isAdmin = useIsAdmin();
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [accepting, setAccepting] = useState<Vuln | null>(null);
  const [fixableOnly, setFixableOnly] = useState(true);
  const [sev, setSev] = useState<Severity | "">("");
  const [showAccepted, setShowAccepted] = useState(false);
  const [pageSize, setPageSize] = usePageSize();
  const [page, setPage] = useState(1);

  if (d.error) return <ErrorBox error={d.error} retry={d.reload} />;
  if (!d.data || d.data.image.digest !== digest) return <Loading />;
  const { image: img, vulns, confirmed } = d.data;
  const open = vulns.filter((v) => !v.accepted);
  const accepted = vulns.filter((v) => v.accepted);
  const rows = open.filter((v) => (!fixableOnly || v.fixed) && (!sev || v.severity === sev));
  const pg = paginate(rows, page, pageSize);
  const bySev = (s: Severity) => open.filter((v) => (!fixableOnly || v.fixed) && v.severity === s).length;
  const repo = img.repositories[0] ?? img.ref.split("@")[0];

  async function rescan() {
    setMsg(null);
    try {
      const r = await api.rescan(digest);
      setMsg({ ok: true, text: r.queued ? "Queued first — see the Scanner on the Vulnerabilities page." : "Already queued or being scanned." });
      d.reload();
    } catch (e) { setMsg({ ok: false, text: writeErrorText(e) }); }
  }
  async function unaccept(v: Vuln) {
    setMsg(null);
    try { await api.unacceptVuln(v.accepted!.repository, v.id); setMsg({ ok: true, text: `${v.id} counts again.` }); d.reload(); }
    catch (e) { setMsg({ ok: false, text: writeErrorText(e) }); }
  }

  return (
    <>
      <p className="crumbs"><Link to="/vulns">Vulnerabilities</Link> / {repo.split("/").pop()}</p>
      <div className="page-head">
        <div>
          <h1 className="mono" style={{ wordBreak: "break-all", fontSize: 20 }}>{repo}</h1>
          <p className="muted mono small" style={{ wordBreak: "break-all" }}>{img.digest}</p>
        </div>
        {isAdmin && <button className="btn" onClick={() => void rescan()}>Rescan now</button>}
      </div>
      <Msg msg={msg} />
      {img.state === "failed" && <div className="callout warn"><span>The last scan attempt failed: {img.error}{img.scannedAt ? ` — showing the result from ${ago(img.scannedAt)}.` : ""}</span></div>}

      <Card>
        <div className="kv-grid">
          <div><span>Fixable Critical / High</span><span className="big"><VulnCounts img={img} link={false} /></span></div>
          <div><span>All findings</span><span>{img.scannedAt ? <SevSummary c={img.total} accepted={img.accepted} /> : img.state}</span></div>
          <div><span>Scanned</span><span>{img.scannedAt ? ago(img.scannedAt) : "—"}{img.os && <div className="subtle" style={{ margin: 0 }}>{img.os}</div>}</span></div>
        </div>
        <div className="row" style={{ gap: 4 }}>
          {img.inHouse && <Tag tone="info">in-house</Tag>}
          {img.components.map((c) => <Link key={c} to={`/components/${encodeURIComponent(c)}`} className="tag">{c}</Link>)}
        </div>
        <div className="small"><Places places={img.places} /></div>
      </Card>

      {confirmed.length > 0 && (
        <div className="callout info"><span>
          {confirmed.length === 1 ? "1 finding is" : `${confirmed.length} findings are`} also a security advisory of the component itself (
          {confirmed.map((c, k) => <span key={c.id}>{k > 0 && ", "}<Link to={`/components/${encodeURIComponent(c.component)}`}>{c.id}</Link></span>)}
          ): shown there, marked “Trivy: confirmed”, and not counted again here.
        </span></div>
      )}

      <Card title={`Findings (${rows.length})`} actions={
        <div className="row" style={{ gap: 8 }}>
          <div className="row" role="group" aria-label="Severity">
            <button className={`chip${sev === "" ? " on" : ""}`} aria-pressed={sev === ""} onClick={() => { setSev(""); setPage(1); }}>All</button>
            {(["critical", "high", "medium", "low", "unknown"] as Severity[]).filter((s) => bySev(s) > 0).map((s) => (
              <button key={s} className={`chip${sev === s ? " on" : ""}`} aria-pressed={sev === s} onClick={() => { setSev(s); setPage(1); }} title={s === "unknown" ? UNRATED_HINT : undefined}>{sevLabel[s]} {bySev(s)}</button>
            ))}
          </div>
          <button className={`chip${fixableOnly ? " on" : ""}`} aria-pressed={fixableOnly} onClick={() => { setFixableOnly(!fixableOnly); setPage(1); }}>Fixable only</button>
        </div>
      }>
        {!img.scannedAt ? <Empty>Not scanned yet.</Empty> : rows.length === 0 ? <Empty>{fixableOnly && open.length ? "No findings with a fix." : "No findings."}</Empty> : (
          <VulnTable rows={pg.items} action={isAdmin ? (v) => <button className="btn small" onClick={() => setAccepting(v)} title="The team agreed to leave this one: it stops counting">Accept…</button> : undefined} />
        )}
        <Pager page={pg.page} pages={pg.pages} start={pg.start} shown={pg.items.length} total={pg.total} size={pageSize}
          onPage={setPage} onSize={(n) => { setPageSize(n); setPage(1); }} />
      </Card>

      {accepted.length > 0 && (
        <Card title={`Accepted (${accepted.length})`} actions={<button className="btn small" onClick={() => setShowAccepted(!showAccepted)}>{showAccepted ? "Hide" : "Show"}</button>}>
          <p className="subtle" style={{ margin: 0 }}>The team agreed to leave these in {repo} (every tag and digest). They don't count until the acceptance is removed or expires.</p>
          {showAccepted && <VulnTable rows={accepted} accepted action={isAdmin ? (v) => <button className="btn small" onClick={() => void unaccept(v)}>Remove</button> : undefined} />}
        </Card>
      )}

      {accepting && <AcceptDialog repo={repo} v={accepting} onClose={() => setAccepting(null)} onDone={(t) => { setAccepting(null); setMsg({ ok: true, text: t }); d.reload(); }} />}
    </>
  );
}

function VulnTable({ rows, accepted, action }: { rows: Vuln[]; accepted?: boolean; action?: (v: Vuln) => ReactNode }) {
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th>Severity</th><th>Vulnerability</th><th>Package</th><th>Installed</th><th>Fixed in</th>{accepted && <th>Why accepted</th>}{action && <th />}</tr></thead>
        <tbody>
          {rows.map((v, k) => (
            <tr key={`${v.id}/${v.package}/${v.installed}/${k}`}>
              <td><Tag tone={sevTone(v.severity)} title={v.severity === "unknown" ? UNRATED_HINT : undefined}>{isUpdateNotice(v) ? "Update notice" : sevLabel[v.severity]}</Tag></td>
              <td>{v.url ? <a href={v.url} target="_blank" rel="noreferrer noopener" className="mono">{v.id}</a> : <span className="mono">{v.id}</span>}
                {v.title && <div className="subtle" style={{ margin: 0 }}>{v.title}</div>}
                {v.expiredAcceptance && <Tag tone="warn" title="An acceptance existed but its date passed">acceptance expired</Tag>}</td>
              <td><span className="mono">{v.package}</span><div className="subtle" style={{ margin: 0 }}>{v.class === "os" ? "OS package" : `${v.pkgType ?? "library"}${v.target ? ` · ${v.target}` : ""}`}</div></td>
              <td className="mono small">{v.installed}</td>
              <td className="mono small">{v.fixed || <span className="muted">no fix</span>}</td>
              {accepted && <td className="small">{v.accepted?.reason}<div className="subtle" style={{ margin: 0 }}>{v.accepted?.createdBy} · {date(v.accepted?.createdAt)}{v.accepted?.expiresAt ? ` · until ${date(v.accepted.expiresAt)}` : ""}</div></td>}
              {action && <td>{action(v)}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function AcceptDialog({ repo, v, onClose, onDone }: { repo: string; v: Vuln; onClose: () => void; onDone: (text: string) => void }) {
  const [reason, setReason] = useState("");
  const [expires, setExpires] = useState("");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  return (
    <Dialog title={`Accept ${v.id}`} onClose={onClose}>
      <p className="muted" style={{ margin: 0 }}>
        The team agrees to leave <b>{v.id}</b> ({v.package} {v.installed}) in <span className="mono">{repo}</span> — every tag and digest of it.
        It stops counting until you remove the acceptance or it expires. Recorded in the audit log.
      </p>
      <form className="stack" onSubmit={async (e) => {
        e.preventDefault();
        setBusy(true); setMsg(null);
        try { await api.acceptVuln({ repository: repo, vulnId: v.id, reason, expiresAt: expires }); onDone(`${v.id} accepted for ${repo}.`); }
        catch (err) { setMsg({ ok: false, text: writeErrorText(err) }); }
        finally { setBusy(false); }
      }}>
        <label className="field">Why (required)
          <textarea className="input" rows={3} maxLength={500} required value={reason} onChange={(e) => setReason(e.target.value)}
            placeholder="e.g. the vulnerable function isn't reachable: the service never parses untrusted input" />
        </label>
        <label className="field">Expires (optional — it counts again after this day)
          <input className="input" type="date" value={expires} min={new Date(Date.now() + 86400e3).toISOString().slice(0, 10)} onChange={(e) => setExpires(e.target.value)} />
        </label>
        <div className="row"><button className="btn primary" disabled={busy || !reason.trim()}>Accept</button><button type="button" className="btn" onClick={onClose}>Cancel</button></div>
        <Msg msg={msg} />
      </form>
    </Dialog>
  );
}
