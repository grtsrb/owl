# Troubleshooting

When reporting a problem, include the **request id** from the `X-Request-ID` response header (browser
dev tools → Network) — it's in the matching server log line.

## UI and admin token

**Buttons stay disabled / "Set the admin token" although I pasted it.**
The token is kept per **browser tab** (session storage). A new tab, a new window, or a different
address for the same server (`localhost` vs `127.0.0.1`, or the Vite dev server on `:5173`) each
need it again. Set it in **Settings → Admin token** in the tab you're using; the sidebar shows
"Admin token: set for this tab".

**"The server rejected this token".**
It doesn't match the server's `OWL_ADMIN_TOKEN`. Locally it's in `.env` (`grep OWL_ADMIN_TOKEN .env`);
pasting the whole `OWL_ADMIN_TOKEN=…` line is fine. If you changed `.env`, restart the server
(`make stack-up`) — the running server keeps the token it started with.

**"write endpoints are disabled: set OWL_ADMIN_TOKEN on the server".**
The server was started without an admin token. Set it (≥ 24 characters) and restart.

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
