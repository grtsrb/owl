# User guide

A tour of the web UI. For what the levels, notes and drift mean, see [Concepts](concepts.md).

- [Signing in](#signing-in)
- [Layout](#layout)
- [Overview](#overview)
- [Component page](#component-page)
- [Clusters](#clusters)
- [Cluster page](#cluster-page)
- [Changes](#changes)
- [Settings](#settings)

## Signing in

Owl is invite-only. You get an invite link from an admin (by email, or they send it to you): open it,
choose a password (at least 12 characters) — or click **Sign in with Google** if it's offered — and
you're signed in. The link works once, for 72 hours; ask for a new one if it expired.

Next time, sign in with your email and password, or with Google. **Forgot password?** on the sign-in
page emails you a link (when the server can send email); otherwise ask an admin for a reset link.
You stay signed in for up to 30 days (7 days without using Owl signs you out). Your name at the
bottom of the sidebar leads to **Account** and **Sign out**.

## Layout

The sidebar has **Overview**, **Clusters**, **Changes** and **Settings**; at the bottom it shows who
you're signed in as, your role, and **Sign out**. The UI follows your OS light/dark setting.

**Viewers** can see everything. **Admins** can also change things; for viewers, buttons that change
something are disabled and admin-only sections (users, backups, logs, audit log, summary spending)
say so.

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
- **Upgrade summaries** — one card per upgrade a cluster needs (e.g. 11.2.3 → 12.1.4): Claude's
  summary of the release notes (headline, breaking changes, migrations, deprecations, security fixes,
  notable features, upgrade path, *upgrade risk*), each point linked to its release notes. Without a
  summary the card says why: off, estimate and maximum cost, **Generate** (admin, manual mode), too
  large (**Generate anyway**), waiting for budget, failed, paused. Admins can **Regenerate** one.
  Written by Claude from upstream text — verify before acting.

The Overview's side panel and **Top actions** show the summary's headline and upgrade risk when one
exists.

## Clusters

Every registered cluster with its stage, tenant, Kubernetes version and agent status:
*waiting for the first report*, or when it last reported and how many workloads it has. Click one for
its page.

## Cluster page

- **Header** — Kubernetes/RKE2 version and EOL, last report, counts per level.
- **Workloads** — every workload and container with image, version and level. Under the level: the
  action and the reasons. Flags (with tooltips and a legend): unpinned, scaled to 0, rollout in
  progress, mapped, in-house, in-house (marked), unrecognized, ignored. Filter by level, search, page.
- **Unrecognized images** — images Owl doesn't know, per repository. Admins can:
  - add it as a **New component** (see [Catalog](#catalog)) — for software Owl's catalog doesn't know,
  - **Map** to a component (e.g. your mirror `harbor.example.com/x/redis` → Redis),
  - mark **in-house** (listed, never scored),
  - **ignore** (hidden).

  The decision applies to that repository in every cluster. Undo in Settings → Catalog.

## Changes

History of what changed in the selected clusters: workloads/containers added or removed, image
changes (old → new), Kubernetes version changes, and each cluster's first report. Newest first.

## Settings

A menu on the left (a dropdown on phones) in three groups: **Inventory** (Clusters, Stages & tenants,
Catalog, Version policy), **Integrations** (Notifications, Claude summaries) and **System** (Users,
Backups & restore, Logs & audit). Only admins can change anything; viewers see Clusters, Stages &
tenants, Catalog and Version policy read-only, and don't see the admin-only sections.

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

### Stages & tenants

- **Stages** — add, reorder (↑/↓) and delete. Order defines promotion direction for drift. A stage in
  use can't be deleted.
- **Tenants** — add, rename (applies to every cluster of that tenant), delete when unused.

### Version policy

The global policy (default `lts-or-n-1`) and per-component overrides. See
[policies](concepts.md#version-policies-and-targets).

### Catalog

- **Your components** — software added to Owl's catalog from the UI, with **Edit**, **Delete** and
  **Copy as YAML** (a `catalog.yaml` entry, to make it built-in). A component is *in effect*, or
  *Replaced by built-in* when a newer Owl ships it itself (delete yours then).
- **+ Add component** (or **New component** on a cluster's unrecognized image, or the link in the
  Mattermost alert) opens the form:
  - **Name**, **ID** (fixed once saved), **image repositories** (without tag), **GitHub repository**
    (releases and advisories are read there), optional **endoflife.date product** — only if it's the
    same project.
  - Owl **suggests** the GitHub repository from the image's source label (public registries only) or
    a GitHub repository with exactly the image's name, and endoflife.date products with a matching
    name. Check them — they're suggestions.
  - **Advanced**: version parts (tags with fewer are floating), tag prefix, GitHub release tag prefix,
    and a **version pattern** for tags Owl can't read (a regular expression; its groups joined with dots
    are the version — `^(\d+\.\d+)_p(\d+)` reads `9.7_p1-r4-ls163` as 9.7.1).
  - **Preview** shows the release lines, latest stable, advisory source and how every running tag of
    these images is read — plus warnings, e.g. when GitHub and endoflife.date disagree about the newest
    release (probably different projects). You can save anyway; the button then says so.
- Deleting a component makes its images unrecognized again and also removes its version policy and
  any overrides mapped to it — the dialog lists them.
- **Built-in components** — the list Owl ships with (read-only).
- **Catalog overrides** — every map / in-house / ignore decision, with **Undo**.

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

### Summaries

- **Mode** — Off (default), Estimate only, Manual, Automatic. Manual/Automatic need `ANTHROPIC_API_KEY`
  on the server.
- **Daily / monthly cap** in USD and **max input / output tokens** per summary, with the resulting
  maximum cost of one summary.
- **Spending** today, this month and in total; model, price and refusal fallback.
- **Recent requests** — the last 20: upgrade, tokens, cost, result and error.
- **Paused** notice with **Resume** after 3 failures in a row.

How the protection works: [Operations → Claude upgrade summaries](operations.md#claude-upgrade-summaries).

### Notifications

Admins switch on what Owl posts to Mattermost (needs `OWL_MATTERMOST_WEBHOOK_URL` on the server):

- **Weekly digest** — one message per week: components per level, the top 5 actions (with the Claude
  summary headline if one already exists), drift between stages, **Upgraded this week** (version
  changes from the change history, with the level each component has *now*), **Rolled back** (⚠),
  **No longer Critical**, and clusters that haven't reported for over 24 hours. With more than one
  tenant there's a section per tenant. Day, time and time zone are set here.
- **Critical alert** — when a component on a cluster *newly* becomes Critical (after an agent report or
  an upstream refresh, e.g. a new advisory or an EOL date passing). Once: nothing more while it stays
  Critical, nothing when it stops being Critical (the digest shows that), a new alert if it becomes
  Critical again.
- **Unrecognized image** — the first time any cluster runs an image Owl doesn't know, with a link to
  the cluster's Unrecognized images card (map / in-house / ignore).
- **New cluster connected** — on a cluster's first report: stage, tenant, Kubernetes version and EOL,
  components per level, everything Critical, the next actions and its unrecognized images ("new to
  Owl" or "also on …"). It replaces the two alerts above for that report.

Several findings of one check go out as one message. Switching an alert on doesn't post what's already
Critical or already known. **Send test message** checks the webhook; **Send digest now** posts this
week's digest at once (Mattermost only; the scheduled one still goes out). The card shows the last
message, the last error and when the next digest is due.

### Logs & audit

- **Application logs** — log level (debug / info / warn / error) and how many days of log files to keep.
- **Audit log retention** — days (default 365).
- **Audit log** — every admin action: what, when, who, details and from which IP address.

### Users (admins)

- **Invite user** — email and role (*viewer* or *admin*). Owl emails the invite link when email is set
  up; the link is also shown once, to copy and send yourself (e.g. on Mattermost). Invite people with
  the address of their Google account if you use Google sign-in.
- The table shows each user's role, status (*invited*, *active*, *disabled*), how they sign in and when
  they last did. Per user: change the **role**, **Send invite again** (pending invites; the old link
  stops working), **Reset password** (a one-hour link), **Disable** (signed out at once, can't sign
  in, their API tokens are deleted) / **Enable**, **Delete**. You can't change or delete yourself, and Owl
  always keeps at least one active admin.
- **managed by file** — the user is listed in a users file applied with `owl users apply`
  ([CLI → Users](cli.md#users)). You can still change them here; Owl asks first, because the change
  shows as drift until the file is updated.
- **API tokens** — every user's personal API tokens (scope, last use, expiry), each with **Revoke**.
- **Sign-in & email** — turn **password sign-in** off once Google sign-in works and an admin has used
  it (everyone then signs in with Google); Google status and the redirect URI to register; email
  status and **Send me a test email**.

### Account (your name in the sidebar)

Your email, role and sign-in methods, **Sign out**, and **Change password** (signs out your other
sessions and revokes your API tokens). **Email me the weekly digest** — the digest by email, on the schedule an admin set in
Settings → Notifications (needs email to be set up on the server).

- **Command-line tool** — downloads of the `owl` CLI for macOS and Linux (with checksums) and how to
  install and sign in. See [CLI](cli.md).
- **API tokens** — **New token**: a name, the scope (*read-only*, or *read & write* for admins) and
  the expiry (30, 90, 365 days or never). The token is shown **once**: copy it into `owl login` or a
  CI variable. The list shows when each was created and last used and when it expires (*expired*
  ones stay listed for 30 days); **Revoke** stops one at once.
