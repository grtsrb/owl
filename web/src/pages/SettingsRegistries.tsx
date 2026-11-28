import { useState } from "react";
import { api, writeErrorText, type LoginResult, type RegistryLogin } from "../api";
import { ago, Card, date, Empty, ErrorBox, formValue, Loading, Msg, Tag, useApi } from "../ui";
import { Dialog, useWrite } from "./Settings";
import { LoginFields } from "./AddLogin";

// Settings → Registries (milestones 15.1 + 15.2): the logins Trivy pulls in-house images with, each used for
// one or more prefixes. Write-only — a password is typed once (uncontrolled input, see formValue) and never
// comes back from the server.

const DAY = 86400e3;

function expiryTag(iso?: string) {
  if (!iso) return null;
  const left = new Date(iso).getTime() - Date.now();
  if (left <= 0) return <Tag tone="critical" title="Scans with this login fail: replace it">expired {date(iso)}</Tag>;
  if (left <= 14 * DAY) return <Tag tone="warn" title="Renew the token in the registry, then replace it here">expires in {Math.ceil(left / DAY)} {Math.ceil(left / DAY) === 1 ? "day" : "days"}</Tag>;
  return <span className="subtle" style={{ margin: 0 }}>expires {date(iso)}</span>;
}

function testTag(t?: LoginResult) {
  if (!t) return null;
  return t.outcome === "ok"
    ? <Tag tone="ok" title={`${t.message} · ${ago(t.at)}`}>test OK</Tag>
    : <Tag tone="critical" title={ago(t.at)}>test failed</Tag>;
}

type Mode = { kind: "add" } | { kind: "replace" | "edit" | "scope"; login: RegistryLogin };

