import { useEffect, useState } from "react";
import { api, writeErrorText, type RegistryLogin, type ScopeSuggestion } from "../api";
import { formValue, Loading } from "../ui";
import { Dialog } from "./Settings";

// "Add login" where a scan couldn't log in (milestone 15.2, DESIGN.md "Add a login where the scan fails"):
// pick what the login is used for from the image's own path, then an existing login or a new one. Admin only
// (the server refuses everyone else); the password is typed once into an uncontrolled input (see formValue).

/** Name, username, token and expiry of a new or replaced login. The token input stays uncontrolled. */
export function LoginFields({ name, setName, username, setUsername, expires, setExpires, onPw, showName = true }: {
  name?: string; setName?: (v: string) => void; username: string; setUsername: (v: string) => void;
  expires: string; setExpires: (v: string) => void; onPw: (has: boolean) => void; showName?: boolean;
}) {
  return (
    <>
      {showName && setName && <label className="field">Name (optional)
        <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="devops group deploy token" maxLength={100} />
        <span className="subtle" style={{ fontWeight: 400 }}>How you'll find it when another group needs the same login. Default: username @ registry.</span>
      </label>}
      <label className="field">Username
        <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} placeholder="gitlab+deploy-token-12" autoComplete="off" required />
      </label>
      <label className="field">Password / token
        <input className="input" type="password" name="password" autoComplete="new-password" required onInput={(e) => onPw(e.currentTarget.value !== "")} />
        <span className="subtle" style={{ fontWeight: 400 }}>Stored encrypted; never shown again. Read-only is enough: a GitLab group deploy token with read_registry covers the group and its subgroups.</span>
      </label>
      <label className="field">Expires (optional)
        <input className="input" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
        <span className="subtle" style={{ fontWeight: 400 }}>The token's expiry date: Owl warns 14 days before, on the Registries page and in the weekly digest.</span>
      </label>
    </>
  );
}

