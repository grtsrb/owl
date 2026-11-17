# Owl

Owl keeps track of the third-party software running in your Kubernetes clusters — Grafana,
ingress-nginx, CoreDNS, Redis, Postgres, Longhorn, Kubernetes/RKE2 itself and more — and tells you
what to upgrade first and why.

- A tiny **read-only agent** in each cluster reports which images run where (no Secrets, no write
  access, push-only).
- The **server** maps images to upstream projects, checks release lines, end-of-life dates and
  security advisories, applies your version policy and gives every component a **priority level**
  with plain-language reasons ("CVE-2025-1974 (critical), fixed in 1.11.5").
- It shows **drift between stages** ("newer in staging, not yet in prod"), keeps a **change history**,
  and backs itself up to S3.
- Optionally, **Claude summarizes the release notes** of each upgrade you need — breaking changes,
  migrations, security fixes — from public data only, within spending caps you set.

*Named after Owl from Winnie-the-Pooh: the one who knows things — and is occasionally wrong, which is
why Owl always shows its reasons.*

## Quick start (local, with demo clusters)

Needs Docker, [k3d](https://k3d.io), kubectl, Go 1.27+ and Node 24+.

```bash
echo 'GITHUB_TOKEN=<a GitHub token with no scopes>' > .env   # optional, avoids rate limits
make dev-up      # 3 local k3d clusters with seeded workloads
make stack-up    # server on http://127.0.0.1:8080 + an agent in every cluster
```

Open <http://127.0.0.1:8080>. To change things in the UI, paste `OWL_ADMIN_TOKEN` from `.env`
into **Settings → Admin token**.

## Documentation

| | |
|---|---|
| [Concepts](docs/concepts.md) | Components, release lines, policies, priority levels, drift — how Owl decides |
| [User guide](docs/user-guide.md) | A tour of the UI, page by page |
| [Installation](docs/install.md) | Run the server, connect clusters, first-time setup |
| [Configuration](docs/configuration.md) | Every flag, environment variable and UI setting |
| [Operations](docs/operations.md) | Backups, restore, logs, audit log, upgrades, monitoring |
| [Security](docs/security.md) | What Owl can and cannot see, tokens, hardening |
| [Catalog](docs/catalog.md) | How images are recognized; adding components |
| [CLI](docs/cli.md) | `owl agent`, `owl server`, `owl cluster`, `owl upstream` |
| [HTTP API](docs/api.md) | Endpoint reference |
| [Troubleshooting](docs/troubleshooting.md) | Common problems and fixes |
| [Development](docs/development.md) | Local setup, tests, conventions, architecture |
| [Changelog](docs/changelog.md) | What each milestone shipped |

Design decisions and the roadmap live in [DESIGN.md](DESIGN.md).

## Status

Pre-1.0, used internally. Milestones 1–8 are done (agent, server, catalog, upstream data, scoring,
UI, cluster management, backups/logging/audit, Claude upgrade summaries — off by default, with spending
caps). Next: notifications, CLI reports, sign-in and packaging (9). **Until sign-in lands, don't expose the
server to the internet** — see [Security](docs/security.md).
