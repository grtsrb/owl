import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, useIsAdmin, writeErrorText, type CatalogComponent, type CustomImpact, type CustomInput, type CustomView, type Preview, type Suggestion } from "../api";
import { Icon } from "../icons";
import { Card, Empty, ErrorBox, Loading, Msg, Tag, useApi } from "../ui";
import { Dialog, useWrite } from "./Settings";

/** Settings → Catalog: components added in the UI (milestone 11) and the built-in list. Overrides follow below (Settings). */
export default function CatalogComponents() {
  const custom = useApi(api.customComponents);
  const admin = useIsAdmin();
  const [qs, setQs] = useSearchParams();
  const [dlg, setDlg] = useState<{ kind: "new"; image?: string } | { kind: "edit"; c: CatalogComponent } | { kind: "delete"; c: CatalogComponent } | null>(null);
  const [copied, setCopied] = useState<string | null>(null);

  // ?new=<repository> (from a cluster's Unrecognized images card or a Mattermost alert) opens the form once.
  const fromLink = qs.get("new");
  useEffect(() => {
    if (fromLink && admin) {
      setDlg({ kind: "new", image: fromLink });
      const next = new URLSearchParams(qs); next.delete("new"); setQs(next, { replace: true });
    }
  }, [fromLink, admin]); // eslint-disable-line react-hooks/exhaustive-deps

  const done = () => { setDlg(null); custom.reload(); };
  return (
    <>
      <Card title="Your components" actions={admin && <button className="btn primary" onClick={() => setDlg({ kind: "new" })}>+ Add component</button>}>
        <p className="subtle" style={{ margin: 0 }}>
          Software Owl's built-in catalog doesn't know — e.g. an SFTP server. Owl then reads its releases, EOL dates and advisories
          like any other component. A built-in component always takes precedence over one added here.
        </p>
        {custom.error ? <ErrorBox error={custom.error} retry={custom.reload} /> : !custom.data ? <Loading /> : custom.data.length === 0 ? (
          <Empty>None yet. Add one here, or with “New component” on a cluster's unrecognized image.</Empty>
        ) : (
          <div className="stack">
            {custom.data.map((v) => <CustomRow key={v.component.id} v={v} admin={admin} copied={copied === v.component.id}
              onEdit={() => setDlg({ kind: "edit", c: v.component })} onDelete={() => setDlg({ kind: "delete", c: v.component })}
              onCopy={async () => { if (await copyText(yamlEntry(v.component))) { setCopied(v.component.id); setTimeout(() => setCopied(null), 2000); } }} />)}
          </div>
        )}
      </Card>
      <BuiltinCard />
      {dlg?.kind === "new" && <ComponentDialog image={dlg.image} onClose={() => setDlg(null)} onSaved={done} />}
      {dlg?.kind === "edit" && <ComponentDialog initial={dlg.c} onClose={() => setDlg(null)} onSaved={done} />}
      {dlg?.kind === "delete" && <DeleteComponentDialog c={dlg.c} onClose={() => setDlg(null)} onDeleted={done} />}
    </>
  );
}

function CustomRow({ v, admin, copied, onEdit, onDelete, onCopy }: {
  v: CustomView; admin: boolean; copied: boolean; onEdit: () => void; onDelete: () => void; onCopy: () => void;
}) {
  const c = v.component;
  return (
    <div className="inst" style={{ gap: 6 }}>
      <div className="row" style={{ justifyContent: "space-between" }}>
        <span className="row" style={{ gap: 8 }}>
          {v.state.active ? <Link to={`/components/${encodeURIComponent(c.id)}`}><b>{c.name}</b></Link> : <b>{c.name}</b>}
          <span className="mono subtle" style={{ margin: 0 }}>{c.id}</span>
          {v.state.active ? <Tag tone="ok">in effect</Tag>
            : v.state.replacedBy ? <Tag tone="warn" title="Owl's built-in catalog now covers this; the built-in one is used">Replaced by built-in {v.state.replacedBy}</Tag>
            : <Tag tone="warn" title={v.state.problem}>Not in effect</Tag>}
        </span>
        <span className="row">
          <button className="btn small" onClick={onCopy} title="A catalog.yaml entry, to add it to Owl's built-in catalog">{copied ? "Copied" : "Copy as YAML"}</button>
          {admin && v.state.active && <button className="btn small" onClick={onEdit}>Edit</button>}
          {admin && <button className="btn small danger" onClick={onDelete}>Delete</button>}
        </span>
      </div>
      <div className="subtle" style={{ margin: 0 }}>
        <span className="mono">{(c.images ?? []).join(", ")}</span> · GitHub <a href={`https://github.com/${c.github}`} target="_blank" rel="noreferrer noopener">{c.github}</a>
        {c.eol && <> · endoflife.date <span className="mono">{c.eol}</span></>}
        {c.versionPattern && <> · version pattern <span className="mono">{c.versionPattern}</span></>}
      </div>
      {v.state.problem && <div className="subtle" style={{ margin: 0 }}>{v.state.problem}</div>}
    </div>
  );
}