export function AddLoginDialog({ imageRef, onClose, onDone }: { imageRef: string; onClose: () => void; onDone: (text: string, ok: boolean) => void }) {
  const [sugg, setSugg] = useState<ScopeSuggestion | null>(null);
  const [logins, setLogins] = useState<RegistryLogin[] | null>(null);
  const [canEdit, setCanEdit] = useState(true);
  const [loadErr, setLoadErr] = useState("");
  const [prefix, setPrefix] = useState("");
  const [use, setUse] = useState<"existing" | "new">("new");
  const [loginId, setLoginId] = useState(0);
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
  const [hasPw, setHasPw] = useState(false);
  const [expires, setExpires] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    Promise.all([api.suggestRegistryScopes(imageRef), api.registries()]).then(([s, r]) => {
      setSugg(s);
      const usable = r.logins.filter((l) => l.source === "ui" && !l.problem);
      setLogins(usable);
      setCanEdit(r.canEdit);
      const free = s.options.filter((o) => !o.usedBy);
      setPrefix((free.find((o) => o.suggested) ?? free[free.length - 1])?.prefix ?? "");
      // "Add a new one" stays the default: a GitLab group deploy token reads only its own group, so a new
      // group usually needs a new token. Reusing is for tokens that read several groups.
      if (usable.length > 0) setLoginId(usable[0].id!);
    }).catch((e) => setLoadErr(writeErrorText(e)));
  }, [imageRef]);

  const title = "Add a registry login";
  if (loadErr) return <Dialog title={title} onClose={onClose}><p className="err-text">{loadErr}</p></Dialog>;
  if (!sugg || !logins) return <Dialog title={title} onClose={onClose}><Loading /></Dialog>;
  if (!canEdit) return (
    <Dialog title={title} onClose={onClose}>
      <p className="err-text">OWL_SECRET_KEY isn't set on this server, so logins can't be added in the UI. Generate one with{" "}
        <span className="mono">openssl rand -base64 32</span>, add it to the server's environment and restart.</p>
    </Dialog>
  );
  const ready = prefix !== "" && (use === "existing" ? loginId > 0 : username.trim() !== "" && hasPw);

  return (
    <Dialog title={title} onClose={busy ? undefined : onClose}>
      <p className="subtle" style={{ marginTop: 0 }}>
        Trivy couldn't pull <span className="mono" style={{ wordBreak: "break-all" }}>{sugg.repository}</span>
        {sugg.current ? <> with the login <b>{sugg.current}</b> (rejected — replace it in Settings → Registries, or add a narrower one here).</> : " — no login covers it."}
      </p>
      <form className="stack" autoComplete="off" onSubmit={async (e) => {
        e.preventDefault();
        const form = e.currentTarget;
        setBusy(true); setErr("");
        try {
          let label = "", undo: () => Promise<unknown>;
          if (use === "existing") {
            await api.addRegistryScope(loginId, prefix);
            label = logins.find((l) => l.id === loginId)?.name ?? "";
            undo = () => api.deleteRegistryScope(prefix);
          } else {
            const { id } = await api.addRegistry({ name, prefix, username, password: formValue(form, "password"), expiresAt: expires });
            label = name.trim() || `${username.trim()} @ ${prefix.split("/")[0]}`;
            undo = () => api.deleteRegistry(id);
          }
          const t = await api.testRegistry(prefix).catch(() => null);
          if (t?.outcome === "rejected") {
            // A definite no (wrong token, or no read access to this group): don't keep it — fix it right here.
            await undo().catch(() => undefined);
            setErr(`Not saved — ${t.message}`);
            return;
          }
          form.reset();
          const tail = t ? (t.outcome === "ok" ? " Test: login accepted. Images that failed are being scanned again." : ` Saved, but the test failed: ${t.message}`) : "";
          onDone(`${label} is now used for ${prefix}.${tail}`, !t || t.outcome === "ok");
        } catch (x) {
          setErr(writeErrorText(x));
        } finally { setBusy(false); }
      }}>
        <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="subtle" style={{ marginBottom: 4 }}>Use it for</legend>
          {sugg.options.map((o) => (
            <label key={o.prefix} className="check-row">
              <input type="radio" name="prefix" checked={prefix === o.prefix} disabled={!!o.usedBy} onChange={() => setPrefix(o.prefix)} />
              <span>
                <span className="mono" style={{ wordBreak: "break-all" }}>{o.prefix}</span>{" "}
                <span className="subtle">
                  {o.prefix.includes("/") ? "every image below it" : "the whole registry"}
                  {o.failing > 0 && ` · fixes ${o.failing} ${o.failing === 1 ? "image" : "images"} that failed`}
                  {o.suggested && " · suggested (a GitLab group)"}
                  {o.usedBy && ` · already used by ${o.usedBy}${o.fromFile ? " (OWL_REGISTRY_AUTH_FILE)" : ""}`}
                </span>
              </span>
            </label>
          ))}
          <span className="subtle">The longest matching prefix wins, so a narrower login elsewhere keeps working.</span>
        </fieldset>

        <fieldset className="stack" style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="subtle" style={{ marginBottom: 4 }}>Login</legend>
          <label className="check-row">
            <input type="radio" name="use" checked={use === "existing"} disabled={logins.length === 0} onChange={() => setUse("existing")} />
            <span><b>Use an existing login</b> <span className="subtle">{logins.length === 0 ? "none yet" : "e.g. a token that can read several groups"}</span></span>
          </label>
          {use === "existing" && logins.length > 0 && (
            <select className="input" value={loginId} onChange={(e) => setLoginId(Number(e.target.value))} aria-label="Login">
              {logins.map((l) => <option key={l.id} value={l.id}>{l.name} — {l.username} · used for {l.scopes.map((s) => s.prefix).join(", ")}</option>)}
            </select>
          )}
          <label className="check-row">
            <input type="radio" name="use" checked={use === "new"} onChange={() => setUse("new")} />
            <span><b>Add a new one</b></span>
          </label>
        </fieldset>
        {use === "new" && <LoginFields name={name} setName={setName} username={username} setUsername={setUsername}
          expires={expires} setExpires={setExpires} onPw={setHasPw} />}

        {err && <p className="err-text">{err}</p>}
        <div className="row">
          <button className="btn primary" disabled={busy || !ready}>{busy ? "Saving…" : "Save and test"}</button>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
        </div>
      </form>
    </Dialog>
  );
}
