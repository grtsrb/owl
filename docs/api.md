# HTTP API

Base path `/api/v1`. All responses are JSON unless noted. Errors are `{"error": "<message>"}` with a
matching status code. Every response carries an `X-Request-ID` header.

The UI uses exactly this API, so anything the UI does can be scripted. The TypeScript types in
`web/src/api.ts` mirror the response shapes.

## Authentication

| Kind | How | Used by |
|---|---|---|
| none | — | read endpoints (until sign-in, milestone 9) |
| **admin** | `Authorization: Bearer $OWL_ADMIN_TOKEN` | every write, plus admin-only reads (marked 🔒) |
| **agent** | `Authorization: Bearer <agent token>` | `POST /snapshots` only |

- Admin endpoints return `403` if the server has no `OWL_ADMIN_TOKEN` configured, `401` for a missing
  or wrong token.
- Requests with a `Host` that isn't `localhost` or in `OWL_ALLOWED_HOSTS` get `421` (except
  `/healthz` and agent ingest).
- Request bodies of writes must be `Content-Type: application/json` (`415` otherwise); unknown fields
  are rejected.

```bash
set -a; . ./.env; set +a
ADMIN=(-H "Authorization: Bearer $OWL_ADMIN_TOKEN" -H 'Content-Type: application/json')
curl -s "${ADMIN[@]}" -X PUT -d '{"policy":"pin:12.3"}' 127.0.0.1:8080/api/v1/policies/grafana
```

## Status codes

| Code | Meaning |
|---|---|
| 200 / 201 / 202 | OK / created / snapshot accepted |
| 400 | Invalid input (the message says what) |
| 401 / 403 | Token missing or wrong / writes disabled on the server |
| 404 | Not found |
| 409 | Already exists, or in use (deleting a stage/tenant that clusters use) |
| 413 | Body too large |
| 415 | Wrong content type |
| 421 | Host not allowed |
| 503 | Backups disabled on this server |

## Health

| | |
|---|---|
| `GET /healthz` | `200 ok` (plain text). No auth, no host check. |

## Inventory and reports (read)

| Endpoint | Returns |
|---|---|
| `GET /clusters` | `[{name, stage, tenant, kubernetesVersion, agentVersion, lastSeenAt}]` |
| `GET /clusters/{name}/images` | Current workloads/containers of one cluster (intended image, running images, replicas) |
| `GET /changes?cluster=&limit=` | Change history, newest first. `cluster` optional; `limit` default 200, max 1000 |
| `GET /inventory` | Components × clusters with instances, plus `unpinned`, `inHouse`, `unrecognized`, `ignored` lists |
| `GET /report` | The scored report: `summary` (components per level), `components[]` with `level`, `target`, `upstream`, `instances[]` (each with `score` + reasons and `action`), `drift[]`, `action`; plus `policies`, `stages`, counts |
| `GET /components/{id}` | `component` (catalog entry), `inventory` (where it runs), `upstream` (lines, EOL, archived, …), `advisoriesByVersion[]` |
| `GET /upstream` | Upstream info for every component (release lines, latest stable, archived/retired, advisory source, fetch time, `stale`, errors) |
| `GET /catalog` | The built-in catalog |
| `GET /policies` | `{global, overrides: {componentId: policy}, presets}` |
| `GET /stages` | `[{name, position, clusters}]` in stage order |
| `GET /tenants` | `[{name, clusters}]` |
| `GET /catalog/overrides` | `[{repository, kind, component, updatedAt}]` |
| `GET /settings` | `{serverURL, agentImage}` |
| `GET /agent/manifest` | `agent.yaml` (YAML, download) with the configured agent image. Contains no secrets. |

Drift entries: `{kind: pending_promotion | anomaly | only_in, text, tenant, clusters}`.
Levels: `critical`, `high`, `medium`, `low`, `unknown`, `ok`.

## Agent ingest

| Endpoint | |
|---|---|
| `POST /snapshots` | Agent token. Body: a snapshot (`internal/snapshot`), optionally `Content-Encoding: gzip`. `schemaVersion` must match the server's (currently `1`). Returns `202 {cluster, changes}`. |

The snapshot carries no cluster identity — the server derives it from the token.

## Policies 🔒

| Endpoint | Body |
|---|---|
| `PUT /policies/{scope}` | `{"policy": "n-2"}` — `scope` is `global` or a component id |
| `DELETE /policies/{scope}` | — (component goes back to the global policy) |

Policies: `lts-or-n-1`, `lts`, `latest`, `n-1`, `n-2` (any `n-<0..10>`), `pin:<version>`.

## Clusters 🔒

