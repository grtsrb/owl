import { useState } from "react";
import { api, writeErrorText, type LoginResult, type RegistryCredential } from "../api";
import { ago, Card, date, Empty, ErrorBox, formValue, Loading, Msg, Tag, useApi } from "../ui";
import { Dialog, useWrite } from "./Settings";

// Settings → Registries (milestone 15.1): the logins Trivy pulls in-house images with. Write-only — a
// password is typed once (uncontrolled input, see formValue) and never comes back from the server.

const DAY = 86400e3;

function expiryTag(iso?: string) {
  if (!iso) return null;
  const left = new Date(iso).getTime() - Date.now();
  if (left <= 0) return <Tag tone="critical" title="Scans with this login fail: replace it">expired {date(iso)}</Tag>;
  if (left <= 14 * DAY) return <Tag tone="warn" title="Renew the token in the registry, then replace it here">expires in {Math.max(1, Math.round(left / DAY))} days</Tag>;
  return <span className="subtle" style={{ margin: 0 }}>expires {date(iso)}</span>;
}

function testTag(t?: LoginResult) {
  if (!t) return null;
  return t.outcome === "ok"
    ? <Tag tone="ok" title={`${t.message} · ${ago(t.at)}`}>test OK</Tag>
    : <Tag tone="critical" title={ago(t.at)}>test failed</Tag>;
}

type Mode = { kind: "add" } | { kind: "replace" | "expiry"; cred: RegistryCredential };

export default function RegistriesTab() {
  return <><InHouseCard /><CredentialsCard /></>;
}

// In-house registries: images from them are the team's own. The server's list (env) is read-only here.
function InHouseCard() {
  const list = useApi(api.internalRegistries);
  const w = useWrite();
  const [text, setText] = useState<string | null>(null); // null = not edited
  if (list.error) return <ErrorBox error={list.error} retry={list.reload} />;
  if (!list.data) return <Loading />;
  const { server, ui } = list.data;
  const value = text ?? ui.join("\n");
  return (
    <Card title="In-house registries">
      <p className="subtle" style={{ marginTop: 0 }}>
        Images from these registries are your own: listed as <b>in-house</b> (not compared with upstream releases), shown first on the
        Vulnerabilities page and counted in the digest's in-house line. A registry given as an IP address is scanned only when it's listed
        here. A host covers its subdomains; no paths.
      </p>
      {server.length > 0 && (
        <div className="row" style={{ flexWrap: "wrap", gap: 6, marginBottom: 10 }}>
          <span className="subtle" style={{ margin: 0 }}>From the server's environment (OWL_INTERNAL_REGISTRIES):</span>
          {server.map((r) => <Tag key={r}>{r}</Tag>)}
        </div>
      )}
      <form className="stack" onSubmit={async (e) => {
        e.preventDefault();
        const regs = value.split(/[\n,]/).map((r) => r.trim()).filter(Boolean);
        const r = await w.run(() => api.setInternalRegistries(regs), "Saved. In-house marking applies right away.");
        if (r) { setText(null); list.reload(); }
      }}>
        <label className="field">{server.length > 0 ? "More, one per line" : "One per line"}
          <textarea className="input mono" rows={Math.max(3, value.split("\n").length + 1)} value={value}
            placeholder={"registry.gitlab.example.com\n10.20.0.5:5000"} onChange={(e) => setText(e.target.value)} />
        </label>
        <Msg msg={w.msg} />
        <div><button className="btn primary" disabled={w.busy || text === null}>{w.busy ? "Saving…" : "Save"}</button></div>
      </form>
    </Card>
  );
}

