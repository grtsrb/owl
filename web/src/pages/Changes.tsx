import { api, type Change } from "../api";
import { useReporting } from "../App";
import { useClusterSelection } from "../model";
import { ago, Card, Empty, ErrorBox, Loading, Tag, useApi } from "../ui";
import { DocHelp } from "./Docs";
import { DOC_HELP } from "../dochelp";

const typeLabel: Record<string, [string, string]> = {
  image_changed: ["changed", "info"],
  added: ["added", "ok"],
  removed: ["removed", "warn"],
  kubernetes_version_changed: ["kubernetes", "info"],
  first_snapshot: ["first report", "neutral"],
};

export default function Changes() {
  const reporting = useReporting();
  const sel = useClusterSelection(reporting);
  // One cluster: ask the server for just that one; several: fetch recent history and filter here.
  const one = sel.selected.length === 1 ? sel.selected[0] : "";
  const changes = useApi(() => api.changes(one, one ? 300 : 1000), [one]);
  const shown = changes.data?.filter((c) => sel.selected.includes(c.cluster));

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Changes<DocHelp to={DOC_HELP.changes} label="change history" /></h1>
          <p className="muted">Image and Kubernetes version changes reported by the agents (template changes only), for the clusters selected above.</p>
        </div>
      </div>
      <Card>
        {changes.error ? <ErrorBox error={changes.error} retry={changes.reload} /> : !shown ? <Loading /> :
          shown.length === 0 ? <Empty>No changes recorded yet.</Empty> : (
            <div className="table-wrap">
              <table>
                <thead><tr><th>When</th><th>Cluster</th><th>Type</th><th>Workload</th><th>Change</th></tr></thead>
                <tbody>{shown.map((c) => <Row key={c.id} c={c} />)}</tbody>
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
