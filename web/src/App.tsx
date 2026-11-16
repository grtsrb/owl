import { createContext, useContext, useMemo, type ReactNode } from "react";
import { Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import { adminToken, api, type Cluster, type StageInfo } from "./api";
import { Icon } from "./icons";
import { orderClusters, useClusterSelection } from "./model";
import { useApi } from "./ui";
import Overview from "./pages/Overview";
import ComponentPage from "./pages/Component";
import Changes from "./pages/Changes";
import Clusters from "./pages/Clusters";
import ClusterDetail from "./pages/ClusterDetail";
import Settings from "./pages/Settings";

interface AppData {
  clusters: Cluster[] | null; // ordered by stage order
  stages: StageInfo[] | null;
  stageNames: string[];
  error: unknown;
  reload: () => void;
}
const DataCtx = createContext<AppData>({ clusters: null, stages: null, stageNames: [], error: null, reload: () => {} });
export const useAppData = () => useContext(DataCtx);
/** Clusters that have reported at least once (a just-added cluster has no data to show yet). */
export function useReporting(): Cluster[] {
  const { clusters } = useAppData();
  return useMemo(() => (clusters ?? []).filter((c) => c.lastSeenAt), [clusters]);
}

export default function App() {
  const clusters = useApi(api.clusters);
  const stages = useApi(api.stages);
  const data = useMemo<AppData>(() => {
    const stageNames = (stages.data ?? []).map((s) => s.name);
    return {
      clusters: clusters.data ? orderClusters(clusters.data, stageNames) : null,
      stages: stages.data,
      stageNames,
      error: clusters.error ?? stages.error,
      reload: () => { clusters.reload(); stages.reload(); },
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [clusters.data, clusters.error, stages.data, stages.error, clusters.reload, stages.reload]);
  const { pathname } = useLocation();
  const showPicker = pathname === "/" || pathname === "/changes";
  const reporting = useMemo(() => (data.clusters ?? []).filter((c) => c.lastSeenAt), [data.clusters]);

  return (
    <DataCtx.Provider value={data}>
      <div className="shell">
        <nav className="side" aria-label="Main">
          <NavLink to="/" className="brand"><span className="brand-mark"><Icon.logo /></span>Service Inventory</NavLink>
          <NavItem to="/" end icon={<Icon.overview />}>Overview</NavItem>
          <NavItem to="/clusters" icon={<Icon.clusters />}>Clusters</NavItem>
          <NavItem to="/changes" icon={<Icon.changes />}>Changes</NavItem>
          <NavItem to="/settings" icon={<Icon.settings />}>Settings</NavItem>
          <div className="side-foot">
            <span>Admin token: {adminToken.get() ? "set for this tab" : "not set"}</span>
          </div>
        </nav>
        <div className="main">
          {showPicker && reporting.length > 0 && <ClusterPicker clusters={reporting} />}
          <main className="content">
            <Routes>
              <Route path="/" element={<Overview />} />
              <Route path="/components/:id" element={<ComponentPage />} />
              <Route path="/clusters" element={<Clusters />} />
              <Route path="/clusters/:name" element={<ClusterDetail />} />
              <Route path="/changes" element={<Changes />} />
              <Route path="/settings" element={<Navigate to="/settings/clusters" replace />} />
              <Route path="/settings/:tab" element={<Settings />} />
              <Route path="/images" element={<Navigate to="/clusters" replace />} />
              <Route path="*" element={<p className="empty">Page not found.</p>} />
            </Routes>
          </main>
        </div>
      </div>
    </DataCtx.Provider>
  );
}

function NavItem({ to, end, icon, children }: { to: string; end?: boolean; icon: ReactNode; children: ReactNode }) {
  return <NavLink to={to} end={end} className={({ isActive }) => `nav${isActive ? " active" : ""}`}>{icon}{children}</NavLink>;
}

/** Multi-select of clusters, with presets per tenant and per stage. Shared by Overview and Changes. */
function ClusterPicker({ clusters }: { clusters: Cluster[] }) {
  const sel = useClusterSelection(clusters);
  const tenants = [...new Set(clusters.map((c) => c.tenant))].sort();
  const stages = [...new Set(clusters.map((c) => c.stage))];
  return (
    <div className="topbar" role="group" aria-label="Clusters shown">
      <span className="topbar-l">Clusters</span>
      {clusters.map((c) => {
        const on = sel.selected.includes(c.name);
        return (
          <button key={c.name} className={`chip${on ? " on" : ""}`} aria-pressed={on} onClick={() => sel.toggle(c.name)}>
            <span className="st">{c.stage}</span><span>{c.name}</span>
          </button>
        );
      })}
      <span className="sep" aria-hidden />
      <button className="link-btn" onClick={() => sel.set([])}>All</button>
      {tenants.length > 1 && tenants.map((t) => (
        <button key={t} className="link-btn" onClick={() => sel.set(clusters.filter((c) => c.tenant === t).map((c) => c.name))}>Tenant {t}</button>
      ))}
      {stages.length > 1 && stages.map((s) => (
        <button key={s} className="link-btn" onClick={() => sel.set(clusters.filter((c) => c.stage === s).map((c) => c.name))}>Stage {s}</button>
      ))}
      <span className="topbar-r">{sel.selected.length} of {clusters.length} clusters</span>
    </div>
  );
}
