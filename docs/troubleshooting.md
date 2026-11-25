# Troubleshooting

When reporting a problem, include the **request id** from the `X-Request-ID` response header (browser
dev tools → Network) — it's in the matching server log line.

## Sign-in and UI

### I'm signed out right after signing in / the login page keeps coming back

The browser didn't keep the session cookie, which is `Secure`: Owl must be opened over **https://**
(only `http://localhost` / `127.0.0.1` work without it). The sign-in page warns when it's on plain
HTTP. Behind a proxy, make sure users reach it through the HTTPS address in `OWL_PUBLIC_URL`.

### "This link is invalid, was already used or has expired"

Invite links work once for 72 hours, reset links once for an hour, and a new link replaces the
previous one. Ask an admin for a new one (Settings → Users → Send invite again / Reset password).

### "Wrong email or password" / "Too many failed sign-ins"

After 10 failures for an account from your IP (or 30 from your IP in total) in 15 minutes, sign-in
from there waits 15 minutes. Use
**Forgot password?** or ask an admin for a reset link.

### Google: "That Google account hasn't been invited to Owl"

The invite must be for the exact address of the Google account. Ask an admin to invite that address
(and delete the other invite). "isn't from the allowed Google Workspace domain" means
`OWL_GOOGLE_DOMAIN` excludes it; "linked to a different Google account" means this Owl user already
signed in with another Google account — an admin can delete and re-invite the user.

### Google: "The sign-in took too long or was started in another browser"

Start again from the sign-in page; the round trip to Google must finish within 10 minutes in the
same browser.

### Google: `redirect_uri_mismatch` on Google's page

The client's *Authorized redirect URI* in the Google Cloud console must be exactly
`<OWL_PUBLIC_URL>/auth/google/callback` (Settings → Users shows it).

### Locked out (no admin can sign in)

On the server: `docker compose exec owl /owl db user reset-link --email you@example.com` prints a link
that sets a new password and signs you in — it works even with password sign-in turned off. No users
at all yet: `owl db user invite --admin you@example.com`.

### Emails don't arrive

