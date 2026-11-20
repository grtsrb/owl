# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is
Owl: tracks third-party components running in Kubernetes clusters (RKE2), compares them
with upstream releases / EOL / advisories, scores upgrade priority, and uses Claude to summarize
version jumps. **`DESIGN.md` is the source of truth for decisions** — read it before non-trivial work,
and update it when a decision changes.

**User/operator docs live in `README.md` + `docs/`** and describe what's on `main`. Every change users
or operators can notice updates the matching page in the same branch (which page: table at the end of
`docs/development.md`); each merged milestone adds a `docs/changelog.md` entry.

## Status
Milestones 1–11 (agent, server core, catalog, upstream data, policy/scoring/drift, web UI, UI
redesign + cluster management, backups/restore/logging/audit, Claude summaries, sign-in + packaging,
Mattermost notifications, catalog from the UI + Settings reorganised, CLI + personal API tokens + users
from a file) done; v1 roadmap complete — v2 ideas at the bottom of `DESIGN.md`. Milestone 13 (bigger built-in
catalog, DESIGN.md "Bigger built-in catalog") in progress: plumbing + batch 1 (Elastic, networking, certs,
monitoring) and batch 2 (CI runners, storage/backup — 71 built-ins) done; batch 3 (data/policy/ops) and
GitOps (Argo/Flux) parked by the user — DESIGN.md "Later". Keep this file's commands and
layout in sync as milestones land.

## Commands
```bash
make build test lint        # bin/owl, go test ./..., go vet + gofmt check
make cli                    # client-only `owl` (-tags owlclient) for 4 platforms into bin/cli (the image builds the same)
make ui                     # build web/ (React) into web/dist, embedded by the next `make build`
make docs                   # docs/*.md → internal/docs/dist (HTML + search index); `make build` runs it
make ui-dev                 # Vite dev server :5173 with /api proxied to :8080 (needs make stack-up)
cd web && npm run typecheck # TypeScript check only
go test ./internal/agent -run TestCollect -v    # single test
make dev-up                 # create the 3 k3d clusters (different k3s versions) + seed them
make seed                   # re-apply seed workloads (dev/seed/seed.sh — bash 3.2 compatible)
make agent-print CLUSTER=it-aws-prod   # what the agent would send, as JSON (uses k3d-<name> context)
make image                  # Docker image (same image for agent and server)
make stack-up               # server + S3Mock + Mailpit (127.0.0.1:8025) in Docker, agent in every k3d cluster
                            # first run prints the admin@owl.local invite link
docker exec owl-server /owl db cluster list         # registered clusters, last seen
docker exec owl-server /owl db user reset-link --email admin@owl.local   # lost the dev password
make mattermost-up          # optional throwaway Mattermost + webhook (dev/.mattermost.env); then make stack-up
make mattermost-down
make image-push REGISTRY=registry.gitlab.example.com/devops VERSION=0.9.0   # amd64+arm64
# API: a personal token from Account → API tokens (or a session cookie from /api/v1/auth/login)
./bin/owl login --server http://127.0.0.1:8080      # then: owl status / outdated / component coredns / users plan …
curl -s -H "Authorization: Bearer $OWL_TOKEN" 127.0.0.1:8080/api/v1/report   # levels + reasons + targets + drift
./bin/owl upstream --component redis --version 7.2.4      # debug upstream data (needs GITHUB_TOKEN)
make catalog-check COMPONENTS=redis,coredns               # verify catalog entries live (GitHub/endoflife.date/OSV)
set -a; . ./.env; set +a   # GITHUB_TOKEN (+ optional OWL_GOOGLE_CLIENT_ID/SECRET for local Google sign-in)
make dev-down
```

## Layout / architecture
- `cmd/owl` — single binary. `main.go` dispatches: client commands (`internal/cli`) and, in the full
  build (`full.go`, `agent.go`, `server.go`, `auth.go` are `//go:build !owlclient`): `agent`, `server`,
  `db cluster add|list|rotate-token|delete` and `db user invite|list|reset-link|delete` (work on the DB
  file, refuse a missing one: first admin, emergencies), `healthcheck` (Docker HEALTHCHECK; distroless
  has no curl), `upstream` (debug). `legacy()` keeps `owl user …` / `owl cluster … --name|--db|list`
  working with a warning for one release. `-tags owlclient` = the downloadable client (no server,
  agent, k8s or SQLite code — keep it that way).
