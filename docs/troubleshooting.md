# Troubleshooting

When reporting a problem, include the **request id** from the `X-Request-ID` response header (browser
dev tools → Network) — it's in the matching server log line.

## Sign-in and UI

**I'm signed out right after signing in / the login page keeps coming back.**
The browser didn't keep the session cookie, which is `Secure`: Owl must be opened over **https://**
(only `http://localhost` / `127.0.0.1` work without it). The sign-in page warns when it's on plain
HTTP. Behind a proxy, make sure users reach it through the HTTPS address in `OWL_PUBLIC_URL`.

**"This link is invalid, was already used or has expired".**
Invite links work once for 72 hours, reset links once for an hour, and a new link replaces the
previous one. Ask an admin for a new one (Settings → Users → Send invite again / Reset password).

**"Wrong email or password" / "Too many failed sign-ins".**
After 10 failures for an account from your IP (or 30 from your IP in total) in 15 minutes, sign-in
from there waits 15 minutes. Use
**Forgot password?** or ask an admin for a reset link.

**Google: "That Google account hasn't been invited to Owl".**
The invite must be for the exact address of the Google account. Ask an admin to invite that address
(and delete the other invite). "isn't from the allowed Google Workspace domain" means
`OWL_GOOGLE_DOMAIN` excludes it; "linked to a different Google account" means this Owl user already
signed in with another Google account — an admin can delete and re-invite the user.

**Google: "The sign-in took too long or was started in another browser".**
Start again from the sign-in page; the round trip to Google must finish within 10 minutes in the
same browser.

**Google: `redirect_uri_mismatch` on Google's page.**
The client's *Authorized redirect URI* in the Google Cloud console must be exactly
`<OWL_PUBLIC_URL>/auth/google/callback` (Settings → Users shows it).

<a id="locked-out"></a>
**Locked out (no admin can sign in).**
On the server: `docker compose exec owl /owl user reset-link --email you@example.com` prints a link
that sets a new password and signs you in — it works even with password sign-in turned off. No users
at all yet: `owl user invite --admin you@example.com`.

**Emails don't arrive.**
Settings → Users → **Send me a test email** shows the SMTP error. "doesn't offer STARTTLS": the port is
for implicit TLS — use `OWL_SMTP_TLS=tls` with port 465. Check the spam folder and that `OWL_SMTP_FROM`
is a sender your mail server may use. "Forgot password?" never sends to unknown, invited or disabled
accounts (by design, it doesn't say so).

**"cross-origin request refused".**
A write came from another site or origin (e.g. the Vite dev server on `:5173` talking to `:8080`
directly). Use Owl's own address; in development, the Vite proxy (`make ui-dev`) is same-origin.

**`421 host not allowed (set OWL_ALLOWED_HOSTS)`.**
You opened the UI under a host name the server doesn't know. Add it to `OWL_ALLOWED_HOSTS` and
restart. `localhost`/`127.0.0.1` always work.

**"UI not built: run `make ui`".**
The binary was built without the UI. Run `make ui && make build` (the Docker image always includes it).

## Agents

**A cluster stays "waiting for the first report".**
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
| `server rejected snapshot: 413 … snapshot too large` | More than ~100k containers; report it (the limit is in `internal/server`). |
| `forbidden: … cannot list …` | The ClusterRole doesn't match the agent — re-apply the current `agent.yaml`. |
| `server URL (OWL_SERVER_URL) and token (OWL_TOKEN) are required` | The `owl-agent` Secret is missing or incomplete in namespace `owl` (if the Secret is missing entirely, the pod shows `CreateContainerConfigError`). |

**A cluster's last report is old.**
The agent is down, can't reach the server, or its token was revoked — check its logs as above. On a
laptop that sleeps, late reports are normal.

## Data looks wrong

**A component is "Unknown".**
No upstream release data yet: the first refresh runs at startup and takes a moment; GitHub may be
rate-limiting (set `GITHUB_TOKEN`); or the project has no releases. Check with
`owl upstream --component <id>` and the server log (`upstream refresh incomplete`).

**"Upstream data stale".**
An upstream source failed and Owl is using its last good copy. It retries every 6 hours; check the
server log for the error.

**An image is "unrecognized".**
The catalog doesn't know it. Map it to a component, mark it in-house or ignore it on the cluster
page; or add it to the [catalog](catalog.md).

**A version is "unpinned".**
The tag doesn't say which version runs (`latest`, `postgres:16`, digest only). Pin an exact version
in the workload.

**Advisories show "needs review".**
The advisory's affected range is open-ended and no fix is listed (common with Redis). Read the
advisory and decide; it doesn't change the level.

**Drift is missing.**
Drift is only computed between clusters of the **same tenant** for components running in both, and is
hidden for clusters not selected in the cluster picker.

## Upgrade summaries

| You see | Meaning / fix |
|---|---|
| "Summaries are off" | The default. Settings → Summaries → choose a mode. |
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

## Backups

See the warnings table in [Operations → Backups](operations.md#warnings-in-settings--backups--restore).

**Restore says "not an Owl backup".**
The file isn't a database written by Owl (or it's corrupted). Nothing was changed.

**Restore says the schema is newer than this binary supports.**
The backup comes from a newer Owl. Upgrade the server first, then restore.

**After a restore an agent gets `401`.**
Its token was rotated, or its cluster deleted, after the backup was taken. Revocations survive
restores by design — **Rotate token** and apply the new Secret.

## Local development

**`make stack-up` fails to connect agents.**
Check the k3d clusters exist (`k3d cluster list`; create with `make dev-up`) and Docker is running.
Re-running `make stack-up` is safe; it rotates the dev agent tokens and keeps the database volume.

**GitHub rate limit locally.**
Put `GITHUB_TOKEN=<token with no scopes>` in `.env` and re-run `make stack-up`.
