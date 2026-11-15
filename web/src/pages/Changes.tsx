import { useState } from "react";
import { api, type Change } from "../api";
import { ago, Card, Empty, ErrorBox, Loading, Tag, useApi } from "../ui";

const typeLabel: Record<string, [string, string]> = {
  image_changed: ["changed", "info"],
  added: ["added", "ok"],
  removed: ["removed", "warn"],
  kubernetes_version_changed: ["kubernetes", "info"],
  first_snapshot: ["first report", "neutral"],
};

export default function Changes() {
  const [cluster, setCluster] = useState("");
  const clusters = useApi(api.clusters);
  const changes = useApi(() => api.changes(cluster), [cluster]);

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Changes</h1>
          <p className="muted">Image and Kubernetes version changes reported by the agents (template changes only).</p>
        </div>
        <select className="input" value={cluster} onChange={(e) => setCluster(e.target.value)} aria-label="Cluster">
          <option value="">All clusters</option>
          {clusters.data?.map((c) => <option key={c.name} value={c.name}>{c.name}</option>)}
        </select>
      </div>
      <Card>
        {changes.error ? <ErrorBox error={changes.error} retry={changes.reload} /> : !changes.data ? <Loading /> :
          changes.data.length === 0 ? <Empty>No changes recorded yet.</Empty> : (
            <div className="table-wrap">
              <table>
                <thead><tr><th>When</th><th>Cluster</th><th>Type</th><th>Workload</th><th>Change</th></tr></thead>
                <tbody>{changes.data.map((c) => <Row key={c.id} c={c} />)}</tbody>
              </table>
            </div>
          )}
      </Card>
    </>
  );
}

function Row({ c }: { c: Change }) {
  const [label, tone] = typeLabel[c.type] ?? [c.type, "neutral"];
  return (
    <tr>
      <td title={c.at}>{ago(c.at)}</td>
      <td>{c.cluster}</td>
      <td><Tag tone={tone}>{label}</Tag></td>
      <td>{c.workload ? <>{c.namespace}/{c.workload}<div className="subtle">{c.kind} · {c.container}</div></> : "—"}</td>
      <td className="mono">
        {c.oldValue && <span className="old">{c.oldValue}</span>}
        {c.oldValue && c.newValue && " → "}
        {c.newValue}
      </td>
    </tr>
  );
}
