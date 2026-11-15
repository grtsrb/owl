# Service Inventory — Design

Tracks third-party components running in our Kubernetes clusters, shows what is outdated / EOL /
vulnerable / retired, and uses Claude to explain what an upgrade involves.

Status: agreed design for v1 (2026-11-15). Changes to decisions below should update this file.

## Users & scope
- Personal tool first, team-ready design. DevOps operates it; **IT** and **DS** are tenants (filter only).
- v1 tracks: third-party components in Kubernetes + each cluster's RKE2/Kubernetes version.
- Initial catalog: Grafana, ingress-nginx (controller), nginx (plain), CoreDNS, Longhorn, Redis,
  Postgres, Redpanda, HAProxy, HAProxy Kubernetes Ingress — extensible.
- Out of v1: Docker hosts, Git repo scanning, managed cloud services, version scoring of in-house
  services (listed only), Helm release data (opt-in later — needs Secret read access).

## Clusters
| name | stage | tenant | where |
|---|---|---|---|
| it-hetzner | staging | IT | Hetzner (RKE2) |
| it-aws-test | prelive | IT | AWS (RKE2) |
| it-aws-prod | live | IT | AWS (RKE2) |
| ds (later) | — | DS | — |

Stage order `staging → prelive → live` drives promotion-drift detection.

## Architecture
One Go binary, two modes; React UI embedded via `go:embed`.

```
cluster (each)                               server (Docker locally, later one EC2 instance)
┌───────────────────────┐   HTTPS POST      ┌──────────────────────────────────────────────┐
│ inv agent             │  Bearer <token>   │ ingest API ── SQLite (Postgres later)         │
│ read-only RBAC:       │ ────────────────▶ │ upstream checker (GitHub, endoflife.date, OSV)│
│ workloads, pods, nodes│  every ~15 min    │ policy + scoring + drift                      │
│ (no Secrets)          │                   │ Claude summaries (cached per version jump)    │
└───────────────────────┘                   │ React UI, CLI API, Mattermost digest/alerts   │
                                            └──────────────────────────────────────────────┘
```

- **Agent**: lists Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs/CronJobs, Pods, Nodes.
  Reads images from workload templates *and* running pods (actual image ID), so half-done rollouts
  are visible and scaled-to-0 workloads still count. Sends a full snapshot each time (idempotent).
- **Auth**: one random token per cluster, created on the server, shown once, stored hashed.
  The server never connects into clusters and holds no kubeconfigs. Revoke = delete token.
- **Storage**: SQLite behind a repository interface. Keep change events (history), not every snapshot.

## Detection
- Image → component via built-in catalog: image patterns → component → upstream GitHub repo →
  endoflife.date product. Catalog extensible from the UI.
- Vendor builds mapped back to upstream versions, exact image still shown:
  `rancher/hardened-coredns:v1.11.1-build20240305` → CoreDNS 1.11.1,
  `rancher/nginx-ingress-controller:v1.10.4-hardened3` → ingress-nginx 1.10.4,
  `bitnami/redis:7.2.4-debian-12-r3` → Redis 7.2.4.
- RKE2/Kubernetes version per cluster from node `kubeletVersion` (e.g. `v1.30.4+rke2r1`).
- `:latest` / digest-only / non-version tags → **⚠ unpinned** (Medium).
- Unknown images → "unrecognized" list, mappable from the UI.

## Versions & policy
- Three targets per component: latest patch (current line), latest minor, latest major. Stable only.
- **Policy presets**: `lts`, `latest`, `n-1`, `n-2`, `pin:<version>`.
  Default: LTS if the product has one, otherwise `n-1` (previous release line, latest patch).
  "Release line" = endoflife.date cycle when available, otherwise minor version.
- Set globally or per component **in the UI** (stored in DB). Per-stage policies: v2.

## Security & lifecycle
- Advisories: OSV.dev (Go modules: kubernetes, rke2, k3s, ingress-nginx, grafana, coredns) or the
  GitHub repo's published security advisories → "affected, fixed in X". Blind spot: vulns in bundled
  OS packages/libs (later: ingest Trivy Operator reports).
  - **Ranges are evaluated locally**, never via OSV's per-version query: for modules whose tags aren't
    Go module versions (ingress-nginx `controller-v1.x`, Grafana) that query returns every advisory
    regardless of version (verified: latest ingress-nginx "had" CVE-2025-1974).
  - Open-ended repo ranges (`>= 7.0.0`, `All`) are decided by the patched list (one fix per release
    line). Open range + `TBD`/no patch → **needs review**, not affected (Redis rarely closes ranges).
  - Affected with no fixed version → shown as "no fix listed", not counted for priority.
  - Repos that publish no GitHub advisories (postgres, nginx, haproxy, haproxytech ingress,
    metrics-server) → **"no advisory data"** — never shown as "no vulnerabilities".
