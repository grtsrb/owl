import { NavLink, Route, Routes } from "react-router-dom";
import Overview from "./pages/Overview";
import ComponentPage from "./pages/Component";
import Changes from "./pages/Changes";
import Images from "./pages/Images";
import Settings from "./pages/Settings";

export default function App() {
  return (
    <div className="app">
      <header className="topbar">
        <NavLink to="/" className="brand">
          <span className="brand-mark" aria-hidden>◆</span> Service Inventory
        </NavLink>
        <nav>
          <NavLink to="/" end>Overview</NavLink>
          <NavLink to="/changes">Changes</NavLink>
          <NavLink to="/images">Images</NavLink>
          <NavLink to="/settings">Settings</NavLink>
        </nav>
      </header>
      <main className="content">
        <Routes>
          <Route path="/" element={<Overview />} />
          <Route path="/components/:id" element={<ComponentPage />} />
          <Route path="/changes" element={<Changes />} />
          <Route path="/images" element={<Images />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<p className="empty">Page not found.</p>} />
        </Routes>
      </main>
    </div>
  );
}