| Endpoint | Body / result |
|---|---|
| `POST /clusters` | `{name, stage, tenant}` → `201 {cluster, token}` — the token is returned **only here** |
| `PATCH /clusters/{name}` | `{stage, tenant}` → cluster |
| `POST /clusters/{name}/rotate-token` | → `{cluster, token}`; old tokens revoked immediately |
| `DELETE /clusters/{name}` | → `{deleted}`; removes all its data, revokes its tokens permanently |
| `GET /clusters/{name}/export` | JSON download `{format: "owl/cluster-export", version: 1, cluster, createdAt, images, changes}` |
| `POST /clusters/import` | body: an export (`application/json`, ≤ 64 MiB) → `201 {cluster, token}` |

## Stages, tenants 🔒

| Endpoint | Body |
|---|---|
| `POST /stages` | `{name}` → stage list |
| `PUT /stages` | `{order: [names…]}` — every stage exactly once → stage list |
| `DELETE /stages/{name}` | → stage list (`409` if in use) |
| `POST /tenants` | `{name}` → tenant list |
| `PATCH /tenants/{name}` | `{name: <new name>}` → tenant list (renames it on every cluster) |
| `DELETE /tenants/{name}` | → tenant list (`409` if in use) |

## Catalog overrides 🔒

| Endpoint | Body |
|---|---|
| `PUT /catalog/overrides` | `{repository, kind: map \| inhouse \| ignore, component}` (`component` only for `map`) → override list |
| `DELETE /catalog/overrides?repository=<repo>` | → override list |

`repository` is `registry/path` without tag or digest, e.g. `harbor.example.com/proxy/redis`.

## Settings 🔒

| Endpoint | Body |
|---|---|
| `PUT /settings` | `{serverURL, agentImage}` |
| `GET /ops` 🔒 | `{settings, backupsEnabled, logFile, backup: {target, dir, lastOK, lastAttempt, lastError, pendingUploads, nextRun, warnings}}` |
| `PUT /ops` | `{backupTime: "03:00", backupLocalDays, backupLocalCopies, logLevel, logRetentionDays, auditRetentionDays}` — all fields required → same as `GET /ops` |

## Backups and restore 🔒

| Endpoint | |
|---|---|
| `GET /backups` | `{backups: [{name, size, at, local, remote, uploaded}], remoteError?}` — a bucket error still returns the local list |
| `POST /backups` | Take a backup now → `201` backup info |
| `GET /backups/{name}` | Download (`application/gzip`) |
| `POST /backups/{name}/restore` | Restore a stored backup → `{restored, safetyBackup}` |
| `POST /restore` | Restore an uploaded file. Raw body, `Content-Type` one of `application/gzip`, `application/x-gzip`, `application/octet-stream`, `application/vnd.sqlite3`, `application/x-sqlite3`; ≤ 2 GiB → `{restored, safetyBackup}` |

```bash
curl -s -H "Authorization: Bearer $OWL_ADMIN_TOKEN" -X POST 127.0.0.1:8080/api/v1/backups
curl -s -H "Authorization: Bearer $OWL_ADMIN_TOKEN" -H 'Content-Type: application/gzip' \
  --data-binary @owl-20261117-030000.db.gz 127.0.0.1:8080/api/v1/restore
```

## Claude upgrade summaries

| Endpoint | |
|---|---|
| `GET /summaries` | Public. `{mode, enabled, paused, jumps: [{component, name, from, to, state, tokens, exact, maxCostMicros, sources: [{version, url}], error, summary?}]}`. `summary` has `headline`, `breakingChanges`, `migrations`, `deprecations`, `securityFixes`, `notableFeatures` (each `[{text, version}]`), `upgradePath {direct, via, reason}`, `risk {level, reason}`, `sources`, `model`, `promptVersion`, `createdAt`. States: `ready`, `off`, `collecting`, `no_notes`, `error`, `estimate`, `no_key`, `paused`, `too_large`, `failed`, `budget`, `available`, `queued`. |
| `GET /summaries/status` 🔒 | `{settings, enabled, model, fallback, price, paused, spentTodayMicros, spentMonthMicros, spentTotalMicros, maxPerSummaryMicros, summaries, requests: [last 20]}` |
| `PUT /summaries/settings` 🔒 | `{mode: off \| estimate \| manual \| auto, dailyCapMicros, monthlyCapMicros, maxInputTokens, maxOutputTokens}` — all required → status |
| `POST /summaries/generate` 🔒 | `{component, from, to, force}` — writes one summary now (synchronous, up to a minute). `force` = "Generate anyway" above the input limit. `409` when nothing was sent (mode, paused, too large, budget, no notes); `503` without an API key; `502` when the request was made and failed. → same as `GET /summaries` |
| `POST /summaries/resume` 🔒 | Clears a pause after repeated failures → status |

Money is in micro-dollars (`1000000` = $1). Days and months for the caps are UTC.

## Audit log 🔒

| Endpoint | |
|---|---|
| `GET /audit?limit=` | `[{id, at, action, details, remote, actor?}]`, newest first; `limit` default 500, max 5000 |
