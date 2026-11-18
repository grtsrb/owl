import { useState } from "react";
import { api, useIsAdmin, type ApiToken, type CreatedApiToken, type TokenScope, type User } from "../api";
import { Icon } from "../icons";
import { ago, Card, date, Empty, ErrorBox, Loading, Msg, Tag, useApi } from "../ui";
import { Dialog, useWrite } from "./Settings";

// Personal API tokens for the CLI (milestone 12): Account → API tokens, and every user's tokens for
// admins on the Users page.

const WEEK = 7 * 24 * 3600 * 1000;

const platformName: Record<string, string> = {
  "darwin-arm64": "macOS (Apple silicon)", "darwin-amd64": "macOS (Intel)",
  "linux-amd64": "Linux (x86-64)", "linux-arm64": "Linux (ARM64)",
};

/** Downloads of the `owl` client, built into the server image. */
export function CliDownloadCard() {
  const dl = useApi(api.cliDownloads);
  const origin = window.location.origin;
  return (
    <Card title="Command-line tool">
      <p className="subtle" style={{ margin: 0 }}>The <code>owl</code> CLI reads the same data as this UI (and, with a read & write token, adds
        clusters, runs backups and manages users). Download it for your machine, then sign in with a token from below.</p>
      {dl.error ? <ErrorBox error={dl.error} retry={dl.reload} /> : !dl.data ? <Loading /> : dl.data.files.length === 0 ? (
        <div className="callout info"><Icon.info /><span>This server has no CLI downloads (they're built into the Docker image). Build them with <code>make cli</code> and point <code>OWL_CLI_DIR</code> at <code>bin/cli</code>.</span></div>
      ) : (
        <>
          <div className="row">
            {dl.data.files.map((f) => {
              const p = `${f.os}-${f.arch}`;
              return <a key={p} className="btn small" href={`/api/v1/cli/${p}`} download="owl"
                title={`SHA-256 ${f.sha256}`}>{platformName[p] ?? p} · {(f.size / 1e6).toFixed(1)} MB</a>;
            })}
          </div>
          <pre className="code" style={{ whiteSpace: "pre-wrap", wordBreak: "break-all", margin: 0 }}>{[
            "chmod +x owl && sudo mv owl /usr/local/bin/   # macOS, only if it refuses to open: xattr -d com.apple.quarantine owl",
            `owl login --server ${origin}`,
            "owl status",
            "",
            "# CI (token in OWL_TOKEN):",
            `curl -fsSL -H "Authorization: Bearer $OWL_TOKEN" ${origin}/api/v1/cli/linux-amd64 -o owl && chmod +x owl`,
            `OWL_SERVER=${origin} ./owl outdated --fail-on critical`,
          ].join("\n")}</pre>
          <details>
            <summary className="subtle">SHA-256 checksums (version {dl.data.version})</summary>
            <pre className="code" style={{ whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{dl.data.files.map((f) => `${f.sha256}  owl-${f.os}-${f.arch}`).join("\n")}</pre>
          </details>
        </>
      )}
    </Card>
  );
}

function Expiry({ t }: { t: ApiToken }) {
  if (!t.expiresAt) return <span className="muted" title="This token never expires">never</span>;
  const left = new Date(t.expiresAt).getTime() - Date.now();
  if (left <= 0) return <Tag tone="warn" title={`Expired on ${date(t.expiresAt)}; it's removed from this list 30 days later`}>expired</Tag>;
  return <span className={left < WEEK ? "warn-text" : undefined} title={date(t.expiresAt)}>{ago(t.expiresAt)}</span>;
}

/** Scope as it works right now: a write token of someone who is no longer an admin only reads. */
function Scope({ t, ownerRole }: { t: ApiToken; ownerRole?: string }) {
  if (t.scope === "write" && ownerRole && ownerRole !== "admin") {
    return <Tag title="Created as read & write, but its owner is no longer an admin: it only reads">read (write inactive)</Tag>;
  }
  return t.scope === "write" ? <Tag tone="warn" title="Can change things, like its owner in the web UI">read & write</Tag> : <Tag>read-only</Tag>;
}

export function ApiTokensCard() {
  const tokens = useApi(api.apiTokens);
  const isAdmin = useIsAdmin();
  const w = useWrite();
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<CreatedApiToken | null>(null);

  return (
    <Card title="API tokens" actions={<button className="btn primary" onClick={() => setCreating(true)}>+ New token</button>}>
      <p className="subtle" style={{ margin: 0 }}>For the <code>owl</code> command-line tool and CI jobs. A token acts as you: everything it changes
        is in the audit log under your name and the token's name. It can never change your password or create other tokens.</p>
      {tokens.error ? <ErrorBox error={tokens.error} retry={tokens.reload} /> : !tokens.data ? <Loading /> : tokens.data.length === 0 ? <Empty>No tokens yet.</Empty> : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>Name</th><th>Scope</th><th>Created</th><th>Last used</th><th>Expires</th><th></th></tr></thead>
            <tbody>
              {tokens.data.map((t) => (
                <tr key={t.id}>
                  <td><b>{t.name}</b> <span className="mono muted">{t.prefix}…</span></td>
                  <td><Scope t={t} ownerRole={isAdmin ? "admin" : "viewer"} /></td>
                  <td title={t.createdAt}>{date(t.createdAt)}</td>
                  <td>{ago(t.lastUsedAt)}</td>
                  <td><Expiry t={t} /></td>
                  <td style={{ textAlign: "right" }}>
                    <button className="btn small danger" disabled={w.busy} onClick={() => {
                      if (confirm(`Revoke the token "${t.name}"? Anything using it stops working at once.`)) {
                        void w.run(() => api.revokeApiToken(t.id), `Token "${t.name}" revoked.`).then(tokens.reload);
                      }
                    }}>Revoke</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Msg msg={w.msg} />
      {creating && <NewTokenDialog admin={isAdmin} onClose={() => setCreating(false)}
        onCreated={(c) => { setCreating(false); setCreated(c); tokens.reload(); }} />}
      {created && <SecretDialog c={created} onClose={() => setCreated(null)} />}
    </Card>
  );
}

function NewTokenDialog({ admin, onClose, onCreated }: { admin: boolean; onClose: () => void; onCreated: (c: CreatedApiToken) => void }) {
  const [name, setName] = useState("");
  const [scope, setScope] = useState<TokenScope>("read");
  const [days, setDays] = useState(90);
  const w = useWrite();
  const valid = /^[A-Za-z0-9._-]([A-Za-z0-9 ._-]{0,38}[A-Za-z0-9._-])?$/.test(name);
  return (
    <Dialog title="New API token" onClose={onClose}>
      <form className="stack" onSubmit={async (e) => {
        e.preventDefault();
        const c = await w.run(() => api.createApiToken(name, scope, days));
        if (c) onCreated(c);
      }}>
        <label className="field">Name
          <input className="input" autoComplete="off" placeholder="laptop, gitlab-ci, …" maxLength={40} value={name} onChange={(e) => setName(e.target.value)} autoFocus />
          <span className="subtle" style={{ fontWeight: 400 }}>Where it's used, so you know which one to revoke. Letters, digits, spaces and . _ -</span>
        </label>
        <label className="field">Scope
          <select className="select" value={scope} onChange={(e) => setScope(e.target.value as TokenScope)}>
            <option value="read">read-only — reports, clusters, components, changes</option>
            {admin && <option value="write">read & write — also add clusters, run backups, manage users</option>}
          </select>
          {!admin && <span className="subtle" style={{ fontWeight: 400 }}>Viewers' tokens are read-only.</span>}
        </label>
        <label className="field">Expires
          <select className="select" value={days} onChange={(e) => setDays(Number(e.target.value))}>
            <option value={30}>in 30 days</option>
            <option value={90}>in 90 days</option>
            <option value={365}>in a year</option>
            <option value={0}>never</option>
          </select>
          <span className="subtle" style={{ fontWeight: 400 }}>{days === 0
            ? "A token that never expires works until you revoke it — fine for CI, but keep it in a protected variable."
            : "You get an email a week before it expires (if email is set up)."}</span>
        </label>
        <Msg msg={w.msg} />
        <div className="row" style={{ justifyContent: "flex-end" }}>
          <button type="button" className="btn" onClick={onClose}>Cancel</button>
          <button className="btn primary" disabled={!valid || w.busy}>{w.busy ? "Creating…" : "Create token"}</button>
        </div>
      </form>
    </Dialog>
  );
}

/** The token itself, shown once. */
function SecretDialog({ c, onClose }: { c: CreatedApiToken; onClose: () => void }) {
  const [copied, setCopied] = useState("");
  async function copy() {
    try { await navigator.clipboard.writeText(c.secret); setCopied("yes"); } catch { setCopied("failed"); }
  }
  return (
    <Dialog title="Token created" onClose={onClose}>
      <div className="callout warn"><Icon.warn /><span>Copy <b>{c.token.name}</b> now: Owl only keeps a hash and <b>can't show it again</b>.</span></div>
      <pre className="code" style={{ whiteSpace: "pre-wrap", wordBreak: "break-all" }}>{c.secret}</pre>
      <p className="subtle" style={{ margin: 0 }}>On your machine: <code>owl login --server {window.location.origin}</code> and paste it.
        In CI: store it as a protected, masked variable <code>OWL_TOKEN</code> and set <code>OWL_SERVER</code>.</p>
      {copied === "failed" && <p className="err-text">Copy failed (the clipboard needs HTTPS or localhost). Select the text instead.</p>}
      <div className="row" style={{ justifyContent: "flex-end" }}>
        <button className="btn" onClick={copy}>{copied === "yes" ? "Copied" : "Copy token"}</button>
        <button className="btn primary" onClick={onClose}>Done</button>
      </div>
    </Dialog>
  );
}

/** Admins: every user's tokens, to see what exists and revoke any of them. */
export function AllTokensCard({ users, reloadKey }: { users: User[]; reloadKey: number }) {
  const tokens = useApi(api.allApiTokens, [reloadKey]);
  const w = useWrite();
  const role = new Map(users.map((u) => [u.id, u]));
  return (
    <Card title="API tokens">
      <p className="subtle" style={{ margin: 0 }}>Every user's tokens. Users create their own under Account; disabling a user deletes their tokens.</p>
      {tokens.error ? <ErrorBox error={tokens.error} retry={tokens.reload} /> : !tokens.data ? <Loading /> : tokens.data.length === 0 ? <Empty>No API tokens.</Empty> : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>User</th><th>Name</th><th>Scope</th><th>Last used</th><th>Expires</th><th></th></tr></thead>
            <tbody>
              {tokens.data.map((t) => {
                const owner = role.get(t.userId);
                return (
                  <tr key={t.id}>
                    <td>{t.userEmail}{owner?.disabledAt && <span className="muted"> (disabled)</span>}</td>
                    <td><b>{t.name}</b> <span className="mono muted">{t.prefix}…</span></td>
                    <td><Scope t={t} ownerRole={owner?.role} /></td>
                    <td>{ago(t.lastUsedAt)}</td>
                    <td><Expiry t={t} /></td>
                    <td style={{ textAlign: "right" }}>
                      <button className="btn small danger" disabled={w.busy} onClick={() => {
                        if (confirm(`Revoke ${t.userEmail}'s token "${t.name}"? Anything using it stops working at once.`)) {
                          void w.run(() => api.revokeApiToken(t.id), `Token "${t.name}" of ${t.userEmail} revoked.`).then(tokens.reload);
                        }
                      }}>Revoke</button>
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
  );
}
