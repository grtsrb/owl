import { useEffect, useState, type ReactNode } from "react";
import { Link, useLocation, useNavigate } from "react-router-dom";
import { api, session, type TokenInfo } from "../api";
import { Icon } from "../icons";
import { formValue, Loading, Msg, pwLength, useApi } from "../ui";

// Reasons the server sends back to /login?error=… after a Google sign-in attempt.
const googleErrors: Record<string, string> = {
  state: "The sign-in took too long or was started in another browser. Try again.",
  google_off: "Google sign-in isn't set up on this server.",
  google_cancelled: "Google sign-in was cancelled.",
  google_failed: "Google sign-in failed. Try again, or ask an admin to check the server log.",
  domain: "That Google account isn't from the allowed Google Workspace domain.",
  not_invited: "That Google account hasn't been invited to Owl. Ask an admin to invite its email address.",
  account_mismatch: "This Owl account is linked to a different Google account.",
  disabled: "This account is disabled. Ask an admin.",
  too_many: "Too many attempts. Wait a few minutes and try again.",
};

function insecure(): boolean {
  const h = window.location.hostname;
  return window.location.protocol === "http:" && h !== "localhost" && h !== "127.0.0.1" && h !== "[::1]";
}

function AuthCard({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="auth-wrap">
      <div className="auth-card">
        <div className="auth-brand"><span className="brand-mark"><Icon.logo size={22} /></span>Owl</div>
        <h1>{title}</h1>
        {insecure() && <div className="callout warn"><Icon.warn /><span>This page isn't on HTTPS, so the browser won't keep you signed in. Open Owl through its https:// address.</span></div>}
        {children}
      </div>
    </div>
  );
}

const RETURN_KEY = "owl-return-to";

/** Where the user was when they started a Google sign-in (path + query + #section), for this tab only. */
function rememberReturn() {
  const { pathname, search, hash } = window.location;
  if (pathname === "/" || pathname === "/login" || pathname === "/invite" || pathname === "/reset") return;
  try { sessionStorage.setItem(RETURN_KEY, pathname + search + hash); } catch { /* storage blocked: lands on Overview */ }
}

/** The page to go back to after a Google sign-in (the server always returns to "/"), once; null = none.
 * Only a path inside Owl is ever followed (never "//host" or a URL). */
export function takeReturn(): string | null {
  let v: string | null = null;
  try { v = sessionStorage.getItem(RETURN_KEY); sessionStorage.removeItem(RETURN_KEY); } catch { return null; }
  return v && v.length <= 1024 && /^\/(?![/\\])[^\s]*$/.test(v) ? v : null;
}

function GoogleButton() {
  // A full-page navigation: the server redirects to Google and back to "/".
  return <a className="btn google-btn" href="/auth/google/start" onClick={rememberReturn}>Sign in with Google</a>;
}

export function LoginPage() {
  const cfg = useApi(api.authConfig);
  const loc = useLocation();
  const error = new URLSearchParams(loc.search).get("error");
  const [email, setEmail] = useState("");
  const [hasPassword, setHasPassword] = useState(false); // the password itself stays in the (uncontrolled) input
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(error ? { ok: false, text: googleErrors[error] ?? "Sign-in failed." } : null);
  const [busy, setBusy] = useState(false);
  const [forgot, setForgot] = useState(false);

  if (!cfg.data) return cfg.error ? <AuthCard title="Sign in"><Msg msg={{ ok: false, text: "Owl isn't reachable. Try again in a moment." }} /></AuthCard> : <Loading />;
  const c = cfg.data;

  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const password = formValue(e.currentTarget, "password");
    setBusy(true); setMsg(null);
    try {
      if (forgot) {
        const r = await api.forgot(email.trim());
        setMsg({ ok: true, text: r.message });
      } else {
        session.set(await api.login(email.trim(), password));
      }
    } catch (err) {
      setMsg({ ok: false, text: err instanceof Error ? err.message : String(err) });
    } finally { setBusy(false); }
  }

  return (
    <AuthCard title={forgot ? "Reset your password" : "Sign in"}>
      {c.google && !forgot && <GoogleButton />}
      {c.google && c.password && !forgot && <div className="auth-or"><span>or</span></div>}
      {(c.password || forgot) && (
        <form className="stack" onSubmit={submit}>
          <label className="field">Email
            <input className="input" type="email" autoComplete="username" value={email} onChange={(e) => setEmail(e.target.value)} autoFocus={!c.google} required />
          </label>
          {!forgot && (
            <label className="field">Password
              <input className="input" type="password" name="password" autoComplete="current-password" onInput={(e) => setHasPassword(e.currentTarget.value !== "")} required />
            </label>
          )}
          <Msg msg={msg} />
          <button className="btn primary" disabled={busy || !email || (!forgot && !hasPassword)}>{busy ? "…" : forgot ? "Email me a link" : "Sign in"}</button>
        </form>
      )}
      {!c.password && !forgot && msg && <Msg msg={msg} />}
      <p className="subtle auth-foot">
        {c.forgot && (forgot
          ? <button className="link-btn" onClick={() => { setForgot(false); setMsg(null); setHasPassword(false); }}>Back to sign-in</button>
          : <button className="link-btn" onClick={() => { setForgot(true); setMsg(null); }}>Forgot password?</button>)}
        {!c.forgot && "Forgot your password? Ask an admin for a reset link."}
      </p>
      <p className="subtle auth-foot">Owl is invite-only: ask an admin for an invite.</p>
    </AuthCard>
  );
}

