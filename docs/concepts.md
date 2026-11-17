# Concepts

This page explains how Owl sees your clusters and how it decides what needs attention. Every
judgement Owl makes is **rule-based and shows its reasons**; nothing is a black box.

- [Clusters, stages and tenants](#clusters-stages-and-tenants)
- [Workloads, images and instances](#workloads-images-and-instances)
- [Components and the catalog](#components-and-the-catalog)
- [Image statuses](#image-statuses)
- [Versions and release lines](#versions-and-release-lines)
- [Version policies and targets](#version-policies-and-targets)
- [Upstream data](#upstream-data)
- [Advisories](#advisories)
- [Priority levels](#priority-levels)
- [Notes](#notes)
- [Recommended action](#recommended-action)
- [Drift between stages](#drift-between-stages)
- [Change history](#change-history)
- [Upgrade summaries](#upgrade-summaries)

## Clusters, stages and tenants

A **cluster** is one Kubernetes cluster with one Owl agent in it. Each cluster has:

- a **name** (lowercase letters, digits, `-`; can't be changed later because history and exports use it),
- a **stage** — where it sits in your promotion path, and
- a **tenant** — who uses it.

**Stages** are an ordered list you define (Settings → Stages & tenants), e.g. `staging → prelive → live`.
Order matters: Owl expects versions to move from earlier to later stages. Several clusters can
share a stage. A stage can only be deleted when no cluster uses it.

**Tenants** are teams or business units (e.g. `IT`, `DS`). They are a filter and a boundary for
drift: Owl only compares clusters of the **same tenant**. Renaming a tenant renames it on every
cluster; deleting needs it to be unused.

## Workloads, images and instances

The agent reports every top-level **workload**: Deployments, StatefulSets, DaemonSets, CronJobs,
Jobs without a CronJob, bare Pods, and pods owned by other controllers (e.g. operator resources —
grouped under that owner). For each workload Owl stores:

- the **intended** images from the pod template (what *should* run) — so a Deployment scaled to 0
  still counts, and
- the **running** images from the pods (what *does* run), with pod/ready counts. Several different
  running images for one container means a rollout is in progress.

An **instance** is one component at one version in one place (cluster + namespace + workload +
container). A component's level is the worst level of its instances.

Kubernetes, RKE2 and k3s are special: their version comes from the cluster's nodes
(`kubeletVersion`, e.g. `v1.30.4+rke2r1`), not from an image.

## Components and the catalog

A **component** is an upstream project Owl knows about (Grafana, CoreDNS, …). The built-in
**catalog** maps image names to components and says where to find upstream data (GitHub repository,
endoflife.date product, OSV module). See [Catalog](catalog.md) for the format.

Matching is by image repository, ignoring the tag:

- exact names: `grafana/grafana`, `registry.k8s.io/coredns/coredns`,
- vendor builds: `rancher/hardened-coredns:v1.11.1-build20240305` → CoreDNS 1.11.1,
- mirrors and proxies by path suffix: `harbor.example.com/proxy/library/nginx` → nginx.

An exact match always beats a suffix match.

Images the catalog doesn't know end up as **unrecognized**. For each such repository you can, in
the UI, **map** it to a known component, mark it **in-house**, or **ignore** it. These choices are
*catalog overrides*, stored in the database, applied across all clusters, and only ever applied to
images the built-in catalog does not recognize.

## Image statuses

| Status | Meaning | Scored? |
|---|---|---|
| identified | Known component, tag pins a version (`redis:7.2.4`) | yes |
| unpinned | Known component but the tag is floating: `latest`, digest only, or fewer version parts than the component uses (`postgres:16`, `redis:7`) | yes — Medium at least, advisories not evaluated |
| in-house | From a registry listed in `--internal-registries` (or marked in-house) | no — listed only |
| unrecognized | Not in the catalog | no — shown for you to classify |
| ignored | Unrecognized and you chose to ignore it | no |

Vendor suffixes are stripped when reading a version (`-hardened3`, `-build…`, `-debian-12-r20`,
`-alpine`), but real pre-releases are kept (`-rc.1`). The exact image is always shown.

## Versions and release lines

A **release line** is a series of versions that get patches together:

- if endoflife.date tracks the product (verified to be the same project), its *cycles* are the lines
  (e.g. Kubernetes `1.33`, Postgres `17`),
- otherwise Owl builds lines from GitHub releases, one per minor version (pre-releases skipped).

For each line Owl knows the latest stable version, whether it's LTS, and its end-of-life date if
published. "**N lines behind**" counts these lines. Note that projects differ: Grafana and Redpanda
ship a new minor roughly monthly, so "1 line behind" there is much less dramatic than for Kubernetes
— use a per-component policy if the default is too strict.

## Version policies and targets

A **policy** decides which version a component *should* run — its **target**.

| Policy | Target |
|---|---|
| `lts-or-n-1` *(default)* | the newest LTS line if the product has one, otherwise the previous line (n-1) — latest patch |
| `lts` | the newest LTS line (falls back to n-1 if there is no LTS) |
| `latest` | the newest line |
| `n-1`, `n-2` | one / two lines before the newest |
| `pin:<line or version>` | exactly that line or version, e.g. `pin:12.3` or `pin:16` |

Policies are set globally and optionally per component (Settings → Version policy, or the
component page). A per-component policy overrides the global one.

## Upstream data

Owl refreshes upstream data every 6 hours (and at startup):

- **endoflife.date** — release lines, LTS flags, EOL dates,
- **GitHub** — releases, whether the repository is archived, published security advisories,
- **OSV.dev** — advisories for Go-module projects (Kubernetes, RKE2, k3s, ingress-nginx, Grafana, CoreDNS).

Responses are cached in the database with ETags. If an upstream is unreachable, the last good copy
is used and marked **stale**. Without `GITHUB_TOKEN`, GitHub allows only 60 requests/hour and data
will be incomplete.

A project is **retired** when its GitHub repository is archived *and* it hasn't released for 90
days (e.g. ingress-nginx). Archived but still releasing (e.g. Redpanda) only gets a note:
development moved somewhere — check where.

## Advisories

For each running version Owl lists advisories that affect it and the version that fixes them.
Owl matches versions against advisory ranges **itself** rather than asking OSV "what affects
version X": for projects whose tags aren't Go module versions (ingress-nginx `controller-v1.x`,
Grafana), that query returns every advisory regardless of version.

- **Affected, fixed in X** — counts towards the level by severity.
- **Affected, no fix listed** — shown, but doesn't raise the level (there's nothing to upgrade to).
- **Needs review** — the advisory's range is open-ended and no patch is listed (common for Redis).
  Shown as a note; doesn't change the level.
- **No advisory data** — the project publishes no GitHub advisories (Postgres, nginx, HAProxy,
  metrics-server). Owl says so explicitly; it never shows "no vulnerabilities" for these.
- Unpinned tags are never evaluated: `redis:7` could be any 7.x.

Blind spot: vulnerabilities in OS packages or libraries bundled into the image are not covered
(a later version may ingest Trivy Operator reports).

## Priority levels

Each instance gets exactly one level, decided by fixed rules (the same input always gives the same
level). The component's level is its worst instance.

| Level | When |
|---|---|
| 🔴 **Critical** | affected by a high/critical advisory that has a fix · past end of life · upstream retired |
| 🟠 **High** | end of life within 90 days · 2 or more release lines behind the target |
| 🟡 **Medium** | 1 release line behind the target · unpinned tag · a moderate or unknown-severity advisory |
| 🟢 **Low** | only patch updates available · a low-severity advisory |
| ✅ **OK** | at or above the target and on the latest patch of its line |
| ❔ **Unknown** | no upstream release data — never shown as OK |

Every level comes with its reasons, e.g. "release line 1.33 is EOL since 2026-06-28".

## Notes

Notes are shown next to an instance but **never change its level**:

- needs review (open-ended advisory range),
- advisory with no fix listed,
- no advisory data for this project,
- archived but still releasing,
- drift between stages,
- upstream data stale.

## Recommended action

Each component has one **goal**, the same whatever clusters you're looking at — e.g. "Update to
1.14.7 — fixes 11 high advisories", "Replace — retired upstream", "Pin an exact version". Below it,
one line per group of clusters says what each needs ("it-hetzner 1.14.6 → patch", "it-aws-test,
it-aws-prod 1.13.1 → 1 line behind"). The action is derived from the level and its reasons, never
the other way round.

## Drift between stages

Owl compares the clusters of each tenant along your stage order, and only for components that run
in both:

- **pending promotion** — an earlier stage runs a newer version than a later one ("newer in staging,
  not yet in prod"): normal while a rollout is moving through,
- **anomaly** — a later stage runs a *newer* version than an earlier one ("prod newer than staging"):
  something skipped the path,
- **only in** — the component runs only in some of the tenant's clusters: information, not a problem
  (environments don't all run the same things).

When you select a subset of clusters in the UI, drift involving unselected clusters is hidden.

## Change history

On every report Owl compares the new intended images with the previous ones and records:
`added`, `removed`, `image_changed` and `kubernetes_version_changed`. The first report from a cluster
is a single `first_snapshot` event rather than hundreds of "added" ones. Replica counts and running
pods are not recorded as changes (too noisy) — only the templates.

## Upgrade summaries

Optionally (off by default — it costs money, see [Operations](operations.md#claude-upgrade-summaries)),
Claude reads the upstream release notes of an upgrade and summarizes what it involves.

- **One summary per upgrade** ("jump"): component + exact running version → exact target version,
  e.g. Grafana 11.2.3 → 12.1.4. Clusters running the same version share it. Nothing for instances that
  are already at their target, ahead of it, or unpinned (the starting version isn't known).
- **Which release notes**: stable releases only. Within one release line, every release after the
  running version up to the target. Across lines, the first release of each line in between plus every
  release of the target line — patches of the old line are backports and skipped. For projects whose
  `.0` entry only lists changes since the last pre-release (Redis, HAProxy), the line's milestones,
  RCs and dev releases are part of its `.0`.
- **Where notes come from**: GitHub release notes, or for projects that publish elsewhere, the
  Kubernetes CHANGELOG (only *Urgent Upgrade Notes*, *Deprecation*, *API Change*, *Feature*), Redis'
  `00-RELEASENOTES`, HAProxy's changelog on haproxy.org (MAJOR/MEDIUM entries) and postgresql.org's
  release notes. Owl collects them; Claude gets no web access.
- **What Claude sees**: the component's name and repository, the two versions and those notes —
  **never** cluster names, namespaces, workloads, tenants, stages, hosts or configs (tested).
- **What a summary says**: a headline; breaking changes, migrations / manual steps, deprecations,
  security fixes, notable features (at most 8 each, each tied to the release it comes from); the
  upgrade path (direct, via intermediate versions, or not stated); and an **upgrade risk** (low /
  medium / high, with a reason).
- **Upgrade risk is not priority.** It's Claude's estimate of how risky or laborious the upgrade itself
  is. The priority level stays rule-based and is never changed, sorted or filtered by it.
- **Verify before acting.** Summaries are written from text published on the internet. Owl treats the
  notes as untrusted (Claude is told to ignore instructions in them), validates the output, shows it as
  plain text, and links each point to the release notes it came from.