Settings → Users → **Send me a test email** shows the SMTP error. "doesn't offer STARTTLS": the port is
for implicit TLS — use `OWL_SMTP_TLS=tls` with port 465. Check the spam folder and that `OWL_SMTP_FROM`
is a sender your mail server may use. "Forgot password?" never sends to unknown, invited or disabled
accounts (by design, it doesn't say so).

### "cross-origin request refused"

A write came from another site or origin. Use Owl's own address (`OWL_PUBLIC_URL`), not another name
or port that reaches the same server.

### `421 host not allowed (set OWL_ALLOWED_HOSTS)`

You opened the UI under a host name the server doesn't know. Add it to `OWL_ALLOWED_HOSTS` and
restart. `localhost`/`127.0.0.1` always work.

### "UI not built: run `make ui`"

The binary was built without the UI. Use the Owl image (it always includes the UI), or build with
`make ui && make build`.

### "The docs aren't built into this server"

The binary was built without its documentation. Use the Owl image (it always includes the docs), or
build with `make build`, which renders them first.

## Agents

### A cluster stays "waiting for the first report"

```bash
kubectl -n owl get pods
kubectl -n owl logs deploy/owl-agent
```
| Agent log | Cause / fix |
|---|---|
| `server rejected snapshot: 401 … invalid or revoked token` | Token wrong, rotated or the cluster was deleted/restored. **Rotate token**, apply the new Secret, `kubectl -n owl rollout restart deploy/owl-agent`. |
| `refusing to send the token over plain http` | Use `https://`, or `OWL_ALLOW_INSECURE_HTTP=true` for local dev only. |
| `push failed, retrying` with connection errors | The cluster can't reach the server: URL, DNS, firewall, proxy. |
| `server rejected snapshot: 400 … unsupported schemaVersion` | Agent and server versions are incompatible — update the older one. |
| `server rejected snapshot: 413 … snapshot too large` | More than ~100k containers in one cluster; report it. |
| `forbidden: … cannot list …` | The ClusterRole doesn't match the agent — re-apply the current `agent.yaml`. |
| `server URL (OWL_SERVER_URL) and token (OWL_TOKEN) are required` | The `owl-agent` Secret is missing or incomplete in namespace `owl` (if the Secret is missing entirely, the pod shows `CreateContainerConfigError`). |

### A cluster's last report is old

The agent is down, can't reach the server, or its token was revoked — check its logs as above. On a
laptop that sleeps, late reports are normal.

## Data looks wrong

### A component is "Unknown"

No upstream release data yet: the first refresh runs at startup and takes a moment; GitHub may be
rate-limiting (set `GITHUB_TOKEN`); or the project has no releases. Check with
`owl upstream --component <id>` and the server log (`upstream refresh incomplete`).

### "Upstream data stale"

An upstream source failed and Owl is using its last good copy. It retries every 6 hours; check the
server log for the error.

### An image is "unrecognized"

The catalog doesn't know it. Map it to a component, mark it in-house or ignore it on the cluster
page; or add it to the [catalog](catalog.md).

### A version is "unpinned"

The tag doesn't say which version runs (`latest`, `postgres:16`, digest only). Pin an exact version
in the workload.

### Advisories show "needs review"

The advisory's affected range is open-ended and no fix is listed (common with Redis). Read the
advisory and decide; it doesn't change the level.

### Drift is missing

Drift is only computed between clusters of the **same tenant** for components running in both, and is
hidden for clusters not selected in the cluster picker.

## Image scanning

### The Vulnerabilities page says scanning is off

The Scanner panel gives the reason: `OWL_SCAN=off`, `--no-upstream`, or *trivy not found* (a server binary
run outside the Owl image — set `OWL_TRIVY` to a Trivy binary).

### "Auth failed for registry.example.com/team-a"

The registry refused the login for that prefix (or wants one, for an image with no matching prefix:
`anonymous: <registry>`). Check the deploy token in `OWL_REGISTRY_AUTH_FILE` hasn't expired and has
`read_registry`, then restart the server and **Rescan now**. The image's last good result stays shown.

### An image is "not scannable"

It was built or imported on the node, so there's no registry digest to pull (k3d's `k3d image import`
does this too). Push it to a registry and deploy it from there to get it scanned.

### "registry host … is an IP address" / "is local or a metadata service"

