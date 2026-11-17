# Catalog

The catalog tells Owl which images belong to which upstream project and where to look up releases,
end-of-life dates and advisories. It's `internal/catalog/catalog.yaml`, compiled into the binary.

## Built-in components

| id | Component | Release lines from | Advisories from |
|---|---|---|---|
| `kubernetes` | Kubernetes *(from nodes)* | endoflife.date | OSV |
| `rke2` | RKE2 *(from nodes)* | GitHub releases | OSV |
| `k3s` | k3s *(from nodes)* | GitHub releases | OSV |
| `ingress-nginx` | ingress-nginx controller | GitHub releases (`controller-` tags) | OSV |
| `nginx` | nginx | endoflife.date | none published |
| `haproxy` | HAProxy | endoflife.date | none published |
| `haproxy-kubernetes-ingress` | HAProxy Kubernetes Ingress (HAProxy Technologies) | GitHub releases | none published |
| `haproxy-ingress` | HAProxy Ingress (jcmoraisjr) | endoflife.date | GitHub |
| `grafana` | Grafana | endoflife.date | OSV |
| `coredns` | CoreDNS | GitHub releases | OSV |
| `longhorn` | Longhorn | endoflife.date | GitHub |
| `redis` | Redis | endoflife.date | GitHub |
| `valkey` | Valkey | endoflife.date | GitHub |
| `postgresql` | PostgreSQL | endoflife.date | none published |
| `redpanda` | Redpanda | GitHub releases | GitHub |
| `metrics-server` | metrics-server | GitHub releases (`v` tags) | none published |

"None published" means the project doesn't publish GitHub security advisories; Owl shows **no
advisory data** for it rather than implying it's clean.

## Format

```yaml
components:
  - id: postgresql                    # stable id: used in URLs, policies and overrides
    name: PostgreSQL
    images: [postgres, bitnami/postgresql, bitnamilegacy/postgresql]
    github: postgres/postgres         # releases, archived status, advisories
    eol: postgresql                   # endoflife.date slug — only if it's verified to be the same project
    versionParts: 2                   # 17.2 is fully pinned; "postgres:17" is floating
```

| Field | Meaning |
|---|---|
| `id`, `name` | Identifier and display name. Never change an `id` once used: policies and overrides refer to it. |
| `images` | Image names without tag: `registry/repository`, or a bare repository for Docker Hub (`redis` = `docker.io/library/redis`). Mirrors are matched by path suffix. |
| `github` | `owner/repo` for releases, archived/retired detection and repository advisories. |
| `eol` | endoflife.date product slug. **Set only when verified to be the same project** — e.g. endoflife.date `haproxy-ingress` is jcmoraisjr's project, not HAProxy Technologies'. |
| `versionParts` | How many numeric parts a pinned version has (default 3). Tags with fewer parts are "unpinned (floating)". |
| `tagPrefix` | Prefix stripped from image tags before parsing (`controller-v1.10.4`). |
| `releaseTagPrefix` | Only GitHub release tags with this prefix count (repositories that release several things). |
| `osv` | `{ecosystem: Go, name: <module>}` — use OSV.dev for advisories instead of the repository's. |
| `source: cluster` | Not matched by image: the version comes from the cluster's nodes. |
| `notes` | Shown with the component. |
| `releaseNotes` | Where [upgrade summaries](concepts.md#upgrade-summaries) read release notes when the GitHub releases don't carry them: `kubernetes-changelog`, `redis-releasenotes`, `haproxy-changelog`, `postgres-release-notes`. Empty = GitHub release notes. A new source needs code in `internal/upstream/notes.go`. |

`releaseTagPrefix` is also stripped before reading a version from a GitHub release tag (nginx tags
releases `release-1.27.4`).

## How tags become versions

- A leading `v` is dropped; vendor suffixes are stripped: `-hardened3`, `-build20240305`,
  `-debian-12-r20`, `-alpine` … (`rancher/hardened-coredns:v1.11.1-build20240305` → 1.11.1).
- Real pre-releases are kept (`1.12.0-rc.1`).
- `latest`, digests only, non-version tags and too few version parts → **unpinned**.

## Adding a component

1. Add an entry to `catalog.yaml` with every image name you know (upstream, Bitnami, vendor mirrors).
2. Check the endoflife.date product is the same project before setting `eol`.
3. Check what upstream data you get:
   ```bash
   set -a; . ./.env; set +a
   make build && ./bin/owl upstream --component <id> --version <a version you run>
   ```
4. Add a case to `internal/catalog/catalog_test.go` for any unusual tag format.
5. `make test`, rebuild, and check the component in the UI.

Adding components from the UI is planned for a later version.

## Overrides (in the UI)

For images the catalog doesn't recognize — your mirror with an odd path, a fork, your own software —
use the actions on a cluster's **Unrecognized images** card instead of editing the catalog:

| Override | Effect |
|---|---|
| map → component | Treat this repository as that component (scored normally, flagged "mapped"). |
| in-house | Our own software: listed, never scored. |
| ignore | Hidden from the unrecognized list. |

Overrides are per repository (across all clusters), stored in the database, included in backups and
listed with **Undo** in Settings → Catalog overrides. They **never** change how an image the catalog
already recognizes is treated. Registries passed in `--internal-registries` are in-house without any
override.
