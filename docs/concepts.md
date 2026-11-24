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

> **Why:** environments don't all run the same things, and different teams move at different speeds. Comparing only clusters of the same tenant, along the promotion path you define, keeps drift about real promotion problems instead of differences between teams. Names can't change because the change history and exports refer to them.

## Workloads, images and instances

The agent reports every top-level **workload**: Deployments, StatefulSets, DaemonSets, CronJobs,
Jobs without a CronJob, bare Pods, and pods owned by other controllers (e.g. operator resources —
grouped under that owner). For each workload Owl stores:

- the **intended** images from the pod template (what *should* run) — so a Deployment scaled to 0
  still counts, and
- the **running** images from the pods (what *does* run), with pod/ready counts. Several different
  running images for one container means a rollout is in progress (**rollout in progress**); pods that
  all still run something other than the template's image are **not rolled out** — typical for
  StatefulSets and DaemonSets with `updateStrategy: OnDelete`, or a paused Deployment.

The version Owl shows is the template's. The **level** also counts what the pods run: an older version of
the same component still running is scored too, and if it's worse, it decides the level (see
[Priority levels](#priority-levels)).

An **instance** is one component at one version in one place (cluster + namespace + workload +
container). A component's level is the worst level of its instances.

Kubernetes, RKE2 and k3s are special: their version comes from the cluster's nodes
(`kubeletVersion`, e.g. `v1.30.4+rke2r1`), not from an image.

> **Why:** the pod template says what *should* run and is there even when a workload is scaled to 0; the pods say what *does* run, so a rollout that is stuck halfway — or never started — is visible, and a vulnerable version that still serves traffic doesn't look fixed just because the YAML changed. The agent sends a complete snapshot every time, so a missed report fixes itself with the next one.

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
*catalog overrides*, stored in the database and applied across all clusters. A map applies only to
images the catalog doesn't recognize; in-house and ignore keep applying even when a newer built-in
catalog learns the image (a component you add yourself for that repository takes precedence).

Some images are shipped and upgraded by the distribution itself — RKE2's `rancher/hardened-coredns`,
k3s's `rancher/mirrored-coredns-coredns`. In a cluster of that distribution they're tagged
*RKE2-managed* (*k3s-managed*), and the recommended action is to upgrade RKE2 rather than the component.
Their level is computed exactly like any other.

> **Why:** the catalog is compiled into Owl — there is no remote catalog to reach or trust, and every built-in entry was checked against real published images. Your own choices (in-house, ignore) are kept in the database and win over the built-in catalog, so upgrading Owl never undoes them.

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

> **Why:** a floating tag (`redis:7`, `latest`) can change under you without anyone deploying, and Owl can't know which exact version runs. So it can't call it safe, and it doesn't check advisories against a guess.

## Versions and release lines

A **release line** is a series of versions that get patches together:

- if endoflife.date tracks the product (verified to be the same project), its *cycles* are the lines
  (e.g. Kubernetes `1.33`, Postgres `17`),
- otherwise Owl builds lines from GitHub releases, one per minor version (pre-releases skipped).

For each line Owl knows the latest stable version, whether it's LTS, and its end-of-life date if
published. "**N lines behind**" counts these lines. Note that projects differ: Grafana and Redpanda
ship a new minor roughly monthly, so "1 line behind" there is much less dramatic than for Kubernetes
— use a per-component policy if the default is too strict.

> **Why:** endoflife.date publishes support and end-of-life dates, which say far more than tags do; GitHub releases are the fallback when a project has no lifecycle data. "Lines" are counted the way each project names them, which is why the same distance means different things for different projects.

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
component page). A per-component policy overrides the global one. A **pin is per component only**: as the
global policy it would make one version (say 1.30) the target of Redis, Postgres and CoreDNS alike, so
the API refuses it, and a global pin stored by an older Owl is ignored with a note (the default applies).

Changing a policy only changes targets: the report is computed on every read, so levels, actions and
drift follow at once. Policies move "lines behind" (High / Medium / Low), never Critical — that comes from
advisories, end of life and retired projects — so a change doesn't trigger Critical alerts. With
summaries on **Automatic**, new targets mean new upgrades to summarize, within the spending caps.

> **Why:** the newest release is rarely what production should run on day one. The default asks for a supported line — LTS where the project has one, otherwise one line behind the newest — and per-component policies cover projects with a different rhythm. Policies are explicit settings, so everyone sees the same target.

## Upstream data

Owl refreshes upstream data every 6 hours (and at startup) for the components running in at least one
cluster, plus every component added in the UI. A component that starts running is fetched right after
the agent report that shows it:

- **endoflife.date** — release lines, LTS flags, EOL dates,
- **GitHub** — releases, whether the repository is archived, published security advisories,
- **OSV.dev** — advisories for packages it tracks (Go modules such as Kubernetes, RKE2, k3s, ingress-nginx,
  Grafana, CoreDNS; other ecosystems where the catalog names the package).

Responses are cached in the database with ETags. If an upstream is unreachable, the last good copy
is used and marked **stale**. Without `GITHUB_TOKEN`, GitHub allows only 60 requests/hour and data
will be incomplete.

A project is **retired** when its GitHub repository is archived *and* it hasn't released for 90
days (e.g. ingress-nginx). Archived but still releasing (e.g. Redpanda) only gets a note:
development moved somewhere — check where.

> **Why:** fetching only what runs keeps GitHub's rate limit for what matters and the data fresh where you need it. Every answer is cached, so an upstream outage shows *stale* data with a warning instead of an empty page.

## Advisories

For each running version Owl lists advisories that affect it and the version that fixes them.
Owl matches versions against advisory ranges **itself** rather than asking OSV "what affects
version X": for projects whose tags aren't Go module versions (ingress-nginx `controller-v1.x`,
Grafana), that query returns every advisory regardless of version.

