# <img src="docs/owl-logo.svg" alt="" width="36" valign="middle"> Owl

Owl keeps track of the third-party software running in your Kubernetes clusters — Grafana,
ingress-nginx, CoreDNS, Redis, Postgres, Longhorn, Kubernetes/RKE2 itself and more — and tells you
what to upgrade first and why.

- A tiny **read-only agent** in each cluster reports which images run where (no Secrets, no write
  access, push-only).
- The **server** maps images to upstream projects, checks release lines, end-of-life dates and
  security advisories, applies your version policy and gives every component a **priority level**
  with plain-language reasons ("CVE-2025-1974 (critical), fixed in 1.11.5").
- It **scans the running images with Trivy** on the server — OS packages and libraries, including your
  in-house images — as a separate signal next to the priority levels.
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

`make stack-up` prints a one-time link for the first admin (`admin@owl.local`): open it and choose a
password. Emails Owl sends locally land in Mailpit at <http://127.0.0.1:8025>.

For a real installation (Docker Compose with automatic HTTPS on one host, or inside a cluster), see
[Installation](docs/install.md).

## Documentation

| | |
|---|---|
| [Concepts](docs/concepts.md) | Components, release lines, policies, priority levels, drift — how Owl decides |
| [User guide](docs/user-guide.md) | A tour of the UI, page by page |
| [Installation](docs/install.md) | Run the server, connect clusters, first-time setup |
| [Configuration](docs/configuration.md) | Every flag, environment variable and UI setting |
| [Operations](docs/operations.md) | Backups, restore, logs, audit log, upgrades, monitoring |
| [Security](docs/security.md) | Sign-in, what Owl can and cannot see, tokens, hardening |
| [Catalog](docs/catalog.md) | How images are recognized; adding components |
| [CLI](docs/cli.md) | The `owl` command line: tokens, reports, clusters, users, server-side commands |
| [HTTP API](docs/api.md) | Endpoint reference |
| [Troubleshooting](docs/troubleshooting.md) | Common problems and fixes |
| [Development](docs/development.md) | Local setup, tests, conventions, architecture |
| [Changelog](docs/changelog.md) | What each milestone shipped |

The same documentation (all but Development) is built into Owl: **Docs** in the sidebar, always for the
version you run. Design decisions and the roadmap live in [DESIGN.md](DESIGN.md).

## Status

Pre-1.0, used internally. Milestones 1–12 are done (agent, server, catalog, upstream data, scoring,
UI, cluster management, backups/logging/audit, Claude upgrade summaries — off by default, with spending
caps — invite-only sign-in with passwords or Google, email, Docker Compose/Kubernetes packaging, and
Mattermost notifications: weekly digest and alerts, adding catalog components from the UI, and the
`owl` CLI with personal API tokens and users from a file). Milestone 13 — a much longer built-in
catalog of common infrastructure tools — is under way. v2 has started: the agent has its own slim image
(milestone 14) and the server scans running images with Trivy (milestone 15); next are Docker hosts
(roadmap in DESIGN.md).
