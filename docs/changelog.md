# Changelog

What each milestone shipped. Newest first. The roadmap is at the end of [DESIGN.md](../DESIGN.md).

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
- GitHub releases are read 30 per page when GitHub times out on 100 (very large release notes).
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
- Newest patch of a line that ends soon: "Plan the move off <line>" instead of "Review advisories".
- Clusters ahead of the policy target that need only a patch no longer show "Update to <older target>".
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