- EOL dates, LTS flags and latest patch per release line from endoflife.date (verified slugs only);
  otherwise lines are derived from GitHub releases per minor (tag filter, pre-releases skipped).
- **Retired** = GitHub repo archived *and* no release for 90 days (ingress-nginx). Archived but still
  releasing (Redpanda) = "archived — check where development moved", not Critical.
- Upstream data refreshes every 6 h, cached in SQLite with ETags; on upstream failure the last good
  copy is served (marked stale).

## Priority (rule-based; Claude explains, never decides)
- 🔴 Critical — affected by a high/critical advisory *with a fix*, past EOL, or upstream retired
- 🟠 High — EOL within 90 days, or ≥ 2 release lines behind target
- 🟡 Medium — 1 release line behind target, unpinned, or a moderate/unknown-severity advisory
- 🟢 Low — only patch updates available (or a low-severity advisory)
- ✅ OK — at/above target and on the latest patch of its line
- ❔ Unknown — no upstream release data (never shown as OK)

"Needs review" advisories, advisories with no fix listed, "no advisory data" and archived-but-
releasing projects are **notes**: shown with the instance, never change the level. Every level comes
with its reasons (e.g. "CVE-2025-1974 (critical), fixed in 1.11.5", "release line 1.33 is EOL since
2026-06-28"). A component's level is its worst instance.

"Lines behind" counts release lines as upstream defines them: minors for Kubernetes, but Grafana and
Redpanda ship a minor roughly monthly, so n-1 is strict there — use per-component policies.

Policies (`internal/policy`): `lts-or-n-1` (default), `lts` (falls back to n-1), `latest`, `n-1`,
`n-2`, `pin:<line or version>`; stored per scope (`global` or component id), editable via
`PUT /api/v1/policies/{scope}` (JSON only — a cross-site form post can't change them).

Promotion drift across stages: "newer in staging, not yet in prod" and anomalies like "prod newer than staging".

## Claude
- Sees public data only: component name, version jump, upstream release notes. No cluster names,
  hostnames, configs or secrets. (Sending own config may become an opt-in later, after approval.)
- One summary per version jump (e.g. cert-manager 1.13 → 1.18), cached:
  breaking changes, migrations, deprecations, notable features, risk (low/med/high + reason),
  upgrade steps, and whether the jump can be direct or needs intermediate versions.
- Optional: without an API key everything else still works.

## Interfaces
- React dashboard; CLI (`inv scan`, `inv outdated`, …).
- Mattermost: weekly digest (Mon 09:00: counts per level, top 5 actions with summaries, drift) +
  instant alert when something *newly* becomes Critical. Google Chat/email later.
- Auth v1: single admin password (local: none). v2: Google sign-in, viewer/admin roles.
- v2: MCP server to query the inventory from Claude.

## Upstream access
- GitHub API with a token that has no scopes (public read); release lists cached ~6 h.

## Local development
- 3 k3d clusters on the dev Mac named `it-hetzner`, `it-aws-test`, `it-aws-prod`, seeded with
  different old versions. Longhorn & Redpanda as scaled-to-0 stand-ins (run for real only if needed).
- Server in Docker on the host; agents in the clusters push to it.
- Deployment artifacts: plain YAML manifests (matches current workflow; no CI yet).
- Repo: local now, later pushed to GitLab (possibly self-hosted on the mac mini).

## Milestones (v1)
1. **Skeleton + agent**: Go module, snapshot model, agent collector, local k3d clusters + seed manifests.
2. **Server core**: ingest API, per-cluster tokens, SQLite store, change history.
3. **Catalog & parsing**: image → component mapping, vendor tag parsing, unpinned/unrecognized.
4. **Upstream data**: GitHub releases, endoflife.date, OSV/GHSA advisories, retired detection, caching.
5. **Policy, scoring, drift**.
6. **React UI** (embedded).
7. **Claude summaries**.
8. **Notifications, CLI, auth, packaging**: Mattermost digest/alerts, admin login, Dockerfile, manifests.
