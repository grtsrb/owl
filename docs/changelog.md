# Changelog

What each milestone shipped. Newest first.

## What runs counts

- When pods still run an older version than the workload's configuration — a rollout half-way, stuck, or
  never started (StatefulSets and DaemonSets with `updateStrategy: OnDelete`, a paused Deployment) — that
  version is scored too, and the worse level wins. Before, changing the image in the YAML made Owl treat
  the upgrade as done even though no pod had been replaced.
- New flag **not rolled out** when no pod runs the configured image; the rollout flags show what each pod
  image runs. New action **Finish the rollout to …** when the configuration itself is fine.
- `owl component` shows "1.36.6 (pods run 1.33.6)"; the API has `runningVersions`, `notRolledOut`,
  `score.stillRunning` and action kind `rollout`.

## Overview: one set of numbers

- The level tiles are gone. Under the title: how many components need attention (Critical, High,
  Medium); per-cluster numbers stay on the cluster cards.
- The level filter is a row of chips right above the Components table. A chip shows the components
  whose (worst) level is that level, and its number matches the rows you get — before, choosing Low
  listed every component that was Low in *any* cluster, which was almost all of them. Saved links with
  `?level=` keep working but now use this meaning, so they may list fewer components.
- The summary line also shows how many components are Unknown, so missing upstream data never reads as
  "nothing to do".

## Choose the summary model

- Settings → Claude summaries → **Model**: Claude Haiku 5.5, Sonnet 5.5 or Opus 5.5, each shown with its
  price and the most one summary can cost. Prices, the refusal fallback and the cap checks follow the
  choice; it's audited and kept across restores.
- The default is now **Claude Sonnet 5.5** (was Opus 5.5): about $0.20 at most per summary instead of
  $0.45. `OWL_CLAUDE_MODEL` still sets the server default.

## Docs in Owl

- **Docs** in the sidebar: this documentation, built into Owl — always the docs of the version you run.
  Signed-in users only. A **Topics** drawer on the right with search and the page list; the arrow hides
  it. Every heading has a link to share; code blocks have **Copy**. A **?** next to the Overview,
  Changes, Clusters and Settings → Catalog / Notifications / Backups / Users titles, and on the level
  legend, opens the matching section.
- The docs now explain **why** each feature works the way it does, include the Compose and Kubernetes
  deployment files of this version, and give every troubleshooting entry its own heading.
- `GET /api/v1/docs` and `/api/v1/docs/{page}` (signed in; readable with API tokens).
- Signing in with Google now brings you back to the page you opened (e.g. a shared docs link) instead of
  the Overview.

## Fix: actions that fix the advisories

- The recommended step now always includes the advisories' fixes: an advisory fixed only in a later line
  than the policy target (Velero 1.17.1, fix in 1.18.1) gives "Upgrade to 1.18.4" with the advisory and
  "beyond the policy target" in the reason, instead of "Patch to 1.17.2" (or "Review advisories").

## Milestone 13, batch 2 — CI runners, storage and backup (19 more)

- **CI**: GitLab Runner and its helper (release lines and support dates from endoflife.date — it's
  developed on gitlab.com), Actions Runner Controller (scale sets and the legacy summerwind mode), the
  GitHub Actions runner image.
- **Backup**: Velero and its AWS, Azure and GCP plugins.
- **Storage**: Rook, Ceph, Ceph CSI, Local Path Provisioner (k3s-managed in k3s clusters), and the
  Kubernetes CSI sidecars — snapshotter/snapshot-controller, provisioner, attacher, resizer,
  node-driver-registrar, livenessprobe, health monitor — including Longhorn's `longhornio/csi-*` copies
  and RKE2's snapshot controller.
- Medium on a supported line: the newest patch of the line first; on it, "Plan the move to `<target>`" (a
  new `plan` action kind in the API), which never replaces a real step in the component's summary.
- RKE2's older snapshot-controller copies are RKE2-managed; Rancher's `mirrored-longhornio-csi-*` copies
  are recognized. GitLab Runner release candidates stay pre-releases.
- Not included: Tekton (image names carry build hashes), MinIO (date tags, no community images).

## Milestone 13, batch 1 — 36 more built-in components

- **Elastic**: ECK operator, Elasticsearch (OSV advisories via Maven), Kibana, Logstash, Beats, Elastic
  Agent, ElastAlert 2.
- **Networking**: Traefik (incl. k3s/RKE2 builds), Cilium + Hubble UI + certgen, Envoy (incl. Cilium's
  build), Calico + Tigera operator, flannel + its CNI plugin, MetalLB + FRR, ExternalDNS.