export default function RegistriesTab() {
  return <><InHouseCard /><LoginsCard /></>;
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

function LoginsCard() {
  const view = useApi(api.registries);
  const w = useWrite();
  const [mode, setMode] = useState<Mode | null>(null);
  const [testing, setTesting] = useState("");
  if (view.error) return <ErrorBox error={view.error} retry={view.reload} />;
  if (!view.data) return <Loading />;
  const { logins, canEdit, scanning, scansOff } = view.data;
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
        actions={canEdit && <button className="btn primary small" onClick={() => setMode({ kind: "add" })}>Add login</button>}>
        <p className="subtle" style={{ marginTop: 0 }}>
          The logins Trivy uses to pull images for vulnerability scans. A login is used for the prefixes listed under it
          (<span className="mono">registry.gitlab.example.com/team-a</span> covers every image below it); the longest matching prefix wins, and
          images that match none are pulled anonymously. The quickest way to add one: <b>Add login</b> on an image whose scan couldn't log in
          (Vulnerabilities page). Use a read-only token, e.g. a GitLab group deploy token with only <span className="mono">read_registry</span>.
          Passwords are stored encrypted and never shown again.
        </p>
        {!canEdit && <p className="err-text">OWL_SECRET_KEY isn't set on this server, so logins can't be added here (only listed).
          Generate one with <span className="mono">openssl rand -base64 32</span>, add it to the server's environment and restart.</p>}
        {!scanning && <p className="subtle">Image scanning is off on this server ({scansOff || "not configured"}): logins are kept but not used.</p>}
        {logins.length === 0 ? <Empty>No registry logins yet. Public images need none.</Empty> : (
          <div className="stack">
            {logins.map((l) => (
              <div key={l.key} className="list-row" style={{ alignItems: "flex-start" }}>
                <div className="grow" style={{ minWidth: 240 }}>
                  <div><b>{l.name}</b></div>
                  <div className="subtle" style={{ margin: 0 }}>
                    {l.username} · {l.source === "file" ? "from OWL_REGISTRY_AUTH_FILE" : <>password set · updated {ago(l.updatedAt)}{l.updatedBy ? ` by ${l.updatedBy}` : ""}</>}
                  </div>
                  {l.problem && <div className="err-text" style={{ margin: "4px 0 0" }}>{l.problem}</div>}
                  <div className="subtle" style={{ margin: "8px 0 2px" }}>Used for</div>
                  <div className="stack" style={{ gap: 4 }}>
                    {l.scopes.map((sc) => (
                      <div key={sc.prefix}>
                      <div className="row" style={{ gap: 6, flexWrap: "wrap" }}>
                        <span className="mono" style={{ wordBreak: "break-all" }}>{sc.prefix}</span>
                        {!sc.problem && !l.problem && <span className="subtle" style={{ margin: 0 }}>{sc.images} running {sc.images === 1 ? "image" : "images"}</span>}
                        {sc.problem && <Tag tone="critical" title={sc.problem}>not used</Tag>}
                        {sc.authFailedAt && <Tag tone="critical" title="A scan couldn't log in with this login">scan login failed {ago(sc.authFailedAt)}</Tag>}
                        {testTag(sc.lastTest)}
                        {!sc.problem && !l.problem && <button className="btn small" disabled={w.busy} onClick={() => test(sc.prefix)}>{testing === sc.prefix ? "Testing…" : "Test"}</button>}
                        {l.source === "ui" && canEdit && l.scopes.length > 1 && <button className="btn small" disabled={w.busy} title="Stop using this login for this prefix" onClick={() => {
                          if (confirm(`Stop using ${l.name} for ${sc.prefix}? Images below it then use another matching login, or none.`))
                            w.run(() => api.deleteRegistryScope(sc.prefix), `${l.name} is no longer used for ${sc.prefix}.`).then(() => view.reload());
                        }}>Remove</button>}
                      </div>
                      {sc.lastTest && sc.lastTest.outcome !== "ok" && <div className="err-text" style={{ margin: "2px 0 0" }}>Test ({ago(sc.lastTest.at)}): {sc.lastTest.message}</div>}
                      </div>
                    ))}
                  </div>
                </div>
                {l.source === "file" && <Tag title="Change it in the mounted file; the file wins over the UI">file</Tag>}
                {expiryTag(l.expiresAt)}
                {l.problem && <Tag tone="critical">not used</Tag>}
                {l.source === "ui" && canEdit && <div className="row" style={{ gap: 6 }}>
                  <button className="btn small" disabled={w.busy} onClick={() => setMode({ kind: "scope", login: l })}>Use for another prefix</button>
                  <button className="btn small" disabled={w.busy} onClick={() => setMode({ kind: "replace", login: l })}>Replace</button>
                  <button className="btn small" disabled={w.busy} onClick={() => setMode({ kind: "edit", login: l })}>Edit</button>
                  <button className="btn small" disabled={w.busy} onClick={() => {
                    const list = l.scopes.map((s) => s.prefix).join(", ");
                    if (confirm(`Delete the login ${l.name}? It's used for: ${list}. Images below them are then pulled with another matching login, or anonymously.`))
                      w.run(() => api.deleteRegistry(l.id!), `Deleted: ${l.name}.`).then(() => view.reload());
                  }}>Delete</button>
                </div>}
              </div>
            ))}
          </div>
        )}
        <Msg msg={w.msg} />
        <p className="subtle">Test logs in from the Owl server, like a pull would: it also shows DNS problems (VPN, ziti, split DNS), a login host
          that isn't reachable (GitLab logs in on its web host, not the registry host) and untrusted certificates.
          After a change, images whose last scan couldn't log in are scanned again right away.</p>
      </Card>
      {mode && <LoginDialog mode={mode} onClose={() => setMode(null)} onDone={(text, prefix) => {
        setMode(null);
        w.setMsg({ ok: true, text });
        view.reload();
        if (prefix) test(prefix);
      }} />}
    </>
  );
}

function LoginDialog({ mode, onClose, onDone }: { mode: Mode; onClose: () => void; onDone: (msg: string, testPrefix?: string) => void }) {
  const login = mode.kind === "add" ? undefined : mode.login;
  const [prefix, setPrefix] = useState("");
  const [name, setName] = useState(mode.kind === "edit" ? login!.name : "");
  const [username, setUsername] = useState("");
  const [hasPw, setHasPw] = useState(false); // the password itself stays in the input
  const [expires, setExpires] = useState(login?.expiresAt?.slice(0, 10) ?? "");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");
  const needPrefix = mode.kind === "add" || mode.kind === "scope";
  const needLogin = mode.kind === "add" || mode.kind === "replace";
  const title = { add: "Add registry login", replace: `Replace the password of ${login?.name}`, edit: `Edit ${login?.name}`, scope: `Use ${login?.name} for another prefix` }[mode.kind];
  return (
    <Dialog title={title} onClose={busy ? undefined : onClose}>
      <form className="stack" autoComplete="off" onSubmit={async (e) => {
        e.preventDefault();
        const form = e.currentTarget;
        const password = formValue(form, "password");
        setBusy(true); setErr("");
        try {
          const p = prefix.trim();
          if (mode.kind === "add") await api.addRegistry({ name, prefix: p, username, password, expiresAt: expires });
          else if (mode.kind === "replace") await api.updateRegistry(login!.id!, { username, password, expiresAt: expires }); // a renewed token: new date
          else if (mode.kind === "edit") await api.updateRegistry(login!.id!, { name, expiresAt: expires });
          else await api.addRegistryScope(login!.id!, p);
          form.reset();
          const testWith = needPrefix ? p : mode.kind === "replace" ? login!.scopes.find((sc) => !sc.problem)?.prefix : undefined;
          onDone(mode.kind === "edit" || !testWith ? `Saved: ${name.trim() || login!.name}.` : `Saved. Testing the login for ${testWith}…`, testWith);
        } catch (x) {
          setErr(writeErrorText(x));
        } finally { setBusy(false); }
      }}>
        {needPrefix && <label className="field">Prefix
          <input className="input mono" value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="registry.gitlab.example.com/team-a" required />
          <span className="subtle" style={{ fontWeight: 400 }}>A registry host, or host/path for one group (covers its subgroups). The longest matching prefix wins.</span>
        </label>}
        {needLogin && <LoginFields name={name} setName={setName} username={username} setUsername={setUsername}
          expires={expires} setExpires={setExpires} onPw={setHasPw} showName={mode.kind === "add"} />}
        {mode.kind === "edit" && <>
          <label className="field">Name
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} maxLength={100} required />
          </label>
          <label className="field">Expires (optional)
            <input className="input" type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
            <span className="subtle" style={{ fontWeight: 400 }}>The token's expiry date: Owl warns 14 days before, here and in the weekly digest.</span>
          </label>
        </>}
        {err && <p className="err-text">{err}</p>}
        <div className="row">
          <button className="btn primary" disabled={busy || (needLogin && (!username.trim() || !hasPw)) || (needPrefix && !prefix.trim()) || (mode.kind === "edit" && !name.trim())}>{busy ? "Saving…" : "Save"}</button>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>Cancel</button>
        </div>
      </form>
    </Dialog>
  );
}