- `internal/cli` — the client (milestone 12): stdlib + sigs.k8s.io/yaml + x/term only. Config
  `~/.config/owl/config.yaml` (0600), `OWL_SERVER`/`OWL_TOKEN` override; refuses `http://` except
  loopback, never follows redirects, token never as an argument. Local JSON structs for API answers
  (don't import server packages). `App` is injectable for tests (`cli_test.go` runs a real in-process
  server). Exit codes: 1 error, bad flags or `--fail-on` hit; 2 only `users plan --detailed-exitcode` drift. A
  saved token is never sent to an `OWL_SERVER` other than the saved one. `cmd/owl/auth.go`
  parses the sign-in env (`OWL_PUBLIC_URL`, `OWL_TRUSTED_PROXIES`, Google, SMTP).
- `internal/snapshot` — **wire contract** agent → server. Contains no cluster identity (server derives
  it from the token). Bump `SchemaVersion` on breaking changes.
- `internal/agent` — `Collector` lists workloads/pods/nodes and resolves pod ownership to the
  top-level workload (Pod→ReplicaSet→Deployment, Pod→Job→CronJob; pods owned by unknown kinds, e.g.
  operator CRDs, are grouped under that owner). Workload *templates* give the intended image (so
  scaled-to-0 workloads count); pods give what actually runs (several `Running` entries = rollout in
  progress). `Agent` pushes gzipped JSON with retry on 5xx/429/network errors, never on 4xx.
- `internal/store` — `Store` interface (clusters, user-defined ordered **stages**, **tenants**, catalog
  overrides, settings, history) + `Diff` (pure; history tracks *template* image changes only,
  not replicas/running pods, to avoid noise). First snapshot of a cluster is one `first_snapshot`
  event, not N "added". `internal/store/sqlite` — schema in `schema.sql`, versioned via
  `PRAGMA user_version`; single connection (SQLite single-writer). Migrations run with foreign keys off
  (needed to rebuild a referenced table) and `foreign_key_check` before commit. `clusters.stage/tenant`
  reference `stages`/`tenants` (rename cascades, delete in use fails).
- `internal/server` — HTTP API. Ingest authenticates by SHA-256 token hash (`internal/auth`), enforces
  compressed/decompressed size limits (413) and `SchemaVersion`. Middleware order: logRequests →
  securityHeaders → checkHost (localhost + public URL host + `OWL_ALLOWED_HOSTS`) → `authn` (loads the
  session cookie; every `/api` path except `publicAPI` needs one; `http.CrossOriginProtection` on writes)
  → mux. `requireAdmin` = session with role admin. Sign-in: `authn.go` (sessions, cookies, `clientIP` via
  trusted proxies, in-memory `limiter`), `signin.go` (login/logout/accept/forgot/password), `users.go`
  (admin user management, auth settings, test mail), `google.go` (OIDC + PKCE; ID token from the token
  endpoint, claims checked, no JWKS). Links: `LinkPath` puts the secret in the URL fragment.
- SQLite timestamps use a fixed-width layout so `ORDER BY` on the text column is chronological.
- `internal/catalog` — `catalog.yaml` (embedded) maps image names → component → GitHub repo /
  endoflife.date slug. `eol` is set **only when verified to be the same project** (endoflife.date
  `haproxy-ingress` is jcmoraisjr's, not haproxytech's). Exact name match beats mirror-suffix match.
  `ParseVersion` strips vendor suffixes (`-hardened3`, `-build…`, `-debian-12-r20`) but keeps real
  pre-releases (`-rc.1`). Tags with fewer parts than `versionParts` (e.g. `postgres:16`) are
  "unpinned (floating)". `source: cluster` components (kubernetes, rke2, k3s) come from the node
  version, not images. `bundledWith` (image → rke2|k3s|component id, built-in only) sets `Instance.BundledWith`
  (+ `BundledName`) — a distribution only in a cluster of it, a component (cilium-envoy → cilium) always; `ActionFor` then says "Comes with RKE2 — upgrade RKE2…" (level
  unchanged). Built-in entries may use `versionPattern` and OSV packages of any ecosystem.