function BuiltinCard() {
  const cat = useApi(api.catalog);
  if (cat.error) return <ErrorBox error={cat.error} retry={cat.reload} />;
  const builtin = (cat.data?.components ?? []).filter((c) => !c.custom);
  return (
    <Card title={`Built-in components (${builtin.length})`}>
      <details>
        <summary className="link-btn" style={{ cursor: "pointer" }}>Show the list</summary>
        <div className="table-wrap" style={{ marginTop: 10 }}>
          <table>
            <thead><tr><th>Component</th><th>Images</th><th>Release data</th></tr></thead>
            <tbody>{builtin.map((c) => (
              <tr key={c.id}>
                <td><b>{c.name}</b><div className="subtle mono">{c.id}</div></td>
                <td className="mono small">{c.source === "cluster" ? "from the cluster's nodes" : (c.images ?? []).join(", ")}</td>
                <td className="small">{[c.github && `GitHub ${c.github}`, c.eol && `endoflife.date ${c.eol}`].filter(Boolean).join(" · ")}</td>
              </tr>
            ))}</tbody>
          </table>
        </div>
        <p className="subtle">Built-in components ship with Owl (catalog.yaml) and can't be edited here.</p>
      </details>
    </Card>
  );
}

const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 63);

function toInput(c?: CatalogComponent, image?: string): CustomInput {
  return {
    id: c?.id ?? "", name: c?.name ?? "", images: c?.images ?? (image ? [image] : []), github: c?.github ?? "", eol: c?.eol ?? "",
    versionParts: c?.versionParts ?? 3, tagPrefix: c?.tagPrefix ?? "", releaseTagPrefix: c?.releaseTagPrefix ?? "", versionPattern: c?.versionPattern ?? "",
  };
}