- **Certificates & secrets**: cert-manager, External Secrets Operator, Sealed Secrets.
- **Monitoring & logging**: Prometheus, Alertmanager, Prometheus Operator, node exporter,
  kube-state-metrics (incl. Rancher monitoring mirrors), Grafana Loki (+ Promtail), Alloy, Tempo, Mimir,
  rollout-operator, Thanos, OpenTelemetry Collector and Operator, Fluent Bit.
- Elastic `-SNAPSHOT` and OpenTelemetry `-nightly` tags read as pre-releases, not as the release.
- GitHub releases are read 30 per page when GitHub times out on 100 (very large release notes); a repo
  that needed it goes there directly next time.
- `bundledWith` can name a component: Cilium's Envoy build says "Comes with Cilium — upgrade Cilium …".
- Medium on a supported line: the action is the newest patch of that line, then "plan the move".
- docker.elastic.co, cr.fluentbit.io and oci.external-secrets.io are public registries (no mirror
  matching there); Elastic `-ubi`/`-wolfi`/`-fips` image variants are recognized.
- After the upgrade, already running ones are announced once ("Owl now recognizes N more components").
- **Priority**: 2+ release lines behind is **Medium**, not High, when endoflife.date says the line is still
  supported and doesn't end within 90 days (Elasticsearch/Kibana 8.19, a supported Kubernetes minor).
  The reason says so: "…; 8.19 supported until 2027-07-15".

## Cluster picker redesign

- Overview and Changes: one clusters button (top right) with a popover — All / stage / tenant
  shortcuts and the clusters grouped by stage with checkboxes, a filter once there are many. Replaces
  the row of chips and preset links.

## Milestone 13 — bigger built-in catalog (plumbing)

Groundwork for a much longer built-in catalog; the new entries follow in batches.

- **Upstream data only for what runs**: the 6-hourly refresh covers components running in at least one
  cluster plus your custom ones. A component that starts running is fetched right after that agent
  report, not at the next refresh.
- **Upgrades never undo your choices**: in-house and ignore overrides apply even when the built-in
  catalog learns the image (a component you add for that repository still wins). A map override a
  component now covers says "Now covered by … — remove?"; removing an override fetches the component's
  upstream data at once.
- **No alert flood after an upgrade**: components a newer catalog recognizes among images already
  running are recorded quietly, with one "Owl now recognizes N more components; M Critical" message.
- **RKE2-/k3s-managed images** (`bundledWith` in the catalog): CoreDNS, metrics-server and the
  ingress-nginx controller shipped by RKE2/k3s are tagged *RKE2-managed* and say "Comes with RKE2 —
  upgrade RKE2 …"; their level is unchanged.
- **Settings → Catalog** lists built-in components with search, links, and a green (running, in N
  clusters) or grey (not running) dot, filterable.
- Built-in entries can read the app version from two-version tags (`versionPattern`) and use OSV
  packages from any ecosystem.
- `make catalog-check` verifies catalog entries against GitHub, endoflife.date and OSV (manual).
- Pairs of a component that left the catalog are forgotten, not reported as "no longer Critical".

## Fixes after milestone 12

- Argo Rollouts and other ReplicaSet-based controllers: after a rollback the intended image is the
  rolled-back revision (revision annotation), not the newest ReplicaSet.
- A release line upstream doesn't list yet is no longer called end-of-life because of much older lines.
- Newest patch of a line that ends soon: "Plan the move off `<line>`" instead of "Review advisories".
- Clusters ahead of the policy target that need only a patch no longer show "Update to `<older target>`".
- OSV advisories without a fix are "affected, no fix listed" instead of "needs review".
- GitHub-derived release lines honour the catalog's release tag prefix (e.g. nginx `release-`).
- Custom components: version parts 1–3 (4 was never parsed).
- Claude summaries: URLs in generated text are removed; a summary that was paid for but couldn't be
  saved counts as a failure (backs off instead of paying again); release-note fetch errors show a
  plain message, details in the log.
- The `lts` policy keeps its "no supported LTS line" note when it falls back to the oldest line.
- Per-line fix lists on components whose release lines are majors ("17"): 16.5 is no longer reported
  affected when the 16 line's fix is 16.4.
- Overview level tiles sum over the selected clusters (a component Critical in staging and in live
  counts 2), with the number of distinct components underneath when it differs.
- Sidebar: your account as one row (avatar, email, role) with a sign-out button.
- Known-CVE regression test: real advisory data for IngressNightmare, RediShell, CoreDNS DoQ,
  Kubernetes gitRepo and Grafana SQL expressions, replayed offline.

