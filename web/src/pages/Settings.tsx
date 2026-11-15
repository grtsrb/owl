import { useState } from "react";
import { api, adminToken, ApiError } from "../api";
import { ago, Card, ErrorBox, Loading, Tag, useApi } from "../ui";

export default function Settings() {
  const clusters = useApi(api.clusters);
  const policies = useApi(api.policies);
  return (
    <>
      <div className="page-head"><div><h1>Settings</h1></div></div>
      <div className="grid-2">
        <TokenCard />
        <Card title="Global version policy">
          {policies.error ? <ErrorBox error={policies.error} /> : !policies.data ? <Loading /> : (
            <GlobalPolicy current={policies.data.global} presets={policies.data.presets} overrides={policies.data.overrides} onSaved={policies.reload} />
          )}
        </Card>
      </div>
      <Card title="Clusters">
        {clusters.error ? <ErrorBox error={clusters.error} /> : !clusters.data ? <Loading /> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Cluster</th><th>Stage</th><th>Tenant</th><th>Kubernetes</th><th>Agent</th><th>Last report</th></tr></thead>
              <tbody>
                {clusters.data.map((c) => {
                  const stale = !c.lastSeenAt || Date.now() - new Date(c.lastSeenAt).getTime() > 45 * 60 * 1000;
                  return (
                    <tr key={c.name}>
                      <td>{c.name}</td><td>{c.stage}</td><td>{c.tenant}</td>
                      <td className="mono">{c.kubernetesVersion || "—"}</td><td className="mono">{c.agentVersion || "—"}</td>
                      <td>{ago(c.lastSeenAt)} {stale && <Tag tone="warn" title="No report for 45+ minutes (agents push every 15)">stale</Tag>}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <p className="subtle">Register clusters on the server host: <code>inv cluster add --name … --stage … --tenant …</code></p>
      </Card>
    </>
  );
}

function TokenCard() {
  const [value, setValue] = useState("");
  const [has, setHas] = useState(adminToken.get() !== "");
  return (
    <Card title="Admin token">
      <p className="subtle">Needed to change policies. Kept only in this browser tab (session storage) and sent only with write requests.</p>
      {has ? (
        <div className="row"><Tag tone="ok">token set for this tab</Tag><button className="btn" onClick={() => { adminToken.set(""); setHas(false); }}>Forget</button></div>
      ) : (
        <form className="row" onSubmit={(e) => { e.preventDefault(); adminToken.set(value.trim()); setValue(""); setHas(true); }}>
          <input className="input mono" type="password" autoComplete="off" placeholder="INV_ADMIN_TOKEN" value={value} onChange={(e) => setValue(e.target.value)} aria-label="Admin token" />
          <button className="btn primary" disabled={!value.trim()}>Use</button>
        </form>
      )}
    </Card>
  );
}

function GlobalPolicy({ current, presets, overrides, onSaved }: { current: string; presets: string[]; overrides: Record<string, string>; onSaved: () => void }) {
  const [value, setValue] = useState(current);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  return (
    <form onSubmit={async (e) => {
      e.preventDefault(); setMsg(null);
      try { await api.setPolicy("global", value); setMsg({ ok: true, text: "Saved." }); onSaved(); }
      catch (err) { setMsg({ ok: false, text: err instanceof ApiError && err.status === 401 ? "Admin token missing or wrong." : String(err instanceof Error ? err.message : err) }); }
    }}>
      <div className="row">
        <input className="input mono" list="gpresets" value={value} onChange={(e) => setValue(e.target.value)} aria-label="Global policy" />
        <datalist id="gpresets">{presets.filter((p) => !p.includes("<")).map((p) => <option key={p} value={p} />)}</datalist>
        <button className="btn primary" disabled={!value || value === current}>Save</button>
      </div>
      <p className="subtle"><code>lts-or-n-1</code> (default): newest LTS line if the product has one, otherwise the previous release line.</p>
      {Object.keys(overrides).length > 0 && (
        <p className="subtle">Overrides: {Object.entries(overrides).map(([k, v]) => <Tag key={k}>{k}: {v}</Tag>)}</p>
      )}
      {msg && <p className={msg.ok ? "ok-text" : "err-text"}>{msg.text}</p>}
    </form>
  );
}
