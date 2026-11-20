import { DocHelp } from "./Docs";
import { DOC_HELP } from "../dochelp";
import { Link } from "react-router-dom";
import { api } from "../api";
import { useAppData } from "../App";
import { unrecognizedIn, workloadCount, workloadsFor } from "../clusterview";
import { ago, CountBadges, Empty, ErrorBox, LevelBar, Loading, useApi } from "../ui";
import { clusterCounts, k8sStatus } from "./Overview";

export default function Clusters() {
  const { clusters, error } = useAppData();
  const report = useApi(api.report);
  const inv = useApi(api.inventory);

  if (error || report.error || inv.error) return <ErrorBox error={error ?? report.error ?? inv.error} retry={() => { report.reload(); inv.reload(); }} />;
  if (!clusters || !report.data || !inv.data) return <Loading />;

  return (
    <>
      <div className="page-head">
        <div>
          <h1>Clusters<DocHelp to={DOC_HELP.clusters} label="clusters, stages and tenants" /></h1>
          <p className="muted">Everything running in each cluster: every workload, its image and what we know about it.</p>
        </div>
        <Link to="/settings/clusters" className="btn primary">+ Add cluster</Link>
      </div>
      {clusters.length === 0 ? <Empty>No clusters yet. <Link to="/settings/clusters">Add one</Link>.</Empty> : (
        <div className="ccards big">
          {clusters.map((cl) => {
            const counts = clusterCounts(report.data!.components, cl.name);
            const k8s = k8sStatus(report.data!.components, cl.name);
            const wl = workloadCount(workloadsFor(cl.name, report.data!, inv.data!));
            const unrec = unrecognizedIn(cl.name, inv.data!).length;
            return (
              <Link key={cl.name} to={`/clusters/${encodeURIComponent(cl.name)}`} className="cc2">
                <span className="row" style={{ justifyContent: "space-between", alignItems: "flex-start" }}>
                  <span className="stack" style={{ gap: 2 }}>
                    <span className="cc2-name">{cl.name}</span>
                    <span className="subtle" style={{ margin: 0 }}>Tenant {cl.tenant}</span>
                  </span>
                  <span className="tag tone-info">{cl.stage}</span>
                </span>
                {cl.lastSeenAt ? (
                  <>
                    <span className="cc2-k8s">
                      <span className="stack" style={{ gap: 2 }}>
                        <span className="subtle" style={{ margin: 0 }}>Kubernetes</span>
                        <span className="mono">{cl.kubernetesVersion || "—"}</span>
                      </span>
                      {k8s && <span className={`badge lvl-${k8s.level}`}>{k8s.text}</span>}
                    </span>
                    <span className="stack">
                      <LevelBar counts={counts} thin />
                      <CountBadges counts={counts} />
                    </span>
                  </>
                ) : (
                  <span className="callout info">Waiting for the first report from its agent.</span>
                )}
                <span className="cc2-foot">
                  <span className="subtle" style={{ margin: 0 }}>{wl} {wl === 1 ? "workload" : "workloads"} · reported {ago(cl.lastSeenAt)}</span>
                  {unrec > 0 && <span className="tag tone-warn">{unrec} unrecognized {unrec === 1 ? "image" : "images"}</span>}
                </span>
              </Link>
            );
          })}
        </div>
      )}
    </>
  );
}
