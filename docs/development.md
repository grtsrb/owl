# Development

How to work on Owl. `DESIGN.md` is the source of truth for decisions; `CLAUDE.md` has the short
command list and architecture notes used by Claude Code.

## Tools

Go 1.27+, Node 24+ (npm), Docker, [k3d](https://k3d.io), kubectl, openssl. macOS or Linux (the
scripts are bash 3.2 compatible for macOS).

## Local environment

Three k3d clusters named like the real ones, each with a different k3s version so drift and EOL show up:

| Cluster | Stage | k3s | Kube context |
|---|---|---|---|
| `it-hetzner` | staging | v1.36.4 | `k3d-it-hetzner` |
| `it-aws-test` | prelive | v1.35.0 | `k3d-it-aws-test` |
| `it-aws-prod` | live | v1.33.6 | `k3d-it-aws-prod` |

These are the **only** clusters to touch from this repository during development.

```bash
make dev-up          # create the clusters (doesn't switch your kube context) and seed them
make seed            # re-apply seed workloads (dev/seed/seed.sh: per-cluster version tables)
make stack-up        # build the image, run server + S3 test server + Mailpit in Docker, agents in every cluster
make stack-down      # remove server, S3, Mailpit and agents (keeps the owl-data volume)
make dev-down        # delete the clusters
make mattermost-up   # optional: throwaway Mattermost (≈1 GB, amd64 image) with a webhook; then make stack-up
make mattermost-down # remove it, its data and dev/.mattermost.env
```

`make mattermost-up` runs `mattermost/mattermost-preview` as `owl-mattermost` on <http://127.0.0.1:8065>
(sign in as `owladmin` / `Owl-dev-only-1234`, channel "Owl alerts"), creates an incoming webhook and
writes its URL to `dev/.mattermost.env` (gitignored), which `make stack-up` passes to the server.

