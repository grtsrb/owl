# HTTP API

Base path `/api/v1`. All responses are JSON unless noted. Errors are `{"error": "<message>"}` with a
matching status code. Every response carries an `X-Request-ID` header.

The UI uses exactly this API, so anything the UI does can be scripted. The TypeScript types in
`web/src/api.ts` mirror the response shapes.

## Authentication

| Kind | How | Used by |
|---|---|---|
| **session** | the `owl_session` cookie (`__Host-owl_session` over HTTPS) from signing in | every endpoint below; admin-only ones are marked 🔒 |
| **agent** | `Authorization: Bearer <agent token>` | `POST /snapshots` only |
| none | — | `/healthz`, `GET /agent/manifest` and the sign-in endpoints (`/auth/…` below) |

- Without a valid session: `401`. Signed in as a viewer on an admin endpoint: `403`.
- **Writes must come from Owl's own pages**: a browser request with `Sec-Fetch-Site` other than
  `same-origin`, or an `Origin` that doesn't match the host, gets `403 cross-origin request refused`.
  Non-browser clients (no such headers) aren't affected.
- Requests with a `Host` that isn't `localhost`, the `OWL_PUBLIC_URL` host or in `OWL_ALLOWED_HOSTS`
  get `421` (except `/healthz` and agent ingest).
- Request bodies of writes must be `Content-Type: application/json` (`415` otherwise); unknown fields
  are rejected.

Scripting with curl (until personal API tokens arrive with the CLI in milestone 10): sign in once
and keep the cookie.

```bash
B=https://owl.example.com
curl -s -c owl.cookies -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"…"}' $B/api/v1/auth/login
curl -s -b owl.cookies -H 'Content-Type: application/json' -X PUT -d '{"policy":"pin:12.3"}' $B/api/v1/policies/grafana
```

## Status codes

| Code | Meaning |
|---|---|
| 200 / 201 / 202 | OK / created / snapshot accepted |
| 400 | Invalid input (the message says what) |
| 401 / 403 | Not signed in (or the session ended) / not allowed: viewer on an admin endpoint, or a cross-origin write |
| 404 | Not found |
| 409 | Already exists, in use (deleting a stage/tenant that clusters use), or would leave no active admin |
| 429 | Too many attempts (sign-in, reset emails) |
| 413 | Body too large |
| 415 | Wrong content type |
| 421 | Host not allowed |
| 503 | Backups disabled on this server |

## Health

| | |
|---|---|
| `GET /healthz` | `200 ok` (plain text). No auth, no host check. |

## Sign-in

All under `/api/v1/auth/` except the two Google redirects.

| Endpoint | |
|---|---|
| `GET /auth/config` | Public. `{password, google, forgot}`: which sign-in methods the login page offers. |
| `POST /auth/login` | Public. `{email, password}` → `200 me` and the session cookie. `401 "Wrong email or password."` for any failure; `403` when password sign-in is off; `429` after 10 failures per account from one IP / 30 per IP in 15 minutes. |
| `POST /auth/logout` | Ends the session. |
| `GET /auth/me` | `{email, role, hasPassword, hasGoogle, digestEmail, emailAvailable}` |
| `PUT /auth/digest-email` | `{on}` — your own digest-by-email opt-in → `me`. `409` when turning it on without SMTP. |
| `POST /auth/password` | `{current, new}` — change your password (signs out your other sessions). |
| `POST /auth/token` | Public. `{token}` → `{email, kind: invite \| reset, password, google}` for an invite/reset page, without using the link. |
| `POST /auth/accept` | Public. `{token, password}` → sets the password, uses up the link, `200 me` and the session cookie. |
| `POST /auth/forgot` | Public. `{email}` → always the same `200 {message}`; emails a reset link (1 hour) to active accounts only. `404` when email or password sign-in is off. |
| `GET /auth/google/start` | Redirects to Google (not under `/api/v1`). |
| `GET /auth/google/callback` | Google redirects back here; on success a session and a redirect to `/`, otherwise to `/login?error=<reason>`. |

## Users 🔒

| Endpoint | |
|---|---|
| `GET /users` | `[{id, email, role, createdAt, activatedAt?, lastLoginAt?, disabledAt?, hasPassword, hasGoogle}]` |
| `POST /users` | `{email, role: admin \| viewer}` → `201 {user, link, emailed, emailError?}` — the invite link is returned **only here** (and emailed when SMTP is set up) |
| `POST /users/{id}/invite` | New invite link for a pending user (the old one stops working) → `{user, link, emailed}` |
| `POST /users/{id}/reset` | Password-reset link for an active user (1 hour) → `{user, link, emailed}`. `409` when password sign-in is off. |
| `PATCH /users/{id}` | `{role, disabled}` → user. Disabling ends their sessions. `409` for yourself, the last active admin, or — with password sign-in off — the last admin who signs in with Google. |
| `DELETE /users/{id}` | `409` in the same cases. |
| `GET /auth/settings` | `{passwordLogin, google: {configured, domain, redirectURL, linkedAdmins}, mail: {configured, host, port, from, tls}, publicURL}` |
| `PUT /auth/settings` | `{passwordLogin}` — turning it off needs Google configured and an admin who signed in with Google (`409` otherwise). |
| `POST /mail/test` | Sends a test email to you → `{to}`; `502` with the SMTP error. |

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