- Custom catalog components (milestone 11): `catalog.ValidateCustom` / `Merge` (built-in always wins;
  `CustomState` says why one isn't in effect); stored as JSON in `custom_components` (**restored** with
  backups). The server keeps the merged catalog in `Server.live` (atomic pointer) — always read it via
  `s.cat()` (notifier/summary manager via `CatalogFn`), never `s.Catalog` (built-in only), and reload
  with `ReloadCatalog` after a change or restore. `versionPattern` = regexp, groups joined with ".".
  Summaries send `comp.GitHub` as the name for custom components (tested). `internal/registry` reads
  OCI labels anonymously from the public registries in `registry.Hosts` only, on demand.
- `internal/inventory` — computed on read from current state + catalog + catalog overrides (per
  repository: `inhouse`/`ignore` win over built-in matches — an upgrade must not undo them; a custom
  component beats them;
  `map` only for images the catalog doesn't recognize). `GET /catalog/overrides` adds `recognizedAs`. Server flag `--internal-registries` marks in-house images.
- Upstream refresh (`Server.RefreshUpstream`) covers only `runningComponents` (in some cluster's inventory)
  + custom ones; `kickFetch` (after a push with changes / a map override) → `FetchNew` in the refresher
  goroutine fetches components that started running. Components no longer running leave `s.upstream`.
- `internal/upstream` — `Fetcher` (cached GET/POST in `upstream_cache`, ETag revalidation, stale
  fallback) and `Client.Refresh` (endoflife.date lines, GitHub releases/archived, advisories).
  `AdvisoriesFor` matches versions against ranges **locally** for both OSV and GitHub advisories —
  see DESIGN.md "Security & lifecycle" for why OSV's version query must not be used and how
  open-ended/TBD ranges are treated. `GITHUB_TOKEN` (no scopes) comes from `.env` (gitignored).
- `internal/policy` — parse/resolve version policies to a target (line + version). `UsableLines`,
  `Position` and `IsEOL` are the single definition of "release line", shared with the scorer — don't
  re-implement line matching elsewhere.
- Admin endpoints (`internal/server/admin.go`: clusters, stages, tenants, overrides, settings, export)
  validate names strictly (they end up in YAML/kubectl) and reject anything but JSON bodies.
- Write endpoints require an admin session; JSON-only bodies + cross-origin protection. Users, sessions,
  auth_tokens and `auth*` settings are **never restored** from a backup (like the audit log); backups drop
  sessions/auth_tokens. Store guards: `ErrLastAdmin` (no demote/disable/delete of the last active admin).
- Personal API tokens (milestone 12): `api_tokens` table (hash, prefix, scope read|write, expiry; never
  in backups, never restored). `authn` → `tokenSession` (Bearer `owl_pat_…`; cookie + bearer = 400;
  failures per IP in `limits`, valid tokens never refused) builds a `store.Session` with `APIToken` set;
  `tokenRefused` uses the mux **route pattern**: reads allowed (not `/auth/*` except GET me), writes only
  from the `tokenWrites` allowlist (what the CLI does) — a new write route is closed to tokens until
  added there. Invite links are never returned to tokens. Password change / disable revoke API tokens
  (`revokeAccess`). `Session.CanWrite()` = admin role AND (no token OR write scope) — checked in
  `requireAdmin` too; `readOnlyPOST` lists POSTs that change nothing (`/users/plan`). Token secrets
  only via `POST /tokens` with a browser session. Expiry emails in `RunMaintenance`.
- Users from a file: `usersfile.go` — `planUsers` (pure) + `POST /users/plan|apply`; apply recomputes
  the plan, compares its id (`expect`), refuses self-demote/disable, and `Store.ApplyUserChanges` does
  everything in one tx with optimistic `Was*` checks (`ErrChanged`) and the last-admin check.
  `users.managed` marks file-managed users (no state file).
- CLI downloads: the image's `/cli` (`OWL_CLI_DIR`) has `owl-<os>-<arch>.gz` + `SHA256SUMS` (Dockerfile
  stage `cli`, BUILDPLATFORM); `GET /api/v1/cli[/{os}-{arch}]` streams them decompressed.
- `internal/auth` — agent tokens (SHA-256), API tokens (`NewAPIToken`, prefix `owl_pat_`), `NewSecret` (sessions/links), argon2id `HashPassword` /
  `VerifyPassword` (PHC string, ≤2 concurrent hashes, dummy hash for unknown users so timing doesn't
  reveal accounts). `internal/mail` — SMTP sender (TLS or STARTTLS required; `none` only loopback/Mailpit),
  header-injection-safe compose; tests use a fake `Sender`.
- `internal/backup` — `Manager`: daily/manual backups (`Store.Backup` = `VACUUM INTO`, gzip), upload to an
  S3-compatible `Remote` (minio-go; the server **never deletes** in the bucket), local retention (never
  deletes a copy not yet uploaded), restore with a safety backup first, status warnings. S3 config only
  from env `OWL_BACKUP_S3_*` (endpoint, bucket, prefix, region, access/secret key, insecure).
- `internal/logfile` — rotating log file (daily or 50 MB, gzip, retention days from the UI). Server logs JSON
  to stderr + `<db dir>/logs/server.log`; level from `OWL_LOG_LEVEL`, then the UI setting (slog.LevelVar).
  Request lines carry a request id (`X-Request-ID`); never log headers or tokens (tested).
- Audit log: `Server.audit` writes every admin action to the `audit_log` table (and the log); pruned daily
  to its retention. Restore keeps the audit log.
- `internal/summary` — Claude upgrade summaries (milestone 8). `Manager.generate` is the **single gate**
  for spending: mode → key → pause → exact token count (free endpoint) → input limit → worst-case price
  (input + max output, + fallback model) vs remaining daily/monthly cap → send → book actual cost;
  everything read under one lock so requests serialize. Unknown model price = refuses (server won't
  start). Default mode **off**. `Claude` is an interface (`claude.go` wraps the Go SDK, beta Messages
  with structured output + server-side refusal fallback); tests use a fake and must assert "no request
  sent" for off/estimate/budget/too-large/paused. Requests are built only from `Jump` (component +
  versions) and notes — never cluster data (tested). Output is untrusted: `parseContent` validates and
  sanitizes; links come only from Owl's `upstream.Note.URL`. `summaries`/`summary_requests` are kept
  across restores (spend counters must not reset).
