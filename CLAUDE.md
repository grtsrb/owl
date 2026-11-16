# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is
Service Inventory: tracks third-party components running in Kubernetes clusters (RKE2), compares them
with upstream releases / EOL / advisories, scores upgrade priority, and uses Claude to summarize
version jumps. **`DESIGN.md` is the source of truth for decisions** — read it before non-trivial work,
and update it when a decision changes.

## Status
Milestones 1–6.5 (agent, server core, catalog, upstream data, policy/scoring/drift, web UI, UI
redesign + cluster management) done; next 7 (backups/restore/logging/audit). Plan at the bottom of `DESIGN.md`. Keep this file's commands and
layout in sync as milestones land.

## Commands
```bash
make build test lint        # bin/inv, go test ./..., go vet + gofmt check
make ui                     # build web/ (React) into web/dist, embedded by the next `make build`
make ui-dev                 # Vite dev server :5173 with /api proxied to :8080 (needs make stack-up)
cd web && npm run typecheck # TypeScript check only
go test ./internal/agent -run TestCollect -v    # single test
make dev-up                 # create the 3 k3d clusters (different k3s versions) + seed them
make seed                   # re-apply seed workloads (dev/seed/seed.sh — bash 3.2 compatible)
make agent-print CLUSTER=it-aws-prod   # what the agent would send, as JSON (uses k3d-<name> context)
make image                  # Docker image (same image for agent and server)
make stack-up               # server in Docker (127.0.0.1:8080) + agent in every k3d cluster
docker exec inventory-server /inv cluster list         # registered clusters, last seen
curl -s 127.0.0.1:8080/api/v1/changes?cluster=it-hetzner  # change history
curl -s 127.0.0.1:8080/api/v1/inventory                   # components x clusters, unpinned, unrecognized
curl -s 127.0.0.1:8080/api/v1/components/coredns          # + upstream lines/EOL + advisories per version
./bin/inv upstream --component redis --version 7.2.4      # debug upstream data (needs GITHUB_TOKEN)
curl -s 127.0.0.1:8080/api/v1/report                      # levels + reasons + targets + drift
set -a; . ./.env; set +a   # GITHUB_TOKEN, INV_ADMIN_TOKEN (stack-up generates the admin token once)
curl -s -X PUT -H 'Content-Type: application/json' -H "Authorization: Bearer $INV_ADMIN_TOKEN" \
  -d '{"policy":"pin:12.3"}' 127.0.0.1:8080/api/v1/policies/grafana
make dev-down
```

## Layout / architecture
- `cmd/inv` — single binary: `agent`, `server`, `cluster add|list|rotate-token|delete` (operator
  fallback that works directly on the DB file; prints the agent token once — the UI does the same via
  Settings → Clusters) and `upstream` (debug:
  print upstream lines/advisories for a component, optionally for one `--version`).
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
  compressed/decompressed size limits (413) and `SchemaVersion`. Read endpoints are unauthenticated
  until milestone 8 and only answer allowlisted `Host` headers (localhost + `INV_ALLOWED_HOSTS`) to
  block DNS rebinding — don't bind the server to a public interface before auth lands.
- SQLite timestamps use a fixed-width layout so `ORDER BY` on the text column is chronological.
- `internal/catalog` — `catalog.yaml` (embedded) maps image names → component → GitHub repo /
  endoflife.date slug. `eol` is set **only when verified to be the same project** (endoflife.date
  `haproxy-ingress` is jcmoraisjr's, not haproxytech's). Exact name match beats mirror-suffix match.
  `ParseVersion` strips vendor suffixes (`-hardened3`, `-build…`, `-debian-12-r20`) but keeps real
  pre-releases (`-rc.1`). Tags with fewer parts than `versionParts` (e.g. `postgres:16`) are
  "unpinned (floating)". `source: cluster` components (kubernetes, rke2, k3s) come from the node
  version, not images.
- `internal/inventory` — computed on read from current state + catalog + catalog overrides (map /
  inhouse / ignore per repository, applied **only to images the catalog doesn't recognize**). Server flag `--internal-registries` marks in-house images.
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
- Write endpoints require `INV_ADMIN_TOKEN` (bearer, constant-time compare, ≥24 chars); unset = writes
  disabled. Interim until Google sign-in; also JSON-only (cross-site form posts can't write).
- `internal/report` — `ScoreInstance` is the **only** place priority levels are decided (pure,
  table-tested in `score_test.go`); `ActionFor` derives the "Recommended action" sentence from a score
  (never the reverse). `Build` combines inventory + upstream + policies + drift: per **tenant**, along
  the configured stage order; entries carry the clusters involved so the UI hides drift between
  clusters that aren't selected; `only_in` is info, not an anomaly.
- `web/` — Vite + React + TS UI. Sidebar layout; cluster picker (Overview + Changes only) keeps its
  selection in `?clusters=`; `model.tsx` (selection, notes), `clusterview.ts` (workloads per cluster).
  Fonts bundled via `@fontsource-variable/*`. `web/embed.go` embeds `web/dist` (`.keep` keeps the dir for
  `go build` without a UI build; the build script recreates it). Server serves it with SPA fallback
  and a strict CSP (`default-src 'self'`) — don't add inline scripts/styles or third-party origins.
  `web/src/api.ts` mirrors the Go JSON types; keep them in sync when changing API structs. The admin
  token is held in sessionStorage only and sent only on writes.
- SQLite migrations: append-only list in `sqlite.go` (`PRAGMA user_version`); never edit a shipped one.
- Agent refuses `http://` server URLs unless `INV_ALLOW_INSECURE_HTTP=true` (local dev only).
- `dev/stack-up.sh` — server container joins each `k3d-<name>` Docker network; agents use that IP.
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
- **Agent tokens are stored hashed** and shown only once at creation.
- Keep the agent lightweight (small static binary, low memory) — avoid pulling heavy deps into the
  agent code path.
- Storage goes through a repository interface so SQLite can be swapped for Postgres.

## Local environment
- Three k3d clusters named like the real ones: `it-hetzner` (staging), `it-aws-test` (prelive),
  `it-aws-prod` (live). Kube contexts are `k3d-<name>`. These are the only clusters to touch from
  this repo during development.
- Server runs in Docker on the host; agents in the k3d clusters push to it.