## Catalog components (milestone 11)

| Endpoint | |
|---|---|
| `GET /catalog` | The current catalog: built-in components plus the ones added in the UI (`custom: true`). |
| `GET /catalog/custom` | `[{component, state: {id, active, replacedBy?, problem?}, createdAt, updatedAt}]` |
| `POST /catalog/custom` 🔒 | `{component: {id, name, images, github, eol?, versionParts?, tagPrefix?, releaseTagPrefix?, versionPattern?}}` → `201` list. `409` when the id or an image is taken (built-in or added); `400` for invalid fields (`osv`, `releaseNotes`, `source`, `notes` can't be set). |
| `PUT /catalog/custom/{id}` 🔒 | Same body (the id comes from the path) → list |
| `GET /catalog/custom/{id}/impact` 🔒 | `{policy, overrides: [repository]}` — what a delete would also remove |
| `DELETE /catalog/custom/{id}` 🔒 | Deletes it, its policy and the overrides mapped to it → `{removed, components}` |
| `POST /catalog/preview` 🔒 | Same body as create; nothing is saved → `{upstream, githubLatest, eolLatest, tags: [{image, clusters, status, version?, reason?}], warnings, conflict?}` |
| `GET /catalog/suggest?image=<repository>` 🔒 | `{image, name, id, github: [{repo, from: label \| same-name}], eol: [{slug, label}], labelSource?, labelError?}` |

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
curl -s -b owl.cookies -X POST $B/api/v1/backups
curl -s -b owl.cookies -H 'Content-Type: application/gzip' \
  --data-binary @owl-20261117-030000.db.gz $B/api/v1/restore
```

## Claude upgrade summaries

| Endpoint | |
|---|---|
| `GET /summaries` | `{mode, enabled, paused, jumps: [{component, name, from, to, state, tokens, exact, maxCostMicros, sources: [{version, url}], error, summary?}]}`. `summary` has `headline`, `breakingChanges`, `migrations`, `deprecations`, `securityFixes`, `notableFeatures` (each `[{text, version}]`), `upgradePath {direct, via, reason}`, `risk {level, reason}`, `sources`, `model`, `promptVersion`, `createdAt`. States: `ready`, `off`, `collecting`, `no_notes`, `error`, `estimate`, `no_key`, `paused`, `too_large`, `failed`, `budget`, `available`, `queued`. |
| `GET /summaries/status` 🔒 | `{settings, enabled, model, fallback, price, paused, spentTodayMicros, spentMonthMicros, spentTotalMicros, maxPerSummaryMicros, summaries, requests: [last 20]}` |
| `PUT /summaries/settings` 🔒 | `{mode: off \| estimate \| manual \| auto, dailyCapMicros, monthlyCapMicros, maxInputTokens, maxOutputTokens}` — all required → status |
| `POST /summaries/generate` 🔒 | `{component, from, to, force}` — writes one summary now (synchronous, up to a minute). `force` = "Generate anyway" above the input limit. `409` when nothing was sent (mode, paused, too large, budget, no notes); `503` without an API key; `502` when the request was made and failed. → same as `GET /summaries` |
| `POST /summaries/resume` 🔒 | Clears a pause after repeated failures → status |

Money is in micro-dollars (`1000000` = $1). Days and months for the caps are UTC.

## Notifications 🔒

| Endpoint | |
|---|---|
| `GET /notifications` | `{settings: {digest, critical, unrecognized, newCluster, digestDay, digestTime, digestTimeZone}, status: {webhookConfigured, mailConfigured, linksConfigured, lastSentAt?, lastSentKind?, lastErrorAt?, lastError?, lastDigest?, nextDigest?, emailRecipients, tracking}}`. Never the webhook URL. |
| `PUT /notifications` | The `settings` object, all fields → same as `GET`. `digestDay` `monday`…`sunday`, `digestTime` `HH:MM`, `digestTimeZone` an IANA zone (`Europe/Belgrade`, `UTC`). |
| `POST /notifications/test` | Posts a test message. `502` with the reason when Mattermost refuses or can't be reached. |
| `POST /notifications/digest` | Posts this week's digest now (Mattermost only; doesn't count as the scheduled one). |

## Audit log 🔒

| Endpoint | |
|---|---|
| `GET /audit?limit=` | `[{id, at, action, details, remote, actor?}]`, newest first; `limit` default 500, max 5000. `actor` is the signed-in user's email (empty for entries from before sign-in). |
