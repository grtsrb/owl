# Operations

Running Owl day to day: backups, restore, logs, the audit log, monitoring and maintenance.

- [Backups](#backups)
- [Restore](#restore)
- [Claude upgrade summaries](#claude-upgrade-summaries)
- [Bringing back one deleted cluster](#bringing-back-one-deleted-cluster)
- [Logs](#logs)
- [Audit log](#audit-log)
- [Monitoring](#monitoring)
- [Agent tokens](#agent-tokens)
- [Background jobs](#background-jobs)

## Backups

**What's in a backup:** everything in the database — clusters, agent token hashes, current
workloads, change history, stages, tenants, policies, catalog overrides, settings, the audit log —
**except the upstream cache**, which is refetched (that keeps a backup at ~15 KB instead of ~70 MB).

**How:** SQLite `VACUUM INTO` takes a consistent copy while agents keep writing; it's gzipped into
`backups/owl-<UTC date>-<time>.db.gz` and then uploaded. Always written locally first.

**When:** every day at the configured time (default 03:00, server local time) and whenever you click
**Back up now**. If the server was down at the scheduled time, the backup runs as soon as it's up
again. One scheduled attempt per day; a failure shows as a warning and the next day tries again.

### With S3 (recommended)

Set the `OWL_BACKUP_S3_*` variables ([Configuration](configuration.md#backups-to-s3)). Then:

- each backup is uploaded after it's taken; if the bucket is unreachable, the copy stays local and
  uploads are retried every 10 minutes (and at the next backup),
- the server keeps the newest *N* local copies (default 3) and **never deletes a local copy that
  isn't confirmed uploaded**,
- **the server never deletes anything in the bucket.** Give it credentials without delete
  permission, and set the bucket's retention with a lifecycle rule.

Example AWS IAM policy — upload, list, read, no delete:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": ["s3:ListBucket"], "Resource": "arn:aws:s3:::owl-backups" },
    { "Effect": "Allow", "Action": ["s3:PutObject", "s3:GetObject", "s3:AbortMultipartUpload"],
      "Resource": "arn:aws:s3:::owl-backups/*" }
  ]
}
```

Example lifecycle rule — keep 90 days:

```json
{ "Rules": [ { "ID": "owl-retention", "Status": "Enabled", "Filter": { "Prefix": "" },
               "Expiration": { "Days": 90 } } ] }
```

Hetzner Object Storage and MinIO accept the same S3 policies/lifecycle rules. For extra protection,
enable bucket versioning or Object Lock so even overwritten keys can be recovered.

### Without S3

Backups stay in `backups/` on the same disk as the database — the UI warns **"Local only — not
safe"**. Backups older than the configured days (default 15) are deleted; the newest is always kept.

### Warnings in Settings → Backups & restore

| Warning | What to do |
|---|---|
| Local only — not safe | Configure `OWL_BACKUP_S3_*`. |
| Last backup run had a problem: … | Read the error; check disk space or bucket access, then **Back up now**. |
| N backup(s) not uploaded yet | Bucket unreachable or credentials wrong; uploads retry every 10 minutes. |
| No backup has been taken yet | Click **Back up now**. |
| The newest backup is more than 36 hours old | Scheduled backups aren't running or failing — check the logs for `backup failed`. |

### Off-site copy by hand

**Download** any backup from the list, or copy files from `backups/` on the host. A backup is a
gzipped SQLite file: `gunzip -c owl-….db.gz > owl.db && sqlite3 owl.db .tables`.

## Restore

From **Settings → Backups & restore**: **Restore** next to a stored backup, or **Restore from file…**
to upload a `.db.gz` / `.db`. The server then:

1. reads and checks the file: gzip or plain SQLite, at most 2 GiB, written by Owl (has Owl's
   schema), not from a *newer* Owl version,
2. takes a **safety backup** of the current state (`…-pre-restore.db.gz`, local, uploaded with the next
   run) — if that fails, nothing is restored,
3. upgrades the backup to the current schema if it's older,
4. replaces, **in one transaction**, clusters, agent tokens, current workloads, change history,
   stages, tenants, policies, catalog overrides and settings,
5. applies the restored log settings.

It **keeps**:

- the **audit log** — a restore doesn't rewrite who did what (and the restore itself is audited),
- the upstream cache,
- **token revocations**: tokens rotated or belonging to clusters deleted since the backup stay
  revoked. An old backup can't bring a leaked token back to life.
- **Claude summaries, their request log and their settings** (mode, caps, a pause): summaries cost
  money to write and the log is what the caps count — restoring an old backup must not reset today's
  spending, unpause paid requests or bring back a higher cap.
- **users, sessions, invite/reset links and the password sign-in setting**: a restore never changes
  who can sign in. (Backups contain users without password hashes, and no sessions or links.)

**Lost the whole server?** Put a backup in place as the database instead of restoring through the UI:
`gunzip -c owl-….db.gz > /srv/owl/owl.db` (owned by UID 65532) on the new host, then start Owl.
Everyone keeps their account and role; Google users sign in as before, password users need a reset
link — `owl db user reset-link --email <admin>` for yourself, then Settings → Users → Reset password.

After a restore:

- clusters added after the backup are gone — add them again,
- agents whose cluster is in the backup keep working, **except** clusters whose token was rotated or
  that were deleted since: use **Rotate token** for those and apply the new Secret,
- if the restore went wrong, restore the `…-pre-restore` safety backup.

A file that isn't an Owl backup is rejected with `400` and changes nothing.

## Claude upgrade summaries

For each upgrade a cluster needs (running version → target), Owl can have Claude summarize the
upstream release notes: breaking changes, migrations, deprecations, security fixes, notable features,
the upgrade path and an *upgrade risk* (Claude's estimate — it never changes a priority level). See
[Concepts → Upgrade summaries](concepts.md#upgrade-summaries) for what's sent and how notes are chosen.

**It costs money** (pay-per-use on your Anthropic account), so it is **off by default** and protected in
two independent layers.

### Layer 1 — on Anthropic's side (set this up first)

A ceiling Owl can't exceed even if it misbehaved:

1. In the [Claude Console](https://platform.claude.com), create a **workspace** just for Owl and an API
   key in it.
2. Set a **monthly spend limit** on that workspace (e.g. $10).
3. Use **prepaid credits with auto-reload off**, so the most that can ever be spent is what's left.
4. Give the key to the server as `ANTHROPIC_API_KEY` (environment only) and restart.

### Layer 2 — inside Owl (Settings → Claude summaries)

| Mode | What happens | Cost |
|---|---|---|
| **Off** *(default)* | Nothing is fetched or sent. Existing summaries still show. | none |
| **Estimate only** | Release notes are collected; each upgrade shows a local token estimate and its maximum cost. Nothing is sent to Claude. | none |
| **Manual** | An admin clicks **Generate** on an upgrade (the button shows the maximum cost). | per click |
| **Automatic** | Missing summaries are written in the background, one at a time, most urgent component first. | within the caps |

Before **every** request Owl:

1. counts the input tokens exactly (Anthropic's token counting, which is free),
2. refuses if it's above the input limit (default 30k) — such upgrades show **Too large** and are only
   written when an admin clicks **Generate anyway** (hard ceiling 200k),
3. prices the **worst case**: all input plus the full output limit (default 4k), plus the same again
   on the refusal-fallback model (Opus 5.5 → Opus 5),
4. sends it only if that worst case fits what's left of the **daily** ($5) and **monthly** ($20) caps
   (UTC). Afterwards the actual cost from the API's usage report is recorded.

With the defaults one summary costs at most about **$0.45**. A paid request is never retried
automatically. One whose outcome is unknown (timeout, dropped connection) is booked at its worst case,
so the caps never undercount.

**Failures**: a failed upgrade (API error, refusal, invalid or truncated output) is retried at most once
per UTC day. After **3 failed requests in a row** summaries pause — Settings → Claude summaries shows the last
error and a **Resume** button.

**Never rewritten automatically**: an existing summary stays as it is, also when the model changes;
it shows which model wrote it and when. Admins can **Regenerate** one (cost shown, caps apply).

Settings → Claude summaries shows spending today, this month and in total, and the last 20 requests (component,
versions, tokens, cost, result). Mode and limit changes, generated and failed summaries and resumes are
in the audit log.

## Notifications

What and when: [User guide → Notifications](user-guide.md#notifications). How it behaves:

- **Checks** run after every agent report that changed something, every upstream refresh, and every
  admin change that can change levels (policies, catalog overrides, clusters) — but only once upstream
  data has loaded after a start, so a restart never looks like "everything resolved, then Critical again".
- **State** — what's Critical, which clusters were announced, which image repositories are known, when the
  last digest went out — is in the database and **kept across restores** (like the audit log). After a
  restore, Owl records the restored state without posting anything. The switches and the schedule are
  ordinary settings and come back with the backup.
- **Delivery**: each message is tried 3 times (network errors, `429`, `5xx`; 2 s and 8 s apart). A failed
  alert is dropped — the digest covers it — and shown in Settings → Notifications and the log. The
  digest is retried hourly. A digest missed while the server was down is sent once when it's back, if
  it's less than 24 hours late; otherwise that week is skipped. It's never sent twice.
- **Digest email** goes to every active user who opted in (your name in the sidebar → Account), once per digest; a
  failing address is logged (without the address) and not retried, so nobody gets it twice.
- Mattermost's "Enable integrations to override usernames" setting decides whether posts show as **Owl**
  or as the webhook's creator.
- **Never** spends on Claude: the digest only shows summaries that already exist.

## Bringing back one deleted cluster

Deleting a cluster offers **Export cluster data** (JSON with its settings, current workloads and
history, but no token). To bring it back: **Settings → Backups & restore → Import cluster export…**.
Owl recreates the cluster (creating its stage/tenant if missing), restores its workloads and history,
and issues a **new agent token**, shown once. Apply the new Secret in the cluster.

## Logs

- JSON lines to **stderr** and to **`logs/server.log`** next to the database.
- A new file every UTC day or at 50 MB; old files are gzipped and deleted after the retention
  (default 5 days, Settings → Logs & audit).
- Level: `OWL_LOG_LEVEL`, or the level saved in the UI (takes effect immediately, no restart).
- One line per HTTP request: method, path, status, duration, remote address and a **request id**
  (also returned as the `X-Request-ID` header — quote it when reporting a problem). Health checks,
  static files and UI page loads log at `debug`.
- Headers, query strings and tokens are **never** logged (covered by a test).
- Cap Docker's own copy of the stderr logs (`--log-opt max-size=10m --log-opt max-file=3`), since the
  file logs are the ones kept.
- Agents log JSON to stderr only; Kubernetes rotates it: `kubectl -n owl logs deploy/owl-agent`.

Useful messages:

| Message | Meaning |
|---|---|
| `snapshot ingested` | An agent reported (cluster, workloads, changes). |
| `audit: <action>` | An admin action (also in the audit log), with the user (`by`). |
| `signed in` / `sign-in failed` | Sign-ins (user, method, client IP); failures without saying which part was wrong. |
| `google sign-in refused` | A Google sign-in that wasn't accepted, with the reason. |
| `email failed` / `password reset email failed` | SMTP problems (the admin still got the link). |
| `backup taken` / `backup uploaded` / `backup failed` | Backups. |
| `upstream refreshed` / `upstream refresh incomplete` | Upstream data refresh results and per-component errors. |
| `push failed, retrying` (agent) | The agent couldn't reach the server; it retries. |

## Audit log

Every admin action is written to the audit log (in the database, viewable in Settings → Logs &
audit) and to the application log: cluster added/edited/deleted/exported/imported, token rotated,
stages, tenants, catalog overrides, policies, agent settings, operations settings, backup taken or
downloaded, database restored, summary settings; users invited, re-invited, role changed, disabled,
enabled, deleted; reset links created, invites accepted, passwords reset or changed, Google accounts
linked, password sign-in turned on/off, test emails. Each entry has the time, the action, **who** (the
signed-in user's email), details (`key=value`) and the client IP (the real one behind a proxy listed
in `OWL_TRUSTED_PROXIES`).

Entries older than the retention (default 365 days) are deleted daily. The audit log is part of
backups but survives a restore unchanged.

## Monitoring

- `GET /healthz` → `200 ok` (no authentication, no host check) — for Docker/Kubernetes health
  checks and uptime monitors.
- Watch for: a cluster's **last report** older than ~2 intervals (agent down or token revoked),
  backup warnings, `backup failed` and `audit log write failed` in the logs.
- Notifications (Mattermost digest and critical alerts) are planned for milestone 10.

## Agent tokens

- One token per cluster, generated by the server, **shown once**, stored only as a SHA-256 hash.
- **Rotate** (UI or `owl cluster rotate-token <name>`) when a token may have leaked or someone with access
  leaves: the old token stops working immediately; apply the new Secret and restart the agent.
- An agent with a bad token logs `401` and doesn't retry. It recovers by itself once the Secret is
  fixed and the pod restarted.

## API tokens

- Personal tokens for the CLI and scripts ([CLI](cli.md#personal-api-tokens)). Admins see every user's
  tokens in **Settings → Users → API tokens** and can revoke any of them; disabling a user deletes theirs.
- The audit log names the token: `you@example.com (token: gitlab-ci)`.
- Not in backups, and a restore keeps the tokens the server has.

## Background jobs

| Job | When |
|---|---|
| Upstream refresh (GitHub, endoflife.date, OSV) | at startup and every 6 hours |
| Scheduled backup | daily at the configured time (checked every minute) |
| Retry pending uploads | every 10 minutes while some are pending |
| Audit log pruning, expired sessions and links, API tokens expired 30+ days ago; email to owners of API tokens that expire within 7 days (once per token) | at startup and every 24 hours |
| Log rotation and cleanup | at day change or 50 MB |
| Cleanup of temp files left by a crash during backup/restore | at startup |
| Summaries: recompute upgrades, collect notes; in automatic mode write missing ones | after every upstream refresh, on settings changes, and hourly |
| Notification check (Critical, unrecognized images, new clusters) | after agent reports with changes, upstream refreshes and admin changes |
| Weekly digest | at the configured time (checked every minute; failed sends retried hourly for 24 h) |