Owl doesn't pull images from IP-address, `localhost` or metadata-service registries unless you configured
them (so a leaked agent token can't point the server at its own network). If it's a real registry, add it
to `OWL_INTERNAL_REGISTRIES` (or give it a login in `OWL_REGISTRY_AUTH_FILE`) and restart the server.

### "not in the registry (pulled from a mirror, deleted, or built locally)"

The registry doesn't have that digest any more (a re-pushed tag whose old digest was cleaned up), or
the nodes pull through a mirror the server can't see. Redeploy from the registry, or scan with a login
for the mirror's prefix.

### Scans wait and "Now scanning" says "waiting for the vulnerability DB"

The first start downloads ~3.5 GB from `mirror.gcr.io`. If the Scanner panel shows *DB update failed*,
the server can't reach it (proxy: `HTTPS_PROXY` is passed to Trivy) or the disk is full.

## Upgrade summaries

| You see | Meaning / fix |
|---|---|
| "Summaries are off" | The default. Settings → Claude summaries → choose a mode. |
| Manual/Automatic can't be selected | `ANTHROPIC_API_KEY` isn't set on the server (check the startup log). |
| Server won't start: "OWL_CLAUDE_MODEL … has no price" | Use one of the models listed in [Configuration](configuration.md); Owl only runs models it can price. |
| "Collecting release notes…" for long | Notes are fetched after each upstream refresh; check the log for `summaries: refresh failed`. |
| "No release notes available" | The project publishes no notes Owl can read for those versions. |
| "Release notes couldn't be fetched" | GitHub / haproxy.org / postgresql.org unreachable or rate-limited; retried on the next refresh. |
| "Larger than the input limit" | Not written automatically. **Generate anyway** (shows the maximum cost), or raise the limit. |
| "Waiting for budget" | Its worst case doesn't fit what's left of today's or this month's cap. Wait, or raise the cap. |
| "Failed today" | See the error; automatic mode retries tomorrow, or click **Try again**. |
| "Paused" | 3 failed requests in a row. Fix the cause (key, model, Anthropic status), then **Resume**. |
| "The output limit was reached" | Raise *Max output tokens* (thinking also counts as output). |

## Notifications

| You see | Meaning / fix |
|---|---|
| Server won't start: "OWL_MATTERMOST_WEBHOOK_URL: …" | The URL must be `https://…/hooks/<key>` (plain `http://` only to localhost). |
| "OWL_MATTERMOST_WEBHOOK_URL is not set" in Settings → Notifications | Set it in the server's environment and restart. |
| "Mattermost answered 400/403/404" | Wrong or deleted webhook, or incoming webhooks are disabled in Mattermost (System Console → Integrations). Not retried. |
| "Mattermost not reachable" | Network/DNS/TLS between Owl and Mattermost; retried 3 times, then dropped (alerts) or retried hourly (digest). |
| Posts show the webhook creator's name, not "Owl" | Mattermost → System Console → Integrations → *Enable integrations to override usernames*. |
| No alert although something is Critical | Alerts fire when something *becomes* Critical. What was Critical when you switched alerts on is in the next digest, not an alert. |
| No digest this week | It's sent at most 24 hours late; check the next-digest time and the last error in Settings → Notifications. |
| Messages have no links | `OWL_PUBLIC_URL` isn't set. |

## Catalog components

| You see | Meaning / fix |
|---|---|
| "… already belongs to the built-in component …" | Owl already knows that image; use it (or Map) instead of adding a component. |
| A component shows **Replaced by built-in** | A newer Owl ships it in its built-in catalog, which wins. Delete yours (move its policy first if you set one). |
| Every running tag shows **unpinned** in the preview | Tags like `latest`/`alpine` say no version. Pin versioned tags, or set a tag prefix / version pattern under Advanced. |
| "endoflife.date's newest release is … but GitHub's is …" | Probably different projects with the same name. Leave the endoflife.date product empty unless you've checked. |
| No suggestion for the GitHub repository | The image has no source label and no GitHub repository has its exact name — or it's on a private registry (no lookups there). Enter it yourself. |

## Backups

See the warnings table in [Operations → Backups](operations.md#warnings-in-settings--backups--restore).

### Restore says "not an Owl backup"

The file isn't a database written by Owl (or it's corrupted). Nothing was changed.

### Restore says the schema is newer than this binary supports

The backup comes from a newer Owl. Upgrade the server first, then restore.

### After a restore an agent gets `401`

Its token was rotated, or its cluster deleted, after the backup was taken. Revocations survive
restores by design — **Rotate token** and apply the new Secret.

## CLI

### `not signed in: run owl login`

No saved token and no `OWL_SERVER`/`OWL_TOKEN`. Create a token in Account → API tokens and run
`owl login --server https://owl.example.com`.

### `401 … invalid or revoked API token` / `this API token expired`

The token was revoked (`owl logout`, the UI, an admin) or expired, or your user was disabled. Create a
new one and `owl login` again (in CI: replace `OWL_TOKEN`).

### `403 this API token is read-only`

The token's scope is read-only, or its owner is no longer an admin. Writes need a *read & write* token
from an admin.

### `refusing plain http://`

Tokens only travel over HTTPS, except to `localhost`/`127.0.0.1`.

### macOS: "owl can't be opened because Apple cannot check it"

Downloaded with a browser, the unsigned binary is quarantined: `xattr -d com.apple.quarantine owl`.
(`No such xattr` means it isn't quarantined — nothing to do.)

### `owl users apply`: users changed since the plan was shown

Someone changed users between the plan and the apply. Nothing was changed — run it again.

### `no Owl database at owl.db`

`owl db …` works on the server's database file, so it runs on the server (`docker compose exec owl
/owl db …`). From your machine use the client commands (`owl clusters`, `owl users`).
