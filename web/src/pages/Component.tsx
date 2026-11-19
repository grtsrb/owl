import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, useIsAdmin, writeErrorText, type ComponentReport, type Match } from "../api";
import { ago, Card, date, Empty, ErrorBox, LevelBadge, Loading, Tag, useApi } from "../ui";
import { SummaryCards } from "../components/Summaries";

export default function ComponentPage() {
  const { id = "" } = useParams();
  const report = useApi(api.report, []);
  const detail = useApi(() => api.component(id), [id]);
  const policies = useApi(api.policies, []);
  const summaries = useApi(api.summaries, []);

  if (report.error) return <ErrorBox error={report.error} retry={report.reload} />;
  if (detail.error) return <ErrorBox error={detail.error} retry={detail.reload} />;
  // While another component loads, never show the previous one's data under this URL.
  if (!report.data || !detail.data || detail.data.component.id !== id) return <Loading />;
  const c = report.data.components.find((x) => x.id === id);
  const d = detail.data;
  const up = d.upstream;

  return (
    <>
      <p className="crumbs"><Link to="/">Overview</Link> / {d.component.name}</p>
      <div className="page-head">
        <div>
          <h1 className="with-badge">{d.component.name} {c && <LevelBadge level={c.level} />}</h1>
          <p className="muted">
            {d.component.github && <a href={`https://github.com/${d.component.github}`} target="_blank" rel="noreferrer noopener">{d.component.github}</a>}
            {up && <> · upstream data {ago(up.fetchedAt)}{up.stale && " (stale — upstream unreachable)"}</>}
          </p>
        </div>
      </div>

      {d.component.notes && <div className="callout">{d.component.notes}</div>}
      {up?.retired && (
        <div className="callout critical">
          Upstream project is <strong>retired</strong>: the repository is archived and the last release was {date(up.lastReleaseAt)}. Plan a replacement.
        </div>
      )}
      {up && !up.retired && up.archived && (
        <div className="callout warn">The upstream repository is archived but still publishing releases — check where development moved.</div>
      )}
      {up && !up.advisorySource && (
        <div className="callout">No advisory data for this component upstream — vulnerability status is <strong>unknown</strong>, not clean.</div>
      )}
      {up?.errors?.length ? <div className="callout warn">Upstream fetch problems: {up.errors.join("; ")}</div> : null}

      <div className="grid-2">
        <Card title="Target">
          {c?.target ? (
            <dl className="kv">
              <dt>Target version</dt><dd className="mono big">{c.target.version}</dd>
              <dt>Policy</dt><dd>{c.target.policy}{c.target.note && <div className="subtle">{c.target.note}</div>}</dd>
              <dt>Latest upstream</dt><dd className="mono">{up?.latestStable ?? "—"}</dd>
            </dl>
          ) : <Empty>No target (no upstream release data).</Empty>}
          {policies.data && (
            <PolicyEditor
              key={id}
              scope={id}
              current={policies.data.overrides[id]}
              inherited={policies.data.global}
              presets={policies.data.presets}
              onSaved={() => { policies.reload(); report.reload(); }}
            />
          )}
        </Card>
        <Card title="Promotion drift">
          {c && c.drift.length > 0 ? (
            <ul className="list">
              {c.drift.map((x, i) => (
                <li key={i}><Tag tone={x.kind === "anomaly" ? "warn" : x.kind === "only_in" ? "info" : "neutral"}>{x.kind === "anomaly" ? "anomaly" : x.kind === "only_in" ? "info" : "pending"}</Tag> <span className="muted">{x.tenant}:</span> {x.text}</li>
              ))}
            </ul>
          ) : <Empty>Same version along the promotion path in every tenant.</Empty>}
        </Card>
      </div>

      {summaries.data && <SummaryCards overview={summaries.data} componentId={id} onChanged={summaries.reload} />}
      {c && <Instances c={c} />}
      <Advisories d={d.advisoriesByVersion} />

      <Card title={`Release lines${up?.linesSource ? ` (${up.linesSource})` : ""}`}>
        {up && up.lines.length ? (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Line</th><th>Latest</th><th>Released</th><th>Support</th><th>End of life</th></tr></thead>
              <tbody>
                {up.lines.slice(0, 15).map((l) => (
                  <tr key={l.name} className={c?.target?.line === l.name ? "row-target" : ""}>
                    <td className="mono">{l.name} {c?.target?.line === l.name && <Tag tone="info">target</Tag>}</td>
                    <td className="mono">{l.latest || "—"}</td>
                    <td>{date(l.latestDate)}</td>
                    <td>{l.lts && <Tag tone="info">LTS</Tag>} {l.eol ? <Tag tone="critical">EOL</Tag> : l.maintained === false ? <Tag tone="warn">unmaintained</Tag> : null}</td>
                    <td>{date(l.eolDate)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : <Empty>No release data.</Empty>}
      </Card>
    </>
  );
}

function Instances({ c }: { c: ComponentReport }) {
  return (
    <Card title={`Where it runs (${c.instances.length})`}>
      <div className="table-wrap">
        <table>
          <thead><tr><th>Level</th><th>Cluster</th><th>Workload</th><th>Version</th><th>Recommended action</th><th>Why</th></tr></thead>
          <tbody>
            {c.instances.map((i, n) => (
              <tr key={n}>
                <td><LevelBadge level={i.score.level} /></td>
                <td>{i.cluster}<div className="subtle">{i.stage} · {i.tenant}</div></td>
                <td>
                  {i.namespace ? `${i.namespace}/${i.workload}` : "cluster"}
                  <div className="subtle">{i.kind}{i.container ? ` · ${i.container}` : ""}{i.kind ? ` · ${i.desiredReplicas} replicas` : ""}</div>
                  {(i.running?.length ?? 0) > 1 && <Tag tone="warn" title={i.running!.map((r) => `${r.image}: ${r.ready}/${r.pods} ready`).join("\n")}>rollout in progress</Tag>}
                </td>
                <td className="mono">{i.version || "?"}<div className="subtle mono" title={i.image}>{i.image.length > 48 ? "…" + i.image.slice(-46) : i.image}</div>
                  {i.bundledWith && <BundledTag by={i.bundledName || distroName(i.bundledWith)} />}</td>
                <td><b>{i.action.text}</b><div className="subtle">{i.action.why}</div></td>
                <td>
                  {i.score.reasons.length === 0 ? <span className="muted">At target, latest patch.</span> : <Reasons reasons={i.score.reasons} />}
                  {i.score.notes?.map((note, k) => <div key={k} className="subtle">ℹ {note}</div>)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

export const distroName = (d: string) => (d === "rke2" ? "RKE2" : d);

/** An image something else ships and upgrades (bundledWith in the catalog): RKE2, k3s or a component like Cilium. */
export function BundledTag({ by }: { by: string }) {
  const d = by;
  return <Tag tone="info" title={`${d} ships this image and upgrades it with ${d} itself — upgrade ${d} to get a newer one.`}>{d}-managed</Tag>;
}

function Reasons({ reasons }: { reasons: { level: string; text: string }[] }) {
  const item = (r: { level: string; text: string }, k: number) => (
    <li key={k}><span className={`dot lvl-${r.level}`} aria-hidden /> {r.text}</li>
  );
  const shown = 4;
  return (
    <ul className="reasons">
      {reasons.slice(0, shown).map(item)}
      {reasons.length > shown && (
        <li>
          <details>
            <summary className="subtle">+{reasons.length - shown} more</summary>
            <ul className="reasons">{reasons.slice(shown).map(item)}</ul>
          </details>
        </li>
      )}
    </ul>
  );
}

function Advisories({ d }: { d: { version: string; advisories: Match[]; error?: string }[] }) {
  if (d.length === 0) return null;
  return (
    <Card title="Security advisories by running version">
      {d.map((v) => {
        const affected = v.advisories.filter((a) => a.certain);
        const review = v.advisories.filter((a) => !a.certain);
        return (
          <details key={v.version} className="adv-group" open={affected.some((a) => a.severity === "critical" || a.severity === "high")}>
            <summary>
              <span className="mono">{v.version}</span> — {v.error ? <span className="muted">{v.error}</span> : (
                <>{affected.length} affecting{review.length > 0 && `, ${review.length} need review`}</>
              )}
            </summary>
            {affected.length + review.length === 0 ? <Empty>No known advisories for this version.</Empty> : (
              <table>
                <thead><tr><th>Severity</th><th>Advisory</th><th>Fixed in</th></tr></thead>
                <tbody>
                  {[...affected, ...review].map((a) => (
                    <tr key={a.id}>
                      <td><Tag tone={sevTone(a.severity)}>{a.severity || "unknown"}</Tag>{!a.certain && <Tag title="Upstream data doesn't say which versions are fixed">review</Tag>}{a.noFix && <Tag title="No fixed version listed">no fix</Tag>}</td>
                      <td><a href={a.url} target="_blank" rel="noreferrer noopener" className="mono">{label(a)}</a><div className="subtle">{a.summary}</div></td>
                      <td className="mono" title={a.fixedIn?.length ? `All fixed versions: ${a.fixedIn.join(", ")}` : undefined}>
                        {a.relevantFix || a.fixedIn?.join(", ") || "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </details>
        );
      })}
    </Card>
  );
}

function PolicyEditor({ scope, current, inherited, presets, onSaved }: {
  scope: string; current?: string; inherited: string; presets: string[]; onSaved: () => void;
}) {
  const [value, setValue] = useState(current ?? "");
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const hasToken = useIsAdmin();

  async function run(fn: () => Promise<unknown>, ok: string): Promise<boolean> {
    setBusy(true); setMsg(null);
    try { await fn(); setMsg({ ok: true, text: ok }); onSaved(); return true; }
    catch (e) { setMsg({ ok: false, text: writeErrorText(e) }); return false; }
    finally { setBusy(false); }
  }

  return (
    <form className="policy-form" onSubmit={(e) => { e.preventDefault(); run(() => api.setPolicy(scope, value), "Policy saved."); }}>
      <label htmlFor="policy">Policy for this component</label>
      <div className="row">
        <input id="policy" className="input mono" list="presets" placeholder={`inherited: ${inherited}`} value={value} onChange={(e) => setValue(e.target.value)} />
        <datalist id="presets">{presets.filter((p) => !p.includes("<")).map((p) => <option key={p} value={p} />)}</datalist>
        <button className="btn primary" disabled={busy || !value || !hasToken}>Save</button>
        {current && <button type="button" className="btn" disabled={busy || !hasToken} onClick={async () => { if (await run(() => api.clearPolicy(scope), "Override removed.")) setValue(""); }}>Use global</button>}
      </div>
      <p className="subtle">e.g. <code>lts-or-n-1</code>, <code>latest</code>, <code>n-2</code>, <code>pin:16</code>{!hasToken && <> · only admins can edit</>}</p>
      {msg && <p className={msg.ok ? "ok-text" : "err-text"}>{msg.text}</p>}
    </form>
  );
}

function label(a: Match) {
  return [a.id, ...(a.aliases ?? [])].find((x) => x.startsWith("CVE-")) ?? a.id;
}

function sevTone(s: string) {
  return s === "critical" || s === "high" ? "critical" : s === "moderate" || s === "medium" ? "warn" : "neutral";
}