/** /invite#<token> and /reset#<token>: set a password (or use Google for an invite) and sign in. */
export function AcceptPage() {
  const navigate = useNavigate();
  const loc = useLocation();
  const token = loc.hash.replace(/^#/, "");
  const [info, setInfo] = useState<TokenInfo | null>(null);
  const [err, setErr] = useState("");
  // The passwords stay in the (uncontrolled) inputs; only what the form needs to show is kept here.
  const [pwState, setPwState] = useState({ long: false, again: false, match: true });
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!token) { setErr("This link is incomplete. Copy the whole link from the email or message."); return; }
    api.tokenInfo(token).then(setInfo, (e) => setErr(e instanceof Error ? e.message : String(e)));
  }, [token]);

  if (err) return <AuthCard title="Link not valid"><Msg msg={{ ok: false, text: err }} /><p className="subtle auth-foot"><Link to="/">Go to sign-in</Link></p></AuthCard>;
  if (!info) return <Loading />;
  const invite = info.kind === "invite";

  function check(e: React.FormEvent<HTMLFormElement>) {
    const pw = formValue(e.currentTarget, "pw"), again = formValue(e.currentTarget, "again");
    setPwState({ long: pwLength(pw) >= 12, again: again !== "", match: pw === again });
  }

  async function submit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const pw = formValue(e.currentTarget, "pw");
    if (pw !== formValue(e.currentTarget, "again")) return;
    setBusy(true); setMsg(null);
    try {
      const me = await api.accept(token, pw);
      history.replaceState(null, "", "/"); // the token leaves the address bar and history
      session.set(me);
      navigate("/", { replace: true });
    } catch (e2) {
      setMsg({ ok: false, text: e2 instanceof Error ? e2.message : String(e2) });
    } finally { setBusy(false); }
  }

  return (
    <AuthCard title={invite ? "Welcome to Owl" : "Set a new password"}>
      <p className="subtle" style={{ margin: 0 }}>{invite ? "You were invited as" : "For"} <b>{info.email}</b>.</p>
      {invite && info.google && (
        <>
          <GoogleButton />
          <p className="subtle" style={{ margin: 0 }}>Use the Google account of {info.email}.</p>
          {info.password && <div className="auth-or"><span>or set a password</span></div>}
        </>
      )}
      {info.password && (
        <form className="stack" onSubmit={submit} onInput={check}>
          <input type="email" autoComplete="username" value={info.email} readOnly hidden />
          <label className="field">{invite ? "Password" : "New password"}
            <input className="input" type="password" name="pw" autoComplete="new-password" autoFocus={!info.google} />
            <span className="subtle" style={{ fontWeight: 400 }}>At least 12 characters. A few random words work well.</span>
          </label>
          <label className="field">Again
            <input className="input" type="password" name="again" autoComplete="new-password" />
          </label>
          {pwState.again && !pwState.match && <p className="err-text">The passwords don't match.</p>}
          <Msg msg={msg} />
          <button className="btn primary" disabled={busy || !pwState.long || !pwState.match}>{busy ? "…" : invite ? "Set password and sign in" : "Save and sign in"}</button>
        </form>
      )}
    </AuthCard>
  );
}