- **Affected, fixed in X** — counts towards the level by severity.
- **Affected, no fix listed** — shown, but doesn't raise the level (there's nothing to upgrade to).
- **Needs review** — a GitHub advisory's range is open-ended and no patch is listed (common for
  Redis). OSV ranges are exact, so an open OSV range is "affected, no fix listed" instead.
  Shown as a note; doesn't change the level.
- **No advisory data** — the project publishes no GitHub advisories (Postgres, nginx, HAProxy,
  metrics-server). Owl says so explicitly; it never shows "no vulnerabilities" for these.
- Unpinned tags are never evaluated: `redis:7` could be any 7.x.

Blind spot: vulnerabilities in OS packages or libraries bundled into the image are not covered
(a later version may ingest Trivy Operator reports).

> **Why:** silence would read as "no vulnerabilities", which is the one thing Owl must never claim without data. Matching ranges locally gives the same result for every project, whatever its tag format; a range Owl can't decide is shown for review rather than guessed.

## Priority levels

Each instance gets exactly one level, decided by fixed rules (the same input always gives the same
level). The component's level is its worst instance.

| Level | When |
|---|---|
| 🔴 **Critical** | affected by a high/critical advisory that has a fix · past end of life · upstream retired |
| 🟠 **High** | end of life within 90 days · 2 or more release lines behind the target (unless that line is still supported, below) |
| 🟡 **Medium** | 1 release line behind the target · 2 or more behind on a line upstream still supports (endoflife.date: end date more than 90 days away, or maintained) · unpinned tag · a moderate or unknown-severity advisory |
| 🟢 **Low** | only patch updates available · a low-severity advisory |
| ✅ **OK** | at or above the target and on the latest patch of its line |
| ❔ **Unknown** | no upstream release data — never shown as OK |

**What runs counts.** When pods still run an older version than the template (a rollout half-way, stuck,
or not started), that version is scored with the same rules, and the instance gets the worse of the two
levels. Its reasons are added as "pods still run 1.33.6: release line 1.33 is end-of-life …". A newer
version in the pods never lowers the level, and the version shown, the target distance and drift stay the
template's.

Every level comes with its reasons, e.g. "release line 1.33 is EOL since 2026-06-28", or "5 release lines
behind target 9.4.9 (lts-or-n-1); 8.19 supported until 2027-07-15" for Elasticsearch 8.19 while 9.x moves on.
Without lifecycle data (release lines from GitHub tags) Owl can't tell a supported line from an abandoned
one, so 2+ lines behind stays High.

> **Why:** a level has to be something you can predict and audit — the same versions and upstream data always give the same level, with the rule that fired written next to it. Claude may explain an upgrade but never sets or changes a level. "Far behind" is High because it usually means unsupported; when lifecycle data says the old line is still supported, it's only Medium.

## Notes

Notes are shown next to an instance but **never change its level**:

- needs review (open-ended advisory range),
- advisory with no fix listed,
- no advisory data for this project,
- archived but still releasing,
- drift between stages,
- upstream data stale.

> **Why:** notes are uncertain (an open advisory range) or not something you can act on (no fix listed, no data). Letting them raise the level would make levels noisy, so they stay visible without changing the priority.

## Recommended action

Each component has one **goal**, the same whatever clusters you're looking at — e.g. "Update to
1.14.7 — fixes 11 high advisories", "Replace — retired upstream", "Pin an exact version", "Plan the move off 1.35" (newest patch of a
line that reaches end-of-life soon). When clusters are ahead of the policy target and only need a
patch in their own line, the goal says that ("Patch to 1.37.2") rather than "Update to" the older target.
On a line upstream still supports but far behind (Medium), it's the newest patch of that line first
("Patch to 8.19.25"), then "Plan the move to 9.4.9" once on it. Below it,
one line per group of clusters says what each needs ("it-hetzner 1.14.6 → patch", "it-aws-test,
it-aws-prod 1.13.1 → 1 line behind"). The action is derived from the level and its reasons, never
the other way round.

When the pods don't run the template's version and the template itself needs nothing, the action is
**Finish the rollout to 1.36.6**, with what the pods run and what to check (restart the pods for
`OnDelete`, or look at why the new pods aren't ready). When the template also needs a patch or upgrade,
that step stays the action — it fixes both once rolled out — and the reason says what the pods still run.

> **Why:** one sentence per component that is the same in every view, so teams agree on the goal; the per-cluster lines say who still has work to do. The action always follows from the level and its reasons, never the other way round. Security beats the policy target: if the target version doesn't contain an advisory's fix, the action goes to the newest patch of the line that does, and says so ("beyond the policy target …").

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

> **Why:** promotion means versions move from earlier stages to later ones. A later stage running something newer means a step was skipped; a component missing somewhere is normal (environments differ), so it's information, not a warning.

## Change history

On every report Owl compares the new intended images with the previous ones and records:
`added`, `removed`, `image_changed` and `kubernetes_version_changed`. The first report from a cluster
is a single `first_snapshot` event rather than hundreds of "added" ones. Replica counts and running
pods are not recorded as changes (too noisy) — only the templates.

> **Why:** the history should answer "what changed and when", not repeat every scaling event. Templates change only when someone deploys; replicas and pods change all the time. One `first_snapshot` event keeps a new cluster from flooding the history.

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

> **Why:** reading release notes across many versions is slow, and Claude does it well — but it costs money and the text comes from the internet. So it's off until an admin turns it on, every request is priced before it's sent and stays within daily and monthly caps, only public data leaves the server, and the result explains an upgrade without ever influencing its priority.
