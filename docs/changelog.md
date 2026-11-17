# Changelog

What each milestone shipped. Newest first. The roadmap is at the end of [DESIGN.md](../DESIGN.md).

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
