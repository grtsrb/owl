import { useState } from "react";
import { api, usd, useIsAdmin, writeErrorText, type SummaryMode, type SummarySettings, type SummaryStatus } from "../api";
import { Icon } from "../icons";
import { ago, Card, Empty, ErrorBox, Loading, Msg, Tag, useApi } from "../ui";

const MODES: [SummaryMode, string, string][] = [
  ["off", "Off", "Nothing is fetched or sent. Existing summaries are still shown."],
  ["estimate", "Estimate only", "Release notes are collected and the cost of each summary is estimated locally. Nothing is sent to Claude."],
  ["manual", "Manual", "A summary is written only when an admin clicks Generate (the button shows the maximum cost)."],
  ["auto", "Automatic", "Missing summaries are written in the background, one at a time, within the caps."],
];

export default function SummariesTab() {
  if (!useIsAdmin()) {
    return <Card title="Summaries"><div className="callout info"><Icon.info /><span>Only admins can see spending and change summary settings.</span></div></Card>;
  }
  return <Inner />;
}

function Inner() {
  const st = useApi(api.summaryStatus);
  if (st.error) return <ErrorBox error={st.error} retry={st.reload} />;
  if (!st.data) return <Loading />;
  const s = st.data;
  return (
    <>
      <Card title="Claude upgrade summaries">
        {!s.enabled && <div className="callout warn"><Icon.warn /><span>ANTHROPIC_API_KEY is not set on the server: only Off and Estimate only work.</span></div>}
        {s.paused && <PausedNotice reason={s.paused} onResumed={st.reload} />}
        <div className="kv-grid">
          <div><span>Model</span><span>{label(s, s.model)}{s.fallback ? <span className="subtle"> (refusal fallback: {label(s, s.fallback)})</span> : <span className="subtle"> (no refusal fallback)</span>}</span></div>
          <div><span>Price per million tokens</span><span>{s.price ? `${perM(s.price.Input)} in / ${perM(s.price.Output)} out` : "unknown"}</span></div>
          <div><span>Summaries stored</span><span>{s.summaries}</span></div>
        </div>
        <div className="kv-grid">
          <div><span>Spent today (UTC)</span><b>{usd(s.spentTodayMicros)} <span className="subtle">of {usd(s.settings.dailyCapMicros)}</span></b></div>
          <div><span>Spent this month</span><b>{usd(s.spentMonthMicros)} <span className="subtle">of {usd(s.settings.monthlyCapMicros)}</span></b></div>
          <div><span>Spent in total</span><b>{usd(s.spentTotalMicros)}</b></div>
        </div>
        <p className="subtle" style={{ margin: 0 }}>
          Before every request Owl counts its tokens exactly (free) and prices the worst case: all input plus the full output limit
          {s.fallback ? ", plus the same again on the fallback model" : ""}. A request that could exceed what's left of either cap isn't sent.
          For a hard outer limit, give Owl its own Claude Console workspace with a monthly spend limit.
        </p>
      </Card>
      <SettingsForm s={s} onSaved={st.reload} />
      <Card title="Recent requests" actions={<span className="subtle">Component and versions only — no cluster data is ever sent</span>}>
        {s.requests.length === 0 ? <Empty>No requests yet.</Empty> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>When</th><th>Upgrade</th><th>Tokens in / out</th><th>Cost</th><th>Result</th></tr></thead>
              <tbody>{s.requests.map((r) => (
                <tr key={r.id}>
                  <td title={r.at}>{ago(r.at)}</td>
                  <td>{r.result === "resumed" ? <span className="subtle">resumed by an admin</span> : <><b>{r.component}</b> <span className="mono">{r.from} → {r.to}</span><div className="subtle">{r.model}</div></>}</td>
                  <td className="mono">{r.inputTokens.toLocaleString()} / {r.outputTokens.toLocaleString()}</td>
                  <td className="mono">{usd(r.costMicros)}</td>
                  <td>{r.result === "ok" ? <Tag tone="ok">ok</Tag> : r.result === "failed" ? <Tag tone="warn">failed</Tag> : <Tag>resumed</Tag>}
                    {r.error && <div className="subtle" style={{ wordBreak: "break-word" }}>{r.error}</div>}</td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        )}
      </Card>
    </>
  );
}

function PausedNotice({ reason, onResumed }: { reason: string; onResumed: () => void }) {
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  return (
    <div className="callout warn"><Icon.warn />
      <span className="stack" style={{ gap: 6 }}>
        <span><b>Paused:</b> {reason}. No requests are sent until you resume.</span>
        <span className="row">
          <button className="btn small" disabled={busy} onClick={async () => {
            setBusy(true);
            try { await api.resumeSummaries(); onResumed(); } catch (e) { setMsg({ ok: false, text: writeErrorText(e) }); } finally { setBusy(false); }
          }}>Resume</button>
          <Msg msg={msg} />
        </span>
      </span>
    </div>
  );
}

// What each offered model is good for here (summarizing given release notes, not open-ended reasoning).
const MODEL_NOTES: Record<string, string> = {
  "claude-haiku-5-5": "cheapest; fine for short, plain release notes",
  "claude-sonnet-5-5": "good summaries at a moderate price",
  "claude-opus-5-5": "most thorough; for long or tricky upgrades",
};

/** "claude-sonnet-5" → "Claude Sonnet 5" (the server's label when it sent one). */
function label(s: SummaryStatus, id: string): string {
  const known = s.models.find((m) => m.id === id)?.label;
  if (known) return known;
  const [name, ...ver] = id.replace(/^claude-/, "").split("-");
  return name ? `Claude ${name.charAt(0).toUpperCase()}${name.slice(1)} ${ver.join(".")}`.trim() : id;
}

/** $ per million tokens: "$2", "$0.10". */
const perM = (n: number) => `$${n < 1 ? n.toFixed(2) : n}`;

// Money is edited in dollars and stored in micro-dollars; tokens as numbers.
function SettingsForm({ s, onSaved }: { s: SummaryStatus; onSaved: () => void }) {
  const [v, setV] = useState<SummarySettings>(s.settings);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const dirty = JSON.stringify(v) !== JSON.stringify(s.settings);
  const dollars = (m: number) => String(m / 1e6);
  const micros = (d: string) => Math.round(Number(d) * 1e6);
  return (
    <Card title="Model, mode & limits">
      <form className="stack" onSubmit={async (e) => {
        e.preventDefault(); setBusy(true); setMsg(null);
        try { await api.saveSummarySettings(v); setMsg({ ok: true, text: "Saved." }); onSaved(); }
        catch (err) { setMsg({ ok: false, text: writeErrorText(err) }); }
        finally { setBusy(false); }
      }}>
        <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="subtle" style={{ marginBottom: 4 }}>Model</legend>
          {s.models.map((o) => {
            const chosen = (v.model || s.defaultModel) === o.id;
            return (
              <label key={o.id} className="check-row">
                <input type="radio" name="model" checked={chosen} onChange={() => setV({ ...v, model: o.id === s.defaultModel ? undefined : o.id })} />
                <span>
                  <b>{o.label}</b>{o.id === s.defaultModel && <span className="subtle"> · default</span>}{" "}
                  <span className="subtle">
                    {perM(o.price.Input)} in / {perM(o.price.Output)} out per million tokens · at most {usd(o.maxPerSummaryMicros)} per summary with the saved limits
                    {o.fallback ? ` (incl. a refusal fallback to ${label(s, o.fallback)})` : " · no refusal fallback: a refused request fails and is retried the next day"}
                    {MODEL_NOTES[o.id] ? ` — ${MODEL_NOTES[o.id]}` : ""}
                  </span>
                </span>
              </label>
            );
          })}
        </fieldset>
        <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="subtle" style={{ marginBottom: 4 }}>Mode</legend>
          {MODES.map(([id, label, desc]) => (
            <label key={id} className="check-row">
              <input type="radio" name="mode" checked={v.mode === id} onChange={() => setV({ ...v, mode: id })}
                disabled={(id === "manual" || id === "auto") && !s.enabled} />
              <span><b>{label}</b> <span className="subtle">{desc}</span></span>
            </label>
          ))}
        </fieldset>
        <div className="kv-grid">
          <label className="field">Daily cap (USD)
            <input className="input" type="number" min={0.01} max={1000} step={0.01} value={dollars(v.dailyCapMicros)} onChange={(e) => setV({ ...v, dailyCapMicros: micros(e.target.value) })} />
          </label>
          <label className="field">Monthly cap (USD)
            <input className="input" type="number" min={0.01} max={1000} step={0.01} value={dollars(v.monthlyCapMicros)} onChange={(e) => setV({ ...v, monthlyCapMicros: micros(e.target.value) })} />
          </label>
          <label className="field">Max input tokens per summary
            <input className="input" type="number" min={2000} max={200000} step={1000} value={v.maxInputTokens} onChange={(e) => setV({ ...v, maxInputTokens: Number(e.target.value) })} />
          </label>
          <label className="field">Max output tokens per summary
            <input className="input" type="number" min={1000} max={16000} step={500} value={v.maxOutputTokens} onChange={(e) => setV({ ...v, maxOutputTokens: Number(e.target.value) })} />
          </label>
        </div>
        {s.price && <p className="subtle" style={{ margin: 0 }}>With the saved limits one summary costs at most {usd(s.maxPerSummaryMicros)}{dirty ? " (save to recalculate)" : ""}. Bigger upgrades are only written when an admin clicks “Generate anyway”.</p>}
        <div className="row"><button className="btn primary" disabled={!dirty || busy}>Save</button><Msg msg={msg} /></div>
      </form>
    </Card>
  );
}