/** New / edit form with suggestions (new only) and a preview before saving. */
function ComponentDialog({ initial, image, onClose, onSaved }: { initial?: CatalogComponent; image?: string; onClose: () => void; onSaved: () => void }) {
  const editing = !!initial;
  const [v, setV] = useState<CustomInput>(toInput(initial, image));
  const [imagesText, setImagesText] = useState(v.images.join("\n"));
  const [idTouched, setIdTouched] = useState(editing);
  const [sug, setSug] = useState<Suggestion | null>(null);
  const [preview, setPreview] = useState<{ key: string; p: Preview } | null>(null);
  const w = useWrite();

  const input: CustomInput = { ...v, images: imagesText.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean) };
  const key = JSON.stringify(input);
  const fresh = preview?.key === key;
  const warnings = fresh ? [...(preview!.p.conflict ? [preview!.p.conflict] : []), ...preview!.p.warnings] : [];

  // Suggestions for the image the form was opened for.
  useEffect(() => {
    if (!image) return;
    let live = true;
    api.suggestComponent(image).then((s) => {
      if (!live) return;
      setSug(s);
      setV((cur) => ({
        ...cur,
        name: cur.name || s.name,
        id: cur.id || s.id,
        github: cur.github || (s.github[0]?.repo ?? ""),
        eol: cur.eol || (s.eol.length === 1 && s.eol[0].slug === s.id ? s.eol[0].slug : ""),
      }));
    }).catch(() => { /* suggestions are optional */ });
    return () => { live = false; };
  }, [image]);

  const set = (patch: Partial<CustomInput>) => setV((cur) => {
    const next = { ...cur, ...patch };
    if (patch.name !== undefined && !idTouched) next.id = slug(patch.name);
    return next;
  });

  async function runPreview() {
    const p = await w.run(() => api.previewComponent(input));
    if (p) setPreview({ key, p });
    return p;
  }
  async function save() {
    let p = fresh ? preview!.p : undefined;
    if (!p) {
      p = await runPreview();
      if (!p) return;
      if (p.warnings.length > 0 || p.conflict) return; // shown; the button now says "Save anyway"
    }
    if (await w.run(() => (editing ? api.updateComponent(input) : api.createComponent(input)))) onSaved();
  }

  return (
    <Dialog title={editing ? `Edit ${initial!.name}` : "New component"} onClose={w.busy ? undefined : onClose}>
      <form className="stack" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        {sug && (sug.github.length > 0 || sug.eol.length > 0 || sug.labelError) && (
          <div className="callout info"><Icon.info />
            <span className="stack" style={{ gap: 4 }}>
              {sug.github.map((g) => (
                <span key={g.repo}>GitHub <button type="button" className="link-btn" onClick={() => set({ github: g.repo })}>{g.repo}</button>
                  <span className="subtle"> — {g.from === "label" ? "from the image's source label" : "a GitHub repository with the same name exists"}</span></span>
              ))}
              {sug.eol.length > 0 && (
                <span>endoflife.date: {sug.eol.map((e, i) => (
                  <span key={e.slug}>{i > 0 && ", "}<button type="button" className="link-btn" onClick={() => set({ eol: e.slug })}>{e.label || e.slug}</button></span>
                ))} <span className="subtle">— only if it's the same project</span></span>
              )}
              {sug.github.length === 0 && <span className="subtle">No source label found{sug.labelError ? ` (${sug.labelError})` : ""}. Enter the GitHub repository yourself.</span>}
              <span className="subtle">Suggestions only — check them before saving.</span>
            </span>
          </div>
        )}
        <div className="form-grid">
          <label className="field">Name
            <input className="input" value={v.name} maxLength={60} onChange={(e) => set({ name: e.target.value })} required />
          </label>
          <label className="field">ID
            <input className="input mono" value={v.id} disabled={editing} maxLength={63}
              onChange={(e) => { setIdTouched(true); setV({ ...v, id: e.target.value.toLowerCase() }); }} required />
            <span className="subtle" style={{ fontWeight: 400 }}>{editing ? "Fixed: policies and links use it." : "Can't be changed later."}</span>
          </label>
        </div>
        <label className="field">Image repositories <span className="subtle" style={{ fontWeight: 400, display: "inline" }}>one per line, without tag</span>
          <textarea className="input mono" rows={2} value={imagesText} onChange={(e) => setImagesText(e.target.value)} required />
        </label>
        <div className="form-grid">
          <label className="field">GitHub repository
            <input className="input mono" placeholder="owner/repo" value={v.github} onChange={(e) => set({ github: e.target.value.trim() })} required />
            <span className="subtle" style={{ fontWeight: 400 }}>Releases and security advisories are read there.</span>
          </label>
          <label className="field">endoflife.date product (optional)
            <input className="input mono" placeholder="e.g. postgresql" value={v.eol} onChange={(e) => set({ eol: e.target.value.trim().toLowerCase() })} />
            <span className="subtle" style={{ fontWeight: 400 }}>Only if it's verified to be the same project.</span>
          </label>
        </div>
        <details>
          <summary className="link-btn" style={{ cursor: "pointer" }}>Advanced: how tags become versions</summary>
          <div className="form-grid" style={{ marginTop: 10 }}>
            <label className="field">Version parts
              <select className="select" value={v.versionParts} onChange={(e) => set({ versionParts: Number(e.target.value) })}>
                <option value={1}>1 (e.g. 17)</option><option value={2}>2 (e.g. 16.4)</option><option value={3}>3 (e.g. 1.2.3)</option>
              </select>
              <span className="subtle" style={{ fontWeight: 400 }}>Tags with fewer parts count as floating (unpinned).</span>
            </label>
            <label className="field">Tag prefix
              <input className="input mono" placeholder="e.g. controller-" value={v.tagPrefix} onChange={(e) => set({ tagPrefix: e.target.value })} />
            </label>
            <label className="field">GitHub release tag prefix
              <input className="input mono" placeholder="only releases starting with…" value={v.releaseTagPrefix} onChange={(e) => set({ releaseTagPrefix: e.target.value })} />
            </label>
            <label className="field">Version pattern
              <input className="input mono" placeholder="^(\d+\.\d+)_p(\d+)" maxLength={200} value={v.versionPattern} onChange={(e) => set({ versionPattern: e.target.value })} />
              <span className="subtle" style={{ fontWeight: 400 }}>For tags Owl can't read: the groups, joined with dots, are the version.</span>
            </label>
          </div>
        </details>

        {fresh && <PreviewBox p={preview!.p} />}
        {warnings.length > 0 && (
          <div className="callout warn"><Icon.warn /><span className="stack" style={{ gap: 4 }}>{warnings.map((x, i) => <span key={i}>{x}</span>)}</span></div>
        )}
        <Msg msg={w.msg} />
        <div className="row">
          <button type="button" className="btn" disabled={w.busy} onClick={() => void runPreview()}>Preview</button>
          <button className="btn primary" disabled={w.busy || !!(fresh && preview!.p.conflict)}>
            {w.busy ? "Working…" : fresh && warnings.length > 0 ? `Save anyway (${warnings.length} ${warnings.length === 1 ? "warning" : "warnings"})` : editing ? "Save" : "Add component"}
          </button>
          <button type="button" className="btn" disabled={w.busy} onClick={onClose}>Cancel</button>
        </div>
      </form>
    </Dialog>
  );
}