`make stack-up`:
- runs `owl-server` on `127.0.0.1:8080` with volume `owl-data`, `owl-s3` (adobe/s3mock — accepts
  any credentials, so the "no delete" permission can only be tested on real S3) and `owl-mailpit`
  (catches every email; inbox at <http://127.0.0.1:8025>),
- on the first run (no users yet) invites `admin@owl.local` (or `$OWL_DEV_ADMIN`) as admin and prints
  the link — open it and set a password. Lost it? `docker exec owl-server /owl user reset-link --email
  admin@owl.local`,
- passes `OWL_GOOGLE_CLIENT_ID/SECRET/DOMAIN` from `.env` if set (a Google client with redirect URI
  `http://127.0.0.1:8080/auth/google/callback` works locally — Google allows http for loopback),
- joins the server to each `k3d-<name>` Docker network; agents use that IP over plain HTTP
  (`OWL_ALLOW_INSECURE_HTTP=true`),
- rotates the agents' tokens on every run.

Heavy components (Longhorn, Redpanda) are seeded scaled to 0 — Owl still counts them.

## Build and test

```bash
make build test lint            # bin/owl, go test ./..., go vet + gofmt check
go test -race ./...             # before merging
go test ./internal/agent -run TestCollect -v
make ui                         # build web/ into web/dist (embedded by the next make build)
make ui-dev                     # Vite on :5173, /api proxied to :8080 (needs make stack-up)
cd web && npm run typecheck
make agent-print CLUSTER=it-aws-prod   # what the agent would send
```

Useful while developing:

```bash
set -a; . ./.env; set +a
docker exec owl-server /owl cluster list
curl -s 127.0.0.1:8080/api/v1/report | jq '.summary'
./bin/owl upstream --component redis --version 7.2.4
docker logs -f owl-server
```

## Repository layout

```
cmd/owl/                 the binary: agent, server, cluster, upstream commands
internal/snapshot/       wire contract agent → server (bump SchemaVersion on breaking changes)
internal/agent/          collector (lists workloads/pods/nodes) and pusher
internal/auth/           token generation and hashing
internal/store/          Store interface, Diff (change history); sqlite/ = implementation + migrations
internal/catalog/        catalog.yaml, image matching, version parsing
internal/inventory/      components × clusters, computed on read
internal/upstream/       GitHub / endoflife.date / OSV fetcher with cache, advisory matching
internal/policy/         policies → targets; the one definition of "release line"
internal/report/         scoring (the only place levels are decided), actions, drift
internal/server/         HTTP API, admin, ops (backups/logs/audit endpoints)
internal/backup/         backup manager, S3 remote
internal/logfile/        rotating log file
internal/notify/         Mattermost digest and alerts, digest email (state in notify_* tables)
web/                     React + TypeScript UI (Vite), embedded via web/embed.go
deploy/agent/agent.yaml  agent manifest (embedded; served at /api/v1/agent/manifest)
dev/                     local stack and seed scripts
docs/                    this documentation
```

## Data flow

```
agent ──POST /api/v1/snapshots──▶ ingest ──▶ store (current images + change events)
                                                   │
upstream refresher (6 h) ──▶ upstream cache ───────┤
                                                   ▼
            GET /report ◀── report.Build: inventory + upstream + policies ─▶ ScoreInstance → level
                                                                          ─▶ ActionFor   → action
                                                                          ─▶ drift per tenant
```

Inventory and report are computed **on read** from current state; nothing derived is stored.

## Conventions and invariants

These are easy to break and hard to notice — tests cover most of them.

- **Agent is read-only and push-only.** Its RBAC never includes Secrets or a write verb; the
  ClusterRole in `deploy/agent/agent.yaml` must match exactly what `collector.go` lists. The server
  never connects into clusters.
- **Keep the agent small**: no heavy dependencies in its code path.
- **Levels are decided only in `report.ScoreInstance`** (pure, table-tested in `score_test.go`).
  `ActionFor` derives the action from the score, never the reverse. Claude (milestone 8) explains
  levels; it never sets one.
- **Release-line logic lives in `internal/policy`** (`UsableLines`, `Position`, `IsEOL`) and is
  shared with the scorer — don't re-implement it.
- **Advisory ranges are matched locally** (see [Concepts → Advisories](concepts.md#advisories)).
- **Catalog `eol` only when verified** to be the same project.
- **Claude gets only public data** (component, version jump, release notes).
- **Tokens**: generated server-side, shown once, stored hashed; never in URLs or logs.
- **Storage goes through `store.Store`** so SQLite can be swapped for Postgres.
- **Migrations are append-only** (`internal/store/sqlite/sqlite.go`, `PRAGMA user_version`); never
  edit one that has shipped. They run with foreign keys off and a `foreign_key_check` before commit.
- **Timestamps** in SQLite use a fixed-width layout so text ordering is chronological.
- **UI**: no inline scripts/styles and no third-party origins (strict CSP); fonts are bundled.
  `web/src/api.ts` mirrors the Go JSON types — update both together. Admin-only `GET`s must pass the
  admin flag to `request()`.
- **Admin endpoints** validate names strictly, accept JSON only, and call `s.audit(...)`.
- **Logs never contain** headers, query strings or tokens.

## Workflow

Each milestone:

1. Design in `DESIGN.md` (agreed before coding).
2. Its own branch (`milestone-N`).
3. Build, test, run it against the local stack.
4. Code review, fix every valid finding.
5. **Update the docs** (see below), `CLAUDE.md` and the status in `DESIGN.md`.
6. Merge to `main`.

## Keeping the docs current

The docs describe what's on `main`. With every change that users or operators can notice:

| You changed | Update |
|---|---|
| a flag, environment variable or UI setting | [configuration.md](configuration.md) |
| an endpoint or JSON shape | [api.md](api.md) and `web/src/api.ts` |
| scoring, policies, notes, drift | [concepts.md](concepts.md) (and DESIGN.md) |
| a UI page | [user-guide.md](user-guide.md) |
| backups, logs, audit, background jobs | [operations.md](operations.md) |
| anything about tokens, RBAC, exposure, what leaves the server | [security.md](security.md) |
| the catalog | [catalog.md](catalog.md) |
| a CLI command | [cli.md](cli.md) |
| a new error people will hit | [troubleshooting.md](troubleshooting.md) |
| a milestone is merged | [changelog.md](changelog.md), README status |
