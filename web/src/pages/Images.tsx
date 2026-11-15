import { api, type Instance } from "../api";
import { Card, Empty, ErrorBox, Loading, useApi } from "../ui";

export default function Images() {
  const inv = useApi(api.inventory);
  if (inv.error) return <ErrorBox error={inv.error} retry={inv.reload} />;
  if (!inv.data) return <Loading />;
  const { unpinned, unrecognized, inHouse } = inv.data;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Images</h1>
          <p className="muted">Images that can’t be scored precisely: unpinned tags, images not in the catalog, and in-house services.</p>
        </div>
      </div>

      <Card title={`Unpinned (${unpinned.length})`}>
        <p className="subtle">Tags like <code>:latest</code>, digest-only or floating (<code>postgres:16</code>) don’t say which version runs. Pin an exact version.</p>
        {unpinned.length === 0 ? <Empty>None — every known component is pinned.</Empty> : <Where items={unpinned} showReason />}
      </Card>

      <Card title={`Unrecognized (${unrecognized.length})`}>
        <p className="subtle">Not in the catalog, so there’s no upstream check. Add them to <code>internal/catalog/catalog.yaml</code> if they’re third-party.</p>
        {unrecognized.length === 0 ? <Empty>Everything is recognized.</Empty> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Repository</th><th>Where</th></tr></thead>
              <tbody>
                {unrecognized.map((u) => (
                  <tr key={u.repository}>
                    <td className="mono">{u.repository}</td>
                    <td>{u.instances.map((i, n) => <div key={n}>{i.cluster} · {i.namespace}/{i.workload} <span className="subtle mono">{i.image.split(":").pop()}</span></div>)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      <Card title={`In-house (${inHouse.length})`}>
        <p className="subtle">Images from internal registries: listed for inventory, not checked against upstream.</p>
        {inHouse.length === 0 ? <Empty>No in-house images (configure <code>--internal-registries</code>).</Empty> : <Where items={inHouse} />}
      </Card>
    </>
  );
}

function Where({ items, showReason = false }: { items: Instance[]; showReason?: boolean }) {
  return (
    <div className="table-wrap">
      <table>
        <thead><tr><th>Cluster</th><th>Workload</th><th>Image</th>{showReason && <th>Why</th>}</tr></thead>
        <tbody>
          {items.map((i, n) => (
            <tr key={n}>
              <td>{i.cluster}<div className="subtle">{i.stage}</div></td>
              <td>{i.namespace}/{i.workload}<div className="subtle">{i.kind}</div></td>
              <td className="mono">{i.image}</td>
              {showReason && <td>{i.reason}</td>}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
