import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { Navigate, NavLink, Route, Routes, useLocation, useNavigate } from "react-router-dom";
import { api, session, useMe, type Cluster, type StageInfo } from "./api";
import { Icon } from "./icons";
import { orderClusters } from "./model";
import { ClusterPicker } from "./components/ClusterPicker";
import { useApi } from "./ui";
import Overview from "./pages/Overview";
import ComponentPage from "./pages/Component";
import Changes from "./pages/Changes";
import Clusters from "./pages/Clusters";
import ClusterDetail from "./pages/ClusterDetail";
import Settings from "./pages/Settings";
import VulnsPage, { VulnImagePage } from "./pages/Vulns";
import Docs, { DocHelp, docsHome } from "./pages/Docs";
import { DOC_HELP } from "./dochelp";
import { AcceptPage, LoginPage, takeReturn } from "./pages/Login";
import { AccountTab, signOut } from "./pages/SettingsUsers";
import { ErrorBox, Loading } from "./ui";

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

/** Signs the user in before anything else loads. Invite/reset links work signed in or not. */
export default function App() {
  const me = useMe();
  const { pathname } = useLocation();
  const [loadError, setLoadError] = useState<unknown>(null);
  const navigate = useNavigate();
  useEffect(() => { if (me === undefined) session.load().catch(setLoadError); }, [me, setLoadError]);
  // Back to the page (e.g. a shared docs section) a Google sign-in started from.
  useEffect(() => {
    if (!me) return;
    const to = takeReturn();
    if (to) navigate(to, { replace: true });
  }, [me, navigate]);
  if (pathname === "/invite" || pathname === "/reset") return <AcceptPage />;
  if (loadError) return <main className="content"><ErrorBox error={loadError} retry={() => { setLoadError(null); void session.load().catch(setLoadError); }} /></main>;
  if (me === undefined) return <Loading />;
  if (me === null) return <LoginPage />;
  if (pathname === "/login") return <Navigate to="/" replace />;
  return <Shell />;
}

function Shell() {
  const me = useMe()!;
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
          <NavLink to="/" className="brand"><span className="brand-mark"><Icon.logo /></span>Owl</NavLink>
          <NavItem to="/" end icon={<Icon.overview />}>Overview</NavItem>
          <NavItem to="/clusters" icon={<Icon.clusters />}>Clusters</NavItem>
          <NavItem to="/changes" icon={<Icon.changes />}>Changes</NavItem>
          <NavItem to="/vulns" icon={<Icon.shield />}>Vulnerabilities</NavItem>
          <NavItem to="/settings" icon={<Icon.settings />}>Settings</NavItem>
          <NavLink to={docsHome()} className={`nav${pathname.startsWith("/docs") ? " active" : ""}`}><Icon.docs />Docs</NavLink>
          <div className="side-foot">
            <NavLink to="/account" className="side-me" title={`${me.email} — your account`}>
              <span className="avatar" aria-hidden="true">{me.email.charAt(0).toUpperCase()}</span>
              <span className="side-me-text">
                <span className="side-user">{me.email}</span>
                <span className="side-role">{me.role === "admin" ? "Admin" : "Viewer"} · Account</span>
              </span>
            </NavLink>
            <button className="side-out" onClick={() => void signOut()} title="Sign out" aria-label="Sign out"><Icon.signout /></button>
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
              <Route path="/vulns" element={<VulnsPage />} />
              <Route path="/vulns/:digest" element={<VulnImagePage />} />
              <Route path="/settings" element={<Navigate to="/settings/clusters" replace />} />
              <Route path="/settings/:tab" element={<Settings />} />
              <Route path="/account" element={<AccountPage />} />
              <Route path="/docs" element={<Docs />} />
              <Route path="/docs/:page" element={<Docs />} />
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

/** Your own account (password, digest email, sign out): under your name in the sidebar, not in Settings. */
function AccountPage() {
  return (
    <>
      <div className="page-head"><div><h1>Account<DocHelp to={DOC_HELP.users} label="users and sign-in" /></h1><p className="muted">Your sign-in and what Owl sends you.</p></div></div>
      <AccountTab />
    </>
  );
}
