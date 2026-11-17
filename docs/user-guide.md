# User guide

A tour of the web UI. For what the levels, notes and drift mean, see [Concepts](concepts.md).

- [Layout](#layout)
- [Overview](#overview)
- [Component page](#component-page)
- [Clusters](#clusters)
- [Cluster page](#cluster-page)
- [Changes](#changes)
- [Settings](#settings)

## Layout

The sidebar has **Overview**, **Clusters**, **Changes** and **Settings**; at the bottom it shows
whether the admin token is set for this browser tab. The UI follows your OS light/dark setting.

Everything is readable without signing in. **Changes need the admin token** (see
[Settings → Admin token](#admin-token)); without it, buttons that change something are disabled and
admin-only sections (backups, logs, audit log) ask for the token.

**Cluster picker** (Overview and Changes only): choose which clusters to look at. Presets select a
whole tenant or stage. The selection is kept in the URL (`?clusters=…`), so you can bookmark or share
a view. Other pages always show every cluster.

**Tables** show 10 rows per page by default (10 / 25 / 50 / All — remembered per browser). Filters,
search and the page number are in the URL, so Back and bookmarks restore the view. Changing a filter
goes back to page 1.

## Overview

- **Level tiles** — how many components are Critical, High, Medium, Low, Unknown, OK. Click one to
  filter the list.
- **Top actions** — the five most urgent components: highest level first, then those running in your
  last (live) stage, then those affecting the most clusters.
- **Cluster cards** — per cluster: Kubernetes version and its EOL status, last report, counts per level.
- **Components** — one cluster selected: a detailed list with versions and reasons. Several clusters:
  a components × clusters matrix, one cell per cluster with its version and level.
  - **Recommended action** column: the component's goal ("Update to 1.14.7 — fixes 11 high
    advisories") and what each group of clusters needs to get there.
  - Note icons (retired, archived, drift, unpinned, needs review, no advisory data, stale data) are
    explained in the legend.
  - Click a row to open a side panel with the details; **What do the levels mean?** spells out the
    rules.

## Component page

Open from any component name. Shows:

- the component's level, target (from the policy) and recommended action,
- every **instance** (cluster, namespace, workload, container, image, version) with its level and
  reasons,
- **advisories** per running version: affected, fixed in, severity, links; "needs review" and "no
  fix listed" separately; "no advisory data" when the project doesn't publish any,
- **release lines**: latest patch per line, LTS, end-of-life dates, where the data comes from
  (endoflife.date or GitHub releases) and when it was fetched,
- **drift** between stages for each tenant,
- **policy editor** — set a policy for just this component, or go back to the global one (admin).

## Clusters

Every registered cluster with its stage, tenant, Kubernetes version and agent status:
*waiting for the first report*, or when it last reported and how many workloads it has. Click one for
its page.

## Cluster page

- **Header** — Kubernetes/RKE2 version and EOL, last report, counts per level.
- **Workloads** — every workload and container with image, version and level. Under the level: the
  action and the reasons. Flags (with tooltips and a legend): unpinned, scaled to 0, rollout in
  progress, mapped, in-house, in-house (marked), unrecognized, ignored. Filter by level, search, page.
- **Unrecognized images** — images Owl doesn't know, per repository. With the admin token you can:
  - **Map** to a component (e.g. your mirror `harbor.example.com/x/redis` → Redis),
  - mark **in-house** (listed, never scored),
  - **ignore** (hidden).

  The decision applies to that repository in every cluster. Undo in Settings → Catalog overrides.

## Changes

History of what changed in the selected clusters: workloads/containers added or removed, image
changes (old → new), Kubernetes version changes, and each cluster's first report. Newest first.

## Settings

All sections need the admin token to change anything.

### Clusters

- **Add cluster** — name, stage, tenant (you can create a new stage or tenant right there). You get:
  - `agent.yaml` — namespace, read-only RBAC and the agent Deployment, with your agent image filled in
    (set it under *Agent settings* first),
  - `agent-secret-<cluster>.yaml` — the Secret with the server URL and the agent token. **This is the
    only time the token is shown.** Download the file (preferred: the token stays out of your shell
    history) or copy the raw token,
  - the `kubectl apply` commands to run.
- **Edit** — change stage or tenant. The name can't change.
- **Rotate token** — issues a new token and revokes the old one immediately. Apply the new Secret and
  run `kubectl -n owl rollout restart deploy/owl-agent`. Use this to cut off an agent without losing
  data.
- **Delete** — type the cluster name to confirm. Offers **Export cluster data** first (JSON: settings,
  current workloads, full history; no token). Deleting removes all of the cluster's data.
- **Agent settings** — the agent image (e.g. `registry.example.com/team/owl:0.7.0`) and the server URL
  agents should use. Until set, generated files contain placeholders and a warning.

### Stages

Add, reorder (↑/↓) and delete stages. Order defines promotion direction for drift. A stage in use
can't be deleted.

### Tenants

Add, rename (applies to every cluster of that tenant), delete when unused.

### Version policy

The global policy (default `lts-or-n-1`) and per-component overrides. See
[policies](concepts.md#version-policies-and-targets).

### Catalog overrides

Every map / in-house / ignore decision, with **Undo**.

### Backups & restore

- Status: where copies go (S3 bucket or "this server only"), last backup, next scheduled run, and
  warnings (no S3, last run failed, uploads pending, newest backup older than 36 h).
- **Back up now**.
- **Schedule & retention** — daily time (server time) and local retention: days to keep (local only)
  or number of local copies (with S3). Bucket retention is a lifecycle rule on the bucket; Owl never
  deletes there.
- **Stored backups** — local and bucket copies. **Download** or **Restore** each.
- **Restore from file…** — upload a `.db.gz` or `.db` backup.
- **Import cluster export…** — bring back one deleted cluster from its JSON export, with a new agent
  token.

Before a restore you confirm what will be replaced. See [Operations → Restore](operations.md#restore)
for exactly what happens.

### Logs & audit

- **Application logs** — log level (debug / info / warn / error) and how many days of log files to keep.
- **Audit log retention** — days (default 365).
- **Audit log** — every admin action: what, when, details and from which IP address.

### Admin token

Paste the server's `OWL_ADMIN_TOKEN` and click **Use**. Owl checks it with the server first and says
so if it's wrong. You can paste the bare token or the whole `OWL_ADMIN_TOKEN=…` line from `.env`.

The token is kept **only in this browser tab** (session storage): it's gone when you close the tab,
and a new tab or window asks again. It is sent only with admin requests. **Forget** removes it.