- `internal/notify` — notifications (milestone 10). `Notifier.Check` diffs the report against stored state
  (`notify_*` tables, **not restored** from backups): Critical pairs, announced clusters (key = name +
  created_at), known unrecognized repos. First check ever / after a restore / before upstream loaded =
  quiet (records, never posts). State is tracked even when switches are off. Content is built once as a
  `Doc` and rendered to Mattermost Markdown / text / HTML; every span is escaped (Markdown, `@`-mentions,
  HTML); links only from `OWL_PUBLIC_URL`. Digest: `Settings.LastSlot` in the configured zone
  (`time/tzdata` embedded), sent if < 24 h late, never twice (slot stored), hourly retry; email per
  opted-in user once. Never calls Claude (stored summary headlines only). Webhook URL: env only, errors
  strip it (`*url.Error`). Tenant sections use `report.ComponentAction` on that tenant's instances.
  Posts happen **outside** `Notifier.mu`; a restore runs inside `WhileQuiet` (checks wait, then a quiet
  check). Pairs of a component whose upstream refresh had errors (`Incomplete`) are never resolved; pairs
  of a component gone from the catalog are dropped. Catalog upgrades: `catalogLearned` compares built-in
  ids with `notify_meta` `catalogBuiltins` (missing = `builtinsBeforeM13`); new ones already running are
  "learning" until `Fetched` && !`Incomplete` (snapshotted before the report is built; ≤ `learnFor` 24 h),
  then recorded quietly with one "Owl now recognizes N more" post; custom ids count as known.