function CredentialsCard() {
  const view = useApi(api.registries);
  const w = useWrite();
  const [mode, setMode] = useState<Mode | null>(null);
  const [testing, setTesting] = useState("");
  if (view.error) return <ErrorBox error={view.error} retry={view.reload} />;
  if (!view.data) return <Loading />;
  const { credentials, canEdit, scanning, scansOff } = view.data;
  const test = async (prefix: string) => {
    setTesting(prefix);
    const r = await w.run(() => api.testRegistry(prefix));
    setTesting("");
    if (r) w.setMsg({ ok: r.outcome === "ok", text: `${prefix}: ${r.message}` });
    view.reload();
  };
  return (
    <>
      <Card title="Registry logins"
        actions={canEdit && <button className="btn primary small" onClick={() => setMode({ kind: "add" })}>Add credential</button>}>
        <p className="subtle" style={{ marginTop: 0 }}>
          The logins Trivy uses to pull images for vulnerability scans, matched by the longest prefix
          (<span className="mono">registry.gitlab.example.com/team-a</span> covers every image below it). Images that match none are
          pulled anonymously. Use a read-only token, e.g. a GitLab group deploy token with only <span className="mono">read_registry</span>.
          Passwords are stored encrypted and never shown again.
        </p>
        {!canEdit && <p className="err-text">OWL_SECRET_KEY isn't set on this server, so credentials can't be added here (only listed).
          Generate one with <span className="mono">openssl rand -base64 32</span>, add it to the server's environment and restart.</p>}
        {!scanning && <p className="subtle">Image scanning is off on this server ({scansOff || "not configured"}): credentials are kept but not used.</p>}
        {credentials.length === 0 ? <Empty>No registry credentials yet. Public images need none.</Empty> : (
          <div className="stack">
            {credentials.map((c) => (
              <div key={c.source + c.prefix} className="list-row">
                <div className="grow" style={{ minWidth: 220 }}>
                  <div className="mono" style={{ wordBreak: "break-all" }}>{c.prefix}</div>
                  <div className="subtle" style={{ margin: 0 }}>
                    {c.username} · {c.source === "file" ? "from OWL_REGISTRY_AUTH_FILE" : <>password set · updated {ago(c.updatedAt)}{c.updatedBy ? ` by ${c.updatedBy}` : ""}</>}
                  </div>
                  {c.problem && <div className="err-text" style={{ margin: "4px 0 0" }}>{c.problem}</div>}
                  {c.lastTest && c.lastTest.outcome !== "ok" && <div className="err-text" style={{ margin: "4px 0 0" }}>Test ({ago(c.lastTest.at)}): {c.lastTest.message}</div>}
                </div>
                {c.source === "file" && <Tag title="Change it in the mounted file; the file wins over the UI">file</Tag>}
                {expiryTag(c.expiresAt)}
                {c.problem && <Tag tone="critical">not used</Tag>}
                {c.authFailedAt && <Tag tone="critical" title="A scan couldn't log in with this credential">scan login failed {ago(c.authFailedAt)}</Tag>}
                {testTag(c.lastTest)}
                <div className="row" style={{ gap: 6 }}>
                  {!c.problem && <button className="btn small" disabled={w.busy} onClick={() => test(c.prefix)}>{testing === c.prefix ? "Testing…" : "Test"}</button>}
                  {c.source === "ui" && canEdit && <>
                    <button className="btn small" disabled={w.busy} onClick={() => setMode({ kind: "replace", cred: c })}>Replace</button>
                    <button className="btn small" disabled={w.busy} onClick={() => setMode({ kind: "expiry", cred: c })}>Expiry</button>
                    <button className="btn small" disabled={w.busy} onClick={() => {
                      if (confirm(`Delete the credential for ${c.prefix}? Images below it are then pulled anonymously.`))
                        w.run(() => api.deleteRegistry(c.prefix), `Deleted: ${c.prefix}.`).then(() => view.reload());
                    }}>Delete</button>
                  </>}
                </div>
              </div>
            ))}
          </div>
        )}
        <Msg msg={w.msg} />
        <p className="subtle">Test logs in from the Owl server, like a pull would: it also shows DNS problems (VPN, ziti, split DNS) and untrusted certificates.
          After a change, images whose last scan couldn't log in are scanned again right away.</p>
      </Card>
      {mode && <CredentialDialog mode={mode} onClose={() => setMode(null)} onDone={(text, prefix) => {
        setMode(null);
        w.setMsg({ ok: true, text });
        view.reload();
        if (prefix) test(prefix);
      }} />}
    </>
  );
}

function CredentialDialog({ mode, onClose, onDone }: { mode: Mode; onClose: () => void; onDone: (msg: string, testPrefix?: string) => void }) {
  const cred = mode.kind === "add" ? undefined : mode.cred;
  const [prefix, setPrefix] = useState(cred?.prefix ?? "");
  const [username, setUsername] = useState(mode.kind === "replace" ? "" : cred?.username ?? "");
  const [hasPw, setHasPw] = useState(false); // the password itself stays in the input
  const [expires, setExpires] = useState(cred?.expiresAt?.slice(0, 10) ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const login = mode.kind !== "expiry";
  const title = mode.kind === "add" ? "Add registry credential" : mode.kind === "replace" ? `Replace the login for ${cred!.prefix}` : `Expiry of ${cred!.prefix}`;
  return (
    <Dialog title={title} onClose={busy ? undefined : onClose}>
      <form className="stack" autoComplete="off" onSubmit={async (e) => {
        e.preventDefault();
        const form = e.currentTarget;
        const password = formValue(form, "password");
        setBusy(true); setErr("");
        try {
          if (mode.kind === "add") await api.addRegistry({ prefix, username, password, expiresAt: expires });
          else if (mode.kind === "replace") await api.updateRegistry({ prefix: cred!.prefix, username, password, expiresAt: expires });
          else await api.updateRegistry({ prefix: cred!.prefix, expiresAt: expires });
          form.reset();
          onDone(mode.kind === "expiry" ? `Expiry saved for ${cred!.prefix}.` : `Saved. Testing the login for ${prefix.trim() || cred!.prefix}…`,
            login ? (mode.kind === "add" ? prefix : cred!.prefix) : undefined);
        } catch (x) {
          setErr(writeErrorText(x));
        } finally { setBusy(false); }
      }}>
        {mode.kind === "add" && <label className="field">Prefix
          <input className="input mono" value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="registry.gitlab.example.com/team-a" required />
          <span className="subtle" style={{ fontWeight: 400 }}>A registry host, or host/path for one group. The longest matching prefix wins.</span>
        </label>}
        {login && <>
          <label className="field">Username
            <input className="input" value={username} onChange={(e) => setUsername(e.target.value)} placeholder="gitlab+deploy-token-12" autoComplete="off" required />
          </label>
          <label className="field">Password / token
            <input className="input" type="password" name="password" autoComplete="new-password" required onInput={(e) => setHasPw(e.currentTarget.value !== "")} />
            <span className="subtle" style={{ fontWeight: 400 }}>Stored encrypted; never shown again. Read-only scope is enough (GitLab: read_registry).</span>
          </label>
        </>}
        <label className="field">Expires (optional)
          <input className="input" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
          <span className="subtle" style={{ fontWeight: 400 }}>The token's expiry date: Owl warns 14 days before, here and in the weekly digest.</span>
        </label>
        {err && <p className="err-text">{err}</p>}
        <div className="row">
          <button className="btn primary" disabled={busy || (login && (!username.trim() || !hasPw)) || (mode.kind === "add" && !prefix.trim())}>{busy ? "Saving…" : "Save"}</button>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
        </div>
      </form>
    </Dialog>
  );
}