function PreviewBox({ p }: { p: Preview }) {
  const u = p.upstream;
  return (
    <div className="stack" style={{ gap: 8 }}>
      <h2>Preview</h2>
      {u && (
        <div className="kv-grid">
          <div><span>Latest stable</span><b>{u.latestStable ?? "—"}</b></div>
          <div><span>Release lines from</span><span>{u.linesSource === "endoflife.date" ? "endoflife.date" : u.linesSource ? "GitHub releases" : "—"}</span></div>
          <div><span>Security advisories</span><span>{u.advisorySource === "github-repo" ? "GitHub advisories" : u.advisorySource === "osv" ? "OSV" : "none published (no advisory data)"}</span></div>
        </div>
      )}
      {u && u.lines.length > 0 && (
        <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
          {u.lines.map((l) => <Tag key={l.name} tone={l.eol ? "warn" : "neutral"}>{l.name} → {l.latest}{l.lts ? " LTS" : ""}{l.eol ? " EOL" : l.eolDate ? ` (EOL ${l.eolDate.slice(0, 10)})` : ""}</Tag>)}
        </div>
      )}
      {p.eolLatest && p.githubLatest && <p className="subtle" style={{ margin: 0 }}>endoflife.date's newest: {p.eolLatest} · GitHub's newest: {p.githubLatest}</p>}
      <span className="subtle" style={{ margin: 0 }}>Running in your clusters</span>
      {p.tags.length === 0 ? <span className="subtle" style={{ margin: 0 }}>—</span> : (
        <div className="stack" style={{ gap: 4 }}>
          {p.tags.map((t) => (
            <div key={t.image} className="row small" style={{ gap: 8 }}>
              <span className="mono" style={{ wordBreak: "break-all" }}>{t.image}</span>→
              {t.status === "identified" ? <Tag tone="ok">{t.version}</Tag> : <Tag tone="warn" title={t.reason}>{t.status === "unpinned" ? "unpinned" : t.status}</Tag>}
              <span className="subtle" style={{ margin: 0 }}>{t.clusters.join(", ")}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function DeleteComponentDialog({ c, onClose, onDeleted }: { c: CatalogComponent; onClose: () => void; onDeleted: () => void }) {
  const [imp, setImp] = useState<CustomImpact | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const w = useWrite();
  useEffect(() => { api.componentImpact(c.id).then(setImp, (e) => setErr(writeErrorText(e))); }, [c.id]);
  return (
    <Dialog title={`Delete ${c.name}?`} onClose={w.busy ? undefined : onClose}>
      {err ? <p className="err-text">{err}</p> : !imp ? <Loading /> : (
        <div className="stack">
          <p style={{ margin: 0 }}>Its images ({(c.images ?? []).join(", ")}) become unrecognized again. Also removed:</p>
          <ul style={{ margin: 0 }}>
            <li>{imp.policy ? "its version policy" : "no version policy (none was set)"}</li>
            {imp.overrides.length === 0 ? <li>no overrides (nothing is mapped to it)</li>
              : imp.overrides.map((o) => <li key={o}>override: <span className="mono">{o}</span> mapped to it</li>)}
          </ul>
          <Msg msg={w.msg} />
          <div className="row">
            <button className="btn danger" disabled={w.busy} onClick={async () => { if (await w.run(() => api.deleteComponent(c.id))) onDeleted(); }}>Delete</button>
            <button className="btn" disabled={w.busy} onClick={onClose}>Cancel</button>
          </div>
        </div>
      )}
    </Dialog>
  );
}

/** A catalog.yaml entry (JSON strings are valid YAML scalars). */
export function yamlEntry(c: CatalogComponent): string {
  const q = (s: string) => JSON.stringify(s);
  const lines = [`  - id: ${c.id}`, `    name: ${q(c.name)}`, `    images: [${(c.images ?? []).map(q).join(", ")}]`];
  if (c.github) lines.push(`    github: ${c.github}`);
  if (c.eol) lines.push(`    eol: ${c.eol}`);
  if (c.versionParts && c.versionParts !== 3) lines.push(`    versionParts: ${c.versionParts}`);
  if (c.tagPrefix) lines.push(`    tagPrefix: ${q(c.tagPrefix)}`);
  if (c.releaseTagPrefix) lines.push(`    releaseTagPrefix: ${q(c.releaseTagPrefix)}`);
  if (c.versionPattern) lines.push(`    versionPattern: ${q(c.versionPattern)}`);
  return lines.join("\n") + "\n";
}

async function copyText(s: string): Promise<boolean> {
  try { await navigator.clipboard.writeText(s); return true; } catch { window.prompt("Copy this catalog.yaml entry:", s); return false; }
}
