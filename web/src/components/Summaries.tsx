import { useState } from "react";
import { Link } from "react-router-dom";
import { api, usd, useIsAdmin, writeErrorText, type JumpView, type SummaryItem, type SummaryOverview, type SummaryView } from "../api";
import { ago, Card, Empty, Msg, Tag } from "../ui";

// Everything a summary says came from Claude reading upstream release notes. It's rendered as plain
// text (React escapes it); the only links are the release-note URLs Owl itself collected.

const riskTone = { low: "ok", medium: "warn", high: "critical" } as const;

export function RiskTag({ s }: { s: SummaryView }) {
  return <Tag tone={riskTone[s.risk.level]} title={`${s.risk.reason} — Claude's estimate of how risky the upgrade is, not its priority`}>
    Upgrade risk: {s.risk.level}
  </Tag>;
}

/** One line for the side panel and Top actions: the first ready summary of a component. */
export function SummaryHeadline({ jumps }: { jumps: JumpView[] }) {
  const j = jumps.find((x) => x.summary);
  if (!j?.summary) return null;
  return (
    <span className="row" style={{ gap: 6, flexWrap: "wrap" }}>
      <span className="subtle" style={{ margin: 0 }}>{j.from} → {j.to}:</span>
      <span>{j.summary.headline}</span>
      <RiskTag s={j.summary} />
    </span>
  );
}

/** Cards for every current jump of one component (component page). */
export function SummaryCards({ overview, componentId, onChanged }: { overview: SummaryOverview; componentId: string; onChanged: () => void }) {
  const jumps = overview.jumps.filter((j) => j.component === componentId);
  if (jumps.length === 0) return null;
  return (
    <Card title="Upgrade summaries" actions={<span className="subtle">Written by Claude from upstream release notes — verify before acting</span>}>
      <div className="stack">
        {jumps.map((j) => <JumpCard key={`${j.from}>${j.to}`} j={j} onChanged={onChanged} />)}
      </div>
    </Card>
  );
}

function JumpCard({ j, onChanged }: { j: JumpView; onChanged: () => void }) {
  const admin = useIsAdmin();
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  async function generate(force: boolean) {
    setBusy(true); setMsg(null);
    try { await api.generateSummary(j, force); onChanged(); }
    catch (e) { setMsg({ ok: false, text: writeErrorText(e) }); onChanged(); }
    finally { setBusy(false); }
  }
  const cost = j.maxCostMicros ? `at most ${usd(j.maxCostMicros)}` : "";
  const tokens = j.tokens ? `${j.exact ? "" : "~"}${j.tokens.toLocaleString()} input tokens${j.exact ? "" : " (estimate)"}` : "";
  const button = (label: string, force = false) => admin
    ? <button className="btn small" disabled={busy} onClick={() => generate(force)}>{busy ? "Writing… (up to a minute)" : `${label}${cost ? ` — ${cost}` : ""}`}</button>
    : <span className="subtle" style={{ margin: 0 }}>Only admins can generate summaries.</span>;

  let body;
  switch (j.state) {
    case "ready": body = <Summary s={j.summary!} />; break;
    case "off": body = <p className="subtle">Summaries are off. <Link to="/settings/summaries">Settings → Summaries</Link></p>; break;
    case "collecting": body = <p className="subtle">Collecting release notes…</p>; break;
    case "no_notes": body = <p className="subtle">No release notes available for this upgrade, so there's nothing to summarize.</p>; break;
    case "error": body = <div className="callout warn">Release notes couldn't be fetched: {j.error}</div>; break;
    case "estimate": body = <p className="subtle">Estimate only (nothing is sent): {tokens}, {cost}.</p>; break;
    case "no_key": body = <p className="subtle">ANTHROPIC_API_KEY is not set on the server.</p>; break;
    case "paused": body = <div className="callout warn">Summaries are paused after repeated failures. <Link to="/settings/summaries">Settings → Summaries</Link></div>; break;
    case "too_large": body = <div className="stack"><p className="subtle">Larger than the input limit: {tokens}. Not written automatically.</p>{button("Generate anyway", true)}</div>; break;
    case "failed": body = <div className="stack"><div className="callout warn">Failed today: {j.error}</div>{button("Try again")}</div>; break;
    case "budget": body = <p className="subtle">Waiting for budget: {cost} doesn't fit what's left of today's or this month's cap.</p>; break;
    case "available": body = <div className="stack"><p className="subtle">{tokens}.</p>{button("Generate")}</div>; break;
    case "queued": body = <p className="subtle">Queued: written automatically ({cost}).</p>; break;
  }
  return (
    <div className="inst">
      <div className="row" style={{ justifyContent: "space-between" }}>
        <b className="mono">{j.from} → {j.to}</b>
        {j.state === "ready" && admin && <button className="btn small" disabled={busy} onClick={() => generate(true)}>{busy ? "Writing…" : `Regenerate${cost ? ` — ${cost}` : ""}`}</button>}
      </div>
      {body}
      <Msg msg={msg} />
      {j.sources && j.sources.length > 0 && (
        <p className="subtle" style={{ margin: 0 }}>Release notes: {j.sources.map((s, i) => <span key={s.version}>{i > 0 && ", "}<a href={s.url} target="_blank" rel="noreferrer noopener">{s.version}</a></span>)}</p>
      )}
    </div>
  );
}

const pathText = { direct_supported: "Direct upgrade supported", via_intermediate: "Upgrade via intermediate versions", not_stated: "Not stated in the release notes" };

function Summary({ s }: { s: SummaryView }) {
  const url = new Map(s.sources.map((x) => [x.version, x.url]));
  const sections: [string, SummaryItem[]][] = [
    ["Breaking changes", s.breakingChanges], ["Migrations / manual steps", s.migrations], ["Deprecations", s.deprecations],
    ["Security fixes", s.securityFixes], ["Notable features", s.notableFeatures],
  ];
  return (
    <div className="stack">
      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}><b>{s.headline}</b><RiskTag s={s} /></div>
      <p style={{ margin: 0 }}><span className="muted">Upgrade risk (Claude's estimate):</span> {s.risk.reason}</p>
      <p style={{ margin: 0 }}>
        <span className="muted">Path:</span> {pathText[s.upgradePath.direct]}{s.upgradePath.via.length > 0 && <> — via <span className="mono">{s.upgradePath.via.join(" → ")}</span></>}
        {s.upgradePath.reason && <span className="subtle"> ({s.upgradePath.reason})</span>}
      </p>
      {sections.filter(([, items]) => items.length > 0).map(([title, items]) => (
        <div key={title}>
          <h3 style={{ margin: "4px 0" }}>{title}</h3>
          <ul className="bullets">
            {items.map((it, k) => (
              <li key={k}>{it.text} {it.version && (url.has(it.version)
                ? <a className="subtle" href={url.get(it.version)} target="_blank" rel="noreferrer noopener">({it.version})</a>
                : <span className="subtle">({it.version})</span>)}</li>
            ))}
          </ul>
        </div>
      ))}
      {sections.every(([, items]) => items.length === 0) && <Empty>No breaking changes, migrations, deprecations, security fixes or notable features in the notes.</Empty>}
      <p className="subtle" style={{ margin: 0 }}>Written by {s.model} {ago(s.createdAt)} from upstream release notes — verify before acting.</p>
    </div>
  );
}
