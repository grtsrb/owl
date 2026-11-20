# Security

What Owl can see, what it stores and sends, and how to run it safely.

## Users and sign-in

- **Everything needs sign-in** except agent ingest (per-cluster token), `/healthz`, the agent manifest
  (`/api/v1/agent/manifest`, contains no secrets) and the sign-in endpoints themselves.
- **Invite-only.** An admin invites an email address with a role; the person opens a one-time link
  (72 hours) and sets a password, or signs in with Google. There's no self sign-up. The first admin is
  created on the server with `owl db user invite --admin`.
- **Roles**: *viewer* reads everything; *admin* can also change clusters, policies, settings, backups
  and users. Owl never lets the last active admin be demoted, disabled or deleted, and admins can't
  change or delete themselves.
- **Passwords**: 12–256 characters, hashed with argon2id (64 MiB, 3 passes; at most two hashes run at
  once so a burst of attempts can't exhaust memory). Failed sign-ins are rate-limited per IP and per
  account-from-that-IP (so nobody can lock your account by failing on purpose); IPv6 addresses count
  per /64 network. An unknown email and a wrong password get the same answer and take the same time.
  Changing or resetting a password signs the account out everywhere else and voids its open links.
- **Google sign-in** (OpenID Connect, authorization-code flow with PKCE, `state` bound to the browser
  by a short-lived cookie, `nonce`): only accounts whose email Google has verified, that match an
  invited user, and — with `OWL_GOOGLE_DOMAIN` — belong to that Workspace domain. After the first
  Google sign-in Owl remembers the account's permanent Google ID, so a later change of the account's
  email can't move it onto another user. The ID token comes straight from Google's token endpoint over
  TLS with the client secret, which OpenID Connect accepts in place of checking its signature; all its
  claims (issuer, audience, expiry, nonce, verified email, domain) are checked.
- **Password sign-in can be turned off** once Google works and an admin has signed in with Google.
  While it's off, the last active admin who signs in with Google can't be demoted, disabled or
  deleted; if the Google settings disappear from the environment, passwords come back on by
  themselves. The emergency way back in is `owl db user reset-link --email <email>` on the server (needs shell
  access to it).
- **Sessions**: a random 256-bit cookie, stored only as a SHA-256 hash. `HttpOnly`, `Secure`,
  `SameSite=Lax`, and the `__Host-` prefix (no subdomain can set or overwrite it); only on
  `http://localhost` is it a plain cookie. Sessions end after 7 days without use and after 30 days in
  any case; disabling a user ends theirs at once.
- **Links in emails** carry the secret in the URL fragment (`/invite#…`), which browsers never send to
  a server — it stays out of access logs, proxies and `Referer` headers. "Forgot password?" answers the
  same whether or not the account exists, and sends nothing for unknown, invited or disabled accounts.
- **Email** is sent only over TLS (implicit or STARTTLS — no fallback to plain); the SMTP password
  never travels unencrypted.
- **Audit log**: every admin action, plus accepted invites, password resets and changes and Google
  account links, with the user's email and client IP.
- **Restores never change who can sign in**: users, sessions, links, API tokens and the password
  sign-in setting are kept as they are; backups contain users (email, role, Google link) but **no
  password hashes**, sessions, links or API tokens.
- **Users from a file** (`owl users apply`) go through the same rules: the person applying can't
  disable or demote themselves, the last active admin stays, and every change is audited.
- **The CLI** refuses `http://` except to localhost, never accepts a token as a command-line argument
  and never follows redirects with it. The downloadable CLI is built into the server image; its
  SHA-256 checksums are shown next to the downloads (the binaries aren't code-signed).

> **Why:** Owl knows which versions — and which known vulnerabilities — run where. That's exactly what an attacker looks for, so nothing is public, nobody can sign up, and every secret Owl stores is a hash that's useless if the database or a backup leaks.

## The agent

- **Read-only**: its ClusterRole allows only `get` and `list` on pods, nodes, deployments,
  statefulsets, daemonsets, replicasets, jobs and cronjobs. **No Secrets, no ConfigMaps, no write
  verbs.** The role is defined once, in the agent manifest Owl serves (Settings → Clusters, or
  `/api/v1/agent/manifest`), and matches exactly what the agent reads.
- **Push-only**: the agent connects out to the server. The server never connects into a cluster and
  never stores kubeconfigs.
- Sends only: Kubernetes version, node info (name, kubelet/OS/kernel/runtime version, architecture),
  and per workload its kind, namespace, name, replica count, container names and images, running
  image digests and pod counts. **No environment variables, no configs, no logs.**
- Runs as non-root with a read-only root filesystem, no privilege escalation, all capabilities dropped.
- Refuses an `http://` server URL unless `OWL_ALLOW_INSECURE_HTTP=true`.
- The token is read from the environment only (a flag would show in `ps`).

> **Why:** if Owl could connect into your clusters, a compromised Owl would be a way in. Pushing from a read-only agent means the server holds nothing that grants access to a cluster, and the agent can't read secrets or change anything even if its token leaks.

## Tokens

| Token | Where it lives | Protection |
|---|---|---|
| Agent token (per cluster) | the cluster's `owl-agent` Secret | Random, shown once, stored as a SHA-256 hash. Revocations are permanent, even across restores. |
| Session cookie | the browser (`HttpOnly`, not readable by scripts) | Random, stored as a SHA-256 hash. See above. |
| Personal API token (`owl_pat_…`) | your `~/.config/owl/config.yaml` (0600) or a CI variable | Random 256-bit, shown once, stored as a SHA-256 hash; never in backups and never restored. Read-only unless an admin chose read & write, and never more than its owner can do (a demoted admin's token only reads; disabling a user deletes their tokens). Reads like its owner; changes only what the CLI needs (clusters, backups, users — an allowlist, so new routes are closed to tokens by default); never gets sign-in links, passwords, sign-in settings or tokens. A password change revokes all the user's tokens. Expires after 30/90/365 days or never (owner's choice; email a week before). Revocable by its owner, any admin, or itself (`owl logout`). Writes are audited as "you (token: name)". |
| Invite / reset link | an email or a message | Random, stored as a SHA-256 hash, works once, 72 h / 1 h. |
| Google client secret, SMTP password | server environment | Never in a flag, the UI, the database or logs. |
| GitHub token | server environment | Needs **no scopes** (public data only). Sent only to `api.github.com`. |
| Anthropic API key | server environment (`ANTHROPIC_API_KEY`) | Never in a flag, the UI, the database or logs. Put it in a dedicated Console workspace with a monthly spend limit, so a leak or a bug has a hard ceiling. |
| S3 keys | server environment | Should not allow deleting (see [Operations](operations.md#with-s3-recommended)). |

## Server protections

- **HTTPS**: run Owl behind a TLS proxy (the Compose setup's Caddy does it, with HSTS). Sign-in
  doesn't work over plain HTTP except on localhost.
- **Host allowlist**: the UI and API answer only to `localhost`, the `OWL_PUBLIC_URL` host and
  `OWL_ALLOWED_HOSTS` (`421` otherwise) — defence in depth against DNS rebinding. Agent ingest and
  `/healthz` are exempt.
- **Cross-site requests are refused**: every write must come from Owl's own pages (`Sec-Fetch-Site`,
  or `Origin` matching the host; Go's `CrossOriginProtection`), so another site can't act with your
  session. Writes are also JSON-only (restore uploads accept only gzip/octet-stream/sqlite types), and
  Owl sends no CORS headers.
- **Strict Content-Security-Policy** (`default-src 'self'`, no inline scripts, no third-party origins),
  `X-Frame-Options: DENY`, `nosniff`, `no-referrer`.
- **Size limits** on every body; gzip bombs are caught (see [Configuration → Limits](configuration.md#limits)).
- **Built-in docs** (`/api/v1/docs`) need sign-in like everything else. They're rendered when Owl is
  built, from Owl's own documentation; raw HTML in them is refused at build time, so a page can't carry
  scripts.
- **Strict validation** of names that end up in YAML or `kubectl` commands.
- **Logs never contain** headers, cookies, query strings, passwords or tokens (tested).
- **Client IPs** come from `X-Forwarded-For` only when the request arrives from `OWL_TRUSTED_PROXIES`.

## What leaves the server

| To | What |
|---|---|
| GitHub API, endoflife.date, OSV.dev | Public project names (e.g. `grafana/grafana`) — never cluster data. |
| Your S3 bucket | Backups (contain the whole database: inventory, token hashes, users without passwords, audit log with emails and IPs). Treat the bucket as sensitive. |
| Your mail server | Invite, reset and test emails: the recipient's address, who invited them, the one-time link. The weekly digest for users who opted in (same content as the Mattermost digest). |
| Your Mattermost (only with `OWL_MATTERMOST_WEBHOOK_URL`) | Digest and alerts: cluster, tenant and stage names, component names, versions, levels, reasons, recommended actions, image repositories, links into Owl. Everyone in the channel sees them. Names are escaped, so nothing in a message can format itself or @-mention people. The webhook URL is a secret and never logged. |
| Google (only with Google sign-in) | The browser is sent to Google to sign in; the server exchanges the one-time code for the account's ID token. |
| Claude API (only when summaries are set to manual or automatic) | Only public data: component name (for components added in the UI: their GitHub repository, never the name typed in Owl) and upstream repository, the two versions, upstream release notes. Never cluster names, namespaces, hosts, workloads, tenants, stages, configs or tokens (a test checks every request). |
| Public container registries (Docker Hub, ghcr.io, quay.io, registry.k8s.io, public.ecr.aws) — only when an admin opens **New component** | Anonymous requests for that one image's manifest and config, to read its source label. Never a private registry, never credentials. |
| GitHub, endoflife.date — for **New component** suggestions and preview | The repository/product names an admin typed or Owl suggested. |
| GitHub (raw files), haproxy.org, postgresql.org | Release-note fetches for summaries: public URLs built from the component and its release lines only. |

## Reporting a problem

Internal project: report security issues directly to the maintainer, not in a public channel.
Include the request id (`X-Request-ID`) if relevant.