## Milestone 12 — CLI and personal API tokens

- **Personal API tokens** (Account → API tokens): read-only by default, read & write for admins (only
  while they're admins), expiring after 30/90/365 days or never, with an email a week before. Shown
  once, stored hashed, never in backups. Writes are audited as "you (token: name)". Tokens can't
  touch passwords, sign-in settings or other tokens. Admins see and revoke everyone's in Settings →
  Users.
- **The `owl` CLI** over the API: `login`/`logout`/`whoami`, `status`, `outdated` (filters, `--fail-on`
  for CI), `component`, `clusters`, `changes`, `unrecognized`, `cluster add|rotate-token|delete`
  (`-o secret | kubectl apply -f -`), `backup now`; `-o json` everywhere. `owl logout` revokes the
  token on the server.
- **Users from a file**: `owl users export|plan|apply -f users.yaml` — no state file (Owl marks
  *managed* users), drift with `--detailed-exitcode`, `--prune` disables (never deletes), refuses to
  lock out the person applying. One-off `owl users add|set-role|disable|enable` for teams without a
  file. Users page: *managed by file* badge, and a warning before changing a managed user.
- **Download the CLI** from Account (macOS/Linux, ~7 MB, SHA-256 checksums), or with `curl` and a token
  in CI. The image grows by ~12 MB. `make cli` builds them locally.
- The database-file commands moved to **`owl db user …` / `owl db cluster …`**, and refuse to create a
  database that doesn't exist. `owl user …` and `owl cluster … --name` still work for one release,
  with a warning.

**Upgrading from milestone 11**: nothing required (one automatic database migration). Scripts using
`owl user …` or `owl cluster add --name …` on the server should move to `owl db …`.

## Milestone 11 — Catalog from the UI, Settings reorganised

- **Add components from the UI**: **New component** on a cluster's unrecognized image, in Settings →
  Catalog, or from the Mattermost alert. Owl suggests the GitHub repository (the image's source label on
  public registries, or a repository with exactly the image's name) and endoflife.date products;
  **Preview** shows release data, how every running tag is read, and warns when GitHub and
  endoflife.date disagree. A **version pattern** reads tags like `9.7_p1-r4-ls163`.
- Edit, delete (with its policy and mapped overrides, listed first), **Copy as YAML**; built-in
  components always win. Restored with backups. Claude only ever gets their GitHub repository as name.
- **Settings** now has a grouped menu (Inventory, Integrations, System; a dropdown on phones); Stages
  and Tenants are one page; **Account** moved to your name in the sidebar; viewers no longer see
  admin-only sections. Old links redirect.
- The Critical alert's title counts components and clusters separately.

## Milestone 10 — Mattermost notifications

- **Mattermost** via an incoming webhook (`OWL_MATTERMOST_WEBHOOK_URL`), four notifications, each off
  until switched on in **Settings → Notifications**:
  - **Weekly digest** (default Monday 09:00 Europe/Belgrade): levels, top 5 actions with existing
    summary headlines, drift, what was **upgraded** or **rolled back** that week, what's **no longer
    Critical**, clusters not reporting; a section per tenant. Missed digests are caught up within 24 h,
    never sent twice.
  - **Critical alert** when something newly becomes Critical — after agent reports *and* upstream
    refreshes (new advisories, EOL dates). Once per occurrence, batched per check.
  - **Unrecognized image** the first time any cluster runs an image Owl doesn't know.
  - **New cluster connected**: one summary of a cluster's first report.
- **Digest by email**, opt-in per user (your name in the sidebar → Account); text + simple HTML.
- Switching alerts on, restarts and restores never re-post what's already known. Nothing is ever sent
  to Claude for a notification.
- Dev: `make mattermost-up` / `mattermost-down` for a throwaway Mattermost.

**Upgrading from milestone 9**: nothing required. To use it, create an incoming webhook in Mattermost,
set `OWL_MATTERMOST_WEBHOOK_URL`, restart and switch notifications on.

## Milestone 9 — Sign-in, packaging, logo

- **Sign-in replaces the admin token** (`OWL_ADMIN_TOKEN` is gone). Owl is invite-only: admins invite
  people by email as *viewer* or *admin* (Settings → Users); the invite link is emailed and/or copied.
  Everything except agent ingest, `/healthz` and the agent manifest now needs a signed-in session.
- Passwords (argon2id, ≥ 12 characters, rate-limited sign-in) and optional **Google sign-in**
  (`OWL_GOOGLE_CLIENT_ID/SECRET`, optional Workspace domain). Password sign-in can be switched off
  once an admin has signed in with Google.
- **Email** for invites, password resets ("Forgot password?") and test messages via SMTP
  (`OWL_SMTP_*`, TLS required).
- `owl user invite|list|reset-link|delete` for the first admin and emergencies; `owl healthcheck`.
- The audit log records **who** did what, and the real client IP behind a proxy (`OWL_TRUSTED_PROXIES`).
- Restores never change who can sign in; backups contain no password hashes, sessions or links.
- **Packaging**: Docker Compose with Caddy (automatic HTTPS) for one host such as EC2, Kubernetes
  manifests for running the server in a cluster, multi-arch images (`make image-push`).
- **Logo** and a new accent colour (#574CF9). The dev stack adds Mailpit and prints the first admin's
  invite link.

**Upgrading from milestone 8**: after upgrading, create the first admin with
`owl user invite --admin <email>` (no one can sign in until then), and remove `OWL_ADMIN_TOKEN` from
your environment. Set `OWL_PUBLIC_URL` and serve Owl over HTTPS — sign-in cookies need it.

## Milestone 8 — Claude upgrade summaries

- Claude summarizes the upstream release notes of each upgrade a cluster needs (running version →
  target): headline, breaking changes, migrations, deprecations, security fixes, notable features,
  upgrade path and upgrade risk (Claude's estimate, never a priority level). Only public data is sent.
- Release notes from GitHub releases, the Kubernetes CHANGELOG, Redis `00-RELEASENOTES`, HAProxy's
  changelog (haproxy.org) and postgresql.org.
- Cost protection: off by default; estimate-only mode; every request priced exactly before sending
  and refused if its worst case exceeds the daily ($5) or monthly ($20) cap; per-request token limits;
  pause after 3 failures; spending and request log in Settings → Summaries.
- Component page card per upgrade; headline and upgrade risk in the side panel and Top actions.
- nginx: GitHub release tags (`release-…`) are now understood.

## Docs and fixes (2026-11-17)

- **Fix**: the admin token now works everywhere in the UI. Backups & restore and Logs & audit always
  failed before, because one admin-only request was sent without the token. A pasted token is checked
  with the server, the whole `OWL_ADMIN_TOKEN=…` line is accepted, and the whole UI updates as soon
  as it's set.
- **Fix**: policy changes are recorded in the audit log.
- **Fix**: restoring a valid SQLite file that isn't an Owl database no longer replaces all data with
  empty tables. It's rejected before anything changes.
- Temp files left behind by a crash during a backup or restore are cleaned up at startup.
- Documentation in `docs/`.

## Milestone 7 — Backups, restore, logging, audit log (2026-11-17)

- Daily and manual backups (consistent SQLite copy, gzip, upstream cache left out), uploaded to an
  S3-compatible bucket. The server never deletes in the bucket. Local retention, retried uploads and
  status warnings.
- Restore from a stored or uploaded backup: validated, safety backup first, one transaction, audit
  log and token revocations kept. Older backups are migrated.
- Import of a per-cluster export (brings back one deleted cluster with a new token).
- Rotating JSON log files (daily / 50 MB, gzip, retention from the UI), request ids, log level from the UI.
- Audit log of admin actions in the database and the UI, with retention.

## Milestone 6.5 — UI redesign and cluster management

- New UI: sidebar, cluster picker with tenant/stage presets, Overview with top actions and
  components × clusters matrix, one recommended action per component, Clusters page.
- Cluster management in the UI: add (agent manifest + Secret file, token shown once), edit, rotate
  token, delete with export.
- User-defined ordered stages, managed tenants, tenant-scoped drift.
- Catalog overrides: map / in-house / ignore unrecognized images.
- Paginated tables with state in the URL; explanations for every level and flag.
- Project renamed from "Service Inventory" to Owl.

## Milestone 6 — Web dashboard

React + TypeScript UI embedded in the binary, strict CSP, Overview / component / changes pages,
policy editor.

## Milestone 5 — Policies, scoring, drift

Version policies (global and per component), rule-based priority levels with reasons, promotion drift
across stages. Admin token for writes.

## Milestone 4 — Upstream data

GitHub releases and archived status, endoflife.date lines and EOL, OSV and GitHub advisories with
local range matching, retired detection, cache with ETags and stale fallback.

## Milestone 3 — Catalog

Built-in catalog, image → component matching (vendor builds, mirrors), version parsing, unpinned and
unrecognized images, in-house registries.

## Milestones 1–2 — Agent and server core

Read-only push agent (workloads, pods, nodes; intended and running images), snapshot wire format,
ingest API with per-cluster hashed tokens, SQLite store, change history, `owl cluster` CLI, local
k3d environment.