- `internal/upstream/notes.go` — release notes per jump: GitHub bodies or `releaseNotes` sources
  (Kubernetes CHANGELOG sections, Redis/HAProxy per-branch files with pre-releases folded into x.y.0 and
  each version taken only from its own line's file, postgresql.org HTML). Which versions belong to a
  jump is decided in `summary.notesFor` via `policy.Position` (upstream can't import policy).
- `internal/report` — `ScoreInstance` is the **only** place priority levels are decided (pure,
  table-tested in `score_test.go`); `ActionFor` derives the "Recommended action" sentence from a score
  (never the reverse). `Build` combines inventory + upstream + policies + drift: per **tenant**, along
  the configured stage order; entries carry the clusters involved so the UI hides drift between
  clusters that aren't selected; `only_in` is info, not an anomaly.
- `internal/docs` — built-in docs (DESIGN.md "Docs in the app"): `render` (goldmark, build time only — never
  imported by the server) turns `docs/*.md` into HTML + a search index, `docsgen` writes them to `dist/`
  (gitignored), `docs.Dist()` embeds them; served signed-in at `/api/v1/docs[/{page}]` (`server/docs.go`).
  A new page in `docs/` must go into `render.Order` or `Excluded`; raw HTML, images and broken anchors fail
  the build (and `TestRealDocsBuild`). UI: `pages/Docs.tsx`; "?" targets in `web/src/dochelp.ts` (tested).
- `web/` — Vite + React + TS UI. `App` gates on `session` (`api.ts`: `useMe`/`useIsAdmin`; any 401 signs
  the UI out); `/invite#…` and `/reset#…` (`pages/Login.tsx`) work signed in or not; Settings → Users /
  Account in `pages/SettingsUsers.tsx`. Logo = `Icon.logo` (even-odd path) + `web/public/favicon.svg`,
  accent #574CF9. Sidebar layout; cluster picker (Overview + Changes only) keeps its
  selection in `?clusters=`; `model.tsx` (selection, notes), `clusterview.ts` (workloads per cluster).
  Fonts bundled via `@fontsource-variable/*`. `web/embed.go` embeds `web/dist` (`.keep` keeps the dir for
  `go build` without a UI build; the build script recreates it). Server serves it with SPA fallback
  and a strict CSP (`default-src 'self'`) — don't add inline scripts/styles or third-party origins.
  `web/src/api.ts` mirrors the Go JSON types; keep them in sync when changing API structs. The session
  cookie is HttpOnly (scripts never see it).
- SQLite migrations: append-only list in `sqlite.go` (`PRAGMA user_version`); never edit a shipped one.
- Agent refuses `http://` server URLs unless `OWL_ALLOW_INSECURE_HTTP=true` (local dev only).
- `dev/stack-up.sh` — server container joins each `k3d-<name>` Docker network; agents use that IP.
  Also runs S3Mock and Mailpit; invites the first admin when there are no users.
- `deploy/compose` — production on one host: Owl + Caddy (Let's Encrypt), `.env` from `owl.env.example`,
  Caddy at fixed IP 172.30.80.10 = `OWL_TRUSTED_PROXIES`. `deploy/server` — in-cluster StatefulSet +
  example Ingress.
- `deploy/agent/agent.yaml` — namespace, SA, read-only ClusterRole, Deployment (no token). Embedded
  via `deploy/embed.go` and served at `/api/v1/agent/manifest` with the configured image. The
  ClusterRole must match exactly what `collector.go` lists. The token Secret is generated in the
  browser (never in a URL) — `agentSecretYAML` in `web/src/api.ts`.
- `dev/seed/seed.sh` — per-cluster version tables; heavy components are scaled to 0.

## Invariants (easy to violate, hard to notice)
- **Agent is read-only and push-only.** Its RBAC must never include Secrets or any write verb; the
  server must never connect into clusters or store kubeconfigs.
- **Claude only receives public data**: component name, version jump, upstream release notes. Never
  send cluster names, namespaces, hostnames, workload names, configs or tokens to the Claude API.
- **Priority levels are rule-based and deterministic** (see DESIGN.md "Priority"). Claude explains a
  level; it never sets one.
- **Agent tokens, API tokens, sessions and invite/reset links are stored hashed** and shown only once at creation.
- **Every API path needs a session** (browser cookie or personal API token) unless it's in `publicAPI`
  (authn.go) — adding a public path is a security decision. Never add an auth-bypass flag. A token can
  never do more than its owner, and never manage sign-in or tokens.
- Keep the agent lightweight (small static binary, low memory) — avoid pulling heavy deps into the
  agent code path.
- Storage goes through a repository interface so SQLite can be swapped for Postgres.

## Local environment
- Three k3d clusters named like the real ones: `it-hetzner` (staging), `it-aws-test` (prelive),
  `it-aws-prod` (live). Kube contexts are `k3d-<name>`. These are the only clusters to touch from
  this repo during development.
- Server runs in Docker on the host; agents in the k3d clusters push to it.
