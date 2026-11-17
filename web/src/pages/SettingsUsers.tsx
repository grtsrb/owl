import { useState } from "react";
import { absoluteLink, api, session, useIsAdmin, useMe, type AuthSettings, type LinkResult, type Role, type User } from "../api";
import { Icon } from "../icons";
import { ago, Card, Empty, ErrorBox, Loading, Msg, Tag, useApi } from "../ui";
import { Dialog, useWrite } from "./Settings";

function status(u: User): { text: string; tone: string } {
  if (u.disabledAt) return { text: "disabled", tone: "neutral" };
  if (!u.activatedAt) return { text: "invited", tone: "warn" };
  return { text: "active", tone: "ok" };
}

export function UsersTab() {
  if (!useIsAdmin()) {
    return <Card title="Users & sign-in"><div className="callout info"><Icon.info /><span>Only admins can manage users and sign-in settings.</span></div></Card>;
  }
  return <UsersTabInner />;
}

function UsersTabInner() {
  const me = useMe();
  const users = useApi(api.users);
  const auth = useApi(api.authSettings);
  const w = useWrite();
  const [inviting, setInviting] = useState(false);
  const [link, setLink] = useState<{ title: string; r: LinkResult } | null>(null);

  if (users.error) return <ErrorBox error={users.error} retry={users.reload} />;
  if (!users.data || !auth.data) return auth.error ? <ErrorBox error={auth.error} retry={auth.reload} /> : <Loading />;
  const passwordLogin = auth.data.passwordLogin;

  async function act<T>(fn: () => Promise<T>, ok?: string) {
    const r = await w.run(fn, ok);
    users.reload(); auth.reload();
    return r;
  }
  async function showLink(title: string, fn: () => Promise<LinkResult>) {
    const r = await act(fn);
    if (r) setLink({ title, r });
  }

  return (
    <>
      <Card title="Users" actions={<button className="btn primary" onClick={() => setInviting(true)}>+ Invite user</button>}>
        <p className="subtle" style={{ margin: 0 }}>Only invited people can sign in. <b>Viewers</b> see everything; <b>admins</b> can also change
          clusters, policies, settings, backups and users.</p>
        {users.data.length === 0 ? <Empty>No users.</Empty> : (
          <div className="table-wrap">
            <table>
              <thead><tr><th>Email</th><th>Role</th><th>Status</th><th>Signs in with</th><th>Last sign-in</th><th></th></tr></thead>
              <tbody>
                {users.data.map((u) => {
                  const self = u.email === me?.email;
                  const st = status(u);
                  const methods = [u.hasPassword && "password", u.hasGoogle && "Google"].filter(Boolean).join(" + ") || "—";
                  return (
                    <tr key={u.id}>
                      <td><b>{u.email}</b>{self && <span className="muted"> (you)</span>}</td>
                      <td>
                        {self ? u.role : (
                          <select className="select" value={u.role} disabled={w.busy} aria-label={`Role of ${u.email}`}
                            onChange={(e) => void act(() => api.updateUser(u.id, e.target.value as Role, !!u.disabledAt), `${u.email} is now ${e.target.value}.`)}>
                            <option value="viewer">viewer</option><option value="admin">admin</option>
                          </select>
                        )}
                      </td>
                      <td><Tag tone={st.tone}>{st.text}</Tag></td>
                      <td>{methods}</td>
                      <td>{u.lastLoginAt ? ago(u.lastLoginAt) : "never"}</td>
                      <td>
                        {!self && (
                          <div className="row" style={{ justifyContent: "flex-end", flexWrap: "nowrap" }}>
                            {st.text === "invited" && <button className="btn small" disabled={w.busy} onClick={() => void showLink(`New invite for ${u.email}`, () => api.reinviteUser(u.id))}>Send invite again</button>}
                            {st.text === "active" && passwordLogin && <button className="btn small" disabled={w.busy} onClick={() => void showLink(`Password reset for ${u.email}`, () => api.resetUser(u.id))}>Reset password</button>}
                            <button className="btn small" disabled={w.busy} onClick={() => {
                              const disable = !u.disabledAt;
                              if (disable && !confirm(`Disable ${u.email}? They're signed out at once and can't sign in until enabled again.`)) return;
                              void act(() => api.updateUser(u.id, u.role, disable), `${u.email} ${disable ? "disabled" : "enabled"}.`);
                            }}>{u.disabledAt ? "Enable" : "Disable"}</button>
                            <button className="btn small danger" disabled={w.busy} onClick={() => {
                              if (confirm(`Delete ${u.email}? The audit log keeps what they did.`)) void act(() => api.deleteUser(u.id), `${u.email} deleted.`);
                            }}>Delete</button>
                          </div>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <Msg msg={w.msg} />
      </Card>

      <SignInSettings a={auth.data} reload={auth.reload} />

      {inviting && <InviteDialog onClose={() => setInviting(false)} onInvited={(r) => { setInviting(false); users.reload(); setLink({ title: `Invite for ${r.user.email}`, r }); }} />}
      {link && <LinkDialog title={link.title} r={link.r} onClose={() => setLink(null)} />}
    </>
  );
}

function InviteDialog({ onClose, onInvited }: { onClose: () => void; onInvited: (r: LinkResult) => void }) {
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const w = useWrite();
  const valid = /^[^\s@<>]+@[^\s@<>]+$/.test(email.trim());
  return (
    <Dialog title="Invite user" onClose={onClose}>
      <form className="stack" onSubmit={async (e) => { e.preventDefault(); const r = await w.run(() => api.inviteUser(email.trim(), role)); if (r) onInvited(r); }}>
        <label className="field">Email
          <input className="input" type="email" autoComplete="off" placeholder="name@example.com" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus />
          <span className="subtle" style={{ fontWeight: 400 }}>With Google sign-in, use the address of their Google account.</span>
        </label>
        <label className="field">Role
          <select className="select" value={role} onChange={(e) => setRole(e.target.value as Role)}>
            <option value="viewer">viewer — sees everything, changes nothing</option>
            <option value="admin">admin — can change everything, including users</option>
          </select>
        </label>
        <Msg msg={w.msg} />
        <div className="row" style={{ justifyContent: "flex-end" }}>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button className="btn primary" disabled={!valid || w.busy}>{w.busy ? "Inviting…" : "Invite"}</button>
        </div>
      </form>
    </Dialog>
  );
}

/** Shows a one-time link once: whether it was emailed, and a copy button either way. */
function LinkDialog({ title, r, onClose }: { title: string; r: LinkResult; onClose: () => void }) {
  const [copied, setCopied] = useState("");
  const link = absoluteLink(r.link);
  const reset = link.includes("/reset#");
  async function copy() {
    try { await navigator.clipboard.writeText(link); setCopied("yes"); } catch { setCopied("failed"); }
  }
  return (
    <Dialog title={title} onClose={onClose}>
      {r.emailed
        ? <div className="callout info"><Icon.info /><span>Emailed to <b>{r.user.email}</b>. You can also send the link yourself (e.g. on Mattermost).</span></div>
        : <div className="callout warn"><Icon.warn /><span>{r.emailError ?? "Email isn't set up on this server."} Send this link to <b>{r.user.email}</b> yourself.</span></div>}
      <pre className="code" style={{ whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{link}</pre>
      <p className="subtle" style={{ margin: 0 }}>Works once, for {reset ? "an hour" : "72 hours"}. {reset ? "It sets a new password and signs them in." : "They set a password (or sign in with Google) and are signed in."} It isn't shown again after you close this.</p>
      {copied === "failed" && <p className="err-text">Copy failed (the clipboard needs HTTPS or localhost). Select the text instead.</p>}
      <div className="row" style={{ justifyContent: "flex-end" }}>
        <button className="btn" onClick={copy}>{copied === "yes" ? "Copied" : "Copy link"}</button>
        <button className="btn primary" onClick={onClose}>Done</button>
      </div>
    </Dialog>
  );
}

/** Uses the parent's auth settings, so role changes above update "admins signed in with Google". */
function SignInSettings({ a, reload }: { a: AuthSettings; reload: () => void }) {
  const w = useWrite();
  const mailW = useWrite();
  const canTurnOff = a.google.configured && a.google.linkedAdmins > 0;
  return (
    <Card title="Sign-in & email">
      <div className="stack">
        <h2>Password sign-in</h2>
        <div className="row">
          <Tag tone={a.passwordLogin ? "ok" : "neutral"}>{a.passwordLogin ? "on" : "off"}</Tag>
          <button className="btn small" disabled={w.busy || (a.passwordLogin && !canTurnOff)} onClick={async () => {
            const off = a.passwordLogin;
            if (off && !confirm("Turn password sign-in off? Everyone must then sign in with Google. In an emergency, `owl user reset-link` on the server still works.")) return;
            if (await w.run(() => api.saveAuthSettings(!off), `Password sign-in turned ${off ? "off" : "on"}.`)) reload();
          }}>{a.passwordLogin ? "Turn off" : "Turn on"}</button>
        </div>
        {a.passwordLogin && !canTurnOff && <p className="subtle" style={{ margin: 0 }}>Can be turned off once Google sign-in is set up and at least one admin has signed in with Google — so nobody gets locked out.</p>}
        <Msg msg={w.msg} />
      </div>
      <div className="stack">
        <h2>Google sign-in</h2>
        {a.google.configured ? (
          <>
            <div className="row"><Tag tone="ok">configured</Tag>{a.google.domain && <span>only <b>@{a.google.domain}</b> accounts</span>}
              <span className="muted">{a.google.linkedAdmins} admin{a.google.linkedAdmins === 1 ? "" : "s"} signed in with Google</span></div>
            <p className="subtle" style={{ margin: 0 }}>Authorized redirect URI (Google Cloud console): <code>{a.google.redirectURL}</code></p>
          </>
        ) : (
          <p className="subtle" style={{ margin: 0 }}><Tag>not configured</Tag> Set <code>OWL_GOOGLE_CLIENT_ID</code> and <code>OWL_GOOGLE_CLIENT_SECRET</code> (and <code>OWL_PUBLIC_URL</code>) on the server — see the docs (Configuration → Sign-in). Invited users can then sign in with the Google account of their invited email.</p>
        )}
      </div>
      <div className="stack">
        <h2>Email</h2>
        {a.mail.configured ? (
          <>
            <div className="row"><Tag tone="ok">configured</Tag><span className="mono">{a.mail.host}:{a.mail.port} ({a.mail.tls})</span><span>from {a.mail.from}</span></div>
            <div className="row">
              <button className="btn small" disabled={mailW.busy} onClick={() => void mailW.run(api.testMail, "Test email sent — check your inbox.")}>{mailW.busy ? "Sending…" : "Send me a test email"}</button>
            </div>
            <Msg msg={mailW.msg} />
          </>
        ) : (
          <p className="subtle" style={{ margin: 0 }}><Tag>not configured</Tag> Invites and reset links are shown here for you to send. To email them, set <code>OWL_SMTP_HOST</code> and the other <code>OWL_SMTP_*</code> variables on the server.</p>
        )}
        {a.publicURL && <p className="subtle" style={{ margin: 0 }}>Links point to <code>{a.publicURL}</code> (OWL_PUBLIC_URL).</p>}
      </div>
    </Card>
  );
}

export function AccountTab() {
  const me = useMe();
  const cfg = useApi(api.authConfig);
  const w = useWrite();
  const [cur, setCur] = useState("");
  const [next, setNext] = useState("");
  const [again, setAgain] = useState("");
  if (!me) return <Loading />;
  const mismatch = again !== "" && next !== again;
  return (
    <>
      <Card title="Your account">
        <div className="row"><b>{me.email}</b><Tag>{me.role}</Tag>
          <span className="muted">signs in with {[me.hasPassword && "password", me.hasGoogle && "Google"].filter(Boolean).join(" + ") || "—"}</span></div>
        <div className="row"><button className="btn" onClick={() => void signOut()}>Sign out</button></div>
      </Card>
      {me.hasPassword && cfg.data?.password && (
        <Card title="Change password">
          <form className="stack" style={{ maxWidth: 420 }} onSubmit={async (e) => {
            e.preventDefault();
            if (await w.run(() => api.changePassword(cur, next), "Password changed. Your other sessions were signed out.")) { setCur(""); setNext(""); setAgain(""); }
          }}>
            <input type="email" autoComplete="username" value={me.email} readOnly hidden />
            <label className="field">Current password
              <input className="input" type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} />
            </label>
            <label className="field">New password
              <input className="input" type="password" autoComplete="new-password" minLength={12} value={next} onChange={(e) => setNext(e.target.value)} />
              <span className="subtle" style={{ fontWeight: 400 }}>At least 12 characters. A few random words work well.</span>
            </label>
            <label className="field">New password again
              <input className="input" type="password" autoComplete="new-password" value={again} onChange={(e) => setAgain(e.target.value)} />
            </label>
            {mismatch && <p className="err-text">The new passwords don't match.</p>}
            <Msg msg={w.msg} />
            <div><button className="btn primary" disabled={w.busy || !cur || [...next].length < 12 || next !== again}>{w.busy ? "Saving…" : "Change password"}</button></div>
          </form>
        </Card>
      )}
    </>
  );
}

export async function signOut() {
  try { await api.logout(); } finally { session.set(null); }
}
