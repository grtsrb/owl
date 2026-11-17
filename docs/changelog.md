# Changelog

What each milestone shipped. Newest first. The roadmap is at the end of [DESIGN.md](../DESIGN.md).

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
